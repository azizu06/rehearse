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

func TestStringWithNoMarkersReturnsValueUnchanged(t *testing.T) {
	t.Parallel()

	if got, want := redact.New().String("hello world"), "hello world"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestBoundedStringKeepsFullReplacementWhenTruncationBoundaryIsNotPartial(t *testing.T) {
	t.Parallel()

	// "ab" expands to the full "[REDACTED]" (10 bytes) at exactly the 10-byte
	// limit, so the post-replacement truncation boundary lands clean: no
	// suffix of the result is a partial prefix of Replacement, exercising the
	// trimReplacementPrefix fallback that returns the value unchanged.
	marker := "ab"
	value := marker + strings.Repeat("c", 8)
	got, truncated := redact.New(marker).BoundedString(value, len(redact.Replacement))
	if got != redact.Replacement {
		t.Fatalf("BoundedString(%q, %d) = %q, want %q", value, len(redact.Replacement), got, redact.Replacement)
	}
	if !truncated {
		t.Fatalf("BoundedString(%q, %d) truncated = false, want true", value, len(redact.Replacement))
	}
}

func TestSentinelExtendsWhenValueAlreadyContainsNulByte(t *testing.T) {
	t.Parallel()

	value := "secret\x00data"
	got := redact.New("secret").String(value)
	want := redact.Replacement + "\x00data"
	if got != want {
		t.Fatalf("String(%q) = %q, want %q", value, got, want)
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
