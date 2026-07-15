package journal_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azizu06/rehearse/internal/drill"
	"github.com/azizu06/rehearse/internal/journal"
)

func TestJournalPersistsCompletedDrillHistoryAcrossRestart(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rehearse.db")
	startedAt := time.Date(2026, time.July, 15, 16, 0, 0, 0, time.UTC)

	store, err := journal.Open(ctx, path)
	if err != nil {
		t.Fatalf("open empty journal: %v", err)
	}

	plan := drill.Plan{
		ID:        "plan-1",
		Name:      "daily recovery drill",
		Version:   1,
		CreatedAt: startedAt,
		Spec: drill.PlanSpec{
			SourceKind: "backup-source",
			TargetKind: "restore-target",
			CredentialReferences: []drill.CredentialReference{
				{Provider: drill.CredentialEnvironment, Locator: "REHEARSE_BACKUP_PASSWORD"},
			},
		},
	}
	if err := store.CreatePlan(ctx, plan); err != nil {
		t.Fatalf("create plan: %v", err)
	}

	run, err := store.CreateRun(ctx, "run-1", plan.ID, plan.Version, startedAt)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	for step, stage := range []drill.Stage{
		drill.StagePreflight,
		drill.StageAcquire,
		drill.StageRestore,
		drill.StageBoot,
		drill.StageProbe,
		drill.StageReport,
	} {
		run, err = store.Transition(ctx, run.ID, stage, startedAt.Add(time.Duration(step+1)*time.Second))
		if err != nil {
			t.Fatalf("transition to %s: %v", stage, err)
		}
	}
	run, err = store.RecordOutcome(ctx, run.ID, drill.OutcomeSucceeded, startedAt.Add(7*time.Second))
	if err != nil {
		t.Fatalf("record success: %v", err)
	}
	run, err = store.RecordCleanup(ctx, run.ID, drill.CleanupSucceeded, startedAt.Add(8*time.Second))
	if err != nil {
		t.Fatalf("record cleanup: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close journal: %v", err)
	}

	reopened, err := journal.Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen journal: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	gotPlan, err := reopened.Plan(ctx, plan.ID, plan.Version)
	if err != nil {
		t.Fatalf("load plan after restart: %v", err)
	}
	if gotPlan.Name != plan.Name || gotPlan.Spec.CredentialReferences[0].Locator != "REHEARSE_BACKUP_PASSWORD" {
		t.Fatalf("plan after restart = %#v, want %#v", gotPlan, plan)
	}

	gotRun, err := reopened.Run(ctx, run.ID)
	if err != nil {
		t.Fatalf("load run after restart: %v", err)
	}
	if gotRun.Outcome != drill.OutcomeSucceeded || gotRun.Cleanup != drill.CleanupSucceeded {
		t.Fatalf("run result after restart = outcome %q cleanup %q", gotRun.Outcome, gotRun.Cleanup)
	}

	events, err := reopened.Events(ctx, run.ID)
	if err != nil {
		t.Fatalf("load events after restart: %v", err)
	}
	if got, want := events[len(events)-2].Kind, drill.EventRunSucceeded; got != want {
		t.Fatalf("penultimate event = %q, want %q", got, want)
	}
	if got, want := events[len(events)-1].Kind, drill.EventCleanupSucceeded; got != want {
		t.Fatalf("last event = %q, want %q", got, want)
	}
}

func TestMigrationsAreIdempotentFromAnEmptyDatabase(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rehearse.db")
	for attempt := 0; attempt < 2; attempt++ {
		store, err := journal.Open(ctx, path)
		if err != nil {
			t.Fatalf("Open attempt %d: %v", attempt+1, err)
		}
		version, err := store.SchemaVersion(ctx)
		if err != nil {
			t.Fatalf("SchemaVersion attempt %d: %v", attempt+1, err)
		}
		if version != journal.CurrentSchemaVersion {
			t.Fatalf("schema version = %d, want %d", version, journal.CurrentSchemaVersion)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("Close attempt %d: %v", attempt+1, err)
		}
	}
}

func TestRestartMarksUnfinishedRunsForReconciliationWithoutLosingHistory(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rehearse.db")
	now := time.Date(2026, time.July, 15, 16, 0, 0, 0, time.UTC)
	store, plan, run := createPlanAndRun(t, ctx, path, now)
	if _, err := store.Transition(ctx, run.ID, drill.StagePreflight, now.Add(time.Second)); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := journal.Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	gotPlan, err := reopened.Plan(ctx, plan.ID, plan.Version)
	if err != nil || gotPlan.Name != plan.Name {
		t.Fatalf("plan after restart = %#v, error %v", gotPlan, err)
	}
	gotRun, err := reopened.Run(ctx, run.ID)
	if err != nil {
		t.Fatalf("Run after restart: %v", err)
	}
	if gotRun.Stage != drill.StagePreflight || !gotRun.NeedsReconciliation || gotRun.ReconciliationRequestedAt.IsZero() {
		t.Fatalf("run after restart = %#v", gotRun)
	}
	events, err := reopened.Events(ctx, run.ID)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if got, want := events[len(events)-1].Kind, drill.EventReconciliationRequired; got != want {
		t.Fatalf("last event = %q, want %q", got, want)
	}
}

func TestRestartPreservesImmutablePlanVersionHistory(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rehearse.db")
	now := time.Date(2026, time.July, 15, 16, 0, 0, 0, time.UTC)
	store, err := journal.Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	first := testPlan(now)
	if err := store.CreatePlan(ctx, first); err != nil {
		t.Fatalf("CreatePlan(first): %v", err)
	}
	second := first
	second.Version = 2
	second.CreatedAt = now.Add(time.Minute)
	second.Spec.TargetKind = "second-restore-target"
	if err := store.CreatePlan(ctx, second); err != nil {
		t.Fatalf("CreatePlan(second): %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := journal.Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	gotFirst, err := reopened.Plan(ctx, first.ID, first.Version)
	if err != nil {
		t.Fatalf("Plan(first): %v", err)
	}
	gotSecond, err := reopened.Plan(ctx, second.ID, second.Version)
	if err != nil {
		t.Fatalf("Plan(second): %v", err)
	}
	if gotFirst.Spec.TargetKind != first.Spec.TargetKind || gotSecond.Spec.TargetKind != second.Spec.TargetKind {
		t.Fatalf("plan history = %q, %q", gotFirst.Spec.TargetKind, gotSecond.Spec.TargetKind)
	}
}

func TestPersistedPlanConfigurationCannotContainEnvironmentSecretValues(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rehearse.db")
	secret := []byte("issue-6-secret-value-must-never-enter-sqlite")
	t.Setenv("REHEARSE_BACKUP_PASSWORD", string(secret))
	now := time.Date(2026, time.July, 15, 16, 0, 0, 0, time.UTC)

	store, err := journal.Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	plan := testPlan(now)
	if err := store.CreatePlan(ctx, plan); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	files, err := filepath.Glob(path + "*")
	if err != nil {
		t.Fatalf("Glob database files: %v", err)
	}
	for _, file := range files {
		contents, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", file, err)
		}
		if bytes.Contains(contents, secret) {
			t.Fatalf("secret value was persisted in %s", file)
		}
	}
}

func TestConcurrentTransitionsAreSerialized(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rehearse.db")
	now := time.Date(2026, time.July, 15, 16, 0, 0, 0, time.UTC)
	store, _, run := createPlanAndRun(t, ctx, path, now)
	t.Cleanup(func() { _ = store.Close() })

	const workers = 32
	var successes atomic.Int32
	errorsSeen := make(chan error, workers)
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := store.Transition(ctx, run.ID, drill.StagePreflight, now.Add(time.Second))
			switch {
			case err == nil:
				successes.Add(1)
			case errors.Is(err, drill.ErrInvalidTransition):
			default:
				errorsSeen <- err
			}
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Errorf("unexpected transition error: %v", err)
	}
	if got := successes.Load(); got != 1 {
		t.Fatalf("successful transitions = %d, want 1", got)
	}

	gotRun, err := store.Run(ctx, run.ID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotRun.Stage != drill.StagePreflight {
		t.Fatalf("stage = %q, want %q", gotRun.Stage, drill.StagePreflight)
	}
	events, err := store.Events(ctx, run.ID)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("event count = %d, want 2", len(events))
	}
}

func TestStoreRejectsInvalidAndMissingRecords(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	if _, err := journal.Open(ctx, " "); err == nil {
		t.Fatal("opening an empty journal path succeeded")
	}
	if _, err := journal.Open(ctx, filepath.Join(t.TempDir(), "missing", "rehearse.db")); err == nil {
		t.Fatal("opening a journal in a missing directory succeeded")
	}

	store, err := journal.Open(ctx, filepath.Join(t.TempDir(), "rehearse.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	now := time.Date(2026, time.July, 15, 16, 0, 0, 0, time.UTC)
	invalidPlan := testPlan(now)
	invalidPlan.Name = ""
	if err := store.CreatePlan(ctx, invalidPlan); !errors.Is(err, drill.ErrInvalidPlan) {
		t.Fatalf("invalid plan error = %v, want ErrInvalidPlan", err)
	}
	plan := testPlan(now)
	if err := store.CreatePlan(ctx, plan); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	renamed := plan
	renamed.Version = 2
	renamed.Name = "renamed plan"
	if err := store.CreatePlan(ctx, renamed); !errors.Is(err, drill.ErrInvalidPlan) {
		t.Fatalf("renamed plan error = %v, want ErrInvalidPlan", err)
	}
	if _, err := store.Plan(ctx, "missing", 1); !errors.Is(err, journal.ErrNotFound) {
		t.Fatalf("missing plan error = %v, want ErrNotFound", err)
	}
	if _, err := store.Run(ctx, "missing"); !errors.Is(err, journal.ErrNotFound) {
		t.Fatalf("missing run error = %v, want ErrNotFound", err)
	}
	if _, err := store.CreateRun(ctx, "", plan.ID, plan.Version, now); !errors.Is(err, drill.ErrInvalidRun) {
		t.Fatalf("invalid run error = %v, want ErrInvalidRun", err)
	}
	if _, err := store.Transition(ctx, "missing", drill.StagePreflight, now); !errors.Is(err, journal.ErrNotFound) {
		t.Fatalf("missing transition error = %v, want ErrNotFound", err)
	}
	run, err := store.CreateRun(ctx, "run-1", plan.ID, plan.Version, now)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := store.Transition(ctx, run.ID, drill.StageAcquire, now.Add(time.Second)); !errors.Is(err, drill.ErrInvalidTransition) {
		t.Fatalf("invalid transition error = %v, want ErrInvalidTransition", err)
	}
	if _, err := store.RecordCleanup(ctx, run.ID, drill.CleanupSucceeded, now.Add(time.Second)); !errors.Is(err, drill.ErrInvalidCleanup) {
		t.Fatalf("invalid cleanup error = %v, want ErrInvalidCleanup", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := store.SchemaVersion(ctx); err == nil {
		t.Fatal("reading schema version after close succeeded")
	}
}

func TestReconciledRunProgressClearsRestartMetadata(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rehearse.db")
	now := time.Date(2026, time.July, 15, 16, 0, 0, 0, time.UTC)
	store, _, run := createPlanAndRun(t, ctx, path, now)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := journal.Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	reconciled, err := reopened.Run(ctx, run.ID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !reconciled.NeedsReconciliation {
		t.Fatal("restart did not mark run for reconciliation")
	}
	progressed, err := reopened.Transition(ctx, run.ID, drill.StagePreflight, reconciled.UpdatedAt.Add(time.Second))
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if progressed.NeedsReconciliation || !progressed.ReconciliationRequestedAt.IsZero() {
		t.Fatalf("progressed run retained restart metadata: %#v", progressed)
	}
}

func createPlanAndRun(t *testing.T, ctx context.Context, path string, now time.Time) (*journal.Store, drill.Plan, drill.Run) {
	t.Helper()

	store, err := journal.Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	plan := testPlan(now)
	if err := store.CreatePlan(ctx, plan); err != nil {
		_ = store.Close()
		t.Fatalf("CreatePlan: %v", err)
	}
	run, err := store.CreateRun(ctx, "run-1", plan.ID, plan.Version, now)
	if err != nil {
		_ = store.Close()
		t.Fatalf("CreateRun: %v", err)
	}
	return store, plan, run
}

func testPlan(now time.Time) drill.Plan {
	return drill.Plan{
		ID:        "plan-1",
		Name:      "daily recovery drill",
		Version:   1,
		CreatedAt: now,
		Spec: drill.PlanSpec{
			SourceKind: "backup-source",
			TargetKind: "restore-target",
			CredentialReferences: []drill.CredentialReference{
				{Provider: drill.CredentialEnvironment, Locator: "REHEARSE_BACKUP_PASSWORD"},
			},
		},
	}
}
