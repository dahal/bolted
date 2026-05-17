package config

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestBoltedDir_RespectsEnvVar(t *testing.T) {
	t.Setenv(boltedHomeEnv, "/tmp/custom-bolted")
	if got := BoltedDir(); got != "/tmp/custom-bolted" {
		t.Errorf("BoltedDir() = %q, want %q", got, "/tmp/custom-bolted")
	}
}

func TestBoltedDir_FallsBackToHome(t *testing.T) {
	t.Setenv(boltedHomeEnv, "")
	// On macOS / Linux test runners HOME is always set.
	prev := userHomeDir
	t.Cleanup(func() { userHomeDir = prev })
	userHomeDir = func() (string, error) { return "/Users/somebody", nil }
	want := filepath.Join("/Users/somebody", ".bolted")
	if got := BoltedDir(); got != want {
		t.Errorf("BoltedDir() = %q, want %q", got, want)
	}
}

func TestBoltedDir_FallbackWhenHomeMissing(t *testing.T) {
	t.Setenv(boltedHomeEnv, "")
	prev := userHomeDir
	t.Cleanup(func() { userHomeDir = prev })
	userHomeDir = func() (string, error) { return "", errors.New("no home") }
	if got := BoltedDir(); got != ".bolted" {
		t.Errorf("BoltedDir() with missing home = %q, want %q", got, ".bolted")
	}
}

func TestBoltedDir_FallbackWhenHomeEmptyNoError(t *testing.T) {
	t.Setenv(boltedHomeEnv, "")
	prev := userHomeDir
	t.Cleanup(func() { userHomeDir = prev })
	userHomeDir = func() (string, error) { return "", nil }
	if got := BoltedDir(); got != ".bolted" {
		t.Errorf("BoltedDir() with empty home = %q, want %q", got, ".bolted")
	}
}
