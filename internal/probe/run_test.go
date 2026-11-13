package probe_test

import (
	"context"
	"testing"

	"github.com/azizu06/rehearse/internal/probe"
)

func TestRunReturnsImmediatelyForInvalidConfig(t *testing.T) {
	t.Parallel()

	runner := probe.NewRunner(probe.Options{})
	result := runner.Run(context.Background(), probe.Config{})

	if result.RequiredPassed {
		t.Fatalf("RequiredPassed = true, want false for a config that never validates")
	}
	if len(result.Probes) != 0 {
		t.Fatalf("Probes = %#v, want none when the config is rejected before any probe runs", result.Probes)
	}
	if result.FinishedAt.Before(result.StartedAt) {
		t.Fatalf("FinishedAt = %v, want on or after StartedAt = %v", result.FinishedAt, result.StartedAt)
	}
}

func TestRunMarksAllProbesNotAttemptedWhenContextIsAlreadyCancelled(t *testing.T) {
	t.Parallel()

	config := probe.Config{
		SchemaVersion: probe.SchemaVersion,
		Probes: []probe.Spec{
			tcpSpec("127.0.0.1:1"),
			{Ordinal: 2, ID: "second", Kind: probe.KindTCP, Required: true, Retry: validRetry, TCP: &probe.TCPSpec{Address: "127.0.0.1:1"}},
		},
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	runner := probe.NewRunner(probe.Options{})
	result := runner.Run(ctx, config)

	if result.RequiredPassed {
		t.Fatalf("RequiredPassed = true, want false when a required probe never ran")
	}
	if len(result.Probes) != len(config.Probes) {
		t.Fatalf("len(Probes) = %d, want %d", len(result.Probes), len(config.Probes))
	}
	for index, probeEvidence := range result.Probes {
		if probeEvidence.Status != probe.StatusNotAttempted {
			t.Fatalf("Probes[%d].Status = %q, want %q", index, probeEvidence.Status, probe.StatusNotAttempted)
		}
		if probeEvidence.Attempts != 0 {
			t.Fatalf("Probes[%d].Attempts = %d, want 0", index, probeEvidence.Attempts)
		}
	}
}
