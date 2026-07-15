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
