package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dahal/bolted/internal/backend"
)

// withRmStdin swaps the rm stdin source for canned input.
func withRmStdin(t *testing.T, body string) {
	t.Helper()
	orig := rmStdinFn
	t.Cleanup(func() { rmStdinFn = orig })
	rmStdinFn = func() io.Reader { return strings.NewReader(body) }
}

// ---- runRm ----------------------------------------------------------------

func TestRunRm_RequireUnlockedFails(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	withStatStub(t, statMissing)
	err := runRm(context.Background(), io.Discard, io.Discard, "api", rmOptions{yes: true})
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != exitLocked {
		t.Errorf("expected exit %d, got %d", exitLocked, code)
	}
}

func TestRunRm_RepoNotFound(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 1}, // test -d
	}, nil)
	err := runRm(context.Background(), io.Discard, io.Discard, "missing", rmOptions{yes: true})
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != exitRepoNotFound {
		t.Errorf("expected exit %d, got %d", exitRepoNotFound, code)
	}
}

func TestRunRm_ReadContainersError(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	if err := writeJunk(ds.stateDir); err != nil {
		t.Fatal(err)
	}
	err := runRm(context.Background(), io.Discard, io.Discard, "api", rmOptions{yes: true})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRunRm_StopsRunningContainer(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0}, // unlocked
		{ExitCode: 0}, // test -d
		{ExitCode: 0}, // rm -rf
	}, nil)
	if err := recordContainer("api", "abc"); err != nil {
		t.Fatal(err)
	}
	if err := runRm(context.Background(), io.Discard, io.Discard, "api", rmOptions{yes: true}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(ds.runner.downCalls) != 1 || ds.runner.downCalls[0] != "abc" {
		t.Errorf("expected Down on abc, got %v", ds.runner.downCalls)
	}
	m, _ := readContainers()
	if _, ok := m["api"]; ok {
		t.Errorf("expected api forgotten, got %v", m)
	}
}

func TestRunRm_DownError(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	_ = recordContainer("api", "abc")
	ds.runner.downErr = errors.New("down boom")
	err := runRm(context.Background(), io.Discard, io.Discard, "api", rmOptions{yes: true})
	if err == nil || !strings.Contains(err.Error(), "stop container") {
		t.Errorf("expected down error, got %v", err)
	}
}

func TestRunRm_ForgetError(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	_ = recordContainer("api", "abc")
	// Read-only state dir → forget write fails.
	if err := chmodReadOnly(ds.stateDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = chmodReadWrite(ds.stateDir) })
	err := runRm(context.Background(), io.Discard, io.Discard, "api", rmOptions{yes: true})
	if err == nil || !strings.Contains(err.Error(), "containers.json") {
		t.Errorf("expected forget error, got %v", err)
	}
}

func TestRunRm_PromptAccept(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},       // unlocked
		{ExitCode: 0},       // test -d
		{ExitCode: 0},       // rm -rf
	}, nil)
	_ = ds
	withRmStdin(t, "y\n")
	var stderr bytes.Buffer
	if err := runRm(context.Background(), io.Discard, &stderr, "api", rmOptions{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(stderr.String(), "remove repo") {
		t.Errorf("expected prompt, got %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "removed") {
		t.Errorf("expected removed confirmation, got %q", stderr.String())
	}
}

func TestRunRm_PromptDecline(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	withRmStdin(t, "n\n")
	var stderr bytes.Buffer
	err := runRm(context.Background(), io.Discard, &stderr, "api", rmOptions{})
	if err == nil {
		t.Fatal("expected abort error")
	}
	if !strings.Contains(stderr.String(), "aborted") {
		t.Errorf("expected aborted message, got %q", stderr.String())
	}
}

func TestRunRm_PromptEmptyDeclines(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	withRmStdin(t, "\n")
	err := runRm(context.Background(), io.Discard, io.Discard, "api", rmOptions{})
	if err == nil {
		t.Fatal("expected abort")
	}
}

// errReader returns a non-EOF error on Read so readLine surfaces it.
type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func TestRunRm_PromptReadError(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	orig := rmStdinFn
	t.Cleanup(func() { rmStdinFn = orig })
	rmStdinFn = func() io.Reader { return errReader{err: errors.New("read boom")} }
	err := runRm(context.Background(), io.Discard, io.Discard, "api", rmOptions{})
	if err == nil || !strings.Contains(err.Error(), "read confirmation") {
		t.Errorf("expected read err, got %v", err)
	}
}

func TestRunRm_RmExecError(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, []error{nil, nil, errors.New("rm boom")})
	err := runRm(context.Background(), io.Discard, io.Discard, "api", rmOptions{yes: true})
	if err == nil || !strings.Contains(err.Error(), "rm repo") {
		t.Errorf("expected rm err, got %v", err)
	}
}

func TestRunRm_RmExecNonZeroExit(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
		{ExitCode: 9, Stderr: []byte("denied")},
	}, nil)
	err := runRm(context.Background(), io.Discard, io.Discard, "api", rmOptions{yes: true})
	if err == nil || !strings.Contains(err.Error(), "rm repo") {
		t.Errorf("expected rm err, got %v", err)
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("expected stderr in err msg, got %v", err)
	}
}

// ---- readLine / isAffirmative ----------------------------------------------

func TestReadLine_EOF(t *testing.T) {
	s, err := readLine(strings.NewReader(""))
	if err != nil {
		t.Errorf("expected nil err on EOF, got %v", err)
	}
	if s != "" {
		t.Errorf("got %q, want empty", s)
	}
}

func TestReadLine_TrimsCRLF(t *testing.T) {
	s, err := readLine(strings.NewReader("hi\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if s != "hi" {
		t.Errorf("got %q, want hi", s)
	}
}

func TestReadLine_ReadError(t *testing.T) {
	_, err := readLine(errReader{err: errors.New("boom")})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestIsAffirmative(t *testing.T) {
	cases := map[string]bool{
		"y":   true,
		"Y":   true,
		"yes": true,
		"YES": true,
		"yes ": true,
		"":    false,
		"n":   false,
		"no":  false,
		"sure": false,
	}
	for in, want := range cases {
		if got := isAffirmative(in); got != want {
			t.Errorf("isAffirmative(%q) = %v, want %v", in, got, want)
		}
	}
}

// ---- Cobra plumbing -------------------------------------------------------

func TestNewRmCmd_FlagsRegistered(t *testing.T) {
	cmd := newRmCmd()
	if cmd.Flags().Lookup("yes") == nil {
		t.Error("expected --yes flag")
	}
}

func TestRmCmd_RunE_RequiresRepoArg(t *testing.T) {
	cmd := newRmCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected arg error")
	}
}

func TestRmCmd_RunE_Dispatch(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	cmd := newRmCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"api", "--yes"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

// Default rmStdinFn points at the package stdinFn helper; verify it
// doesn't return nil in a default test process.
func TestRmStdinFn_Default(t *testing.T) {
	if rmStdinFn() == nil {
		t.Error("rmStdinFn should produce a non-nil reader by default")
	}
}
