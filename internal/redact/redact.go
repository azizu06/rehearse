// Package redact removes in-memory secret values from bounded evidence before
// it crosses a persistence or API boundary.
package redact

import (
	"sort"
	"strings"
)

const Replacement = "[REDACTED]"

// Redactor replaces literal secret markers. Markers never need to be persisted.
type Redactor struct {
	markers []string
}

// New constructs a redactor from non-empty literal secret values. Longer
// values are replaced first so overlapping markers cannot reveal a suffix.
func New(markers ...string) Redactor {
	unique := make(map[string]struct{}, len(markers))
	for _, marker := range markers {
		if marker != "" {
			unique[marker] = struct{}{}
		}
	}
	result := Redactor{markers: make([]string, 0, len(unique))}
	for marker := range unique {
		result.markers = append(result.markers, marker)
	}
	sort.Slice(result.markers, func(left, right int) bool {
		return len(result.markers[left]) > len(result.markers[right])
	})
	return result
}

// String returns value with every configured marker replaced.
func (redactor Redactor) String(value string) string {
	for _, marker := range redactor.markers {
		value = strings.ReplaceAll(value, marker, Replacement)
	}
	return value
}
