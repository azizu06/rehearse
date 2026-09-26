package sandbox

import (
	"reflect"
	"testing"
)

func TestSortedLabels(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   []string
	}{
		{name: "empty", labels: map[string]string{}, want: []string{}},
		{name: "single", labels: map[string]string{"rehearse.run": "run-1"}, want: []string{"rehearse.run=run-1"}},
		{
			name: "sorted by key",
			labels: map[string]string{
				"rehearse.run":    "run-1",
				"rehearse.claim":  "claim-1",
				"rehearse.kind":   "network",
				"rehearse.active": "true",
			},
			want: []string{
				"rehearse.active=true",
				"rehearse.claim=claim-1",
				"rehearse.kind=network",
				"rehearse.run=run-1",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sortedLabels(tt.labels)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("sortedLabels(%v) = %v, want %v", tt.labels, got, tt.want)
			}
		})
	}
}
