package journal_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/azizu06/rehearse/internal/drill"
	"github.com/azizu06/rehearse/internal/evidence"
	"github.com/azizu06/rehearse/internal/journal"
	"github.com/azizu06/rehearse/internal/probe"
	"github.com/azizu06/rehearse/internal/redact"
)

func TestTypedProbeConfigAndRedactedReportSurviveRestart(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	directory := t.TempDir()
	path := filepath.Join(directory, "rehearse.db")
	store, err := journal.Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	startedAt := time.Date(2026, time.July, 15, 20, 0, 0, 0, time.UTC)
	plan := drill.Plan{
		ID: "plan-10", Name: "probe evidence", Version: 1, CreatedAt: startedAt,
		Spec: drill.PlanSpec{
			SourceKind: "backup-source", TargetKind: "restore-target",
			ProbeConfig: probe.Config{SchemaVersion: probe.SchemaVersion, Probes: []probe.Spec{
				{
					Ordinal: 1, ID: "trusted-check", Kind: probe.KindCommand, Required: true,
					Retry:   probe.RetryPolicy{Deadline: time.Second, Backoff: 10 * time.Millisecond, MaxAttempts: 1},
					Command: &probe.CommandSpec{Executable: "/usr/bin/true", ExpectedExitCode: 0, TrustAcknowledged: true},
				},
			}},
		},
	}
	if err := store.CreatePlan(ctx, plan); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	run, err := store.CreateRun(ctx, "run-10", plan.ID, plan.Version, startedAt)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	for index, stage := range []drill.Stage{drill.StagePreflight, drill.StageAcquire, drill.StageRestore, drill.StageBoot, drill.StageProbe, drill.StageReport} {
		run, err = store.Transition(ctx, run.ID, stage, startedAt.Add(time.Duration(index+1)*time.Second))
		if err != nil {
			t.Fatalf("Transition(%s): %v", stage, err)
		}
	}
	run, err = store.RecordOutcome(ctx, run.ID, drill.OutcomeSucceeded, startedAt.Add(7*time.Second))
	if err != nil {
		t.Fatalf("RecordOutcome: %v", err)
	}
	run, err = store.RecordCleanup(ctx, run.ID, drill.CleanupSucceeded, startedAt.Add(8*time.Second))
	if err != nil {
		t.Fatalf("RecordCleanup: %v", err)
	}

	secret := "issue-10-persisted-secret"
	report := evidence.Report{
		SchemaVersion: evidence.SchemaVersion,
		RunID:         run.ID, PlanID: plan.ID, PlanVersion: plan.Version,
		RecoveryPoint: evidence.RecoveryPoint{ID: "snapshot-" + secret, SelectedAt: startedAt},
		Stages:        []evidence.Stage{{Ordinal: 1, Name: drill.StageProbe, StartedAt: startedAt.Add(5 * time.Second), FinishedAt: startedAt.Add(6 * time.Second), Duration: time.Second}},
		Probes:        []probe.Evidence{{Ordinal: 1, ID: "trusted-check", Kind: probe.KindCommand, Required: true, Status: probe.StatusPassed, Attempts: 1, StartedAt: startedAt.Add(5 * time.Second), FinishedAt: startedAt.Add(6 * time.Second), Duration: time.Second, Observed: secret, TrustedHostCommand: true}},
		Outcome:       run.Outcome, Cleanup: run.Cleanup,
	}
	if err := store.SaveReport(ctx, report, redact.New(secret)); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for _, suffix := range []string{"", "-wal", "-shm"} {
		contents, err := os.ReadFile(path + suffix)
		if err != nil && os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("read sqlite file %s: %v", suffix, err)
		}
		if bytes.Contains(contents, []byte(secret)) {
			t.Fatalf("secret marker persisted in sqlite file %s", suffix)
		}
	}

	reopened, err := journal.Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	loadedPlan, err := reopened.Plan(ctx, plan.ID, plan.Version)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !loadedPlan.Spec.ProbeConfig.Probes[0].Command.TrustAcknowledged {
		t.Fatal("persisted command trust acknowledgement was lost")
	}
	loadedReport, err := reopened.Report(ctx, run.ID)
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	encoded, err := loadedReport.CanonicalJSON(redact.New(secret))
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	if bytes.Contains(encoded, []byte(secret)) || !bytes.Contains(encoded, []byte(redact.Replacement)) {
		t.Fatalf("loaded report redaction = %s", encoded)
	}
}
