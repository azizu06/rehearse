package redact_test

import (
	"testing"

	"github.com/azizu06/rehearse/internal/redact"
)

func TestEqualLengthOverlappingMarkersUseStableOrder(t *testing.T) {
	t.Parallel()

	for range 100 {
		if got, want := redact.New("aba", "bab").String("abab"), "[REDACTED]b"; got != want {
			t.Fatalf("redaction = %q, want %q", got, want)
		}
	}
}
