package provision

import (
	"strings"
	"testing"

	"github.com/dahal/bolted/internal/backend/mock"
)

func TestCheck_NilProfile(t *testing.T) {
	_, _, err := Check(mock.New(), nil, NewCache())
	if err == nil || !strings.Contains(err.Error(), "nil profile") {
		t.Errorf("expected nil-profile err, got %v", err)
	}
}

func TestCheck_NilCache(t *testing.T) {
	_, _, err := Check(mock.New(), &BoltedProfile{}, nil)
	if err == nil || !strings.Contains(err.Error(), "nil cache") {
		t.Errorf("expected nil-cache err, got %v", err)
	}
}

func TestCheck_InSync(t *testing.T) {
	cache := NewCache()
	cache.Features = []string{"a:1"}
	cache.Packages = []string{"jq"}
	cache.Shell = "zsh"
	cache.GitConfig = map[string]string{"user.email": "me"}
	cache.Dotfiles[".zshrc"] = "deadbeef"
	profile := &BoltedProfile{
		Features:  []string{"a:1"},
		Packages:  []string{"jq"},
		Shell:     "zsh",
		GitConfig: map[string]string{"user.email": "me"},
		Dotfiles:  []string{".zshrc"},
	}
	drift, summary, err := Check(mock.New(), profile, cache)
	if err != nil {
		t.Fatal(err)
	}
	if drift || summary != "" {
		t.Errorf("expected in sync, got drift=%v summary=%q", drift, summary)
	}
}

func TestCheck_FeaturesDrift(t *testing.T) {
	cache := NewCache()
	cache.Features = []string{"old:1"}
	profile := &BoltedProfile{Features: []string{"new:1"}}
	drift, summary, err := Check(mock.New(), profile, cache)
	if err != nil {
		t.Fatal(err)
	}
	if !drift {
		t.Error("expected drift")
	}
	if !strings.Contains(summary, "features +new:1") || !strings.Contains(summary, "features -old:1") {
		t.Errorf("summary = %q", summary)
	}
}

func TestCheck_PackagesDrift(t *testing.T) {
	cache := NewCache()
	cache.Packages = []string{"old"}
	profile := &BoltedProfile{Packages: []string{"new"}}
	drift, summary, _ := Check(mock.New(), profile, cache)
	if !drift {
		t.Error("expected drift")
	}
	if !strings.Contains(summary, "packages +new") || !strings.Contains(summary, "packages -old") {
		t.Errorf("summary = %q", summary)
	}
}

func TestCheck_ShellDrift(t *testing.T) {
	cache := NewCache()
	cache.Shell = "sh"
	profile := &BoltedProfile{Shell: "zsh"}
	drift, summary, _ := Check(mock.New(), profile, cache)
	if !drift {
		t.Error("expected drift")
	}
	if !strings.Contains(summary, "shell sh→zsh") {
		t.Errorf("summary = %q", summary)
	}
}

func TestCheck_GitConfigDrift(t *testing.T) {
	cache := NewCache()
	cache.GitConfig = map[string]string{"a": "1"}
	profile := &BoltedProfile{GitConfig: map[string]string{"a": "2"}}
	drift, summary, _ := Check(mock.New(), profile, cache)
	if !drift {
		t.Error("expected drift")
	}
	if !strings.Contains(summary, "gitconfig") {
		t.Errorf("summary = %q", summary)
	}
}

func TestCheck_DotfilesDrift(t *testing.T) {
	cache := NewCache()
	profile := &BoltedProfile{Dotfiles: []string{".zshrc", ".vimrc"}}
	drift, summary, _ := Check(mock.New(), profile, cache)
	if !drift {
		t.Error("expected drift")
	}
	if !strings.Contains(summary, "dotfiles .vimrc,.zshrc") {
		t.Errorf("summary = %q", summary)
	}
}

func TestCheck_EmptyShellNotDrift(t *testing.T) {
	cache := NewCache()
	cache.Shell = "sh"
	profile := &BoltedProfile{} // empty shell → don't care
	drift, _, _ := Check(mock.New(), profile, cache)
	if drift {
		t.Error("expected no drift when profile.Shell empty")
	}
}

func TestGitConfigEqual_BothEmpty(t *testing.T) {
	if !gitConfigEqual(nil, map[string]string{}) {
		t.Error("nil and empty should be equal")
	}
}

func TestGitConfigEqual_DifferentValues(t *testing.T) {
	if gitConfigEqual(map[string]string{"a": "1"}, map[string]string{"a": "2"}) {
		t.Error("different values should not be equal")
	}
}
