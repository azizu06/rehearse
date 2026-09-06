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

func TestRedactionIsIdempotentWhenMarkersOverlapReplacement(t *testing.T) {
	t.Parallel()

	redactor := redact.New("secret", "sec", "RE", "A")
	first := redactor.String("secret A RE " + redact.Replacement)
	second := redactor.String(first)
	if second != first {
		t.Fatalf("String(String(value)) = %q, want %q", second, first)
	}
	if got, want := first, strings.Repeat(redact.Replacement+" ", 3)+redact.Replacement; got != want {
		t.Fatalf("String(value) = %q, want %q", got, want)
	}
	bounded, truncated := redactor.BoundedString("A-A-A", 15)
	if !truncated {
		t.Fatal("BoundedString() truncated = false, want true")
	}
	repeated, _ := redactor.BoundedString(bounded, 15)
	if repeated != bounded || len(repeated) > 15 {
		t.Fatalf("BoundedString(BoundedString(value)) = %q, want bounded %q", repeated, bounded)
	}
	spanning := redact.New("prefix"+redact.Replacement+"suffix", "RE")
	if got := spanning.String("prefix" + redact.Replacement + "suffix"); got != redact.Replacement {
		t.Fatalf("String(marker spanning replacement) = %q, want %q", got, redact.Replacement)
	}
}
