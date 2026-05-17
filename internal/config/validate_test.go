package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidate_AcceptsDefaults(t *testing.T) {
	c := NewDefault()
	if err := c.Validate(); err != nil {
		t.Errorf("default config failed validation: %v", err)
	}
}

func TestValidate_RejectsBadMemory(t *testing.T) {
	c := NewDefault()
	c.VM.Memory = "not-a-size"
	if err := c.Validate(); err == nil {
		t.Error("expected error for bad memory")
	}
}

func TestValidate_RejectsBadDisk(t *testing.T) {
	c := NewDefault()
	c.VM.Disk = "8"
	if err := c.Validate(); err == nil {
		t.Error("expected error for bad disk")
	}
}

func TestValidate_RejectsZeroCPU(t *testing.T) {
	c := NewDefault()
	c.VM.CPUs = 0
	// applyDefaults isn't called in Validate, but Validate must catch the 0.
	// Force 0 by calling Validate on a Config we constructed with zero CPU.
	c.VM.CPUs = 0
	if err := (&Config{
		VM:                  VMConfig{Memory: "8GB", CPUs: 0, Disk: "50GB"},
		Backend:             "auto",
		DefaultDevcontainer: "/tmp/x.json",
	}).Validate(); err == nil {
		t.Error("expected error for CPUs == 0")
	}
}

func TestValidate_RejectsNegativeCPU(t *testing.T) {
	if err := (&Config{
		VM:                  VMConfig{Memory: "8GB", CPUs: -1, Disk: "50GB"},
		Backend:             "auto",
		DefaultDevcontainer: "/tmp/x.json",
	}).Validate(); err == nil {
		t.Error("expected error for negative CPUs")
	}
}

func TestValidate_RejectsUnknownBackend(t *testing.T) {
	c := NewDefault()
	c.Backend = "kvm"
	if err := c.Validate(); err == nil {
		t.Error("expected error for unknown backend")
	}
}

func TestValidate_AcceptsKnownBackends(t *testing.T) {
	for _, b := range []string{"auto", "lima", "wsl2"} {
		c := NewDefault()
		c.Backend = b
		if err := c.Validate(); err != nil {
			t.Errorf("backend=%q rejected: %v", b, err)
		}
	}
}

func TestValidate_NormalisesDefaultDevcontainer(t *testing.T) {
	prev := homeDir
	t.Cleanup(func() { homeDir = prev })
	homeDir = func() (string, error) { return "/Users/test", nil }
	c := NewDefault() // DefaultDevcontainer = "~/.bolted/default-devcontainer.json"
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	want := "/Users/test/.bolted/default-devcontainer.json"
	if c.DefaultDevcontainer != want {
		t.Errorf("DefaultDevcontainer = %q, want %q", c.DefaultDevcontainer, want)
	}
}

func TestValidate_RejectsBadDevcontainerPath(t *testing.T) {
	c := NewDefault()
	c.DefaultDevcontainer = ""
	if err := c.Validate(); err == nil {
		t.Error("expected error for empty devcontainer path")
	}
}

func TestParseSize_Valid(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"1B", 1},
		{"1KB", 1024},
		{"512MB", 512 * 1024 * 1024},
		{"8GB", 8 * 1024 * 1024 * 1024},
		{"1TB", 1024 * 1024 * 1024 * 1024},
		{"1024KB", 1024 * 1024},
		{"0B", 0},
		// Case-insensitive.
		{"4gb", 4 * 1024 * 1024 * 1024},
		{"4Gb", 4 * 1024 * 1024 * 1024},
	}
	for _, c := range cases {
		got, err := ParseSize(c.in)
		if err != nil {
			t.Errorf("ParseSize(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseSize(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParseSize_Invalid(t *testing.T) {
	cases := []string{
		"",            // empty
		"8",           // missing unit
		"8 GB",        // whitespace
		"8\tGB",       // whitespace
		"8 gigs",      // garbage unit + space
		"GB",          // unit only
		"-1GB",        // negative
		"abcGB",       // non-numeric prefix
		"99999999999999999999GB", // numeric overflow at parseint
		"9223372036854775808GB",  // overflows mult
		"4194305TB",              // overflows int64 after multiplication
	}
	for _, in := range cases {
		if _, err := ParseSize(in); err == nil {
			t.Errorf("ParseSize(%q) expected error, got nil", in)
		}
	}
}

func TestNormalizePath_Tilde(t *testing.T) {
	prev := homeDir
	t.Cleanup(func() { homeDir = prev })
	homeDir = func() (string, error) { return "/Users/test", nil }

	cases := []struct {
		in, want string
	}{
		{"~", "/Users/test"},
		{"~/foo", "/Users/test/foo"},
		{"~/foo/../bar", "/Users/test/bar"},
		{"$HOME", "/Users/test"},
		{"$HOME/foo", "/Users/test/foo"},
		{"/etc/passwd", "/etc/passwd"},
		{"./rel", "rel"},
	}
	for _, c := range cases {
		got, err := NormalizePath(c.in)
		if err != nil {
			t.Errorf("NormalizePath(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("NormalizePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizePath_Empty(t *testing.T) {
	if _, err := NormalizePath(""); err == nil {
		t.Error("expected error for empty path")
	}
}

func TestNormalizePath_OtherUser(t *testing.T) {
	if _, err := NormalizePath("~bob/foo"); err == nil {
		t.Error("expected error for ~user prefix")
	}
}

func TestNormalizePath_HomeFailure(t *testing.T) {
	prev := homeDir
	t.Cleanup(func() { homeDir = prev })
	homeDir = func() (string, error) { return "", errors.New("no home") }

	for _, in := range []string{"~", "~/foo", "$HOME", "$HOME/foo"} {
		if _, err := NormalizePath(in); err == nil {
			t.Errorf("NormalizePath(%q) expected error when home lookup fails", in)
		}
	}
}

func TestNormalizePath_LooksLikeHomeButIsnt(t *testing.T) {
	// "$HOMEdir" must NOT be expanded — only the exact "$HOME" or "$HOME/"
	// prefixes are recognised.
	got, err := NormalizePath("$HOMEdir/foo")
	if err != nil {
		t.Fatalf("NormalizePath: %v", err)
	}
	if !strings.HasPrefix(got, "$HOMEdir/") {
		t.Errorf("NormalizePath(%q) unexpectedly expanded: %q", "$HOMEdir/foo", got)
	}
}

// Confirms Validate's path normalisation actually writes back to the field
// — meaningful because callers depend on it.
func TestValidate_NormalisesInPlace(t *testing.T) {
	prev := homeDir
	t.Cleanup(func() { homeDir = prev })
	homeDir = func() (string, error) { return filepath.Join(os.TempDir(), "home"), nil }
	c := NewDefault()
	c.DefaultDevcontainer = "~/cfg.json"
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if strings.HasPrefix(c.DefaultDevcontainer, "~") {
		t.Errorf("DefaultDevcontainer not expanded: %q", c.DefaultDevcontainer)
	}
}
