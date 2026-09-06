package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

type projectLocker struct {
	root string
}

type projectLock struct {
	file *os.File
}

func newProjectLocker(root string) projectLocker {
	if root == "" {
		root = filepath.Join(os.TempDir(), "rehearse-sandbox-locks")
	}
	return projectLocker{root: filepath.Clean(root)}
}

func (locker projectLocker) lock(identity identity) (*projectLock, error) {
	if err := os.MkdirAll(locker.root, 0o700); err != nil {
		return nil, fmt.Errorf("create sandbox lock directory: %w", err)
	}
	if err := os.Chmod(locker.root, 0o700); err != nil {
		return nil, fmt.Errorf("protect sandbox lock directory: %w", err)
	}
	path := filepath.Join(locker.root, identity.projectName+".lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open sandbox project lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrProjectBusy
		}
		return nil, fmt.Errorf("lock sandbox project: %w", err)
	}
	return &projectLock{file: file}, nil
}

func (lock *projectLock) release() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	unlockErr := unix.Flock(int(lock.file.Fd()), unix.LOCK_UN)
	closeErr := lock.file.Close()
	return errors.Join(unlockErr, closeErr)
}
