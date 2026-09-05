package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/azizu06/rehearse/internal/drill"
	"github.com/azizu06/rehearse/internal/sandboxid"
)

const (
	cleanupOutputLimit = 4 << 20
	// cleanupQuiescence separates the two empty scans so daemon work that
	// outlives a cancelled Compose client cannot produce a false clean result.
	cleanupQuiescence = 500 * time.Millisecond
)

type contextWaiter interface {
	wait(context.Context, time.Duration) error
}

type timerWaiter struct{}

func (timerWaiter) wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-timer.C:
		return nil
	}
}

// Cleaner removes only resources carrying all Rehearse ownership labels for
// one exact durable run.
type Cleaner struct {
	cleaner cleaner
	locker  projectLocker
}

type cleaner struct {
	command        dockerCommand
	waiter         contextWaiter
	quiescence     time.Duration
	snapshotRoot   string
	removeSnapshot func(string, identity) error
}

// NewCleaner constructs a label-scoped startup reconciliation cleaner.
func NewCleaner(options CleanerOptions) *Cleaner {
	temporaryRoot := options.TemporaryRoot
	if temporaryRoot == "" {
		temporaryRoot = os.TempDir()
	}
	return &Cleaner{
		cleaner: cleaner{command: newCommandExecutor(options.Binary), waiter: timerWaiter{}, quiescence: cleanupQuiescence, snapshotRoot: filepath.Clean(temporaryRoot), removeSnapshot: removeSnapshotDirectory},
		locker:  newProjectLocker(options.LockRoot),
	}
}

// Cleanup is idempotent and safe to repeat after any partial cleanup.
func (cleaner *Cleaner) Cleanup(ctx context.Context, runID, claimID string, resources []drill.SandboxResourceClaim) error {
	identity, err := newIdentity(runID)
	if err != nil {
		return err
	}
	for _, resource := range resources {
		if sandboxid.ValidateResourceName(runID, resource.Kind, resource.Name) != nil {
			return ErrCorruptSandboxClaim
		}
		if _, err := resourceLabels(identity, resource.Generation); err != nil {
			return err
		}
	}
	identity, err = identity.withClaimID(claimID)
	if err != nil {
		return err
	}
	lock, err := cleaner.locker.lock(identity)
	if err != nil {
		return err
	}
	defer func() { _ = lock.release() }()
	return cleaner.cleaner.cleanupManifest(ctx, identity, resources)
}

func (cleaner cleaner) cleanupManifest(ctx context.Context, identity identity, resources []drill.SandboxResourceClaim) error {
	return cleaner.cleanupUntilQuiet(ctx, identity, func(ctx context.Context) (bool, error) {
		return cleaner.removeManifestPass(ctx, identity, resources)
	})
}

func (cleaner cleaner) cleanupCreated(ctx context.Context, identity identity, ledger *createdResourceLedger) error {
	return cleaner.cleanupUntilQuiet(ctx, identity, func(ctx context.Context) (bool, error) {
		return cleaner.removeLedgerPass(ctx, identity, ledger)
	})
}

func (cleaner cleaner) cleanupUntilQuiet(ctx context.Context, identity identity, removePass func(context.Context) (bool, error)) error {
	// Give an in-flight daemon request from a killed Compose client one bounded
	// interval to materialize its labels before the first ownership scan.
	if err := cleaner.waiter.wait(ctx, cleaner.quiescence); err != nil {
		return fmt.Errorf("wait for Docker cleanup settle interval: %w", err)
	}
	emptyObserved := false
	for {
		empty, err := removePass(ctx)
		if err != nil {
			return err
		}
		if !empty {
			emptyObserved = false
			continue
		}
		if emptyObserved {
			removeSnapshot := cleaner.removeSnapshot
			if removeSnapshot == nil {
				removeSnapshot = removeSnapshotDirectory
			}
			return removeSnapshot(cleaner.snapshotRoot, identity)
		}
		emptyObserved = true
		if err := cleaner.waiter.wait(ctx, cleaner.quiescence); err != nil {
			return fmt.Errorf("wait for Docker cleanup quiescence: %w", err)
		}
	}
}

func cleanupResourceKinds() []struct {
	name     string
	listArgs []string
} {
	return []struct {
		name     string
		listArgs []string
	}{
		{name: "container", listArgs: []string{"container", "ls", "--all", "--quiet"}},
		{name: "network", listArgs: []string{"network", "ls", "--quiet"}},
		{name: "volume", listArgs: []string{"volume", "ls", "--quiet"}},
	}
}

func (cleaner cleaner) removeManifestPass(ctx context.Context, identity identity, resources []drill.SandboxResourceClaim) (bool, error) {
	empty := true
	ordered := append([]drill.SandboxResourceClaim(nil), resources...)
	order := map[string]int{"container": 0, "network": 1, "volume": 2}
	sort.Slice(ordered, func(left, right int) bool {
		if order[ordered[left].Kind] != order[ordered[right].Kind] {
			return order[ordered[left].Kind] < order[ordered[right].Kind]
		}
		return ordered[left].Name < ordered[right].Name
	})
	for _, resource := range ordered {
		descriptor := resourceDescriptor(resource.Kind, resource.Name)
		ids, err := findExactResourceIDs(ctx, cleaner.command, descriptor)
		if err != nil {
			return false, fmt.Errorf("inspect expected Rehearse %s %q: %w", resource.Kind, resource.Name, err)
		}
		if len(ids) == 0 {
			continue
		}
		inspected, err := inspectResource(ctx, cleaner.command, descriptor)
		if err != nil {
			return false, fmt.Errorf("verify expected Rehearse %s %q: %w", resource.Kind, resource.Name, err)
		}
		labels, err := resourceLabels(identity, resource.Generation)
		if err != nil {
			return false, err
		}
		if !labelsContain(inspected.Labels, labels) {
			continue
		}
		empty = false
		removeTarget := inspected.DaemonID
		if resource.Kind == "volume" {
			removeTarget = inspected.Name
		}
		args := removeResourceArgs(resource.Kind, removeTarget)
		if _, err := cleaner.command.run(ctx, cleanupOutputLimit, args...); err != nil {
			remaining, inspectErr := inspectResource(ctx, cleaner.command, descriptor)
			if inspectErr == nil && labelsContain(remaining.Labels, labels) {
				return false, fmt.Errorf("remove expected Rehearse %s %q: %w", resource.Kind, resource.Name, err)
			}
		}
	}
	return empty, nil
}

func (cleaner cleaner) removeLedgerPass(ctx context.Context, identity identity, ledger *createdResourceLedger) (bool, error) {
	empty := true
	for _, created := range ledger.ordered() {
		descriptor := resourceDescriptor(created.kind, created.name)
		ids, err := findExactResourceIDs(ctx, cleaner.command, descriptor)
		if err != nil {
			return false, fmt.Errorf("inspect created %s %q: %w", created.kind, created.name, err)
		}
		if len(ids) == 0 {
			continue
		}
		inspected, err := inspectResource(ctx, cleaner.command, descriptor)
		if err != nil {
			return false, fmt.Errorf("verify created %s %q before cleanup: %w", created.kind, created.name, err)
		}
		labels, err := resourceLabels(identity, created.generation)
		if err != nil {
			return false, err
		}
		if !labelsContain(inspected.Labels, labels) || (created.kind != "volume" && inspected.DaemonID != created.daemonID) {
			continue
		}
		empty = false
		removeTarget := inspected.DaemonID
		if created.kind == "volume" {
			removeTarget = inspected.Name
		}
		removeArgs := removeResourceArgs(created.kind, removeTarget)
		if _, err := cleaner.command.run(ctx, cleanupOutputLimit, removeArgs...); err != nil {
			remaining, inspectErr := inspectResource(ctx, cleaner.command, descriptor)
			if inspectErr == nil && labelsContain(remaining.Labels, labels) &&
				(created.kind == "volume" || remaining.DaemonID == created.daemonID) {
				return false, fmt.Errorf("remove created %s %q: %w", created.kind, created.name, err)
			}
		}
	}

	for _, resource := range cleanupResourceKinds() {
		ids, err := cleaner.list(ctx, resource.listArgs, identity)
		if err != nil {
			return false, fmt.Errorf("list claimed Rehearse %s resources: %w", resource.name, err)
		}
		for _, id := range ids {
			descriptor := resourceDescriptor(resource.name, id)
			inspected, err := inspectResourceReference(ctx, cleaner.command, resource.name, id, descriptor.inspectFormat)
			if err != nil {
				return false, fmt.Errorf("inspect claimed Rehearse %s resource: %w", resource.name, err)
			}
			created, verified := ledger.get(resource.name, inspected.Name)
			if verified {
				labels, err := resourceLabels(identity, created.generation)
				if err != nil {
					return false, err
				}
				if !labelsContain(inspected.Labels, labels) ||
					(resource.name != "volume" && created.daemonID != inspected.DaemonID) {
					continue
				}
				empty = false
				continue
			}
			expected, exists := ledger.expectedResource(resource.name, inspected.Name)
			if !exists {
				continue
			}
			labels, err := resourceLabels(identity, expected.Generation)
			if err != nil {
				return false, err
			}
			if labelsContain(inspected.Labels, labels) {
				return false, fmt.Errorf("unverified Rehearse %s resource %q carries its expected generation", resource.name, inspected.Name)
			}
		}
	}
	return empty, nil
}

func removeResourceArgs(kind, target string) []string {
	if kind == "network" {
		return []string{kind, "rm", target}
	}
	return []string{kind, "rm", "--force", target}
}

func (cleaner cleaner) list(ctx context.Context, args []string, identity identity) ([]string, error) {
	filtered := append(append([]string{}, args...), ownershipFilters(identity, true)...)
	output, err := cleaner.command.run(ctx, cleanupOutputLimit, filtered...)
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(output)), nil
}
