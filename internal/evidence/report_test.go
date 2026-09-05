package evidence_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/azizu06/rehearse/internal/drill"
	"github.com/azizu06/rehearse/internal/evidence"
	"github.com/azizu06/rehearse/internal/probe"
	"github.com/azizu06/rehearse/internal/redact"
)

func TestReportRejectsSuccessfulOutcomeWithRequiredProbeFailure(t *testing.T) {
	t.Parallel()

	report := validReport()
	report.Probes[0].Status = probe.StatusFailed
	if err := report.Validate(); !errors.Is(err, evidence.ErrInvalidReport) {
		t.Fatalf("Validate error = %v, want ErrInvalidReport", err)
	}

	report.Probes[0].Required = false
	if err := report.Validate(); err != nil {
		t.Fatalf("optional failure changed successful report truth: %v", err)
	}
}

func TestCanonicalReportPreservesContiguousSemanticOrder(t *testing.T) {
	t.Parallel()

	report := validReport()
	report.Stages = []evidence.Stage{
		{Ordinal: 2, Name: drill.StageReport, StartedAt: report.Stages[0].FinishedAt, FinishedAt: report.Stages[0].FinishedAt.Add(time.Second), Duration: time.Second},
		report.Stages[0],
	}
	secondProbe := report.Probes[0]
	secondProbe.Ordinal = 2
	secondProbe.ID = "z-declared-second"
	report.Probes[0].ID = "a-declared-first"
	report.Probes = []probe.Evidence{secondProbe, report.Probes[0]}
	encoded, err := report.CanonicalJSON(redact.Redactor{})
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	if bytes.Index(encoded, []byte("a-declared-first")) > bytes.Index(encoded, []byte("z-declared-second")) {
		t.Fatalf("canonical report changed declared order: %s", encoded)
	}

	report.Probes[0].Ordinal = 3
	if err := report.Validate(); !errors.Is(err, evidence.ErrInvalidReport) {
		t.Fatalf("gapped probe ordinal error = %v, want ErrInvalidReport", err)
	}

	report = validReport()
	report.Stages = []evidence.Stage{
		{Ordinal: 1, Name: drill.StageReport, StartedAt: report.Stages[0].StartedAt, FinishedAt: report.Stages[0].FinishedAt, Duration: time.Second},
		{Ordinal: 2, Name: drill.StageProbe, StartedAt: report.Stages[0].FinishedAt, FinishedAt: report.Stages[0].FinishedAt.Add(time.Second), Duration: time.Second},
	}
	if err := report.Validate(); !errors.Is(err, evidence.ErrInvalidReport) {
		t.Fatalf("contradictory stage order error = %v, want ErrInvalidReport", err)
	}
}

func TestParseJSONAcceptsMaximumBoundedProbeEvidence(t *testing.T) {
	t.Parallel()

	report := validReport()
	report.Probes = make([]probe.Evidence, 64)
	for index := range report.Probes {
		report.Probes[index] = probe.Evidence{
			Ordinal: index + 1, ID: fmt.Sprintf("probe-%d", index+1), Kind: probe.KindCommand,
			Required: true, Status: probe.StatusPassed, Attempts: 1,
			StartedAt: report.Stages[0].StartedAt, FinishedAt: report.Stages[0].FinishedAt, Duration: time.Second,
			Observed: strings.Repeat("\x00", 33<<10), Detail: strings.Repeat("\x00", 16<<10),
		}
	}
	encoded, err := report.CanonicalJSON(redact.Redactor{})
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	if _, err := evidence.ParseJSON(encoded); err != nil {
		t.Fatalf("ParseJSON rejected valid bounded report: %v", err)
	}
}

func validReport() evidence.Report {
	startedAt := time.Date(2026, time.July, 15, 22, 0, 0, 0, time.UTC)
	return evidence.Report{
		SchemaVersion: evidence.SchemaVersion,
		RunID:         "run-10", PlanID: "plan-10", PlanVersion: 1,
		RecoveryPoint: evidence.RecoveryPoint{ID: "snapshot-10", SelectedAt: startedAt},
		Stages:        []evidence.Stage{{Ordinal: 1, Name: drill.StageProbe, StartedAt: startedAt, FinishedAt: startedAt.Add(time.Second), Duration: time.Second}},
		Probes:        []probe.Evidence{{Ordinal: 1, ID: "required-health", Kind: probe.KindHTTP, Required: true, Status: probe.StatusPassed, Attempts: 1, StartedAt: startedAt, FinishedAt: startedAt.Add(time.Second), Duration: time.Second}},
		Outcome:       drill.OutcomeSucceeded, Cleanup: drill.CleanupSucceeded,
	}
}
