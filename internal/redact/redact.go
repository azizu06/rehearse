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
		leftMarker, rightMarker := result.markers[left], result.markers[right]
		if len(leftMarker) == len(rightMarker) {
			return leftMarker < rightMarker
		}
		return len(leftMarker) > len(rightMarker)
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

// MaxMarkerBytes reports the extra source bytes needed to redact a marker that
// begins at an evidence boundary.
func (redactor Redactor) MaxMarkerBytes() int {
	if len(redactor.markers) == 0 {
		return 0
	}
	return len(redactor.markers[0])
}

// BoundedString redacts before returning bounded text and removes a partial
// marker at a truncated source boundary.
func (redactor Redactor) BoundedString(value string, limit int) (string, bool) {
	truncated := len(value) > limit
	if truncated {
		value = value[:limit]
		value = redactor.trimMarkerPrefix(value)
	}
	value = redactor.String(value)
	if len(value) > limit {
		value = value[:limit]
		truncated = true
	}
	return value, truncated
}

func (redactor Redactor) trimMarkerPrefix(value string) string {
	trim := 0
	for _, marker := range redactor.markers {
		maximum := min(len(marker)-1, len(value))
		for length := maximum; length > trim; length-- {
			if strings.HasSuffix(value, marker[:length]) {
				trim = length
				break
			}
		}
	}
	return value[:len(value)-trim]
}
