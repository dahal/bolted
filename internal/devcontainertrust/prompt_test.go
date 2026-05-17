package devcontainertrust

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// withTTY forces isTerminalFn to report tty=true for the duration of one
// test. Returned cleanup restores the original.
func withTTY(t *testing.T, tty bool) {
	t.Helper()
	orig := isTerminalFn
	t.Cleanup(func() { isTerminalFn = orig })
	isTerminalFn = func(io.Reader) bool { return tty }
}

func TestConfirm_NonTTYReturnsErrNoTTY(t *testing.T) {
	withTTY(t, false)
	in := strings.NewReader("y\n")
	var out bytes.Buffer
	ok, err := Confirm(in, &out, "summary")
	if !errors.Is(err, ErrNoTTY) {
		t.Errorf("expected ErrNoTTY, got %v", err)
	}
	if ok {
		t.Errorf("expected ok=false on non-TTY")
	}
	if out.Len() != 0 {
		t.Errorf("expected no output on non-TTY, got %q", out.String())
	}
}

func TestConfirm_YesAnswersAccepted(t *testing.T) {
	withTTY(t, true)
	for _, ans := range []string{"y\n", "Y\n", "yes\n", "YES\n", "Yes\n", "  y  \n"} {
		t.Run(strings.TrimSpace(ans), func(t *testing.T) {
			in := strings.NewReader(ans)
			var out bytes.Buffer
			ok, err := Confirm(in, &out, "summary")
			if err != nil {
				t.Fatalf("unexpected: %v", err)
			}
			if !ok {
				t.Errorf("expected ok=true for %q", ans)
			}
			if !strings.Contains(out.String(), "Allow this devcontainer") {
				t.Errorf("expected prompt in output, got %q", out.String())
			}
			if !strings.Contains(out.String(), "summary") {
				t.Errorf("expected summary in output, got %q", out.String())
			}
		})
	}
}

func TestConfirm_NoOrEmptyDefaultsNo(t *testing.T) {
	withTTY(t, true)
	for _, ans := range []string{"\n", "n\n", "N\n", "no\n", "garbage\n", ""} {
		t.Run(strings.TrimSpace(ans), func(t *testing.T) {
			in := strings.NewReader(ans)
			var out bytes.Buffer
			ok, err := Confirm(in, &out, "summary")
			if err != nil {
				t.Fatalf("unexpected: %v", err)
			}
			if ok {
				t.Errorf("expected ok=false for %q", ans)
			}
		})
	}
}

func TestConfirm_EmptySummarySkipsSummaryLine(t *testing.T) {
	withTTY(t, true)
	in := strings.NewReader("n\n")
	var out bytes.Buffer
	_, err := Confirm(in, &out, "")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(out.String(), "Allow this devcontainer") {
		t.Errorf("prompt missing: %q", out.String())
	}
	// The first line should be the question, not a blank summary line.
	if strings.HasPrefix(out.String(), "\n") {
		t.Errorf("expected no leading blank line for empty summary, got %q", out.String())
	}
}

// failingWriter returns err on every Write so we can exercise the two
// output-write error paths in Confirm.
type failingWriter struct {
	allowFirst bool
	wrote      int
	err        error
}

func (w *failingWriter) Write(p []byte) (int, error) {
	w.wrote++
	if w.allowFirst && w.wrote == 1 {
		return len(p), nil
	}
	return 0, w.err
}

func TestConfirm_SummaryWriteError(t *testing.T) {
	withTTY(t, true)
	in := strings.NewReader("y\n")
	w := &failingWriter{err: errors.New("disk full")}
	ok, err := Confirm(in, w, "summary")
	if !errors.Is(err, w.err) {
		t.Errorf("expected wrapped write err, got %v", err)
	}
	if ok {
		t.Errorf("expected ok=false on write error")
	}
}

func TestConfirm_PromptWriteError(t *testing.T) {
	withTTY(t, true)
	in := strings.NewReader("y\n")
	// allowFirst=true lets the summary line succeed, then fails the
	// "Allow this devcontainer…" Fprint.
	w := &failingWriter{allowFirst: true, err: errors.New("pipe closed")}
	ok, err := Confirm(in, w, "summary")
	if !errors.Is(err, w.err) {
		t.Errorf("expected wrapped write err, got %v", err)
	}
	if ok {
		t.Errorf("expected ok=false on write error")
	}
}

// failingReader returns an error on Read (not io.EOF) so we can exercise
// the read-error branch.
type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestConfirm_ReadError(t *testing.T) {
	withTTY(t, true)
	want := errors.New("read fail")
	var out bytes.Buffer
	_, err := Confirm(failingReader{err: want}, &out, "summary")
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped read err, got %v", err)
	}
}

func TestConfirm_EOFOnInputIsTreatedAsNo(t *testing.T) {
	withTTY(t, true)
	// io.EOF should be treated as "user pressed Ctrl-D" → no.
	in := strings.NewReader("") // immediate EOF
	var out bytes.Buffer
	ok, err := Confirm(in, &out, "summary")
	if err != nil {
		t.Fatalf("EOF should not surface as error, got %v", err)
	}
	if ok {
		t.Errorf("EOF should default to no")
	}
}

// ---- isTerminalFn default behaviour --------------------------------------

func TestIsTerminalFn_NonFileReturnsFalse(t *testing.T) {
	if isTerminalFn(strings.NewReader("")) {
		t.Errorf("expected non-*os.File to report tty=false")
	}
}

func TestIsTerminalFn_RegularFileReturnsFalse(t *testing.T) {
	f, err := os.CreateTemp("", "tty-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = f.Close()
		_ = os.Remove(f.Name())
	})
	if isTerminalFn(f) {
		t.Errorf("regular file should not be a TTY")
	}
}

func TestIsTerminalFn_ClosedFileReturnsFalse(t *testing.T) {
	// Closing the file makes Stat error → branch where err != nil.
	f, err := os.CreateTemp("", "tty-*")
	if err != nil {
		t.Fatal(err)
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	if isTerminalFn(f) {
		t.Errorf("closed file should not be reported as TTY")
	}
}
