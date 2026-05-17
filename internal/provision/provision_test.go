package provision

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLoad_OK(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bolted.yaml")
	contents := `
features:
  - ghcr.io/devcontainers/features/github-cli:1
packages:
  - jq
  - ripgrep
gitconfig:
  user.email: me@example.com
shell: zsh
dotfiles:
  - .zshrc
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(p.Features) != 1 || p.Features[0] != "ghcr.io/devcontainers/features/github-cli:1" {
		t.Errorf("Features = %v", p.Features)
	}
	if len(p.Packages) != 2 {
		t.Errorf("Packages = %v", p.Packages)
	}
	if p.GitConfig["user.email"] != "me@example.com" {
		t.Errorf("GitConfig = %v", p.GitConfig)
	}
	if p.Shell != "zsh" {
		t.Errorf("Shell = %q", p.Shell)
	}
	if len(p.Dotfiles) != 1 || p.Dotfiles[0] != ".zshrc" {
		t.Errorf("Dotfiles = %v", p.Dotfiles)
	}
}

func TestLoad_Missing(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected fs.ErrNotExist, got %v", err)
	}
}

func TestLoad_BadYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bolted.yaml")
	if err := os.WriteFile(path, []byte("not: [ valid"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "parse") {
		t.Errorf("expected parse error, got %v", err)
	}
}

func TestSave_OK(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.yaml")
	p := &BoltedProfile{
		Features: []string{"foo:1"},
		Shell:    "zsh",
	}
	if err := p.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rt BoltedProfile
	if err := yaml.Unmarshal(data, &rt); err != nil {
		t.Fatalf("round-trip parse: %v", err)
	}
	if rt.Shell != "zsh" || len(rt.Features) != 1 {
		t.Errorf("round-trip mismatch: %+v", rt)
	}
}

func TestSave_NilProfile(t *testing.T) {
	var p *BoltedProfile
	err := p.Save(filepath.Join(t.TempDir(), "x.yaml"))
	if err == nil || !strings.Contains(err.Error(), "nil") {
		t.Errorf("expected nil-profile error, got %v", err)
	}
}

func TestSave_MarshalError(t *testing.T) {
	orig := yamlMarshal
	t.Cleanup(func() { yamlMarshal = orig })
	yamlMarshal = func(any) ([]byte, error) { return nil, errors.New("boom") }
	p := &BoltedProfile{}
	err := p.Save(filepath.Join(t.TempDir(), "x.yaml"))
	if err == nil || !strings.Contains(err.Error(), "marshal") {
		t.Errorf("expected marshal err, got %v", err)
	}
}

func TestSave_WriteError(t *testing.T) {
	// Write into a path that requires a non-existent parent directory.
	p := &BoltedProfile{}
	err := p.Save(filepath.Join(t.TempDir(), "missing", "x.yaml"))
	if err == nil || !strings.Contains(err.Error(), "write") {
		t.Errorf("expected write err, got %v", err)
	}
}
