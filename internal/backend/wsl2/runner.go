package wsl2

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
)

// runner is the small abstraction the WSL2 Backend uses to invoke external
// commands (chiefly `wsl.exe` and `netsh.exe`). It exists so unit tests can
// substitute a fakeRunner that records calls and returns canned output
// without ever needing a real Windows host.
//
// The interface is deliberately narrow: Run for fire-and-forget commands,
// RunWithStdin when a command needs piped input (used by Exec when the
// caller supplies opts.Stdin).
type runner interface {
	// Run executes name with the given args and returns its combined
	// stdout. Implementations should return an error that satisfies
	// errors.As(&*exec.ExitError) when the command exits non-zero, so the
	// Backend can extract exit codes for Exec.
	Run(ctx context.Context, name string, args ...string) (stdout []byte, err error)

	// RunWithStdin is identical to Run except that stdin is piped into
	// the command's standard input. A nil stdin is treated as empty.
	RunWithStdin(ctx context.Context, stdin io.Reader, name string, args ...string) (stdout []byte, err error)
}

// realRunner is the production runner. It shells out via os/exec.
// Construction is trivial — the zero value works — but New() wires it up
// explicitly for readability.
type realRunner struct{}

// Run shells out via exec.CommandContext. Stderr is captured into the
// returned error on failure; stdout is always returned (even on failure)
// so callers can inspect partial output.
func (realRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return runCmd(ctx, nil, name, args...)
}

// RunWithStdin is Run with a stdin pipe.
func (realRunner) RunWithStdin(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
	return runCmd(ctx, stdin, name, args...)
}

// runCmd is the shared implementation of Run / RunWithStdin. Kept private
// so the runner interface stays the only surface unit tests can fake.
func runCmd(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if stdin != nil {
		cmd.Stdin = stdin
	}
	err := cmd.Run()
	if err != nil {
		// Wrap with stderr so callers see a useful message; the original
		// *exec.ExitError is preserved via errors.Is/As.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && stderr.Len() > 0 {
			return stdout.Bytes(), &cmdError{
				underlying: err,
				stderr:     stderr.Bytes(),
			}
		}
		return stdout.Bytes(), err
	}
	return stdout.Bytes(), nil
}

// cmdError wraps an *exec.ExitError so callers see the captured stderr in
// the error message while still being able to unwrap to the original
// *exec.ExitError for exit-code extraction.
type cmdError struct {
	underlying error
	stderr     []byte
}

// Error reports the wrapped exit error followed by the captured stderr,
// trimmed of trailing whitespace.
func (e *cmdError) Error() string {
	msg := e.underlying.Error()
	if len(e.stderr) == 0 {
		return msg
	}
	return msg + ": " + string(bytes.TrimSpace(e.stderr))
}

// Unwrap exposes the underlying *exec.ExitError so errors.As keeps
// working.
func (e *cmdError) Unwrap() error { return e.underlying }
