// Package source defines the vendor-independent boundary between recovery
// orchestration and configured backup-source adapters.
package source

import (
	"context"
	"fmt"
	"time"

	"github.com/azizu06/rehearse/internal/drill"
)

// Adapter is a fully configured backup source. Provider-specific configuration
// is deliberately kept outside this interface. Orchestration must successfully
// call Capabilities on each new instance before ListRecoveryPoints or Acquire.
type Adapter interface {
	Capabilities(context.Context) (Capabilities, error)
	ListRecoveryPoints(context.Context) ([]RecoveryPoint, error)
	Acquire(context.Context, AcquireRequest) (Artifact, error)
}

// CredentialResolver materializes a credential reference only for the
// lifetime of an adapter operation. Implementations must not include resolved
// values in returned errors.
type CredentialResolver interface {
	Resolve(context.Context, drill.CredentialReference) ([]byte, error)
}

// FailureKind is a stable, vendor-independent source failure category.
type FailureKind string

const (
	FailureInvalidInput      FailureKind = "invalid_input"
	FailureUnavailable       FailureKind = "unavailable"
	FailureUnsupported       FailureKind = "unsupported"
	FailureRepositoryMissing FailureKind = "repository_missing"
	FailureAuthentication    FailureKind = "authentication"
	FailureCancelled         FailureKind = "cancelled"
	FailureTimeout           FailureKind = "timeout"
	FailureCorruptOutput     FailureKind = "corrupt_output"
	FailureOutputLimit       FailureKind = "output_limit"
	FailureProcess           FailureKind = "process"
)

// Failure contains only typed and explicitly safe process metadata. It never
// stores raw adapter output or restored item paths.
type Failure struct {
	Kind      FailureKind
	Operation string
	ExitCode  int
	SafeHint  string
}

func (failure *Failure) Error() string {
	if failure.SafeHint != "" {
		return failure.SafeHint
	}
	if failure.Operation != "" {
		return fmt.Sprintf("source %s failed (%s)", failure.Operation, failure.Kind)
	}
	return fmt.Sprintf("source operation failed (%s)", failure.Kind)
}

// Capabilities describes behavior the configured adapter has preflighted.
type Capabilities struct {
	AdapterVersion  string
	Lists           bool
	Acquires        bool
	ReportsProgress bool
}

// RecoveryPoint is stable, secret-free metadata for one selectable backup.
type RecoveryPoint struct {
	ID        string
	CreatedAt time.Time
	Host      string
	Paths     []string
	Tags      []string
	Files     uint64
	Bytes     uint64
}

// Progress reports aggregate acquisition progress. Paths and restored content
// are intentionally absent from the contract.
type Progress struct {
	PercentDone float64
	FilesDone   uint64
	TotalFiles  uint64
	BytesDone   uint64
	TotalBytes  uint64
}

// ProgressReporter receives synchronous informational progress. Cancellation
// is controlled exclusively by the Acquire context.
type ProgressReporter func(Progress)

// AcquireRequest selects a recovery point and a caller-owned workspace.
type AcquireRequest struct {
	RecoveryPointID string
	Workspace       string
	Report          ProgressReporter
}

// ArtifactKind identifies a successfully acquired artifact representation.
type ArtifactKind string

const (
	// ArtifactDirectory is a directory tree ready for a restore-target adapter.
	ArtifactDirectory ArtifactKind = "directory"
)

// Artifact is secret-free metadata for a completed acquisition. The caller
// owns cleanup after Acquire returns successfully.
type Artifact struct {
	Kind            ArtifactKind
	Path            string
	RecoveryPointID string
}
