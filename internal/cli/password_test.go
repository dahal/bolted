package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// withTermStubs swaps the package-level isTerminalFn / readPasswordFn for
// one test.
func withTermStubs(t *testing.T, isTerm func(int) bool, read func(int) ([]byte, error)) {
	t.Helper()
	origIsTerm := isTerminalFn
	origRead := readPasswordFn
	t.Cleanup(func() {
		isTerminalFn = origIsTerm
		readPasswordFn = origRead
	})
	isTerminalFn = isTerm
	readPasswordFn = read
}

func TestZero(t *testing.T) {
	b := []byte("secret")
	zero(b)
	for i, x := range b {
		if x != 0 {
			t.Errorf("byte %d = %d, want 0", i, x)
		}
	}
}

func TestReadPasswordFromReader_TrimsLineEndings(t *testing.T) {
	cases := map[string]string{
		"abc\n":     "abc",
		"abc\r\n":   "abc",
		"abc":       "abc",
		"abc\nxyz":  "abc", // only first line
	}
	for in, want := range cases {
		got, err := readPasswordFromReader(strings.NewReader(in))
		if err != nil {
			t.Errorf("input %q: unexpected error: %v", in, err)
			continue
		}
		if string(got) != want {
			t.Errorf("input %q: got %q want %q", in, got, want)
		}
	}
}

func TestReadPasswordFromReader_EmptyIsError(t *testing.T) {
	_, err := readPasswordFromReader(strings.NewReader(""))
	if !errors.Is(err, errEmptyPassword) {
		t.Errorf("expected errEmptyPassword, got %v", err)
	}
}

// flakyReader returns a non-EOF error mid-read.
type flakyReader struct{}

func (flakyReader) Read(p []byte) (int, error) {
	return 0, errors.New("simulated read error")
}

func TestReadPasswordFromReader_ReaderError(t *testing.T) {
	_, err := readPasswordFromReader(flakyReader{})
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, errEmptyPassword) {
		t.Errorf("expected raw reader error, got %v", err)
	}
}

func TestTTYPrompter_Prompt_NotATerminal(t *testing.T) {
	withTermStubs(t, func(int) bool { return false }, nil)
	p := &ttyPrompter{stdin: os.Stdin, stderr: io.Discard}
	_, err := p.Prompt("Password: ")
	if !errors.Is(err, errNotATerminal) {
		t.Errorf("expected errNotATerminal, got %v", err)
	}
}

func TestTTYPrompter_Prompt_OK(t *testing.T) {
	withTermStubs(t, func(int) bool { return true }, func(int) ([]byte, error) {
		return []byte("hunter2"), nil
	})
	var stderr bytes.Buffer
	p := &ttyPrompter{stdin: os.Stdin, stderr: &stderr}
	got, err := p.Prompt("Password: ")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if string(got) != "hunter2" {
		t.Errorf("got %q, want hunter2", got)
	}
	if !strings.Contains(stderr.String(), "Password:") {
		t.Errorf("expected label on stderr, got %q", stderr.String())
	}
}

func TestTTYPrompter_Prompt_ReadError(t *testing.T) {
	wantErr := errors.New("read failed")
	withTermStubs(t, func(int) bool { return true }, func(int) ([]byte, error) {
		return nil, wantErr
	})
	p := &ttyPrompter{stdin: os.Stdin, stderr: io.Discard}
	_, err := p.Prompt("Password: ")
	if !errors.Is(err, wantErr) {
		t.Errorf("expected wrapped read error, got %v", err)
	}
}

// errWriter is an io.Writer that always errors.
type errWriter struct{ err error }

func (w errWriter) Write([]byte) (int, error) { return 0, w.err }

func TestTTYPrompter_Prompt_StderrWriteError(t *testing.T) {
	withTermStubs(t, func(int) bool { return true }, func(int) ([]byte, error) {
		return []byte("ok"), nil
	})
	wantErr := errors.New("broken pipe")
	p := &ttyPrompter{stdin: os.Stdin, stderr: errWriter{err: wantErr}}
	_, err := p.Prompt("Password: ")
	if !errors.Is(err, wantErr) {
		t.Errorf("expected stderr write error, got %v", err)
	}
}

func TestTTYPrompter_PromptTwiceConfirm_OK(t *testing.T) {
	calls := 0
	withTermStubs(t, func(int) bool { return true }, func(int) ([]byte, error) {
		calls++
		return []byte("samepw"), nil
	})
	p := &ttyPrompter{stdin: os.Stdin, stderr: io.Discard}
	got, err := p.PromptTwiceConfirm("Password: ")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if string(got) != "samepw" {
		t.Errorf("got %q", got)
	}
	if calls != 2 {
		t.Errorf("expected 2 prompts, got %d", calls)
	}
}

func TestTTYPrompter_PromptTwiceConfirm_Mismatch(t *testing.T) {
	pw := [][]byte{[]byte("first"), []byte("second")}
	idx := 0
	withTermStubs(t, func(int) bool { return true }, func(int) ([]byte, error) {
		b := pw[idx]
		idx++
		return b, nil
	})
	p := &ttyPrompter{stdin: os.Stdin, stderr: io.Discard}
	_, err := p.PromptTwiceConfirm("Password: ")
	if !errors.Is(err, errPasswordMismatch) {
		t.Errorf("expected errPasswordMismatch, got %v", err)
	}
}

func TestTTYPrompter_PromptTwiceConfirm_EmptyFirst(t *testing.T) {
	withTermStubs(t, func(int) bool { return true }, func(int) ([]byte, error) {
		return []byte{}, nil
	})
	p := &ttyPrompter{stdin: os.Stdin, stderr: io.Discard}
	_, err := p.PromptTwiceConfirm("Password: ")
	if !errors.Is(err, errEmptyPassword) {
		t.Errorf("expected errEmptyPassword, got %v", err)
	}
}

func TestTTYPrompter_PromptTwiceConfirm_FirstError(t *testing.T) {
	wantErr := errors.New("read fail")
	withTermStubs(t, func(int) bool { return true }, func(int) ([]byte, error) {
		return nil, wantErr
	})
	p := &ttyPrompter{stdin: os.Stdin, stderr: io.Discard}
	_, err := p.PromptTwiceConfirm("Password: ")
	if !errors.Is(err, wantErr) {
		t.Errorf("expected wrapped read error, got %v", err)
	}
}

func TestTTYPrompter_PromptTwiceConfirm_SecondError(t *testing.T) {
	wantErr := errors.New("second read fail")
	calls := 0
	withTermStubs(t, func(int) bool { return true }, func(int) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte("ok"), nil
		}
		return nil, wantErr
	})
	p := &ttyPrompter{stdin: os.Stdin, stderr: io.Discard}
	_, err := p.PromptTwiceConfirm("Password: ")
	if !errors.Is(err, wantErr) {
		t.Errorf("expected wrapped second read error, got %v", err)
	}
}

func TestTTYPrompter_FromStdin(t *testing.T) {
	r, w, _ := os.Pipe()
	t.Cleanup(func() { _ = r.Close() })
	go func() {
		defer w.Close()
		w.Write([]byte("piped-pw\n"))
	}()
	p := &ttyPrompter{stdin: r, stderr: io.Discard}
	got, err := p.FromStdin()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if string(got) != "piped-pw" {
		t.Errorf("got %q", got)
	}
}

func TestNewTTYPrompter_DefaultsToOSStreams(t *testing.T) {
	p := newTTYPrompter()
	if p.stdin != os.Stdin {
		t.Error("stdin should default to os.Stdin")
	}
	if p.stderr != os.Stderr {
		t.Error("stderr should default to os.Stderr")
	}
}
