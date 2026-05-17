package profiles

import (
	"errors"
	"io/fs"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/dahal/bolted/internal/provision"
)

// expectedNames is the authoritative set the spec asks us to ship.
// Updating descriptions.go without updating this slice (or vice
// versa) should fail TestNames_MatchesExpectedSet — that's the point.
var expectedNames = []string{"data", "fullstack", "minimal", "mobile"}

func TestNames_MatchesExpectedSet(t *testing.T) {
	got := Names()
	if len(got) != len(expectedNames) {
		t.Fatalf("Names() = %v, want %v", got, expectedNames)
	}
	for i, want := range expectedNames {
		if got[i] != want {
			t.Errorf("Names()[%d] = %q, want %q", i, got[i], want)
		}
	}
}

func TestNames_IsSorted(t *testing.T) {
	got := Names()
	if !sort.StringsAreSorted(got) {
		t.Errorf("Names() not sorted: %v", got)
	}
}

func TestDescription_KnownProfilesHaveNonEmptyText(t *testing.T) {
	for _, name := range expectedNames {
		if d := Description(name); d == "" {
			t.Errorf("Description(%q) is empty", name)
		}
	}
}

func TestDescription_UnknownReturnsEmpty(t *testing.T) {
	if d := Description("does-not-exist"); d != "" {
		t.Errorf("Description(unknown) = %q, want empty", d)
	}
}

func TestList_PairsNameAndDescription(t *testing.T) {
	entries := List()
	if len(entries) != len(expectedNames) {
		t.Fatalf("List() len = %d, want %d", len(entries), len(expectedNames))
	}
	for i, e := range entries {
		if e.Name != expectedNames[i] {
			t.Errorf("List()[%d].Name = %q, want %q", i, e.Name, expectedNames[i])
		}
		if e.Description != descriptions[e.Name] {
			t.Errorf("List()[%d].Description = %q, want %q", i, e.Description, descriptions[e.Name])
		}
	}
}

func TestGet_ReturnsBytesForKnownProfiles(t *testing.T) {
	for _, name := range expectedNames {
		data, err := Get(name)
		if err != nil {
			t.Errorf("Get(%q) err = %v", name, err)
			continue
		}
		if len(data) == 0 {
			t.Errorf("Get(%q) returned zero bytes", name)
		}
	}
}

func TestGet_UnknownNameErrors(t *testing.T) {
	_, err := Get("nope")
	if err == nil {
		t.Fatal("expected error on unknown name")
	}
	msg := err.Error()
	if !strings.Contains(msg, "unknown profile") {
		t.Errorf("error missing 'unknown profile': %v", err)
	}
	// The available list should be inlined so the user can recover.
	for _, name := range expectedNames {
		if !strings.Contains(msg, name) {
			t.Errorf("error %q should mention available profile %q", msg, name)
		}
	}
}

// TestGet_ParsesAsBoltedProfile round-trips each embedded yaml
// through the real provision.BoltedProfile schema. Anything we
// ship has to be a valid bolted.yaml the first time.
func TestGet_ParsesAsBoltedProfile(t *testing.T) {
	for _, name := range expectedNames {
		data, err := Get(name)
		if err != nil {
			t.Fatalf("Get(%q): %v", name, err)
		}
		var p provision.BoltedProfile
		if err := yaml.Unmarshal(data, &p); err != nil {
			t.Errorf("profile %q is not valid yaml for provision.BoltedProfile: %v\n---\n%s", name, err, data)
		}
	}
}

// TestGet_ProfileContents_ContainAdvertisedFeatures sanity-checks that
// each non-minimal profile actually includes the marker features the
// brainstorm doc advertises. Keeps profile drift honest.
func TestGet_ProfileContents_ContainAdvertisedFeatures(t *testing.T) {
	want := map[string][]string{
		"fullstack": {"github-cli", "gcloud-cli", "kubectl-helm-minikube", "terraform", "jq", "ripgrep", "fzf"},
		"data":      {"github-cli", "gcloud-cli", "jq", "duckdb", "python3"},
		"mobile":    {"github-cli", "java", "fastlane"},
		"minimal":   {}, // explicitly nothing
	}
	for name, markers := range want {
		data, err := Get(name)
		if err != nil {
			t.Fatalf("Get(%q): %v", name, err)
		}
		body := string(data)
		for _, m := range markers {
			if !strings.Contains(body, m) {
				t.Errorf("profile %q missing expected marker %q\n---\n%s", name, m, body)
			}
		}
	}
}

// failingReadFS is an fs.ReadFileFS that always errors. Used to drive
// the "should be impossible" embed-read failure branch of Get.
type failingReadFS struct{ err error }

func (f failingReadFS) Open(_ string) (fs.File, error)            { return nil, f.err }
func (f failingReadFS) ReadFile(_ string) ([]byte, error)         { return nil, f.err }

func TestGet_EmbedReadFailure(t *testing.T) {
	orig := readEmbeddedFn
	t.Cleanup(func() { readEmbeddedFn = orig })
	readEmbeddedFn = failingReadFS{err: errors.New("io broken")}

	_, err := Get("minimal")
	if err == nil {
		t.Fatal("expected read failure")
	}
	if !strings.Contains(err.Error(), "read embedded") {
		t.Errorf("expected 'read embedded' in err, got %v", err)
	}
}

// TestDescriptionsCoverEveryEmbeddedYAML guards against the dual-bug
// of a yaml that ships but is invisible (no description) or a
// described profile whose yaml file is missing.
func TestDescriptionsCoverEveryEmbeddedYAML(t *testing.T) {
	entries, err := profilesFS.ReadDir("files")
	if err != nil {
		t.Fatalf("read embedded dir: %v", err)
	}
	embedded := map[string]bool{}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".yaml")
		embedded[name] = true
	}
	for name := range descriptions {
		if !embedded[name] {
			t.Errorf("description for %q but no files/%s.yaml embedded", name, name)
		}
	}
	for name := range embedded {
		if _, ok := descriptions[name]; !ok {
			t.Errorf("embedded files/%s.yaml has no description entry", name)
		}
	}
}
