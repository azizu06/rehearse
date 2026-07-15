package sandbox

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"
)

// DockerRunner owns the Compose CLI, project lock, and label-scoped cleaner.
type DockerRunner struct {
	command        dockerCommand
	cleaner        cleaner
	locker         projectLocker
	temporaryRoot  string
	cleanupTimeout time.Duration
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
		locker:         newProjectLocker(options.LockRoot),
		temporaryRoot:  filepath.Clean(temporaryRoot),
		cleanupTimeout: cleanupTimeout,
	}
}

// Run creates one project, invokes use while it is healthy, and always attempts
// cleanup with an independent deadline after start success/failure, timeout,
// cancellation, or output overflow.
func (runner *DockerRunner) Run(
	ctx context.Context,
	request Request,
	use func(context.Context, Instance) error,
) (Result, error) {
	normalized, err := normalizeRequest(request)
	if err != nil {
		return Result{}, err
	}
	instance := Instance{RunID: normalized.RunID, ProjectName: normalized.identity.projectName}
	if use == nil {
		return instance, fmt.Errorf("%w: sandbox callback is required", ErrInvalidRequest)
	}
	if info, err := os.Stat(runner.temporaryRoot); err != nil || !info.IsDir() {
		return instance, fmt.Errorf("%w: temporary root must exist", ErrInvalidRequest)
	}

	lock, err := runner.locker.lock(normalized.identity)
	if err != nil {
		return instance, err
	}
	defer func() { _ = lock.release() }()
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
	defer func() { _ = removeSnapshotDirectory(runner.temporaryRoot, normalized.identity) }()
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
	if _, err := parseAndValidateSnapshot(snapshotData, normalized.identity, normalized.Limits); err != nil {
		return instance, fmt.Errorf("validate constrained Compose snapshot: %w", err)
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
	var useErr error
	if startErr == nil {
		useErr = use(runContext, instance)
	}

	cleanupContext, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), runner.cleanupTimeout)
	cleanupErr := runner.cleaner.cleanup(cleanupContext, normalized.identity)
	cleanupCancel()

	if startErr != nil {
		return instance, errors.Join(fmt.Errorf("start Compose sandbox: %w", startErr), cleanupErr)
	}
	if cause := context.Cause(runContext); cause != nil {
		useErr = errors.Join(useErr, cause)
	}
	return instance, errors.Join(useErr, cleanupErr)
}

func durationSeconds(duration time.Duration) string {
	return fmt.Sprintf("%.0f", math.Ceil(duration.Seconds()))
}
