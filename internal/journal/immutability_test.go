package journal

import (
	"context"
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
