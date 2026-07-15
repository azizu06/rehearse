package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azizu06/rehearse/internal/drill"
)

func TestRunnerUsesShellFreeNoBuildNoPullAndFreshCleanupContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	command := &runnerDocker{cancel: cancel}
	runner := &DockerRunner{
		command:       command,
		cleaner:       cleaner{command: command, waiter: &recordingWaiter{}, quiescence: cleanupQuiescence},
		journal:       &recordingCleanupJournal{},
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
	upSnapshotImage   string
	cleanupObserved   bool
	cleanupContextErr error
	configCalls       int
	platform          string
	imageID           string
	retagTo           string
	imageOS           string
	imageArchitecture string
	imageVariant      string
	imageInspectCall  []string
	imageVolumes      []byte
}

const testImageID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func (docker *runnerDocker) run(ctx context.Context, _ int64, args ...string) ([]byte, error) {
	if containsSequence(args, "config", "--format", "json") {
		docker.configCalls++
		if docker.configCalls > 1 {
			for index := len(args) - 2; index >= 0; index-- {
				if args[index] == "--file" && strings.HasSuffix(args[index+1], overrideFileName) {
					data, err := os.ReadFile(args[index+1])
					if err != nil {
						return nil, err
					}
					var document map[string]any
					if err := json.Unmarshal(data, &document); err != nil {
						return nil, err
					}
					document["services"].(map[string]any)["worker"].(map[string]any)["image"] = "alpine"
					if docker.platform != "" {
						document["services"].(map[string]any)["worker"].(map[string]any)["platform"] = docker.platform
					}
					return json.Marshal(document)
				}
			}
		}
		service := map[string]any{"image": "alpine"}
		if docker.platform != "" {
			service["platform"] = docker.platform
		}
		return json.Marshal(map[string]any{"services": map[string]any{"worker": service}})
	}
	if containsSequence(args, "image", "inspect") {
		docker.imageInspectCall = append([]string(nil), args...)
		imageID := docker.imageID
		if imageID == "" {
			imageID = testImageID
		}
		operatingSystem := docker.imageOS
		if operatingSystem == "" {
			operatingSystem = "linux"
		}
		architecture := docker.imageArchitecture
		if architecture == "" {
			architecture = "amd64"
		}
		volumes := json.RawMessage("null")
		if docker.imageVolumes != nil {
			volumes = docker.imageVolumes
		}
		output, err := json.Marshal(struct {
			ID           string          `json:"id"`
			OS           string          `json:"os"`
			Architecture string          `json:"architecture"`
			Variant      string          `json:"variant"`
			Volumes      json.RawMessage `json:"volumes"`
		}{imageID, operatingSystem, architecture, docker.imageVariant, volumes})
		if docker.retagTo != "" {
			docker.imageID = docker.retagTo
		}
		return output, err
	}
	if containsSequence(args, "up", "--detach") {
		docker.upCall = append([]string(nil), args...)
		data, err := os.ReadFile(lastFileArgument(args))
		if err != nil {
			return nil, err
		}
		var snapshot struct {
			Services map[string]struct {
				Image string `json:"image"`
			} `json:"services"`
		}
		if err := json.Unmarshal(data, &snapshot); err != nil {
			return nil, err
		}
		docker.upSnapshotImage = snapshot.Services["worker"].Image
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

func TestRunnerPinsSelectedPlatformImageBeforeMutableTagChanges(t *testing.T) {
	retaggedID := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	command := &runnerDocker{
		platform: "linux/arm64/v8", imageID: testImageID, retagTo: retaggedID,
		imageArchitecture: "arm64", imageVariant: "v8",
	}
	runner := testRunner(t, command, &recordingCleanupJournal{})
	if _, err := runner.Run(context.Background(), testRunnerRequest("run-pinned-platform", time.Minute), func(context.Context, Instance) error { return nil }); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := command.upSnapshotImage; got != testImageID {
		t.Fatalf("execution snapshot image = %q, want immutable selected ID %q", got, testImageID)
	}
	if command.imageID != retaggedID {
		t.Fatalf("mutable tag did not change during preflight: %q", command.imageID)
	}
	if !containsSequence(command.imageInspectCall, "--platform", "linux/arm64/v8") {
		t.Fatalf("image inspection ignored selected platform: %v", command.imageInspectCall)
	}
}

func TestRunnerRejectsSelectedImagePlatformMismatch(t *testing.T) {
	command := &runnerDocker{platform: "linux/arm64", imageArchitecture: "amd64"}
	runner := testRunner(t, command, &recordingCleanupJournal{})
	_, err := runner.Run(context.Background(), testRunnerRequest("run-platform-mismatch", time.Minute), func(context.Context, Instance) error { return nil })
	if !errors.Is(err, ErrUnsafeCompose) {
		t.Fatalf("Run error = %v, want ErrUnsafeCompose", err)
	}
	if command.upCall != nil {
		t.Fatal("runner started a sandbox with mismatched selected image metadata")
	}
}

func TestRunnerRejectsImagesThatDeclareAnonymousVolumes(t *testing.T) {
	command := &runnerDocker{imageVolumes: []byte(`{"/var/lib/data":{}}`)}
	runner := testRunner(t, command, &recordingCleanupJournal{})
	callbackCalled := false
	_, err := runner.Run(context.Background(), testRunnerRequest("run-image-volume", time.Minute), func(context.Context, Instance) error {
		callbackCalled = true
		return nil
	})
	if !errors.Is(err, ErrUnsafeCompose) {
		t.Fatalf("Run error = %v, want ErrUnsafeCompose", err)
	}
	if callbackCalled || command.upCall != nil {
		t.Fatal("runner started a sandbox for an image declaring anonymous volumes")
	}
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
				journal: &recordingCleanupJournal{},
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

func TestRunnerEnforcesDurationWithoutCallbackCooperation(t *testing.T) {
	release := make(chan struct{})
	command := &runnerDocker{}
	runner := testRunner(t, command, &recordingCleanupJournal{})
	request := testRunnerRequest("run-uncooperative-timeout", 20*time.Millisecond)
	started := time.Now()
	_, err := runner.Run(context.Background(), request, func(context.Context, Instance) error {
		<-release
		return nil
	})
	close(release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run error = %v, want context deadline", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Run waited %s for an uncooperative callback", elapsed)
	}
	if !command.cleanupObserved {
		t.Fatal("runner skipped cleanup after independent timeout")
	}
}

func TestRunnerCleansAndRecordsBeforeRepanicking(t *testing.T) {
	command := &runnerDocker{}
	journal := &recordingCleanupJournal{}
	runner := testRunner(t, command, journal)
	panicValue := errors.New("callback panic")
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_, _ = runner.Run(context.Background(), testRunnerRequest("run-panic", time.Minute), func(context.Context, Instance) error {
			panic(panicValue)
		})
	}()
	if recovered != panicValue {
		t.Fatalf("recovered panic = %v, want %v", recovered, panicValue)
	}
	if !command.cleanupObserved {
		t.Fatal("runner skipped cleanup after callback panic")
	}
	if got := journal.lastStatus(); got != drill.CleanupSucceeded {
		t.Fatalf("recorded cleanup = %q, want succeeded", got)
	}
}

func TestRunnerDurablyRecordsSnapshotDeletionFailure(t *testing.T) {
	root := t.TempDir()
	command := &runnerDocker{}
	journal := &recordingCleanupJournal{}
	wantErr := errors.New("remove snapshot failed")
	runner := &DockerRunner{
		command: command,
		cleaner: cleaner{
			command: command, waiter: &recordingWaiter{}, quiescence: cleanupQuiescence,
			snapshotRoot: root, removeSnapshot: func(string, identity) error { return wantErr },
		},
		journal: journal, locker: newProjectLocker(filepath.Join(root, "locks")),
		temporaryRoot: root, cleanupTimeout: time.Second,
	}
	_, err := runner.Run(context.Background(), testRunnerRequest("run-snapshot-delete", time.Minute), func(context.Context, Instance) error { return nil })
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want snapshot deletion failure", err)
	}
	if got := journal.lastStatus(); got != drill.CleanupFailed {
		t.Fatalf("recorded cleanup = %q, want failed", got)
	}
}

func TestRunnerRejectsRunsWithoutDurableOwnershipBeforeDocker(t *testing.T) {
	command := &runnerDocker{}
	wantErr := errors.New("run not persisted")
	runner := testRunner(t, command, &recordingCleanupJournal{claimErr: wantErr})
	_, err := runner.Run(context.Background(), testRunnerRequest("run-missing", time.Minute), func(context.Context, Instance) error { return nil })
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want durable ownership failure", err)
	}
	if command.configCalls != 0 {
		t.Fatalf("Docker config calls = %d, want zero", command.configCalls)
	}
}

func testRunner(t *testing.T, command *runnerDocker, journal CleanupJournal) *DockerRunner {
	t.Helper()
	root := t.TempDir()
	return &DockerRunner{
		command: command,
		cleaner: cleaner{command: command, waiter: &recordingWaiter{}, quiescence: cleanupQuiescence, snapshotRoot: root},
		journal: journal, locker: newProjectLocker(filepath.Join(root, "locks")),
		temporaryRoot: root, cleanupTimeout: time.Second,
	}
}

func testRunnerRequest(runID string, duration time.Duration) Request {
	return Request{
		RunID: runID, ComposeFiles: []string{"testdata/compose.yaml"},
		Limits: Limits{CPUs: "0.5", MemoryBytes: 32 << 20, PIDs: 16, Duration: duration, OutputBytes: 4096},
	}
}

type recordingCleanupJournal struct {
	mu       sync.Mutex
	claimErr error
	statuses []drill.CleanupStatus
}

func (journal *recordingCleanupJournal) ClaimSandboxCleanup(context.Context, string, time.Time) error {
	return journal.claimErr
}

func (journal *recordingCleanupJournal) RecordSandboxCleanup(_ context.Context, _ string, status drill.CleanupStatus, _ time.Time) error {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	journal.statuses = append(journal.statuses, status)
	return nil
}

func (journal *recordingCleanupJournal) lastStatus() drill.CleanupStatus {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if len(journal.statuses) == 0 {
		return ""
	}
	return journal.statuses[len(journal.statuses)-1]
}

func containsSequence(values []string, sequence ...string) bool {
	for index := 0; index+len(sequence) <= len(values); index++ {
		if reflect.DeepEqual(values[index:index+len(sequence)], sequence) {
			return true
		}
	}
	return false
}
