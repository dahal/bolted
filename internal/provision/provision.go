// Package provision implements the Bolted-profile layer for the
// Bolted CLI: a declarative `bolted.yaml` that lists devcontainer
// features, raw Alpine apk packages, gitconfig entries, the login shell,
// and dotfiles to seed into the VM home directory.
//
// The package is split into:
//
//   - provision.go — the BoltedProfile schema plus Load / Save.
//   - cache.go    — the on-disk "currently provisioned" JSON cache, used
//     to diff the desired state against what is actually installed.
//   - apply.go    — the idempotent Apply driver that walks the diff and
//     shells out to the VM (devcontainer features, apk, git, chsh, dotfile
//     copy) via backend.Backend.Exec.
//   - check.go    — a read-only drift detector used by `bolt provision
//     --check`.
//   - fetch.go    — FetchYAML, which pulls a bolted.yaml from either an
//     https URL or a local file path.
//
// All file IO is funnelled through small var indirection points so tests
// can substitute fakes without touching the real filesystem or network.
package provision

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// BoltedProfile mirrors the on-disk schema of ~/.bolted/bolted.yaml.
// Every field is optional; an empty profile is valid (nothing to provision)
// and round-trips through Save / Load unchanged.
//
// Field semantics per .claude/brainstorm/07-toolchain-config.md:
//
//   - Features   — devcontainer feature OCI refs, version-tagged
//     (e.g. "ghcr.io/devcontainers/features/github-cli:1"). Installed
//     via `devcontainer features install <ref>` inside the VM.
//   - Packages   — Alpine apk package names. Installed via `apk add`,
//     removed via `apk del`.
//   - GitConfig  — key/value pairs applied via `git config --global`.
//   - Shell      — login shell binary path (e.g. "zsh", "/bin/bash").
//     Applied via `chsh -s <shell> <user>`.
//   - Dotfiles   — paths (relative to the bolted.yaml file's directory)
//     of files to copy into the VM home directory, preserving mode.
type BoltedProfile struct {
	Features  []string          `yaml:"features,omitempty"`
	Packages  []string          `yaml:"packages,omitempty"`
	GitConfig map[string]string `yaml:"gitconfig,omitempty"`
	Shell     string            `yaml:"shell,omitempty"`
	Dotfiles  []string          `yaml:"dotfiles,omitempty"`
}

// Indirection points so tests can simulate marshal / unmarshal / IO
// failures. Production callers should never reassign these.
var (
	readFileFn    = os.ReadFile
	writeFileFn   = os.WriteFile
	yamlMarshal   = yaml.Marshal
	yamlUnmarshal = yaml.Unmarshal
)

// Load reads YAML from path and returns a *BoltedProfile. A missing
// file is reported as an error — callers that want "missing = empty
// profile" should detect it via errors.Is(err, fs.ErrNotExist) and
// substitute &BoltedProfile{}.
func Load(path string) (*BoltedProfile, error) {
	data, err := readFileFn(path)
	if err != nil {
		return nil, fmt.Errorf("provision: read %s: %w", path, err)
	}
	p := &BoltedProfile{}
	if err := yamlUnmarshal(data, p); err != nil {
		return nil, fmt.Errorf("provision: parse %s: %w", path, err)
	}
	return p, nil
}

// Save writes the profile to path as YAML, mode 0600. The write is not
// atomic — bolted.yaml is user-edited and infrequently written, so we
// keep the implementation simple and match config.Save's convention.
func (p *BoltedProfile) Save(path string) error {
	if p == nil {
		return fmt.Errorf("provision: Save: nil profile")
	}
	data, err := yamlMarshal(p)
	if err != nil {
		return fmt.Errorf("provision: marshal: %w", err)
	}
	if err := writeFileFn(path, data, 0o600); err != nil {
		return fmt.Errorf("provision: write %s: %w", path, err)
	}
	return nil
}
