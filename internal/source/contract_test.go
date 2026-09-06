package source_test

import (
	"context"
	"errors"
	"strings"
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

type cancellationAdapter struct{}

func (cancellationAdapter) Capabilities(context.Context) (source.Capabilities, error) {
	return source.Capabilities{AdapterVersion: "contract-fixture/1", Acquires: true}, nil
}

func (cancellationAdapter) ListRecoveryPoints(context.Context) ([]source.RecoveryPoint, error) {
	return nil, nil
}

func (cancellationAdapter) Acquire(ctx context.Context, _ source.AcquireRequest) (source.Artifact, error) {
	<-ctx.Done()
	kind := source.FailureCancelled
	hint := "source acquisition was cancelled"
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		kind = source.FailureTimeout
		hint = "source acquisition timed out"
	}
	return source.Artifact{}, &source.Failure{Kind: kind, Operation: "acquire", SafeHint: hint}
}

func TestConfiguredAdapterContractDefinesCancellationAndTypedFailures(t *testing.T) {
	t.Parallel()

	var adapter source.Adapter = cancellationAdapter{}
	contexts := []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
		kind source.FailureKind
	}{
		{
			name: "cancelled",
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, func() {}
			},
			kind: source.FailureCancelled,
		},
		{
			name: "timed out",
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Unix(0, 0))
			},
			kind: source.FailureTimeout,
		},
	}

	for _, test := range contexts {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := test.ctx()
			defer cancel()
			_, err := adapter.Acquire(ctx, source.AcquireRequest{RecoveryPointID: "private-recovery-point"})
			var failure *source.Failure
			if !errors.As(err, &failure) || failure.Kind != test.kind || failure.Operation != "acquire" {
				t.Fatalf("failure = %+v, want %s acquire failure", failure, test.kind)
			}
			if strings.Contains(err.Error(), "private-recovery-point") {
				t.Fatalf("typed failure leaked request data: %v", err)
			}
		})
	}
}
