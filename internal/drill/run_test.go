package drill_test

import (
	"errors"
	"testing"
	"time"

	"github.com/azizu06/rehearse/internal/drill"
)

func TestRunRejectsInvalidTransitions(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 15, 16, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		from drill.Stage
		to   drill.Stage
	}{
		{name: "queued cannot skip preflight", from: drill.StageQueued, to: drill.StageAcquire},
		{name: "preflight cannot move backwards", from: drill.StagePreflight, to: drill.StageQueued},
		{name: "acquire cannot skip restore", from: drill.StageAcquire, to: drill.StageBoot},
		{name: "restore cannot repeat", from: drill.StageRestore, to: drill.StageRestore},
		{name: "boot cannot skip probes", from: drill.StageBoot, to: drill.StageReport},
		{name: "probe cannot move backwards", from: drill.StageProbe, to: drill.StageBoot},
		{name: "report cannot transition directly to cleanup", from: drill.StageReport, to: drill.StageCleanup},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			run := runAtStage(t, test.from, now)
			_, err := run.Transition(test.to, now.Add(time.Hour))
			if !errors.Is(err, drill.ErrInvalidTransition) {
				t.Fatalf("Transition(%s -> %s) error = %v, want ErrInvalidTransition", test.from, test.to, err)
			}
		})
	}
}

func TestRunAcceptsOnlyTheDeterministicStageOrder(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 15, 16, 0, 0, 0, time.UTC)
	run, event, err := drill.NewRun("run-1", "plan-1", 1, now)
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	if event.Kind != drill.EventRunCreated || event.Stage != drill.StageQueued {
		t.Fatalf("created event = %#v", event)
	}

	for index, stage := range orderedExecutionStages() {
		event, err = run.Transition(stage, now.Add(time.Duration(index+1)*time.Second))
		if err != nil {
			t.Fatalf("Transition(%s): %v", stage, err)
		}
		if event.Kind != drill.EventStageStarted || event.Stage != stage {
			t.Fatalf("transition event = %#v", event)
		}
	}
}

func TestRunOutcomesAndCleanupRemainOrthogonal(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 15, 16, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		outcome   drill.Outcome
		wantEvent drill.EventKind
	}{
		{name: "success", outcome: drill.OutcomeSucceeded, wantEvent: drill.EventRunSucceeded},
		{name: "failure", outcome: drill.OutcomeFailed, wantEvent: drill.EventRunFailed},
		{name: "cancellation", outcome: drill.OutcomeCancelled, wantEvent: drill.EventRunCancelled},
		{name: "timeout", outcome: drill.OutcomeTimedOut, wantEvent: drill.EventRunTimedOut},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			run, _, err := drill.NewRun("run-1", "plan-1", 1, now)
			if err != nil {
				t.Fatalf("NewRun: %v", err)
			}
			if test.outcome == drill.OutcomeSucceeded {
				for index, stage := range orderedExecutionStages() {
					if _, err := run.Transition(stage, now.Add(time.Duration(index+1)*time.Second)); err != nil {
						t.Fatalf("Transition(%s): %v", stage, err)
					}
				}
			}

			outcomeEvent, err := run.RecordOutcome(test.outcome, now.Add(time.Minute))
			if err != nil {
				t.Fatalf("RecordOutcome(%s): %v", test.outcome, err)
			}
			if outcomeEvent.Kind != test.wantEvent || run.Outcome != test.outcome {
				t.Fatalf("outcome = %q event = %q", run.Outcome, outcomeEvent.Kind)
			}
			if run.Stage != drill.StageCleanup || run.Cleanup != drill.CleanupPending {
				t.Fatalf("after outcome stage = %q cleanup = %q", run.Stage, run.Cleanup)
			}

			cleanupEvent, err := run.RecordCleanup(drill.CleanupFailed, now.Add(2*time.Minute))
			if err != nil {
				t.Fatalf("RecordCleanup: %v", err)
			}
			if cleanupEvent.Kind != drill.EventCleanupFailed || run.Outcome != test.outcome {
				t.Fatalf("cleanup event = %q outcome changed to %q", cleanupEvent.Kind, run.Outcome)
			}
		})
	}
}

func TestRunRejectsInvalidTerminalOperations(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 15, 16, 0, 0, 0, time.UTC)
	run, _, err := drill.NewRun("run-1", "plan-1", 1, now)
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	if _, err := run.RecordOutcome(drill.OutcomeSucceeded, now.Add(time.Second)); !errors.Is(err, drill.ErrInvalidOutcome) {
		t.Fatalf("early success error = %v, want ErrInvalidOutcome", err)
	}
	if _, err := run.RecordCleanup(drill.CleanupSucceeded, now.Add(time.Second)); !errors.Is(err, drill.ErrInvalidCleanup) {
		t.Fatalf("early cleanup error = %v, want ErrInvalidCleanup", err)
	}
	if _, err := run.RecordOutcome(drill.OutcomeFailed, now.Add(time.Second)); err != nil {
		t.Fatalf("RecordOutcome(failed): %v", err)
	}
	if _, err := run.Transition(drill.StagePreflight, now.Add(2*time.Second)); !errors.Is(err, drill.ErrInvalidTransition) {
		t.Fatalf("transition after outcome error = %v, want ErrInvalidTransition", err)
	}
	if _, err := run.RecordCleanup(drill.CleanupSucceeded, now.Add(2*time.Second)); err != nil {
		t.Fatalf("RecordCleanup: %v", err)
	}
	if _, err := run.RecordCleanup(drill.CleanupFailed, now.Add(3*time.Second)); !errors.Is(err, drill.ErrInvalidCleanup) {
		t.Fatalf("second cleanup error = %v, want ErrInvalidCleanup", err)
	}
}

func TestRunRestartReconciliationIsExplicitAndClearedByProgress(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 15, 16, 0, 0, 0, time.UTC)
	run, _, err := drill.NewRun("run-1", "plan-1", 1, now)
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	event, err := run.RequireReconciliation(now.Add(time.Second))
	if err != nil {
		t.Fatalf("RequireReconciliation: %v", err)
	}
	if event.Kind != drill.EventReconciliationRequired || !run.NeedsReconciliation || run.Terminal() {
		t.Fatalf("reconciled run = %#v event = %#v", run, event)
	}
	if _, err := run.RequireReconciliation(now.Add(2 * time.Second)); !errors.Is(err, drill.ErrInvalidReconciliation) {
		t.Fatalf("second reconciliation error = %v", err)
	}
	if _, err := run.Transition(drill.StagePreflight, now.Add(2*time.Second)); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if run.NeedsReconciliation || !run.ReconciliationRequestedAt.IsZero() {
		t.Fatalf("progress did not clear reconciliation: %#v", run)
	}
}

func TestTerminalRunCannotRequireReconciliation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 15, 16, 0, 0, 0, time.UTC)
	run, _, err := drill.NewRun("run-1", "plan-1", 1, now)
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	if _, err := run.RecordOutcome(drill.OutcomeFailed, now.Add(time.Second)); err != nil {
		t.Fatalf("RecordOutcome: %v", err)
	}
	if _, err := run.RecordCleanup(drill.CleanupSucceeded, now.Add(2*time.Second)); err != nil {
		t.Fatalf("RecordCleanup: %v", err)
	}
	if !run.Terminal() {
		t.Fatal("completed cleanup did not make run terminal")
	}
	if _, err := run.RequireReconciliation(now.Add(3 * time.Second)); !errors.Is(err, drill.ErrInvalidReconciliation) {
		t.Fatalf("terminal reconciliation error = %v", err)
	}
}

func TestRunRejectsInvalidInputsAndEventTimes(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 15, 16, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name        string
		id          string
		planID      string
		planVersion int64
		at          time.Time
	}{
		{name: "missing run id", planID: "plan-1", planVersion: 1, at: now},
		{name: "missing plan id", id: "run-1", planVersion: 1, at: now},
		{name: "invalid plan version", id: "run-1", planID: "plan-1", at: now},
		{name: "missing creation time", id: "run-1", planID: "plan-1", planVersion: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := drill.NewRun(test.id, test.planID, test.planVersion, test.at); !errors.Is(err, drill.ErrInvalidRun) {
				t.Fatalf("NewRun error = %v, want ErrInvalidRun", err)
			}
		})
	}

	run, _, err := drill.NewRun("run-1", "plan-1", 1, now)
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	if _, err := run.Transition(drill.StagePreflight, now.Add(-time.Second)); !errors.Is(err, drill.ErrInvalidRun) {
		t.Fatalf("backdated transition error = %v", err)
	}
	if _, err := run.RecordOutcome("unknown", now.Add(time.Second)); !errors.Is(err, drill.ErrInvalidOutcome) {
		t.Fatalf("unknown outcome error = %v", err)
	}
	if _, err := run.RecordOutcome(drill.OutcomeFailed, now.Add(-time.Second)); !errors.Is(err, drill.ErrInvalidRun) {
		t.Fatalf("backdated outcome error = %v", err)
	}
	if _, err := run.RecordOutcome(drill.OutcomeFailed, now.Add(time.Second)); err != nil {
		t.Fatalf("RecordOutcome: %v", err)
	}
	if _, err := run.RecordCleanup(drill.CleanupPending, now.Add(2*time.Second)); !errors.Is(err, drill.ErrInvalidCleanup) {
		t.Fatalf("pending cleanup error = %v", err)
	}
	if _, err := run.RecordCleanup(drill.CleanupSucceeded, now); !errors.Is(err, drill.ErrInvalidRun) {
		t.Fatalf("backdated cleanup error = %v", err)
	}
}

func runAtStage(t *testing.T, target drill.Stage, now time.Time) drill.Run {
	t.Helper()

	run, _, err := drill.NewRun("run-1", "plan-1", 1, now)
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	if target == drill.StageQueued {
		return run
	}
	for index, stage := range orderedExecutionStages() {
		if _, err := run.Transition(stage, now.Add(time.Duration(index+1)*time.Second)); err != nil {
			t.Fatalf("Transition(%s): %v", stage, err)
		}
		if stage == target {
			return run
		}
	}
	if target == drill.StageCleanup {
		if _, err := run.RecordOutcome(drill.OutcomeSucceeded, now.Add(time.Minute)); err != nil {
			t.Fatalf("RecordOutcome: %v", err)
		}
		return run
	}
	t.Fatalf("unsupported target stage %q", target)
	return drill.Run{}
}

func orderedExecutionStages() []drill.Stage {
	return []drill.Stage{
		drill.StagePreflight,
		drill.StageAcquire,
		drill.StageRestore,
		drill.StageBoot,
		drill.StageProbe,
		drill.StageReport,
	}
}
