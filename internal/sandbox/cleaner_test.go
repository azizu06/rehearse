package sandbox

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCleanerRequiresSeparatedEmptyScansAndFindsDelayedResource(t *testing.T) {
	identity, _ := newIdentity("run-1")
	command := &scriptedDocker{lists: map[string][][]byte{
		"container": {nil, nil, nil, nil},
		"network":   {nil, nil, nil, nil},
		"volume":    {nil, []byte("delayed-volume\n"), nil, nil},
	}}
	waiter := &recordingWaiter{}
	cleaner := cleaner{command: command, waiter: waiter, quiescence: 500 * time.Millisecond}
	if err := cleaner.cleanup(context.Background(), identity); err != nil {
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
		if strings.Contains(joined, " ls ") && (!strings.Contains(joined, "label="+managedLabel+"=true") || !strings.Contains(joined, "label="+runFingerprintLabel+"="+identity.fingerprint)) {
			t.Fatalf("unscoped list call: %v", call)
		}
	}
}

func TestCleanerReturnsFailureWhenQuiescenceContextExpires(t *testing.T) {
	identity, _ := newIdentity("run-1")
	waiter := &recordingWaiter{err: context.DeadlineExceeded}
	cleaner := cleaner{command: &scriptedDocker{}, waiter: waiter, quiescence: 500 * time.Millisecond}
	if err := cleaner.cleanup(context.Background(), identity); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cleanup error = %v, want context deadline", err)
	}
}

func TestCleanerIsIdempotentWhenNoResourcesExist(t *testing.T) {
	identity, _ := newIdentity("run-idempotent")
	command := &scriptedDocker{}
	cleaner := cleaner{command: command, waiter: &recordingWaiter{}, quiescence: 500 * time.Millisecond}
	if err := cleaner.cleanup(context.Background(), identity); err != nil {
		t.Fatalf("first cleanup: %v", err)
	}
	if err := cleaner.cleanup(context.Background(), identity); err != nil {
		t.Fatalf("second cleanup: %v", err)
	}
}

type scriptedDocker struct {
	mu    sync.Mutex
	lists map[string][][]byte
	calls [][]string
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

func (waiter *recordingWaiter) wait(_ context.Context, duration time.Duration) error {
	waiter.durations = append(waiter.durations, duration)
	return waiter.err
}
