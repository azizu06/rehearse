// Package journal persists drill plans, run projections, and immutable run
// events in SQLite.
package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/azizu06/rehearse/internal/drill"
	"github.com/azizu06/rehearse/internal/probe"
	_ "modernc.org/sqlite"
)

var (
	// ErrNotFound identifies an unknown persisted plan or run.
	ErrNotFound = errors.New("journal record not found")
	// ErrConcurrentMutation identifies a stale projection write.
	ErrConcurrentMutation = errors.New("run changed concurrently")
)

// Store owns one SQLite connection pool. A single open connection serializes
// state-machine writes while still allowing callers to invoke Store safely from
// concurrent goroutines.
type Store struct {
	database *sql.DB
}

// Open opens or creates a journal, applies embedded migrations, and marks any
// interrupted run for restart reconciliation.
func Open(ctx context.Context, path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("journal path is required")
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite journal: %w", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)

	closeWithError := func(cause error) (*Store, error) {
		_ = database.Close()
		return nil, cause
	}
	for _, pragma := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
	} {
		if _, err := database.ExecContext(ctx, pragma); err != nil {
			return closeWithError(fmt.Errorf("configure sqlite journal: %w", err))
		}
	}
	if err := applyMigrations(ctx, database); err != nil {
		return closeWithError(err)
	}
	store := &Store{database: database}
	if err := store.requireRestartReconciliation(ctx, time.Now().UTC()); err != nil {
		return closeWithError(err)
	}
	return store, nil
}

// Close closes the SQLite journal.
func (store *Store) Close() error {
	return store.database.Close()
}

// SchemaVersion returns the newest applied migration version.
func (store *Store) SchemaVersion(ctx context.Context) (int, error) {
	var version int
	if err := store.database.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return version, nil
}

// CreatePlan writes one immutable plan version after enforcing its secret-free
// domain shape.
func (store *Store) CreatePlan(ctx context.Context, plan drill.Plan) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	references, err := json.Marshal(plan.Spec.CredentialReferences)
	if err != nil {
		return fmt.Errorf("encode credential references: %w", err)
	}
	probeConfig := ""
	if !plan.Spec.ProbeConfig.IsZero() {
		encoded, err := plan.Spec.ProbeConfig.CanonicalJSON()
		if err != nil {
			return fmt.Errorf("encode probe config: %w", err)
		}
		probeConfig = string(encoded)
	}

	transaction, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin plan transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if _, err := transaction.ExecContext(
		ctx,
		`INSERT INTO plans(id, name, created_at) VALUES (?, ?, ?)
         ON CONFLICT(id) DO NOTHING`,
		plan.ID,
		plan.Name,
		formatTime(plan.CreatedAt),
	); err != nil {
		return fmt.Errorf("insert plan: %w", err)
	}
	var persistedName string
	if err := transaction.QueryRowContext(ctx, "SELECT name FROM plans WHERE id = ?", plan.ID).Scan(&persistedName); err != nil {
		return fmt.Errorf("read persisted plan identity: %w", err)
	}
	if persistedName != plan.Name {
		return fmt.Errorf("%w: plan name cannot change between versions", drill.ErrInvalidPlan)
	}
	if _, err := transaction.ExecContext(
		ctx,
		`INSERT INTO plan_versions(
			plan_id, version, source_kind, target_kind, credential_references, probe_config, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		plan.ID,
		plan.Version,
		plan.Spec.SourceKind,
		plan.Spec.TargetKind,
		string(references),
		probeConfig,
		formatTime(plan.CreatedAt),
	); err != nil {
		return fmt.Errorf("insert plan version: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit plan: %w", err)
	}
	return nil
}

// Plan loads one immutable plan version.
func (store *Store) Plan(ctx context.Context, id string, version int64) (drill.Plan, error) {
	var (
		plan        drill.Plan
		references  string
		probeConfig string
		createdAt   string
	)
	err := store.database.QueryRowContext(ctx, `
        SELECT plans.id, plans.name, plan_versions.version,
               plan_versions.source_kind, plan_versions.target_kind,
			   plan_versions.credential_references, plan_versions.probe_config,
			   plan_versions.created_at
        FROM plans
        JOIN plan_versions ON plan_versions.plan_id = plans.id
        WHERE plans.id = ? AND plan_versions.version = ?
    `, id, version).Scan(
		&plan.ID,
		&plan.Name,
		&plan.Version,
		&plan.Spec.SourceKind,
		&plan.Spec.TargetKind,
		&references,
		&probeConfig,
		&createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return drill.Plan{}, ErrNotFound
	}
	if err != nil {
		return drill.Plan{}, fmt.Errorf("read plan: %w", err)
	}
	if err := json.Unmarshal([]byte(references), &plan.Spec.CredentialReferences); err != nil {
		return drill.Plan{}, fmt.Errorf("decode credential references: %w", err)
	}
	if probeConfig != "" {
		plan.Spec.ProbeConfig, err = probe.ParseConfigBytes([]byte(probeConfig))
		if err != nil {
			return drill.Plan{}, fmt.Errorf("decode probe config: %w", err)
		}
	}
	plan.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return drill.Plan{}, fmt.Errorf("parse plan created time: %w", err)
	}
	return plan, nil
}

// CreateRun writes the queued projection and its run-created event in one
// transaction.
func (store *Store) CreateRun(ctx context.Context, id, planID string, planVersion int64, at time.Time) (drill.Run, error) {
	run, event, err := drill.NewRun(id, planID, planVersion, at)
	if err != nil {
		return drill.Run{}, err
	}
	transaction, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return drill.Run{}, fmt.Errorf("begin run transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if err := insertRun(ctx, transaction, run); err != nil {
		return drill.Run{}, err
	}
	if err := insertEvent(ctx, transaction, run.ID, event); err != nil {
		return drill.Run{}, err
	}
	if err := transaction.Commit(); err != nil {
		return drill.Run{}, fmt.Errorf("commit run: %w", err)
	}
	return run, nil
}

// Run loads the current transactional projection.
func (store *Store) Run(ctx context.Context, id string) (drill.Run, error) {
	return loadRun(ctx, store.database, id)
}

// Transition advances a run by one deterministic stage and appends its event.
func (store *Store) Transition(ctx context.Context, id string, stage drill.Stage, at time.Time) (drill.Run, error) {
	return store.mutateRun(ctx, id, func(run *drill.Run) (drill.Event, error) {
		return run.Transition(stage, at)
	})
}

// RecordOutcome persists one terminal execution outcome independently of cleanup.
func (store *Store) RecordOutcome(ctx context.Context, id string, outcome drill.Outcome, at time.Time) (drill.Run, error) {
	return store.mutateRun(ctx, id, func(run *drill.Run) (drill.Event, error) {
		return run.RecordOutcome(outcome, at)
	})
}

// RecordCleanup persists the final independent cleanup result.
func (store *Store) RecordCleanup(ctx context.Context, id string, status drill.CleanupStatus, at time.Time) (drill.Run, error) {
	return store.mutateRun(ctx, id, func(run *drill.Run) (drill.Event, error) {
		return run.RecordCleanup(status, at)
	})
}

// Events returns the immutable run history in sequence order.
func (store *Store) Events(ctx context.Context, runID string) ([]drill.Event, error) {
	rows, err := store.database.QueryContext(ctx, `
        SELECT sequence, kind, stage, outcome, cleanup_status, occurred_at
        FROM run_events
        WHERE run_id = ?
        ORDER BY sequence
    `, runID)
	if err != nil {
		return nil, fmt.Errorf("query run events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var events []drill.Event
	for rows.Next() {
		var (
			event      drill.Event
			occurredAt string
		)
		if err := rows.Scan(&event.Sequence, &event.Kind, &event.Stage, &event.Outcome, &event.Cleanup, &occurredAt); err != nil {
			return nil, fmt.Errorf("scan run event: %w", err)
		}
		event.OccurredAt, err = parseTime(occurredAt)
		if err != nil {
			return nil, fmt.Errorf("parse run event time: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate run events: %w", err)
	}
	return events, nil
}

func (store *Store) mutateRun(
	ctx context.Context,
	id string,
	mutation func(*drill.Run) (drill.Event, error),
) (drill.Run, error) {
	transaction, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return drill.Run{}, fmt.Errorf("begin run mutation: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	run, err := loadRun(ctx, transaction, id)
	if err != nil {
		return drill.Run{}, err
	}
	previousVersion := run.Version
	event, err := mutation(&run)
	if err != nil {
		return drill.Run{}, err
	}
	result, err := transaction.ExecContext(ctx, `
        UPDATE runs
        SET stage = ?, outcome = ?, cleanup_status = ?, updated_at = ?, version = ?,
            needs_reconciliation = ?, reconciliation_requested_at = ?
        WHERE id = ? AND version = ?
    `,
		run.Stage,
		run.Outcome,
		run.Cleanup,
		formatTime(run.UpdatedAt),
		run.Version,
		boolInt(run.NeedsReconciliation),
		optionalTime(run.ReconciliationRequestedAt),
		run.ID,
		previousVersion,
	)
	if err != nil {
		return drill.Run{}, fmt.Errorf("update run projection: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return drill.Run{}, fmt.Errorf("inspect run update: %w", err)
	}
	if rowsAffected != 1 {
		return drill.Run{}, ErrConcurrentMutation
	}
	if err := insertEvent(ctx, transaction, run.ID, event); err != nil {
		return drill.Run{}, err
	}
	if err := transaction.Commit(); err != nil {
		return drill.Run{}, fmt.Errorf("commit run mutation: %w", err)
	}
	return run, nil
}

func (store *Store) requireRestartReconciliation(ctx context.Context, at time.Time) error {
	transaction, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin restart reconciliation: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	rows, err := transaction.QueryContext(ctx, `
        SELECT id, plan_id, plan_version, stage, outcome, cleanup_status,
               created_at, updated_at, version, needs_reconciliation,
               reconciliation_requested_at
        FROM runs
        WHERE cleanup_status NOT IN ('succeeded', 'failed')
          AND needs_reconciliation = 0
        ORDER BY id
    `)
	if err != nil {
		return fmt.Errorf("query interrupted runs: %w", err)
	}
	var runs []drill.Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			_ = rows.Close()
			return err
		}
		runs = append(runs, run)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close interrupted run rows: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate interrupted runs: %w", err)
	}

	for index := range runs {
		run := &runs[index]
		eventAt := at
		if eventAt.Before(run.UpdatedAt) {
			eventAt = run.UpdatedAt
		}
		previousVersion := run.Version
		event, err := run.RequireReconciliation(eventAt)
		if err != nil {
			return fmt.Errorf("mark run %s for reconciliation: %w", run.ID, err)
		}
		result, err := transaction.ExecContext(ctx, `
            UPDATE runs
            SET updated_at = ?, version = ?, needs_reconciliation = 1,
                reconciliation_requested_at = ?
            WHERE id = ? AND version = ? AND needs_reconciliation = 0
        `, formatTime(run.UpdatedAt), run.Version, formatTime(run.ReconciliationRequestedAt), run.ID, previousVersion)
		if err != nil {
			return fmt.Errorf("update restart reconciliation: %w", err)
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("inspect restart reconciliation: %w", err)
		}
		if rowsAffected != 1 {
			return ErrConcurrentMutation
		}
		if err := insertEvent(ctx, transaction, run.ID, event); err != nil {
			return err
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit restart reconciliation: %w", err)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

type runQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadRun(ctx context.Context, queryer runQueryer, id string) (drill.Run, error) {
	return scanRun(queryer.QueryRowContext(ctx, `
        SELECT id, plan_id, plan_version, stage, outcome, cleanup_status,
               created_at, updated_at, version, needs_reconciliation,
               reconciliation_requested_at
        FROM runs
        WHERE id = ?
    `, id))
}

func scanRun(scanner rowScanner) (drill.Run, error) {
	var (
		run                       drill.Run
		createdAt                 string
		updatedAt                 string
		reconciliationRequestedAt string
		needsReconciliation       int
	)
	err := scanner.Scan(
		&run.ID,
		&run.PlanID,
		&run.PlanVersion,
		&run.Stage,
		&run.Outcome,
		&run.Cleanup,
		&createdAt,
		&updatedAt,
		&run.Version,
		&needsReconciliation,
		&reconciliationRequestedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return drill.Run{}, ErrNotFound
	}
	if err != nil {
		return drill.Run{}, fmt.Errorf("scan run: %w", err)
	}
	if run.CreatedAt, err = parseTime(createdAt); err != nil {
		return drill.Run{}, fmt.Errorf("parse run created time: %w", err)
	}
	if run.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return drill.Run{}, fmt.Errorf("parse run updated time: %w", err)
	}
	run.NeedsReconciliation = needsReconciliation == 1
	if reconciliationRequestedAt != "" {
		if run.ReconciliationRequestedAt, err = parseTime(reconciliationRequestedAt); err != nil {
			return drill.Run{}, fmt.Errorf("parse reconciliation time: %w", err)
		}
	}
	return run, nil
}

func insertRun(ctx context.Context, transaction *sql.Tx, run drill.Run) error {
	_, err := transaction.ExecContext(ctx, `
        INSERT INTO runs(
            id, plan_id, plan_version, stage, outcome, cleanup_status,
            created_at, updated_at, version, needs_reconciliation,
            reconciliation_requested_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `,
		run.ID,
		run.PlanID,
		run.PlanVersion,
		run.Stage,
		run.Outcome,
		run.Cleanup,
		formatTime(run.CreatedAt),
		formatTime(run.UpdatedAt),
		run.Version,
		boolInt(run.NeedsReconciliation),
		optionalTime(run.ReconciliationRequestedAt),
	)
	if err != nil {
		return fmt.Errorf("insert run: %w", err)
	}
	return nil
}

func insertEvent(ctx context.Context, transaction *sql.Tx, runID string, event drill.Event) error {
	_, err := transaction.ExecContext(ctx, `
        INSERT INTO run_events(
            run_id, sequence, kind, stage, outcome, cleanup_status, occurred_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?)
    `,
		runID,
		event.Sequence,
		event.Kind,
		event.Stage,
		event.Outcome,
		event.Cleanup,
		formatTime(event.OccurredAt),
	)
	if err != nil {
		return fmt.Errorf("insert run event: %w", err)
	}
	return nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func optionalTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return formatTime(value)
}

func parseTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, value)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
