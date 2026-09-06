// Package evidence defines deterministic, versioned recovery evidence reports.
package evidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/azizu06/rehearse/internal/drill"
	"github.com/azizu06/rehearse/internal/probe"
	"github.com/azizu06/rehearse/internal/redact"
)

const (
	SchemaVersion      = "rehearse.report/v1"
	maxReportBytes     = 20 << 20
	maxReportViewBytes = maxReportBytes + (1 << 10)
)

var ErrInvalidReport = errors.New("invalid evidence report")

var ErrReportNotFound = errors.New("evidence report not found")

var probeIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

type RecoveryPoint struct {
	ID         string    `json:"id"`
	SelectedAt time.Time `json:"selected_at"`
}

type Stage struct {
	Ordinal    int           `json:"ordinal"`
	Name       drill.Stage   `json:"name"`
	StartedAt  time.Time     `json:"started_at"`
	FinishedAt time.Time     `json:"finished_at"`
	Duration   time.Duration `json:"duration_ns"`
}

// Report is one immutable execution and cleanup truth document.
type Report struct {
	SchemaVersion    string              `json:"schema_version"`
	SnapshotSequence int64               `json:"snapshot_sequence,omitempty"`
	SnapshotAt       time.Time           `json:"snapshot_at,omitzero"`
	RunID            string              `json:"run_id"`
	PlanID           string              `json:"plan_id"`
	PlanVersion      int64               `json:"plan_version"`
	RecoveryPoint    RecoveryPoint       `json:"recovery_point"`
	Stages           []Stage             `json:"stages"`
	Probes           []probe.Evidence    `json:"probes"`
	Outcome          drill.Outcome       `json:"outcome"`
	Cleanup          drill.CleanupStatus `json:"cleanup"`
}

func (report Report) Validate() error {
	if err := report.validateIdentities(); err != nil {
		return err
	}
	if report.SchemaVersion != SchemaVersion || strings.TrimSpace(report.RunID) == "" || len(report.RunID) > maxReportBytes || strings.TrimSpace(report.PlanID) == "" || len(report.PlanID) > maxReportBytes || report.PlanVersion < 1 || report.RecoveryPoint.ID == "" || len(report.RecoveryPoint.ID) > 512 || report.RecoveryPoint.SelectedAt.IsZero() {
		return ErrInvalidReport
	}
	if report.SnapshotSequence < 0 || (report.SnapshotSequence == 0) != report.SnapshotAt.IsZero() {
		return ErrInvalidReport
	}
	if !validOutcome(report.Outcome) || (report.Cleanup != drill.CleanupSucceeded && report.Cleanup != drill.CleanupFailed) {
		return ErrInvalidReport
	}
	if len(report.Stages) == 0 || len(report.Stages) > 16 || len(report.Probes) == 0 || len(report.Probes) > 64 {
		return ErrInvalidReport
	}
	previousStageRank := -1
	for index, stage := range sortedStages(report.Stages) {
		if stage.Ordinal != index+1 || !validStage(stage.Name) || stage.StartedAt.IsZero() || stage.FinishedAt.Before(stage.StartedAt) || stage.Duration != stage.FinishedAt.Sub(stage.StartedAt) {
			return fmt.Errorf("%w: invalid stage evidence", ErrInvalidReport)
		}
		rank := stageRank(stage.Name)
		if rank <= previousStageRank {
			return fmt.Errorf("%w: stage ordinals contradict execution order", ErrInvalidReport)
		}
		previousStageRank = rank
	}
	for index, item := range sortedProbes(report.Probes) {
		if item.Ordinal != index+1 || !probeIDPattern.MatchString(item.ID) || item.Attempts > 100 || !validProbeKind(item.Kind) || !validProbeStatus(item.Status) || len(item.Observed) > 33<<10 || len(item.Detail) > 16<<10 {
			return fmt.Errorf("%w: invalid probe evidence", ErrInvalidReport)
		}
		if item.Status == probe.StatusNotAttempted {
			if item.Attempts != 0 || !item.StartedAt.IsZero() || !item.FinishedAt.IsZero() || item.Duration != 0 || item.Observed != "" || item.Detail != "" || item.Truncated || item.StdoutTruncated || item.StderrTruncated || item.TrustedHostCommand || item.ExhaustedBy != "" {
				return fmt.Errorf("%w: invalid unattempted probe evidence", ErrInvalidReport)
			}
		} else if item.Attempts < 1 || item.StartedAt.IsZero() || item.FinishedAt.Before(item.StartedAt) || item.Duration != item.FinishedAt.Sub(item.StartedAt) {
			return fmt.Errorf("%w: invalid executed probe evidence", ErrInvalidReport)
		}
		if item.Required && item.Status != probe.StatusPassed && report.Outcome == drill.OutcomeSucceeded {
			return fmt.Errorf("%w: successful outcome contains a required probe failure", ErrInvalidReport)
		}
	}
	return nil
}

func (report Report) validateIdentities() error {
	if !utf8.ValidString(report.RunID) || strings.TrimSpace(report.RunID) == "" ||
		!utf8.ValidString(report.PlanID) || strings.TrimSpace(report.PlanID) == "" ||
		!utf8.ValidString(report.RecoveryPoint.ID) || strings.TrimSpace(report.RecoveryPoint.ID) == "" {
		return fmt.Errorf("%w: %w", ErrInvalidReport, drill.ErrInvalidIdentity)
	}
	return nil
}

// CanonicalJSON returns stable bytes in semantic stage/probe ordinal order.
func (report Report) CanonicalJSON(redactor redact.Redactor) ([]byte, error) {
	if err := report.validateIdentities(); err != nil {
		return nil, err
	}
	report.RecoveryPoint.SelectedAt = report.RecoveryPoint.SelectedAt.UTC()
	report.SnapshotAt = report.SnapshotAt.UTC()
	report.RecoveryPoint.ID = redactor.String(report.RecoveryPoint.ID)
	report.Stages = sortedStages(report.Stages)
	report.Probes = sortedProbes(report.Probes)
	for index := range report.Stages {
		report.Stages[index].StartedAt = report.Stages[index].StartedAt.UTC()
		report.Stages[index].FinishedAt = report.Stages[index].FinishedAt.UTC()
	}
	for index := range report.Probes {
		report.Probes[index].StartedAt = report.Probes[index].StartedAt.UTC()
		report.Probes[index].FinishedAt = report.Probes[index].FinishedAt.UTC()
		report.Probes[index].Observed = redactor.String(report.Probes[index].Observed)
		report.Probes[index].Detail = redactor.String(report.Probes[index].Detail)
	}
	if err := report.Validate(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxReportBytes {
		return nil, ErrInvalidReport
	}
	return encoded, nil
}

type CurrentCleanup struct {
	Status       drill.CleanupStatus `json:"status"`
	AsOfSequence int64               `json:"as_of_sequence"`
	AsOf         time.Time           `json:"as_of"`
}

type ReportView struct {
	Snapshot       Report         `json:"snapshot"`
	CurrentCleanup CurrentCleanup `json:"current_cleanup"`
}

func (view ReportView) CanonicalJSON(redactor redact.Redactor) ([]byte, error) {
	snapshot, err := view.Snapshot.CanonicalJSON(redactor)
	if err != nil {
		return nil, err
	}
	view.Snapshot, err = ParseJSON(snapshot)
	if err != nil {
		return nil, err
	}
	view.CurrentCleanup.AsOf = view.CurrentCleanup.AsOf.UTC()
	if view.Snapshot.SnapshotSequence < 1 || view.Snapshot.SnapshotAt.IsZero() ||
		(view.CurrentCleanup.Status != drill.CleanupPending && view.CurrentCleanup.Status != drill.CleanupSucceeded && view.CurrentCleanup.Status != drill.CleanupFailed) ||
		view.CurrentCleanup.AsOfSequence < view.Snapshot.SnapshotSequence ||
		view.CurrentCleanup.AsOf.Before(view.Snapshot.SnapshotAt) {
		return nil, ErrInvalidReport
	}
	if view.CurrentCleanup.AsOfSequence == view.Snapshot.SnapshotSequence &&
		(view.CurrentCleanup.Status != view.Snapshot.Cleanup || !view.CurrentCleanup.AsOf.Equal(view.Snapshot.SnapshotAt)) {
		return nil, ErrInvalidReport
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxReportViewBytes {
		return nil, ErrInvalidReport
	}
	return encoded, nil
}

func validOutcome(outcome drill.Outcome) bool {
	switch outcome {
	case drill.OutcomeSucceeded, drill.OutcomeFailed, drill.OutcomeCancelled, drill.OutcomeTimedOut:
		return true
	default:
		return false
	}
}

func validStage(stage drill.Stage) bool {
	switch stage {
	case drill.StageQueued, drill.StagePreflight, drill.StageAcquire, drill.StageRestore, drill.StageBoot, drill.StageProbe, drill.StageReport, drill.StageCleanup:
		return true
	default:
		return false
	}
}

func stageRank(stage drill.Stage) int {
	switch stage {
	case drill.StageQueued:
		return 0
	case drill.StagePreflight:
		return 1
	case drill.StageAcquire:
		return 2
	case drill.StageRestore:
		return 3
	case drill.StageBoot:
		return 4
	case drill.StageProbe:
		return 5
	case drill.StageReport:
		return 6
	case drill.StageCleanup:
		return 7
	default:
		return -1
	}
}

func validProbeKind(kind probe.Kind) bool {
	switch kind {
	case probe.KindHTTP, probe.KindTCP, probe.KindCommand, probe.KindSQL, probe.KindData:
		return true
	default:
		return false
	}
}

func validProbeStatus(status probe.Status) bool {
	switch status {
	case probe.StatusPassed, probe.StatusFailed, probe.StatusTimedOut, probe.StatusCancelled, probe.StatusNotAttempted:
		return true
	default:
		return false
	}
}

// ParseJSON strictly decodes one persisted report and revalidates its schema.
func ParseJSON(contents []byte) (Report, error) {
	if len(contents) == 0 || len(contents) > maxReportBytes {
		return Report{}, ErrInvalidReport
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var report Report
	if err := decoder.Decode(&report); err != nil {
		return Report{}, fmt.Errorf("%w: decode: %v", ErrInvalidReport, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Report{}, fmt.Errorf("%w: trailing JSON", ErrInvalidReport)
	}
	if err := report.Validate(); err != nil {
		return Report{}, err
	}
	return report, nil
}

func sortedStages(values []Stage) []Stage {
	result := append([]Stage(nil), values...)
	sort.SliceStable(result, func(left, right int) bool { return result[left].Ordinal < result[right].Ordinal })
	return result
}

func sortedProbes(values []probe.Evidence) []probe.Evidence {
	result := append([]probe.Evidence(nil), values...)
	sort.SliceStable(result, func(left, right int) bool { return result[left].Ordinal < result[right].Ordinal })
	return result
}
