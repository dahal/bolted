package lima

import (
	"errors"
	"strings"
	"testing"
)

// TestExitError_IncludesStderr proves exitError.Error() surfaces the
// captured stderr, not just the underlying "exit status N". This is
// the regression for bug 04 — without it, every limactl failure
// looked identical regardless of cause.
func TestExitError_IncludesStderr(t *testing.T) {
	cause := errors.New("exit status 1")
	stderr := []byte(`fatal msg="field mountType must be one of …, got none"` + "\n")
	e := &exitError{ExitCode: 1, Stderr: stderr, Cause: cause}

	got := e.Error()
	if !strings.Contains(got, "exit status 1") {
		t.Errorf("Error() = %q; want it to contain the cause", got)
	}
	if !strings.Contains(got, "mountType") {
		t.Errorf("Error() = %q; want it to contain the stderr", got)
	}
}

// TestExitError_EmptyStderrFallsBack confirms we don't append a
// trailing ": " when there's nothing useful to show.
func TestExitError_EmptyStderrFallsBack(t *testing.T) {
	cause := errors.New("exit status 1")
	e := &exitError{ExitCode: 1, Stderr: []byte("  \n"), Cause: cause}

	if got, want := e.Error(), "exit status 1"; got != want {
		t.Errorf("Error() = %q; want %q", got, want)
	}
}
