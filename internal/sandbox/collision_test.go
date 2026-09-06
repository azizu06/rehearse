package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestResolvedCollisionPreflightRejectsUnlabeledExactVolumeName(t *testing.T) {
	identity, _ := newIdentity("run-unlabeled-volume")
	volumeName := identity.projectName + "_work"
	model := composeModel{
		Services: map[string]composeService{"worker": {Image: "alpine"}},
		Volumes:  map[string]composeResource{"work": {Name: volumeName}},
	}
	tests := []struct {
		name   string
		labels map[string]string
	}{
		{name: "absent labels", labels: map[string]string{}},
		{name: "mismatched fingerprint", labels: map[string]string{
			managedLabel: "true", projectLabel: identity.projectName, runIDLabel: identity.runID,
			runFingerprintLabel: strings.Repeat("f", 64),
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := &exactCollisionDocker{kind: "volume", name: volumeName, labels: test.labels}
			err := ensureResolvedProjectVacant(context.Background(), command, identity, model)
			if !errors.Is(err, ErrProjectCollision) {
				t.Fatalf("ensureResolvedProjectVacant error = %v, want ErrProjectCollision", err)
			}
			for _, call := range command.calls {
				if len(call) >= 2 && call[0] == "volume" && call[1] == "ls" {
					joined := strings.Join(call, " ")
					if strings.Contains(joined, "label=") || !strings.Contains(joined, "name=^"+volumeName+"$") {
						t.Fatalf("exact-name collision query = %v", call)
					}
				}
			}
		})
	}
}

func TestResolvedCollisionPreflightNeverAdoptsOwnedExactName(t *testing.T) {
	identity, _ := newIdentity("run-owned-volume")
	volumeName := identity.projectName + "_work"
	command := &exactCollisionDocker{kind: "volume", name: volumeName, labels: identity.labels()}
	model := composeModel{Volumes: map[string]composeResource{"work": {Name: volumeName}}}
	if err := ensureResolvedProjectVacant(context.Background(), command, identity, model); !errors.Is(err, ErrProjectExists) {
		t.Fatalf("ensureResolvedProjectVacant error = %v, want ErrProjectExists", err)
	}
}

type exactCollisionDocker struct {
	kind   string
	name   string
	labels map[string]string
	calls  [][]string
}

func (docker *exactCollisionDocker) run(_ context.Context, _ int64, args ...string) ([]byte, error) {
	docker.calls = append(docker.calls, append([]string(nil), args...))
	if len(args) >= 2 && args[0] == docker.kind && args[1] == "ls" && strings.Contains(strings.Join(args, " "), "name=^"+docker.name+"$") {
		return []byte("resource-id\n"), nil
	}
	if len(args) >= 2 && args[0] == docker.kind && args[1] == "inspect" {
		return json.Marshal(inspectedResource{DaemonID: "resource-id", Name: docker.name, Labels: docker.labels})
	}
	return nil, nil
}
