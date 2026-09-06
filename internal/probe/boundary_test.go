package probe_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/azizu06/rehearse/internal/probe"
)

func TestOptionalHTTPFailureStaysVisibleWhileRequiredTCPPasses(t *testing.T) {
	t.Parallel()

	httpServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		http.Error(response, "not ready", http.StatusServiceUnavailable)
	}))
	t.Cleanup(httpServer.Close)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go acceptOne(listener)

	configuration, err := probe.ParseConfig(strings.NewReader(fmt.Sprintf(`{
  "schema_version": "rehearse.probes/v1",
  "probes": [
    {
      "ordinal": 1,
      "id": "optional-health",
      "kind": "http",
      "required": false,
      "retry": {"deadline": "100ms", "backoff": "10ms", "max_attempts": 1},
      "http": {"url": %q, "expected_status": 204}
    },
    {
      "ordinal": 2,
      "id": "required-port",
      "kind": "tcp",
      "required": true,
      "retry": {"deadline": "100ms", "backoff": "10ms", "max_attempts": 1},
      "tcp": {"address": %q}
    }
  ]
}`, httpServer.URL, listener.Addr().String())))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}

	result := probe.NewRunner(probe.Options{HTTPClient: httpServer.Client()}).Run(context.Background(), configuration)
	if !result.RequiredPassed {
		t.Fatalf("optional failure changed required truth: %#v", result.Probes)
	}
	if got, want := len(result.Probes), 2; got != want {
		t.Fatalf("probe evidence count = %d, want %d", got, want)
	}
	if result.Probes[0].Status != probe.StatusFailed || result.Probes[1].Status != probe.StatusPassed {
		t.Fatalf("probe statuses = %q, %q", result.Probes[0].Status, result.Probes[1].Status)
	}
	if result.Probes[0].Ordinal != 1 || result.Probes[1].Ordinal != 2 {
		t.Fatalf("probe order changed: %#v", result.Probes)
	}
}

func TestHTTPProbeDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()

	var followed bool
	mux := http.NewServeMux()
	mux.HandleFunc("/redirect", func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, "/target", http.StatusFound)
	})
	mux.HandleFunc("/target", func(response http.ResponseWriter, _ *http.Request) {
		followed = true
		response.WriteHeader(http.StatusNoContent)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	configuration, err := probe.ParseConfig(strings.NewReader(fmt.Sprintf(`{
  "schema_version":"rehearse.probes/v1",
  "probes":[{
    "ordinal":1,"id":"redirect-boundary","kind":"http","required":true,
    "retry":{"deadline":"1s","backoff":"10ms","max_attempts":1},
    "http":{"url":%q,"expected_status":302}
  }]
}`, server.URL+"/redirect")))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	result := probe.NewRunner(probe.Options{HTTPClient: server.Client()}).Run(context.Background(), configuration)
	if !result.RequiredPassed || followed {
		t.Fatalf("redirect boundary result=%#v followed=%t", result.Probes, followed)
	}
}

func acceptOne(listener net.Listener) {
	connection, err := listener.Accept()
	if err == nil {
		_ = connection.Close()
	}
}
