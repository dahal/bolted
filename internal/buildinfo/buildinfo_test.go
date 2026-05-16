package buildinfo

import (
	"runtime/debug"
	"strings"
	"testing"
)

// withReader temporarily replaces the readBuildInfo var.
func withReader(t *testing.T, fn func() (*debug.BuildInfo, bool)) {
	t.Helper()
	orig := readBuildInfo
	t.Cleanup(func() { readBuildInfo = orig })
	readBuildInfo = fn
}

// withVars temporarily overrides the package-level Version/Commit vars, then
// restores them on test teardown.
func withVars(t *testing.T, v, c string) {
	t.Helper()
	origV, origC := Version, Commit
	t.Cleanup(func() { Version, Commit = origV, origC })
	Version, Commit = v, c
}

func TestString_WithLdflagsValues(t *testing.T) {
	withVars(t, "v1.2.3", "abcdef1234567890")
	if got, want := String(), "bolt v1.2.3 (abcdef1)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestString_ShortCommitWhenAlreadyShort(t *testing.T) {
	withVars(t, "v0.1.0", "abc")
	if got, want := String(), "bolt v0.1.0 (abc)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestString_FallbackWhenEmpty(t *testing.T) {
	withVars(t, "", "")
	got := String()
	if !strings.HasPrefix(got, "bolt ") {
		t.Errorf("missing prefix, got %q", got)
	}
	if !strings.Contains(got, "(") || !strings.Contains(got, ")") {
		t.Errorf("missing parens, got %q", got)
	}
}

func TestString_FallbackVersionOnlyMissing(t *testing.T) {
	withVars(t, "", "deadbeefcafe")
	got := String()
	if !strings.Contains(got, "(deadbee)") {
		t.Errorf("expected shortened commit deadbee, got %q", got)
	}
}

func TestString_FallbackCommitOnlyMissing(t *testing.T) {
	withVars(t, "v2.0.0", "")
	got := String()
	if !strings.Contains(got, "v2.0.0") {
		t.Errorf("expected version v2.0.0, got %q", got)
	}
}

func TestFromDebug_NoBuildInfo(t *testing.T) {
	withReader(t, func() (*debug.BuildInfo, bool) { return nil, false })
	v, c := fromDebug()
	if v != "" || c != "" {
		t.Errorf("expected empty values when ReadBuildInfo returns false, got v=%q c=%q", v, c)
	}
}

func TestFromDebug_DevelVersionFiltered(t *testing.T) {
	withReader(t, func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true
	})
	v, _ := fromDebug()
	if v != "" {
		t.Errorf("expected (devel) to be filtered to empty, got %q", v)
	}
}

func TestFromDebug_VCSRevisionExtracted(t *testing.T) {
	withReader(t, func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Main: debug.Module{Version: "v1.5.0"},
			Settings: []debug.BuildSetting{
				{Key: "GOOS", Value: "darwin"},
				{Key: "vcs.revision", Value: "deadbeefcafe"},
				{Key: "vcs.modified", Value: "false"},
			},
		}, true
	})
	v, c := fromDebug()
	if v != "v1.5.0" {
		t.Errorf("expected version v1.5.0, got %q", v)
	}
	if c != "deadbeefcafe" {
		t.Errorf("expected commit deadbeefcafe, got %q", c)
	}
}

func TestFromDebug_NoVCSRevision(t *testing.T) {
	withReader(t, func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Main:     debug.Module{Version: "v2.0.0"},
			Settings: []debug.BuildSetting{{Key: "GOOS", Value: "linux"}},
		}, true
	})
	_, c := fromDebug()
	if c != "" {
		t.Errorf("expected empty commit when no vcs.revision, got %q", c)
	}
}

func TestShortCommit(t *testing.T) {
	cases := map[string]string{
		"abcdef1234567890": "abcdef1",
		"abc":              "abc",
		"":                 "",
		"1234567":          "1234567", // exactly 7
		"12345678":         "1234567",
	}
	for in, want := range cases {
		if got := shortCommit(in); got != want {
			t.Errorf("shortCommit(%q) = %q, want %q", in, got, want)
		}
	}
}
