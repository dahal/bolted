package cli

import (
	"bytes"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
)

// captureExecute redirects os.Stdout and the package-level stderr var, runs
// Execute, and returns the captured streams alongside the exit code.
func captureExecute(t *testing.T, name string, args []string) (stdout, errout string, code int) {
	t.Helper()

	oldStdout := os.Stdout
	rStdout, wStdout, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = wStdout
	defer func() { os.Stdout = oldStdout }()

	var errBuf bytes.Buffer
	oldStderr := stderr
	stderr = &errBuf
	defer func() { stderr = oldStderr }()

	var outBuf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&outBuf, rStdout)
	}()

	code = Execute(name, args)
	_ = wStdout.Close()
	wg.Wait()
	_ = rStdout.Close()

	return outBuf.String(), errBuf.String(), code
}

func TestExecute_VersionAsBolt(t *testing.T) {
	out, _, code := captureExecute(t, "bolt", []string{"version"})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d (out=%q)", code, out)
	}
	if !strings.HasPrefix(out, "bolt ") {
		t.Errorf("expected output to start with 'bolt ', got %q", out)
	}
}

func TestExecute_VersionAsWs(t *testing.T) {
	out, _, code := captureExecute(t, "bolt", []string{"version"})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d (out=%q)", code, out)
	}
	if !strings.HasPrefix(out, "bolt ") {
		t.Errorf("expected output to start with 'bolt ', got %q", out)
	}
}

func TestExecute_HelpUsesInvokedName(t *testing.T) {
	for _, name := range []string{"bolt"} {
		out, _, code := captureExecute(t, name, []string{"--help"})
		if code != 0 {
			t.Fatalf("%s: expected exit 0, got %d", name, code)
		}
		want := "Usage:\n  " + name
		if !strings.Contains(out, want) {
			t.Errorf("%s: expected help to contain %q, got:\n%s", name, want, out)
		}
	}
}

func TestExecute_HelpListsReservedSubcommands(t *testing.T) {
	out, _, _ := captureExecute(t, "bolt", []string{"--help"})
	// Sample a handful of reserved subcommand names. The full list is
	// covered by TestRegisterSubcommands_AllReservedPresent.
	for _, want := range []string{"init", "unlock", "dev", "version"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected help to list reserved subcommand %q, got:\n%s", want, out)
		}
	}
}

func TestExecute_PassthroughStubExitsNonZero(t *testing.T) {
	_, errOut, code := captureExecute(t, "bolt", []string{"git", "--version"})
	if code == 0 {
		t.Fatalf("expected non-zero exit, got 0")
	}
	if !strings.Contains(errOut, "spec 11") {
		t.Errorf("expected passthrough message to reference spec 11, got: %q", errOut)
	}
}

func TestExecute_ReservedSubcommandStubExitsNonZero(t *testing.T) {
	_, _, code := captureExecute(t, "bolt", []string{"init"})
	if code == 0 {
		t.Fatalf("expected non-zero exit from stub, got 0")
	}
}

func TestExecute_BadLogLevel(t *testing.T) {
	_, _, code := captureExecute(t, "bolt", []string{"--log-level", "trace", "version"})
	if code == 0 {
		t.Fatal("expected non-zero exit for invalid log level")
	}
}

func TestIsPassthrough(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"empty", nil, false},
		{"empty string only", []string{""}, false},
		{"long flag at front", []string{"--help"}, false},
		{"short flag at front", []string{"-h"}, false},
		{"reserved", []string{"init"}, false},
		{"unknown command", []string{"git"}, true},
		{"unknown with following args", []string{"gh", "auth", "login"}, true},
		// Flag-before-command defers to Cobra (full handling lands in spec 11).
		{"flag-before-reserved defers to Cobra", []string{"--log-level", "debug", "init"}, false},
		{"flag-before-unknown defers to Cobra", []string{"-v", "git"}, false},
		// Spec 11 AC 4: `bolt -- ls /etc` MUST passthrough (not invoke
		// the reserved `ls` subcommand).
		{"double-dash forces passthrough", []string{"--", "ls"}, true},
	}
	for _, c := range cases {
		if got := isPassthrough(c.args); got != c.want {
			t.Errorf("%s: isPassthrough(%v) = %v, want %v", c.name, c.args, got, c.want)
		}
	}
}
