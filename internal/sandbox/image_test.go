package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const testScalarImageMetadata = `{"id":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","os":"linux","architecture":"amd64","variant":""}`

type imageInspectResult struct {
	output []byte
	err    error
}

type recordingImageInspector struct {
	results                 []imageInspectResult
	calls                   [][]string
	mutableReferenceChanged bool
}

func (inspector *recordingImageInspector) run(_ context.Context, _ int64, args ...string) ([]byte, error) {
	inspector.calls = append(inspector.calls, append([]string(nil), args...))
	index := len(inspector.calls) - 1
	if index >= len(inspector.results) {
		return nil, errors.New("unexpected image inspection")
	}
	if index == 0 {
		inspector.mutableReferenceChanged = true
	}
	return inspector.results[index].output, inspector.results[index].err
}

func TestInspectLocalImageUsesNarrowVolumeTemplateCompatibilityPair(t *testing.T) {
	tests := []struct {
		name        string
		results     []imageInspectResult
		wantVolumes bool
		wantErr     bool
		wantCalls   int
	}{
		{
			name: "primary direct volumes template",
			results: []imageInspectResult{
				{output: []byte(testScalarImageMetadata)},
				{output: []byte(`null`)},
			},
			wantCalls: 2,
		},
		{
			name: "fallback map compatible volumes template",
			results: []imageInspectResult{
				{output: []byte(testScalarImageMetadata)},
				{output: []byte("primary-template-secret"), err: errors.New("primary template failed with secret")},
				{output: []byte(`{"/data":{}}`)},
			},
			wantVolumes: true,
			wantCalls:   3,
		},
		{
			name: "both template commands fail closed without output",
			results: []imageInspectResult{
				{output: []byte(testScalarImageMetadata)},
				{output: []byte("primary-template-secret"), err: errors.New("primary template failed with secret")},
				{output: []byte("fallback-template-secret"), err: errors.New("fallback template failed with secret")},
			},
			wantErr:   true,
			wantCalls: 3,
		},
		{
			name: "malformed successful output fails without fallback",
			results: []imageInspectResult{
				{output: []byte(testScalarImageMetadata)},
				{output: []byte(`["volume-metadata-secret"]`)},
			},
			wantErr:   true,
			wantCalls: 2,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inspector := &recordingImageInspector{results: test.results}
			metadata, err := inspectLocalImage(context.Background(), inspector, "worker", composeService{Image: "mutable:latest"})
			if test.wantErr {
				if !errors.Is(err, ErrUnsafeCompose) {
					t.Fatalf("inspectLocalImage error = %v, want ErrUnsafeCompose", err)
				}
				if strings.Contains(err.Error(), "secret") {
					t.Fatalf("inspectLocalImage exposed command output or error: %v", err)
				}
			} else if err != nil {
				t.Fatalf("inspectLocalImage: %v", err)
			}
			if got := len(inspector.calls); got != test.wantCalls {
				t.Fatalf("image inspect calls = %d, want %d: %#v", got, test.wantCalls, inspector.calls)
			}
			if !test.wantErr && (len(metadata.Volumes) > 0) != test.wantVolumes {
				t.Fatalf("volumes = %#v, want present %t", metadata.Volumes, test.wantVolumes)
			}

			scalar := strings.Join(inspector.calls[0], " ")
			for _, forbidden := range []string{".Config", "Env", "Cmd", "index"} {
				if strings.Contains(scalar, forbidden) {
					t.Fatalf("scalar inspection rendered forbidden %q: %v", forbidden, inspector.calls[0])
				}
			}
			if test.wantCalls >= 2 {
				assertImageVolumeCall(t, inspector.calls[1], `{{json .Config.Volumes}}`)
			}
			if test.wantCalls == 3 {
				assertImageVolumeCall(t, inspector.calls[2], `{{json (index .Config "Volumes")}}`)
			}
		})
	}
}

func TestInspectLocalImageTargetsResolvedImmutableIDAfterMutableReferenceChanges(t *testing.T) {
	inspector := &recordingImageInspector{results: []imageInspectResult{
		{output: []byte(testScalarImageMetadata)},
		{output: []byte(`null`)},
	}}
	metadata, err := inspectLocalImage(context.Background(), inspector, "worker", composeService{
		Image:    "registry.example/worker:latest",
		Platform: "linux/amd64",
	})
	if err != nil {
		t.Fatalf("inspectLocalImage: %v", err)
	}
	if metadata.ID != testImageID {
		t.Fatalf("resolved ID = %q, want %q", metadata.ID, testImageID)
	}
	if !inspector.mutableReferenceChanged {
		t.Fatal("mutable caller reference did not change after scalar resolution")
	}
	if got := inspector.calls[0][len(inspector.calls[0])-1]; got != "registry.example/worker:latest" {
		t.Fatalf("scalar inspection target = %q, want mutable caller reference", got)
	}
	if got := len(inspector.calls); got != 2 {
		t.Fatalf("image inspect calls = %d, want 2: %#v", got, inspector.calls)
	}
	assertImageVolumeCall(t, inspector.calls[1], `{{json .Config.Volumes}}`)
}

func assertImageVolumeCall(t *testing.T, call []string, wantTemplate string) {
	t.Helper()
	if got, want := len(call), 5; got != want {
		t.Fatalf("volume inspection argv = %v, want image inspect --format <template> <immutable-ID>", call)
	}
	if got := call[len(call)-1]; got != testImageID {
		t.Fatalf("volume inspection target = %q, want immutable ID %q", got, testImageID)
	}
	if got := argumentAfter(call, "--format"); got != wantTemplate {
		t.Fatalf("volume inspection template = %q, want %q", got, wantTemplate)
	}
	joined := strings.Join(call, " ")
	for _, forbidden := range []string{"{{json .Config}}", ".Config.Env", ".Config.Cmd"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("volume inspection rendered forbidden %q: %v", forbidden, call)
		}
	}
}
