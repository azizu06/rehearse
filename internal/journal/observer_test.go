package journal_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
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
	// slowOddEvents stalls odd-sequence deliveries before recording them so a
	// later commit could overtake an earlier one if delivery were unordered.
	slowOddEvents bool
	mu            sync.Mutex
	changes       []observedChange
}

func (observer *recordingObserver) ObserveRunChange(before, after drill.Run, event drill.Event) {
	if observer.slowOddEvents && event.Sequence%2 == 1 {
		time.Sleep(time.Millisecond)
	}
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

func TestRunObserverReceivesConcurrentChangesInCommitOrder(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "rehearse.db")
	observer := &recordingObserver{slowOddEvents: true}
	store, err := journal.Open(ctx, path, journal.WithRunObserver(observer))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	plan := testPlan(now)
	if err := store.CreatePlan(ctx, plan); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}

	var workers sync.WaitGroup
	errs := make(chan error, 16)
	for worker := range 16 {
		workers.Go(func() {
			runID := fmt.Sprintf("run-%02d", worker)
			if _, err := store.CreateRun(ctx, runID, plan.ID, plan.Version, now); err != nil {
				errs <- err
				return
			}
			for step, stage := range []drill.Stage{drill.StagePreflight, drill.StageAcquire, drill.StageRestore} {
				if _, err := store.Transition(ctx, runID, stage, now.Add(time.Duration(step+1)*time.Second)); err != nil {
					errs <- err
					return
				}
			}
		})
	}
	workers.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent mutation: %v", err)
	}

	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open commit-order evidence connection: %v", err)
	}
	defer func() { _ = database.Close() }()
	rows, err := database.QueryContext(ctx, `SELECT run_id, sequence FROM run_events ORDER BY id`)
	if err != nil {
		t.Fatalf("query commit order: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var committed []string
	for rows.Next() {
		var runID string
		var sequence int64
		if err := rows.Scan(&runID, &sequence); err != nil {
			t.Fatalf("scan commit order: %v", err)
		}
		committed = append(committed, fmt.Sprintf("%s/%d", runID, sequence))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate commit order: %v", err)
	}

	observer.mu.Lock()
	defer observer.mu.Unlock()
	var delivered []string
	for _, change := range observer.changes {
		delivered = append(delivered, fmt.Sprintf("%s/%d", change.after.ID, change.event.Sequence))
	}
	if !slices.Equal(delivered, committed) {
		t.Fatalf("delivery order differs from commit order:\ndelivered %v\ncommitted %v", delivered, committed)
	}
}
