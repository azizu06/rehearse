package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
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
func (cleaner *Cleaner) Cleanup(ctx context.Context, runID string) error {
	identity, err := newIdentity(runID)
	if err != nil {
		return err
	}
	lock, err := cleaner.locker.lock(identity)
	if err != nil {
		return err
	}
	defer func() { _ = lock.release() }()
	return cleaner.cleaner.cleanup(ctx, identity)
}

func (cleaner cleaner) cleanup(ctx context.Context, identity identity) error {
	// Give an in-flight daemon request from a killed Compose client one bounded
	// interval to materialize its labels before the first ownership scan.
	if err := cleaner.waiter.wait(ctx, cleaner.quiescence); err != nil {
		return fmt.Errorf("wait for Docker cleanup settle interval: %w", err)
	}
	emptyObserved := false
	for {
		empty, err := cleaner.removePass(ctx, identity)
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

func (cleaner cleaner) removePass(ctx context.Context, identity identity) (bool, error) {
	resources := []struct {
		name       string
		listArgs   []string
		removeArgs []string
	}{
		{name: "container", listArgs: []string{"container", "ls", "--all", "--quiet"}, removeArgs: []string{"container", "rm", "--force", "--volumes"}},
		{name: "network", listArgs: []string{"network", "ls", "--quiet"}, removeArgs: []string{"network", "rm", "--force"}},
		{name: "volume", listArgs: []string{"volume", "ls", "--quiet"}, removeArgs: []string{"volume", "rm", "--force"}},
	}
	empty := true
	for _, resource := range resources {
		ids, err := cleaner.list(ctx, resource.listArgs, identity)
		if err != nil {
			return false, fmt.Errorf("list Rehearse %s resources: %w", resource.name, err)
		}
		if len(ids) == 0 {
			continue
		}
		empty = false
		args := append(append([]string{}, resource.removeArgs...), ids...)
		if _, err := cleaner.command.run(ctx, cleanupOutputLimit, args...); err != nil {
			remaining, listErr := cleaner.list(ctx, resource.listArgs, identity)
			if listErr != nil {
				return false, errors.Join(err, listErr)
			}
			if len(remaining) != 0 {
				return false, fmt.Errorf("remove Rehearse %s resources: %w", resource.name, err)
			}
		}
	}
	return empty, nil
}

func (cleaner cleaner) list(ctx context.Context, args []string, identity identity) ([]string, error) {
	filtered := append(append([]string{}, args...), ownershipFilters(identity, true)...)
	output, err := cleaner.command.run(ctx, cleanupOutputLimit, filtered...)
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(output)), nil
}
