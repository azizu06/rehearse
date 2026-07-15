package sandbox

import (
	"context"
	"fmt"
	"strings"
)

const dockerMetadataOutputLimit = 4 << 20

func ensureProjectVacant(ctx context.Context, command dockerCommand, identity identity) error {
	resources := []struct {
		kind          string
		listArgs      []string
		inspectFormat string
	}{
		{kind: "container", listArgs: []string{"container", "ls", "--all", "--quiet"}, inspectFormat: `{{ index .Config.Labels "` + runFingerprintLabel + `" }}`},
		{kind: "network", listArgs: []string{"network", "ls", "--quiet"}, inspectFormat: `{{ index .Labels "` + runFingerprintLabel + `" }}`},
		{kind: "volume", listArgs: []string{"volume", "ls", "--quiet"}, inspectFormat: `{{ index .Labels "` + runFingerprintLabel + `" }}`},
	}
	foundMatching := false
	for _, resource := range resources {
		args := append(append([]string{}, resource.listArgs...), ownershipFilters(identity, false)...)
		output, err := command.run(ctx, dockerMetadataOutputLimit, args...)
		if err != nil {
			return fmt.Errorf("list candidate Rehearse %s resources: %w", resource.kind, err)
		}
		for _, id := range strings.Fields(string(output)) {
			fingerprint, err := command.run(ctx, dockerMetadataOutputLimit,
				resource.kind, "inspect", "--format", resource.inspectFormat, id,
			)
			if err != nil {
				return fmt.Errorf("inspect candidate Rehearse %s: %w", resource.kind, err)
			}
			if strings.TrimSpace(string(fingerprint)) != identity.fingerprint {
				return ErrProjectCollision
			}
			foundMatching = true
		}
	}
	if foundMatching {
		return ErrProjectExists
	}
	return nil
}

func ownershipFilters(identity identity, includeFingerprint bool) []string {
	filters := []string{
		"--filter", "label=" + managedLabel + "=true",
		"--filter", "label=" + projectLabel + "=" + identity.projectName,
	}
	if includeFingerprint {
		filters = append(filters, "--filter", "label="+runFingerprintLabel+"="+identity.fingerprint)
	}
	return filters
}
