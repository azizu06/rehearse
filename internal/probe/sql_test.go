package probe_test

import (
	"context"
	"strings"
	"testing"

	"github.com/azizu06/rehearse/internal/probe"
)

func TestSQLProbeFailsWhenConnectionIsUnavailable(t *testing.T) {
	t.Parallel()

	configuration, err := probe.ParseConfig(strings.NewReader(`{
  "schema_version":"rehearse.probes/v1",
  "probes":[{
    "ordinal":1,"id":"primary-role-check","kind":"sql","required":true,
    "retry":{"deadline":"100ms","backoff":"10ms","max_attempts":1},
    "sql":{"connection_id":"primary","expected_role":"app_user","query":"SELECT 1","expected_value":"1"}
  }]
}`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}

	result := probe.NewRunner(probe.Options{}).Run(context.Background(), configuration)

	if result.RequiredPassed {
		t.Fatalf("Runner.Run() required passed = true, want false with no SQL connection configured")
	}
	if got, want := result.Probes[0].Status, probe.StatusFailed; got != want {
		t.Fatalf("probe status = %q, want %q", got, want)
	}
	if got, want := result.Probes[0].Detail, "SQL probe connection is unavailable"; got != want {
		t.Fatalf("probe detail = %q, want %q", got, want)
	}
	if got, want := result.Probes[0].Attempts, 1; got != want {
		t.Fatalf("probe attempts = %d, want %d", got, want)
	}
}
