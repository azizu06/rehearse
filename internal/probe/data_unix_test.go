//go:build darwin || linux

package probe

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenDataFileDoesNotFollowReplacedSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	candidate := filepath.Join(root, "orders.check")
	if err := os.Symlink(filepath.Join(outside, "outside.check"), candidate); err != nil {
		t.Fatalf("create replacement symlink: %v", err)
	}
	if file, err := openDataFile(root, candidate); err == nil {
		_ = file.Close()
		t.Fatal("openDataFile followed a replacement symlink")
	}
}
