// Package probe defines bounded application-level probe configuration and
// executes it against restored application boundaries.
package probe

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	SchemaVersion  = "rehearse.probes/v1"
	maxConfigBytes = 256 << 10
	maxProbes      = 64
)

var (
	ErrInvalidConfig = errors.New("invalid probe configuration")
	probeIDPattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	sqlRolePattern   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)
)

type Kind string

const (
	KindHTTP    Kind = "http"
	KindTCP     Kind = "tcp"
	KindCommand Kind = "command"
	KindData    Kind = "data"
	KindSQL     Kind = "sql"
)

type Status string

const (
	StatusPassed       Status = "passed"
	StatusFailed       Status = "failed"
	StatusTimedOut     Status = "timed_out"
	StatusCancelled    Status = "cancelled"
	StatusNotAttempted Status = "not_attempted"
)

// RetryPolicy bounds retries by attempts and one shared absolute deadline.
type RetryPolicy struct {
	Deadline    time.Duration
	Backoff     time.Duration
	MaxAttempts int
}

type HTTPSpec struct {
	URL            string `json:"url"`
	ExpectedStatus int    `json:"expected_status"`
}

type TCPSpec struct {
	Address string `json:"address"`
}

type CommandSpec struct {
	Executable        string   `json:"executable"`
	Args              []string `json:"args,omitempty"`
	ExpectedExitCode  int      `json:"expected_exit_code"`
	TrustAcknowledged bool     `json:"trust_acknowledged"`
}

type DataSpec struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type SQLSpec struct {
	ConnectionID  string `json:"connection_id"`
	ExpectedRole  string `json:"expected_role"`
	Query         string `json:"query"`
	ExpectedValue string `json:"expected_value"`
}

// Spec is one probe in declaration order.
type Spec struct {
	Ordinal  int          `json:"ordinal"`
	ID       string       `json:"id"`
	Kind     Kind         `json:"kind"`
	Required bool         `json:"required"`
	Retry    RetryPolicy  `json:"-"`
	HTTP     *HTTPSpec    `json:"http,omitempty"`
	TCP      *TCPSpec     `json:"tcp,omitempty"`
	Command  *CommandSpec `json:"command,omitempty"`
	Data     *DataSpec    `json:"data,omitempty"`
	SQL      *SQLSpec     `json:"sql,omitempty"`
}

// Config is the closed, versioned probe document persisted with a plan.
type Config struct {
	SchemaVersion string `json:"schema_version"`
	Probes        []Spec `json:"probes"`
}

type wireRetry struct {
	Deadline    string `json:"deadline"`
	Backoff     string `json:"backoff"`
	MaxAttempts int    `json:"max_attempts"`
}

type wireSpec struct {
	Ordinal  int          `json:"ordinal"`
	ID       string       `json:"id"`
	Kind     Kind         `json:"kind"`
	Required bool         `json:"required"`
	Retry    wireRetry    `json:"retry"`
	HTTP     *HTTPSpec    `json:"http,omitempty"`
	TCP      *TCPSpec     `json:"tcp,omitempty"`
	Command  *CommandSpec `json:"command,omitempty"`
	Data     *DataSpec    `json:"data,omitempty"`
	SQL      *SQLSpec     `json:"sql,omitempty"`
}

type wireConfig struct {
	SchemaVersion string     `json:"schema_version"`
	Probes        []wireSpec `json:"probes"`
}

// IsZero reports whether no probe contract has been attached to a legacy plan.
func (config Config) IsZero() bool {
	return config.SchemaVersion == "" && len(config.Probes) == 0
}

// CanonicalJSON persists typed configuration in semantic declaration order.
func (config Config) CanonicalJSON() ([]byte, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	ordered := append([]Spec(nil), config.Probes...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].Ordinal < ordered[right].Ordinal })
	wire := wireConfig{SchemaVersion: config.SchemaVersion, Probes: make([]wireSpec, 0, len(ordered))}
	for _, item := range ordered {
		wire.Probes = append(wire.Probes, wireSpec{
			Ordinal: item.Ordinal, ID: item.ID, Kind: item.Kind, Required: item.Required,
			Retry: wireRetry{Deadline: item.Retry.Deadline.String(), Backoff: item.Retry.Backoff.String(), MaxAttempts: item.Retry.MaxAttempts},
			HTTP:  item.HTTP, TCP: item.TCP, Command: item.Command, Data: item.Data, SQL: item.SQL,
		})
	}
	return json.Marshal(wire)
}

// ParseConfigBytes is the persistence counterpart to ParseConfig.
func ParseConfigBytes(contents []byte) (Config, error) {
	return ParseConfig(bytes.NewReader(contents))
}

// ParseConfig strictly decodes one bounded JSON document.
func ParseConfig(reader io.Reader) (Config, error) {
	limited := io.LimitReader(reader, maxConfigBytes+1)
	contents, err := io.ReadAll(limited)
	if err != nil {
		return Config{}, fmt.Errorf("%w: read: %v", ErrInvalidConfig, err)
	}
	if len(contents) > maxConfigBytes {
		return Config{}, fmt.Errorf("%w: document exceeds %d bytes", ErrInvalidConfig, maxConfigBytes)
	}
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	var wire wireConfig
	if err := decoder.Decode(&wire); err != nil {
		return Config{}, fmt.Errorf("%w: decode: %v", ErrInvalidConfig, err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Config{}, err
	}

	config := Config{SchemaVersion: wire.SchemaVersion, Probes: make([]Spec, 0, len(wire.Probes))}
	for _, item := range wire.Probes {
		deadline, err := time.ParseDuration(item.Retry.Deadline)
		if err != nil {
			return Config{}, fmt.Errorf("%w: probe %q deadline: %v", ErrInvalidConfig, item.ID, err)
		}
		backoff, err := time.ParseDuration(item.Retry.Backoff)
		if err != nil {
			return Config{}, fmt.Errorf("%w: probe %q backoff: %v", ErrInvalidConfig, item.ID, err)
		}
		config.Probes = append(config.Probes, Spec{
			Ordinal: item.Ordinal, ID: item.ID, Kind: item.Kind, Required: item.Required,
			Retry:   RetryPolicy{Deadline: deadline, Backoff: backoff, MaxAttempts: item.Retry.MaxAttempts},
			HTTP:    item.HTTP,
			TCP:     item.TCP,
			Command: item.Command,
			Data:    item.Data,
			SQL:     item.SQL,
		})
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("%w: multiple JSON values", ErrInvalidConfig)
		}
		return fmt.Errorf("%w: trailing data: %v", ErrInvalidConfig, err)
	}
	return nil
}

// Validate enforces closed kinds, semantic order, and resource bounds.
func (config Config) Validate() error {
	if config.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: unsupported schema version", ErrInvalidConfig)
	}
	if len(config.Probes) == 0 || len(config.Probes) > maxProbes {
		return fmt.Errorf("%w: probes must contain 1..%d items", ErrInvalidConfig, maxProbes)
	}
	ordered := append([]Spec(nil), config.Probes...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].Ordinal < ordered[right].Ordinal })
	ids := make(map[string]struct{}, len(ordered))
	for index, spec := range ordered {
		if spec.Ordinal != index+1 {
			return fmt.Errorf("%w: probe ordinals must be unique and contiguous from 1", ErrInvalidConfig)
		}
		if !probeIDPattern.MatchString(spec.ID) {
			return fmt.Errorf("%w: invalid probe id %q", ErrInvalidConfig, spec.ID)
		}
		if _, duplicate := ids[spec.ID]; duplicate {
			return fmt.Errorf("%w: duplicate probe id %q", ErrInvalidConfig, spec.ID)
		}
		ids[spec.ID] = struct{}{}
		if spec.Retry.Deadline < 10*time.Millisecond || spec.Retry.Deadline > 5*time.Minute {
			return fmt.Errorf("%w: probe %q deadline out of bounds", ErrInvalidConfig, spec.ID)
		}
		if spec.Retry.Backoff < 10*time.Millisecond || spec.Retry.Backoff > 30*time.Second {
			return fmt.Errorf("%w: probe %q backoff out of bounds", ErrInvalidConfig, spec.ID)
		}
		if spec.Retry.MaxAttempts < 1 || spec.Retry.MaxAttempts > 100 {
			return fmt.Errorf("%w: probe %q max_attempts out of bounds", ErrInvalidConfig, spec.ID)
		}
		switch spec.Kind {
		case KindHTTP:
			if spec.HTTP == nil || payloadCount(spec) != 1 {
				return fmt.Errorf("%w: probe %q must contain only an HTTP payload", ErrInvalidConfig, spec.ID)
			}
			parsed, err := url.Parse(spec.HTTP.URL)
			if err != nil || len(spec.HTTP.URL) > 2048 || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
				return fmt.Errorf("%w: probe %q has invalid HTTP URL", ErrInvalidConfig, spec.ID)
			}
			if spec.HTTP.ExpectedStatus < 100 || spec.HTTP.ExpectedStatus > 599 {
				return fmt.Errorf("%w: probe %q has invalid expected status", ErrInvalidConfig, spec.ID)
			}
		case KindTCP:
			if spec.TCP == nil || payloadCount(spec) != 1 {
				return fmt.Errorf("%w: probe %q must contain only a TCP payload", ErrInvalidConfig, spec.ID)
			}
			host, port, err := net.SplitHostPort(spec.TCP.Address)
			if err != nil || len(spec.TCP.Address) > 256 || strings.TrimSpace(host) == "" || strings.TrimSpace(port) == "" {
				return fmt.Errorf("%w: probe %q has invalid TCP address", ErrInvalidConfig, spec.ID)
			}
		case KindCommand:
			if spec.Command == nil || payloadCount(spec) != 1 {
				return fmt.Errorf("%w: probe %q must contain only a command payload", ErrInvalidConfig, spec.ID)
			}
			if !spec.Command.TrustAcknowledged {
				return fmt.Errorf("%w: probe %q requires persisted command trust acknowledgement", ErrInvalidConfig, spec.ID)
			}
			if len(spec.Command.Executable) > 4096 || !filepath.IsAbs(spec.Command.Executable) || filepath.Clean(spec.Command.Executable) != spec.Command.Executable {
				return fmt.Errorf("%w: probe %q command executable must be a clean absolute path", ErrInvalidConfig, spec.ID)
			}
			if len(spec.Command.Args) > 64 || spec.Command.ExpectedExitCode < 0 || spec.Command.ExpectedExitCode > 255 {
				return fmt.Errorf("%w: probe %q command bounds are invalid", ErrInvalidConfig, spec.ID)
			}
			for _, argument := range spec.Command.Args {
				if len(argument) > 4096 || strings.ContainsRune(argument, 0) {
					return fmt.Errorf("%w: probe %q command argument is invalid", ErrInvalidConfig, spec.ID)
				}
			}
		case KindData:
			if spec.Data == nil || payloadCount(spec) != 1 {
				return fmt.Errorf("%w: probe %q must contain only a data payload", ErrInvalidConfig, spec.ID)
			}
			if spec.Data.Path == "" || len(spec.Data.Path) > 4096 || filepath.IsAbs(spec.Data.Path) || filepath.Clean(spec.Data.Path) != spec.Data.Path || strings.HasPrefix(spec.Data.Path, "..") {
				return fmt.Errorf("%w: probe %q data path must stay relative", ErrInvalidConfig, spec.ID)
			}
			digest, err := hex.DecodeString(spec.Data.SHA256)
			if err != nil || len(digest) != 32 {
				return fmt.Errorf("%w: probe %q data sha256 is invalid", ErrInvalidConfig, spec.ID)
			}
		case KindSQL:
			if spec.SQL == nil || payloadCount(spec) != 1 {
				return fmt.Errorf("%w: probe %q must contain only a SQL payload", ErrInvalidConfig, spec.ID)
			}
			if !probeIDPattern.MatchString(spec.SQL.ConnectionID) || !sqlRolePattern.MatchString(spec.SQL.ExpectedRole) {
				return fmt.Errorf("%w: probe %q SQL connection or role is invalid", ErrInvalidConfig, spec.ID)
			}
			if strings.TrimSpace(spec.SQL.Query) == "" || len(spec.SQL.Query) > 16<<10 || strings.ContainsRune(spec.SQL.Query, 0) || len(spec.SQL.ExpectedValue) > 16<<10 {
				return fmt.Errorf("%w: probe %q SQL statement is outside bounds", ErrInvalidConfig, spec.ID)
			}
		default:
			return fmt.Errorf("%w: probe %q has unsupported kind", ErrInvalidConfig, spec.ID)
		}
	}
	return nil
}

func payloadCount(spec Spec) int {
	count := 0
	for _, present := range []bool{spec.HTTP != nil, spec.TCP != nil, spec.Command != nil, spec.Data != nil, spec.SQL != nil} {
		if present {
			count++
		}
	}
	return count
}
