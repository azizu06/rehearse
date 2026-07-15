package sandbox_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azizu06/rehearse/internal/drill"
	"github.com/azizu06/rehearse/internal/journal"
	"github.com/azizu06/rehearse/internal/sandbox"
)

func TestDockerRunnerAppliesIsolationLabelsAndResourceLimits(t *testing.T) {
	requireDockerIntegration(t)

	runner := sandbox.NewDockerRunner(sandbox.DockerRunnerOptions{
		Binary:         "docker",
		CleanupJournal: newCleanupJournal(t, "integration-run-limits"),
		TemporaryRoot:  t.TempDir(),
		CleanupTimeout: 15 * time.Second,
	})
	request := sandbox.Request{
		RunID:        "integration-run-limits",
		ComposeFiles: []string{filepath.Join("testdata", "compose.yaml")},
		Limits: sandbox.Limits{
			CPUs:        "0.50",
			MemoryBytes: 64 << 20,
			PIDs:        32,
			Duration:    30 * time.Second,
			OutputBytes: 1 << 20,
		},
	}

	var projectName string
	result, err := runner.Run(context.Background(), request, func(ctx context.Context, instance sandbox.Instance) error {
		projectName = instance.ProjectName
		containerID := dockerOutput(t, "container", "ls", "--all", "--quiet",
			"--filter", "label=dev.rehearse.managed=true",
			"--filter", "label=dev.rehearse.run-id="+request.RunID,
		)
		if containerID == "" {
			t.Fatal("runner created no Rehearse-labeled container")
		}

		var inspected struct {
			Config struct {
				Labels map[string]string `json:"Labels"`
			} `json:"Config"`
			HostConfig struct {
				Memory    int64 `json:"Memory"`
				NanoCPUs  int64 `json:"NanoCpus"`
				PidsLimit int64 `json:"PidsLimit"`
				LogConfig struct {
					Type   string            `json:"Type"`
					Config map[string]string `json:"Config"`
				} `json:"LogConfig"`
			} `json:"HostConfig"`
		}
		raw := dockerOutput(t, "container", "inspect", containerID)
		var records []json.RawMessage
		if err := json.Unmarshal([]byte(raw), &records); err != nil || len(records) != 1 {
			t.Fatalf("decode container inspect envelope: records=%d error=%v", len(records), err)
		}
		if err := json.Unmarshal(records[0], &inspected); err != nil {
			t.Fatalf("decode container inspect: %v", err)
		}
		if got := inspected.Config.Labels["dev.rehearse.project"]; got != projectName {
			t.Fatalf("project label = %q, want %q", got, projectName)
		}
		if got := inspected.Config.Labels["dev.rehearse.run-fingerprint"]; got != fingerprint(request.RunID) {
			t.Fatalf("fingerprint label = %q, want full run digest", got)
		}
		if got := inspected.HostConfig.Memory; got != request.Limits.MemoryBytes {
			t.Fatalf("memory limit = %d, want %d", got, request.Limits.MemoryBytes)
		}
		if got := inspected.HostConfig.NanoCPUs; got != 500_000_000 {
			t.Fatalf("CPU limit = %d NanoCPUs, want 500000000", got)
		}
		if got := inspected.HostConfig.PidsLimit; got != request.Limits.PIDs {
			t.Fatalf("PID limit = %d, want %d", got, request.Limits.PIDs)
		}
		if got := inspected.HostConfig.LogConfig; got.Type != "local" || got.Config["compress"] != "false" || got.Config["max-file"] != "1" || got.Config["max-size"] != "1048576" {
			t.Fatalf("log limit = %#v, want bounded local logs", got)
		}
		for _, resource := range []string{"network", "volume"} {
			id := dockerOutput(t, resource, "ls", "--quiet",
				"--filter", "label=dev.rehearse.managed=true",
				"--filter", "label=dev.rehearse.run-fingerprint="+fingerprint(request.RunID),
			)
			if id == "" {
				t.Fatalf("runner created no labeled %s", resource)
			}
			label := dockerOutput(t, resource, "inspect", "--format", `{{ index .Labels "dev.rehearse.project" }}`, id)
			if label != projectName {
				t.Fatalf("%s project label = %q, want %q", resource, label, projectName)
			}
			if resource == "network" {
				internal := dockerOutput(t, resource, "inspect", "--format", `{{ .Internal }}`, id)
				if internal != "true" {
					t.Fatalf("network internal = %q, want true", internal)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	wantProject := "rehearse-" + fingerprint(request.RunID)[:24]
	if result.ProjectName != projectName || projectName != wantProject {
		t.Fatalf("result project = %q, callback project = %q", result.ProjectName, projectName)
	}

	assertNoRunResources(t, request.RunID)
}

func TestCleanerWaitsForAndRemovesDelayedResources(t *testing.T) {
	requireDockerIntegration(t)

	runID := "integration-delayed-cleanup"
	runFingerprint := fingerprint(runID)
	projectName := "rehearse-" + runFingerprint[:24]
	volumeName := "rehearse-delayed-" + runFingerprint[:12]
	_ = exec.Command("docker", "volume", "rm", "--force", volumeName).Run()
	t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", "--force", volumeName).Run() })
	created := make(chan struct{})
	go func() {
		time.Sleep(200 * time.Millisecond)
		command := exec.Command("docker", "volume", "create",
			"--label", "dev.rehearse.managed=true",
			"--label", "dev.rehearse.run-fingerprint="+runFingerprint,
			"--label", "dev.rehearse.project="+projectName,
			volumeName,
		)
		_ = command.Run()
		close(created)
	}()

	cleaner := sandbox.NewCleaner(sandbox.CleanerOptions{Binary: "docker"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	if err := cleaner.Cleanup(ctx, runID); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	<-created
	if elapsed := time.Since(started); elapsed < time.Second {
		t.Fatalf("cleanup returned after %s; want separated empty scans with 500ms quiescence", elapsed)
	}
	if got := dockerOutput(t, "volume", "ls", "--quiet", "--filter", "name="+volumeName); got != "" {
		t.Fatalf("delayed volume remains: %s", got)
	}
}

func TestCleanerDoesNotDeleteUnrelatedDockerResources(t *testing.T) {
	requireDockerIntegration(t)

	name := "rehearse-unrelated-" + fingerprint(t.Name())[:12]
	dockerOutput(t, "volume", "create", name)
	t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", "--force", name).Run() })

	cleaner := sandbox.NewCleaner(sandbox.CleanerOptions{Binary: "docker"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := cleaner.Cleanup(ctx, "integration-unrelated-scope"); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if got := dockerOutput(t, "volume", "ls", "--quiet", "--filter", "name="+name); got == "" {
		t.Fatal("cleaner deleted an unrelated volume")
	}
}

func TestDockerRunnerCleansAfterCancellation(t *testing.T) {
	requireDockerIntegration(t)

	runner := sandbox.NewDockerRunner(sandbox.DockerRunnerOptions{
		Binary:         "docker",
		CleanupJournal: newCleanupJournal(t, "integration-run-cancelled"),
		TemporaryRoot:  t.TempDir(),
		LockRoot:       t.TempDir(),
		CleanupTimeout: 5 * time.Second,
	})
	request := sandbox.Request{
		RunID:        "integration-run-cancelled",
		ComposeFiles: []string{filepath.Join("testdata", "compose.yaml")},
		Limits: sandbox.Limits{
			CPUs: "0.25", MemoryBytes: 32 << 20, PIDs: 16,
			Duration: 10 * time.Second, OutputBytes: 1 << 20,
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, err := runner.Run(ctx, request, func(context.Context, sandbox.Instance) error {
		cancel()
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	assertNoRunResources(t, request.RunID)
}

func TestDockerRunnerCleansAfterCallbackFailure(t *testing.T) {
	requireDockerIntegration(t)

	runner := sandbox.NewDockerRunner(sandbox.DockerRunnerOptions{
		Binary: "docker", CleanupJournal: newCleanupJournal(t, "integration-run-failed"), TemporaryRoot: t.TempDir(), LockRoot: t.TempDir(), CleanupTimeout: 5 * time.Second,
	})
	request := sandbox.Request{
		RunID: "integration-run-failed", ComposeFiles: []string{filepath.Join("testdata", "compose.yaml")},
		Limits: sandbox.Limits{CPUs: "0.25", MemoryBytes: 32 << 20, PIDs: 16, Duration: 10 * time.Second, OutputBytes: 1 << 20},
	}
	wantErr := errors.New("drill stage failed")
	_, err := runner.Run(context.Background(), request, func(context.Context, sandbox.Instance) error { return wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want callback failure", err)
	}
	assertNoRunResources(t, request.RunID)
}

func TestDockerRunnerEnforcesTimeLimitAndCleans(t *testing.T) {
	requireDockerIntegration(t)

	runner := sandbox.NewDockerRunner(sandbox.DockerRunnerOptions{
		Binary: "docker", CleanupJournal: newCleanupJournal(t, "integration-run-timeout"), TemporaryRoot: t.TempDir(), LockRoot: t.TempDir(), CleanupTimeout: 5 * time.Second,
	})
	request := sandbox.Request{
		RunID: "integration-run-timeout", ComposeFiles: []string{filepath.Join("testdata", "compose.yaml")},
		Limits: sandbox.Limits{CPUs: "0.25", MemoryBytes: 32 << 20, PIDs: 16, Duration: time.Second, OutputBytes: 1 << 20},
	}
	_, err := runner.Run(context.Background(), request, func(ctx context.Context, _ sandbox.Instance) error {
		<-ctx.Done()
		return context.Cause(ctx)
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run error = %v, want context deadline", err)
	}
	assertNoRunResources(t, request.RunID)
}

func TestDockerRunnerNamesDoNotCollideAcrossConcurrentRuns(t *testing.T) {
	requireDockerIntegration(t)

	root := t.TempDir()
	lockRoot := t.TempDir()
	entered := make(chan string, 2)
	release := make(chan struct{})
	errorsSeen := make(chan error, 2)
	projects := make(chan string, 2)
	cleanupJournal := newCleanupJournal(t, "integration-concurrent-a", "integration-concurrent-b")
	var wait sync.WaitGroup
	for _, runID := range []string{"integration-concurrent-a", "integration-concurrent-b"} {
		runID := runID
		wait.Add(1)
		go func() {
			defer wait.Done()
			runner := sandbox.NewDockerRunner(sandbox.DockerRunnerOptions{
				Binary: "docker", CleanupJournal: cleanupJournal, TemporaryRoot: root, LockRoot: lockRoot, CleanupTimeout: 5 * time.Second,
			})
			request := sandbox.Request{
				RunID: runID, ComposeFiles: []string{filepath.Join("testdata", "compose.yaml")},
				Limits: sandbox.Limits{CPUs: "0.25", MemoryBytes: 32 << 20, PIDs: 16, Duration: 15 * time.Second, OutputBytes: 1 << 20},
			}
			result, err := runner.Run(context.Background(), request, func(_ context.Context, instance sandbox.Instance) error {
				projects <- instance.ProjectName
				entered <- runID
				<-release
				return nil
			})
			if err == nil && result.ProjectName == "" {
				err = errors.New("runner returned an empty project name")
			}
			errorsSeen <- err
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(15 * time.Second):
			close(release)
			t.Fatal("concurrent runner did not reach callback")
		}
	}
	close(release)
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent Run: %v", err)
		}
	}
	first, second := <-projects, <-projects
	if first == second {
		t.Fatalf("concurrent projects collided: %q", first)
	}
	assertNoRunResources(t, "integration-concurrent-a")
	assertNoRunResources(t, "integration-concurrent-b")
}

func TestStartupJanitorRecoversAfterForcedProcessKill(t *testing.T) {
	requireDockerIntegration(t)

	directory := t.TempDir()
	databasePath := filepath.Join(directory, "rehearse.db")
	readyPath := filepath.Join(directory, "ready")
	lockRoot := filepath.Join(directory, "locks")
	runID := "integration-forced-kill"
	command := exec.Command(os.Args[0], "-test.run=^TestDockerRunnerForcedKillHelper$")
	command.Env = append(os.Environ(),
		"REHEARSE_FORCE_KILL_HELPER=1",
		"REHEARSE_FORCE_KILL_DB="+databasePath,
		"REHEARSE_FORCE_KILL_READY="+readyPath,
		"REHEARSE_FORCE_KILL_LOCKS="+lockRoot,
		"REHEARSE_FORCE_KILL_COMPOSE="+filepath.Join(mustWorkingDirectory(t), "testdata", "compose.yaml"),
		"REHEARSE_FORCE_KILL_RUN_ID="+runID,
	)
	if err := command.Start(); err != nil {
		t.Fatalf("start forced-kill helper: %v", err)
	}
	killAndWait := func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
	}
	readyDeadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		if time.Now().After(readyDeadline) {
			killAndWait()
			t.Fatal("forced-kill helper did not create its sandbox")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := dockerOutput(t, "container", "ls", "--all", "--quiet",
		"--filter", "label=dev.rehearse.run-fingerprint="+fingerprint(runID)); got == "" {
		killAndWait()
		t.Fatal("forced-kill helper reported ready without a labeled container")
	}
	killAndWait()
	crashSnapshot := filepath.Join(directory, "rehearse-compose-"+fingerprint(runID))
	if info, err := os.Stat(filepath.Join(crashSnapshot, "compose.json")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("forced kill did not leave a protected reconciliation snapshot: info=%v error=%v", info, err)
	}

	store, err := journal.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatalf("reopen forced-kill journal: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cleaner := sandbox.NewCleaner(sandbox.CleanerOptions{Binary: "docker", LockRoot: lockRoot, TemporaryRoot: directory})
	janitor := sandbox.NewJanitor(store, cleaner, sandbox.JanitorOptions{CleanupTimeout: 10 * time.Second})
	if err := janitor.Reconcile(context.Background()); err != nil {
		t.Fatalf("startup reconciliation: %v", err)
	}
	assertNoRunResources(t, runID)
	if _, err := os.Stat(crashSnapshot); !os.IsNotExist(err) {
		t.Fatalf("startup janitor left crash snapshot behind: %v", err)
	}
	run, err := store.Run(context.Background(), runID)
	if err != nil {
		t.Fatalf("load reconciled run: %v", err)
	}
	if run.Outcome != drill.OutcomeFailed || run.Cleanup != drill.CleanupSucceeded || run.NeedsReconciliation {
		t.Fatalf("reconciled run = %#v", run)
	}
}

func TestDockerRunnerForcedKillHelper(t *testing.T) {
	if os.Getenv("REHEARSE_FORCE_KILL_HELPER") != "1" {
		t.Skip("forced-kill subprocess helper")
	}
	ctx := context.Background()
	store, err := journal.Open(ctx, os.Getenv("REHEARSE_FORCE_KILL_DB"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	now := time.Now().UTC()
	plan := drill.Plan{
		ID: "forced-kill-plan", Name: "forced kill plan", Version: 1, CreatedAt: now,
		Spec: drill.PlanSpec{SourceKind: "source", TargetKind: "target"},
	}
	if err := store.CreatePlan(ctx, plan); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	runID := os.Getenv("REHEARSE_FORCE_KILL_RUN_ID")
	if _, err := store.CreateRun(ctx, runID, plan.ID, plan.Version, now); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	runner := sandbox.NewDockerRunner(sandbox.DockerRunnerOptions{
		Binary: "docker", TemporaryRoot: filepath.Dir(os.Getenv("REHEARSE_FORCE_KILL_READY")),
		CleanupJournal: store, LockRoot: os.Getenv("REHEARSE_FORCE_KILL_LOCKS"), CleanupTimeout: 10 * time.Second,
	})
	request := sandbox.Request{
		RunID: runID, ComposeFiles: []string{os.Getenv("REHEARSE_FORCE_KILL_COMPOSE")},
		Limits: sandbox.Limits{CPUs: "0.25", MemoryBytes: 32 << 20, PIDs: 16, Duration: time.Minute, OutputBytes: 1 << 20},
	}
	_, err = runner.Run(ctx, request, func(context.Context, sandbox.Instance) error {
		if err := os.WriteFile(os.Getenv("REHEARSE_FORCE_KILL_READY"), []byte("ready"), 0o600); err != nil {
			return err
		}
		select {}
	})
	t.Fatalf("forced-kill helper returned unexpectedly: %v", err)
}

func mustWorkingDirectory(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	return directory
}

func newCleanupJournal(t *testing.T, runIDs ...string) *journal.Store {
	t.Helper()
	ctx := context.Background()
	store, err := journal.Open(ctx, filepath.Join(t.TempDir(), "rehearse.db"))
	if err != nil {
		t.Fatalf("open cleanup journal: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	plan := drill.Plan{
		ID: "sandbox-plan", Name: "sandbox integration plan", Version: 1, CreatedAt: now,
		Spec: drill.PlanSpec{SourceKind: "source", TargetKind: "target"},
	}
	if err := store.CreatePlan(ctx, plan); err != nil {
		t.Fatalf("create cleanup plan: %v", err)
	}
	for index, runID := range runIDs {
		if _, err := store.CreateRun(ctx, runID, plan.ID, plan.Version, now.Add(time.Duration(index)*time.Nanosecond)); err != nil {
			t.Fatalf("create cleanup run %q: %v", runID, err)
		}
	}
	return store
}

func requireDockerIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("REHEARSE_DOCKER_INTEGRATION") != "1" {
		t.Skip("set REHEARSE_DOCKER_INTEGRATION=1 to run real Docker tests")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("Docker is unavailable: %v", err)
	}
}

func dockerOutput(t *testing.T, args ...string) string {
	t.Helper()
	output, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func assertNoRunResources(t *testing.T, runID string) {
	t.Helper()
	for _, resource := range []string{"container", "network", "volume"} {
		if got := dockerOutput(t, resource, "ls", "--quiet",
			"--filter", "label=dev.rehearse.managed=true",
			"--filter", "label=dev.rehearse.run-fingerprint="+fingerprint(runID),
		); got != "" {
			t.Fatalf("%s resources remain for run %q: %s", resource, runID, got)
		}
	}
}

func fingerprint(value string) string {
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", digest)
}
