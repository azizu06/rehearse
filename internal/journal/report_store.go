package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/azizu06/rehearse/internal/drill"
	"github.com/azizu06/rehearse/internal/evidence"
	"github.com/azizu06/rehearse/internal/probe"
	"github.com/azizu06/rehearse/internal/redact"
)

// SaveReport writes one canonical, redacted final report. Reports are immutable
// and capture one terminal run projection already in the journal.
func (store *Store) SaveReport(ctx context.Context, report evidence.Report, redactor redact.Redactor) error {
	transaction, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin report transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	encoded, err := report.CanonicalJSON(redactor)
	if err != nil {
		return err
	}
	sanitized, err := evidence.ParseJSON(encoded)
	if err != nil {
		return err
	}
	run, err := loadRun(ctx, transaction, sanitized.RunID)
	if err != nil {
		return err
	}
	if !run.Terminal() || run.PlanID != sanitized.PlanID || run.PlanVersion != sanitized.PlanVersion || run.Outcome != sanitized.Outcome || run.Cleanup != sanitized.Cleanup {
		return fmt.Errorf("%w: report disagrees with terminal run", evidence.ErrInvalidReport)
	}
	sanitized.SnapshotSequence = run.Version
	sanitized.SnapshotAt = run.UpdatedAt
	encoded, err = sanitized.CanonicalJSON(redact.Redactor{})
	if err != nil {
		return err
	}
	sanitized, err = evidence.ParseJSON(encoded)
	if err != nil {
		return err
	}
	events, err := loadEvents(ctx, transaction, run.ID)
	if err != nil {
		return err
	}
	if err := validateReportHistory(events, sanitized); err != nil {
		return err
	}
	plan, err := loadPlan(ctx, transaction, run.PlanID, run.PlanVersion)
	if err != nil {
		return err
	}
	if err := validateProbeEvidence(plan.Spec.ProbeConfig, sanitized.Probes); err != nil {
		return err
	}
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO run_reports(run_id, schema_version, document, created_at)
		VALUES (?, ?, ?, ?)
	`, sanitized.RunID, sanitized.SchemaVersion, string(encoded), formatTime(sanitized.SnapshotAt)); err != nil {
		return fmt.Errorf("insert run report: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit report: %w", err)
	}
	return nil
}

type stageBounds struct {
	startedAt  time.Time
	finishedAt time.Time
}

func validateReportHistory(events []drill.Event, report evidence.Report) error {
	bounds := make(map[drill.Stage]stageBounds)
	var active drill.Stage
	for _, event := range events {
		switch event.Kind {
		case drill.EventRunCreated:
			active = drill.StageQueued
			bounds[active] = stageBounds{startedAt: event.OccurredAt}
		case drill.EventStageStarted:
			if active != "" {
				current := bounds[active]
				current.finishedAt = event.OccurredAt
				bounds[active] = current
			}
			active = event.Stage
			bounds[active] = stageBounds{startedAt: event.OccurredAt}
		case drill.EventRunSucceeded, drill.EventRunFailed, drill.EventRunCancelled, drill.EventRunTimedOut:
			if active != "" {
				current := bounds[active]
				current.finishedAt = event.OccurredAt
				bounds[active] = current
			}
			active = drill.StageCleanup
			bounds[active] = stageBounds{startedAt: event.OccurredAt}
		case drill.EventCleanupSucceeded, drill.EventCleanupFailed:
			current := bounds[drill.StageCleanup]
			current.finishedAt = event.OccurredAt
			bounds[drill.StageCleanup] = current
		}
	}
	for _, stage := range report.Stages {
		known, ok := bounds[stage.Name]
		if !ok || known.finishedAt.IsZero() || !stage.StartedAt.Equal(known.startedAt) || !stage.FinishedAt.Equal(known.finishedAt) {
			return fmt.Errorf("%w: report stage contradicts run history", evidence.ErrInvalidReport)
		}
	}
	probeStage, reachedProbe := bounds[drill.StageProbe]
	for _, item := range report.Probes {
		if item.Status == probe.StatusNotAttempted {
			continue
		}
		if !reachedProbe || probeStage.finishedAt.IsZero() || item.StartedAt.Before(probeStage.startedAt) || item.FinishedAt.After(probeStage.finishedAt) {
			return fmt.Errorf("%w: attempted probe contradicts run history", evidence.ErrInvalidReport)
		}
	}
	return nil
}

func validateProbeEvidence(config probe.Config, items []probe.Evidence) error {
	if config.IsZero() || len(config.Probes) != len(items) {
		return fmt.Errorf("%w: report probe evidence disagrees with plan", evidence.ErrInvalidReport)
	}
	declared := append([]probe.Spec(nil), config.Probes...)
	sort.Slice(declared, func(left, right int) bool { return declared[left].Ordinal < declared[right].Ordinal })
	for index, item := range items {
		spec := declared[index]
		if item.Ordinal != spec.Ordinal || item.ID != spec.ID || item.Kind != spec.Kind || item.Required != spec.Required || item.Attempts > spec.Retry.MaxAttempts {
			return fmt.Errorf("%w: report probe evidence disagrees with plan", evidence.ErrInvalidReport)
		}
		switch item.Status {
		case probe.StatusPassed, probe.StatusCancelled:
			if item.ExhaustedBy != "" {
				return fmt.Errorf("%w: report probe exhaustion disagrees with status", evidence.ErrInvalidReport)
			}
		case probe.StatusNotAttempted:
			if item.Attempts != 0 || item.ExhaustedBy != "" {
				return fmt.Errorf("%w: report probe execution disagrees with status", evidence.ErrInvalidReport)
			}
		case probe.StatusFailed:
			if item.ExhaustedBy != probe.ExhaustedAttempts || item.Attempts != spec.Retry.MaxAttempts {
				return fmt.Errorf("%w: report probe exhaustion disagrees with status", evidence.ErrInvalidReport)
			}
		case probe.StatusTimedOut:
			if item.ExhaustedBy != probe.ExhaustedDeadline {
				return fmt.Errorf("%w: report probe exhaustion disagrees with status", evidence.ErrInvalidReport)
			}
		}
	}
	return nil
}

// Report loads one typed immutable report.
func (store *Store) Report(ctx context.Context, runID string) (evidence.Report, error) {
	return loadReport(ctx, store.database, runID)
}

func loadReport(ctx context.Context, queryer rowQueryer, runID string) (evidence.Report, error) {
	var (
		schemaVersion string
		document      string
	)
	err := queryer.QueryRowContext(ctx, `
		SELECT schema_version, document FROM run_reports WHERE run_id = ?
	`, runID).Scan(&schemaVersion, &document)
	if errors.Is(err, sql.ErrNoRows) {
		return evidence.Report{}, evidence.ErrReportNotFound
	}
	if err != nil {
		return evidence.Report{}, fmt.Errorf("read run report: %w", err)
	}
	if schemaVersion != evidence.SchemaVersion {
		return evidence.Report{}, fmt.Errorf("decode run report: %w: unsupported schema version", evidence.ErrInvalidReport)
	}
	report, err := evidence.ParseJSON([]byte(document))
	if err != nil {
		return evidence.Report{}, fmt.Errorf("decode run report: %w", err)
	}
	if report.SchemaVersion != schemaVersion {
		return evidence.Report{}, fmt.Errorf("decode run report: %w: schema column mismatch", evidence.ErrInvalidReport)
	}
	return report, nil
}

func (store *Store) ReportView(ctx context.Context, runID string) (evidence.ReportView, error) {
	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return evidence.ReportView{}, fmt.Errorf("begin report view transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	snapshot, err := loadReport(ctx, transaction, runID)
	if err != nil {
		return evidence.ReportView{}, err
	}
	run, err := loadRun(ctx, transaction, runID)
	if err != nil {
		return evidence.ReportView{}, err
	}
	view := evidence.ReportView{
		Snapshot: snapshot,
		CurrentCleanup: evidence.CurrentCleanup{
			Status:       run.Cleanup,
			AsOfSequence: run.Version,
			AsOf:         run.UpdatedAt,
		},
	}
	if _, err := view.CanonicalJSON(redact.Redactor{}); err != nil {
		return evidence.ReportView{}, err
	}
	if err := transaction.Commit(); err != nil {
		return evidence.ReportView{}, fmt.Errorf("commit report view: %w", err)
	}
	return view, nil
}
