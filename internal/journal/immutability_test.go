package journal

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/azizu06/rehearse/internal/drill"
)

func TestSQLiteRejectsRunEventMutation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "rehearse.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, time.July, 15, 16, 0, 0, 0, time.UTC)
	plan := drill.Plan{
		ID:        "plan-1",
		Name:      "immutable journal",
		Version:   1,
		CreatedAt: now,
		Spec: drill.PlanSpec{
			SourceKind: "backup-source",
			TargetKind: "restore-target",
		},
	}
	if err := store.CreatePlan(ctx, plan); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	if _, err := store.CreateRun(ctx, "run-1", plan.ID, plan.Version, now); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	if _, err := store.database.ExecContext(ctx, "UPDATE run_events SET kind = 'run_failed' WHERE run_id = 'run-1'"); err == nil {
		t.Fatal("updating an immutable run event succeeded")
	}
	if _, err := store.database.ExecContext(ctx, "DELETE FROM run_events WHERE run_id = 'run-1'"); err == nil {
		t.Fatal("deleting an immutable run event succeeded")
	}
}

func TestLegacyInvalidUTF8IdentitiesReturnTypedErrorsWithoutMutation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "rehearse.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, time.September, 6, 3, 0, 0, 0, time.UTC)
	validPlan := drill.Plan{
		ID: "valid-plan", Name: "valid plan", Version: 1, CreatedAt: now,
		Spec: drill.PlanSpec{SourceKind: "backup-source", TargetKind: "restore-target"},
	}
	if err := store.CreatePlan(ctx, validPlan); err != nil {
		t.Fatalf("CreatePlan() error = %v", err)
	}
	legacyPlans := []struct {
		name  string
		id    []byte
		hexID string
	}{
		{name: "invalid UTF-8", id: []byte{0xff}, hexID: "FF"},
		{name: "blank", id: []byte{}, hexID: ""},
		{name: "whitespace", id: []byte(" "), hexID: "20"},
	}
	for _, legacy := range legacyPlans {
		if _, err := store.database.ExecContext(ctx, `
			INSERT INTO plans(id, name, created_at)
			VALUES (CAST(? AS TEXT), 'legacy invalid plan', ?)
		`, legacy.id, formatTime(now)); err != nil {
			t.Fatalf("seed %s plan identity: %v", legacy.name, err)
		}
		if _, err := store.database.ExecContext(ctx, `
			INSERT INTO plan_versions(plan_id, version, source_kind, target_kind, credential_references, probe_config, created_at)
			VALUES (CAST(? AS TEXT), 1, 'backup-source', 'restore-target', '[]', '', ?)
		`, legacy.id, formatTime(now)); err != nil {
			t.Fatalf("seed %s plan version identity: %v", legacy.name, err)
		}
		if _, err := store.Plan(ctx, string(legacy.id), 1); !errors.Is(err, drill.ErrInvalidIdentity) {
			t.Fatalf("Plan(%s) error = %v, want ErrInvalidIdentity", legacy.name, err)
		}
		var count int
		if err := store.database.QueryRowContext(ctx, "SELECT COUNT(*) FROM plans WHERE hex(id) = ?", legacy.hexID).Scan(&count); err != nil {
			t.Fatalf("count legacy %s plan row: %v", legacy.name, err)
		}
		if count != 1 {
			t.Fatalf("legacy %s plan rows = %d, want 1", legacy.name, count)
		}
	}
	legacyRuns := []struct {
		name  string
		id    []byte
		hexID string
	}{
		{name: "invalid UTF-8", id: []byte{0xfe}, hexID: "FE"},
		{name: "blank", id: []byte{}, hexID: ""},
		{name: "whitespace", id: []byte("\t"), hexID: "09"},
	}
	for _, legacy := range legacyRuns {
		if _, err := store.database.ExecContext(ctx, `
			INSERT INTO runs(
				id, plan_id, plan_version, stage, outcome, cleanup_status,
				created_at, updated_at, version, needs_reconciliation, reconciliation_requested_at
			) VALUES (CAST(? AS TEXT), ?, 1, 'cleanup', 'failed', 'succeeded', ?, ?, 1, 0, '')
		`, legacy.id, validPlan.ID, formatTime(now), formatTime(now)); err != nil {
			t.Fatalf("seed %s run identity: %v", legacy.name, err)
		}
		if _, err := store.Run(ctx, string(legacy.id)); !errors.Is(err, drill.ErrInvalidIdentity) {
			t.Fatalf("Run(%s) error = %v, want ErrInvalidIdentity", legacy.name, err)
		}
		var count int
		if err := store.database.QueryRowContext(ctx, "SELECT COUNT(*) FROM runs WHERE hex(id) = ?", legacy.hexID).Scan(&count); err != nil {
			t.Fatalf("count legacy %s run row: %v", legacy.name, err)
		}
		if count != 1 {
			t.Fatalf("legacy %s run rows = %d, want 1", legacy.name, count)
		}
	}
}
