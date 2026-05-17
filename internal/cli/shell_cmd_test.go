package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/dahal/bolted/internal/backend"
	"github.com/dahal/bolted/internal/backend/mock"
)

// withShellStubs swaps stdinFn and isTerminalFn for one test.
func withShellStubs(t *testing.T, isTerm bool, stdin io.Reader) {
	t.Helper()
	origIsTerm := isTerminalFn
	origStdin := stdinFn
	t.Cleanup(func() {
		isTerminalFn = origIsTerm
		stdinFn = origStdin
	})
	isTerminalFn = func(int) bool { return isTerm }
	stdinFn = func() io.Reader { return stdin }
}

// ---- runShell --------------------------------------------------------------

func TestRunShell_NotInitialised(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	withStatStub(t, statMissing)
	withShellStubs(t, true, strings.NewReader(""))

	var stderr bytes.Buffer
	err := runShell(context.Background(), io.Discard, &stderr)
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != exitLocked {
		t.Errorf("expected exit code %d, got %d", exitLocked, code)
	}
	if !strings.Contains(stderr.String(), "bolt init") {
		t.Errorf("expected init hint, got: %q", stderr.String())
	}
}

func TestRunShell_StatGenericError(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	want := errors.New("io error")
	withStatStub(t, func(string) (os.FileInfo, error) { return nil, want })
	withShellStubs(t, true, strings.NewReader(""))

	err := runShell(context.Background(), io.Discard, io.Discard)
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped stat error, got %v", err)
	}
}

func TestRunShell_LoadConfigFails(t *testing.T) {
	want := errors.New("bad yaml")
	s := &lifecycleStubs{cfgErr: want}
	s.install(t)
	withStatStub(t, statExists)
	withShellStubs(t, true, strings.NewReader(""))

	err := runShell(context.Background(), io.Discard, io.Discard)
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped load error, got %v", err)
	}
}

func TestRunShell_NotATerminal(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	withStatStub(t, statExists)
	withShellStubs(t, false, strings.NewReader(""))

	var stderr bytes.Buffer
	err := runShell(context.Background(), io.Discard, &stderr)
	if err == nil {
		t.Fatal("expected non-tty error")
	}
	if code := exitCodeFromError(err); code != exitGeneric {
		t.Errorf("expected exit code %d, got %d", exitGeneric, code)
	}
	if !strings.Contains(stderr.String(), "interactive terminal") {
		t.Errorf("expected friendly tty message, got: %q", stderr.String())
	}
}

func TestRunShell_BackendInitFails(t *testing.T) {
	want := errors.New("be fail")
	s := &lifecycleStubs{backendErr: want}
	s.install(t)
	withStatStub(t, statExists)
	withShellStubs(t, true, strings.NewReader(""))

	err := runShell(context.Background(), io.Discard, io.Discard)
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped backend error, got %v", err)
	}
}

func TestRunShell_IsRunningFails(t *testing.T) {
	want := errors.New("is running fail")
	s := &lifecycleStubs{mockBE: mock.New()}
	s.mockBE.ErrIsRunning = want
	s.install(t)
	withStatStub(t, statExists)
	withShellStubs(t, true, strings.NewReader(""))

	err := runShell(context.Background(), io.Discard, io.Discard)
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped IsRunning error, got %v", err)
	}
}

func TestRunShell_VMNotRunning(t *testing.T) {
	s := &lifecycleStubs{mockBE: mock.New()}
	s.mockBE.IsRunningResult = false
	s.install(t)
	withStatStub(t, statExists)
	withShellStubs(t, true, strings.NewReader(""))

	var stderr bytes.Buffer
	err := runShell(context.Background(), io.Discard, &stderr)
	if err == nil {
		t.Fatal("expected exit-2 error")
	}
	if code := exitCodeFromError(err); code != exitLocked {
		t.Errorf("expected exit code %d, got %d", exitLocked, code)
	}
	if !strings.Contains(stderr.String(), "bolt unlock") {
		t.Errorf("expected unlock hint, got: %q", stderr.String())
	}
}

func TestRunShell_Locked(t *testing.T) {
	// IsRunning=true but `ls /bolted/repos` returns non-zero ⇒ locked.
	scripted := &scriptedBackend{
		Mock: mock.New(),
		execScript: []backend.ExecResult{
			{ExitCode: 2}, // ls probe
		},
	}
	scripted.Mock.IsRunningResult = true

	s := &lifecycleStubs{}
	s.install(t)
	newBackendFn = func(_ backend.Config) (backend.Backend, error) { return scripted, nil }
	withStatStub(t, statExists)
	withShellStubs(t, true, strings.NewReader(""))

	var stderr bytes.Buffer
	err := runShell(context.Background(), io.Discard, &stderr)
	if err == nil {
		t.Fatal("expected locked error")
	}
	if code := exitCodeFromError(err); code != exitLocked {
		t.Errorf("expected exit code %d, got %d", exitLocked, code)
	}
	if !strings.Contains(stderr.String(), "locked") {
		t.Errorf("expected locked message, got: %q", stderr.String())
	}
}

func TestRunShell_LockedProbeExecError(t *testing.T) {
	// Exec returns an error → treated as locked.
	scripted := &scriptedBackend{
		Mock:     mock.New(),
		execErrs: []error{errors.New("exec wedge")},
	}
	scripted.Mock.IsRunningResult = true

	s := &lifecycleStubs{}
	s.install(t)
	newBackendFn = func(_ backend.Config) (backend.Backend, error) { return scripted, nil }
	withStatStub(t, statExists)
	withShellStubs(t, true, strings.NewReader(""))

	err := runShell(context.Background(), io.Discard, io.Discard)
	if err == nil {
		t.Fatal("expected locked error")
	}
	if code := exitCodeFromError(err); code != exitLocked {
		t.Errorf("expected exit code %d, got %d", exitLocked, code)
	}
}

func TestRunShell_HappyPath(t *testing.T) {
	scripted := &scriptedBackend{
		Mock: mock.New(),
		execScript: []backend.ExecResult{
			{ExitCode: 0}, // ls probe
			{ExitCode: 0, Stdout: []byte("hello from shell\n"), Stderr: []byte("note: warning\n")},
		},
	}
	scripted.Mock.IsRunningResult = true

	s := &lifecycleStubs{}
	s.install(t)
	newBackendFn = func(_ backend.Config) (backend.Backend, error) { return scripted, nil }
	withStatStub(t, statExists)
	withShellStubs(t, true, strings.NewReader("input from user\n"))

	var stdout, stderr bytes.Buffer
	if err := runShell(context.Background(), &stdout, &stderr); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(stdout.String(), "hello from shell") {
		t.Errorf("expected stdout passthrough, got: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "note: warning") {
		t.Errorf("expected stderr passthrough, got: %q", stderr.String())
	}
	// Verify Exec was called with the shell path + TTY=true and the stdin
	// reader was attached.
	var shellCall *mock.Call
	for i := range scripted.Mock.Calls {
		c := &scripted.Mock.Calls[i]
		if c.Method == "Exec" && len(c.Cmd) > 0 && c.Cmd[0] == defaultShellPath {
			shellCall = c
			break
		}
	}
	if shellCall == nil {
		t.Fatalf("expected an Exec call for %q, got methods=%v", defaultShellPath, scripted.Mock.Methods())
	}
	if !shellCall.ExecOpts.TTY {
		t.Error("expected TTY=true on the shell exec")
	}
	if shellCall.ExecOpts.Stdin == nil {
		t.Error("expected stdin reader to be wired to the shell exec")
	}
}

func TestRunShell_ExecError(t *testing.T) {
	scripted := &scriptedBackend{
		Mock: mock.New(),
		execScript: []backend.ExecResult{
			{ExitCode: 0}, // ls probe
			{},            // shell exec — will fail
		},
		execErrs: []error{nil, errors.New("exec exploded")},
	}
	scripted.Mock.IsRunningResult = true

	s := &lifecycleStubs{}
	s.install(t)
	newBackendFn = func(_ backend.Config) (backend.Backend, error) { return scripted, nil }
	withStatStub(t, statExists)
	withShellStubs(t, true, strings.NewReader(""))

	err := runShell(context.Background(), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "exec shell") {
		t.Errorf("expected exec error, got %v", err)
	}
}

func TestRunShell_NonZeroExitPropagates(t *testing.T) {
	scripted := &scriptedBackend{
		Mock: mock.New(),
		execScript: []backend.ExecResult{
			{ExitCode: 0}, // ls probe
			{ExitCode: 42, Stdout: []byte("bye"), Stderr: []byte("err")},
		},
	}
	scripted.Mock.IsRunningResult = true

	s := &lifecycleStubs{}
	s.install(t)
	newBackendFn = func(_ backend.Config) (backend.Backend, error) { return scripted, nil }
	withStatStub(t, statExists)
	withShellStubs(t, true, strings.NewReader(""))

	var stdout, stderr bytes.Buffer
	err := runShell(context.Background(), &stdout, &stderr)
	if err == nil {
		t.Fatal("expected non-zero exit error")
	}
	if code := exitCodeFromError(err); code != 42 {
		t.Errorf("expected exit code 42, got %d", code)
	}
	// Output should still have been flushed before returning.
	if !strings.Contains(stdout.String(), "bye") {
		t.Errorf("expected stdout flushed before exit, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "err") {
		t.Errorf("expected stderr flushed before exit, got %q", stderr.String())
	}
}

// ---- shellFromConfig + isLocked direct coverage ---------------------------

func TestShellFromConfig_Default(t *testing.T) {
	if got := shellFromConfig(nil); got != defaultShellPath {
		t.Errorf("expected %q, got %q", defaultShellPath, got)
	}
}

func TestIsLocked_ExecError(t *testing.T) {
	scripted := &scriptedBackend{
		Mock:     mock.New(),
		execErrs: []error{errors.New("boom")},
	}
	if !isLocked(context.Background(), scripted) {
		t.Error("exec error should be treated as locked")
	}
}

func TestIsLocked_ExitNonZero(t *testing.T) {
	scripted := &scriptedBackend{
		Mock:       mock.New(),
		execScript: []backend.ExecResult{{ExitCode: 5}},
	}
	if !isLocked(context.Background(), scripted) {
		t.Error("non-zero exit should mean locked")
	}
}

func TestIsLocked_ExitZero(t *testing.T) {
	scripted := &scriptedBackend{
		Mock:       mock.New(),
		execScript: []backend.ExecResult{{ExitCode: 0}},
	}
	if isLocked(context.Background(), scripted) {
		t.Error("zero exit should mean unlocked")
	}
}

// ---- Cobra plumbing -------------------------------------------------------

func TestNewShellCmd_Construction(t *testing.T) {
	cmd := newShellCmd()
	if cmd.Use != "shell" {
		t.Errorf("expected Use=shell, got %q", cmd.Use)
	}
	if cmd.Short == "" {
		t.Error("expected non-empty Short")
	}
}

func TestShellCmd_RunE_DispatchesRunShell(t *testing.T) {
	// Smoke test: cmd.Execute → runShell. We expect failure (not a TTY)
	// but the dispatch must happen.
	s := &lifecycleStubs{}
	s.install(t)
	withStatStub(t, statExists)
	withShellStubs(t, false, strings.NewReader(""))

	cmd := newShellCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected non-tty failure to surface")
	}
}

// ---- stdinFn default ------------------------------------------------------

func TestStdinFn_DefaultsToOSStdin(t *testing.T) {
	// stdinFn is package-level, so just verify the default points
	// somewhere non-nil. We can't compare directly because tests
	// running in different harnesses may swap os.Stdin, but the result
	// must be non-nil.
	if stdinFn() == nil {
		t.Error("stdinFn() should return a non-nil reader")
	}
}
