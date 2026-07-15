package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		newClaimID:      func() (string, error) { return testClaimID, nil },
		newGenerationID: testGenerationGenerator(),
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
	cancel              context.CancelFunc
	upErr               error
	upCall              []string
	upSnapshotImage     string
	cleanupObserved     bool
	cleanupContextErr   error
	configCalls         int
	platform            string
	imageID             string
	retagTo             string
	imageOS             string
	imageArchitecture   string
	imageVariant        string
	imageInspectCall    []string
	imageVolumes        []byte
	includeVolume       bool
	lateCollision       bool
	collisionVisible    bool
	reservationMismatch string
	reservationCalls    [][]string
	reservedLabels      map[string]map[string]string
	upReservedVolume    bool
	upReservedNetwork   bool
	resources           map[string]inspectedResource
	removed             []createdResource
}

const testImageID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const testClaimID = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"

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
		document := map[string]any{"services": map[string]any{"worker": service}}
		if docker.includeVolume {
			service["volumes"] = []map[string]any{{"type": "volume", "source": "work", "target": "/work"}}
			document["volumes"] = map[string]any{"work": map[string]any{}}
		}
		if docker.platform != "" {
			service["platform"] = docker.platform
		}
		return json.Marshal(document)
	}
	if len(args) >= 2 && (args[0] == "network" || args[0] == "volume") && args[1] == "create" {
		docker.reservationCalls = append(docker.reservationCalls, append([]string(nil), args...))
		if docker.reservedLabels == nil {
			docker.reservedLabels = make(map[string]map[string]string)
		}
		labels := make(map[string]string)
		for index := 0; index+1 < len(args); index++ {
			if args[index] == "--label" {
				key, value, ok := strings.Cut(args[index+1], "=")
				if ok {
					labels[key] = value
				}
			}
		}
		docker.reservedLabels[args[0]] = labels
		name := args[len(args)-1]
		if docker.lateCollision && args[0] == "volume" {
			docker.collisionVisible = true
			collisionLabels := make(map[string]string, len(labels)-1)
			for key, value := range labels {
				if key != sandboxClaimLabel && key != resourceGenerationLabel {
					collisionLabels[key] = value
				}
			}
			docker.storeResource(inspectedResource{DaemonID: "late-volume-id", Name: name, Labels: collisionLabels}, args[0])
			return nil, errors.New("docker create name conflict")
		}
		docker.storeResource(inspectedResource{DaemonID: args[0] + "-id", Name: name, Labels: labels}, args[0])
		return []byte(name + "\n"), nil
	}
	if len(args) >= 2 && (args[0] == "network" || args[0] == "volume") && args[1] == "inspect" {
		if docker.reservationMismatch == args[0] {
			return []byte(`{}`), nil
		}
		if resource, exists := docker.findResource(args[0], args[len(args)-1]); exists {
			return json.Marshal(resource)
		}
		return nil, errors.New("resource not found")
	}
	if len(args) >= 2 && (args[0] == "network" || args[0] == "volume") && args[1] == "ls" && strings.Contains(strings.Join(args, " "), "name=^") {
		for key, resource := range docker.resources {
			if strings.HasPrefix(key, args[0]+"\x00") && strings.Contains(strings.Join(args, " "), "name=^"+resource.Name+"$") {
				reference := resource.DaemonID
				if args[0] == "volume" {
					reference = resource.Name
				}
				return []byte(reference + "\n"), nil
			}
		}
		return nil, nil
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
				Image  string            `json:"image"`
				Labels map[string]string `json:"labels"`
			} `json:"services"`
			Networks map[string]struct {
				External bool   `json:"external"`
				Name     string `json:"name"`
			} `json:"networks"`
			Volumes map[string]struct {
				External bool   `json:"external"`
				Name     string `json:"name"`
			} `json:"volumes"`
		}
		if err := json.Unmarshal(data, &snapshot); err != nil {
			return nil, err
		}
		docker.upSnapshotImage = snapshot.Services["worker"].Image
		projectName := argumentAfter(args, "--project-name")
		docker.storeResource(inspectedResource{
			DaemonID: "container-id", Name: projectName + "-worker-1", Labels: snapshot.Services["worker"].Labels,
		}, "container")
		for _, resource := range snapshot.Networks {
			docker.upReservedNetwork = resource.External && resource.Name != ""
		}
		for _, resource := range snapshot.Volumes {
			docker.upReservedVolume = resource.External && resource.Name != ""
		}
		if docker.cancel != nil {
			docker.cancel()
			return nil, context.Canceled
		}
		return nil, docker.upErr
	}
	if len(args) >= 2 && args[1] == "ls" {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "label="+sandboxClaimLabel+"=") {
			docker.cleanupObserved = true
			docker.cleanupContextErr = ctx.Err()
			return docker.listClaimed(args[0], joined), nil
		}
		if strings.Contains(joined, "name=^") {
			for key, resource := range docker.resources {
				if strings.HasPrefix(key, args[0]+"\x00") && strings.Contains(joined, "name=^/"+resource.Name+"$") {
					return []byte(resource.DaemonID + "\n"), nil
				}
			}
		}
	}
	if len(args) >= 2 && args[1] == "inspect" {
		if resource, exists := docker.findResource(args[0], args[len(args)-1]); exists {
			return json.Marshal(resource)
		}
		return nil, errors.New("resource not found")
	}
	if len(args) >= 4 && args[1] == "rm" {
		if resource, exists := docker.findResource(args[0], args[len(args)-1]); exists {
			delete(docker.resources, args[0]+"\x00"+resource.Name)
			docker.removed = append(docker.removed, createdResource{kind: args[0], name: resource.Name, daemonID: resource.DaemonID})
		}
	}
	return nil, nil
}

func (docker *runnerDocker) storeResource(resource inspectedResource, kind string) {
	if docker.resources == nil {
		docker.resources = make(map[string]inspectedResource)
	}
	docker.resources[kind+"\x00"+resource.Name] = resource
}

func (docker *runnerDocker) findResource(kind, reference string) (inspectedResource, bool) {
	for key, resource := range docker.resources {
		if strings.HasPrefix(key, kind+"\x00") && (resource.Name == reference || resource.DaemonID == reference) {
			return resource, true
		}
	}
	return inspectedResource{}, false
}

func (docker *runnerDocker) listClaimed(kind, joined string) []byte {
	var result strings.Builder
	for key, resource := range docker.resources {
		if !strings.HasPrefix(key, kind+"\x00") {
			continue
		}
		matched := true
		for _, filter := range []string{managedLabel, projectLabel, runFingerprintLabel, runIDLabel, sandboxClaimLabel} {
			marker := "label=" + filter + "="
			index := strings.Index(joined, marker)
			if index < 0 {
				continue
			}
			value := strings.Fields(joined[index+len(marker):])[0]
			if resource.Labels[filter] != value {
				matched = false
				break
			}
		}
		if matched {
			if kind == "volume" {
				result.WriteString(resource.Name)
			} else {
				result.WriteString(resource.DaemonID)
			}
			result.WriteByte('\n')
		}
	}
	return []byte(result.String())
}

func argumentAfter(values []string, key string) string {
	for index := 0; index+1 < len(values); index++ {
		if values[index] == key {
			return values[index+1]
		}
	}
	return ""
}

func TestRunnerAtomicallyReservesGeneratedResourcesBeforeComposeUp(t *testing.T) {
	command := &runnerDocker{includeVolume: true}
	journal := &recordingCleanupJournal{}
	runner := testRunner(t, command, journal)
	request := testRunnerRequest("run-reserved-resources", time.Minute)
	if _, err := runner.Run(context.Background(), request, func(context.Context, Instance) error { return nil }); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(command.reservationCalls) != 2 {
		t.Fatalf("resource reservation calls = %v, want one network and one volume", command.reservationCalls)
	}
	identity, _ := newIdentity(request.RunID)
	for _, kind := range []string{"network", "volume"} {
		if !labelsContain(command.reservedLabels[kind], identity.labels()) {
			t.Fatalf("%s reservation labels = %#v, want exact run ownership", kind, command.reservedLabels[kind])
		}
	}
	if !command.upReservedNetwork || !command.upReservedVolume {
		t.Fatalf("Compose up snapshot did not use only reserved external resources: network=%t volume=%t", command.upReservedNetwork, command.upReservedVolume)
	}
	if len(journal.resources) != 3 {
		t.Fatalf("durable cleanup manifest = %#v, want network, volume, and container", journal.resources)
	}
	seenGenerations := make(map[string]bool)
	for _, resource := range journal.resources {
		if len(resource.Generation) != 64 || seenGenerations[resource.Generation] {
			t.Fatalf("invalid or reused resource generation in manifest: %#v", journal.resources)
		}
		seenGenerations[resource.Generation] = true
	}
}

func TestRunnerRejectsLateStableLabelReservationCollisionWithoutDeletingIt(t *testing.T) {
	command := &runnerDocker{includeVolume: true, lateCollision: true}
	runner := testRunner(t, command, &recordingCleanupJournal{})
	callbackCalled := false
	_, err := runner.Run(context.Background(), testRunnerRequest("run-late-reservation-collision", time.Minute), func(context.Context, Instance) error {
		callbackCalled = true
		return nil
	})
	if !errors.Is(err, ErrProjectCollision) {
		t.Fatalf("Run error = %v, want ErrProjectCollision", err)
	}
	if callbackCalled || command.upCall != nil {
		t.Fatal("runner started containers after a late stable-label resource collision")
	}
	var collisionSurvived bool
	for key, resource := range command.resources {
		if strings.HasPrefix(key, "volume\x00") && resource.Labels[sandboxClaimLabel] == "" {
			collisionSurvived = true
		}
	}
	if !collisionSurvived {
		t.Fatal("runner deleted the late-colliding stable-label volume")
	}
	createdNetworkRemoved := false
	for _, removed := range command.removed {
		if removed.kind == "volume" {
			t.Fatalf("runner removed late collision: %#v", removed)
		}
		if removed.kind == "network" {
			createdNetworkRemoved = true
		}
	}
	if !createdNetworkRemoved {
		t.Fatal("runner failed to clean the network created before the collision")
	}
}

func TestRunnerPreservesPreExistingExactStableLabelResource(t *testing.T) {
	request := testRunnerRequest("run-preexisting-stable-labels", time.Minute)
	stableIdentity, _ := newIdentity(request.RunID)
	command := &stableCollisionDocker{identity: stableIdentity}
	root := t.TempDir()
	runner := &DockerRunner{
		command: command,
		cleaner: cleaner{command: command, waiter: &recordingWaiter{}, quiescence: cleanupQuiescence, snapshotRoot: root},
		journal: &recordingCleanupJournal{}, locker: newProjectLocker(filepath.Join(root, "locks")),
		temporaryRoot: root, cleanupTimeout: time.Second,
		newClaimID:      func() (string, error) { return testClaimID, nil },
		newGenerationID: testGenerationGenerator(),
	}
	_, err := runner.Run(context.Background(), request, func(context.Context, Instance) error {
		t.Fatal("callback ran after a pre-existing collision")
		return nil
	})
	if !errors.Is(err, ErrProjectExists) {
		t.Fatalf("Run error = %v, want ErrProjectExists", err)
	}
	if command.removed {
		t.Fatal("runner deleted the pre-existing stable-label resource")
	}
}

type stableCollisionDocker struct {
	identity identity
	removed  bool
}

func (docker *stableCollisionDocker) run(_ context.Context, _ int64, args ...string) ([]byte, error) {
	joined := strings.Join(args, " ")
	if len(args) >= 2 && args[0] == "volume" && args[1] == "ls" &&
		strings.Contains(joined, "label="+projectLabel+"="+docker.identity.projectName) &&
		!strings.Contains(joined, "label="+sandboxClaimLabel+"=") {
		return []byte("pre-existing-volume\n"), nil
	}
	if len(args) >= 2 && args[0] == "volume" && args[1] == "inspect" {
		return []byte(docker.identity.fingerprint + "\n"), nil
	}
	if len(args) >= 2 && args[1] == "rm" {
		docker.removed = true
	}
	return nil, nil
}

func TestRunnerRejectsReservationWhoseImmediateOwnershipVerificationMismatches(t *testing.T) {
	command := &runnerDocker{includeVolume: true, reservationMismatch: "network"}
	journal := &recordingCleanupJournal{}
	runner := testRunner(t, command, journal)
	_, err := runner.Run(context.Background(), testRunnerRequest("run-reservation-verification", time.Minute), func(context.Context, Instance) error {
		t.Fatal("callback ran after reservation ownership mismatch")
		return nil
	})
	if !errors.Is(err, ErrProjectCollision) {
		t.Fatalf("Run error = %v, want ErrProjectCollision", err)
	}
	if command.upCall != nil {
		t.Fatal("runner started containers before every reservation verified")
	}
	if len(command.resources) != 1 {
		t.Fatalf("live cleanup touched unverified post-create resource: %#v", command.resources)
	}
	command.reservationMismatch = ""
	identity, _ := newIdentity("run-reservation-verification")
	identity, _ = identity.withClaimID(testClaimID)
	startupCleaner := cleaner{command: command, waiter: &recordingWaiter{}, quiescence: cleanupQuiescence}
	if err := startupCleaner.cleanupManifest(context.Background(), identity, journal.resources); err != nil {
		t.Fatalf("startup cleanup after post-create crash window: %v", err)
	}
	if len(command.resources) != 0 {
		t.Fatalf("startup cleanup left durable post-create resource: %#v", command.resources)
	}
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
				newClaimID:      func() (string, error) { return testClaimID, nil },
				newGenerationID: testGenerationGenerator(),
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
		newClaimID:      func() (string, error) { return testClaimID, nil },
		newGenerationID: testGenerationGenerator(),
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
		newClaimID:      func() (string, error) { return testClaimID, nil },
		newGenerationID: testGenerationGenerator(),
	}
}

func testRunnerRequest(runID string, duration time.Duration) Request {
	return Request{
		RunID: runID, ComposeFiles: []string{"testdata/compose.yaml"},
		Limits: Limits{CPUs: "0.5", MemoryBytes: 32 << 20, PIDs: 16, Duration: duration, OutputBytes: 4096},
	}
}

type recordingCleanupJournal struct {
	mu        sync.Mutex
	claimErr  error
	claimID   string
	resources []drill.SandboxResourceClaim
	statuses  []drill.CleanupStatus
}

func (journal *recordingCleanupJournal) AppendSandboxCleanupResource(
	_ context.Context,
	_, _ string,
	resource drill.SandboxResourceClaim,
	_ time.Time,
) error {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	journal.resources = append(journal.resources, resource)
	return nil
}

func (journal *recordingCleanupJournal) ClaimSandboxCleanup(_ context.Context, _, claimID string, _ time.Time) error {
	journal.claimID = claimID
	return journal.claimErr
}

func testGenerationGenerator() func() (string, error) {
	index := 0
	return func() (string, error) {
		index++
		return fmt.Sprintf("%064x", index), nil
	}
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
