package journal_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/azizu06/rehearse/internal/drill"
	"github.com/azizu06/rehearse/internal/journal"
)

type observedChange struct {
	before drill.Run
	after  drill.Run
	event  drill.Event
}

type recordingObserver struct {
	mu      sync.Mutex
	changes []observedChange
}

func (observer *recordingObserver) ObserveRunChange(before, after drill.Run, event drill.Event) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.changes = append(observer.changes, observedChange{before: before, after: after, event: event})
}

func TestRunObserverSeesOnlyCommittedChanges(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)
	observer := &recordingObserver{}
	store, err := journal.Open(ctx, filepath.Join(t.TempDir(), "rehearse.db"), journal.WithRunObserver(observer))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	plan := testPlan(now)
	if err := store.CreatePlan(ctx, plan); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}

	run, err := store.CreateRun(ctx, "run-1", plan.ID, plan.Version, now)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := store.Transition(ctx, run.ID, drill.StageBoot, now.Add(time.Second)); !errors.Is(err, drill.ErrInvalidTransition) {
		t.Fatalf("skipping transition error = %v, want ErrInvalidTransition", err)
	}
	if _, err := store.Transition(ctx, run.ID, drill.StagePreflight, now.Add(2*time.Second)); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if _, err := store.RecordOutcome(ctx, run.ID, drill.OutcomeFailed, now.Add(3*time.Second)); err != nil {
		t.Fatalf("RecordOutcome: %v", err)
	}
	if _, err := store.RecordCleanup(ctx, run.ID, drill.CleanupFailed, now.Add(4*time.Second)); err != nil {
		t.Fatalf("RecordCleanup: %v", err)
	}
	if _, err := store.BeginCleanupRetry(ctx, run.ID, now.Add(5*time.Second)); err != nil {
		t.Fatalf("BeginCleanupRetry: %v", err)
	}

	events, err := store.Events(ctx, run.ID)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if len(observer.changes) != len(events) {
		t.Fatalf("observed %d changes, journal committed %d events", len(observer.changes), len(events))
	}
	var previous drill.Run
	for index, change := range observer.changes {
		if change.event != events[index] {
			t.Fatalf("change %d event = %#v, want committed %#v", index, change.event, events[index])
		}
		if change.before != previous {
			t.Fatalf("change %d before = %#v, want prior projection %#v", index, change.before, previous)
		}
		previous = change.after
	}
	persisted, err := store.Run(ctx, run.ID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if previous != persisted {
		t.Fatalf("last observed projection = %#v, want persisted %#v", previous, persisted)
	}
}
