package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

type createdResource struct {
	kind     string
	name     string
	daemonID string
}

type createdResourceLedger struct {
	resources map[string]createdResource
}

func newCreatedResourceLedger() *createdResourceLedger {
	return &createdResourceLedger{resources: make(map[string]createdResource)}
}

func (ledger *createdResourceLedger) add(resource createdResource) {
	ledger.resources[resource.kind+"\x00"+resource.name] = resource
}

func (ledger *createdResourceLedger) get(kind, name string) (createdResource, bool) {
	resource, exists := ledger.resources[kind+"\x00"+name]
	return resource, exists
}

func (ledger *createdResourceLedger) ordered() []createdResource {
	resources := make([]createdResource, 0, len(ledger.resources))
	for _, resource := range ledger.resources {
		resources = append(resources, resource)
	}
	order := map[string]int{"container": 0, "network": 1, "volume": 2}
	sort.Slice(resources, func(left, right int) bool {
		if order[resources[left].kind] != order[resources[right].kind] {
			return order[resources[left].kind] < order[resources[right].kind]
		}
		return resources[left].name < resources[right].name
	})
	return resources
}

type inspectedResource struct {
	DaemonID string            `json:"id"`
	Name     string            `json:"name"`
	Labels   map[string]string `json:"labels"`
}

func reserveComposeResources(ctx context.Context, command dockerCommand, identity identity, model composeModel, ledger *createdResourceLedger) error {
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
		inspected, err := inspectResource(ctx, command, resource)
		if err != nil {
			return fmt.Errorf("verify reserved Compose %s %q: %w", resource.kind, resource.name, err)
		}
		if !labelsContain(inspected.Labels, identity.labels()) {
			return ErrProjectCollision
		}
		ledger.add(createdResource{kind: resource.kind, name: resource.name, daemonID: inspected.DaemonID})
	}
	return nil
}

func classifyReservationFailure(ctx context.Context, command dockerCommand, identity identity, resource resolvedResourceName, createErr error) error {
	inspected, inspectErr := inspectResource(ctx, command, resource)
	if inspectErr != nil {
		return fmt.Errorf("reserve Compose %s %q: %w", resource.kind, resource.name, createErr)
	}
	if !labelsContain(inspected.Labels, identity.labels()) {
		return errors.Join(ErrProjectCollision, createErr)
	}
	return errors.Join(ErrProjectExists, createErr)
}

func inspectResource(ctx context.Context, command dockerCommand, resource resolvedResourceName) (inspectedResource, error) {
	inspected, err := inspectResourceReference(ctx, command, resource.kind, resource.name, resource.inspectFormat)
	if err != nil {
		return inspectedResource{}, err
	}
	if inspected.Name != resource.name {
		return inspectedResource{}, ErrProjectCollision
	}
	return inspected, nil
}

func inspectResourceReference(ctx context.Context, command dockerCommand, kind, reference, inspectFormat string) (inspectedResource, error) {
	output, err := command.run(ctx, dockerMetadataOutputLimit,
		kind, "inspect", "--format", inspectFormat, reference,
	)
	if err != nil {
		return inspectedResource{}, err
	}
	var inspected inspectedResource
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(output))), &inspected); err != nil {
		return inspectedResource{}, ErrProjectCollision
	}
	if kind == "container" {
		inspected.Name = strings.TrimPrefix(inspected.Name, "/")
	}
	if inspected.Name == "" || inspected.DaemonID == "" {
		return inspectedResource{}, ErrProjectCollision
	}
	return inspected, nil
}

func captureComposeContainers(ctx context.Context, command dockerCommand, identity identity, model composeModel, ledger *createdResourceLedger, requireAll bool) error {
	for _, resource := range resolvedResourceNames(identity, model) {
		if resource.kind != "container" {
			continue
		}
		ids, err := findExactResourceIDs(ctx, command, resource)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			if requireAll {
				return fmt.Errorf("verify started Compose container %q: %w", resource.name, ErrProjectCollision)
			}
			continue
		}
		inspected, err := inspectResource(ctx, command, resource)
		if err != nil {
			return fmt.Errorf("verify started Compose container %q: %w", resource.name, err)
		}
		if !labelsContain(inspected.Labels, identity.labels()) {
			return ErrProjectCollision
		}
		ledger.add(createdResource{kind: resource.kind, name: resource.name, daemonID: inspected.DaemonID})
	}
	return nil
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
