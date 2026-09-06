package probe_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azizu06/rehearse/internal/drill"
	"github.com/azizu06/rehearse/internal/evidence"
	"github.com/azizu06/rehearse/internal/probe"
	"github.com/azizu06/rehearse/internal/redact"
)

func TestHTTPRetryProducesSuccessfulCanonicalReport(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			http.Error(response, "restored service is warming up", http.StatusServiceUnavailable)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	configuration, err := probe.ParseConfig(strings.NewReader(fmt.Sprintf(`{
  "schema_version": "rehearse.probes/v1",
  "probes": [{
    "ordinal": 1,
    "id": "restored-api",
    "kind": "http",
    "required": true,
    "retry": {"deadline": "1s", "backoff": "10ms", "max_attempts": 3},
    "http": {"url": %q, "expected_status": 204}
  }]
}`, server.URL)))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}

	runner := probe.NewRunner(probe.Options{
		HTTPClient: server.Client(),
		Redactor:   redact.New("issue-10-secret-marker"),
	})
	result := runner.Run(context.Background(), configuration)
	if !result.RequiredPassed {
		t.Fatalf("required probes failed: %#v", result.Probes)
	}
	if got, want := result.Probes[0].Attempts, 2; got != want {
		t.Fatalf("attempts = %d, want %d", got, want)
	}
	if detail := result.Probes[0].Detail; detail != "" {
		t.Fatalf("successful retry detail = %q, want empty", detail)
	}
	if observed := result.Probes[0].Observed; observed != "HTTP 204" {
		t.Fatalf("successful retry observed = %q, want final passing attempt", observed)
	}

	startedAt := time.Date(2026, time.July, 15, 18, 0, 0, 0, time.UTC)
	probeEvidence := result.Probes[0]
	probeEvidence.StartedAt = startedAt.Add(4 * time.Second)
	probeEvidence.FinishedAt = startedAt.Add(5 * time.Second)
	probeEvidence.Duration = time.Second
	report := evidence.Report{
		SchemaVersion: evidence.SchemaVersion,
		RunID:         "run-10",
		PlanID:        "plan-10",
		PlanVersion:   1,
		RecoveryPoint: evidence.RecoveryPoint{ID: "snapshot-10", SelectedAt: startedAt},
		Stages: []evidence.Stage{
			{Ordinal: 1, Name: drill.StageProbe, StartedAt: startedAt.Add(4 * time.Second), FinishedAt: startedAt.Add(5 * time.Second), Duration: time.Second},
		},
		Probes:  []probe.Evidence{probeEvidence},
		Outcome: drill.OutcomeSucceeded,
		Cleanup: drill.CleanupSucceeded,
	}

	first, err := report.CanonicalJSON(redact.New("issue-10-secret-marker"))
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	second, err := report.CanonicalJSON(redact.New("issue-10-secret-marker"))
	if err != nil {
		t.Fatalf("CanonicalJSON second pass: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("canonical report changed between encodes:\nfirst:  %s\nsecond: %s", first, second)
	}
	for _, marker := range []string{
		`"schema_version":"rehearse.report/v1"`,
		`"ordinal":1,"id":"restored-api"`,
		`"attempts":2`,
		`"outcome":"succeeded"`,
		`"cleanup":"succeeded"`,
	} {
		if !bytes.Contains(first, []byte(marker)) {
			t.Fatalf("canonical report %s does not contain %s", first, marker)
		}
	}
}
