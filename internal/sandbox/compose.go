package sandbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/azizu06/rehearse/internal/sandboxid"
)

const maxComposeResources = 64

type composeModel struct {
	Name     string                     `json:"name"`
	Version  string                     `json:"version"`
	Services map[string]composeService  `json:"services"`
	Networks map[string]composeResource `json:"networks"`
	Volumes  map[string]composeResource `json:"volumes"`
	Configs  json.RawMessage            `json:"configs"`
	Models   json.RawMessage            `json:"models"`
	Secrets  json.RawMessage            `json:"secrets"`
}

type composeService struct {
	Build          json.RawMessage                   `json:"build"`
	BlkioConfig    json.RawMessage                   `json:"blkio_config"`
	CapAdd         []string                          `json:"cap_add"`
	Cgroup         string                            `json:"cgroup"`
	CgroupParent   string                            `json:"cgroup_parent"`
	Command        json.RawMessage                   `json:"command"`
	CPUCount       json.RawMessage                   `json:"cpu_count"`
	CPUPercent     json.RawMessage                   `json:"cpu_percent"`
	CPUPeriod      json.RawMessage                   `json:"cpu_period"`
	CPUQuota       json.RawMessage                   `json:"cpu_quota"`
	CPURTRuntime   json.RawMessage                   `json:"cpu_rt_runtime"`
	CPURTPeriod    json.RawMessage                   `json:"cpu_rt_period"`
	CPUShares      json.RawMessage                   `json:"cpu_shares"`
	CPUSet         json.RawMessage                   `json:"cpuset"`
	CPUs           json.RawMessage                   `json:"cpus"`
	CredentialSpec json.RawMessage                   `json:"credential_spec"`
	Configs        []json.RawMessage                 `json:"configs"`
	ContainerName  string                            `json:"container_name"`
	Deploy         *composeDeploy                    `json:"deploy"`
	DependsOn      map[string]composeDependency      `json:"depends_on"`
	Devices        []json.RawMessage                 `json:"devices"`
	DeviceCgroups  []string                          `json:"device_cgroup_rules"`
	DNS            json.RawMessage                   `json:"dns"`
	DNSOptions     json.RawMessage                   `json:"dns_opt"`
	DNSSearch      json.RawMessage                   `json:"dns_search"`
	DomainName     string                            `json:"domainname"`
	Entrypoint     json.RawMessage                   `json:"entrypoint"`
	Environment    json.RawMessage                   `json:"environment"`
	EnvFile        json.RawMessage                   `json:"env_file"`
	ExtraHosts     []json.RawMessage                 `json:"extra_hosts"`
	ExternalLinks  []string                          `json:"external_links"`
	GPUs           json.RawMessage                   `json:"gpus"`
	GroupAdd       []string                          `json:"group_add"`
	Healthcheck    *composeHealthcheck               `json:"healthcheck"`
	Hostname       string                            `json:"hostname"`
	Image          string                            `json:"image"`
	Init           *bool                             `json:"init"`
	Ipc            string                            `json:"ipc"`
	Isolation      string                            `json:"isolation"`
	Links          []string                          `json:"links"`
	Labels         map[string]string                 `json:"labels"`
	Logging        *composeLogging                   `json:"logging"`
	LabelFile      json.RawMessage                   `json:"label_file"`
	MemLimit       json.RawMessage                   `json:"mem_limit"`
	MemReservation json.RawMessage                   `json:"mem_reservation"`
	MemSwapLimit   json.RawMessage                   `json:"memswap_limit"`
	MemSwappiness  json.RawMessage                   `json:"mem_swappiness"`
	Models         json.RawMessage                   `json:"models"`
	MacAddress     string                            `json:"mac_address"`
	NetworkMode    string                            `json:"network_mode"`
	Networks       map[string]*composeServiceNetwork `json:"networks"`
	OOMKillDisable *bool                             `json:"oom_kill_disable"`
	OOMScoreAdj    *int                              `json:"oom_score_adj"`
	Pid            string                            `json:"pid"`
	PidsLimit      json.RawMessage                   `json:"pids_limit"`
	Ports          []json.RawMessage                 `json:"ports"`
	PostStart      json.RawMessage                   `json:"post_start"`
	PreStop        json.RawMessage                   `json:"pre_stop"`
	Platform       string                            `json:"platform"`
	Privileged     bool                              `json:"privileged"`
	Profiles       []string                          `json:"profiles"`
	Provider       json.RawMessage                   `json:"provider"`
	PullPolicy     string                            `json:"pull_policy"`
	ReadOnly       bool                              `json:"read_only"`
	Restart        string                            `json:"restart"`
	Runtime        string                            `json:"runtime"`
	Scale          *int                              `json:"scale"`
	Secrets        []json.RawMessage                 `json:"secrets"`
	SecurityOpt    []string                          `json:"security_opt"`
	ShmSize        json.RawMessage                   `json:"shm_size"`
	StorageOpt     map[string]string                 `json:"storage_opt"`
	StdinOpen      bool                              `json:"stdin_open"`
	StopGrace      json.RawMessage                   `json:"stop_grace_period"`
	StopSignal     string                            `json:"stop_signal"`
	Sysctls        map[string]string                 `json:"sysctls"`
	Ulimits        json.RawMessage                   `json:"ulimits"`
	Tmpfs          json.RawMessage                   `json:"tmpfs"`
	Tty            bool                              `json:"tty"`
	UseAPISocket   bool                              `json:"use_api_socket"`
	User           string                            `json:"user"`
	UsernsMode     string                            `json:"userns_mode"`
	Uts            string                            `json:"uts"`
	Volumes        []composeMount                    `json:"volumes"`
	VolumesFrom    []string                          `json:"volumes_from"`
	WorkingDir     string                            `json:"working_dir"`
}

type composeDependency struct {
	Condition string `json:"condition"`
	Required  bool   `json:"required"`
	Restart   bool   `json:"restart"`
}

type composeHealthcheck struct {
	Disable       bool            `json:"disable"`
	Interval      json.RawMessage `json:"interval"`
	Retries       int             `json:"retries"`
	StartInterval json.RawMessage `json:"start_interval"`
	StartPeriod   json.RawMessage `json:"start_period"`
	Test          json.RawMessage `json:"test"`
	Timeout       json.RawMessage `json:"timeout"`
}

type composeDeploy struct {
	EndpointMode   string          `json:"endpoint_mode"`
	Labels         json.RawMessage `json:"labels"`
	Mode           string          `json:"mode"`
	Placement      json.RawMessage `json:"placement"`
	Replicas       *int            `json:"replicas"`
	Resources      json.RawMessage `json:"resources"`
	RollbackConfig json.RawMessage `json:"rollback_config"`
	UpdateConfig   json.RawMessage `json:"update_config"`
}

type composeServiceNetwork struct {
	Aliases       []string          `json:"aliases"`
	DriverOpts    map[string]string `json:"driver_opts"`
	InterfaceName string            `json:"interface_name"`
	IPv4Address   string            `json:"ipv4_address"`
	IPv6Address   string            `json:"ipv6_address"`
	LinkLocalIPs  []string          `json:"link_local_ips"`
	MacAddress    string            `json:"mac_address"`
}

type composeMount struct {
	Bind        json.RawMessage `json:"bind"`
	Cluster     json.RawMessage `json:"cluster"`
	Consistency string          `json:"consistency"`
	Image       json.RawMessage `json:"image"`
	ReadOnly    bool            `json:"read_only"`
	Source      string          `json:"source"`
	Target      string          `json:"target"`
	Tmpfs       json.RawMessage `json:"tmpfs"`
	Type        string          `json:"type"`
	Volume      json.RawMessage `json:"volume"`
}

type composeResource struct {
	Attachable bool              `json:"attachable"`
	Driver     string            `json:"driver"`
	DriverOpts map[string]string `json:"driver_opts"`
	EnableIPv4 *bool             `json:"enable_ipv4"`
	EnableIPv6 *bool             `json:"enable_ipv6"`
	External   bool              `json:"external"`
	IPAM       json.RawMessage   `json:"ipam"`
	Internal   bool              `json:"internal"`
	Labels     map[string]string `json:"labels"`
	Name       string            `json:"name"`
}

type composeLogging struct {
	Driver  string            `json:"driver"`
	Options map[string]string `json:"options"`
}

type composeOverride struct {
	Services map[string]serviceOverride  `json:"services"`
	Networks map[string]resourceOverride `json:"networks,omitempty"`
	Volumes  map[string]resourceOverride `json:"volumes,omitempty"`
}

type serviceOverride struct {
	CPUs       string            `json:"cpus"`
	Deploy     deployOverride    `json:"deploy"`
	Labels     map[string]string `json:"labels"`
	Logging    loggingOverride   `json:"logging"`
	MemLimit   string            `json:"mem_limit"`
	PidsLimit  int64             `json:"pids_limit"`
	PullPolicy string            `json:"pull_policy"`
	Restart    string            `json:"restart"`
}

type deployOverride struct {
	Resources deployResourcesOverride `json:"resources"`
}

type deployResourcesOverride struct {
	Limits deployLimitsOverride `json:"limits"`
}

type deployLimitsOverride struct {
	CPUs   string `json:"cpus"`
	Memory string `json:"memory"`
	PIDs   int64  `json:"pids"`
}

type loggingOverride struct {
	Driver  string            `json:"driver"`
	Options map[string]string `json:"options"`
}

type resourceOverride struct {
	Internal bool              `json:"internal,omitempty"`
	Labels   map[string]string `json:"labels"`
}

func parseAndValidateCompose(data []byte, identity identity) (composeModel, error) {
	return parseAndValidateComposeMode(data, identity, nil, false)
}

func parseAndValidateSnapshot(data []byte, identity identity, limits Limits) (composeModel, error) {
	return parseAndValidateComposeMode(data, identity, &limits, false)
}

func parseAndValidateReservedSnapshot(data []byte, identity identity, limits Limits) (composeModel, error) {
	return parseAndValidateComposeMode(data, identity, &limits, true)
}

func parseAndValidateComposeMode(data []byte, identity identity, enforced *Limits, reserved bool) (composeModel, error) {
	var model composeModel
	if err := decodeStrictJSON(data, &model); err != nil {
		return composeModel{}, fmt.Errorf("%w: parse resolved Compose model: %v", ErrUnsafeCompose, err)
	}
	if len(model.Services) == 0 || len(model.Services) > maxComposeResources {
		return composeModel{}, fmt.Errorf("%w: Compose must define one to %d services", ErrUnsafeCompose, maxComposeResources)
	}
	if len(model.Networks) > maxComposeResources || len(model.Volumes) > maxComposeResources {
		return composeModel{}, fmt.Errorf("%w: too many networks or volumes", ErrUnsafeCompose)
	}
	if rawSet(model.Configs) || rawSet(model.Models) || rawSet(model.Secrets) {
		return composeModel{}, fmt.Errorf("%w: top-level configs, models, and secrets are not supported", ErrUnsafeCompose)
	}
	if enforced != nil && model.Name != "" && model.Name != identity.projectName {
		return composeModel{}, fmt.Errorf("%w: rendered project identity changed", ErrUnsafeCompose)
	}

	for name, service := range model.Services {
		if err := validateComposeKey("service", name); err != nil {
			return composeModel{}, err
		}
		switch {
		case len(service.Build) > 0 && string(service.Build) != "null":
			return composeModel{}, unsafeService(name, "build steps are not allowed")
		case service.ContainerName != "":
			return composeModel{}, unsafeService(name, "custom container names are not allowed")
		case service.Scale != nil && *service.Scale != 1:
			return composeModel{}, unsafeService(name, "service scale must be one")
		case len(service.Profiles) > 0:
			return composeModel{}, unsafeService(name, "service profiles are not supported")
		case service.Deploy != nil && service.Deploy.Replicas != nil && *service.Deploy.Replicas != 1:
			return composeModel{}, unsafeService(name, "deploy replicas must be one")
		case service.Deploy != nil && service.Deploy.Mode != "" && service.Deploy.Mode != "replicated":
			return composeModel{}, unsafeService(name, "non-replicated deploy modes are not allowed")
		case service.Deploy != nil && (rawSet(service.Deploy.Labels) || rawSet(service.Deploy.Placement) || rawSet(service.Deploy.RollbackConfig) || rawSet(service.Deploy.UpdateConfig) || service.Deploy.EndpointMode != ""):
			return composeModel{}, unsafeService(name, "deploy placement and rollout policy is not allowed")
		case enforced == nil && service.Deploy != nil && rawPresent(service.Deploy.Resources):
			return composeModel{}, unsafeService(name, "caller-supplied deploy resources are not allowed")
		case service.Privileged:
			return composeModel{}, unsafeService(name, "privileged containers are not allowed")
		case len(service.CapAdd) > 0:
			return composeModel{}, unsafeService(name, "additional Linux capabilities are not allowed")
		case len(service.Devices) > 0:
			return composeModel{}, unsafeService(name, "host devices are not allowed")
		case rawSet(service.BlkioConfig) || len(service.DeviceCgroups) > 0 || service.CgroupParent != "":
			return composeModel{}, unsafeService(name, "host block-device and cgroup policy is not allowed")
		case len(service.Ports) > 0:
			return composeModel{}, unsafeService(name, "host-published ports are not allowed")
		case len(service.Configs) > 0 || len(service.Secrets) > 0:
			return composeModel{}, unsafeService(name, "host config and secret mounts are not supported")
		case rawSet(service.EnvFile):
			return composeModel{}, unsafeService(name, "env_file host reads are not allowed")
		case rawSet(service.LabelFile):
			return composeModel{}, unsafeService(name, "label_file host reads are not allowed")
		case len(service.ExtraHosts) > 0 || len(service.Links) > 0 || len(service.ExternalLinks) > 0:
			return composeModel{}, unsafeService(name, "host mappings and legacy links are not allowed")
		case len(service.VolumesFrom) > 0:
			return composeModel{}, unsafeService(name, "volumes_from is not allowed")
		case service.NetworkMode != "" && service.NetworkMode != "none":
			return composeModel{}, unsafeService(name, "shared or host network modes are not allowed")
		case service.Pid != "" || service.Ipc != "" || service.Uts != "":
			return composeModel{}, unsafeService(name, "explicit PID, IPC, or UTS namespaces are not allowed")
		case service.Cgroup == "host" || service.UsernsMode == "host":
			return composeModel{}, unsafeService(name, "host cgroup or user namespace is not allowed")
		case service.UseAPISocket:
			return composeModel{}, unsafeService(name, "Docker API socket access is not allowed")
		case rawSet(service.Provider):
			return composeModel{}, unsafeService(name, "host-executed provider services are not allowed")
		case rawSet(service.Models):
			return composeModel{}, unsafeService(name, "model runner resources are not allowed")
		case rawSet(service.PostStart) || rawSet(service.PreStop):
			return composeModel{}, unsafeService(name, "lifecycle hooks are not allowed")
		case service.Runtime != "" || rawSet(service.GPUs) || rawSet(service.CredentialSpec):
			return composeModel{}, unsafeService(name, "custom runtimes, GPUs, and credential specs are not allowed")
		case len(service.GroupAdd) > 0:
			return composeModel{}, unsafeService(name, "supplemental host groups are not allowed")
		case len(service.SecurityOpt) > 0 || len(service.Sysctls) > 0:
			return composeModel{}, unsafeService(name, "security profile and sysctl overrides are not allowed")
		case enforced == nil && service.Logging != nil:
			return composeModel{}, unsafeService(name, "caller-supplied logging drivers are not allowed")
		case rawSet(service.DNS) || rawSet(service.DNSOptions) || rawSet(service.DNSSearch) || service.DomainName != "":
			return composeModel{}, unsafeService(name, "custom DNS configuration is not allowed")
		case service.OOMKillDisable != nil || service.OOMScoreAdj != nil || rawSet(service.ShmSize) || len(service.StorageOpt) > 0 || rawSet(service.Tmpfs) || rawSet(service.Ulimits):
			return composeModel{}, unsafeService(name, "host pressure and storage overrides are not allowed")
		case service.Isolation != "" || service.MacAddress != "":
			return composeModel{}, unsafeService(name, "custom runtime isolation and MAC policy is not allowed")
		case enforced == nil && (service.PullPolicy != "" || service.Restart != ""):
			return composeModel{}, unsafeService(name, "pull and restart policy is owned by the Rehearse runner")
		case enforced == nil && service.hasCallerResourceLimits():
			return composeModel{}, unsafeService(name, "resource limits are owned by the Rehearse runner")
		case enforced != nil && service.hasNonGeneratedResourceLimits():
			return composeModel{}, unsafeService(name, "non-generated resource limits are not allowed in the execution snapshot")
		case service.Hostname != "" && !dnsLabelPattern.MatchString(service.Hostname):
			return composeModel{}, unsafeService(name, "hostname must be one RFC 1123 label of at most 63 characters")
		}
		if enforced != nil {
			if err := validateEnforcedService(name, service, identity, *enforced); err != nil {
				return composeModel{}, err
			}
		}
		for _, network := range service.Networks {
			if network == nil {
				continue
			}
			for _, alias := range network.Aliases {
				if !dnsLabelPattern.MatchString(alias) {
					return composeModel{}, unsafeService(name, "network aliases must be RFC 1123 labels of at most 63 characters")
				}
			}
			if len(network.DriverOpts) > 0 || network.InterfaceName != "" || network.IPv4Address != "" || network.IPv6Address != "" || len(network.LinkLocalIPs) > 0 || network.MacAddress != "" {
				return composeModel{}, unsafeService(name, "custom service network policy is not allowed")
			}
		}
		for _, mount := range service.Volumes {
			if rawSet(mount.Bind) || rawSet(mount.Cluster) || rawSet(mount.Image) || rawSet(mount.Tmpfs) || rawSet(mount.Volume) || mount.Consistency != "" {
				return composeModel{}, unsafeService(name, "caller-supplied mount policy is not allowed")
			}
			switch mount.Type {
			case "volume":
				if mount.Source == "" {
					return composeModel{}, unsafeService(name, "anonymous volumes are not attributable to a run")
				}
			case "tmpfs":
			default:
				return composeModel{}, unsafeService(name, "only named volumes and tmpfs mounts are allowed")
			}
			if strings.Contains(strings.ToLower(mount.Source), "docker.sock") || strings.Contains(strings.ToLower(mount.Target), "docker.sock") {
				return composeModel{}, unsafeService(name, "Docker socket mounts are not allowed")
			}
		}
	}

	if err := validateResources("network", model.Networks, identity.projectName, true, reserved); err != nil {
		return composeModel{}, err
	}
	if err := validateResources("volume", model.Volumes, identity.projectName, false, reserved); err != nil {
		return composeModel{}, err
	}
	if enforced != nil && !reserved {
		if err := validateEnforcedResources(model, identity); err != nil {
			return composeModel{}, err
		}
	}
	return model, nil
}

func validateResources(kind string, resources map[string]composeResource, projectName string, network bool, reserved bool) error {
	for name, resource := range resources {
		if err := validateComposeKey(kind, name); err != nil {
			return err
		}
		expectedName, err := sandboxid.ResourceName(projectName, kind, name)
		if err != nil {
			return fmt.Errorf("%w: invalid generated %s name", ErrUnsafeCompose, kind)
		}
		if reserved {
			if !resource.External || resource.Name != expectedName || resource.Attachable || resource.Driver != "" || len(resource.DriverOpts) != 0 || resource.EnableIPv4 != nil || resource.EnableIPv6 != nil || rawSet(resource.IPAM) || resource.Internal || len(resource.Labels) != 0 {
				return fmt.Errorf("%w: reserved %s %q must be an exact external name-only reference", ErrUnsafeCompose, kind, name)
			}
			continue
		}
		if resource.External {
			return fmt.Errorf("%w: external %s %q is not allowed", ErrUnsafeCompose, kind, name)
		}
		if resource.Name != "" && resource.Name != expectedName {
			return fmt.Errorf("%w: custom %s name %q is not allowed", ErrUnsafeCompose, kind, resource.Name)
		}
		if len(resource.DriverOpts) > 0 {
			return fmt.Errorf("%w: %s %q driver options are not allowed", ErrUnsafeCompose, kind, name)
		}
		if network {
			if resource.Attachable || resource.EnableIPv4 != nil || resource.EnableIPv6 != nil || rawSet(resource.IPAM) {
				return fmt.Errorf("%w: network %q custom attachment or IPAM policy is not allowed", ErrUnsafeCompose, name)
			}
			if resource.Driver != "" && resource.Driver != "bridge" {
				return fmt.Errorf("%w: network %q must use the bridge driver", ErrUnsafeCompose, name)
			}
		} else if resource.Driver != "" && resource.Driver != "local" {
			return fmt.Errorf("%w: volume %q must use the local driver", ErrUnsafeCompose, name)
		}
	}
	return nil
}

func validateEnforcedService(name string, service composeService, identity identity, limits Limits) error {
	if !labelsContain(service.Labels, identity.labels()) {
		return unsafeService(name, "ownership labels do not match the durable run")
	}
	if !rawFloatEqual(service.CPUs, limits.CPUs) || !rawIntEqual(service.MemLimit, limits.MemoryBytes) || !rawIntEqual(service.PidsLimit, limits.PIDs) {
		return unsafeService(name, "enforced CPU, memory, or PID limit changed after rendering")
	}
	if service.Deploy == nil || !deployLimitsEqual(service.Deploy.Resources, limits) {
		return unsafeService(name, "enforced deploy limits changed after rendering")
	}
	wantLogging := map[string]string{
		"compress": "false",
		"max-file": "1",
		"max-size": strconv.FormatInt(limits.OutputBytes, 10),
	}
	if service.Logging == nil || service.Logging.Driver != "local" || !stringMapEqual(service.Logging.Options, wantLogging) {
		return unsafeService(name, "enforced output policy changed after rendering")
	}
	if service.PullPolicy != "never" || service.Restart != "no" {
		return unsafeService(name, "enforced pull or restart policy changed after rendering")
	}
	return nil
}

func validateEnforcedResources(model composeModel, identity identity) error {
	wantLabels := identity.labels()
	for name, network := range model.Networks {
		if !network.Internal || !labelsContain(network.Labels, wantLabels) {
			return fmt.Errorf("%w: network %q lost internal or ownership policy", ErrUnsafeCompose, name)
		}
	}
	for name, volume := range model.Volumes {
		if !labelsContain(volume.Labels, wantLabels) {
			return fmt.Errorf("%w: volume %q lost ownership policy", ErrUnsafeCompose, name)
		}
	}
	return nil
}

func deployLimitsEqual(data json.RawMessage, limits Limits) bool {
	var resources map[string]json.RawMessage
	if json.Unmarshal(data, &resources) != nil || len(resources) != 1 {
		return false
	}
	var values map[string]json.RawMessage
	if json.Unmarshal(resources["limits"], &values) != nil || len(values) != 3 {
		return false
	}
	return rawFloatEqual(values["cpus"], limits.CPUs) &&
		rawIntEqual(values["memory"], limits.MemoryBytes) &&
		rawIntEqual(values["pids"], limits.PIDs)
}

func rawFloatEqual(data json.RawMessage, expected string) bool {
	actual, err := strconv.ParseFloat(strings.Trim(string(data), `"`), 64)
	if err != nil {
		return false
	}
	want, err := strconv.ParseFloat(expected, 64)
	return err == nil && actual == want
}

func rawIntEqual(data json.RawMessage, expected int64) bool {
	actual, err := strconv.ParseInt(strings.Trim(string(data), `"`), 10, 64)
	return err == nil && actual == expected
}

func labelsContain(actual, expected map[string]string) bool {
	for key, value := range expected {
		if actual[key] != value {
			return false
		}
	}
	return true
}

func stringMapEqual(actual, expected map[string]string) bool {
	if len(actual) != len(expected) {
		return false
	}
	return labelsContain(actual, expected)
}

func validateComposeKey(kind, name string) error {
	if !sandboxid.ValidResourceKey(name) {
		return fmt.Errorf("%w: %s key %q must be lowercase alphanumeric/dash and at most %d characters", ErrUnsafeCompose, kind, name, maxComposeKeyLength)
	}
	return nil
}

func unsafeService(name, reason string) error {
	return fmt.Errorf("%w: service %q: %s", ErrUnsafeCompose, name, reason)
}

func buildOverride(model composeModel, identity identity, limits Limits) (composeOverride, error) {
	resourceLabels := identity.labels()
	memory := strconv.FormatInt(limits.MemoryBytes, 10)
	override := composeOverride{
		Services: make(map[string]serviceOverride, len(model.Services)),
		Networks: make(map[string]resourceOverride, len(model.Networks)),
		Volumes:  make(map[string]resourceOverride, len(model.Volumes)),
	}
	for name := range model.Services {
		override.Services[name] = serviceOverride{
			CPUs: limits.CPUs,
			Deploy: deployOverride{Resources: deployResourcesOverride{Limits: deployLimitsOverride{
				CPUs: limits.CPUs, Memory: memory, PIDs: limits.PIDs,
			}}},
			Labels: resourceLabels,
			Logging: loggingOverride{Driver: "local", Options: map[string]string{
				"compress": "false", "max-file": "1", "max-size": strconv.FormatInt(limits.OutputBytes, 10),
			}},
			MemLimit: memory, PidsLimit: limits.PIDs,
			PullPolicy: "never", Restart: "no",
		}
	}
	for name := range model.Networks {
		override.Networks[name] = resourceOverride{Internal: true, Labels: resourceLabels}
	}
	for name := range model.Volumes {
		override.Volumes[name] = resourceOverride{Labels: resourceLabels}
	}
	return override, nil
}

func (service composeService) hasCallerResourceLimits() bool {
	return rawPresent(service.CPUCount) || rawPresent(service.CPUPercent) || rawPresent(service.CPUPeriod) ||
		rawPresent(service.CPUQuota) || rawPresent(service.CPURTRuntime) || rawPresent(service.CPURTPeriod) || rawPresent(service.CPUShares) || rawPresent(service.CPUSet) || rawPresent(service.CPUs) ||
		rawPresent(service.MemLimit) || rawPresent(service.MemReservation) || rawPresent(service.MemSwapLimit) ||
		rawPresent(service.MemSwappiness) || rawPresent(service.PidsLimit)
}

func (service composeService) hasNonGeneratedResourceLimits() bool {
	return rawPresent(service.CPUCount) || rawPresent(service.CPUPercent) || rawPresent(service.CPUPeriod) ||
		rawPresent(service.CPUQuota) || rawPresent(service.CPURTRuntime) || rawPresent(service.CPURTPeriod) || rawPresent(service.CPUShares) || rawPresent(service.CPUSet) ||
		rawPresent(service.MemReservation) || rawPresent(service.MemSwapLimit) || rawPresent(service.MemSwappiness)
}

func rawPresent(value json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(value))
	return trimmed != "" && trimmed != "null"
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func rawSet(value json.RawMessage) bool {
	switch strings.TrimSpace(string(value)) {
	case "", "null", "{}", "[]", "0", "false":
		return false
	default:
		return true
	}
}

func encodeOverride(override composeOverride) ([]byte, error) {
	data, err := json.MarshalIndent(override, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode Compose safety override: %w", err)
	}
	return data, nil
}

func composeArgs(request normalizedRequest, extraFile string) []string {
	args := []string{"compose", "--ansi", "never", "--progress", "plain", "--env-file", os.DevNull, "--project-name", request.identity.projectName, "--project-directory", request.projectDirectory}
	for _, file := range request.composeFiles {
		args = append(args, "--file", file)
	}
	if extraFile != "" {
		args = append(args, "--file", extraFile)
	}
	return args
}

func snapshotComposeArgs(request normalizedRequest, snapshotPath string) []string {
	return []string{
		"compose", "--ansi", "never", "--progress", "plain", "--env-file", os.DevNull,
		"--project-name", request.identity.projectName,
		"--project-directory", request.projectDirectory,
		"--file", snapshotPath,
	}
}
