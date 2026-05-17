package provision

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dahal/bolted/internal/state"
)

func TestNewCache_NonNilMaps(t *testing.T) {
	c := NewCache()
	if c.Dotfiles == nil || c.GitConfig == nil {
		t.Fatal("expected non-nil maps")
	}
	c.Dotfiles["x"] = "y"
	c.GitConfig["a"] = "b"
}

func TestCachePath(t *testing.T) {
	got := CachePath("/tmp/state")
	want := filepath.Join("/tmp/state", state.ProvisionedFile)
	if got != want {
		t.Errorf("CachePath = %q, want %q", got, want)
	}
}

func TestLoadCache_Missing_ReturnsEmpty(t *testing.T) {
	c, err := LoadCache(t.TempDir())
	if err != nil {
		t.Fatalf("LoadCache: %v", err)
	}
	if c == nil || c.Dotfiles == nil || c.GitConfig == nil {
		t.Errorf("expected non-nil cache with maps, got %+v", c)
	}
}

func TestLoadCache_ParseError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(CachePath(dir), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadCache(dir)
	if err == nil || !strings.Contains(err.Error(), "load cache") {
		t.Errorf("expected load cache err, got %v", err)
	}
}

func TestSaveCache_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	c := NewCache()
	c.Features = []string{"b:1", "a:1"}
	c.Packages = []string{"jq"}
	c.Dotfiles[".zshrc"] = "deadbeef"
	c.GitConfig["user.email"] = "me@x"
	c.Shell = "zsh"

	if err := SaveCache(dir, c); err != nil {
		t.Fatalf("SaveCache: %v", err)
	}

	// Features should be sorted on save.
	data, err := os.ReadFile(CachePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var on Cache
	if err := json.Unmarshal(data, &on); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(on.Features, []string{"a:1", "b:1"}) {
		t.Errorf("Features not sorted: %v", on.Features)
	}

	loaded, err := LoadCache(dir)
	if err != nil {
		t.Fatalf("LoadCache: %v", err)
	}
	if loaded.Shell != "zsh" || loaded.Dotfiles[".zshrc"] != "deadbeef" {
		t.Errorf("round-trip mismatch: %+v", loaded)
	}
}

func TestSaveCache_NilCache(t *testing.T) {
	err := SaveCache(t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "nil") {
		t.Errorf("expected nil-cache err, got %v", err)
	}
}

func TestSaveCache_WriteError(t *testing.T) {
	// A path whose dir-parent is a file makes MkdirAll fail.
	tmp := t.TempDir()
	blocker := filepath.Join(tmp, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// stateDir under blocker — Join means CachePath becomes <blocker>/provisioned.json,
	// and the WriteJSON mkdirAll on <blocker> fails because it's a file.
	err := SaveCache(blocker, NewCache())
	if err == nil {
		t.Error("expected write error")
	}
}

func TestLoadCache_PopulatesNilMaps(t *testing.T) {
	dir := t.TempDir()
	// Write a cache JSON that omits both maps.
	raw := `{"features":["a"],"packages":[],"shell":"sh"}`
	if err := os.WriteFile(CachePath(dir), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadCache(dir)
	if err != nil {
		t.Fatalf("LoadCache: %v", err)
	}
	if c.Dotfiles == nil || c.GitConfig == nil {
		t.Errorf("expected nil maps to be populated, got %+v", c)
	}
}

func TestDiffStrings(t *testing.T) {
	cases := []struct {
		name                string
		have, want          []string
		wantAdd, wantRemove []string
	}{
		{"both empty", nil, nil, nil, nil},
		{"add only", []string{"a"}, []string{"a", "b"}, []string{"b"}, nil},
		{"remove only", []string{"a", "b"}, []string{"a"}, nil, []string{"b"}},
		{"swap", []string{"a"}, []string{"b"}, []string{"b"}, []string{"a"}},
		{"identical", []string{"a", "b"}, []string{"b", "a"}, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			add, rem := diffStrings(tc.have, tc.want)
			if !sliceEqual(add, tc.wantAdd) {
				t.Errorf("add = %v, want %v", add, tc.wantAdd)
			}
			if !sliceEqual(rem, tc.wantRemove) {
				t.Errorf("remove = %v, want %v", rem, tc.wantRemove)
			}
		})
	}
}

func sliceEqual(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}
