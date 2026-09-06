package journal

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/azizu06/rehearse/internal/drill"
	"github.com/azizu06/rehearse/internal/evidence"
	"github.com/azizu06/rehearse/internal/probe"
	"github.com/azizu06/rehearse/internal/redact"
)

func TestApplyMigrationsRejectsInvalidVersionSequenceBeforeApplyingSQL(t *testing.T) {
	testCases := []struct {
		name           string
		currentVersion int
		files          fstest.MapFS
	}{
		{
			name:           "duplicate version",
			currentVersion: 1,
			files: fstest.MapFS{
				"migrations/0001_first.sql":     {Data: []byte("CREATE TABLE migration_probe (id INTEGER);")},
				"migrations/0001_duplicate.sql": {Data: []byte("CREATE TABLE duplicate_probe (id INTEGER);")},
			},
		},
		{
			name:           "non-increasing version",
			currentVersion: 0,
			files: fstest.MapFS{
				"migrations/0000_zero.sql": {Data: []byte("CREATE TABLE migration_probe (id INTEGER);")},
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			database, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatalf("open SQLite database: %v", err)
			}
			database.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = database.Close() })

			if err := applyMigrationsFromFS(context.Background(), database, testCase.files, testCase.currentVersion); err == nil {
				t.Fatal("applying an invalid migration version sequence succeeded")
			}

			for _, table := range []string{"schema_migrations", "migration_probe", "duplicate_probe"} {
				var count int
				if err := database.QueryRow(
					"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?",
					table,
				).Scan(&count); err != nil {
					t.Fatalf("query table %q: %v", table, err)
				}
				if count != 0 {
					t.Fatalf("table %q exists after migration validation failed", table)
				}
			}
		})
	}
}

func TestPublishedSchemasUpgradeToReportsAndRemainIdempotent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		version    int
		migrations []string
		sandbox    bool
	}{
		{name: "schema 1", version: 1, migrations: []string{"migrations/0001_initial.sql"}},
		{name: "sandbox schema 2", version: 2, migrations: []string{"migrations/0001_initial.sql", "migrations/0002_sandbox_cleanup_claims.sql"}, sandbox: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "rehearse.db")
			database, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatalf("open fixture database: %v", err)
			}
			database.SetMaxOpenConns(1)
			if err := applyMigrationsFromFS(ctx, database, migrationFixture(t, test.migrations...), test.version); err != nil {
				t.Fatalf("apply published schema %d: %v", test.version, err)
			}
			runID := "upgrade-run-" + strings.ReplaceAll(test.name, " ", "-")
			startedAt := time.Date(2026, time.September, 6, 4, 0, 0, 0, time.UTC)
			seedPublishedRun(t, database, runID, startedAt)
			if test.sandbox {
				if _, err := database.ExecContext(ctx, `
					INSERT INTO sandbox_cleanup_claims(run_id, claim_id, status, claimed_at, updated_at)
					VALUES (?, ?, 'failed', ?, ?)
				`, runID, strings.Repeat("a", 64), formatTime(startedAt), formatTime(startedAt)); err != nil {
					t.Fatalf("seed sandbox cleanup claim: %v", err)
				}
			}
			if err := database.Close(); err != nil {
				t.Fatalf("close fixture database: %v", err)
			}

			store, err := Open(ctx, path)
			if err != nil {
				t.Fatalf("upgrade schema %d: %v", test.version, err)
			}
			report := evidence.Report{
				SchemaVersion: evidence.SchemaVersion,
				RunID:         runID, PlanID: "upgrade-plan", PlanVersion: 1,
				RecoveryPoint: evidence.RecoveryPoint{ID: "upgrade-snapshot", SelectedAt: startedAt},
				Stages:        []evidence.Stage{{Ordinal: 1, Name: drill.StageQueued, StartedAt: startedAt, FinishedAt: startedAt.Add(time.Second), Duration: time.Second}},
				Probes:        []probe.Evidence{{Ordinal: 1, ID: "upgrade-probe", Kind: probe.KindHTTP, Required: false, Status: probe.StatusNotAttempted}},
				Outcome:       drill.OutcomeFailed, Cleanup: drill.CleanupSucceeded,
			}
			encoded, err := report.CanonicalJSON(redact.Redactor{})
			if err != nil {
				t.Fatalf("encode report fixture: %v", err)
			}
			if _, err := store.database.ExecContext(ctx, `
				INSERT INTO run_reports(run_id, schema_version, document, created_at)
				VALUES (?, ?, ?, ?)
			`, runID, evidence.SchemaVersion, string(encoded), formatTime(startedAt.Add(time.Second))); err != nil {
				t.Fatalf("seed report after upgrade: %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatalf("close upgraded store: %v", err)
			}

			for attempt := 1; attempt <= 2; attempt++ {
				reopened, err := Open(ctx, path)
				if err != nil {
					t.Fatalf("reopen attempt %d: %v", attempt, err)
				}
				version, err := reopened.SchemaVersion(ctx)
				if err != nil || version != CurrentSchemaVersion {
					t.Fatalf("schema version on attempt %d = %d, error %v, want %d", attempt, version, err, CurrentSchemaVersion)
				}
				loaded, err := reopened.Report(ctx, runID)
				if err != nil || loaded.RunID != runID {
					t.Fatalf("report on attempt %d = %#v, error %v", attempt, loaded, err)
				}
				if test.sandbox {
					claimID, _, active, err := reopened.SandboxCleanupClaim(ctx, runID)
					if err != nil || !active || claimID != strings.Repeat("a", 64) {
						t.Fatalf("sandbox claim on attempt %d active=%t id=%q error=%v", attempt, active, claimID, err)
					}
				}
				if err := reopened.Close(); err != nil {
					t.Fatalf("close attempt %d: %v", attempt, err)
				}
			}
		})
	}
}

func TestLegacyProbeSchemaTwoFailsWithMigrationMismatch(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rehearse.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	database.SetMaxOpenConns(1)
	fixture := migrationFixture(t, "migrations/0001_initial.sql")
	reportMigration, err := fs.ReadFile(migrationFiles, "migrations/0003_probe_reports.sql")
	if err != nil {
		t.Fatalf("read report migration: %v", err)
	}
	fixture["migrations/0002_probe_reports.sql"] = &fstest.MapFile{Data: reportMigration}
	if err := applyMigrationsFromFS(ctx, database, fixture, 2); err != nil {
		t.Fatalf("apply legacy probe schema: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close fixture database: %v", err)
	}

	store, err := Open(ctx, path)
	if store != nil {
		_ = store.Close()
	}
	if !errors.Is(err, ErrMigrationMismatch) || !strings.Contains(err.Error(), "migration ledger mismatch") {
		t.Fatalf("Open() error = %v, want explicit migration ledger mismatch", err)
	}
	database, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen fixture database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	var (
		versionTwoName string
		versionThree   int
		sandboxTable   int
	)
	if err := database.QueryRowContext(ctx, "SELECT name FROM schema_migrations WHERE version = 2").Scan(&versionTwoName); err != nil {
		t.Fatalf("read version 2 ledger: %v", err)
	}
	if err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = 3").Scan(&versionThree); err != nil {
		t.Fatalf("count version 3 ledger: %v", err)
	}
	if err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'sandbox_cleanup_claims'").Scan(&sandboxTable); err != nil {
		t.Fatalf("count sandbox table: %v", err)
	}
	if versionTwoName != "probe_reports" || versionThree != 0 || sandboxTable != 0 {
		t.Fatalf("legacy mismatch mutated ledger or schema: version2=%q version3=%d sandbox=%d", versionTwoName, versionThree, sandboxTable)
	}
}

func migrationFixture(t *testing.T, names ...string) fstest.MapFS {
	t.Helper()
	fixture := make(fstest.MapFS, len(names))
	for _, name := range names {
		contents, err := fs.ReadFile(migrationFiles, name)
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		fixture[name] = &fstest.MapFile{Data: contents}
	}
	return fixture
}

func seedPublishedRun(t *testing.T, database *sql.DB, runID string, startedAt time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, err := database.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("enable fixture foreign keys: %v", err)
	}
	if _, err := database.ExecContext(ctx, "INSERT INTO plans(id, name, created_at) VALUES ('upgrade-plan', 'upgrade plan', ?)", formatTime(startedAt)); err != nil {
		t.Fatalf("seed plan: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO plan_versions(plan_id, version, source_kind, target_kind, credential_references, created_at)
		VALUES ('upgrade-plan', 1, 'backup-source', 'restore-target', '[]', ?)
	`, formatTime(startedAt)); err != nil {
		t.Fatalf("seed plan version: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO runs(
			id, plan_id, plan_version, stage, outcome, cleanup_status,
			created_at, updated_at, version, needs_reconciliation, reconciliation_requested_at
		) VALUES (?, 'upgrade-plan', 1, 'cleanup', 'failed', 'succeeded', ?, ?, 1, 0, '')
	`, runID, formatTime(startedAt), formatTime(startedAt.Add(time.Second))); err != nil {
		t.Fatalf("seed run: %v", err)
	}
}
