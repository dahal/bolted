package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dahal/bolted/internal/profiles"
)

// withProfilesLister swaps profilesLister for the test and restores
// the original via t.Cleanup.
func withProfilesLister(t *testing.T, fn func() []profiles.Entry) {
	t.Helper()
	orig := profilesLister
	t.Cleanup(func() { profilesLister = orig })
	profilesLister = fn
}

func fixtureEntries() []profiles.Entry {
	return []profiles.Entry{
		{Name: "alpha", Description: "first profile"},
		{Name: "beta", Description: "second profile"},
	}
}

func TestRunProfiles_HumanListing(t *testing.T) {
	withProfilesLister(t, fixtureEntries)

	var out bytes.Buffer
	if err := runProfiles(&out, profilesOptions{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	got := out.String()
	wantLines := []string{
		"alpha — first profile",
		"beta — second profile",
	}
	for _, line := range wantLines {
		if !strings.Contains(got, line) {
			t.Errorf("output missing %q\n---\n%s", line, got)
		}
	}
}

func TestRunProfiles_JSON(t *testing.T) {
	withProfilesLister(t, fixtureEntries)

	var out bytes.Buffer
	if err := runProfiles(&out, profilesOptions{jsonOut: true}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	var decoded []profiles.Entry
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out.String())
	}
	if len(decoded) != 2 || decoded[0].Name != "alpha" || decoded[1].Description != "second profile" {
		t.Errorf("decoded JSON does not match fixture: %+v", decoded)
	}
}

// Note: errWriter is defined in password_test.go; reused here to drive
// the error paths in runProfiles to 100% coverage.

func TestRunProfiles_JSONEncodeError(t *testing.T) {
	withProfilesLister(t, fixtureEntries)

	err := runProfiles(errWriter{err: errors.New("disk full")}, profilesOptions{jsonOut: true})
	if err == nil || !strings.Contains(err.Error(), "encode JSON") {
		t.Errorf("expected encode JSON err, got %v", err)
	}
}

func TestRunProfiles_HumanWriteError(t *testing.T) {
	withProfilesLister(t, fixtureEntries)

	err := runProfiles(errWriter{err: errors.New("pipe closed")}, profilesOptions{})
	if err == nil || !strings.Contains(err.Error(), "write listing") {
		t.Errorf("expected write listing err, got %v", err)
	}
}

func TestNewProfilesCmd_FlagsAndUse(t *testing.T) {
	cmd := newProfilesCmd()
	if cmd.Use != "profiles" {
		t.Errorf("Use = %q, want profiles", cmd.Use)
	}
	if cmd.Flags().Lookup("json") == nil {
		t.Error("expected --json flag")
	}
	if cmd.Args == nil {
		t.Error("expected Args set (NoArgs)")
	}
	if err := cmd.Args(cmd, []string{"extra"}); err == nil {
		t.Error("expected cobra.NoArgs to reject extra args")
	}
}

func TestProfilesCmd_RunE_HappyPath(t *testing.T) {
	// Exercise the real wiring once — no stub — so the default
	// profilesLister (profiles.List) gets covered.
	cmd := newProfilesCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestProfilesCmd_RunE_JSONFlag(t *testing.T) {
	cmd := newProfilesCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(out.String()), "[") {
		t.Errorf("expected JSON array, got %q", out.String())
	}
}
