package sandbox

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestCollisionPreflightInspectsOnlyRehearseLabeledCandidates(t *testing.T) {
	identity, _ := newIdentity("run-collision")
	command := &collisionDocker{fingerprint: strings.Repeat("f", 64)}
	err := ensureProjectVacant(context.Background(), command, identity)
	if !errors.Is(err, ErrProjectCollision) {
		t.Fatalf("ensureProjectVacant error = %v, want ErrProjectCollision", err)
	}
	for _, call := range command.calls {
		if len(call) >= 2 && call[1] == "ls" {
			joined := strings.Join(call, " ")
			if !strings.Contains(joined, "label="+managedLabel+"=true") || !strings.Contains(joined, "label="+projectLabel+"="+identity.projectName) {
				t.Fatalf("unscoped collision list: %v", call)
			}
		}
	}
}

func TestCollisionPreflightRejectsResourcesFromTheSameRun(t *testing.T) {
	identity, _ := newIdentity("run-existing")
	command := &collisionDocker{fingerprint: identity.fingerprint}
	if err := ensureProjectVacant(context.Background(), command, identity); !errors.Is(err, ErrProjectExists) {
		t.Fatalf("ensureProjectVacant error = %v, want ErrProjectExists", err)
	}
}

type collisionDocker struct {
	fingerprint string
	calls       [][]string
}

func (docker *collisionDocker) run(_ context.Context, _ int64, args ...string) ([]byte, error) {
	docker.calls = append(docker.calls, append([]string(nil), args...))
	if len(args) >= 2 && reflect.DeepEqual(args[:2], []string{"container", "ls"}) {
		return []byte("container-id\n"), nil
	}
	if len(args) >= 2 && reflect.DeepEqual(args[:2], []string{"container", "inspect"}) {
		return []byte(docker.fingerprint + "\n"), nil
	}
	return nil, nil
}
