package sandboxid

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
)

func TestProject(t *testing.T) {
	tests := []struct {
		name    string
		runID   string
		wantErr error
	}{
		{name: "simple", runID: "durable-run"},
		{name: "single char", runID: "a"},
		{name: "max length", runID: fmt.Sprintf("%0128d", 1)},
		{name: "empty rejected", runID: "", wantErr: ErrInvalidRunID},
		{name: "too long rejected", runID: fmt.Sprintf("%0129d", 1), wantErr: ErrInvalidRunID},
		{name: "leading dot rejected", runID: ".run", wantErr: ErrInvalidRunID},
		{name: "slash rejected", runID: "run/id", wantErr: ErrInvalidRunID},
		{name: "space rejected", runID: "run id", wantErr: ErrInvalidRunID},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectName, fingerprint, err := Project(tt.runID)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Project(%q) error = %v, want %v", tt.runID, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Project(%q) unexpected error: %v", tt.runID, err)
			}
			digest := sha256.Sum256([]byte(tt.runID))
			wantFingerprint := fmt.Sprintf("%x", digest)
			if fingerprint != wantFingerprint {
				t.Errorf("fingerprint = %q, want %q", fingerprint, wantFingerprint)
			}
			wantProject := ProjectPrefix + wantFingerprint[:ProjectDigestLength]
			if projectName != wantProject {
				t.Errorf("projectName = %q, want %q", projectName, wantProject)
			}
		})
	}
}

func TestValidResourceKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want bool
	}{
		{name: "simple", key: "postgres", want: true},
		{name: "digits and dashes", key: "svc-1-primary", want: true},
		{name: "max length", key: "abcdefghijklmnopqrstuvwxyz"[:MaxResourceKeyLength], want: true},
		{name: "too long", key: "abcdefghijklmnopqrstuvwxyz0", want: false},
		{name: "empty", key: "", want: false},
		{name: "uppercase rejected", key: "Postgres", want: false},
		{name: "leading dash rejected", key: "-postgres", want: false},
		{name: "underscore rejected", key: "post_gres", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidResourceKey(tt.key); got != tt.want {
				t.Errorf("ValidResourceKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestResourceName(t *testing.T) {
	const project = "rehearse-abc123"

	tests := []struct {
		name    string
		kind    string
		key     string
		want    string
		wantErr error
	}{
		{name: "container", kind: "container", key: "app", want: project + "-app-1"},
		{name: "network", kind: "network", key: "default", want: project + "_default"},
		{name: "volume", kind: "volume", key: "data", want: project + "_data"},
		{name: "unknown kind rejected", kind: "unknown", key: "app", wantErr: ErrInvalidResource},
		{name: "invalid key rejected", kind: "container", key: "Bad_Key", wantErr: ErrInvalidResource},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResourceName(project, tt.kind, tt.key)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ResourceName error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResourceName unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("ResourceName = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateResourceName(t *testing.T) {
	const runID = "durable-run"

	projectName, _, err := Project(runID)
	if err != nil {
		t.Fatalf("Project(%q) error: %v", runID, err)
	}
	containerName, err := ResourceName(projectName, "container", "app")
	if err != nil {
		t.Fatalf("ResourceName error: %v", err)
	}
	networkName, err := ResourceName(projectName, "network", "default")
	if err != nil {
		t.Fatalf("ResourceName error: %v", err)
	}

	tests := []struct {
		name    string
		runID   string
		kind    string
		resName string
		wantErr bool
	}{
		{name: "valid container", runID: runID, kind: "container", resName: containerName},
		{name: "valid network", runID: runID, kind: "network", resName: networkName},
		{name: "wrong project prefix", runID: "other-run", kind: "container", resName: containerName, wantErr: true},
		{name: "missing suffix", runID: runID, kind: "container", resName: projectName + "-app", wantErr: true},
		{name: "unknown kind", runID: runID, kind: "volume-typo", resName: containerName, wantErr: true},
		{name: "invalid run ID", runID: "bad id", kind: "container", resName: containerName, wantErr: true},
		{name: "tampered key", runID: runID, kind: "container", resName: projectName + "-Bad_Key-1", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateResourceName(tt.runID, tt.kind, tt.resName)
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidResource) {
					t.Fatalf("ValidateResourceName error = %v, want %v", err, ErrInvalidResource)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateResourceName unexpected error: %v", err)
			}
		})
	}
}
