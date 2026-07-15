package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

func reserveComposeResources(ctx context.Context, command dockerCommand, identity identity, model composeModel) error {
	for _, resource := range resolvedResourceNames(identity, model) {
		if resource.kind != "network" && resource.kind != "volume" {
			continue
		}
		args := []string{resource.kind, "create"}
		if resource.kind == "network" {
			args = append(args, "--driver", "bridge", "--internal")
		} else {
			args = append(args, "--driver", "local")
		}
		for _, label := range sortedOwnershipLabels(identity) {
			args = append(args, "--label", label)
		}
		args = append(args, resource.name)
		if _, err := command.run(ctx, dockerMetadataOutputLimit, args...); err != nil {
			return classifyReservationFailure(ctx, command, identity, resource, err)
		}
		labels, err := inspectReservedResource(ctx, command, resource)
		if err != nil {
			return fmt.Errorf("verify reserved Compose %s %q: %w", resource.kind, resource.name, err)
		}
		if !stringMapEqual(labels, identity.labels()) {
			return ErrProjectCollision
		}
	}
	return nil
}

func classifyReservationFailure(ctx context.Context, command dockerCommand, identity identity, resource resolvedResourceName, createErr error) error {
	labels, inspectErr := inspectReservedResource(ctx, command, resource)
	if inspectErr != nil {
		return fmt.Errorf("reserve Compose %s %q: %w", resource.kind, resource.name, createErr)
	}
	if !stringMapEqual(labels, identity.labels()) {
		return errors.Join(ErrProjectCollision, createErr)
	}
	return errors.Join(ErrProjectExists, createErr)
}

func inspectReservedResource(ctx context.Context, command dockerCommand, resource resolvedResourceName) (map[string]string, error) {
	output, err := command.run(ctx, dockerMetadataOutputLimit,
		resource.kind, "inspect", "--format", resource.inspectFormat, resource.name,
	)
	if err != nil {
		return nil, err
	}
	var labels map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(output))), &labels); err != nil {
		return nil, ErrProjectCollision
	}
	return labels, nil
}

func sortedOwnershipLabels(identity identity) []string {
	labels := identity.labels()
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+labels[key])
	}
	return result
}

func rewriteSnapshotReservedResources(data []byte, identity identity, model composeModel) ([]byte, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("decode Compose snapshot for resource reservation: %w", err)
	}

	networks := make(map[string]composeResource, len(model.Networks)+1)
	for name, network := range model.Networks {
		networks[name] = network
	}
	for _, service := range model.Services {
		if service.NetworkMode != "none" && len(service.Networks) == 0 {
			if _, exists := networks["default"]; !exists {
				networks["default"] = composeResource{}
			}
			break
		}
	}
	document["networks"] = encodeReservedResourceReferences(identity, networks)
	document["volumes"] = encodeReservedResourceReferences(identity, model.Volumes)

	rewritten, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode reserved Compose snapshot: %w", err)
	}
	return rewritten, nil
}

func encodeReservedResourceReferences(identity identity, resources map[string]composeResource) json.RawMessage {
	references := make(map[string]map[string]any, len(resources))
	for name, resource := range resources {
		references[name] = map[string]any{
			"external": true,
			"name":     resolvedComposeResourceName(identity, name, resource),
		}
	}
	data, _ := json.Marshal(references)
	return data
}
