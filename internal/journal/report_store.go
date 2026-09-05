package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"github.com/azizu06/rehearse/internal/evidence"
	"github.com/azizu06/rehearse/internal/probe"
	"github.com/azizu06/rehearse/internal/redact"
)

// SaveReport writes one canonical, redacted final report. Reports are immutable
// and must agree with the terminal run projection already in the journal.
func (store *Store) SaveReport(ctx context.Context, report evidence.Report, redactor redact.Redactor) error {
	encoded, err := report.CanonicalJSON(redactor)
	if err != nil {
		return err
	}
	sanitized, err := evidence.ParseJSON(encoded)
	if err != nil {
		return err
	}
	run, err := store.Run(ctx, sanitized.RunID)
	if err != nil {
		return err
	}
	if !run.Terminal() || run.PlanID != sanitized.PlanID || run.PlanVersion != sanitized.PlanVersion || run.Outcome != sanitized.Outcome || run.Cleanup != sanitized.Cleanup {
		return fmt.Errorf("%w: report disagrees with terminal run", evidence.ErrInvalidReport)
	}
	plan, err := store.Plan(ctx, run.PlanID, run.PlanVersion)
	if err != nil {
		return err
	}
	if err := validateProbeEvidence(plan.Spec.ProbeConfig, sanitized.Probes); err != nil {
		return err
	}
	if _, err := store.database.ExecContext(ctx, `
		INSERT INTO run_reports(run_id, schema_version, document, created_at)
		VALUES (?, ?, ?, ?)
	`, sanitized.RunID, sanitized.SchemaVersion, string(encoded), formatTime(run.UpdatedAt)); err != nil {
		return fmt.Errorf("insert run report: %w", err)
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
		if item.Ordinal != spec.Ordinal || item.ID != spec.ID || item.Kind != spec.Kind || item.Required != spec.Required {
			return fmt.Errorf("%w: report probe evidence disagrees with plan", evidence.ErrInvalidReport)
		}
	}
	return nil
}

// Report loads one typed immutable report.
func (store *Store) Report(ctx context.Context, runID string) (evidence.Report, error) {
	var (
		schemaVersion string
		document      string
	)
	err := store.database.QueryRowContext(ctx, `
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
