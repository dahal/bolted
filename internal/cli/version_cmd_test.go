package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionCmd_OutputFormat(t *testing.T) {
	cmd := newVersionCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("version returned error: %v", err)
	}
	got := strings.TrimSpace(out.String())
	if !strings.HasPrefix(got, "bolt ") {
		t.Errorf("expected output to start with 'bolt ', got %q", got)
	}
	if !strings.HasSuffix(got, ")") {
		t.Errorf("expected output to end with ')', got %q", got)
	}
}

func TestVersionCmd_Use(t *testing.T) {
	if newVersionCmd().Use != "version" {
		t.Errorf("expected Use=version, got %q", newVersionCmd().Use)
	}
}

func TestVersionCmd_RejectsArgs(t *testing.T) {
	cmd := newVersionCmd()
	if cmd.Args == nil {
		t.Fatal("expected version subcommand to set Args")
	}
	// cobra.NoArgs returns an error when args are passed.
	if err := cmd.Args(cmd, []string{"extra"}); err == nil {
		t.Error("expected error from cobra.NoArgs on extra arg")
	}
}
