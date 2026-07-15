package journal_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azizu06/rehearse/internal/drill"
	"github.com/azizu06/rehearse/internal/journal"
	"github.com/azizu06/rehearse/internal/sandbox"
)

const testSandboxClaimID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestJournalOwnershipExcludesLiveProcessAndReleasesAfterCrash(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	path := filepath.Join(directory, "rehearse.db")
	readyPath := filepath.Join(directory, "ready")
	command := exec.Command(os.Args[0], "-test.run=^TestJournalOwnershipHelper$")
	command.Env = append(os.Environ(),
		"REHEARSE_JOURNAL_OWNER_HELPER=1",
		"REHEARSE_JOURNAL_OWNER_DB="+path,
		"REHEARSE_JOURNAL_OWNER_READY="+readyPath,
	)
	if err := command.Start(); err != nil {
		t.Fatalf("start journal owner helper: %v", err)
	}
	killAndWait := func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
	}
	t.Cleanup(killAndWait)

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("journal owner helper did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}

	second, err := journal.Open(ctx, path)
	if second != nil {
		_ = second.Close()
	}
	if !errors.Is(err, journal.ErrJournalOwned) {
		t.Fatalf("second Open error = %v, want ErrJournalOwned", err)
	}
	aliasPath := filepath.Join(directory, "journal-alias.db")
	if err := os.Symlink(path, aliasPath); err != nil {
		t.Fatalf("create journal path alias: %v", err)
	}
	aliased, err := journal.Open(ctx, aliasPath)
	if aliased != nil {
		_ = aliased.Close()
	}
	if !errors.Is(err, journal.ErrJournalOwned) {
		t.Fatalf("aliased Open error = %v, want ErrJournalOwned", err)
	}

	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open read-only ownership evidence connection: %v", err)
	}
	defer func() { _ = database.Close() }()
	var claimStatus string
	if err := database.QueryRowContext(ctx, `SELECT status FROM sandbox_cleanup_claims WHERE run_id = 'run-owned'`).Scan(&claimStatus); err != nil {
		t.Fatalf("read live owner claim: %v", err)
	}
	if claimStatus != "pending" {
		t.Fatalf("live owner claim status = %q, want pending", claimStatus)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close ownership evidence connection: %v", err)
	}

	killAndWait()
	reopened, err := journal.Open(ctx, path)
	if err != nil {
		t.Fatalf("Open after owner crash: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	runs, err := reopened.RunsNeedingReconciliation(ctx)
	if err != nil {
		t.Fatalf("RunsNeedingReconciliation: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != "run-owned" {
		t.Fatalf("reconciliation queue after owner crash = %#v, want run-owned", runs)
	}
}

func TestJournalOwnershipHelper(t *testing.T) {
	if os.Getenv("REHEARSE_JOURNAL_OWNER_HELPER") != "1" {
		t.Skip("journal ownership subprocess helper")
	}
	ctx := context.Background()
	store, err := journal.Open(ctx, os.Getenv("REHEARSE_JOURNAL_OWNER_DB"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	now := time.Now().UTC()
	plan := testPlan(now)
	if err := store.CreatePlan(ctx, plan); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	if _, err := store.CreateRun(ctx, "run-owned", plan.ID, plan.Version, now); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := store.ClaimSandboxCleanup(ctx, "run-owned", testSandboxClaimID, now.Add(time.Second)); err != nil {
		t.Fatalf("ClaimSandboxCleanup: %v", err)
	}
	if err := os.WriteFile(os.Getenv("REHEARSE_JOURNAL_OWNER_READY"), []byte("ready"), 0o600); err != nil {
		t.Fatalf("write ready file: %v", err)
	}
	for {
		time.Sleep(time.Hour)
	}
}

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

func TestCleanupFailureRemainsInDurableReconciliationQueueAcrossRestarts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rehearse.db")
	now := time.Date(2026, time.July, 15, 17, 0, 0, 0, time.UTC)
	store, _, run := createPlanAndRun(t, ctx, path, now)
	if _, err := store.RecordOutcome(ctx, run.ID, drill.OutcomeFailed, now.Add(time.Second)); err != nil {
		t.Fatalf("RecordOutcome: %v", err)
	}
	failed, err := store.RecordCleanup(ctx, run.ID, drill.CleanupFailed, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("RecordCleanup(failed): %v", err)
	}
	if !failed.NeedsReconciliation {
		t.Fatal("cleanup failure was not queued for retry")
	}
	queued, err := store.RunsNeedingReconciliation(ctx)
	if err != nil || len(queued) != 1 || queued[0].ID != run.ID {
		t.Fatalf("queued runs = %#v, error %v", queued, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := journal.Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	queued, err = reopened.RunsNeedingReconciliation(ctx)
	if err != nil || len(queued) != 1 || queued[0].Cleanup != drill.CleanupFailed {
		t.Fatalf("reopened queue = %#v, error %v", queued, err)
	}
	retrying, err := reopened.BeginCleanupRetry(ctx, run.ID, now.Add(3*time.Second))
	if err != nil {
		t.Fatalf("BeginCleanupRetry: %v", err)
	}
	if retrying.Cleanup != drill.CleanupPending || !retrying.NeedsReconciliation {
		t.Fatalf("retrying projection = %#v", retrying)
	}
	completed, err := reopened.RecordCleanup(ctx, run.ID, drill.CleanupSucceeded, now.Add(4*time.Second))
	if err != nil {
		t.Fatalf("RecordCleanup(succeeded): %v", err)
	}
	if completed.NeedsReconciliation {
		t.Fatal("successful cleanup remained queued")
	}
	queued, err = reopened.RunsNeedingReconciliation(ctx)
	if err != nil || len(queued) != 0 {
		t.Fatalf("queue after success = %#v, error %v", queued, err)
	}
}

func TestSandboxCleanupClaimRequiresOneDurableRunAndQueuesFailures(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Date(2026, time.July, 15, 18, 0, 0, 0, time.UTC)
	store, _, run := createPlanAndRun(t, ctx, filepath.Join(t.TempDir(), "rehearse.db"), now)
	t.Cleanup(func() { _ = store.Close() })

	if err := store.ClaimSandboxCleanup(ctx, run.ID, "not-a-claim", now); !errors.Is(err, drill.ErrInvalidRun) {
		t.Fatalf("ClaimSandboxCleanup(invalid claim) error = %v, want ErrInvalidRun", err)
	}
	if err := store.ClaimSandboxCleanup(ctx, "missing-run", testSandboxClaimID, now); !errors.Is(err, journal.ErrNotFound) {
		t.Fatalf("ClaimSandboxCleanup(missing) error = %v, want ErrNotFound", err)
	}
	if err := store.ClaimSandboxCleanup(ctx, run.ID, testSandboxClaimID, now.Add(time.Second)); err != nil {
		t.Fatalf("ClaimSandboxCleanup: %v", err)
	}
	claimID, resources, active, err := store.SandboxCleanupClaim(ctx, run.ID)
	if err != nil || !active || claimID != testSandboxClaimID {
		t.Fatalf("SandboxCleanupClaim = %q active=%t error=%v", claimID, active, err)
	}
	if len(resources) != 0 {
		t.Fatalf("initial sandbox cleanup manifest = %#v, want empty", resources)
	}
	resource := drill.SandboxResourceClaim{Kind: "volume", Name: "rehearse-work", Generation: strings.Repeat("c", 64)}
	if err := store.AppendSandboxCleanupResource(ctx, run.ID, testSandboxClaimID, resource, now.Add(2*time.Second)); err != nil {
		t.Fatalf("AppendSandboxCleanupResource: %v", err)
	}
	if err := store.AppendSandboxCleanupResource(ctx, run.ID, testSandboxClaimID, resource, now.Add(2*time.Second)); !errors.Is(err, journal.ErrSandboxResourceClaimed) {
		t.Fatalf("duplicate AppendSandboxCleanupResource error = %v, want ErrSandboxResourceClaimed", err)
	}
	invalidResource := resource
	invalidResource.Generation = "not-a-generation"
	if err := store.AppendSandboxCleanupResource(ctx, run.ID, testSandboxClaimID, invalidResource, now.Add(2*time.Second)); !errors.Is(err, drill.ErrInvalidRun) {
		t.Fatalf("invalid AppendSandboxCleanupResource error = %v, want ErrInvalidRun", err)
	}
	claimID, resources, active, err = store.SandboxCleanupClaim(ctx, run.ID)
	if err != nil || !active || claimID != testSandboxClaimID || !reflect.DeepEqual(resources, []drill.SandboxResourceClaim{resource}) {
		t.Fatalf("SandboxCleanupClaim with manifest = %q %#v active=%t error=%v", claimID, resources, active, err)
	}
	if err := store.ClaimSandboxCleanup(ctx, run.ID, strings.Repeat("a", 64), now.Add(2*time.Second)); !errors.Is(err, journal.ErrSandboxClaimed) {
		t.Fatalf("second ClaimSandboxCleanup error = %v, want ErrSandboxClaimed", err)
	}
	queued, err := store.RunsNeedingReconciliation(ctx)
	if err != nil || len(queued) != 0 {
		t.Fatalf("live claim entered janitor queue = %#v, error %v", queued, err)
	}
	if err := store.RecordSandboxCleanup(ctx, run.ID, drill.CleanupFailed, now.Add(3*time.Second)); err != nil {
		t.Fatalf("RecordSandboxCleanup(failed): %v", err)
	}
	queued, err = store.RunsNeedingReconciliation(ctx)
	if err != nil || len(queued) != 1 {
		t.Fatalf("failed cleanup queue = %#v, error %v", queued, err)
	}
	if err := store.RecordSandboxCleanup(ctx, run.ID, drill.CleanupSucceeded, now.Add(4*time.Second)); err != nil {
		t.Fatalf("RecordSandboxCleanup(succeeded): %v", err)
	}
	if claimID, resources, active, err := store.SandboxCleanupClaim(ctx, run.ID); err != nil || active || claimID != "" || len(resources) != 0 {
		t.Fatalf("completed SandboxCleanupClaim = %q active=%t error=%v", claimID, active, err)
	}
	queued, err = store.RunsNeedingReconciliation(ctx)
	if err != nil || len(queued) != 0 {
		t.Fatalf("successful cleanup queue = %#v, error %v", queued, err)
	}
	if err := store.ClaimSandboxCleanup(ctx, run.ID, strings.Repeat("b", 64), now.Add(5*time.Second)); !errors.Is(err, journal.ErrSandboxClaimed) {
		t.Fatalf("post-success ClaimSandboxCleanup error = %v, want ErrSandboxClaimed", err)
	}
}

func TestCorruptSandboxClaimFailsClosedAndRemainsRetryable(t *testing.T) {
	testCases := []struct {
		name       string
		claimID    string
		resource   bool
		generation string
	}{
		{name: "legacy empty", claimID: ""},
		{name: "malformed", claimID: "not-a-claim"},
		{name: "legacy empty manifest generation", claimID: testSandboxClaimID, resource: true, generation: ""},
		{name: "malformed manifest generation", claimID: testSandboxClaimID, resource: true, generation: "not-a-generation"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, time.July, 15, 18, 15, 0, 0, time.UTC)
			path := filepath.Join(t.TempDir(), "rehearse.db")
			store, _, run := createPlanAndRun(t, ctx, path, now)

			database, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatalf("open corruption injector: %v", err)
			}
			if _, err := database.ExecContext(ctx, "PRAGMA ignore_check_constraints = ON"); err != nil {
				t.Fatalf("enable corruption injection: %v", err)
			}
			if _, err := database.ExecContext(ctx, `
				INSERT INTO sandbox_cleanup_claims(run_id, claim_id, status, claimed_at, updated_at)
				VALUES (?, ?, 'pending', ?, ?)
			`, run.ID, testCase.claimID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
				t.Fatalf("inject corrupt sandbox claim: %v", err)
			}
			if testCase.resource {
				if _, err := database.ExecContext(ctx, `
					INSERT INTO sandbox_cleanup_resources(run_id, claim_id, kind, name, generation_id, expected_at)
					VALUES (?, ?, 'volume', 'rehearse-corrupt', ?, ?)
				`, run.ID, testCase.claimID, testCase.generation, now.Format(time.RFC3339Nano)); err != nil {
					t.Fatalf("inject corrupt sandbox manifest: %v", err)
				}
			}
			if err := database.Close(); err != nil {
				t.Fatalf("close corruption injector: %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatalf("close initial store: %v", err)
			}
			store, err = journal.Open(ctx, path)
			if err != nil {
				t.Fatalf("reopen corrupt journal: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })

			if _, _, active, err := store.SandboxCleanupClaim(ctx, run.ID); !errors.Is(err, journal.ErrCorruptSandboxClaim) || active {
				t.Fatalf("SandboxCleanupClaim active=%t error=%v, want corrupt inactive result", active, err)
			}
			cleaner := &neverCalledCleaner{}
			janitor := sandbox.NewJanitor(store, cleaner, sandbox.JanitorOptions{CleanupTimeout: time.Second, Now: func() time.Time { return now.Add(time.Hour) }})
			for attempt := 0; attempt < 2; attempt++ {
				if err := janitor.Reconcile(ctx); !errors.Is(err, journal.ErrCorruptSandboxClaim) {
					t.Fatalf("reconcile attempt %d error = %v, want corrupt claim", attempt+1, err)
				}
			}
			if cleaner.calls != 0 {
				t.Fatalf("Docker cleaner called %d times for corrupt ownership", cleaner.calls)
			}
			queued, err := store.RunsNeedingReconciliation(ctx)
			if err != nil || len(queued) != 1 || queued[0].Cleanup != drill.CleanupFailed || !queued[0].NeedsReconciliation {
				t.Fatalf("corrupt claim queue = %#v, error %v", queued, err)
			}
		})
	}
}

type neverCalledCleaner struct {
	calls int
}

func (cleaner *neverCalledCleaner) Cleanup(context.Context, string, string, []drill.SandboxResourceClaim) error {
	cleaner.calls++
	return nil
}

func TestSandboxClaimAndJanitorQueueStayExclusiveAcrossTransition(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Date(2026, time.July, 15, 18, 30, 0, 0, time.UTC)
	store, _, run := createPlanAndRun(t, ctx, filepath.Join(t.TempDir(), "rehearse.db"), now)
	t.Cleanup(func() { _ = store.Close() })
	if err := store.ClaimSandboxCleanup(ctx, run.ID, testSandboxClaimID, now.Add(time.Second)); err != nil {
		t.Fatalf("ClaimSandboxCleanup: %v", err)
	}

	start := make(chan struct{})
	transitionResult := make(chan error, 1)
	queueResult := make(chan []drill.Run, 1)
	queueError := make(chan error, 1)
	go func() {
		<-start
		_, err := store.Transition(ctx, run.ID, drill.StagePreflight, now.Add(2*time.Second))
		transitionResult <- err
	}()
	go func() {
		<-start
		queued, err := store.RunsNeedingReconciliation(ctx)
		queueResult <- queued
		queueError <- err
	}()
	close(start)
	if err := <-transitionResult; err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if err := <-queueError; err != nil {
		t.Fatalf("RunsNeedingReconciliation: %v", err)
	}
	if queued := <-queueResult; len(queued) != 0 {
		t.Fatalf("live claim entered janitor queue during transition: %#v", queued)
	}
	if err := store.RecordSandboxCleanup(ctx, run.ID, drill.CleanupFailed, now.Add(3*time.Second)); err != nil {
		t.Fatalf("RecordSandboxCleanup(failed): %v", err)
	}
	queued, err := store.RunsNeedingReconciliation(ctx)
	if err != nil || len(queued) != 1 || queued[0].ID != run.ID {
		t.Fatalf("failed claim queue = %#v, error %v", queued, err)
	}
}

func TestSandboxOutcomeCannotEnterJanitorQueueBeforeLiveCleanupCloses(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Date(2026, time.July, 15, 18, 45, 0, 0, time.UTC)
	store, _, run := createPlanAndRun(t, ctx, filepath.Join(t.TempDir(), "rehearse.db"), now)
	t.Cleanup(func() { _ = store.Close() })
	if err := store.ClaimSandboxCleanup(ctx, run.ID, testSandboxClaimID, now.Add(time.Second)); err != nil {
		t.Fatalf("ClaimSandboxCleanup: %v", err)
	}

	start := make(chan struct{})
	outcomeResult := make(chan error, 1)
	queueResult := make(chan []drill.Run, 1)
	queueError := make(chan error, 1)
	go func() {
		<-start
		_, err := store.RecordOutcome(ctx, run.ID, drill.OutcomeFailed, now.Add(2*time.Second))
		outcomeResult <- err
	}()
	go func() {
		<-start
		queued, err := store.RunsNeedingReconciliation(ctx)
		queueResult <- queued
		queueError <- err
	}()
	close(start)
	if err := <-outcomeResult; err != nil {
		t.Fatalf("RecordOutcome: %v", err)
	}
	if err := <-queueError; err != nil {
		t.Fatalf("RunsNeedingReconciliation: %v", err)
	}
	if queued := <-queueResult; len(queued) != 0 {
		t.Fatalf("live cleanup claim overlapped the janitor queue: %#v", queued)
	}
	queued, err := store.RunsNeedingReconciliation(ctx)
	if err != nil || len(queued) != 0 {
		t.Fatalf("outcome with live cleanup claim queue = %#v, error %v", queued, err)
	}
	if err := store.RecordSandboxCleanup(ctx, run.ID, drill.CleanupFailed, now.Add(3*time.Second)); err != nil {
		t.Fatalf("RecordSandboxCleanup(failed): %v", err)
	}
	queued, err = store.RunsNeedingReconciliation(ctx)
	if err != nil || len(queued) != 1 || queued[0].ID != run.ID {
		t.Fatalf("closed failed cleanup claim queue = %#v, error %v", queued, err)
	}
}

func TestInterruptedSandboxClaimEntersJanitorQueueAfterRestart(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Date(2026, time.July, 15, 18, 50, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "rehearse.db")
	store, _, run := createPlanAndRun(t, ctx, path, now)
	if err := store.ClaimSandboxCleanup(ctx, run.ID, testSandboxClaimID, now.Add(time.Second)); err != nil {
		t.Fatalf("ClaimSandboxCleanup: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := journal.Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	queued, err := reopened.RunsNeedingReconciliation(ctx)
	if err != nil || len(queued) != 1 || queued[0].ID != run.ID {
		t.Fatalf("restart queue = %#v, error %v", queued, err)
	}
}

func TestSandboxCleanupClaimRejectsJanitorOwnedRuns(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Date(2026, time.July, 15, 19, 0, 0, 0, time.UTC)

	t.Run("restart reconciliation", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "rehearse.db")
		store, _, run := createPlanAndRun(t, ctx, path, now)
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
		if err := reopened.ClaimSandboxCleanup(ctx, run.ID, testSandboxClaimID, now.Add(2*time.Second)); !errors.Is(err, drill.ErrInvalidRun) {
			t.Fatalf("ClaimSandboxCleanup error = %v, want ErrInvalidRun", err)
		}
		queued, err := reopened.RunsNeedingReconciliation(ctx)
		if err != nil || len(queued) != 1 || queued[0].ID != run.ID {
			t.Fatalf("restart queue = %#v, error %v", queued, err)
		}
	})

	t.Run("outcome cleanup pending", func(t *testing.T) {
		store, _, run := createPlanAndRun(t, ctx, filepath.Join(t.TempDir(), "rehearse.db"), now)
		t.Cleanup(func() { _ = store.Close() })
		if _, err := store.RecordOutcome(ctx, run.ID, drill.OutcomeFailed, now.Add(time.Second)); err != nil {
			t.Fatalf("RecordOutcome: %v", err)
		}
		if err := store.ClaimSandboxCleanup(ctx, run.ID, testSandboxClaimID, now.Add(2*time.Second)); !errors.Is(err, drill.ErrInvalidRun) {
			t.Fatalf("ClaimSandboxCleanup error = %v, want ErrInvalidRun", err)
		}
		queued, err := store.RunsNeedingReconciliation(ctx)
		if err != nil || len(queued) != 1 || queued[0].ID != run.ID {
			t.Fatalf("cleanup-pending queue = %#v, error %v", queued, err)
		}
	})
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
