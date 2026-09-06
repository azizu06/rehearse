//go:build darwin || linux

package probe

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// openDataFile traverses from root through no-follow descriptors so a path
// replacement cannot redirect a data assertion outside restored data.
func openDataFile(root, candidate string) (*os.File, error) {
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == "." || filepath.IsAbs(relative) || strings.HasPrefix(relative, "..") {
		return nil, fmt.Errorf("data assertion path escapes restored root")
	}
	directory, err := os.Open(root)
	if err != nil {
		return nil, err
	}
	components := strings.Split(relative, string(os.PathSeparator))
	for index, component := range components {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC
		if index < len(components)-1 {
			flags |= unix.O_DIRECTORY
		}
		fd, err := unix.Openat(int(directory.Fd()), component, flags, 0)
		_ = directory.Close()
		if err != nil {
			return nil, err
		}
		directory = os.NewFile(uintptr(fd), component)
	}
	return directory, nil
}
