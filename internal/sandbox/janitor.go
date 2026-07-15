package sandbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/azizu06/rehearse/internal/drill"
)

// ReconciliationStore is the Issue #6 durable queue consumed at startup.
type ReconciliationStore interface {
	RunsNeedingReconciliation(context.Context) ([]drill.Run, error)
	RecordOutcome(context.Context, string, drill.Outcome, time.Time) (drill.Run, error)
	BeginCleanupRetry(context.Context, string, time.Time) (drill.Run, error)
	RecordCleanup(context.Context, string, drill.CleanupStatus, time.Time) (drill.Run, error)
}

// RunCleaner is implemented by the label-scoped Docker cleaner.
type RunCleaner interface {
	Cleanup(context.Context, string) error
}

// JanitorOptions configure bounded startup reconciliation.
type JanitorOptions struct {
	CleanupTimeout time.Duration
	Now            func() time.Time
}

// Janitor consumes durable reconciliation truth without scanning unrelated
// Docker resources or persisting Docker-specific identifiers.
type Janitor struct {
	store          ReconciliationStore
	cleaner        RunCleaner
	cleanupTimeout time.Duration
	now            func() time.Time
}

// NewJanitor creates a startup janitor.
func NewJanitor(store ReconciliationStore, cleaner RunCleaner, options JanitorOptions) *Janitor {
	timeout := options.CleanupTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Janitor{store: store, cleaner: cleaner, cleanupTimeout: timeout, now: now}
}

// Reconcile attempts every queued run and leaves failures durably retryable.
func (janitor *Janitor) Reconcile(ctx context.Context) error {
	if janitor.store == nil || janitor.cleaner == nil {
		return errors.New("janitor store and cleaner are required")
	}
	runs, err := janitor.store.RunsNeedingReconciliation(ctx)
	if err != nil {
		return fmt.Errorf("load startup reconciliation queue: %w", err)
	}
	var reconciliationErrors []error
	for _, queued := range runs {
		run := queued
		at := janitor.eventTime(run)
		switch {
		case run.Outcome == "":
			run, err = janitor.store.RecordOutcome(ctx, run.ID, drill.OutcomeFailed, at)
		case run.Cleanup == drill.CleanupFailed:
			run, err = janitor.store.BeginCleanupRetry(ctx, run.ID, at)
		}
		if err != nil {
			reconciliationErrors = append(reconciliationErrors, fmt.Errorf("prepare run %s cleanup retry: %w", run.ID, err))
			continue
		}

		cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), janitor.cleanupTimeout)
		cleanupErr := janitor.cleaner.Cleanup(cleanupContext, run.ID)
		cancel()
		status := drill.CleanupSucceeded
		if cleanupErr != nil {
			status = drill.CleanupFailed
		}
		recordContext, recordCancel := context.WithTimeout(context.WithoutCancel(ctx), janitor.cleanupTimeout)
		_, recordErr := janitor.store.RecordCleanup(recordContext, run.ID, status, janitor.eventTime(run))
		recordCancel()
		if cleanupErr != nil || recordErr != nil {
			reconciliationErrors = append(reconciliationErrors,
				fmt.Errorf("reconcile run %s: %w", run.ID, errors.Join(cleanupErr, recordErr)))
		}
	}
	return errors.Join(reconciliationErrors...)
}

func (janitor *Janitor) eventTime(run drill.Run) time.Time {
	at := janitor.now().UTC()
	if at.Before(run.UpdatedAt) {
		return run.UpdatedAt
	}
	return at
}
