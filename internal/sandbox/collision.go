package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
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

func ensureResolvedProjectVacant(ctx context.Context, command dockerCommand, identity identity, model composeModel) error {
	resources := resolvedResourceNames(identity, model)
	for _, resource := range resources {
		filterName := resource.name
		if resource.kind == "container" {
			filterName = "/" + filterName
		}
		filter := "name=^" + regexp.QuoteMeta(filterName) + "$"
		args := []string{resource.kind, "ls"}
		if resource.all != "" {
			args = append(args, resource.all)
		}
		args = append(args, "--quiet", "--filter", filter)
		output, err := command.run(ctx, dockerMetadataOutputLimit, args...)
		if err != nil {
			return fmt.Errorf("inspect exact Compose %s name %q: %w", resource.kind, resource.name, err)
		}
		for _, id := range strings.Fields(string(output)) {
			labelsOutput, err := command.run(ctx, dockerMetadataOutputLimit,
				resource.kind, "inspect", "--format", resource.inspectFormat, id,
			)
			if err != nil {
				return fmt.Errorf("inspect exact Compose %s %q ownership: %w", resource.kind, resource.name, err)
			}
			var labels map[string]string
			if err := json.Unmarshal([]byte(strings.TrimSpace(string(labelsOutput))), &labels); err != nil {
				return ErrProjectCollision
			}
			if !labelsContain(labels, identity.labels()) {
				return ErrProjectCollision
			}
			return ErrProjectExists
		}
	}
	return nil
}

type resolvedResourceName struct {
	kind          string
	name          string
	all           string
	inspectFormat string
}

func resolvedResourceNames(identity identity, model composeModel) []resolvedResourceName {
	resources := make([]resolvedResourceName, 0, len(model.Services)+len(model.Networks)+len(model.Volumes)+1)
	for name := range model.Services {
		resources = append(resources, resolvedResourceName{
			kind: "container", name: identity.projectName + "-" + name + "-1", all: "--all",
			inspectFormat: `{{json .Config.Labels}}`,
		})
	}
	networks := make(map[string]composeResource, len(model.Networks)+1)
	for name, network := range model.Networks {
		networks[name] = network
	}
	for _, service := range model.Services {
		if service.NetworkMode != "none" && len(service.Networks) == 0 {
			if _, exists := networks["default"]; !exists {
				networks["default"] = composeResource{Name: identity.projectName + "_default"}
			}
			break
		}
	}
	for name, network := range networks {
		resources = append(resources, resolvedResourceName{
			kind: "network", name: resolvedComposeResourceName(identity, name, network),
			inspectFormat: `{{json .Labels}}`,
		})
	}
	for name, volume := range model.Volumes {
		resources = append(resources, resolvedResourceName{
			kind: "volume", name: resolvedComposeResourceName(identity, name, volume),
			inspectFormat: `{{json .Labels}}`,
		})
	}
	sort.Slice(resources, func(left, right int) bool {
		if resources[left].kind == resources[right].kind {
			return resources[left].name < resources[right].name
		}
		return resources[left].kind < resources[right].kind
	})
	return resources
}

func resolvedComposeResourceName(identity identity, name string, resource composeResource) string {
	if resource.Name != "" {
		return resource.Name
	}
	return identity.projectName + "_" + name
}

func ownershipFilters(identity identity, includeFingerprint bool) []string {
	filters := []string{
		"--filter", "label=" + managedLabel + "=true",
		"--filter", "label=" + projectLabel + "=" + identity.projectName,
	}
	if includeFingerprint {
		filters = append(filters,
			"--filter", "label="+runFingerprintLabel+"="+identity.fingerprint,
			"--filter", "label="+runIDLabel+"="+identity.runID,
		)
	}
	return filters
}
