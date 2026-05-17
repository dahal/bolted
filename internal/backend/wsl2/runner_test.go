package wsl2

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// realRunner is the production runner; we smoke-test it against trivial
// shell commands so the wrapper is exercised end-to-end. The tests skip on
// Windows because they rely on a POSIX `sh` and `cat`.

func skipIfWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("realRunner smoke tests rely on POSIX sh/cat; on Windows the same coverage comes from real WSL invocations.")
	}
}

func TestRealRunner_RunSmoke(t *testing.T) {
	skipIfWindows(t)
	r := realRunner{}
	out, err := r.Run(context.Background(), "sh", "-c", "echo smoke")
	if err != nil {
		t.Fatalf("realRunner.Run: %v", err)
	}
	if !strings.Contains(string(out), "smoke") {
		t.Errorf("unexpected stdout: %q", out)
	}
}

func TestRealRunner_RunWithStdinSmoke(t *testing.T) {
	skipIfWindows(t)
	r := realRunner{}
	out, err := r.RunWithStdin(context.Background(), strings.NewReader("hello"), "cat")
	if err != nil {
		t.Fatalf("realRunner.RunWithStdin: %v", err)
	}
	if string(out) != "hello" {
		t.Errorf("unexpected stdout: %q", out)
	}
}

func TestRealRunner_ExitErrorWrap(t *testing.T) {
	skipIfWindows(t)
	r := realRunner{}
	_, err := r.Run(context.Background(), "sh", "-c", "echo oops 1>&2; exit 5")
	if err == nil {
		t.Fatal("expected error")
	}
	var ce *cmdError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *cmdError, got %T: %v", err, err)
	}
	if !strings.Contains(ce.Error(), "oops") {
		t.Errorf("expected Error() to include stderr, got %q", ce.Error())
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Errorf("expected errors.As to find *exec.ExitError")
	}
	if errors.Unwrap(ce) == nil {
		t.Error("expected non-nil Unwrap")
	}
}

func TestRealRunner_StartFailure(t *testing.T) {
	skipIfWindows(t)
	r := realRunner{}
	_, err := r.Run(context.Background(), "/no/such/binary/anywhere", "x")
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
	// Should NOT be wrapped as *cmdError because there's no ExitError.
	var ce *cmdError
	if errors.As(err, &ce) {
		t.Errorf("unexpected *cmdError wrap for start failure: %v", err)
	}
}

func TestCmdError_ErrorWithoutStderr(t *testing.T) {
	ce := &cmdError{underlying: errors.New("boom")}
	if ce.Error() != "boom" {
		t.Errorf("Error() = %q, want %q", ce.Error(), "boom")
	}
}
