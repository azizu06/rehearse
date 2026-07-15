// Package sandbox creates and removes one constrained Docker Compose project
// for a durable drill run.
package sandbox

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/azizu06/rehearse/internal/drill"
)

const (
	managedLabel        = "dev.rehearse.managed"
	projectLabel        = "dev.rehearse.project"
	runFingerprintLabel = "dev.rehearse.run-fingerprint"
	runIDLabel          = "dev.rehearse.run-id"
	projectPrefix       = "rehearse-"
	projectDigestLength = 24
	maxComposeKeyLength = 26
)

var (
	ErrInvalidRequest      = errors.New("invalid sandbox request")
	ErrUnsafeCompose       = errors.New("unsafe compose configuration")
	ErrOutputLimitExceeded = errors.New("docker output limit exceeded")
	ErrProjectBusy         = errors.New("sandbox project is already locked")
	ErrProjectCollision    = errors.New("sandbox project fingerprint collision")
	ErrProjectExists       = errors.New("sandbox project resources already exist")
	runIDPattern           = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	composeKeyPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,25}$`)
	dnsLabelPattern        = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)
)

// Limits are mandatory per-service and per-run boundaries.
type Limits struct {
	CPUs        string
	MemoryBytes int64
	PIDs        int64
	Duration    time.Duration
	OutputBytes int64
}

// Request identifies one durable run and its input Compose model.
type Request struct {
	RunID            string
	ComposeFiles     []string
	ProjectDirectory string
	Limits           Limits
}

// Instance is the stable identity exposed while the isolated project runs.
type Instance struct {
	RunID       string
	ProjectName string
}

// Result records the stable identity of the project that was cleaned.
type Result = Instance

// DockerRunnerOptions configure the trusted local Docker CLI boundary.
type DockerRunnerOptions struct {
	Binary         string
	CleanupJournal CleanupJournal
	TemporaryRoot  string
	LockRoot       string
	CleanupTimeout time.Duration
}

// CleanupJournal is the narrow durable ownership seam shared with startup
// reconciliation.
type CleanupJournal interface {
	ClaimSandboxCleanup(context.Context, string, time.Time) error
	RecordSandboxCleanup(context.Context, string, drill.CleanupStatus, time.Time) error
}

// CleanerOptions configure standalone startup reconciliation cleanup.
type CleanerOptions struct {
	Binary        string
	LockRoot      string
	TemporaryRoot string
}

type identity struct {
	runID       string
	fingerprint string
	projectName string
}

type normalizedRequest struct {
	Request
	identity         identity
	projectDirectory string
	composeFiles     []string
}

func newIdentity(runID string) (identity, error) {
	return newIdentityWithDigest(runID, sha256.Sum256([]byte(runID)))
}

func newIdentityWithDigest(runID string, digest [sha256.Size]byte) (identity, error) {
	if !runIDPattern.MatchString(runID) {
		return identity{}, fmt.Errorf("%w: run ID must match %s", ErrInvalidRequest, runIDPattern)
	}
	fingerprint := fmt.Sprintf("%x", digest)
	return identity{
		runID:       runID,
		fingerprint: fingerprint,
		projectName: projectPrefix + fingerprint[:projectDigestLength],
	}, nil
}

func (value identity) labels() map[string]string {
	return map[string]string{
		managedLabel:        "true",
		projectLabel:        value.projectName,
		runFingerprintLabel: value.fingerprint,
		runIDLabel:          value.runID,
	}
}

func normalizeRequest(request Request) (normalizedRequest, error) {
	identity, err := newIdentity(request.RunID)
	if err != nil {
		return normalizedRequest{}, err
	}
	if len(request.ComposeFiles) == 0 || len(request.ComposeFiles) > 16 {
		return normalizedRequest{}, fmt.Errorf("%w: one to sixteen Compose files are required", ErrInvalidRequest)
	}
	if err := request.Limits.validate(); err != nil {
		return normalizedRequest{}, err
	}

	files := make([]string, 0, len(request.ComposeFiles))
	for _, file := range request.ComposeFiles {
		absolute, err := filepath.Abs(file)
		if err != nil {
			return normalizedRequest{}, fmt.Errorf("%w: resolve Compose file: %v", ErrInvalidRequest, err)
		}
		info, err := os.Stat(absolute)
		if err != nil || !info.Mode().IsRegular() {
			return normalizedRequest{}, fmt.Errorf("%w: Compose file must be a regular file", ErrInvalidRequest)
		}
		files = append(files, filepath.Clean(absolute))
	}

	projectDirectory := request.ProjectDirectory
	if strings.TrimSpace(projectDirectory) == "" {
		projectDirectory = filepath.Dir(files[0])
	}
	absoluteDirectory, err := filepath.Abs(projectDirectory)
	if err != nil {
		return normalizedRequest{}, fmt.Errorf("%w: resolve project directory: %v", ErrInvalidRequest, err)
	}
	info, err := os.Stat(absoluteDirectory)
	if err != nil || !info.IsDir() {
		return normalizedRequest{}, fmt.Errorf("%w: project directory must exist", ErrInvalidRequest)
	}

	return normalizedRequest{
		Request:          request,
		identity:         identity,
		projectDirectory: filepath.Clean(absoluteDirectory),
		composeFiles:     files,
	}, nil
}

func (limits Limits) validate() error {
	cpus, err := strconv.ParseFloat(limits.CPUs, 64)
	switch {
	case err != nil || math.IsNaN(cpus) || math.IsInf(cpus, 0) || cpus <= 0 || cpus > 64:
		return fmt.Errorf("%w: CPUs must be greater than zero and at most 64", ErrInvalidRequest)
	case limits.MemoryBytes < 16<<20 || limits.MemoryBytes > 1<<40:
		return fmt.Errorf("%w: memory must be between 16 MiB and 1 TiB", ErrInvalidRequest)
	case limits.PIDs < 1 || limits.PIDs > 32768:
		return fmt.Errorf("%w: PID limit must be between 1 and 32768", ErrInvalidRequest)
	case limits.Duration <= 0 || limits.Duration > 24*time.Hour:
		return fmt.Errorf("%w: duration must be greater than zero and at most 24 hours", ErrInvalidRequest)
	case limits.OutputBytes < 1024 || limits.OutputBytes > 64<<20:
		return fmt.Errorf("%w: output limit must be between 1 KiB and 64 MiB", ErrInvalidRequest)
	default:
		return nil
	}
}
