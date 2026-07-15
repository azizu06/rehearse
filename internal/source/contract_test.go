package source_test

import (
	"context"
	"testing"
	"time"

	"github.com/azizu06/rehearse/internal/source"
)

type configuredAdapter struct{}

func (configuredAdapter) Capabilities(context.Context) (source.Capabilities, error) {
	return source.Capabilities{
		AdapterVersion:  "contract-fixture/1",
		Lists:           true,
		Acquires:        true,
		ReportsProgress: true,
	}, nil
}

func (configuredAdapter) ListRecoveryPoints(context.Context) ([]source.RecoveryPoint, error) {
	return []source.RecoveryPoint{{
		ID:        "recovery-point-1",
		CreatedAt: time.Date(2026, 7, 15, 17, 30, 0, 0, time.UTC),
		Host:      "fixture-host",
		Paths:     []string{"/data"},
		Tags:      []string{"daily"},
		Files:     2,
		Bytes:     128,
	}}, nil
}

func (configuredAdapter) Acquire(_ context.Context, request source.AcquireRequest) (source.Artifact, error) {
	request.Report(source.Progress{
		PercentDone: 1,
		FilesDone:   2,
		TotalFiles:  2,
		BytesDone:   128,
		TotalBytes:  128,
	})
	return source.Artifact{
		Kind:            source.ArtifactDirectory,
		Path:            request.Workspace,
		RecoveryPointID: request.RecoveryPointID,
	}, nil
}

func TestConfiguredAdapterContractIsVendorIndependent(t *testing.T) {
	t.Parallel()

	var adapter source.Adapter = configuredAdapter{}

	capabilities, err := adapter.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	if !capabilities.Lists || !capabilities.Acquires || !capabilities.ReportsProgress {
		t.Fatalf("unexpected capabilities: %+v", capabilities)
	}

	points, err := adapter.ListRecoveryPoints(context.Background())
	if err != nil {
		t.Fatalf("list recovery points: %v", err)
	}
	if len(points) != 1 || points[0].ID != "recovery-point-1" {
		t.Fatalf("unexpected recovery points: %+v", points)
	}

	var progress source.Progress
	artifact, err := adapter.Acquire(context.Background(), source.AcquireRequest{
		RecoveryPointID: points[0].ID,
		Workspace:       t.TempDir(),
		Report: func(event source.Progress) {
			progress = event
		},
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if artifact.RecoveryPointID != points[0].ID || progress.PercentDone != 1 {
		t.Fatalf("unexpected artifact/progress: artifact=%+v progress=%+v", artifact, progress)
	}
}
