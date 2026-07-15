package sandbox_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/azizu06/rehearse/internal/drill"
	"github.com/azizu06/rehearse/internal/journal"
	"github.com/azizu06/rehearse/internal/sandbox"
)

func TestJanitorRetriesFailedCleanupUntilItSucceeds(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rehearse.db")
	startedAt := time.Date(2026, time.July, 15, 16, 0, 0, 0, time.UTC)
	store, err := journal.Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	plan := drill.Plan{
		ID: "plan-1", Name: "janitor plan", Version: 1, CreatedAt: startedAt,
		Spec: drill.PlanSpec{SourceKind: "source", TargetKind: "target"},
	}
	if err := store.CreatePlan(ctx, plan); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	if _, err := store.CreateRun(ctx, "run-janitor", plan.ID, plan.Version, startedAt); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	store, err = journal.Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cleaner := &sequencedCleaner{errors: []error{errors.New("Docker unavailable"), nil}}
	clock := &sequenceClock{next: startedAt.Add(time.Hour)}
	janitor := sandbox.NewJanitor(store, cleaner, sandbox.JanitorOptions{
		CleanupTimeout: time.Second,
		Now:            clock.Now,
	})
	if err := janitor.Reconcile(ctx); err == nil {
		t.Fatal("first reconciliation succeeded despite cleanup failure")
	}
	failed, err := store.Run(ctx, "run-janitor")
	if err != nil {
		t.Fatalf("Run after failure: %v", err)
	}
	if failed.Outcome != drill.OutcomeFailed || failed.Cleanup != drill.CleanupFailed || !failed.NeedsReconciliation {
		t.Fatalf("failed reconciliation projection = %#v", failed)
	}

	if err := janitor.Reconcile(ctx); err != nil {
		t.Fatalf("second reconciliation: %v", err)
	}
	completed, err := store.Run(ctx, "run-janitor")
	if err != nil {
		t.Fatalf("Run after success: %v", err)
	}
	if completed.Cleanup != drill.CleanupSucceeded || completed.NeedsReconciliation {
		t.Fatalf("completed reconciliation projection = %#v", completed)
	}
	events, err := store.Events(ctx, "run-janitor")
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	wantTail := []drill.EventKind{
		drill.EventRunFailed,
		drill.EventCleanupFailed,
		drill.EventReconciliationRequired,
		drill.EventCleanupSucceeded,
	}
	if len(events) < len(wantTail) {
		t.Fatalf("events = %#v", events)
	}
	tail := events[len(events)-len(wantTail):]
	for index, want := range wantTail {
		if tail[index].Kind != want {
			t.Fatalf("event %d = %q, want %q", index, tail[index].Kind, want)
		}
	}
}

type sequencedCleaner struct {
	errors []error
}

func (cleaner *sequencedCleaner) Cleanup(context.Context, string) error {
	if len(cleaner.errors) == 0 {
		return nil
	}
	err := cleaner.errors[0]
	cleaner.errors = cleaner.errors[1:]
	return err
}

type sequenceClock struct {
	next time.Time
}

func (clock *sequenceClock) Now() time.Time {
	result := clock.next
	clock.next = clock.next.Add(time.Second)
	return result
}
