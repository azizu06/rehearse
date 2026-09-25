package probe_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/azizu06/rehearse/internal/probe"
)

func TestConfigRejectsBoundsAndUntrustedCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		json string
	}{
		{name: "attempts above ceiling", json: validHTTPConfig(`"max_attempts":101`)},
		{name: "gapped ordinal", json: strings.Replace(validHTTPConfig(`"max_attempts":1`), `"ordinal":1`, `"ordinal":2`, 1)},
		{name: "unknown field", json: strings.Replace(validHTTPConfig(`"max_attempts":1`), `"required":true`, `"required":true,"surprise":1`, 1)},
		{name: "multiple JSON values", json: validHTTPConfig(`"max_attempts":1`) + `{}`},
		{name: "untrusted command", json: `{"schema_version":"rehearse.probes/v1","probes":[{"ordinal":1,"id":"command","kind":"command","required":true,"retry":{"deadline":"1s","backoff":"10ms","max_attempts":1},"command":{"executable":"/usr/bin/true","expected_exit_code":0,"trust_acknowledged":false}}]}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := probe.ParseConfig(strings.NewReader(test.json)); !errors.Is(err, probe.ErrInvalidConfig) {
				t.Fatalf("ParseConfig error = %v, want ErrInvalidConfig", err)
			}
		})
	}
}

func TestCanonicalConfigRejectsOversizedProgrammaticDocument(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "valid maximum command arguments exceed aggregate document cap", args: make([]string, 64)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			for index := range test.args {
				test.args[index] = strings.Repeat("a", 4096)
			}
			config := probe.Config{
				SchemaVersion: probe.SchemaVersion,
				Probes: []probe.Spec{{
					Ordinal: 1, ID: "large-command", Kind: probe.KindCommand, Required: true,
					Retry:   probe.RetryPolicy{Deadline: time.Second, Backoff: 10 * time.Millisecond, MaxAttempts: 1},
					Command: &probe.CommandSpec{Executable: "/usr/bin/true", Args: test.args, ExpectedExitCode: 0, TrustAcknowledged: true},
				}},
			}
			if err := config.Validate(); err != nil {
				t.Fatalf("Validate() error = %v, want individually valid command bounds", err)
			}
			if encoded, err := config.CanonicalJSON(); !errors.Is(err, probe.ErrInvalidConfig) || encoded != nil {
				t.Fatalf("CanonicalJSON() bytes = %d error = %v, want nil and ErrInvalidConfig", len(encoded), err)
			}
		})
	}
}

func TestConfigIsZero(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config probe.Config
		want   bool
	}{
		{name: "zero value", config: probe.Config{}, want: true},
		{name: "schema version only", config: probe.Config{SchemaVersion: probe.SchemaVersion}, want: false},
		{
			name:   "probes only",
			config: probe.Config{Probes: []probe.Spec{{Ordinal: 1, ID: "health", Kind: probe.KindHTTP}}},
			want:   false,
		},
		{
			name: "schema version and probes",
			config: probe.Config{
				SchemaVersion: probe.SchemaVersion,
				Probes:        []probe.Spec{{Ordinal: 1, ID: "health", Kind: probe.KindHTTP}},
			},
			want: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := test.config.IsZero(); got != test.want {
				t.Fatalf("IsZero() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestParseConfigBytesMatchesParseConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		json    string
		wantErr bool
	}{
		{name: "valid document", json: validHTTPConfig(`"max_attempts":1`)},
		{name: "invalid document", json: strings.Replace(validHTTPConfig(`"max_attempts":1`), `"required":true`, `"required":true,"surprise":1`, 1), wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fromBytes, bytesErr := probe.ParseConfigBytes([]byte(test.json))
			fromReader, readerErr := probe.ParseConfig(strings.NewReader(test.json))
			if test.wantErr {
				if !errors.Is(bytesErr, probe.ErrInvalidConfig) {
					t.Fatalf("ParseConfigBytes error = %v, want ErrInvalidConfig", bytesErr)
				}
				return
			}
			if bytesErr != nil {
				t.Fatalf("ParseConfigBytes error = %v, want nil", bytesErr)
			}
			if readerErr != nil {
				t.Fatalf("ParseConfig error = %v, want nil", readerErr)
			}
			if !reflect.DeepEqual(fromBytes, fromReader) {
				t.Fatalf("ParseConfigBytes() = %+v, want %+v", fromBytes, fromReader)
			}
		})
	}
}

func FuzzParseConfigNeverPanicsOrBypassesBounds(f *testing.F) {
	f.Add([]byte(validHTTPConfig(`"max_attempts":1`)))
	f.Add([]byte(`{"schema_version":"rehearse.probes/v1","probes":[]}`))
	f.Add([]byte(strings.Repeat("x", 300<<10)))
	f.Fuzz(func(t *testing.T, input []byte) {
		config, err := probe.ParseConfig(strings.NewReader(string(input)))
		if err == nil {
			if err := config.Validate(); err != nil {
				t.Fatalf("parser returned config that bypasses validation: %v", err)
			}
		}
	})
}

func validHTTPConfig(retryTail string) string {
	return `{"schema_version":"rehearse.probes/v1","probes":[{"ordinal":1,"id":"health","kind":"http","required":true,"retry":{"deadline":"1s","backoff":"10ms",` + retryTail + `},"http":{"url":"http://127.0.0.1:8080/health","expected_status":200}}]}`
}
