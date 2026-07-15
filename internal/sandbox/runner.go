package sandbox

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/azizu06/rehearse/internal/drill"
)

// DockerRunner owns the Compose CLI, project lock, and label-scoped cleaner.
type DockerRunner struct {
	command        dockerCommand
	cleaner        cleaner
	journal        CleanupJournal
	locker         projectLocker
	temporaryRoot  string
	cleanupTimeout time.Duration
	newClaimID     func() (string, error)
}

// NewDockerRunner creates a runner without contacting Docker. Run performs
// validation and collision preflight before any resource is created.
func NewDockerRunner(options DockerRunnerOptions) *DockerRunner {
	cleanupTimeout := options.CleanupTimeout
	if cleanupTimeout <= 0 {
		cleanupTimeout = 30 * time.Second
	}
	temporaryRoot := options.TemporaryRoot
	if temporaryRoot == "" {
		temporaryRoot = os.TempDir()
	}
	command := newCommandExecutor(options.Binary)
	return &DockerRunner{
		command:        command,
		cleaner:        cleaner{command: command, waiter: timerWaiter{}, quiescence: cleanupQuiescence, snapshotRoot: filepath.Clean(temporaryRoot)},
		journal:        options.CleanupJournal,
		locker:         newProjectLocker(options.LockRoot),
		temporaryRoot:  filepath.Clean(temporaryRoot),
		cleanupTimeout: cleanupTimeout,
		newClaimID:     generateClaimID,
	}
}

// Run creates one project, invokes use while it is healthy, and always attempts
// cleanup with an independent deadline after start success/failure, timeout,
// cancellation, or output overflow.
func (runner *DockerRunner) Run(
	ctx context.Context,
	request Request,
	use func(context.Context, Instance) error,
) (result Result, resultErr error) {
	normalized, err := normalizeRequest(request)
	if err != nil {
		return Result{}, err
	}
	instance := Instance{RunID: normalized.RunID, ProjectName: normalized.identity.projectName}
	result = instance
	if use == nil {
		return instance, fmt.Errorf("%w: sandbox callback is required", ErrInvalidRequest)
	}
	if runner.journal == nil {
		return instance, fmt.Errorf("%w: durable cleanup journal is required", ErrInvalidRequest)
	}
	if info, err := os.Stat(runner.temporaryRoot); err != nil || !info.IsDir() {
		return instance, fmt.Errorf("%w: temporary root must exist", ErrInvalidRequest)
	}

	lock, err := runner.locker.lock(normalized.identity)
	if err != nil {
		return instance, err
	}
	defer func() { resultErr = errors.Join(resultErr, lock.release()) }()
	claimIDGenerator := runner.newClaimID
	if claimIDGenerator == nil {
		claimIDGenerator = generateClaimID
	}
	claimID, err := claimIDGenerator()
	if err != nil {
		return instance, err
	}
	normalized.identity, err = normalized.identity.withClaimID(claimID)
	if err != nil {
		return instance, err
	}
	if err := runner.journal.ClaimSandboxCleanup(ctx, normalized.RunID, claimID, time.Now().UTC()); err != nil {
		return instance, fmt.Errorf("claim durable sandbox cleanup: %w", err)
	}
	ledger := newCreatedResourceLedger()
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), runner.cleanupTimeout)
		cleanupErr := runner.cleaner.cleanupCreated(cleanupContext, normalized.identity, ledger)
		cleanupCancel()
		status := drill.CleanupSucceeded
		if cleanupErr != nil {
			status = drill.CleanupFailed
		}
		recordContext, recordCancel := context.WithTimeout(context.WithoutCancel(ctx), runner.cleanupTimeout)
		recordErr := runner.journal.RecordSandboxCleanup(recordContext, normalized.RunID, status, time.Now().UTC())
		recordCancel()
		resultErr = errors.Join(resultErr, cleanupErr, recordErr)
	}()
	runContext, cancel := context.WithTimeout(ctx, normalized.Limits.Duration)
	defer cancel()
	if err := ensureProjectVacant(runContext, runner.command, normalized.identity); err != nil {
		return instance, err
	}
	configArgs := append(composeArgs(normalized, ""), "config", "--format", "json", "--no-env-resolution", "--no-path-resolution")
	resolved, err := runner.command.run(runContext, normalized.Limits.OutputBytes, configArgs...)
	if err != nil {
		return instance, fmt.Errorf("resolve Compose model: %w", err)
	}
	model, err := parseAndValidateCompose(resolved, normalized.identity)
	if err != nil {
		return instance, err
	}
	override, err := buildOverride(model, normalized.identity, normalized.Limits)
	if err != nil {
		return instance, err
	}
	overrideData, err := encodeOverride(override)
	if err != nil {
		return instance, err
	}
	snapshotDirectory, err := prepareSnapshotDirectory(runner.temporaryRoot, normalized.identity)
	if err != nil {
		return instance, err
	}
	overridePath, err := writeSnapshotFile(snapshotDirectory, overrideFileName, overrideData)
	if err != nil {
		return instance, err
	}

	renderArgs := append(composeArgs(normalized, overridePath), "config", "--format", "json", "--no-env-resolution", "--no-path-resolution")
	rendered, err := runner.command.run(runContext, normalized.Limits.OutputBytes, renderArgs...)
	if err != nil {
		return instance, fmt.Errorf("render constrained Compose snapshot: %w", err)
	}
	snapshotData := escapeSnapshotInterpolation(rendered)
	snapshotModel, err := parseAndValidateSnapshot(snapshotData, normalized.identity, normalized.Limits)
	if err != nil {
		return instance, fmt.Errorf("validate constrained Compose snapshot: %w", err)
	}
	if err := ensureResolvedProjectVacant(runContext, runner.command, normalized.identity, snapshotModel); err != nil {
		return instance, err
	}
	snapshotData, err = pinServiceImages(runContext, runner.command, snapshotData, snapshotModel)
	if err != nil {
		return instance, fmt.Errorf("validate service image volume policy: %w", err)
	}
	snapshotModel, err = parseAndValidateSnapshot(snapshotData, normalized.identity, normalized.Limits)
	if err != nil {
		return instance, fmt.Errorf("validate pinned Compose snapshot: %w", err)
	}
	if err := reserveComposeResources(runContext, runner.command, normalized.identity, snapshotModel, ledger); err != nil {
		return instance, err
	}
	snapshotData, err = rewriteSnapshotReservedResources(snapshotData, normalized.identity, snapshotModel)
	if err != nil {
		return instance, err
	}
	if _, err := parseAndValidateReservedSnapshot(snapshotData, normalized.identity, normalized.Limits); err != nil {
		return instance, fmt.Errorf("validate reserved Compose snapshot: %w", err)
	}
	snapshotPath, err := writeSnapshotFile(snapshotDirectory, snapshotFileName, snapshotData)
	if err != nil {
		return instance, err
	}
	if err := os.Remove(overridePath); err != nil {
		return instance, fmt.Errorf("remove transient Compose policy: %w", err)
	}

	upArgs := append(snapshotComposeArgs(normalized, snapshotPath),
		"up", "--detach", "--wait", "--wait-timeout", durationSeconds(normalized.Limits.Duration),
		"--no-build", "--pull", "never",
	)
	_, startErr := runner.command.run(runContext, normalized.Limits.OutputBytes, upArgs...)
	captureContext, captureCancel := context.WithTimeout(context.WithoutCancel(ctx), runner.cleanupTimeout)
	captureErr := captureComposeContainers(captureContext, runner.command, normalized.identity, snapshotModel, ledger, startErr == nil)
	captureCancel()
	if startErr != nil {
		return instance, fmt.Errorf("start Compose sandbox: %w", errors.Join(startErr, captureErr))
	}
	if captureErr != nil {
		return instance, captureErr
	}
	callback := make(chan callbackResult, 1)
	go invokeCallback(runContext, instance, use, callback)
	select {
	case completed := <-callback:
		if completed.panicValue != nil {
			panic(completed.panicValue)
		}
		if cause := context.Cause(runContext); cause != nil {
			return instance, errors.Join(completed.err, cause)
		}
		return instance, completed.err
	case <-runContext.Done():
		return instance, context.Cause(runContext)
	}
}

type callbackResult struct {
	err        error
	panicValue any
}

func invokeCallback(ctx context.Context, instance Instance, use func(context.Context, Instance) error, result chan<- callbackResult) {
	defer func() {
		if value := recover(); value != nil {
			result <- callbackResult{panicValue: value}
		}
	}()
	result <- callbackResult{err: use(ctx, instance)}
}

func durationSeconds(duration time.Duration) string {
	return fmt.Sprintf("%.0f", math.Ceil(duration.Seconds()))
}
