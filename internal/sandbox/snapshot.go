package sandbox

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

const (
	snapshotFileName = "compose.json"
	overrideFileName = "policy.json"
)

func snapshotDirectory(root string, identity identity) string {
	return filepath.Join(root, "rehearse-compose-"+identity.fingerprint)
}

func prepareSnapshotDirectory(root string, identity identity) (string, error) {
	path := snapshotDirectory(root, identity)
	if err := removeSnapshotDirectory(root, identity); err != nil {
		return "", err
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		return "", fmt.Errorf("create protected Compose snapshot directory: %w", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		_ = os.RemoveAll(path)
		return "", fmt.Errorf("protect Compose snapshot directory: %w", err)
	}
	return path, nil
}

func writeSnapshotFile(directory, name string, data []byte) (string, error) {
	path := filepath.Join(directory, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("create protected Compose snapshot: %w", err)
	}
	removeOnError := func(cause error) (string, error) {
		_ = file.Close()
		_ = os.Remove(path)
		return "", cause
	}
	if err := file.Chmod(0o600); err != nil {
		return removeOnError(fmt.Errorf("protect Compose snapshot: %w", err))
	}
	if _, err := file.Write(data); err != nil {
		return removeOnError(fmt.Errorf("write Compose snapshot: %w", err))
	}
	if err := file.Sync(); err != nil {
		return removeOnError(fmt.Errorf("sync Compose snapshot: %w", err))
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close Compose snapshot: %w", err)
	}
	return path, nil
}

func removeSnapshotDirectory(root string, identity identity) error {
	if root == "" {
		return nil
	}
	path := snapshotDirectory(root, identity)
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove protected Compose snapshot: %w", err)
	}
	return nil
}

// escapeSnapshotInterpolation preserves the fully rendered values when Compose
// parses the immutable JSON snapshot a second time for `up`.
func escapeSnapshotInterpolation(data []byte) []byte {
	return bytes.ReplaceAll(data, []byte("$"), []byte("$$"))
}
