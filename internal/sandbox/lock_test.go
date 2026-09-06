package sandbox

import (
	"errors"
	"testing"
)

func TestProjectLockFailsClosedAcrossConcurrentHolders(t *testing.T) {
	identity, _ := newIdentity("run-lock")
	locker := newProjectLocker(t.TempDir())
	first, err := locker.lock(identity)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	defer func() { _ = first.release() }()
	if _, err := locker.lock(identity); !errors.Is(err, ErrProjectBusy) {
		t.Fatalf("second lock error = %v, want ErrProjectBusy", err)
	}
	if err := first.release(); err != nil {
		t.Fatalf("release first: %v", err)
	}
	third, err := locker.lock(identity)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	if err := third.release(); err != nil {
		t.Fatalf("release third: %v", err)
	}
}
