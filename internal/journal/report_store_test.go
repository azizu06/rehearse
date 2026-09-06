package journal_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/azizu06/rehearse/internal/controlplane"
	"github.com/azizu06/rehearse/internal/drill"
	"github.com/azizu06/rehearse/internal/evidence"
	"github.com/azizu06/rehearse/internal/journal"
	"github.com/azizu06/rehearse/internal/probe"
	"github.com/azizu06/rehearse/internal/redact"
)

func TestCommandBoundaryEvidenceRemainsRedactedThroughRestartAndAPI(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	secret := "issue-10-persisted-secret"
	boundaryMarker := commandReportBoundaryMarker()
	redactor := redact.New(secret, boundaryMarker)
	planID := "Plan.ID_雪 with space " + strings.Repeat("p", 128)
	runID := "Run/ID_雪 with space/" + strings.Repeat("r", 128)
	directory := t.TempDir()
	path := filepath.Join(directory, "rehearse.db")
	store, err := journal.Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	startedAt := time.Date(2026, time.July, 15, 20, 0, 0, 0, time.UTC)
	plan := drill.Plan{
		ID: planID, Name: "probe evidence", Version: 1, CreatedAt: startedAt,
		Spec: drill.PlanSpec{
			SourceKind: "backup-source", TargetKind: "restore-target",
			ProbeConfig: probe.Config{SchemaVersion: probe.SchemaVersion, Probes: []probe.Spec{
				{
					Ordinal: 1, ID: "trusted-check", Kind: probe.KindCommand, Required: true,
					Retry: probe.RetryPolicy{Deadline: 5 * time.Second, Backoff: 10 * time.Millisecond, MaxAttempts: 1},
					Command: &probe.CommandSpec{
						Executable:        executable,
						Args:              []string{"-test.run=TestProbeCommandReportBoundaryHelper", "--", "report-boundary-helper"},
						ExpectedExitCode:  0,
						TrustAcknowledged: true,
					},
				},
			}},
		},
	}
	if err := store.CreatePlan(ctx, plan); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	run, err := store.CreateRun(ctx, runID, plan.ID, plan.Version, startedAt)
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

	probeResult := probe.NewRunner(probe.Options{Redactor: redactor}).Run(ctx, plan.Spec.ProbeConfig)
	if !probeResult.RequiredPassed {
		t.Fatalf("Runner.Run() required passed = false, probes = %#v", probeResult.Probes)
	}
	probeResult.Probes[0].StartedAt = startedAt.Add(5 * time.Second)
	probeResult.Probes[0].FinishedAt = startedAt.Add(6 * time.Second)
	probeResult.Probes[0].Duration = time.Second
	report := evidence.Report{
		SchemaVersion: evidence.SchemaVersion,
		RunID:         run.ID, PlanID: plan.ID, PlanVersion: plan.Version,
		RecoveryPoint: evidence.RecoveryPoint{ID: "snapshot-" + secret, SelectedAt: startedAt},
		Stages:        []evidence.Stage{{Ordinal: 1, Name: drill.StageProbe, StartedAt: startedAt.Add(5 * time.Second), FinishedAt: startedAt.Add(6 * time.Second), Duration: time.Second}},
		Probes:        probeResult.Probes,
		Outcome:       run.Outcome, Cleanup: run.Cleanup,
	}
	forged := report
	forged.Probes = append([]probe.Evidence(nil), report.Probes...)
	forged.Probes[0].Required = false
	if err := store.SaveReport(ctx, forged, redactor); !errors.Is(err, evidence.ErrInvalidReport) {
		t.Fatalf("SaveReport accepted evidence that hid a required probe: %v", err)
	}
	forged = report
	forged.Probes = append([]probe.Evidence(nil), report.Probes...)
	forged.Probes[0].Attempts = 100
	if err := store.SaveReport(ctx, forged, redactor); !errors.Is(err, evidence.ErrInvalidReport) {
		t.Fatalf("SaveReport accepted impossible retry evidence: %v", err)
	}
	if err := store.SaveReport(ctx, report, redactor); err != nil {
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
	assertBoundedCommandEvidence(t, loadedReport.Probes[0].Observed, boundaryMarker)
	handler := controlplane.NewHandler(controlplane.Options{ReportReader: reopened, Redactor: redactor})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+url.PathEscape(run.ID)+"/report", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("report API status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	var apiView evidence.ReportView
	if err := json.NewDecoder(response.Body).Decode(&apiView); err != nil {
		t.Fatalf("decode report API error = %v", err)
	}
	assertBoundedCommandEvidence(t, apiView.Snapshot.Probes[0].Observed, boundaryMarker)
	encoded, err := loadedReport.CanonicalJSON(redactor)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	if bytes.Contains(encoded, []byte(secret)) || !bytes.Contains(encoded, []byte(redact.Replacement)) {
		t.Fatalf("loaded report redaction = %s", encoded)
	}
}

func TestTerminalReportsPersistUnattemptedProbeEvidence(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	startedAt := time.Date(2026, time.September, 6, 2, 0, 0, 0, time.UTC)
	store, err := journal.Open(ctx, filepath.Join(t.TempDir(), "rehearse.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	plan := drill.Plan{
		ID: "terminal-plan", Name: "terminal reports", Version: 1, CreatedAt: startedAt,
		Spec: drill.PlanSpec{
			SourceKind: "backup-source", TargetKind: "restore-target",
			ProbeConfig: probe.Config{SchemaVersion: probe.SchemaVersion, Probes: []probe.Spec{
				{Ordinal: 1, ID: "executed-probe", Kind: probe.KindHTTP, Required: true, Retry: probe.RetryPolicy{Deadline: time.Second, Backoff: 10 * time.Millisecond, MaxAttempts: 1}, HTTP: &probe.HTTPSpec{URL: "http://127.0.0.1/executed", ExpectedStatus: http.StatusNoContent}},
				{Ordinal: 2, ID: "unattempted-probe", Kind: probe.KindHTTP, Required: false, Retry: probe.RetryPolicy{Deadline: time.Second, Backoff: 10 * time.Millisecond, MaxAttempts: 1}, HTTP: &probe.HTTPSpec{URL: "http://127.0.0.1/unattempted", ExpectedStatus: http.StatusNoContent}},
			}},
		},
	}
	if err := store.CreatePlan(ctx, plan); err != nil {
		t.Fatalf("CreatePlan() error = %v", err)
	}
	tests := []struct {
		name         string
		outcome      drill.Outcome
		status       probe.Status
		exhausted    probe.ExhaustedBy
		beforeProbes bool
	}{
		{name: "failed-before-probes", outcome: drill.OutcomeFailed, beforeProbes: true},
		{name: "cancelled", outcome: drill.OutcomeCancelled, status: probe.StatusCancelled},
		{name: "timed-out", outcome: drill.OutcomeTimedOut, status: probe.StatusTimedOut, exhausted: probe.ExhaustedDeadline},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			offset := time.Duration(index) * time.Minute
			run, err := store.CreateRun(ctx, "terminal-run-"+test.name, plan.ID, plan.Version, startedAt.Add(offset))
			if err != nil {
				t.Fatalf("CreateRun() error = %v", err)
			}
			outcomeAt := startedAt.Add(offset + time.Second)
			stageEvidence := evidence.Stage{Ordinal: 1, Name: drill.StageQueued, StartedAt: startedAt.Add(offset), FinishedAt: outcomeAt, Duration: time.Second}
			probeEvidence := []probe.Evidence{
				{Ordinal: 1, ID: "executed-probe", Kind: probe.KindHTTP, Required: true, Status: probe.StatusNotAttempted},
				{Ordinal: 2, ID: "unattempted-probe", Kind: probe.KindHTTP, Required: false, Status: probe.StatusNotAttempted},
			}
			if !test.beforeProbes {
				for stageIndex, stage := range []drill.Stage{drill.StagePreflight, drill.StageAcquire, drill.StageRestore, drill.StageBoot, drill.StageProbe} {
					run, err = store.Transition(ctx, run.ID, stage, startedAt.Add(offset+time.Duration(stageIndex+1)*time.Second))
					if err != nil {
						t.Fatalf("Transition(%s) error = %v", stage, err)
					}
				}
				outcomeAt = startedAt.Add(offset + 6*time.Second)
				stageEvidence = evidence.Stage{Ordinal: 1, Name: drill.StageProbe, StartedAt: startedAt.Add(offset + 5*time.Second), FinishedAt: outcomeAt, Duration: time.Second}
				probeEvidence[0] = probe.Evidence{Ordinal: 1, ID: "executed-probe", Kind: probe.KindHTTP, Required: true, Status: test.status, Attempts: 1, StartedAt: startedAt.Add(offset + 5*time.Second), FinishedAt: outcomeAt, Duration: time.Second, ExhaustedBy: test.exhausted}
			}
			run, err = store.RecordOutcome(ctx, run.ID, test.outcome, outcomeAt)
			if err != nil {
				t.Fatalf("RecordOutcome(%s) error = %v", test.outcome, err)
			}
			cleanup := drill.CleanupSucceeded
			if test.beforeProbes {
				cleanup = drill.CleanupFailed
			}
			run, err = store.RecordCleanup(ctx, run.ID, cleanup, outcomeAt.Add(time.Second))
			if err != nil {
				t.Fatalf("RecordCleanup() error = %v", err)
			}
			report := evidence.Report{
				SchemaVersion: evidence.SchemaVersion,
				RunID:         run.ID, PlanID: plan.ID, PlanVersion: plan.Version,
				RecoveryPoint: evidence.RecoveryPoint{ID: "snapshot-terminal", SelectedAt: startedAt.Add(offset)},
				Stages:        []evidence.Stage{stageEvidence},
				Probes:        probeEvidence,
				Outcome:       run.Outcome, Cleanup: run.Cleanup,
			}
			if test.beforeProbes {
				forged := report
				forged.Probes = append([]probe.Evidence(nil), report.Probes...)
				forged.Probes[0] = probe.Evidence{Ordinal: 1, ID: "executed-probe", Kind: probe.KindHTTP, Required: true, Status: probe.StatusPassed, Attempts: 1, StartedAt: stageEvidence.StartedAt, FinishedAt: stageEvidence.FinishedAt, Duration: stageEvidence.Duration}
				if err := store.SaveReport(ctx, forged, redact.Redactor{}); !errors.Is(err, evidence.ErrInvalidReport) {
					t.Fatalf("SaveReport() forged queued attempt error = %v, want ErrInvalidReport", err)
				}
			}
			contradictory := report
			contradictory.Stages = append([]evidence.Stage(nil), report.Stages...)
			contradictory.Stages[0].StartedAt = contradictory.Stages[0].StartedAt.Add(time.Millisecond)
			contradictory.Stages[0].FinishedAt = contradictory.Stages[0].FinishedAt.Add(time.Millisecond)
			if err := store.SaveReport(ctx, contradictory, redact.Redactor{}); !errors.Is(err, evidence.ErrInvalidReport) {
				t.Fatalf("SaveReport() contradictory stage error = %v, want ErrInvalidReport", err)
			}
			if !test.beforeProbes {
				omitted := report
				omitted.Probes = report.Probes[1:]
				if err := store.SaveReport(ctx, omitted, redact.Redactor{}); !errors.Is(err, evidence.ErrInvalidReport) {
					t.Fatalf("SaveReport() omitted executed probe error = %v, want ErrInvalidReport", err)
				}
			}
			if err := store.SaveReport(ctx, report, redact.Redactor{}); err != nil {
				t.Fatalf("SaveReport() error = %v", err)
			}
			loaded, err := store.Report(ctx, run.ID)
			if err != nil {
				t.Fatalf("Report() error = %v", err)
			}
			firstUnattempted := 1
			if test.beforeProbes {
				firstUnattempted = 0
			}
			for _, unattempted := range loaded.Probes[firstUnattempted:] {
				if unattempted.Status != probe.StatusNotAttempted || unattempted.Attempts != 0 || !unattempted.StartedAt.IsZero() || !unattempted.FinishedAt.IsZero() || unattempted.Duration != 0 {
					t.Errorf("Report() unattempted evidence = %#v, want status not_attempted with zero attempts and timing", unattempted)
				}
			}
			if test.beforeProbes {
				beforeRetry, err := loaded.CanonicalJSON(redact.Redactor{})
				if err != nil {
					t.Fatalf("CanonicalJSON() before retry error = %v", err)
				}
				run, err = store.BeginCleanupRetry(ctx, run.ID, outcomeAt.Add(2*time.Second))
				if err != nil {
					t.Fatalf("BeginCleanupRetry() error = %v", err)
				}
				pending := getReportAPIView(t, store, run.ID)
				if pending.Snapshot["cleanup"] != string(drill.CleanupFailed) {
					t.Errorf("report API snapshot cleanup = %v, want %q", pending.Snapshot["cleanup"], drill.CleanupFailed)
				}
				if pending.Snapshot["snapshot_sequence"] == nil || pending.Snapshot["snapshot_at"] == nil {
					t.Errorf("report API snapshot provenance = %#v, want sequence and timestamp", pending.Snapshot)
				}
				if pending.CurrentCleanup.Status != drill.CleanupPending || pending.CurrentCleanup.AsOfSequence != run.Version || !pending.CurrentCleanup.AsOf.Equal(run.UpdatedAt) {
					t.Errorf("report API current cleanup = %#v, want pending at run version %d", pending.CurrentCleanup, run.Version)
				}
				run, err = store.RecordCleanup(ctx, run.ID, drill.CleanupSucceeded, outcomeAt.Add(3*time.Second))
				if err != nil {
					t.Fatalf("RecordCleanup(retry) error = %v", err)
				}
				succeeded := getReportAPIView(t, store, run.ID)
				if succeeded.Snapshot["cleanup"] != string(drill.CleanupFailed) || succeeded.CurrentCleanup.Status != drill.CleanupSucceeded || succeeded.CurrentCleanup.AsOfSequence != run.Version || !succeeded.CurrentCleanup.AsOf.Equal(run.UpdatedAt) {
					t.Errorf("report API after cleanup retry = snapshot %#v current %#v", succeeded.Snapshot, succeeded.CurrentCleanup)
				}
				storedAfterRetry, err := store.Report(ctx, run.ID)
				if err != nil {
					t.Fatalf("Report() after retry error = %v", err)
				}
				afterRetry, err := storedAfterRetry.CanonicalJSON(redact.Redactor{})
				if err != nil {
					t.Fatalf("CanonicalJSON() after retry error = %v", err)
				}
				if !bytes.Equal(afterRetry, beforeRetry) {
					t.Fatalf("stored report changed across cleanup retry:\nbefore: %s\nafter:  %s", beforeRetry, afterRetry)
				}
			}
		})
	}
}

type reportAPIView struct {
	Snapshot       map[string]any `json:"snapshot"`
	CurrentCleanup struct {
		Status       drill.CleanupStatus `json:"status"`
		AsOfSequence int64               `json:"as_of_sequence"`
		AsOf         time.Time           `json:"as_of"`
	} `json:"current_cleanup"`
}

func getReportAPIView(t *testing.T, reader controlplane.ReportReader, runID string) reportAPIView {
	t.Helper()
	handler := controlplane.NewHandler(controlplane.Options{ReportReader: reader})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+url.PathEscape(runID)+"/report", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("report API status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	var view reportAPIView
	if err := json.NewDecoder(response.Body).Decode(&view); err != nil {
		t.Fatalf("decode report API: %v", err)
	}
	return view
}

func TestSaveReportAndCleanupRetryPublishOneConsistentOrder(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	startedAt := time.Date(2026, time.September, 6, 5, 0, 0, 0, time.UTC)
	store, err := journal.Open(ctx, filepath.Join(t.TempDir(), "rehearse.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	configuration := probe.Config{SchemaVersion: probe.SchemaVersion, Probes: []probe.Spec{
		{Ordinal: 1, ID: "never-started", Kind: probe.KindHTTP, Required: false, Retry: probe.RetryPolicy{Deadline: time.Second, Backoff: 10 * time.Millisecond, MaxAttempts: 1}, HTTP: &probe.HTTPSpec{URL: "http://127.0.0.1/unattempted", ExpectedStatus: http.StatusNoContent}},
	}}
	plan := drill.Plan{
		ID: "report-order-plan", Name: "report ordering", Version: 1, CreatedAt: startedAt,
		Spec: drill.PlanSpec{SourceKind: "backup-source", TargetKind: "restore-target", ProbeConfig: configuration},
	}
	if err := store.CreatePlan(ctx, plan); err != nil {
		t.Fatalf("CreatePlan() error = %v", err)
	}
	for attempt := 0; attempt < 16; attempt++ {
		runStartedAt := startedAt.Add(time.Duration(attempt) * time.Minute)
		run, err := store.CreateRun(ctx, fmt.Sprintf("report-order-run-%d", attempt), plan.ID, plan.Version, runStartedAt)
		if err != nil {
			t.Fatalf("CreateRun(%d) error = %v", attempt, err)
		}
		run, err = store.RecordOutcome(ctx, run.ID, drill.OutcomeFailed, runStartedAt.Add(time.Second))
		if err != nil {
			t.Fatalf("RecordOutcome(%d) error = %v", attempt, err)
		}
		run, err = store.RecordCleanup(ctx, run.ID, drill.CleanupFailed, runStartedAt.Add(2*time.Second))
		if err != nil {
			t.Fatalf("RecordCleanup(%d) error = %v", attempt, err)
		}
		snapshotVersion := run.Version
		snapshotAt := run.UpdatedAt
		report := evidence.Report{
			SchemaVersion: evidence.SchemaVersion,
			RunID:         run.ID, PlanID: plan.ID, PlanVersion: plan.Version,
			RecoveryPoint: evidence.RecoveryPoint{ID: "snapshot-ordering", SelectedAt: runStartedAt},
			Stages:        []evidence.Stage{{Ordinal: 1, Name: drill.StageQueued, StartedAt: runStartedAt, FinishedAt: runStartedAt.Add(time.Second), Duration: time.Second}},
			Probes:        []probe.Evidence{{Ordinal: 1, ID: "never-started", Kind: probe.KindHTTP, Required: false, Status: probe.StatusNotAttempted}},
			Outcome:       run.Outcome, Cleanup: run.Cleanup,
		}
		start := make(chan struct{})
		saveResult := make(chan error, 1)
		retryResult := make(chan error, 1)
		go func() {
			<-start
			saveResult <- store.SaveReport(ctx, report, redact.Redactor{})
		}()
		go func() {
			<-start
			_, err := store.BeginCleanupRetry(ctx, run.ID, runStartedAt.Add(3*time.Second))
			retryResult <- err
		}()
		close(start)
		saveErr := <-saveResult
		retryErr := <-retryResult
		if retryErr != nil {
			t.Fatalf("BeginCleanupRetry(%d) error = %v", attempt, retryErr)
		}
		if saveErr != nil {
			if !errors.Is(saveErr, evidence.ErrInvalidReport) {
				t.Fatalf("SaveReport(%d) error = %v, want ErrInvalidReport", attempt, saveErr)
			}
			if _, err := store.Report(ctx, run.ID); !errors.Is(err, evidence.ErrReportNotFound) {
				t.Fatalf("Report(%d) after rejected capture error = %v, want ErrReportNotFound", attempt, err)
			}
			continue
		}
		view, err := store.ReportView(ctx, run.ID)
		if err != nil {
			t.Fatalf("ReportView(%d) error = %v", attempt, err)
		}
		if view.Snapshot.SnapshotSequence != snapshotVersion || !view.Snapshot.SnapshotAt.Equal(snapshotAt) || view.Snapshot.Cleanup != drill.CleanupFailed {
			t.Fatalf("ReportView(%d) snapshot = %#v, want failed cleanup at sequence %d", attempt, view.Snapshot, snapshotVersion)
		}
		if view.CurrentCleanup.Status != drill.CleanupPending || view.CurrentCleanup.AsOfSequence <= snapshotVersion || !view.CurrentCleanup.AsOf.After(snapshotAt) {
			t.Fatalf("ReportView(%d) current cleanup = %#v, want later pending state", attempt, view.CurrentCleanup)
		}
	}
}

func TestParentDeadlineRunnerEvidencePersistsAsTimedOut(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	startedAt := time.Now().UTC()
	store, err := journal.Open(ctx, filepath.Join(t.TempDir(), "rehearse.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	configuration := probe.Config{SchemaVersion: probe.SchemaVersion, Probes: []probe.Spec{
		{Ordinal: 1, ID: "parent-deadline", Kind: probe.KindHTTP, Required: true, Retry: probe.RetryPolicy{Deadline: time.Second, Backoff: 10 * time.Millisecond, MaxAttempts: 1}, HTTP: &probe.HTTPSpec{URL: "http://127.0.0.1/deadline", ExpectedStatus: http.StatusNoContent}},
	}}
	plan := drill.Plan{
		ID: "parent-deadline-plan", Name: "parent deadline", Version: 1, CreatedAt: startedAt,
		Spec: drill.PlanSpec{SourceKind: "backup-source", TargetKind: "restore-target", ProbeConfig: configuration},
	}
	if err := store.CreatePlan(ctx, plan); err != nil {
		t.Fatalf("CreatePlan() error = %v", err)
	}
	run, err := store.CreateRun(ctx, "parent-deadline-run", plan.ID, plan.Version, startedAt)
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	probeStageAt := time.Now().UTC()
	for _, stage := range []drill.Stage{drill.StagePreflight, drill.StageAcquire, drill.StageRestore, drill.StageBoot, drill.StageProbe} {
		run, err = store.Transition(ctx, run.ID, stage, probeStageAt)
		if err != nil {
			t.Fatalf("Transition(%s) error = %v", stage, err)
		}
	}
	parent, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	result := probe.NewRunner(probe.Options{HTTPClient: &http.Client{Transport: successfulJournalResponseAfterContextDoneTransport{}}}).Run(parent, configuration)
	if got := result.Probes[0].Status; got != probe.StatusTimedOut {
		t.Fatalf("Runner.Run() parent deadline status = %q, want %q", got, probe.StatusTimedOut)
	}
	if got := result.Probes[0].ExhaustedBy; got != probe.ExhaustedDeadline {
		t.Fatalf("Runner.Run() parent deadline exhausted by = %q, want %q", got, probe.ExhaustedDeadline)
	}
	outcomeAt := time.Now().UTC()
	run, err = store.RecordOutcome(ctx, run.ID, drill.OutcomeTimedOut, outcomeAt)
	if err != nil {
		t.Fatalf("RecordOutcome() error = %v", err)
	}
	run, err = store.RecordCleanup(ctx, run.ID, drill.CleanupSucceeded, outcomeAt)
	if err != nil {
		t.Fatalf("RecordCleanup() error = %v", err)
	}
	report := evidence.Report{
		SchemaVersion: evidence.SchemaVersion,
		RunID:         run.ID, PlanID: plan.ID, PlanVersion: plan.Version,
		RecoveryPoint: evidence.RecoveryPoint{ID: "snapshot-parent-deadline", SelectedAt: startedAt},
		Stages:        []evidence.Stage{{Ordinal: 1, Name: drill.StageProbe, StartedAt: probeStageAt, FinishedAt: outcomeAt, Duration: outcomeAt.Sub(probeStageAt)}},
		Probes:        result.Probes,
		Outcome:       run.Outcome, Cleanup: run.Cleanup,
	}
	if err := store.SaveReport(ctx, report, redact.Redactor{}); err != nil {
		t.Fatalf("SaveReport() error = %v", err)
	}
	loaded, err := store.Report(ctx, run.ID)
	if err != nil {
		t.Fatalf("Report() error = %v", err)
	}
	if loaded.Probes[0].Status != probe.StatusTimedOut || loaded.Probes[0].ExhaustedBy != probe.ExhaustedDeadline {
		t.Fatalf("Report() parent deadline evidence = %#v, want timed_out by deadline", loaded.Probes[0])
	}
}

type successfulJournalResponseAfterContextDoneTransport struct{}

func (successfulJournalResponseAfterContextDoneTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	<-request.Context().Done()
	return &http.Response{
		StatusCode: http.StatusNoContent,
		Header:     make(http.Header),
		Body:       http.NoBody,
		Request:    request,
	}, nil
}

func commandReportBoundaryMarker() string {
	return "xY" + strings.Repeat("z", 2<<10)
}

func assertBoundedCommandEvidence(t *testing.T, observed, marker string) {
	t.Helper()
	if strings.Contains(observed, marker) || strings.Contains(observed, "xY") {
		t.Fatalf("command Observed leaked capture-boundary marker prefix: %q", observed)
	}
	stdout, _, ok := strings.Cut(observed, "\nstderr:")
	if !ok {
		t.Fatalf("command Observed = %q, want stdout/stderr sections", observed)
	}
	if got := len(strings.TrimPrefix(stdout, "stdout:")); got > 16<<10 {
		t.Fatalf("command stdout evidence bytes = %d, want at most %d", got, 16<<10)
	}
}

func TestProbeCommandReportBoundaryHelper(t *testing.T) {
	isHelper := false
	for _, argument := range os.Args {
		if argument == "report-boundary-helper" {
			isHelper = true
			break
		}
	}
	if !isHelper {
		t.Skip("helper process only")
	}
	marker := commandReportBoundaryMarker()
	if _, err := fmt.Fprint(os.Stdout, strings.Repeat("o", (16<<10)+len(marker)-2)+marker); err != nil {
		t.Fatalf("write stdout: %v", err)
	}
}
