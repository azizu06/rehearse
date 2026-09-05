package probe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/azizu06/rehearse/internal/redact"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Evidence is the bounded, redacted truth for one declared probe.
type Evidence struct {
	Ordinal            int           `json:"ordinal"`
	ID                 string        `json:"id"`
	Kind               Kind          `json:"kind"`
	Required           bool          `json:"required"`
	Status             Status        `json:"status"`
	Attempts           int           `json:"attempts"`
	StartedAt          time.Time     `json:"started_at"`
	FinishedAt         time.Time     `json:"finished_at"`
	Duration           time.Duration `json:"duration_ns"`
	Observed           string        `json:"observed,omitempty"`
	Detail             string        `json:"detail,omitempty"`
	Truncated          bool          `json:"truncated"`
	StdoutTruncated    bool          `json:"stdout_truncated"`
	StderrTruncated    bool          `json:"stderr_truncated"`
	TrustedHostCommand bool          `json:"trusted_host_command"`
	ExhaustedBy        ExhaustedBy   `json:"exhausted_by,omitempty"`
}

type ExhaustedBy string

const (
	ExhaustedAttempts ExhaustedBy = "max_attempts"
	ExhaustedDeadline ExhaustedBy = "deadline"
)

// Clock makes retry sequencing deterministic while real executors still
// receive a context bounded by the same total deadline.
type Clock interface {
	Now() time.Time
	Wait(context.Context, time.Duration) error
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

func (realClock) Wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Result aggregates required truth without hiding optional failures.
type Result struct {
	StartedAt      time.Time
	FinishedAt     time.Time
	Duration       time.Duration
	RequiredPassed bool
	Probes         []Evidence
}

type Options struct {
	HTTPClient     *http.Client
	Dialer         *net.Dialer
	DataRoot       string
	Redactor       redact.Redactor
	Clock          Clock
	SQLConnections map[string]*pgxpool.Pool
}

type Runner struct {
	httpClient     *http.Client
	dialer         *net.Dialer
	dataRoot       string
	redactor       redact.Redactor
	clock          Clock
	sqlConnections map[string]*pgxpool.Pool
}

func NewRunner(options Options) *Runner {
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	clientCopy := *client
	clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	client = &clientCopy
	dialer := options.Dialer
	if dialer == nil {
		dialer = &net.Dialer{}
	}
	clock := options.Clock
	if clock == nil {
		clock = realClock{}
	}
	return &Runner{httpClient: client, dialer: dialer, dataRoot: options.DataRoot, redactor: options.Redactor, clock: clock, sqlConnections: options.SQLConnections}
}

// Run executes probes sequentially in declaration order.
func (runner *Runner) Run(ctx context.Context, config Config) Result {
	startedAt := runner.clock.Now().UTC()
	result := Result{StartedAt: startedAt, RequiredPassed: true}
	if err := config.Validate(); err != nil {
		result.RequiredPassed = false
		result.FinishedAt = runner.clock.Now().UTC()
		result.Duration = result.FinishedAt.Sub(startedAt)
		return result
	}
	ordered := append([]Spec(nil), config.Probes...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].Ordinal < ordered[right].Ordinal })
	for _, spec := range ordered {
		evidence := runner.run(ctx, spec)
		result.Probes = append(result.Probes, evidence)
		if spec.Required && evidence.Status != StatusPassed {
			result.RequiredPassed = false
		}
		if ctx.Err() != nil {
			break
		}
	}
	result.FinishedAt = runner.clock.Now().UTC()
	result.Duration = result.FinishedAt.Sub(startedAt)
	return result
}

func (runner *Runner) run(parent context.Context, spec Spec) Evidence {
	switch spec.Kind {
	case KindTCP:
		return runner.runTCP(parent, spec)
	case KindCommand:
		return runner.runCommand(parent, spec)
	case KindData:
		return runner.runData(parent, spec)
	case KindSQL:
		return runner.runSQL(parent, spec)
	default:
		return runner.runHTTP(parent, spec)
	}
}

func (runner *Runner) runSQL(parent context.Context, spec Spec) Evidence {
	return runner.runAttempts(parent, spec, func(ctx context.Context) (string, error) {
		connection := runner.sqlConnections[spec.SQL.ConnectionID]
		if connection == nil {
			return "", errors.New("SQL probe connection is unavailable")
		}
		transaction, err := connection.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
		if err != nil {
			return "", err
		}
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = transaction.Rollback(cleanup)
		}()

		remaining := spec.Retry.Deadline
		if deadline, ok := ctx.Deadline(); ok {
			remaining = time.Until(deadline)
		}
		milliseconds := remaining.Milliseconds()
		if milliseconds < 1 {
			milliseconds = 1
		}
		if _, err := transaction.Exec(ctx, fmt.Sprintf("SET LOCAL statement_timeout = %d", milliseconds)); err != nil {
			return "", err
		}
		var role string
		if err := transaction.QueryRow(ctx, "SELECT current_user", pgx.QueryExecModeCacheStatement).Scan(&role); err != nil {
			return "", err
		}
		if role != spec.SQL.ExpectedRole {
			return "", fmt.Errorf("SQL probe role %q does not match required least-privileged role", role)
		}
		var value any
		if err := transaction.QueryRow(ctx, spec.SQL.Query, pgx.QueryExecModeCacheStatement).Scan(&value); err != nil {
			return "", err
		}
		observed := fmt.Sprint(value)
		if len(observed) > maxObservedBytes {
			return "", errors.New("SQL scalar evidence exceeds safe bounds")
		}
		if observed != spec.SQL.ExpectedValue {
			return observed, errors.New("SQL scalar assertion did not match")
		}
		return observed, nil
	})
}

const (
	maxObservedBytes = 16 << 10
	maxDataBytes     = 1 << 20
	commandWaitDelay = 50 * time.Millisecond
)

type cappedBuffer struct {
	buffer        bytes.Buffer
	limit         int
	evidenceLimit int
	truncated     bool
}

func (buffer *cappedBuffer) Write(value []byte) (int, error) {
	originalLength := len(value)
	if buffer.buffer.Len()+originalLength > buffer.evidenceLimit {
		buffer.truncated = true
	}
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining <= 0 {
		buffer.truncated = buffer.truncated || originalLength > 0
		return originalLength, nil
	}
	if len(value) > remaining {
		value = value[:remaining]
		buffer.truncated = true
	}
	_, _ = buffer.buffer.Write(value)
	return originalLength, nil
}

func (runner *Runner) runCommand(parent context.Context, spec Spec) Evidence {
	captureLimit := maxObservedBytes + runner.redactor.MaxMarkerBytes()
	stdout := cappedBuffer{limit: captureLimit, evidenceLimit: maxObservedBytes}
	stderr := cappedBuffer{limit: captureLimit, evidenceLimit: maxObservedBytes}
	evidence := runner.runAttempts(parent, spec, func(ctx context.Context) (string, error) {
		stdout = cappedBuffer{limit: captureLimit, evidenceLimit: maxObservedBytes}
		stderr = cappedBuffer{limit: captureLimit, evidenceLimit: maxObservedBytes}
		command := exec.CommandContext(ctx, spec.Command.Executable, spec.Command.Args...)
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		command.Cancel = func() error {
			if command.Process == nil {
				return nil
			}
			if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				return err
			}
			return nil
		}
		command.WaitDelay = commandWaitDelay
		command.Env = []string{"LANG=C", "LC_ALL=C", "PATH=/usr/bin:/bin"}
		command.Dir = "/"
		command.Stdout = &stdout
		command.Stderr = &stderr
		err := command.Run()
		exitCode := 0
		if err != nil {
			var exitError *exec.ExitError
			if !errors.As(err, &exitError) {
				return commandOutput(stdout, stderr), err
			}
			exitCode = exitError.ExitCode()
		}
		if exitCode != spec.Command.ExpectedExitCode {
			return commandOutput(stdout, stderr), fmt.Errorf("command exit code %d", exitCode)
		}
		return commandOutput(stdout, stderr), nil
	})
	evidence.TrustedHostCommand = true
	evidence.StdoutTruncated = stdout.truncated
	evidence.StderrTruncated = stderr.truncated
	evidence.Truncated = evidence.Truncated || stdout.truncated || stderr.truncated
	return evidence
}

func commandOutput(stdout, stderr cappedBuffer) string {
	return "stdout:" + stdout.buffer.String() + "\nstderr:" + stderr.buffer.String()
}

func (runner *Runner) runData(parent context.Context, spec Spec) Evidence {
	return runner.runAttempts(parent, spec, func(ctx context.Context) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if !filepath.IsAbs(runner.dataRoot) || filepath.Clean(runner.dataRoot) == string(os.PathSeparator) {
			return "", errors.New("data assertion root must be a non-root absolute path")
		}
		root, err := filepath.EvalSymlinks(runner.dataRoot)
		if err != nil {
			return "", fmt.Errorf("resolve data root: %w", err)
		}
		candidate, err := filepath.EvalSymlinks(filepath.Join(root, spec.Data.Path))
		if err != nil {
			return "", fmt.Errorf("resolve data assertion path: %w", err)
		}
		if candidate != root && !strings.HasPrefix(candidate, root+string(os.PathSeparator)) {
			return "", errors.New("data assertion path escapes restored root")
		}
		file, err := openDataFile(root, candidate)
		if err != nil {
			return "", err
		}
		defer func() { _ = file.Close() }()
		info, err := file.Stat()
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() || info.Size() > maxDataBytes {
			return "", errors.New("data assertion file exceeds safe bounds")
		}
		hasher := sha256.New()
		written, err := io.Copy(hasher, io.LimitReader(file, maxDataBytes+1))
		if err != nil {
			return "", err
		}
		if written > maxDataBytes {
			return "", errors.New("data assertion file exceeds safe bounds")
		}
		digest := fmt.Sprintf("%x", hasher.Sum(nil))
		if digest != strings.ToLower(spec.Data.SHA256) {
			return "", errors.New("data assertion hash did not match")
		}
		return fmt.Sprintf("sha256:%s bytes:%d", digest, written), nil
	})
}

func (runner *Runner) runTCP(parent context.Context, spec Spec) Evidence {
	return runner.runAttempts(parent, spec, func(ctx context.Context) (string, error) {
		connection, err := runner.dialer.DialContext(ctx, "tcp", spec.TCP.Address)
		if err != nil {
			return "", err
		}
		if err := connection.Close(); err != nil {
			return "", err
		}
		return "TCP connected", nil
	})
}

func (runner *Runner) runHTTP(parent context.Context, spec Spec) Evidence {
	return runner.runAttempts(parent, spec, func(ctx context.Context) (string, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, spec.HTTP.URL, nil)
		if err != nil {
			return "", err
		}
		response, err := runner.httpClient.Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
		if err != nil {
			return "", err
		}
		if response.StatusCode != spec.HTTP.ExpectedStatus {
			return "", fmt.Errorf("unexpected HTTP status %d", response.StatusCode)
		}
		return fmt.Sprintf("HTTP %d", response.StatusCode), nil
	})
}

func (runner *Runner) runAttempts(parent context.Context, spec Spec, attempt func(context.Context) (string, error)) Evidence {
	startedAt := runner.clock.Now().UTC()
	evidence := Evidence{Ordinal: spec.Ordinal, ID: spec.ID, Kind: spec.Kind, Required: spec.Required, StartedAt: startedAt}
	deadline := startedAt.Add(spec.Retry.Deadline)
	ctx, cancel := context.WithTimeout(parent, spec.Retry.Deadline)
	defer cancel()

	for evidence.Attempts < spec.Retry.MaxAttempts {
		if ctx.Err() != nil {
			break
		}
		evidence.Attempts++
		observed, err := attempt(ctx)
		if err == nil {
			evidence.Status = StatusPassed
			evidence.Observed, evidence.Truncated = runner.redactor.BoundedString(observed, 33<<10)
			break
		}
		if err != nil {
			evidence.Detail, evidence.Truncated = runner.redactor.BoundedString(err.Error(), maxObservedBytes)
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			evidence.ExhaustedBy = ExhaustedDeadline
			break
		}
		if parent.Err() != nil {
			break
		}
		if evidence.Attempts >= spec.Retry.MaxAttempts {
			evidence.ExhaustedBy = ExhaustedAttempts
			break
		}
		if deadline.Sub(runner.clock.Now()) <= spec.Retry.Backoff {
			evidence.ExhaustedBy = ExhaustedDeadline
			break
		}
		if err := runner.clock.Wait(ctx, spec.Retry.Backoff); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				evidence.ExhaustedBy = ExhaustedDeadline
			}
			break
		}
	}

	if evidence.Status == "" {
		switch {
		case parent.Err() != nil:
			evidence.Status = StatusCancelled
		case evidence.ExhaustedBy == ExhaustedDeadline || !runner.clock.Now().Before(deadline) || ctx.Err() == context.DeadlineExceeded:
			evidence.Status = StatusTimedOut
		default:
			evidence.Status = StatusFailed
		}
	}
	evidence.FinishedAt = runner.clock.Now().UTC()
	evidence.Duration = evidence.FinishedAt.Sub(startedAt)
	return evidence
}
