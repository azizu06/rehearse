package journal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

type journalOwnership struct {
	file *os.File
}

func acquireJournalOwnership(path string) (*journalOwnership, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve journal ownership lock: %w", err)
	}
	file, err := os.OpenFile(filepath.Clean(absolute)+".owner.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open journal ownership lock: %w", err)
	}
	if err := os.Chmod(file.Name(), 0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("protect journal ownership lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrJournalOwned
		}
		return nil, fmt.Errorf("lock journal ownership: %w", err)
	}
	return &journalOwnership{file: file}, nil
}

func canonicalJournalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve journal path: %w", err)
	}
	absolute = filepath.Clean(absolute)
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return resolved, rejectHardLinkedJournal(resolved)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("resolve journal path: %w", err)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", fmt.Errorf("resolve journal directory: %w", err)
	}
	return filepath.Join(parent, filepath.Base(absolute)), nil
}

func rejectHardLinkedJournal(path string) error {
	var metadata unix.Stat_t
	if err := unix.Stat(path, &metadata); err != nil {
		return fmt.Errorf("inspect journal path: %w", err)
	}
	if metadata.Nlink != 1 {
		return errors.New("journal hard links are not supported")
	}
	return nil
}

func (ownership *journalOwnership) release() error {
	if ownership == nil || ownership.file == nil {
		return nil
	}
	unlockErr := unix.Flock(int(ownership.file.Fd()), unix.LOCK_UN)
	closeErr := ownership.file.Close()
	ownership.file = nil
	return errors.Join(unlockErr, closeErr)
}
