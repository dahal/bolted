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
	"github.com/dahal/bolted/internal/volume"
)

// ---- runRestart -----------------------------------------------------------

func TestRunRestart_NotInitialised(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	withStatStub(t, statMissing)
	var stderr bytes.Buffer
	err := runRestart(context.Background(), io.Discard, &stderr)
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != exitLocked {
		t.Errorf("expected exit %d, got %d", exitLocked, code)
	}
	if !strings.Contains(stderr.String(), "bolt init") {
		t.Errorf("expected init hint, got %q", stderr.String())
	}
}

func TestRunRestart_StatGenericError(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	withStatStub(t, func(string) (os.FileInfo, error) { return nil, errors.New("io boom") })
	err := runRestart(context.Background(), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "stat config") {
		t.Errorf("expected stat err, got %v", err)
	}
}

func TestRunRestart_LoadConfigFails(t *testing.T) {
	want := errors.New("bad yaml")
	s := &lifecycleStubs{cfgErr: want}
	s.install(t)
	withStatStub(t, statExists)
	err := runRestart(context.Background(), io.Discard, io.Discard)
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped load err, got %v", err)
	}
}

func TestRunRestart_BackendInitFails(t *testing.T) {
	want := errors.New("backend boom")
	s := &lifecycleStubs{backendErr: want}
	s.install(t)
	withStatStub(t, statExists)
	err := runRestart(context.Background(), io.Discard, io.Discard)
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped backend err, got %v", err)
	}
}

func TestRunRestart_IsRunningFails(t *testing.T) {
	want := errors.New("is-running boom")
	s := &lifecycleStubs{mockBE: mock.New()}
	s.mockBE.ErrIsRunning = want
	s.install(t)
	withStatStub(t, statExists)
	err := runRestart(context.Background(), io.Discard, io.Discard)
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped IsRunning err, got %v", err)
	}
}

func TestRunRestart_VMNotRunningStartsIt(t *testing.T) {
	s := &lifecycleStubs{mockBE: mock.New()}
	s.mockBE.IsRunningResult = false
	s.install(t)
	withStatStub(t, statExists)
	var stderr bytes.Buffer
	if err := runRestart(context.Background(), io.Discard, &stderr); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	methods := s.mockBE.Methods()
	// Should have IsRunning then StartVM, no StopVM.
	var sawStart, sawStop bool
	for _, m := range methods {
		if m == "StartVM" {
			sawStart = true
		}
		if m == "StopVM" {
			sawStop = true
		}
	}
	if !sawStart {
		t.Errorf("expected StartVM, methods=%v", methods)
	}
	if sawStop {
		t.Errorf("did not expect StopVM when VM not running, methods=%v", methods)
	}
	if !strings.Contains(stderr.String(), "started") {
		t.Errorf("expected start confirmation, got %q", stderr.String())
	}
}

func TestRunRestart_VMNotRunningStartVMFails(t *testing.T) {
	want := errors.New("start boom")
	s := &lifecycleStubs{mockBE: mock.New()}
	s.mockBE.IsRunningResult = false
	s.mockBE.ErrStartVM = want
	s.install(t)
	withStatStub(t, statExists)
	err := runRestart(context.Background(), io.Discard, io.Discard)
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped start err, got %v", err)
	}
}

// installRestartSetup wires a scripted Exec backend so we can drive
// the isLocked probe (locked vs unlocked) precisely, then plug it
// into the lifecycleStubs that runRestart depends on.
func installRestartSetup(t *testing.T, execScript []backend.ExecResult, execErrs []error) *scriptedBackend {
	t.Helper()
	scripted := &scriptedBackend{
		Mock:       mock.New(),
		execScript: execScript,
		execErrs:   execErrs,
	}
	scripted.Mock.IsRunningResult = true
	s := &lifecycleStubs{}
	s.install(t)
	newBackendFn = func(_ backend.Config) (backend.Backend, error) { return scripted, nil }
	withStatStub(t, statExists)
	return scripted
}

func TestRunRestart_LockedSkipsLockStep(t *testing.T) {
	// isLocked probe returns non-zero → Bolted already locked, so
	// runLock is NOT called. We just need StopVM + StartVM to fire.
	scripted := installRestartSetup(t, []backend.ExecResult{
		{ExitCode: 1}, // ls /bolted/repos → locked
	}, nil)
	var stderr bytes.Buffer
	if err := runRestart(context.Background(), io.Discard, &stderr); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	methods := scripted.Mock.Methods()
	var sawStop, sawStart bool
	for _, m := range methods {
		if m == "StopVM" {
			sawStop = true
		}
		if m == "StartVM" {
			sawStart = true
		}
	}
	if !sawStop || !sawStart {
		t.Errorf("expected StopVM+StartVM, methods=%v", methods)
	}
	if !strings.Contains(stderr.String(), "restarted") {
		t.Errorf("expected restart confirmation, got %q", stderr.String())
	}
}

func TestRunRestart_UnlockedCallsLockAndCyclesVM(t *testing.T) {
	// isLocked probe → exit 0 (unlocked). runLock is invoked, which
	// in turn loads config, builds backend, and calls Unmount/Close
	// on the fake volume. We rely on the same lifecycleStubs vol
	// machinery (fake volume succeeds), so runLock returns nil.
	scripted := installRestartSetup(t, []backend.ExecResult{
		{ExitCode: 0}, // ls /bolted/repos → unlocked
	}, nil)
	var stderr bytes.Buffer
	if err := runRestart(context.Background(), io.Discard, &stderr); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	methods := scripted.Mock.Methods()
	var sawStop, sawStart bool
	for _, m := range methods {
		if m == "StopVM" {
			sawStop = true
		}
		if m == "StartVM" {
			sawStart = true
		}
	}
	if !sawStop || !sawStart {
		t.Errorf("expected StopVM+StartVM, methods=%v", methods)
	}
}

func TestRunRestart_LockFailureIsWarning(t *testing.T) {
	// Unlocked + lock fails → warning printed, restart still proceeds.
	scripted := installRestartSetup(t, []backend.ExecResult{
		{ExitCode: 0}, // ls /bolted/repos → unlocked
	}, nil)
	// Override volume so Unmount fails.
	orig := newVolumeFn
	t.Cleanup(func() { newVolumeFn = orig })
	failingVol := &fakeVolume{unmountErr: errors.New("busy")}
	newVolumeFn = func(_ backend.Backend, _ volume.Options) volumeOps { return failingVol }

	var stderr bytes.Buffer
	if err := runRestart(context.Background(), io.Discard, &stderr); err != nil {
		t.Fatalf("unexpected restart err: %v", err)
	}
	if !strings.Contains(stderr.String(), "warning") {
		t.Errorf("expected warning on stderr, got %q", stderr.String())
	}
	// Restart should still have cycled the VM.
	methods := scripted.Mock.Methods()
	var sawStop bool
	for _, m := range methods {
		if m == "StopVM" {
			sawStop = true
		}
	}
	if !sawStop {
		t.Errorf("expected StopVM even after warning, methods=%v", methods)
	}
}

func TestRunRestart_StopVMFails(t *testing.T) {
	scripted := installRestartSetup(t, []backend.ExecResult{
		{ExitCode: 1}, // locked → skip pre-lock
	}, nil)
	scripted.Mock.ErrStopVM = errors.New("stop boom")
	err := runRestart(context.Background(), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "stop VM") {
		t.Errorf("expected stop err, got %v", err)
	}
}

func TestRunRestart_StartVMFails(t *testing.T) {
	scripted := installRestartSetup(t, []backend.ExecResult{
		{ExitCode: 1},
	}, nil)
	scripted.Mock.ErrStartVM = errors.New("start boom")
	err := runRestart(context.Background(), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "start VM") {
		t.Errorf("expected start err, got %v", err)
	}
}

// ---- Cobra plumbing -------------------------------------------------------

func TestNewRestartCmd_Construction(t *testing.T) {
	cmd := newRestartCmd()
	if cmd.Use != "restart" {
		t.Errorf("expected Use=restart, got %q", cmd.Use)
	}
}

func TestRestartCmd_RunE_Dispatch(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	withStatStub(t, statMissing) // fail-fast inside runRestart
	cmd := newRestartCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error from runRestart (not initialised)")
	}
}
