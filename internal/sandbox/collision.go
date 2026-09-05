package sandbox

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/azizu06/rehearse/internal/sandboxid"
)

const dockerMetadataOutputLimit = 4 << 20

func ensureResolvedProjectVacant(ctx context.Context, command dockerCommand, identity identity, model composeModel) error {
	resources, err := resolvedResourceNames(identity, model)
	if err != nil {
		return err
	}
	for _, resource := range resources {
		ids, err := findExactResourceIDs(ctx, command, resource)
		if err != nil {
			return fmt.Errorf("inspect exact Compose %s name %q: %w", resource.kind, resource.name, err)
		}
		for range ids {
			inspected, err := inspectResource(ctx, command, resource)
			if err != nil {
				return fmt.Errorf("inspect exact Compose %s %q ownership: %w", resource.kind, resource.name, err)
			}
			if !labelsContain(inspected.Labels, identity.labels()) {
				return ErrProjectCollision
			}
			return ErrProjectExists
		}
	}
	return nil
}

func findExactResourceIDs(ctx context.Context, command dockerCommand, resource resolvedResourceName) ([]string, error) {
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
		return nil, err
	}
	return strings.Fields(string(output)), nil
}

type resolvedResourceName struct {
	kind          string
	name          string
	all           string
	inspectFormat string
}

func resourceDescriptor(kind, name string) resolvedResourceName {
	switch kind {
	case "container":
		return resolvedResourceName{
			kind: kind, name: name, all: "--all",
			inspectFormat: `{"id":{{json .Id}},"name":{{json .Name}},"labels":{{json .Config.Labels}}}`,
		}
	case "network":
		return resolvedResourceName{
			kind: kind, name: name,
			inspectFormat: `{"id":{{json .Id}},"name":{{json .Name}},"labels":{{json .Labels}}}`,
		}
	default:
		return resolvedResourceName{
			kind: kind, name: name,
			inspectFormat: `{"id":"","name":{{json .Name}},"labels":{{json .Labels}}}`,
		}
	}
}

func resolvedResourceNames(identity identity, model composeModel) ([]resolvedResourceName, error) {
	resources := make([]resolvedResourceName, 0, len(model.Services)+len(model.Networks)+len(model.Volumes)+1)
	for name := range model.Services {
		resourceName, err := sandboxid.ResourceName(identity.projectName, "container", name)
		if err != nil {
			return nil, fmt.Errorf("resolve Compose container name: %w", err)
		}
		resources = append(resources, resourceDescriptor("container", resourceName))
	}
	networks := make(map[string]composeResource, len(model.Networks)+1)
	for name, network := range model.Networks {
		networks[name] = network
	}
	for _, service := range model.Services {
		if service.NetworkMode != "none" && len(service.Networks) == 0 {
			if _, exists := networks["default"]; !exists {
				resourceName, err := sandboxid.ResourceName(identity.projectName, "network", "default")
				if err != nil {
					return nil, fmt.Errorf("resolve default Compose network name: %w", err)
				}
				networks["default"] = composeResource{Name: resourceName}
			}
			break
		}
	}
	for name, network := range networks {
		resourceName, err := resolvedComposeResourceName(identity, "network", name, network)
		if err != nil {
			return nil, err
		}
		resources = append(resources, resourceDescriptor("network", resourceName))
	}
	for name, volume := range model.Volumes {
		resourceName, err := resolvedComposeResourceName(identity, "volume", name, volume)
		if err != nil {
			return nil, err
		}
		resources = append(resources, resourceDescriptor("volume", resourceName))
	}
	sort.Slice(resources, func(left, right int) bool {
		if resources[left].kind == resources[right].kind {
			return resources[left].name < resources[right].name
		}
		return resources[left].kind < resources[right].kind
	})
	return resources, nil
}

func resolvedComposeResourceName(identity identity, kind, key string, resource composeResource) (string, error) {
	expected, err := sandboxid.ResourceName(identity.projectName, kind, key)
	if err != nil {
		return "", fmt.Errorf("resolve Compose %s name: %w", kind, err)
	}
	if resource.Name != "" && resource.Name != expected {
		return "", fmt.Errorf("%w: Compose %s name does not match its generated identity", ErrUnsafeCompose, kind)
	}
	return expected, nil
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
		if identity.claimID != "" {
			filters = append(filters, "--filter", "label="+sandboxClaimLabel+"="+identity.claimID)
		}
	}
	return filters
}
