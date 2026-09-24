package journal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRejectHardLinkedJournal(t *testing.T) {
	t.Parallel()

	t.Run("regular file with a single link is accepted", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "rehearse.db")
		if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
			t.Fatalf("write journal file: %v", err)
		}
		if err := rejectHardLinkedJournal(path); err != nil {
			t.Fatalf("rejectHardLinkedJournal() = %v, want nil", err)
		}
	})

	t.Run("hard-linked file is rejected", func(t *testing.T) {
		t.Parallel()
		directory := t.TempDir()
		path := filepath.Join(directory, "rehearse.db")
		link := filepath.Join(directory, "rehearse.db.alias")
		if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
			t.Fatalf("write journal file: %v", err)
		}
		if err := os.Link(path, link); err != nil {
			t.Fatalf("create hard link: %v", err)
		}
		if err := rejectHardLinkedJournal(path); err == nil {
			t.Fatal("rejectHardLinkedJournal() = nil, want error for hard-linked journal")
		}
	})

	t.Run("missing path is rejected", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "missing.db")
		if err := rejectHardLinkedJournal(path); err == nil {
			t.Fatal("rejectHardLinkedJournal() = nil, want error for missing journal")
		}
	})
}
