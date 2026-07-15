package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRunnerUsesShellFreeNoBuildNoPullAndFreshCleanupContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	command := &runnerDocker{cancel: cancel}
	runner := &DockerRunner{
		command:       command,
		cleaner:       cleaner{command: command, waiter: &recordingWaiter{}, quiescence: cleanupQuiescence},
		locker:        newProjectLocker(t.TempDir()),
		temporaryRoot: t.TempDir(), cleanupTimeout: time.Second,
	}
	request := Request{
		RunID: "run-cancel", ComposeFiles: []string{"testdata/compose.yaml"},
		Limits: Limits{CPUs: "0.5", MemoryBytes: 32 << 20, PIDs: 16, Duration: time.Minute, OutputBytes: 4096},
	}
	_, err := runner.Run(ctx, request, func(context.Context, Instance) error {
		t.Fatal("callback ran after failed Compose start")
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	if command.cleanupContextErr != nil {
		t.Fatalf("cleanup inherited cancelled run context: %v", command.cleanupContextErr)
	}
	if !command.cleanupObserved {
		t.Fatal("runner did not attempt label-scoped cleanup after cancellation")
	}
	if command.upCall == nil {
		t.Fatal("Compose up was not called")
	}
	joined := strings.Join(command.upCall, " ")
	if !strings.Contains(joined, " --no-build ") || !strings.Contains(joined, " --pull never") {
		t.Fatalf("unsafe Compose up argv: %v", command.upCall)
	}
	for _, argument := range command.upCall {
		if argument == "sh" || argument == "bash" || argument == "-c" || argument == "--build" {
			t.Fatalf("shell/build argument crossed boundary: %q", argument)
		}
	}
}

type runnerDocker struct {
	cancel            context.CancelFunc
	upErr             error
	upCall            []string
	cleanupObserved   bool
	cleanupContextErr error
	configCalls       int
}

func (docker *runnerDocker) run(ctx context.Context, _ int64, args ...string) ([]byte, error) {
	if containsSequence(args, "config", "--format", "json") {
		docker.configCalls++
		if docker.configCalls > 1 {
			for index := len(args) - 2; index >= 0; index-- {
				if args[index] == "--file" && strings.HasSuffix(args[index+1], overrideFileName) {
					return os.ReadFile(args[index+1])
				}
			}
		}
		return []byte(`{"services":{"worker":{"image":"alpine"}}}`), nil
	}
	if containsSequence(args, "up", "--detach") {
		docker.upCall = append([]string(nil), args...)
		if docker.cancel != nil {
			docker.cancel()
			return nil, context.Canceled
		}
		return nil, docker.upErr
	}
	if len(args) >= 2 && args[1] == "ls" && strings.Contains(strings.Join(args, " "), "label="+runFingerprintLabel+"=") {
		docker.cleanupObserved = true
		docker.cleanupContextErr = ctx.Err()
	}
	return nil, nil
}

func TestRunnerRemovesSnapshotAndCleansAfterStartBoundaryErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "output overflow", err: ErrOutputLimitExceeded},
		{name: "process start", err: errors.New("start docker command")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			command := &runnerDocker{upErr: test.err}
			runner := &DockerRunner{
				command: command,
				cleaner: cleaner{command: command, waiter: &recordingWaiter{}, quiescence: cleanupQuiescence, snapshotRoot: root},
				locker:  newProjectLocker(filepath.Join(root, "locks")), temporaryRoot: root, cleanupTimeout: time.Second,
			}
			request := Request{
				RunID: "run-" + strings.ReplaceAll(test.name, " ", "-"), ComposeFiles: []string{"testdata/compose.yaml"},
				Limits: Limits{CPUs: "0.5", MemoryBytes: 32 << 20, PIDs: 16, Duration: time.Minute, OutputBytes: 4096},
			}
			if _, err := runner.Run(context.Background(), request, func(context.Context, Instance) error { return nil }); !errors.Is(err, test.err) {
				t.Fatalf("Run error = %v, want %v", err, test.err)
			}
			if !command.cleanupObserved {
				t.Fatal("runner skipped label-scoped cleanup")
			}
			identity, _ := newIdentity(request.RunID)
			if _, err := os.Stat(snapshotDirectory(root, identity)); !os.IsNotExist(err) {
				t.Fatalf("snapshot directory survived error cleanup: %v", err)
			}
		})
	}
}

func containsSequence(values []string, sequence ...string) bool {
	for index := 0; index+len(sequence) <= len(values); index++ {
		if reflect.DeepEqual(values[index:index+len(sequence)], sequence) {
			return true
		}
	}
	return false
}
