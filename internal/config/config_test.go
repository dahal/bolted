package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewDefault_AppliesAllDefaults(t *testing.T) {
	c := NewDefault()
	if c.VM.Memory != defaultMemory {
		t.Errorf("VM.Memory = %q, want %q", c.VM.Memory, defaultMemory)
	}
	if c.VM.CPUs != defaultCPUs {
		t.Errorf("VM.CPUs = %d, want %d", c.VM.CPUs, defaultCPUs)
	}
	if c.VM.Disk != defaultDisk {
		t.Errorf("VM.Disk = %q, want %q", c.VM.Disk, defaultDisk)
	}
	if c.Backend != defaultBackend {
		t.Errorf("Backend = %q, want %q", c.Backend, defaultBackend)
	}
	if c.Keychain {
		t.Errorf("Keychain = true, want false")
	}
	if !c.Forwarding.AutoEnabled() {
		t.Errorf("Forwarding.AutoEnabled() = false, want true")
	}
	if c.DefaultDevcontainer == "" {
		t.Errorf("DefaultDevcontainer is empty")
	}
}

func TestLoad_MissingFileReturnsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.yaml")
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.VM.Memory != defaultMemory {
		t.Errorf("expected defaults, got Memory=%q", c.VM.Memory)
	}
	// Loader must NOT create the file.
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("Load created the file at %s; it should not", path)
	}
}

func TestLoad_PartialConfigFillsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// Only set memory + cpus. Everything else must come from defaults.
	if err := os.WriteFile(path, []byte("vm:\n  memory: 16GB\n  cpus: 8\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.VM.Memory != "16GB" {
		t.Errorf("Memory = %q, want 16GB", c.VM.Memory)
	}
	if c.VM.CPUs != 8 {
		t.Errorf("CPUs = %d, want 8", c.VM.CPUs)
	}
	if c.VM.Disk != defaultDisk {
		t.Errorf("Disk = %q, want default %q", c.VM.Disk, defaultDisk)
	}
	if c.Backend != defaultBackend {
		t.Errorf("Backend = %q, want default %q", c.Backend, defaultBackend)
	}
	if !c.Forwarding.AutoEnabled() {
		t.Errorf("Forwarding.AutoEnabled() = false, want default true")
	}
}

func TestLoad_ExplicitForwardingFalseHonored(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("forwarding:\n  auto: false\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Forwarding.AutoEnabled() {
		t.Errorf("explicit false was overridden by default")
	}
}

func TestLoad_BadYAMLReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("vm: : :"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected parse error, got nil")
	}
}

func TestLoad_ReadErrorReturnsError(t *testing.T) {
	dir := t.TempDir()
	// Path is a directory, not a file → ReadFile returns a non-NotExist error.
	if _, err := Load(dir); err == nil {
		t.Fatal("expected read error for directory path, got nil")
	}
}

func TestLoad_InvalidConfigReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("vm:\n  memory: nope\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestLoad_MissingFileWithUnreadableHome(t *testing.T) {
	// When the file is missing we return defaults; NewDefault populates the
	// DefaultDevcontainer with "~/...", and Validate normalises it. Force the
	// normalisation to fail so we exercise the validation branch of the
	// missing-file path.
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.yaml")
	prev := homeDir
	t.Cleanup(func() { homeDir = prev })
	homeDir = func() (string, error) { return "", os.ErrNotExist }
	if _, err := Load(path); err == nil {
		t.Fatal("expected error when home is unreadable")
	}
}

func TestSave_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	in := NewDefault()
	in.VM.Memory = "12GB"
	in.VM.CPUs = 6
	in.Backend = "lima"
	in.Keychain = true
	if err := in.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.VM.Memory != "12GB" || out.VM.CPUs != 6 || out.Backend != "lima" || !out.Keychain {
		t.Errorf("round-trip mismatch: %+v", out)
	}
}

func TestSave_ValidationFailureNoFileWritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	c := NewDefault()
	c.VM.Memory = "not-a-size"
	if err := c.Save(path); err == nil {
		t.Fatal("expected validation error from Save")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("Save wrote the file despite validation failure")
	}
}

func TestSave_WriteErrorReturned(t *testing.T) {
	// Write into a non-existent directory under tempdir.
	dir := t.TempDir()
	path := filepath.Join(dir, "nope", "config.yaml")
	c := NewDefault()
	if err := c.Save(path); err == nil {
		t.Fatal("expected write error for missing dir")
	}
}

func TestSave_MarshalFailureReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	prev := yamlMarshal
	t.Cleanup(func() { yamlMarshal = prev })
	yamlMarshal = func(any) ([]byte, error) { return nil, errors.New("boom") }
	c := NewDefault()
	if err := c.Save(path); err == nil {
		t.Fatal("expected marshal error")
	}
}

func TestSave_AppliesDefaultsOnSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	c := &Config{} // empty
	if err := c.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), defaultMemory) {
		t.Errorf("Save did not apply defaults; file contents:\n%s", data)
	}
}

func TestForwardingConfig_AutoEnabledExplicit(t *testing.T) {
	tval := true
	fval := false
	cases := []struct {
		name string
		f    ForwardingConfig
		want bool
	}{
		{"nil → default true", ForwardingConfig{Auto: nil}, true},
		{"explicit true", ForwardingConfig{Auto: &tval}, true},
		{"explicit false", ForwardingConfig{Auto: &fval}, false},
	}
	for _, c := range cases {
		if got := c.f.AutoEnabled(); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
