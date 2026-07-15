package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

const imageMetadataFormat = `{"id":{{json .Id}},"os":{{json .Os}},"architecture":{{json .Architecture}},"variant":{{json .Variant}},"volumes":{{json (index .Config "Volumes")}}}`

var immutableImageIDPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type localImageMetadata struct {
	ID           string                     `json:"id"`
	OS           string                     `json:"os"`
	Architecture string                     `json:"architecture"`
	Variant      string                     `json:"variant"`
	Volumes      map[string]json.RawMessage `json:"volumes"`
}

func pinServiceImages(ctx context.Context, command dockerCommand, snapshot []byte, model composeModel) ([]byte, error) {
	serviceNames := make([]string, 0, len(model.Services))
	for name := range model.Services {
		serviceNames = append(serviceNames, name)
	}
	sort.Strings(serviceNames)
	pinned := make(map[string]string, len(serviceNames))
	for _, name := range serviceNames {
		service := model.Services[name]
		metadata, err := inspectLocalImage(ctx, command, name, service)
		if err != nil {
			return nil, err
		}
		for target := range metadata.Volumes {
			if !serviceMountCoversTarget(service, target) {
				return nil, unsafeService(name, "image declares an anonymous volume target without an attributable mount")
			}
		}
		pinned[name] = metadata.ID
	}
	return rewriteSnapshotImages(snapshot, pinned)
}

func inspectLocalImage(ctx context.Context, command dockerCommand, name string, service composeService) (localImageMetadata, error) {
	if service.Image == "" {
		return localImageMetadata{}, unsafeService(name, "a local image reference is required")
	}
	args := []string{"image", "inspect"}
	if service.Platform != "" {
		args = append(args, "--platform", service.Platform)
	}
	args = append(args, "--format", imageMetadataFormat, service.Image)
	output, err := command.run(ctx, dockerMetadataOutputLimit, args...)
	if err != nil {
		return localImageMetadata{}, fmt.Errorf("inspect service %q selected local image: %w", name, err)
	}
	var metadata localImageMetadata
	if err := decodeStrictJSON(output, &metadata); err != nil {
		return localImageMetadata{}, unsafeService(name, "image metadata is invalid")
	}
	if !immutableImageIDPattern.MatchString(metadata.ID) {
		return localImageMetadata{}, unsafeService(name, "image metadata did not resolve to an immutable local image ID")
	}
	if err := validateImagePlatform(service.Platform, metadata); err != nil {
		return localImageMetadata{}, unsafeService(name, err.Error())
	}
	return metadata, nil
}

func validateImagePlatform(platform string, metadata localImageMetadata) error {
	if platform == "" {
		return nil
	}
	parts := strings.Split(strings.ToLower(platform), "/")
	if len(parts) < 2 || len(parts) > 3 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("selected image platform is invalid")
	}
	if strings.ToLower(metadata.OS) != parts[0] || strings.ToLower(metadata.Architecture) != parts[1] {
		return fmt.Errorf("selected image platform does not match the requested platform")
	}
	if len(parts) == 3 && strings.ToLower(metadata.Variant) != parts[2] {
		return fmt.Errorf("selected image variant does not match the requested platform")
	}
	return nil
}

func rewriteSnapshotImages(snapshot []byte, pinned map[string]string) ([]byte, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(snapshot, &document); err != nil {
		return nil, fmt.Errorf("%w: rewrite snapshot images: %v", ErrUnsafeCompose, err)
	}
	var services map[string]map[string]json.RawMessage
	if err := json.Unmarshal(document["services"], &services); err != nil {
		return nil, fmt.Errorf("%w: rewrite snapshot services: %v", ErrUnsafeCompose, err)
	}
	for name, imageID := range pinned {
		service, ok := services[name]
		if !ok {
			return nil, unsafeService(name, "service disappeared while pinning images")
		}
		encoded, err := json.Marshal(imageID)
		if err != nil {
			return nil, fmt.Errorf("encode immutable image ID: %w", err)
		}
		service["image"] = encoded
	}
	encodedServices, err := json.Marshal(services)
	if err != nil {
		return nil, fmt.Errorf("encode pinned snapshot services: %w", err)
	}
	document["services"] = encodedServices
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode pinned Compose snapshot: %w", err)
	}
	return encoded, nil
}

func serviceMountCoversTarget(service composeService, target string) bool {
	if target == "" {
		return false
	}
	want := path.Clean(target)
	for _, mount := range service.Volumes {
		if mount.Target == "" || path.Clean(mount.Target) != want {
			continue
		}
		if mount.Type == "tmpfs" || (mount.Type == "volume" && mount.Source != "") {
			return true
		}
	}
	return false
}
