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

// orderObserver records each delivery. Odd-sequence deliveries stall first so
// a later commit would overtake an earlier one if delivery were unordered.
type orderObserver struct {
	mu        sync.Mutex
	delivered []string
	previous  map[string]drill.Run
	err       error
}

func (observer *orderObserver) ObserveRunChange(before, after drill.Run, event drill.Event) {
	if event.Sequence%2 == 1 {
		time.Sleep(time.Millisecond)
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if before != observer.previous[after.ID] && observer.err == nil {
		observer.err = fmt.Errorf("%s/%d before = %#v, want prior projection", after.ID, event.Sequence, before)
	}
	observer.previous[after.ID] = after
	observer.delivered = append(observer.delivered, fmt.Sprintf("%s/%d/%s", after.ID, event.Sequence, event.Kind))
}

func TestRunObserverReceivesOnlyCommittedChangesInCommitOrder(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "rehearse.db")
	observer := &orderObserver{previous: map[string]drill.Run{}}
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
	errs := make(chan error, 8)
	for worker := range 8 {
		workers.Go(func() {
			runID := fmt.Sprintf("run-%d", worker)
			at := func(seconds int) time.Time { return now.Add(time.Duration(seconds) * time.Second) }
			if _, err := store.CreateRun(ctx, runID, plan.ID, plan.Version, now); err != nil {
				errs <- err
				return
			}
			if _, err := store.Transition(ctx, runID, drill.StageBoot, at(1)); !errors.Is(err, drill.ErrInvalidTransition) {
				errs <- fmt.Errorf("skipping transition error = %v, want ErrInvalidTransition", err)
				return
			}
			for _, mutate := range []func() (drill.Run, error){
				func() (drill.Run, error) { return store.Transition(ctx, runID, drill.StagePreflight, at(2)) },
				func() (drill.Run, error) { return store.RecordOutcome(ctx, runID, drill.OutcomeFailed, at(3)) },
				func() (drill.Run, error) { return store.RecordCleanup(ctx, runID, drill.CleanupFailed, at(4)) },
				func() (drill.Run, error) { return store.BeginCleanupRetry(ctx, runID, at(5)) },
			} {
				if _, err := mutate(); err != nil {
					errs <- err
					return
				}
			}
		})
	}
	workers.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("mutation: %v", err)
	}

	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open commit-order evidence connection: %v", err)
	}
	defer func() { _ = database.Close() }()
	rows, err := database.QueryContext(ctx, `SELECT run_id, sequence, kind FROM run_events ORDER BY id`)
	if err != nil {
		t.Fatalf("query commit order: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var committed []string
	for rows.Next() {
		var runID, kind string
		var sequence int64
		if err := rows.Scan(&runID, &sequence, &kind); err != nil {
			t.Fatalf("scan commit order: %v", err)
		}
		committed = append(committed, fmt.Sprintf("%s/%d/%s", runID, sequence, kind))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate commit order: %v", err)
	}

	observer.mu.Lock()
	defer observer.mu.Unlock()
	if observer.err != nil {
		t.Fatal(observer.err)
	}
	if !slices.Equal(observer.delivered, committed) {
		t.Fatalf("deliveries differ from committed events:\ndelivered %v\ncommitted %v", observer.delivered, committed)
	}
	for runID, last := range observer.previous {
		persisted, err := store.Run(ctx, runID)
		if err != nil || persisted != last {
			t.Fatalf("last delivered %s = %#v, want persisted %#v (err %v)", runID, last, persisted, err)
		}
	}
}
