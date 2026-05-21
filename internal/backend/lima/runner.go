package lima

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
)

// runner is the indirection through which every limactl invocation
// goes. Production code uses realRunner, which wraps os/exec; tests
// substitute a fakeRunner that records calls and returns canned output.
type runner interface {
	// Run executes name with args and returns the captured stdout. On a
	// non-zero exit it returns *exitError so callers can recover
	// stderr and the exit code.
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
	// RunWithStdin is identical to Run but pipes stdin into the
	// process. Separated rather than overloading via a struct so the
	// common no-stdin path stays one line.
	RunWithStdin(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error)
}

// exitError carries a non-zero exit status plus the captured stderr.
// runner.Run returns this (wrapped) when the underlying os/exec
// invocation reports an *exec.ExitError; non-exit errors (binary
// missing, signal, etc.) are returned bare so callers can distinguish.
type exitError struct {
	// ExitCode is the process exit status.
	ExitCode int
	// Stderr is the captured standard error.
	Stderr []byte
	// Cause is the underlying error from os/exec for context.
	Cause error
}

// Error implements error. Appends the captured stderr so diagnostic
// output from limactl reaches the user instead of being stripped down
// to "exit status N".
func (e *exitError) Error() string {
	if trimmed := bytes.TrimSpace(e.Stderr); len(trimmed) > 0 {
		return fmt.Sprintf("%s: %s", e.Cause.Error(), trimmed)
	}
	return e.Cause.Error()
}

// Unwrap exposes the underlying os/exec error so errors.As/Is keep
// working through the wrapper.
func (e *exitError) Unwrap() error { return e.Cause }

// realRunner is the production runner. It uses os/exec directly with
// no buffering surprises: stdout and stderr are captured into separate
// buffers, and exit information is surfaced via *exitError.
type realRunner struct{}

// Run satisfies runner. Captured stdout is returned; stderr is wrapped
// in *exitError on non-zero exit.
func (realRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return runCmd(ctx, nil, name, args...)
}

// RunWithStdin satisfies runner. Identical to Run but the given
// reader is piped into the child's standard input.
func (realRunner) RunWithStdin(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
	return runCmd(ctx, stdin, name, args...)
}

// runCmd is the shared exec implementation. Kept package-level so the
// realRunner type has nothing on it that needs covering — tests use
// fakeRunner and we leave realRunner as a thin indirection.
func runCmd(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return stdout.Bytes(), &exitError{
			ExitCode: ee.ExitCode(),
			Stderr:   stderr.Bytes(),
			Cause:    err,
		}
	}
	return stdout.Bytes(), err
}
