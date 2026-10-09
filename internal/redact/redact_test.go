package redact_test

import (
	"strings"
	"testing"

	"github.com/azizu06/rehearse/internal/redact"
)

func TestStringReturnsValueUnchangedWithNoMarkers(t *testing.T) {
	t.Parallel()

	if got, want := redact.New().String("no secrets here"), "no secrets here"; got != want {
		t.Fatalf("String(%q) = %q, want %q", "no secrets here", got, want)
	}
}

func TestStringAvoidsSentinelCollisionWithNullByteInValue(t *testing.T) {
	t.Parallel()

	value := "pre\x00fix secret end"
	got := redact.New("secret").String(value)
	want := "pre\x00fix [REDACTED] end"
	if got != want {
		t.Fatalf("String(%q) = %q, want %q", value, got, want)
	}
}

func TestBoundedStringKeepsValueWhenCutAlignsWithReplacementBoundary(t *testing.T) {
	t.Parallel()

	got, truncated := redact.New("x").BoundedString("xx", len(redact.Replacement))
	if got != redact.Replacement {
		t.Fatalf("BoundedString() = %q, want %q", got, redact.Replacement)
	}
	if !truncated {
		t.Fatal("BoundedString() truncated = false, want true")
	}
}

func TestEqualLengthOverlappingMarkersUseStableOrder(t *testing.T) {
	t.Parallel()

	for range 100 {
		if got, want := redact.New("aba", "bab").String("abab"), "[REDACTED]b"; got != want {
			t.Fatalf("redaction = %q, want %q", got, want)
		}
	}
}

func TestMaxMarkerBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		markers []string
		want    int
	}{
		{name: "no markers", markers: nil, want: 0},
		{name: "only empty marker", markers: []string{""}, want: 0},
		{name: "single marker", markers: []string{"secret"}, want: len("secret")},
		{name: "longest marker wins", markers: []string{"ab", "abcdefgh", "abcd"}, want: len("abcdefgh")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := redact.New(test.markers...).MaxMarkerBytes(); got != test.want {
				t.Fatalf("MaxMarkerBytes() = %d, want %d", got, test.want)
			}
		})
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

func TestBoundedStringTrimsPartialMarkerAtTruncationBoundary(t *testing.T) {
	t.Parallel()

	marker := "secret"
	tests := []struct {
		name  string
		value string
		limit int
		want  string
	}{
		{name: "multi-byte overlap trimmed", value: "aaasecretbbb", limit: 6, want: "aaa"},
		{name: "single-byte overlap trimmed", value: "zzzsqqqqqqqqqq", limit: 4, want: "zzz"},
		{name: "no overlap kept in full", value: "aaaaaaaaaaaa", limit: 6, want: "aaaaaa"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, truncated := redact.New(marker).BoundedString(test.value, test.limit)
			if got != test.want {
				t.Fatalf("BoundedString(%q, %d) = %q, want %q", test.value, test.limit, got, test.want)
			}
			if !truncated {
				t.Fatalf("BoundedString(%q, %d) truncated = false, want true", test.value, test.limit)
			}
			if strings.Contains(got, marker[:1]) {
				t.Fatalf("BoundedString(%q, %d) = %q leaks marker fragment", test.value, test.limit, got)
			}
		})
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
