package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunnerExecutesTheExactValidatedSnapshotAfterInputsMutate(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "compose.yaml")
	if err := os.WriteFile(source, []byte("services:\n  worker:\n    image: alpine\n"), 0o600); err != nil {
		t.Fatalf("write source Compose file: %v", err)
	}
	t.Setenv("REHEARSE_MUTATION_INPUT", "approved")
	command := &mutationDocker{t: t, source: source}
	runner := &DockerRunner{
		command: command,
		cleaner: cleaner{command: command, waiter: &recordingWaiter{}, quiescence: cleanupQuiescence, snapshotRoot: root},
		journal: &recordingCleanupJournal{},
		locker:  newProjectLocker(filepath.Join(root, "locks")), temporaryRoot: root, cleanupTimeout: time.Second,
		newClaimID: func() (string, error) { return testClaimID, nil },
	}
	request := Request{
		RunID: "run-immutable-snapshot", ComposeFiles: []string{source}, ProjectDirectory: root,
		Limits: Limits{CPUs: "0.5", MemoryBytes: 32 << 20, PIDs: 16, Duration: time.Minute, OutputBytes: 4096},
	}
	if _, err := runner.Run(context.Background(), request, func(context.Context, Instance) error { return nil }); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !command.upObserved {
		t.Fatal("Compose up did not consume the protected snapshot")
	}
	identity, _ := newIdentity(request.RunID)
	if _, err := os.Stat(snapshotDirectory(root, identity)); !os.IsNotExist(err) {
		t.Fatalf("snapshot directory survived cleanup: %v", err)
	}
}

type mutationDocker struct {
	t           *testing.T
	source      string
	configCalls int
	rendered    []byte
	upObserved  bool
	resources   map[string]inspectedResource
}

func (docker *mutationDocker) run(_ context.Context, _ int64, args ...string) ([]byte, error) {
	if containsSequence(args, "config", "--format", "json") {
		docker.configCalls++
		if docker.configCalls == 1 {
			return []byte(`{"services":{"worker":{"image":"alpine","environment":{"TOKEN":"approved"}}}}`), nil
		}
		policyPath := lastFileArgument(args)
		policy, err := os.ReadFile(policyPath)
		if err != nil {
			docker.t.Fatalf("read generated policy: %v", err)
		}
		var model map[string]any
		if err := json.Unmarshal(policy, &model); err != nil {
			docker.t.Fatalf("decode generated policy: %v", err)
		}
		service := model["services"].(map[string]any)["worker"].(map[string]any)
		service["image"] = "alpine"
		service["environment"] = map[string]string{"TOKEN": "approved-$value"}
		docker.rendered, err = json.Marshal(model)
		if err != nil {
			docker.t.Fatalf("encode rendered snapshot: %v", err)
		}
		return docker.rendered, nil
	}
	if containsSequence(args, "image", "inspect") {
		return []byte(`{"id":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","os":"linux","architecture":"amd64","variant":"","volumes":null}`), nil
	}
	if len(args) >= 2 && (args[0] == "network" || args[0] == "volume") && args[1] == "create" {
		if docker.resources == nil {
			docker.resources = make(map[string]inspectedResource)
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
		name := args[len(args)-1]
		docker.resources[args[0]+"\x00"+name] = inspectedResource{DaemonID: args[0] + "-id", Name: name, Labels: labels}
		return []byte(name + "\n"), nil
	}
	if len(args) >= 2 && (args[0] == "network" || args[0] == "volume") && args[1] == "inspect" {
		for key, resource := range docker.resources {
			if strings.HasPrefix(key, args[0]+"\x00") && (resource.Name == args[len(args)-1] || resource.DaemonID == args[len(args)-1]) {
				return json.Marshal(resource)
			}
		}
		return nil, os.ErrNotExist
	}
	if containsSequence(args, "up", "--detach") {
		docker.upObserved = true
		if err := os.WriteFile(docker.source, []byte("services:\n  worker:\n    privileged: true\n"), 0o600); err != nil {
			docker.t.Fatalf("mutate original Compose file: %v", err)
		}
		if err := os.Setenv("REHEARSE_MUTATION_INPUT", "changed"); err != nil {
			docker.t.Fatalf("mutate interpolation input: %v", err)
		}
		snapshotPath := lastFileArgument(args)
		if snapshotPath == docker.source || !strings.HasSuffix(snapshotPath, snapshotFileName) {
			docker.t.Fatalf("up file = %q, want immutable snapshot only", snapshotPath)
		}
		for _, argument := range args {
			if argument == docker.source || strings.HasSuffix(argument, overrideFileName) {
				docker.t.Fatalf("up rereads mutable input: %v", args)
			}
		}
		actual, err := os.ReadFile(snapshotPath)
		if err != nil {
			docker.t.Fatalf("read execution snapshot: %v", err)
		}
		want, err := rewriteSnapshotImages(escapeSnapshotInterpolation(docker.rendered), map[string]string{"worker": testImageID})
		if err != nil {
			docker.t.Fatalf("build expected pinned snapshot: %v", err)
		}
		var model composeModel
		if err := json.Unmarshal(want, &model); err != nil {
			docker.t.Fatalf("decode expected pinned snapshot: %v", err)
		}
		identity, err := newIdentity("run-immutable-snapshot")
		if err != nil {
			docker.t.Fatalf("build expected identity: %v", err)
		}
		identity, err = identity.withClaimID(testClaimID)
		if err != nil {
			docker.t.Fatalf("build expected claimed identity: %v", err)
		}
		want, err = rewriteSnapshotReservedResources(want, identity, model)
		if err != nil {
			docker.t.Fatalf("build expected reserved snapshot: %v", err)
		}
		if !bytes.Equal(actual, want) {
			docker.t.Fatal("validated and executed snapshot bytes differ")
		}
		assertProtectedMode(docker.t, filepath.Dir(snapshotPath), 0o700)
		assertProtectedMode(docker.t, snapshotPath, 0o600)
		projectName := argumentAfter(args, "--project-name")
		var executed composeModel
		if err := json.Unmarshal(actual, &executed); err != nil {
			docker.t.Fatalf("decode executed snapshot: %v", err)
		}
		docker.resources["container\x00"+projectName+"-worker-1"] = inspectedResource{
			DaemonID: "container-id", Name: projectName + "-worker-1", Labels: executed.Services["worker"].Labels,
		}
		return nil, nil
	}
	if len(args) >= 2 && args[1] == "ls" {
		joined := strings.Join(args, " ")
		for key, resource := range docker.resources {
			if !strings.HasPrefix(key, args[0]+"\x00") {
				continue
			}
			if strings.Contains(joined, "name=^") && (strings.Contains(joined, "name=^"+resource.Name+"$") || strings.Contains(joined, "name=^/"+resource.Name+"$")) {
				return []byte(resource.DaemonID + "\n"), nil
			}
			if strings.Contains(joined, "label="+sandboxClaimLabel+"="+testClaimID) {
				return []byte(resource.DaemonID + "\n"), nil
			}
		}
		return nil, nil
	}
	if len(args) >= 2 && args[1] == "inspect" {
		for key, resource := range docker.resources {
			if strings.HasPrefix(key, args[0]+"\x00") && (resource.Name == args[len(args)-1] || resource.DaemonID == args[len(args)-1]) {
				return json.Marshal(resource)
			}
		}
		return nil, os.ErrNotExist
	}
	if len(args) >= 4 && args[1] == "rm" {
		for key, resource := range docker.resources {
			if strings.HasPrefix(key, args[0]+"\x00") && (resource.Name == args[len(args)-1] || resource.DaemonID == args[len(args)-1]) {
				delete(docker.resources, key)
			}
		}
	}
	return nil, nil
}

func lastFileArgument(args []string) string {
	for index := len(args) - 2; index >= 0; index-- {
		if args[index] == "--file" {
			return args[index+1]
		}
	}
	return ""
}

func assertProtectedMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat protected path: %v", err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode for %s = %o, want %o", path, got, want)
	}
}
