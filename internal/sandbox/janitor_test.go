package sandbox_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
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
	if err := store.ClaimSandboxCleanup(ctx, "run-janitor", strings.Repeat("e", 64), startedAt.Add(time.Second)); err != nil {
		t.Fatalf("ClaimSandboxCleanup: %v", err)
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
	options := sandbox.JanitorOptions{
		CleanupTimeout: time.Second,
		Now:            clock.Now,
	}
	if err := sandbox.ReconcileStartup(ctx, store, cleaner, options); err == nil {
		t.Fatal("first reconciliation succeeded despite cleanup failure")
	}
	failed, err := store.Run(ctx, "run-janitor")
	if err != nil {
		t.Fatalf("Run after failure: %v", err)
	}
	if failed.Outcome != drill.OutcomeFailed || failed.Cleanup != drill.CleanupFailed || !failed.NeedsReconciliation {
		t.Fatalf("failed reconciliation projection = %#v", failed)
	}

	if err := sandbox.ReconcileStartup(ctx, store, cleaner, options); err != nil {
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

func TestJanitorReconcilesLaterRunsAfterAnEarlierFailure(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, time.July, 15, 16, 0, 0, 0, time.UTC)
	store := &stubReconciliationStore{
		queue: []drill.Run{
			{ID: "run-a", Cleanup: drill.CleanupPending, UpdatedAt: at},
			{ID: "run-b", Outcome: drill.OutcomeFailed, Cleanup: drill.CleanupPending, UpdatedAt: at},
		},
		outcomeErr: errors.New("concurrent mutation"),
	}
	cleaner := &sequencedCleaner{}
	options := sandbox.JanitorOptions{CleanupTimeout: time.Second, Now: (&sequenceClock{next: at}).Now}

	err := sandbox.ReconcileStartup(context.Background(), store, cleaner, options)
	if err == nil || !strings.Contains(err.Error(), "run-a") {
		t.Fatalf("ReconcileStartup error = %v, want one naming run-a", err)
	}
	if store.claimed != "run-b" {
		t.Fatalf("claimed = %q, want run-b", store.claimed)
	}
	if store.cleanedRun != "run-b" || store.cleanedStatus != drill.CleanupSucceeded {
		t.Fatalf("RecordCleanup(%q, %q), want run-b succeeded", store.cleanedRun, store.cleanedStatus)
	}
}

type stubReconciliationStore struct {
	queue         []drill.Run
	outcomeErr    error
	claimed       string
	cleanedRun    string
	cleanedStatus drill.CleanupStatus
}

func (store *stubReconciliationStore) RunsNeedingReconciliation(context.Context) ([]drill.Run, error) {
	return store.queue, nil
}

func (store *stubReconciliationStore) SandboxCleanupClaim(_ context.Context, runID string) (string, []drill.SandboxResourceClaim, bool, error) {
	store.claimed = runID
	return strings.Repeat("e", 64), nil, true, nil
}

func (store *stubReconciliationStore) RecordOutcome(_ context.Context, runID string, _ drill.Outcome, _ time.Time) (drill.Run, error) {
	if store.outcomeErr != nil {
		return drill.Run{}, store.outcomeErr
	}
	return drill.Run{ID: runID}, nil
}

func (store *stubReconciliationStore) BeginCleanupRetry(_ context.Context, runID string, _ time.Time) (drill.Run, error) {
	return drill.Run{ID: runID}, nil
}

func (store *stubReconciliationStore) RecordCleanup(_ context.Context, runID string, status drill.CleanupStatus, _ time.Time) (drill.Run, error) {
	store.cleanedRun = runID
	store.cleanedStatus = status
	return drill.Run{ID: runID, Cleanup: status}, nil
}

type sequencedCleaner struct {
	errors []error
}

func (cleaner *sequencedCleaner) Cleanup(context.Context, string, string, []drill.SandboxResourceClaim) error {
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
