package journal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalJournalPathRejectsHardLinkedJournal(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "rehearse.db")
	if err := os.WriteFile(original, []byte("journal"), 0o600); err != nil {
		t.Fatalf("seed journal file: %v", err)
	}
	linked := filepath.Join(dir, "rehearse-alias.db")
	if err := os.Link(original, linked); err != nil {
		t.Fatalf("create hard link: %v", err)
	}

	if _, err := canonicalJournalPath(linked); err == nil || err.Error() != "journal hard links are not supported" {
		t.Fatalf("canonicalJournalPath(%q) error = %v, want hard link rejection", linked, err)
	}
}

func TestCanonicalJournalPathAcceptsSingleLinkedJournal(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "rehearse.db")
	if err := os.WriteFile(existing, []byte("journal"), 0o600); err != nil {
		t.Fatalf("seed journal file: %v", err)
	}

	testCases := []struct {
		name string
		path string
	}{
		{name: "existing file", path: existing},
		{name: "not yet created", path: filepath.Join(dir, "new.db")},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			resolved, err := canonicalJournalPath(testCase.path)
			if err != nil {
				t.Fatalf("canonicalJournalPath(%q) error = %v, want nil", testCase.path, err)
			}
			if filepath.Base(resolved) != filepath.Base(testCase.path) {
				t.Fatalf("canonicalJournalPath(%q) = %q, want same base name", testCase.path, resolved)
			}
		})
	}
}

func TestRejectHardLinkedJournalRequiresExistingPath(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.db")

	if err := rejectHardLinkedJournal(missing); err == nil {
		t.Fatalf("rejectHardLinkedJournal(%q) error = nil, want stat failure", missing)
	}
}
