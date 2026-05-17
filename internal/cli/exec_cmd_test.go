package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dahal/bolted/internal/backend"
	"github.com/dahal/bolted/internal/devcontainer"
)

// ---- runExec --------------------------------------------------------------

func TestRunExec_EmptyCommand(t *testing.T) {
	err := runExec(context.Background(), io.Discard, io.Discard, "api", nil)
	if err == nil || !strings.Contains(err.Error(), "no command") {
		t.Errorf("expected no-command error, got %v", err)
	}
}

func TestRunExec_RequireUnlockedFails(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	withStatStub(t, statMissing)
	err := runExec(context.Background(), io.Discard, io.Discard, "api", []string{"echo"})
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != exitLocked {
		t.Errorf("expected exit %d, got %d", exitLocked, code)
	}
}

func TestRunExec_RepoNotFound(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0}, // unlocked
		{ExitCode: 1}, // test -d
	}, nil)
	err := runExec(context.Background(), io.Discard, io.Discard, "missing", []string{"echo"})
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != exitRepoNotFound {
		t.Errorf("expected exit %d, got %d", exitRepoNotFound, code)
	}
}

func TestRunExec_StoredIDReadError(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	// Corrupt containers.json so storedContainerID errors.
	if err := writeJunk(ds.stateDir); err != nil {
		t.Fatal(err)
	}
	if err := runExec(context.Background(), io.Discard, io.Discard, "api", []string{"echo"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestRunExec_UpFails(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	ds.runner.upErr = errors.New("up boom")
	err := runExec(context.Background(), io.Discard, io.Discard, "api", []string{"echo"})
	if err == nil || !strings.Contains(err.Error(), "devcontainer up") {
		t.Errorf("expected up error, got %v", err)
	}
}

func TestRunExec_PersistContainerError(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	// Seed valid empty JSON; chmod read-only so write fails.
	if err := writeEmptyContainers(ds.stateDir); err != nil {
		t.Fatal(err)
	}
	if err := chmodReadOnly(ds.stateDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = chmodReadWrite(ds.stateDir) })
	err := runExec(context.Background(), io.Discard, io.Discard, "api", []string{"echo"})
	if err == nil || !strings.Contains(err.Error(), "persist container id") {
		t.Errorf("expected persist error, got %v", err)
	}
}

func TestRunExec_HappyPathBringsContainerUp(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	ds.runner.execResult = devcontainer.ExecResult{Stdout: []byte("v20\n"), ExitCode: 0}
	var stdout bytes.Buffer
	if err := runExec(context.Background(), &stdout, io.Discard, "api", []string{"node", "--version"}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(stdout.String(), "v20") {
		t.Errorf("expected stdout, got %q", stdout.String())
	}
	if len(ds.runner.upCalls) != 1 {
		t.Errorf("expected Up call on first invocation, got %d", len(ds.runner.upCalls))
	}
	if len(ds.runner.execCalls) != 1 {
		t.Fatalf("expected 1 Exec, got %d", len(ds.runner.execCalls))
	}
	if ds.runner.execCalls[0].opts.TTY {
		t.Errorf("bolt exec should not request a TTY")
	}
}

func TestRunExec_ReuseExistingContainer(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	if err := recordContainer("api", "existing"); err != nil {
		t.Fatal(err)
	}
	if err := runExec(context.Background(), io.Discard, io.Discard, "api", []string{"echo"}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(ds.runner.upCalls) != 0 {
		t.Errorf("expected no Up when container is already known, got %d", len(ds.runner.upCalls))
	}
	if ds.runner.execCalls[0].containerID != "existing" {
		t.Errorf("expected reuse, got %q", ds.runner.execCalls[0].containerID)
	}
}

func TestRunExec_ExecError(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	ds.runner.execErr = errors.New("exec boom")
	err := runExec(context.Background(), io.Discard, io.Discard, "api", []string{"echo"})
	if err == nil || !strings.Contains(err.Error(), "exec in container") {
		t.Errorf("expected exec error, got %v", err)
	}
}

func TestRunExec_NonZeroExitPropagates(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	ds.runner.execResult = devcontainer.ExecResult{ExitCode: 13, Stdout: []byte("err out"), Stderr: []byte("err msg")}
	var stdout, stderr bytes.Buffer
	err := runExec(context.Background(), &stdout, &stderr, "api", []string{"false"})
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != 13 {
		t.Errorf("expected exit 13, got %d", code)
	}
	if !strings.Contains(stdout.String(), "err out") || !strings.Contains(stderr.String(), "err msg") {
		t.Errorf("expected output flush, got stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// ---- Cobra plumbing -------------------------------------------------------

func TestNewExecCmd_Construction(t *testing.T) {
	cmd := newExecCmd()
	if !strings.HasPrefix(cmd.Use, "exec") {
		t.Errorf("expected Use prefix 'exec', got %q", cmd.Use)
	}
	if cmd.Short == "" {
		t.Error("expected non-empty Short")
	}
}

func TestExecCmd_RunE_RequiresArgs(t *testing.T) {
	// Cobra-level: MinimumNArgs(2) → 1 arg should fail before RunE.
	cmd := newExecCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"api"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected arg-count error")
	}
}

func TestExecCmd_RunE_Dispatch(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	cmd := newExecCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"api", "echo", "hi"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(ds.runner.execCalls) != 1 {
		t.Fatalf("expected 1 exec, got %d", len(ds.runner.execCalls))
	}
	if got := ds.runner.execCalls[0].cmd; len(got) != 2 || got[0] != "echo" || got[1] != "hi" {
		t.Errorf("unexpected cmd: %v", got)
	}
}

// ---- helpers shared with other test files ---------------------------------

// writeJunk corrupts containers.json so the next read errors.
func writeJunk(stateDir string) error {
	return os.WriteFile(filepath.Join(stateDir, "containers.json"), []byte("garbage"), 0o600)
}

// writeEmptyContainers writes a valid empty containers.json so the
// next read succeeds; the next write may still fail if the directory
// is read-only.
func writeEmptyContainers(stateDir string) error {
	return os.WriteFile(filepath.Join(stateDir, "containers.json"), []byte("{}"), 0o600)
}

func chmodReadOnly(dir string) error  { return os.Chmod(dir, 0o500) }
func chmodReadWrite(dir string) error { return os.Chmod(dir, 0o700) }
