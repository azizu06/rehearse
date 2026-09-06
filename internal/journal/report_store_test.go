package journal_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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

	probeResult := probe.NewRunner(probe.Options{Redactor: redactor}).Run(ctx, plan.Spec.ProbeConfig)
	if !probeResult.RequiredPassed {
		t.Fatalf("Runner.Run() required passed = false, probes = %#v", probeResult.Probes)
	}
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
	request := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run-10/report", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("report API status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	apiReport, err := evidence.ParseJSON(response.Body.Bytes())
	if err != nil {
		t.Fatalf("ParseJSON(report API) error = %v", err)
	}
	assertBoundedCommandEvidence(t, apiReport.Probes[0].Observed, boundaryMarker)
	encoded, err := loadedReport.CanonicalJSON(redactor)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	if bytes.Contains(encoded, []byte(secret)) || !bytes.Contains(encoded, []byte(redact.Replacement)) {
		t.Fatalf("loaded report redaction = %s", encoded)
	}
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
