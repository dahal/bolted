package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config mirrors ~/.bolted/config.yaml. The on-disk schema is documented
// in .claude/brainstorm/04-cli-design.md § Config and in spec 03; this struct
// is the single source of truth in code.
//
// Every field has a zero-value-safe default applied by Load (via
// applyDefaults). Callers that construct a Config in-memory should call
// applyDefaults explicitly, or use NewDefault.
type Config struct {
	VM                  VMConfig         `yaml:"vm"`
	Backend             string           `yaml:"backend"`
	Keychain            bool             `yaml:"keychain"`
	Forwarding          ForwardingConfig `yaml:"forwarding"`
	DefaultDevcontainer string           `yaml:"default_devcontainer"`
}

// VMConfig holds the static VM allocation. memory and disk are user-facing
// strings ("8GB", "50GB") parsed via ParseSize; cpus is a positive integer.
type VMConfig struct {
	Memory string `yaml:"memory"`
	CPUs   int    `yaml:"cpus"`
	Disk   string `yaml:"disk"`
}

// ForwardingConfig controls host port-forwarding behaviour. Auto is a pointer
// so we can distinguish "user explicitly set false" from "field omitted, apply
// default (true)".
type ForwardingConfig struct {
	Auto *bool `yaml:"auto,omitempty"`
}

// AutoEnabled reports whether automatic port forwarding is on. Treats a nil
// Auto as the documented default (true). Callers should prefer this method
// over reading Auto directly.
func (f ForwardingConfig) AutoEnabled() bool {
	if f.Auto == nil {
		return defaultForwardingAuto
	}
	return *f.Auto
}

// Backend names recognised by the loader. The "auto" sentinel asks the CLI to
// pick at runtime; concrete backend values are validated by the backend
// package (spec 06).
const (
	BackendAuto = "auto"
)

// Default values, kept as package vars (not consts) so tests and callers can
// reference them and so the defaults travel with the struct via NewDefault.
var (
	defaultMemory              = "8GB"
	defaultCPUs                = 4
	defaultDisk                = "50GB"
	defaultBackend             = BackendAuto
	defaultKeychain            = false
	defaultForwardingAuto      = true
	defaultDefaultDevcontainer = "~/.bolted/default-devcontainer.json"
)

// yamlMarshal is an indirection point so tests can simulate a marshalling
// failure (which the real yaml.Marshal almost never produces for our schema).
var yamlMarshal = yaml.Marshal

// NewDefault returns a Config with every field set to its documented default.
// Equivalent to applying applyDefaults to a zero Config.
func NewDefault() *Config {
	c := &Config{}
	applyDefaults(c)
	return c
}

// Load reads YAML from path and returns a Config with defaults applied for
// any missing fields. If the file does not exist, Load returns a default
// Config and does NOT create the file. Other I/O or parse errors are
// returned to the caller. The returned Config is always validated.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			c := NewDefault()
			if verr := c.Validate(); verr != nil {
				return nil, verr
			}
			return c, nil
		}
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	c := &Config{}
	if err := yaml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	applyDefaults(c)
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("config: validate %s: %w", path, err)
	}
	return c, nil
}

// Save writes the Config to path as YAML, applying defaults first so partial
// in-memory configs round-trip cleanly. Save validates before writing — an
// invalid config never reaches disk. The parent directory is created (mode
// 0o700) if it doesn't yet exist — config.yaml lives in ~/.bolted alongside
// password-derived state, and first-run `bolt init` will hit a fresh machine
// without that dir. Writes are NOT atomic; callers that need atomicity
// should compose with internal/state.WriteJSON-style helpers (config.yaml is
// human-edited and infrequently written, so atomicity is not worth the
// temp-file noise here).
func (c *Config) Save(path string) error {
	applyDefaults(c)
	if err := c.Validate(); err != nil {
		return fmt.Errorf("config: validate: %w", err)
	}
	data, err := yamlMarshal(c)
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config: ensure dir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	return nil
}

// applyDefaults fills any zero-valued field with its documented default.
//
// Boolean handling:
//   - keychain's default is false (the Go zero value), so no action needed.
//   - forwarding.auto's default is true. We use a *bool so we can distinguish
//     "omitted" (nil → apply default) from "explicitly false" (non-nil pointer
//     to false → keep the user's choice).
func applyDefaults(c *Config) {
	if c.VM.Memory == "" {
		c.VM.Memory = defaultMemory
	}
	if c.VM.CPUs == 0 {
		c.VM.CPUs = defaultCPUs
	}
	if c.VM.Disk == "" {
		c.VM.Disk = defaultDisk
	}
	if c.Backend == "" {
		c.Backend = defaultBackend
	}
	// Keychain: zero value (false) is the documented default — no-op.
	_ = defaultKeychain
	if c.Forwarding.Auto == nil {
		v := defaultForwardingAuto
		c.Forwarding.Auto = &v
	}
	if c.DefaultDevcontainer == "" {
		c.DefaultDevcontainer = defaultDefaultDevcontainer
	}
}
