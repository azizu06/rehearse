package sandbox

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestComposeSafetyRejectsEscapeAndUnboundedFeatures(t *testing.T) {
	identity, _ := newIdentity("run-1")
	tests := []struct {
		name  string
		model string
	}{
		{name: "long service key", model: fmt.Sprintf(`{"services":{"%s":{"image":"alpine"}}}`, strings.Repeat("a", 27))},
		{name: "custom container name", model: `{"services":{"worker":{"image":"alpine","container_name":"shared"}}}`},
		{name: "scale", model: `{"services":{"worker":{"image":"alpine","scale":2}}}`},
		{name: "build", model: `{"services":{"worker":{"build":{"context":"."}}}}`},
		{name: "privileged", model: `{"services":{"worker":{"image":"alpine","privileged":true}}}`},
		{name: "host network", model: `{"services":{"worker":{"image":"alpine","network_mode":"host"}}}`},
		{name: "host PID", model: `{"services":{"worker":{"image":"alpine","pid":"host"}}}`},
		{name: "published port", model: `{"services":{"worker":{"image":"alpine","ports":[{"target":80,"published":"8080"}]}}}`},
		{name: "bind mount", model: `{"services":{"worker":{"image":"alpine","volumes":[{"type":"bind","source":"/","target":"/host"}]}}}`},
		{name: "Docker socket", model: `{"services":{"worker":{"image":"alpine","volumes":[{"type":"volume","source":"sock","target":"/var/run/docker.sock"}]}},"volumes":{"sock":{"name":"rehearse-test_sock"}}}`},
		{name: "external volume", model: `{"services":{"worker":{"image":"alpine"}},"volumes":{"data":{"external":true,"name":"data"}}}`},
		{name: "custom volume name", model: `{"services":{"worker":{"image":"alpine"}},"volumes":{"data":{"name":"shared"}}}`},
		{name: "additional capability", model: `{"services":{"worker":{"image":"alpine","cap_add":["SYS_ADMIN"]}}}`},
		{name: "device", model: `{"services":{"worker":{"image":"alpine","devices":[{"source":"/dev/null","target":"/dev/x"}]}}}`},
		{name: "environment file", model: `{"services":{"worker":{"image":"alpine","env_file":[".env"]}}}`},
		{name: "custom logging", model: `{"services":{"worker":{"image":"alpine","logging":{"driver":"json-file"}}}}`},
		{name: "caller CPU limit", model: `{"services":{"worker":{"image":"alpine","cpus":2}}}`},
		{name: "deploy resources", model: `{"services":{"worker":{"image":"alpine","deploy":{"resources":{"limits":{"memory":"1G"}}}}}}`},
		{name: "provider service", model: `{"services":{"worker":{"provider":{"type":"host-helper"}}}}`},
		{name: "model resource", model: `{"services":{"worker":{"image":"alpine","models":{"llm":null}}},"models":{"llm":{"model":"example"}}}`},
		{name: "label file", model: `{"services":{"worker":{"image":"alpine","label_file":["labels.env"]}}}`},
		{name: "privileged lifecycle hook", model: `{"services":{"worker":{"image":"alpine","post_start":[{"command":"true","privileged":true}]}}}`},
		{name: "device cgroup rule", model: `{"services":{"worker":{"image":"alpine","device_cgroup_rules":["c 1:3 mr"]}}}`},
		{name: "cgroup parent", model: `{"services":{"worker":{"image":"alpine","cgroup_parent":"host.slice"}}}`},
		{name: "tmpfs policy", model: `{"services":{"worker":{"image":"alpine","tmpfs":["/run:size=1g"]}}}`},
		{name: "CPU shares", model: `{"services":{"worker":{"image":"alpine","cpu_shares":2048}}}`},
		{name: "CPU realtime", model: `{"services":{"worker":{"image":"alpine","cpu_rt_runtime":1000}}}`},
		{name: "service model", model: `{"services":{"worker":{"image":"alpine","models":{"llm":null}}}}`},
		{name: "runtime isolation", model: `{"services":{"worker":{"image":"alpine","isolation":"hyperv"}}}`},
		{name: "service MAC", model: `{"services":{"worker":{"image":"alpine","mac_address":"02:42:ac:11:00:02"}}}`},
		{name: "service static IP", model: `{"services":{"worker":{"image":"alpine","networks":{"default":{"ipv4_address":"10.0.0.2"}}}},"networks":{"default":{}}}`},
		{name: "attachable network", model: `{"services":{"worker":{"image":"alpine","networks":{"default":null}}},"networks":{"default":{"attachable":true}}}`},
		{name: "custom IPAM", model: `{"services":{"worker":{"image":"alpine","networks":{"default":null}}},"networks":{"default":{"ipam":{"config":[{"subnet":"10.0.0.0/24"}]}}}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseAndValidateCompose([]byte(test.model), identity); !errors.Is(err, ErrUnsafeCompose) {
				t.Fatalf("parseAndValidateCompose error = %v, want ErrUnsafeCompose", err)
			}
		})
	}
}

func TestComposeSafetyAcceptsProjectScopedSingleReplicaModel(t *testing.T) {
	identity, _ := newIdentity("run-1")
	model := fmt.Sprintf(`{
		"services":{"worker":{"image":"alpine","scale":1,"networks":{"default":null},"volumes":[{"type":"volume","source":"work","target":"/work"}]}},
		"networks":{"default":{"name":%q}},
		"volumes":{"work":{"name":%q}}
	}`, identity.projectName+"_default", identity.projectName+"_work")
	parsed, err := parseAndValidateCompose([]byte(model), identity)
	if err != nil {
		t.Fatalf("parseAndValidateCompose: %v", err)
	}
	override, err := buildOverride(parsed, identity, Limits{CPUs: "0.5", MemoryBytes: 32 << 20, PIDs: 16, OutputBytes: 4096})
	if err != nil {
		t.Fatalf("buildOverride: %v", err)
	}
	for key, value := range identity.labels() {
		if got := override.Services["worker"].Labels[key]; got != value {
			t.Fatalf("service label %s = %q, want %q", key, got, value)
		}
	}
	if !override.Networks["default"].Internal {
		t.Fatal("generated network is not internal")
	}
	if got := override.Services["worker"].Logging; got.Driver != "local" || got.Options["compress"] != "false" || got.Options["max-file"] != "1" || got.Options["max-size"] != "4096" {
		t.Fatalf("logging override = %#v, want bounded local logs", got)
	}
	request := normalizedRequest{projectDirectory: "/tmp/project", identity: identity}
	args := strings.Join(composeArgs(request, ""), " ")
	if !strings.Contains(args, "--env-file "+os.DevNull) {
		t.Fatalf("compose args = %q, want empty environment file", args)
	}
}
