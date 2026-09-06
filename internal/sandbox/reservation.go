package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/azizu06/rehearse/internal/drill"
	"github.com/azizu06/rehearse/internal/sandboxid"
)

type createdResource struct {
	kind       string
	name       string
	daemonID   string
	generation string
}

type createdResourceLedger struct {
	resources map[string]createdResource
	expected  map[string]drill.SandboxResourceClaim
}

func newCreatedResourceLedger() *createdResourceLedger {
	return &createdResourceLedger{
		resources: make(map[string]createdResource),
		expected:  make(map[string]drill.SandboxResourceClaim),
	}
}

func resourceKey(kind, name string) string { return kind + "\x00" + name }

func (ledger *createdResourceLedger) expect(resource drill.SandboxResourceClaim) {
	ledger.expected[resourceKey(resource.Kind, resource.Name)] = resource
}

func (ledger *createdResourceLedger) add(resource createdResource) {
	ledger.resources[resourceKey(resource.kind, resource.name)] = resource
}

func (ledger *createdResourceLedger) get(kind, name string) (createdResource, bool) {
	resource, exists := ledger.resources[resourceKey(kind, name)]
	return resource, exists
}

func (ledger *createdResourceLedger) expectedResource(kind, name string) (drill.SandboxResourceClaim, bool) {
	resource, exists := ledger.expected[resourceKey(kind, name)]
	return resource, exists
}

func (ledger *createdResourceLedger) expectedResources(kind string) []drill.SandboxResourceClaim {
	resources := make([]drill.SandboxResourceClaim, 0, len(ledger.expected))
	for _, resource := range ledger.expected {
		if resource.Kind == kind {
			resources = append(resources, resource)
		}
	}
	sort.Slice(resources, func(left, right int) bool {
		return resources[left].Name < resources[right].Name
	})
	return resources
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

func reserveComposeResources(
	ctx context.Context,
	command dockerCommand,
	journal CleanupJournal,
	identity identity,
	model composeModel,
	ledger *createdResourceLedger,
	newGeneration func() (string, error),
) error {
	resources, err := resolvedResourceNames(identity, model)
	if err != nil {
		return err
	}
	for _, resource := range resources {
		if resource.kind != "network" && resource.kind != "volume" {
			continue
		}
		generation, err := newGeneration()
		if err != nil {
			return err
		}
		expected := drill.SandboxResourceClaim{Kind: resource.kind, Name: resource.name, Generation: generation}
		if err := journal.AppendSandboxCleanupResource(ctx, identity.runID, identity.claimID, expected, time.Now().UTC()); err != nil {
			return fmt.Errorf("record expected Compose %s %q: %w", resource.kind, resource.name, err)
		}
		ledger.expect(expected)
		labels, err := resourceLabels(identity, generation)
		if err != nil {
			return err
		}
		args := []string{resource.kind, "create"}
		if resource.kind == "network" {
			args = append(args, "--driver", "bridge", "--internal")
		} else {
			args = append(args, "--driver", "local")
		}
		for _, label := range sortedLabels(labels) {
			args = append(args, "--label", label)
		}
		args = append(args, resource.name)
		if _, err := command.run(ctx, dockerMetadataOutputLimit, args...); err != nil {
			return classifyReservationFailure(ctx, command, resource, labels, err)
		}
		inspected, err := inspectResource(ctx, command, resource)
		if err != nil {
			return fmt.Errorf("verify reserved Compose %s %q: %w", resource.kind, resource.name, err)
		}
		if !labelsContain(inspected.Labels, labels) {
			return ErrProjectCollision
		}
		ledger.add(createdResource{
			kind: resource.kind, name: resource.name, daemonID: inspected.DaemonID, generation: generation,
		})
	}
	return nil
}

func classifyReservationFailure(ctx context.Context, command dockerCommand, resource resolvedResourceName, labels map[string]string, createErr error) error {
	inspected, inspectErr := inspectResource(ctx, command, resource)
	if inspectErr != nil {
		return fmt.Errorf("reserve Compose %s %q: %w", resource.kind, resource.name, createErr)
	}
	if !labelsContain(inspected.Labels, labels) {
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
	if inspected.Name == "" || (kind != "volume" && inspected.DaemonID == "") {
		return inspectedResource{}, ErrProjectCollision
	}
	return inspected, nil
}

func captureComposeContainers(ctx context.Context, command dockerCommand, identity identity, model composeModel, ledger *createdResourceLedger, requireAll bool) error {
	resources, err := resolvedResourceNames(identity, model)
	if err != nil {
		return err
	}
	for _, resource := range resources {
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
		expected, exists := ledger.expectedResource(resource.kind, resource.name)
		if !exists {
			return ErrProjectCollision
		}
		labels, err := resourceLabels(identity, expected.Generation)
		if err != nil {
			return err
		}
		if !labelsContain(inspected.Labels, labels) {
			return ErrProjectCollision
		}
		ledger.add(createdResource{
			kind: resource.kind, name: resource.name, daemonID: inspected.DaemonID, generation: expected.Generation,
		})
	}
	return nil
}

func sortedLabels(labels map[string]string) []string {
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

func prepareComposeContainerClaims(
	ctx context.Context,
	journal CleanupJournal,
	identity identity,
	data []byte,
	model composeModel,
	ledger *createdResourceLedger,
	newGeneration func() (string, error),
) ([]byte, error) {
	serviceNames := make([]string, 0, len(model.Services))
	for name := range model.Services {
		serviceNames = append(serviceNames, name)
	}
	sort.Strings(serviceNames)
	generations := make(map[string]string, len(serviceNames))
	for _, serviceName := range serviceNames {
		resourceName, err := sandboxid.ResourceName(identity.projectName, "container", serviceName)
		if err != nil {
			return nil, err
		}
		resource := resourceDescriptor("container", resourceName)
		generation, err := newGeneration()
		if err != nil {
			return nil, err
		}
		expected := drill.SandboxResourceClaim{Kind: resource.kind, Name: resource.name, Generation: generation}
		if err := journal.AppendSandboxCleanupResource(ctx, identity.runID, identity.claimID, expected, time.Now().UTC()); err != nil {
			return nil, fmt.Errorf("record expected Compose container %q: %w", resource.name, err)
		}
		ledger.expect(expected)
		generations[serviceName] = generation
	}

	return applyComposeContainerGenerations(data, generations)
}

func applyComposeContainerGenerations(data []byte, generations map[string]string) ([]byte, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("decode reserved Compose snapshot for container claims: %w", err)
	}
	var services map[string]json.RawMessage
	if err := json.Unmarshal(document["services"], &services); err != nil {
		return nil, fmt.Errorf("decode reserved Compose services for container claims: %w", err)
	}
	for name, raw := range services {
		generation, exists := generations[name]
		if !exists {
			return nil, fmt.Errorf("missing reserved Compose service %q generation", name)
		}
		var service map[string]json.RawMessage
		if err := json.Unmarshal(raw, &service); err != nil {
			return nil, fmt.Errorf("decode reserved Compose service %q: %w", name, err)
		}
		var labels map[string]string
		if err := json.Unmarshal(service["labels"], &labels); err != nil {
			return nil, fmt.Errorf("decode reserved Compose service %q labels: %w", name, err)
		}
		labels[resourceGenerationLabel] = generation
		service["labels"], _ = json.Marshal(labels)
		services[name], _ = json.Marshal(service)
	}
	document["services"], _ = json.Marshal(services)
	result, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode reserved Compose container claims: %w", err)
	}
	return result, nil
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
	networkReferences, err := encodeReservedResourceReferences(identity, "network", networks)
	if err != nil {
		return nil, err
	}
	document["networks"] = networkReferences
	volumeReferences, err := encodeReservedResourceReferences(identity, "volume", model.Volumes)
	if err != nil {
		return nil, err
	}
	document["volumes"] = volumeReferences

	rewritten, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode reserved Compose snapshot: %w", err)
	}
	return rewritten, nil
}

func encodeReservedResourceReferences(identity identity, kind string, resources map[string]composeResource) (json.RawMessage, error) {
	references := make(map[string]map[string]any, len(resources))
	for name, resource := range resources {
		resourceName, err := resolvedComposeResourceName(identity, kind, name, resource)
		if err != nil {
			return nil, err
		}
		references[name] = map[string]any{
			"external": true,
			"name":     resourceName,
		}
	}
	data, _ := json.Marshal(references)
	return data, nil
}
