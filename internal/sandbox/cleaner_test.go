package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azizu06/rehearse/internal/drill"
)

const testResourceGeneration = "2222222222222222222222222222222222222222222222222222222222222222"

func TestCleanerRequiresSeparatedEmptyScansAndFindsDelayedResource(t *testing.T) {
	identity := claimedTestIdentity(t, "run-1")
	command := &scriptedDocker{lists: map[string][][]byte{
		"container": {nil, nil, nil, nil},
		"network":   {nil, nil, nil, nil},
		"volume":    {nil, []byte("delayed-volume\n"), nil, nil},
	}, identity: identity, generation: testResourceGeneration}
	waiter := &recordingWaiter{}
	cleaner := cleaner{command: command, waiter: waiter, quiescence: 500 * time.Millisecond}
	manifest := []drill.SandboxResourceClaim{{Kind: "volume", Name: "delayed-volume", Generation: testResourceGeneration}}
	if err := cleaner.cleanupManifest(context.Background(), identity, manifest); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if got, want := waiter.durations, []time.Duration{500 * time.Millisecond, 500 * time.Millisecond, 500 * time.Millisecond}; !reflect.DeepEqual(got, want) {
		t.Fatalf("wait durations = %v, want %v", got, want)
	}
	if !command.calledWith("volume", "rm", "--force", "delayed-volume") {
		t.Fatal("cleaner did not remove the delayed resource")
	}
	for _, call := range command.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, " ls ") && !strings.Contains(joined, "name=^") && (!strings.Contains(joined, "label="+managedLabel+"=true") || !strings.Contains(joined, "label="+runFingerprintLabel+"="+identity.fingerprint) || !strings.Contains(joined, "label="+runIDLabel+"="+identity.runID)) {
			t.Fatalf("unscoped list call: %v", call)
		}
	}
}

func TestCleanerReturnsFailureWhenQuiescenceContextExpires(t *testing.T) {
	identity := claimedTestIdentity(t, "run-1")
	waiter := &recordingWaiter{err: context.DeadlineExceeded}
	cleaner := cleaner{command: &scriptedDocker{}, waiter: waiter, quiescence: 500 * time.Millisecond}
	if err := cleaner.cleanupManifest(context.Background(), identity, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cleanup error = %v, want context deadline", err)
	}
}

func TestCleanerIsIdempotentWhenNoResourcesExist(t *testing.T) {
	identity := claimedTestIdentity(t, "run-idempotent")
	command := &scriptedDocker{}
	cleaner := cleaner{command: command, waiter: &recordingWaiter{}, quiescence: 500 * time.Millisecond}
	if err := cleaner.cleanupManifest(context.Background(), identity, nil); err != nil {
		t.Fatalf("first cleanup: %v", err)
	}
	if err := cleaner.cleanupManifest(context.Background(), identity, nil); err != nil {
		t.Fatalf("second cleanup: %v", err)
	}
}

func TestCleanerRejectsForeignManifestNameBeforeDocker(t *testing.T) {
	identity := claimedTestIdentity(t, "run-cleaner-manifest-validation")
	foreign := claimedTestIdentity(t, "another-cleaner-run")
	command := &scriptedDocker{}
	cleaner := &Cleaner{cleaner: cleaner{command: command}}
	manifest := []drill.SandboxResourceClaim{{Kind: "volume", Name: foreign.projectName + "_work", Generation: testResourceGeneration}}
	if err := cleaner.Cleanup(context.Background(), identity.runID, identity.claimID, manifest); !errors.Is(err, ErrCorruptSandboxClaim) {
		t.Fatalf("Cleanup error = %v, want ErrCorruptSandboxClaim", err)
	}
	if len(command.calls) != 0 {
		t.Fatalf("Cleaner executed Docker for corrupt manifest: %#v", command.calls)
	}
}

func TestStartupCleanupTreatsPreCreateManifestEntryAsAbsent(t *testing.T) {
	identity := claimedTestIdentity(t, "run-pre-create-crash")
	command := &runnerDocker{resources: make(map[string]inspectedResource)}
	cleaner := cleaner{command: command, waiter: &recordingWaiter{}, quiescence: 500 * time.Millisecond}
	manifest := []drill.SandboxResourceClaim{{Kind: "volume", Name: identity.projectName + "_work", Generation: testResourceGeneration}}
	if err := cleaner.cleanupManifest(context.Background(), identity, manifest); err != nil {
		t.Fatalf("cleanup pre-create manifest: %v", err)
	}
	if len(command.removed) != 0 {
		t.Fatalf("pre-create crash cleanup removed resources: %#v", command.removed)
	}
}

func TestCleanerDoesNotCascadeContainerRemovalIntoUnlabeledVolumes(t *testing.T) {
	identity := claimedTestIdentity(t, "run-container-volume-scope")
	command := &scriptedDocker{lists: map[string][][]byte{
		"container": {[]byte("owned-container\n"), nil, nil},
		"network":   {nil, nil, nil},
		"volume":    {nil, nil, nil},
	}, identity: identity, generation: testResourceGeneration}
	cleaner := cleaner{command: command, waiter: &recordingWaiter{}, quiescence: 500 * time.Millisecond}
	manifest := []drill.SandboxResourceClaim{{Kind: "container", Name: "owned-container", Generation: testResourceGeneration}}
	if err := cleaner.cleanupManifest(context.Background(), identity, manifest); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if !command.calledWith("container", "rm", "--force", "owned-container") {
		t.Fatal("cleaner did not remove the explicitly enumerated container")
	}
	for _, call := range command.calls {
		if containsSequence(call, "container", "rm") && containsSequence(call, "--volumes") {
			t.Fatalf("container removal cascaded into attached volumes: %v", call)
		}
		if containsSequence(call, "volume", "rm") {
			t.Fatalf("cleaner removed a volume absent from the ownership-filtered list: %v", call)
		}
	}
}

func TestLiveCleanupPreservesSameTimestampVolumeGenerationReplacement(t *testing.T) {
	identity := claimedTestIdentity(t, "run-daemon-replacement")
	name := identity.projectName + "_work"
	replacementLabels, _ := resourceLabels(identity, strings.Repeat("3", 64))
	command := &runnerDocker{resources: map[string]inspectedResource{
		"volume\x00" + name: {DaemonID: "same-created-at", Name: name, Labels: replacementLabels},
	}}
	ledger := newCreatedResourceLedger()
	ledger.add(createdResource{kind: "volume", name: name, daemonID: "same-created-at", generation: testResourceGeneration})
	cleaner := cleaner{command: command, waiter: &recordingWaiter{}, quiescence: 500 * time.Millisecond}
	if err := cleaner.cleanupCreated(context.Background(), identity, ledger); err != nil {
		t.Fatalf("cleanupCreated: %v", err)
	}
	if len(command.removed) != 0 {
		t.Fatalf("live cleanup removed replacement: %#v", command.removed)
	}
	if _, exists := command.resources["volume\x00"+name]; !exists {
		t.Fatal("daemon identity replacement did not survive")
	}
}

func TestLiveCleanupRemovesDependenciesInDockerOrder(t *testing.T) {
	identity := claimedTestIdentity(t, "run-cleanup-order")
	command := &runnerDocker{resources: make(map[string]inspectedResource)}
	ledger := newCreatedResourceLedger()
	for _, resource := range []createdResource{
		{kind: "volume", name: "work", generation: testResourceGeneration},
		{kind: "network", name: "default", daemonID: "network-id", generation: testResourceGeneration},
		{kind: "container", name: "worker", daemonID: "container-id", generation: testResourceGeneration},
	} {
		ledger.add(resource)
		labels, _ := resourceLabels(identity, resource.generation)
		command.resources[resource.kind+"\x00"+resource.name] = inspectedResource{
			DaemonID: resource.daemonID, Name: resource.name, Labels: labels,
		}
	}
	cleaner := cleaner{command: command, waiter: &recordingWaiter{}, quiescence: 500 * time.Millisecond}
	if err := cleaner.cleanupCreated(context.Background(), identity, ledger); err != nil {
		t.Fatalf("cleanupCreated: %v", err)
	}
	var removedKinds []string
	for _, resource := range command.removed {
		removedKinds = append(removedKinds, resource.kind)
	}
	if want := []string{"container", "network", "volume"}; !reflect.DeepEqual(removedKinds, want) {
		t.Fatalf("cleanup order = %v, want %v", removedKinds, want)
	}
}

func TestStartupCleanupPreservesStableAndStaleClaimResources(t *testing.T) {
	active := claimedTestIdentity(t, "run-startup-claim-scope")
	stable, _ := newIdentity(active.runID)
	stale, err := stable.withClaimID(strings.Repeat("1", 64))
	if err != nil {
		t.Fatalf("withClaimID: %v", err)
	}
	activeLabels, _ := resourceLabels(active, testResourceGeneration)
	command := &runnerDocker{resources: map[string]inspectedResource{
		"volume\x00active": {Name: "active", Labels: activeLabels},
		"volume\x00stable": {Name: "stable", Labels: stable.labels()},
		"volume\x00stale":  {Name: "stale", Labels: stale.labels()},
	}}
	cleaner := cleaner{command: command, waiter: &recordingWaiter{}, quiescence: 500 * time.Millisecond}
	manifest := []drill.SandboxResourceClaim{{Kind: "volume", Name: "active", Generation: testResourceGeneration}}
	if err := cleaner.cleanupManifest(context.Background(), active, manifest); err != nil {
		t.Fatalf("cleanupManifest: %v", err)
	}
	if _, exists := command.resources["volume\x00active"]; exists {
		t.Fatal("startup cleanup left the active-claim resource")
	}
	for _, name := range []string{"stable", "stale"} {
		if _, exists := command.resources["volume\x00"+name]; !exists {
			t.Fatalf("startup cleanup deleted %s resource", name)
		}
	}
}

type scriptedDocker struct {
	mu         sync.Mutex
	lists      map[string][][]byte
	calls      [][]string
	identity   identity
	generation string
}

func (docker *scriptedDocker) run(_ context.Context, _ int64, args ...string) ([]byte, error) {
	docker.mu.Lock()
	defer docker.mu.Unlock()
	docker.calls = append(docker.calls, append([]string(nil), args...))
	if len(args) >= 2 && args[1] == "ls" {
		queue := docker.lists[args[0]]
		if len(queue) > 0 {
			docker.lists[args[0]] = queue[1:]
			return queue[0], nil
		}
	}
	if len(args) >= 2 && args[1] == "inspect" {
		name := args[len(args)-1]
		daemonID := name
		if args[0] == "volume" {
			daemonID = "volume-created-at"
		}
		labels, _ := resourceLabels(docker.identity, docker.generation)
		return json.Marshal(inspectedResource{DaemonID: daemonID, Name: name, Labels: labels})
	}
	return nil, nil
}

func (docker *scriptedDocker) calledWith(want ...string) bool {
	for _, call := range docker.calls {
		if reflect.DeepEqual(call, want) {
			return true
		}
	}
	return false
}

type recordingWaiter struct {
	durations []time.Duration
	err       error
}

func claimedTestIdentity(t *testing.T, runID string) identity {
	t.Helper()
	value, err := newIdentity(runID)
	if err != nil {
		t.Fatalf("newIdentity: %v", err)
	}
	value, err = value.withClaimID(testClaimID)
	if err != nil {
		t.Fatalf("withClaimID: %v", err)
	}
	return value
}

func (waiter *recordingWaiter) wait(_ context.Context, duration time.Duration) error {
	waiter.durations = append(waiter.durations, duration)
	return waiter.err
}
