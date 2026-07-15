package sandbox

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestIdentityUsesBoundedDigestAndFullFingerprintLabel(t *testing.T) {
	identity, err := newIdentity("run-1")
	if err != nil {
		t.Fatalf("newIdentity: %v", err)
	}
	if len(identity.projectName) != 33 || !strings.HasPrefix(identity.projectName, "rehearse-") {
		t.Fatalf("project name = %q, want 33-character rehearse name", identity.projectName)
	}
	if len(identity.fingerprint) != 64 || identity.projectName != "rehearse-"+identity.fingerprint[:24] {
		t.Fatalf("identity = %#v", identity)
	}
	if got := identity.labels()[runFingerprintLabel]; got != identity.fingerprint {
		t.Fatalf("fingerprint label = %q, want %q", got, identity.fingerprint)
	}
}

func TestRequestValidationRejectsInvalidBounds(t *testing.T) {
	valid := Request{
		RunID:        "run-1",
		ComposeFiles: []string{"testdata/compose.yaml"},
		Limits: Limits{
			CPUs: "0.5", MemoryBytes: 32 << 20, PIDs: 32,
			Duration: time.Minute, OutputBytes: 4096,
		},
	}
	tests := []struct {
		name   string
		mutate func(*Request)
	}{
		{name: "unsafe run ID", mutate: func(request *Request) { request.RunID = "run/id" }},
		{name: "missing Compose file", mutate: func(request *Request) { request.ComposeFiles = nil }},
		{name: "zero CPU", mutate: func(request *Request) { request.Limits.CPUs = "0" }},
		{name: "small memory", mutate: func(request *Request) { request.Limits.MemoryBytes = 1 }},
		{name: "zero PIDs", mutate: func(request *Request) { request.Limits.PIDs = 0 }},
		{name: "zero duration", mutate: func(request *Request) { request.Limits.Duration = 0 }},
		{name: "small output", mutate: func(request *Request) { request.Limits.OutputBytes = 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			test.mutate(&request)
			if _, err := normalizeRequest(request); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("normalizeRequest error = %v, want ErrInvalidRequest", err)
			}
		})
	}
}
