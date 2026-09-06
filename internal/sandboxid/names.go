package sandboxid

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	ProjectPrefix        = "rehearse-"
	ProjectDigestLength  = 24
	MaxResourceKeyLength = 26
)

var (
	ErrInvalidRunID    = errors.New("invalid sandbox run ID")
	ErrInvalidResource = errors.New("invalid sandbox resource name")
	ErrCorruptManifest = errors.New("corrupt sandbox cleanup ownership requires manual intervention")
	runIDPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	resourceKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,25}$`)
)

func Project(runID string) (string, string, error) {
	if !runIDPattern.MatchString(runID) {
		return "", "", fmt.Errorf("%w: run ID must match %s", ErrInvalidRunID, runIDPattern)
	}
	digest := sha256.Sum256([]byte(runID))
	return ProjectFromDigest(runID, digest)
}

func ProjectFromDigest(runID string, digest [sha256.Size]byte) (string, string, error) {
	if !runIDPattern.MatchString(runID) {
		return "", "", fmt.Errorf("%w: run ID must match %s", ErrInvalidRunID, runIDPattern)
	}
	fingerprint := fmt.Sprintf("%x", digest)
	return ProjectPrefix + fingerprint[:ProjectDigestLength], fingerprint, nil
}

func ValidResourceKey(key string) bool {
	return len(key) <= MaxResourceKeyLength && resourceKeyPattern.MatchString(key)
}

func ResourceName(projectName, kind, key string) (string, error) {
	if !ValidResourceKey(key) {
		return "", ErrInvalidResource
	}
	switch kind {
	case "container":
		return projectName + "-" + key + "-1", nil
	case "network", "volume":
		return projectName + "_" + key, nil
	default:
		return "", ErrInvalidResource
	}
}

func ValidateResourceName(runID, kind, name string) error {
	projectName, _, err := Project(runID)
	if err != nil {
		return ErrInvalidResource
	}
	var key string
	switch kind {
	case "container":
		prefix := projectName + "-"
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, "-1") {
			return ErrInvalidResource
		}
		key = strings.TrimSuffix(strings.TrimPrefix(name, prefix), "-1")
	case "network", "volume":
		prefix := projectName + "_"
		if !strings.HasPrefix(name, prefix) {
			return ErrInvalidResource
		}
		key = strings.TrimPrefix(name, prefix)
	default:
		return ErrInvalidResource
	}
	expected, err := ResourceName(projectName, kind, key)
	if err != nil || expected != name {
		return ErrInvalidResource
	}
	return nil
}
