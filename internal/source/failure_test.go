package source_test

import (
	"testing"

	"github.com/azizu06/rehearse/internal/source"
)

func TestFailureError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		failure source.Failure
		want    string
	}{
		{
			name:    "safe hint takes precedence over operation and kind",
			failure: source.Failure{Kind: source.FailureProcess, Operation: "acquire", SafeHint: "restic command failed"},
			want:    "restic command failed",
		},
		{
			name:    "operation and kind without a safe hint",
			failure: source.Failure{Kind: source.FailureTimeout, Operation: "acquire"},
			want:    "source acquire failed (timeout)",
		},
		{
			name:    "kind only when operation and safe hint are absent",
			failure: source.Failure{Kind: source.FailureUnavailable},
			want:    "source operation failed (unavailable)",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := test.failure.Error(); got != test.want {
				t.Fatalf("Error() = %q, want %q", got, test.want)
			}
		})
	}
}
