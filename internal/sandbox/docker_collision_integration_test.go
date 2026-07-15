package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRealDockerLateStableCollisionPreservesCollisionAndCleansReservation(t *testing.T) {
	if os.Getenv("REHEARSE_DOCKER_INTEGRATION") != "1" {
		t.Skip("set REHEARSE_DOCKER_INTEGRATION=1 to run real Docker tests")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("Docker is unavailable: %v", err)
	}

	runID := "integration-late-stable-collision"
	stable, _ := newIdentity(runID)
	volumeName := stable.projectName + "_work"
	networkName := stable.projectName + "_default"
	_ = exec.Command("docker", "volume", "rm", "--force", volumeName).Run()
	_ = exec.Command("docker", "network", "rm", "--force", networkName).Run()
	t.Cleanup(func() {
		_ = exec.Command("docker", "volume", "rm", "--force", volumeName).Run()
		_ = exec.Command("docker", "network", "rm", "--force", networkName).Run()
	})

	root := t.TempDir()
	command := &lateCollisionRealDocker{delegate: newCommandExecutor("docker")}
	runner := &DockerRunner{
		command: command,
		cleaner: cleaner{command: command, waiter: timerWaiter{}, quiescence: cleanupQuiescence, snapshotRoot: root},
		journal: &recordingCleanupJournal{}, locker: newProjectLocker(filepath.Join(root, "locks")),
		temporaryRoot: root, cleanupTimeout: 10 * time.Second,
		newClaimID: func() (string, error) { return testClaimID, nil },
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

	identity, _ := newIdentity("integration-daemon-replacement")
	identity, _ = identity.withClaimID(testClaimID)
	name := identity.projectName + "_replacement"
	_ = exec.Command("docker", "network", "rm", "--force", name).Run()
	t.Cleanup(func() { _ = exec.Command("docker", "network", "rm", "--force", name).Run() })
	create := func() string {
		args := []string{"network", "create", "--driver", "bridge", "--internal"}
		for _, label := range sortedOwnershipLabels(identity) {
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
	originalID := create()
	if output, err := exec.Command("docker", "network", "rm", "--force", originalID).CombinedOutput(); err != nil {
		t.Fatalf("remove original network: %v\n%s", err, output)
	}
	replacementID := create()
	if replacementID == originalID {
		t.Fatalf("replacement reused daemon ID %q", replacementID)
	}

	ledger := newCreatedResourceLedger()
	ledger.add(createdResource{kind: "network", name: name, daemonID: originalID})
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

type lateCollisionRealDocker struct {
	delegate dockerCommand
	injected bool
}

func (docker *lateCollisionRealDocker) run(ctx context.Context, limit int64, args ...string) ([]byte, error) {
	if !docker.injected && len(args) >= 2 && args[0] == "volume" && args[1] == "create" {
		docker.injected = true
		stableArgs := make([]string, 0, len(args))
		for index := 0; index < len(args); index++ {
			if args[index] == "--label" && index+1 < len(args) && strings.HasPrefix(args[index+1], sandboxClaimLabel+"=") {
				index++
				continue
			}
			stableArgs = append(stableArgs, args[index])
		}
		if _, err := docker.delegate.run(ctx, limit, stableArgs...); err != nil {
			return nil, err
		}
	}
	return docker.delegate.run(ctx, limit, args...)
}
