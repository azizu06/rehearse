package drill

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrInvalidRun            = errors.New("invalid drill run")
	ErrInvalidTransition     = errors.New("invalid drill transition")
	ErrInvalidOutcome        = errors.New("invalid drill outcome")
	ErrInvalidCleanup        = errors.New("invalid cleanup result")
	ErrInvalidReconciliation = errors.New("invalid restart reconciliation")
)

// Stage is the current deterministic execution stage of a run.
type Stage string

const (
	StageQueued    Stage = "queued"
	StagePreflight Stage = "preflight"
	StageAcquire   Stage = "acquire"
	StageRestore   Stage = "restore"
	StageBoot      Stage = "boot"
	StageProbe     Stage = "probe"
	StageReport    Stage = "report"
	StageCleanup   Stage = "cleanup"
)

// Outcome records why execution stopped. Cleanup is deliberately separate.
type Outcome string

const (
	OutcomeSucceeded Outcome = "succeeded"
	OutcomeFailed    Outcome = "failed"
	OutcomeCancelled Outcome = "cancelled"
	OutcomeTimedOut  Outcome = "timed_out"
)

// CleanupStatus records the independent lifecycle of cleanup.
type CleanupStatus string

const (
	CleanupNotStarted CleanupStatus = "not_started"
	CleanupPending    CleanupStatus = "pending"
	CleanupSucceeded  CleanupStatus = "succeeded"
	CleanupFailed     CleanupStatus = "failed"
)

// EventKind names immutable facts in the run journal.
type EventKind string

const (
	EventRunCreated             EventKind = "run_created"
	EventStageStarted           EventKind = "stage_started"
	EventRunSucceeded           EventKind = "run_succeeded"
	EventRunFailed              EventKind = "run_failed"
	EventRunCancelled           EventKind = "run_cancelled"
	EventRunTimedOut            EventKind = "run_timed_out"
	EventCleanupSucceeded       EventKind = "cleanup_succeeded"
	EventCleanupFailed          EventKind = "cleanup_failed"
	EventReconciliationRequired EventKind = "reconciliation_required"
)

// Event is an immutable, secret-free state-machine fact.
type Event struct {
	Sequence   int64
	Kind       EventKind
	Stage      Stage
	Outcome    Outcome
	Cleanup    CleanupStatus
	OccurredAt time.Time
}

// Run is the current projection derived transactionally alongside its events.
type Run struct {
	ID                        string
	PlanID                    string
	PlanVersion               int64
	Stage                     Stage
	Outcome                   Outcome
	Cleanup                   CleanupStatus
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
	Version                   int64
	NeedsReconciliation       bool
	ReconciliationRequestedAt time.Time
}

var nextStage = map[Stage]Stage{
	StageQueued:    StagePreflight,
	StagePreflight: StageAcquire,
	StageAcquire:   StageRestore,
	StageRestore:   StageBoot,
	StageBoot:      StageProbe,
	StageProbe:     StageReport,
}

// NewRun creates the queued projection and its first immutable event.
func NewRun(id, planID string, planVersion int64, at time.Time) (Run, Event, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(planID) == "" || planVersion < 1 || at.IsZero() {
		return Run{}, Event{}, ErrInvalidRun
	}
	run := Run{
		ID:          id,
		PlanID:      planID,
		PlanVersion: planVersion,
		Stage:       StageQueued,
		Cleanup:     CleanupNotStarted,
		CreatedAt:   at.UTC(),
		UpdatedAt:   at.UTC(),
		Version:     1,
	}
	return run, run.event(EventRunCreated, at), nil
}

// Transition advances a run by exactly one vendor-independent stage.
func (run *Run) Transition(to Stage, at time.Time) (Event, error) {
	want, ok := nextStage[run.Stage]
	if !ok || run.Outcome != "" || to != want {
		return Event{}, fmt.Errorf("%w: %s to %s", ErrInvalidTransition, run.Stage, to)
	}
	if err := run.validateTimestamp(at); err != nil {
		return Event{}, err
	}
	run.Stage = to
	run.advance(at)
	return run.event(EventStageStarted, at), nil
}

// RecordOutcome ends execution and makes cleanup the active independent stage.
func (run *Run) RecordOutcome(outcome Outcome, at time.Time) (Event, error) {
	if run.Outcome != "" || run.Cleanup != CleanupNotStarted || run.Stage == StageCleanup {
		return Event{}, ErrInvalidOutcome
	}
	if outcome == OutcomeSucceeded && run.Stage != StageReport {
		return Event{}, fmt.Errorf("%w: success requires report stage", ErrInvalidOutcome)
	}
	kind, ok := eventForOutcome(outcome)
	if !ok {
		return Event{}, ErrInvalidOutcome
	}
	if err := run.validateTimestamp(at); err != nil {
		return Event{}, err
	}
	run.Stage = StageCleanup
	run.Outcome = outcome
	run.Cleanup = CleanupPending
	run.advance(at)
	return run.event(kind, at), nil
}

// RecordCleanup closes the cleanup dimension without changing run outcome.
func (run *Run) RecordCleanup(status CleanupStatus, at time.Time) (Event, error) {
	if run.Stage != StageCleanup || run.Outcome == "" || run.Cleanup != CleanupPending {
		return Event{}, ErrInvalidCleanup
	}
	var kind EventKind
	switch status {
	case CleanupSucceeded:
		kind = EventCleanupSucceeded
	case CleanupFailed:
		kind = EventCleanupFailed
	default:
		return Event{}, ErrInvalidCleanup
	}
	if err := run.validateTimestamp(at); err != nil {
		return Event{}, err
	}
	run.Cleanup = status
	run.Version++
	run.UpdatedAt = at.UTC()
	if status == CleanupFailed {
		run.NeedsReconciliation = true
		run.ReconciliationRequestedAt = at.UTC()
	} else {
		run.NeedsReconciliation = false
		run.ReconciliationRequestedAt = time.Time{}
	}
	return run.event(kind, at), nil
}

// BeginCleanupRetry reopens only the cleanup dimension after a prior failed
// attempt. Execution outcome remains immutable while startup reconciliation
// continues to own the abandoned resources.
func (run *Run) BeginCleanupRetry(at time.Time) (Event, error) {
	if run.Stage != StageCleanup || run.Outcome == "" || run.Cleanup != CleanupFailed {
		return Event{}, ErrInvalidReconciliation
	}
	if err := run.validateTimestamp(at); err != nil {
		return Event{}, err
	}
	run.Cleanup = CleanupPending
	run.NeedsReconciliation = true
	run.ReconciliationRequestedAt = at.UTC()
	run.Version++
	run.UpdatedAt = at.UTC()
	return run.event(EventReconciliationRequired, at), nil
}

// RequireReconciliation marks a non-terminal run when a persisted process is
// reopened after restart.
func (run *Run) RequireReconciliation(at time.Time) (Event, error) {
	if run.Terminal() || run.NeedsReconciliation {
		return Event{}, ErrInvalidReconciliation
	}
	if err := run.validateTimestamp(at); err != nil {
		return Event{}, err
	}
	run.NeedsReconciliation = true
	run.ReconciliationRequestedAt = at.UTC()
	run.Version++
	run.UpdatedAt = at.UTC()
	return run.event(EventReconciliationRequired, at), nil
}

// Terminal reports whether execution and cleanup have both reached final truth.
func (run Run) Terminal() bool {
	return run.Outcome != "" && (run.Cleanup == CleanupSucceeded || run.Cleanup == CleanupFailed)
}

func (run *Run) validateTimestamp(at time.Time) error {
	if at.IsZero() || at.Before(run.UpdatedAt) {
		return fmt.Errorf("%w: event time precedes projection", ErrInvalidRun)
	}
	return nil
}

func (run *Run) advance(at time.Time) {
	run.Version++
	run.UpdatedAt = at.UTC()
	if run.NeedsReconciliation {
		run.NeedsReconciliation = false
		run.ReconciliationRequestedAt = time.Time{}
	}
}

func (run Run) event(kind EventKind, at time.Time) Event {
	return Event{
		Sequence:   run.Version,
		Kind:       kind,
		Stage:      run.Stage,
		Outcome:    run.Outcome,
		Cleanup:    run.Cleanup,
		OccurredAt: at.UTC(),
	}
}

func eventForOutcome(outcome Outcome) (EventKind, bool) {
	switch outcome {
	case OutcomeSucceeded:
		return EventRunSucceeded, true
	case OutcomeFailed:
		return EventRunFailed, true
	case OutcomeCancelled:
		return EventRunCancelled, true
	case OutcomeTimedOut:
		return EventRunTimedOut, true
	default:
		return "", false
	}
}
