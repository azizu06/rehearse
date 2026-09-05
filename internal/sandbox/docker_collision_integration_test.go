package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/azizu06/rehearse/internal/drill"
)

const internalDockerTestOwnerLabel = "com.azizu06.rehearse-test-owner"

func TestRealDockerLateStableCollisionPreservesCollisionAndCleansReservation(t *testing.T) {
	if os.Getenv("REHEARSE_DOCKER_INTEGRATION") != "1" {
		t.Skip("set REHEARSE_DOCKER_INTEGRATION=1 to run real Docker tests")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("Docker is unavailable: %v", err)
	}

	runID := uniqueInternalDockerRunID(t, "integration-late-stable-collision")
	stable, _ := newIdentity(runID)
	volumeName := stable.projectName + "_work"
	networkName := stable.projectName + "_default"
	registerOwnedDockerCleanup(t, "volume", volumeName, map[string]string{internalDockerTestOwnerLabel: runID})
	claimed, _ := stable.withClaimID(testClaimID)
	networkLabels, _ := resourceLabels(claimed, strings.Repeat("0", 63)+"1")
	registerOwnedDockerCleanup(t, "network", networkName, networkLabels)

	root := t.TempDir()
	command := &lateCollisionRealDocker{delegate: newCommandExecutor("docker"), testOwner: runID}
	runner := &DockerRunner{
		command: command,
		cleaner: cleaner{command: command, waiter: timerWaiter{}, quiescence: cleanupQuiescence, snapshotRoot: root},
		journal: &recordingCleanupJournal{}, locker: newProjectLocker(filepath.Join(root, "locks")),
		temporaryRoot: root, cleanupTimeout: 10 * time.Second,
		newClaimID:      func() (string, error) { return testClaimID, nil },
		newGenerationID: testGenerationGenerator(),
	}
	request := Request{
		RunID: runID, ComposeFiles: []string{filepath.Join("testdata", "compose.yaml")},
		Limits: Limits{CPUs: "0.25", MemoryBytes: 32 << 20, PIDs: 16, Duration: 20 * time.Second, OutputBytes: 1 << 20},
	}
	_, err := runner.Run(context.Background(), request, func(context.Context, Instance) error {
		t.Fatal("callback ran after late reservation collision")
		return nil
	})
	if !errors.Is(err, ErrProjectCollision) {
		t.Fatalf("Run error = %v, want ErrProjectCollision", err)
	}
	if output, err := exec.Command("docker", "volume", "inspect", "--format", `{{ index .Labels "dev.rehearse.sandbox-claim" }}`, volumeName).CombinedOutput(); err != nil || strings.TrimSpace(string(output)) != "" {
		t.Fatalf("late-colliding volume changed or disappeared: output=%q error=%v", output, err)
	}
	if output, err := exec.Command("docker", "network", "ls", "--quiet", "--filter", "name=^"+networkName+"$").CombinedOutput(); err != nil || strings.TrimSpace(string(output)) != "" {
		t.Fatalf("invocation-created network survived partial cleanup: output=%q error=%v", output, err)
	}
}

func TestRealDockerLiveCleanupPreservesDaemonIdentityReplacement(t *testing.T) {
	if os.Getenv("REHEARSE_DOCKER_INTEGRATION") != "1" {
		t.Skip("set REHEARSE_DOCKER_INTEGRATION=1 to run real Docker tests")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("Docker is unavailable: %v", err)
	}

	identity, _ := newIdentity(uniqueInternalDockerRunID(t, "integration-daemon-replacement"))
	identity, _ = identity.withClaimID(testClaimID)
	name := identity.projectName + "_replacement"
	originalGeneration := strings.Repeat("4", 64)
	replacementGeneration := strings.Repeat("5", 64)
	originalLabels, _ := resourceLabels(identity, originalGeneration)
	replacementLabels, _ := resourceLabels(identity, replacementGeneration)
	registerOwnedDockerCleanup(t, "network", name, originalLabels)
	registerOwnedDockerCleanup(t, "network", name, replacementLabels)
	create := func(generation string) string {
		args := []string{"network", "create", "--driver", "bridge", "--internal"}
		labels, _ := resourceLabels(identity, generation)
		for _, label := range sortedLabels(labels) {
			args = append(args, "--label", label)
		}
		args = append(args, name)
		if output, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
			t.Fatalf("create replacement network: %v\n%s", err, output)
		}
		output, err := exec.Command("docker", "network", "inspect", "--format", `{{ .Id }}`, name).CombinedOutput()
		if err != nil {
			t.Fatalf("inspect replacement network: %v\n%s", err, output)
		}
		return strings.TrimSpace(string(output))
	}
	originalID := create(originalGeneration)
	removeOwnedDockerResource(t, "network", name, originalLabels, true)
	replacementID := create(replacementGeneration)
	if replacementID == originalID {
		t.Fatalf("replacement reused daemon ID %q", replacementID)
	}

	ledger := newCreatedResourceLedger()
	ledger.add(createdResource{kind: "network", name: name, daemonID: originalID, generation: originalGeneration})
	command := newCommandExecutor("docker")
	cleaner := cleaner{command: command, waiter: timerWaiter{}, quiescence: cleanupQuiescence}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := cleaner.cleanupCreated(ctx, identity, ledger); err != nil {
		t.Fatalf("cleanupCreated: %v", err)
	}
	output, err := exec.Command("docker", "network", "inspect", "--format", `{{ .Id }}`, name).CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != replacementID {
		t.Fatalf("replacement network did not survive: output=%q error=%v", output, err)
	}
}

func TestRealDockerLiveCleanupPreservesRapidVolumeGenerationReplacement(t *testing.T) {
	if os.Getenv("REHEARSE_DOCKER_INTEGRATION") != "1" {
		t.Skip("set REHEARSE_DOCKER_INTEGRATION=1 to run real Docker tests")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("Docker is unavailable: %v", err)
	}

	identity, _ := newIdentity(uniqueInternalDockerRunID(t, "integration-volume-generation-replacement"))
	identity, _ = identity.withClaimID(testClaimID)
	name := identity.projectName + "_replacement"
	create := func(generation string) {
		args := []string{"volume", "create", "--driver", "local"}
		labels, _ := resourceLabels(identity, generation)
		for _, label := range sortedLabels(labels) {
			args = append(args, "--label", label)
		}
		args = append(args, name)
		if output, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
			t.Fatalf("create replacement volume: %v\n%s", err, output)
		}
	}
	originalGeneration := strings.Repeat("6", 64)
	replacementGeneration := strings.Repeat("7", 64)
	originalLabels, _ := resourceLabels(identity, originalGeneration)
	replacementLabels, _ := resourceLabels(identity, replacementGeneration)
	registerOwnedDockerCleanup(t, "volume", name, originalLabels)
	registerOwnedDockerCleanup(t, "volume", name, replacementLabels)
	create(originalGeneration)
	removeOwnedDockerResource(t, "volume", name, originalLabels, true)
	create(replacementGeneration)

	ledger := newCreatedResourceLedger()
	ledger.add(createdResource{kind: "volume", name: name, generation: originalGeneration})
	cleaner := cleaner{command: newCommandExecutor("docker"), waiter: timerWaiter{}, quiescence: cleanupQuiescence}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := cleaner.cleanupCreated(ctx, identity, ledger); err != nil {
		t.Fatalf("cleanupCreated: %v", err)
	}
	output, err := exec.Command("docker", "volume", "inspect", "--format", `{{ index .Labels "dev.rehearse.resource-generation" }}`, name).CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != replacementGeneration {
		t.Fatalf("replacement volume did not survive: output=%q error=%v", output, err)
	}
	startup := NewCleaner(CleanerOptions{Binary: "docker", LockRoot: t.TempDir()})
	manifest := []drill.SandboxResourceClaim{{Kind: "volume", Name: name, Generation: originalGeneration}}
	if err := startup.Cleanup(ctx, identity.runID, identity.claimID, manifest); err != nil {
		t.Fatalf("startup cleanup replacement check: %v", err)
	}
	output, err = exec.Command("docker", "volume", "inspect", "--format", `{{ index .Labels "dev.rehearse.resource-generation" }}`, name).CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != replacementGeneration {
		t.Fatalf("startup cleanup deleted replacement volume: output=%q error=%v", output, err)
	}
}

type lateCollisionRealDocker struct {
	delegate  dockerCommand
	injected  bool
	testOwner string
}

func (docker *lateCollisionRealDocker) run(ctx context.Context, limit int64, args ...string) ([]byte, error) {
	if !docker.injected && len(args) >= 2 && args[0] == "volume" && args[1] == "create" {
		docker.injected = true
		stableArgs := make([]string, 0, len(args))
		for index := 0; index < len(args); index++ {
			if args[index] == "--label" && index+1 < len(args) {
				label := args[index+1]
				if strings.HasPrefix(label, sandboxClaimLabel+"=") || strings.HasPrefix(label, resourceGenerationLabel+"=") {
					index++
					continue
				}
			}
			stableArgs = append(stableArgs, args[index])
		}
		stableArgs = append(stableArgs[:len(stableArgs)-1], "--label", internalDockerTestOwnerLabel+"="+docker.testOwner, stableArgs[len(stableArgs)-1])
		if _, err := docker.delegate.run(ctx, limit, stableArgs...); err != nil {
			return nil, err
		}
	}
	return docker.delegate.run(ctx, limit, args...)
}

func uniqueInternalDockerRunID(t *testing.T, prefix string) string {
	t.Helper()
	value, err := generateClaimID()
	if err != nil {
		t.Fatalf("generate unique Docker run ID: %v", err)
	}
	return prefix + "-" + value[:16]
}

func registerOwnedDockerCleanup(t *testing.T, kind, name string, expected map[string]string) {
	t.Helper()
	t.Cleanup(func() { removeOwnedDockerResource(t, kind, name, expected, false) })
}

func removeOwnedDockerResource(t *testing.T, kind, name string, expected map[string]string, required bool) {
	t.Helper()
	output, err := exec.Command("docker", kind, "inspect", "--format", `{{json .Labels}}`, name).CombinedOutput()
	if err != nil {
		if required {
			t.Fatalf("inspect owned test %s %q: %v\n%s", kind, name, err, output)
		}
		return
	}
	var labels map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(output))), &labels); err != nil {
		t.Fatalf("decode owned test %s %q labels: %v", kind, name, err)
	}
	for key, value := range expected {
		if labels[key] != value {
			if required {
				t.Fatalf("refusing to remove %s %q with %s=%q, want %q", kind, name, key, labels[key], value)
			}
			return
		}
	}
	remove := []string{kind, "rm", name}
	if kind == "volume" {
		remove = []string{kind, "rm", "--force", name}
	}
	if output, err := exec.Command("docker", remove...).CombinedOutput(); err != nil {
		t.Fatalf("remove owned test %s %q: %v\n%s", kind, name, err, output)
	}
}
