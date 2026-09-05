package redact_test

import (
	"strings"
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

func TestBoundedStringDoesNotExposeMarkerPrefixAfterEarlierRedactions(t *testing.T) {
	t.Parallel()

	marker := "abcdefghijklmnopqrst"
	value := marker + marker + strings.Repeat("x", 70) + marker
	got, _ := redact.New(marker).BoundedString(value, 100)
	if strings.Contains(got, marker[:10]) {
		t.Fatalf("bounded redaction leaked marker prefix: %q", got)
	}
}
