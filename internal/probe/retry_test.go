package probe_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/azizu06/rehearse/internal/probe"
)

func TestRetryStopsAtFirstExhaustedConstraint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		deadline      string
		maxAttempts   int
		wantAttempts  int
		wantExhausted probe.ExhaustedBy
	}{
		{name: "attempt ceiling first", deadline: "1s", maxAttempts: 3, wantAttempts: 3, wantExhausted: probe.ExhaustedAttempts},
		{name: "deadline budget first", deadline: "25ms", maxAttempts: 100, wantAttempts: 3, wantExhausted: probe.ExhaustedDeadline},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			clock := &fakeClock{now: time.Date(2026, time.July, 15, 19, 0, 0, 0, time.UTC)}
			client := &http.Client{Transport: failingTransport{}}
			configuration, err := probe.ParseConfig(strings.NewReader(`{
  "schema_version":"rehearse.probes/v1",
  "probes":[{
    "ordinal":1,"id":"bounded-retry","kind":"http","required":true,
    "retry":{"deadline":"` + test.deadline + `","backoff":"10ms","max_attempts":` + itoa(test.maxAttempts) + `},
    "http":{"url":"http://127.0.0.1:1","expected_status":200}
  }]
}`))
			if err != nil {
				t.Fatalf("ParseConfig: %v", err)
			}
			result := probe.NewRunner(probe.Options{HTTPClient: client, Clock: clock}).Run(context.Background(), configuration)
			if got := result.Probes[0].Attempts; got != test.wantAttempts {
				t.Fatalf("attempts = %d, want %d", got, test.wantAttempts)
			}
			if got := result.Probes[0].ExhaustedBy; got != test.wantExhausted {
				t.Fatalf("exhausted_by = %q, want %q", got, test.wantExhausted)
			}
		})
	}
}

func TestRunRejectsAttemptSuccessAfterCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	client := &http.Client{Transport: cancellingSuccessfulTransport{cancel: cancel}}
	configuration, err := probe.ParseConfig(strings.NewReader(`{
  "schema_version":"rehearse.probes/v1",
  "probes":[{
    "ordinal":1,"id":"cancelled-success","kind":"http","required":true,
    "retry":{"deadline":"1s","backoff":"10ms","max_attempts":1},
    "http":{"url":"http://127.0.0.1/success","expected_status":204}
  }]
}`))
	if err != nil {
		t.Fatalf("ParseConfig() error = %v", err)
	}

	result := probe.NewRunner(probe.Options{HTTPClient: client}).Run(ctx, configuration)

	if result.RequiredPassed {
		t.Error("Runner.Run() required passed = true, want false after cancellation")
	}
	if got := result.Probes[0].Status; got != probe.StatusCancelled {
		t.Errorf("Runner.Run() probe status = %q, want %q", got, probe.StatusCancelled)
	}
	if got := result.Probes[0].Attempts; got != 1 {
		t.Errorf("Runner.Run() attempts = %d, want 1", got)
	}
}

func TestRunRejectsAttemptSuccessAfterProbeDeadline(t *testing.T) {
	t.Parallel()

	client := &http.Client{Transport: successfulAfterContextDoneTransport{}}
	configuration, err := probe.ParseConfig(strings.NewReader(`{
  "schema_version":"rehearse.probes/v1",
  "probes":[{
    "ordinal":1,"id":"late-success","kind":"http","required":true,
    "retry":{"deadline":"10ms","backoff":"10ms","max_attempts":1},
    "http":{"url":"http://127.0.0.1/success","expected_status":204}
  }]
}`))
	if err != nil {
		t.Fatalf("ParseConfig() error = %v", err)
	}

	result := probe.NewRunner(probe.Options{HTTPClient: client}).Run(context.Background(), configuration)

	if result.RequiredPassed {
		t.Error("Runner.Run() required passed = true, want false after probe deadline")
	}
	if got := result.Probes[0].Status; got != probe.StatusTimedOut {
		t.Errorf("Runner.Run() probe status = %q, want %q", got, probe.StatusTimedOut)
	}
	if got := result.Probes[0].ExhaustedBy; got != probe.ExhaustedDeadline {
		t.Errorf("Runner.Run() exhausted by = %q, want %q", got, probe.ExhaustedDeadline)
	}
	if got := result.Probes[0].Attempts; got != 1 {
		t.Errorf("Runner.Run() attempts = %d, want 1", got)
	}
}

type fakeClock struct {
	now time.Time
}

func (clock *fakeClock) Now() time.Time { return clock.now }

func (clock *fakeClock) Wait(ctx context.Context, duration time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		clock.now = clock.now.Add(duration)
		return nil
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("boundary unavailable")
}

type cancellingSuccessfulTransport struct {
	cancel context.CancelFunc
}

func (transport cancellingSuccessfulTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.cancel()
	return &http.Response{
		StatusCode: http.StatusNoContent,
		Header:     make(http.Header),
		Body:       http.NoBody,
		Request:    request,
	}, nil
}

type successfulAfterContextDoneTransport struct{}

func (successfulAfterContextDoneTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	<-request.Context().Done()
	return &http.Response{
		StatusCode: http.StatusNoContent,
		Header:     make(http.Header),
		Body:       http.NoBody,
		Request:    request,
	}, nil
}

func itoa(value int) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	var result [20]byte
	index := len(result)
	for value > 0 {
		index--
		result[index] = digits[value%10]
		value /= 10
	}
	return string(result[index:])
}
