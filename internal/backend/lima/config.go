package lima

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/dahal/bolted/internal/backend"
)

// limaInstance is the subset of `limactl ls --json` we care about.
// Lima emits one JSON object per line (NDJSON); see parseInstances.
type limaInstance struct {
	// Name is the Lima instance name.
	Name string `json:"name"`
	// Status is the lifecycle state ("Running", "Stopped", "Broken"…).
	Status string `json:"status"`
}

// parseInstances decodes Lima's NDJSON `limactl ls --json` output.
// Blank input is tolerated and yields an empty slice; the first
// malformed object returns an error so we don't silently drop data.
func parseInstances(raw []byte) ([]limaInstance, error) {
	out := []limaInstance{}
	dec := json.NewDecoder(bytes.NewReader(raw))
	for dec.More() {
		var inst limaInstance
		if err := dec.Decode(&inst); err != nil {
			return nil, fmt.Errorf("parse limactl ls --json: %w", err)
		}
		out = append(out, inst)
	}
	return out, nil
}

// limaConfig is the minimal lima.yaml shape we render. Lima accepts a
// lot more knobs; we intentionally only set what spec 05 calls out
// (CPU/RAM/disk + Alpine image + port forwards). Anything else uses
// Lima's defaults.
type limaConfig struct {
	// Images is the Alpine image list. Lima walks it in order and
	// picks the first one matching the host arch.
	Images []limaImage `yaml:"images"`
	// CPUs is the number of vCPUs.
	CPUs int `yaml:"cpus"`
	// Memory is the RAM allocation as a Lima size string ("4GiB").
	Memory string `yaml:"memory"`
	// Disk is the maximum sparse disk size as a Lima size string.
	Disk string `yaml:"disk"`
	// Mounts is intentionally empty: Bolted volumes mount inside the
	// VM, not from the host. We do not set `mountType` — Lima rejects
	// any value other than reverse-sshfs/9p/virtiofs/wsl2, and with
	// no mounts the field has nothing to apply to.
	Mounts []any `yaml:"mounts"`
	// Containerd is explicitly disabled because Alpine (our base
	// image) ships without systemd, and Lima's default containerd
	// startup requires systemd. Leaving the field unset makes Lima
	// fatal-exit StartVM with "systemd must be available" even though
	// the VM is actually running. We don't use Lima's containerd —
	// Bolted runs containers via the host-mounted devcontainer flow.
	Containerd limaContainerd `yaml:"containerd"`
	// PortForwards mirrors loadForwards' contents so EnsureVM picks up
	// previously requested forwards on every render.
	PortForwards []limaPortForward `yaml:"portForwards,omitempty"`
}

// limaContainerd toggles Lima's bundled containerd. We pin both
// fields to false; see limaConfig.Containerd for why.
type limaContainerd struct {
	// System enables the system-wide containerd (requires systemd).
	System bool `yaml:"system"`
	// User enables the user-mode containerd (requires systemd).
	User bool `yaml:"user"`
}

// limaImage is one entry in limaConfig.Images.
type limaImage struct {
	// Location is the image URL or local path.
	Location string `yaml:"location"`
	// Arch matches the qemu-style arch naming Lima uses
	// ("aarch64"/"x86_64").
	Arch string `yaml:"arch"`
}

// limaPortForward is the YAML shape Lima expects for a single port
// forward entry under portForwards.
type limaPortForward struct {
	// GuestPort is the port inside the VM.
	GuestPort int `yaml:"guestPort"`
	// HostPort is the port exposed on the host.
	HostPort int `yaml:"hostPort"`
}

// portForward is the JSON shape used in the tracking file. Kept
// separate from limaPortForward so the on-disk format can evolve
// independently of Lima's YAML schema.
type portForward struct {
	// GuestPort is the port inside the VM.
	GuestPort int `json:"guestPort"`
	// HostPort is the port exposed on the host.
	HostPort int `json:"hostPort"`
}

// alpineImageARM64 is the Alpine cloud image baked into lima.yaml for
// Apple Silicon. Spec 07 owns the canonical image choice; this URL is
// the closest pre-built equivalent until that lands.
const alpineImageARM64 = "https://dl-cdn.alpinelinux.org/alpine/v3.20/releases/cloud/nocloud_alpine-3.20.3-aarch64-uefi-cloudinit-r0.qcow2"

// alpineImageAMD64 is the x86_64 counterpart so the same lima.yaml
// works on Intel Macs.
const alpineImageAMD64 = "https://dl-cdn.alpinelinux.org/alpine/v3.20/releases/cloud/nocloud_alpine-3.20.3-x86_64-uefi-cloudinit-r0.qcow2"

// yamlMarshal is the indirection point for yaml.Marshal so tests can
// inject a failure into writeLimaYAML's marshal branch (which is
// otherwise unreachable for a struct with only basic field types).
var yamlMarshal = yaml.Marshal

// jsonMarshalIndent is the same indirection for json.MarshalIndent so
// saveForwards' marshal branch is testable.
var jsonMarshalIndent = json.MarshalIndent

// writeLimaYAML renders a limaConfig from spec + forwards and writes
// it to path. The file is rewritten on every EnsureVM so any
// portForward changes are picked up on next start.
func writeLimaYAML(path string, spec backend.VMSpec, forwards []portForward) error {
	cfg := limaConfig{
		Images: []limaImage{
			{Location: alpineImageARM64, Arch: "aarch64"},
			{Location: alpineImageAMD64, Arch: "x86_64"},
		},
		CPUs:       spec.CPUs,
		Memory:     fmt.Sprintf("%dMiB", spec.MemoryMB),
		Disk:       fmt.Sprintf("%dGiB", spec.DiskGB),
		Mounts:     []any{},
		Containerd: limaContainerd{System: false, User: false},
	}
	for _, f := range forwards {
		cfg.PortForwards = append(cfg.PortForwards, limaPortForward{
			GuestPort: f.GuestPort,
			HostPort:  f.HostPort,
		})
	}
	data, err := yamlMarshal(&cfg)
	if err != nil {
		return fmt.Errorf("marshal lima.yaml: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write lima.yaml: %w", err)
	}
	return nil
}

// forwardsFileName is the tracking file under dataDir that records
// every ForwardPort request. JSON rather than YAML because the file is
// machine-only — no human edits it.
const forwardsFileName = "portForwards.json"

// loadForwards reads dataDir/portForwards.json into a slice. A missing
// file is not an error: the returned slice is empty.
func loadForwards(dataDir string) ([]portForward, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, forwardsFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read forwards: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var out []portForward
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse forwards: %w", err)
	}
	return out, nil
}

// saveForwards persists forwards to dataDir/portForwards.json. The
// list is sorted by HostPort first so the file is diff-friendly across
// runs.
func saveForwards(dataDir string, forwards []portForward) error {
	sort.Slice(forwards, func(i, j int) bool {
		return forwards[i].HostPort < forwards[j].HostPort
	})
	data, err := jsonMarshalIndent(forwards, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal forwards: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, forwardsFileName), data, 0o644); err != nil {
		return fmt.Errorf("write forwards: %w", err)
	}
	return nil
}

// upsertForward replaces an existing entry with the same HostPort
// (rebinding the guest side) or appends a new one. Keeps ForwardPort's
// logic readable.
func upsertForward(forwards []portForward, f portForward) []portForward {
	for i := range forwards {
		if forwards[i].HostPort == f.HostPort {
			forwards[i] = f
			return forwards
		}
	}
	return append(forwards, f)
}

// removeForward returns forwards minus the entry matching hostPort.
// If no entry matches, the slice is returned unchanged (caller uses
// length comparison to detect a no-op).
func removeForward(forwards []portForward, hostPort int) []portForward {
	out := make([]portForward, 0, len(forwards))
	for _, f := range forwards {
		if f.HostPort == hostPort {
			continue
		}
		out = append(out, f)
	}
	return out
}
