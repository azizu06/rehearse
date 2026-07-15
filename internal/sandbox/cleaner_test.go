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
)

func TestCleanerRequiresSeparatedEmptyScansAndFindsDelayedResource(t *testing.T) {
	identity := claimedTestIdentity(t, "run-1")
	command := &scriptedDocker{lists: map[string][][]byte{
		"container": {nil, nil, nil, nil},
		"network":   {nil, nil, nil, nil},
		"volume":    {nil, []byte("delayed-volume\n"), nil, nil},
	}, identity: identity}
	waiter := &recordingWaiter{}
	cleaner := cleaner{command: command, waiter: waiter, quiescence: 500 * time.Millisecond}
	if err := cleaner.cleanupClaim(context.Background(), identity); err != nil {
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
		if strings.Contains(joined, " ls ") && (!strings.Contains(joined, "label="+managedLabel+"=true") || !strings.Contains(joined, "label="+runFingerprintLabel+"="+identity.fingerprint) || !strings.Contains(joined, "label="+runIDLabel+"="+identity.runID)) {
			t.Fatalf("unscoped list call: %v", call)
		}
	}
}

func TestCleanerReturnsFailureWhenQuiescenceContextExpires(t *testing.T) {
	identity := claimedTestIdentity(t, "run-1")
	waiter := &recordingWaiter{err: context.DeadlineExceeded}
	cleaner := cleaner{command: &scriptedDocker{}, waiter: waiter, quiescence: 500 * time.Millisecond}
	if err := cleaner.cleanupClaim(context.Background(), identity); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cleanup error = %v, want context deadline", err)
	}
}

func TestCleanerIsIdempotentWhenNoResourcesExist(t *testing.T) {
	identity := claimedTestIdentity(t, "run-idempotent")
	command := &scriptedDocker{}
	cleaner := cleaner{command: command, waiter: &recordingWaiter{}, quiescence: 500 * time.Millisecond}
	if err := cleaner.cleanupClaim(context.Background(), identity); err != nil {
		t.Fatalf("first cleanup: %v", err)
	}
	if err := cleaner.cleanupClaim(context.Background(), identity); err != nil {
		t.Fatalf("second cleanup: %v", err)
	}
}

func TestCleanerDoesNotCascadeContainerRemovalIntoUnlabeledVolumes(t *testing.T) {
	identity := claimedTestIdentity(t, "run-container-volume-scope")
	command := &scriptedDocker{lists: map[string][][]byte{
		"container": {[]byte("owned-container\n"), nil, nil},
		"network":   {nil, nil, nil},
		"volume":    {nil, nil, nil},
	}, identity: identity}
	cleaner := cleaner{command: command, waiter: &recordingWaiter{}, quiescence: 500 * time.Millisecond}
	if err := cleaner.cleanupClaim(context.Background(), identity); err != nil {
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

func TestLiveCleanupPreservesDaemonIdentityReplacement(t *testing.T) {
	identity := claimedTestIdentity(t, "run-daemon-replacement")
	name := identity.projectName + "_work"
	command := &runnerDocker{resources: map[string]inspectedResource{
		"volume\x00" + name: {DaemonID: "replacement-created-at", Name: name, Labels: identity.labels()},
	}}
	ledger := newCreatedResourceLedger()
	ledger.add(createdResource{kind: "volume", name: name, daemonID: "original-created-at"})
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
		{kind: "volume", name: "work", daemonID: "volume-id"},
		{kind: "network", name: "default", daemonID: "network-id"},
		{kind: "container", name: "worker", daemonID: "container-id"},
	} {
		ledger.add(resource)
		command.resources[resource.kind+"\x00"+resource.name] = inspectedResource{
			DaemonID: resource.daemonID, Name: resource.name, Labels: identity.labels(),
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
	command := &runnerDocker{resources: map[string]inspectedResource{
		"volume\x00active": {DaemonID: "active-created-at", Name: "active", Labels: active.labels()},
		"volume\x00stable": {DaemonID: "stable-created-at", Name: "stable", Labels: stable.labels()},
		"volume\x00stale":  {DaemonID: "stale-created-at", Name: "stale", Labels: stale.labels()},
	}}
	cleaner := cleaner{command: command, waiter: &recordingWaiter{}, quiescence: 500 * time.Millisecond}
	if err := cleaner.cleanupClaim(context.Background(), active); err != nil {
		t.Fatalf("cleanupClaim: %v", err)
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
	mu       sync.Mutex
	lists    map[string][][]byte
	calls    [][]string
	identity identity
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
		return json.Marshal(inspectedResource{DaemonID: daemonID, Name: name, Labels: docker.identity.labels()})
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
