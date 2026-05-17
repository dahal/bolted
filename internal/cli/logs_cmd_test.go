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

// ---- containerNameForRepo -------------------------------------------------

func TestContainerNameForRepo(t *testing.T) {
	if got := containerNameForRepo("api"); got != "bolted-api" {
		t.Errorf("got %q, want bolted-api", got)
	}
}

// ---- runLogs --------------------------------------------------------------

func TestRunLogs_RequireUnlockedFails(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	withStatStub(t, statMissing)
	err := runLogs(context.Background(), io.Discard, io.Discard, "api", logsOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != exitLocked {
		t.Errorf("expected exit %d, got %d", exitLocked, code)
	}
}

func TestRunLogs_RepoNotFound(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0}, // unlocked probe
		{ExitCode: 1}, // test -d (repo missing)
	}, nil)
	err := runLogs(context.Background(), io.Discard, io.Discard, "missing", logsOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != exitRepoNotFound {
		t.Errorf("expected exit %d, got %d", exitRepoNotFound, code)
	}
}

func TestRunLogs_ExecError(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, []error{nil, nil, errors.New("podman boom")})
	err := runLogs(context.Background(), io.Discard, io.Discard, "api", logsOptions{})
	if err == nil || !strings.Contains(err.Error(), "podman logs") {
		t.Errorf("expected wrapped podman err, got %v", err)
	}
}

func TestRunLogs_NonZeroExit(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
		{ExitCode: 4, Stdout: []byte("partial out"), Stderr: []byte("no such container\n")},
	}, nil)
	var stdout, stderr bytes.Buffer
	err := runLogs(context.Background(), &stdout, &stderr, "api", logsOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != 4 {
		t.Errorf("expected exit 4, got %d", code)
	}
	if !strings.Contains(stdout.String(), "partial out") {
		t.Errorf("expected stdout flushed even on non-zero exit, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "no such container") {
		t.Errorf("expected stderr flushed, got %q", stderr.String())
	}
}

func TestRunLogs_HappyPath(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0}, // unlocked probe
		{ExitCode: 0}, // test -d
		{ExitCode: 0, Stdout: []byte("hello logs\n"), Stderr: []byte("warning\n")},
	}, nil)
	var stdout, stderr bytes.Buffer
	if err := runLogs(context.Background(), &stdout, &stderr, "api", logsOptions{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(stdout.String(), "hello logs") {
		t.Errorf("expected stdout, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "warning") {
		t.Errorf("expected stderr, got %q", stderr.String())
	}
	// Verify the third Exec was a `podman logs` against the expected name.
	calls := ds.scripted.Mock.Calls
	if len(calls) < 3 {
		t.Fatalf("expected at least 3 backend calls, got %d", len(calls))
	}
	last := calls[len(calls)-1]
	if last.Method != "Exec" {
		t.Errorf("expected last call Exec, got %s", last.Method)
	}
	if len(last.Cmd) < 3 || last.Cmd[0] != "podman" || last.Cmd[1] != "logs" {
		t.Errorf("expected `podman logs ...`, got %v", last.Cmd)
	}
	if last.Cmd[len(last.Cmd)-1] != "bolted-api" {
		t.Errorf("expected last arg bolted-api, got %v", last.Cmd)
	}
	for _, a := range last.Cmd {
		if a == "-f" {
			t.Errorf("did not expect -f without --follow, got %v", last.Cmd)
		}
	}
}

func TestRunLogs_FollowFlagAddsDashF(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
		{ExitCode: 0, Stdout: []byte("streamed\n")},
	}, nil)
	if err := runLogs(context.Background(), io.Discard, io.Discard, "api", logsOptions{follow: true}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	calls := ds.scripted.Mock.Calls
	last := calls[len(calls)-1]
	var sawF bool
	for _, a := range last.Cmd {
		if a == "-f" {
			sawF = true
		}
	}
	if !sawF {
		t.Errorf("expected -f in cmd, got %v", last.Cmd)
	}
}

// ---- Cobra plumbing -------------------------------------------------------

func TestNewLogsCmd_FlagsRegistered(t *testing.T) {
	cmd := newLogsCmd()
	if cmd.Use != "logs <repo>" {
		t.Errorf("unexpected Use=%q", cmd.Use)
	}
	if cmd.Flags().Lookup("follow") == nil {
		t.Error("expected --follow flag")
	}
}

func TestLogsCmd_RunE_RequiresRepoArg(t *testing.T) {
	cmd := newLogsCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected arg-count error")
	}
}

func TestLogsCmd_RunE_Dispatch(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	withStatStub(t, statMissing) // fail-fast inside runLogs
	cmd := newLogsCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"api"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error from runLogs (not initialised)")
	}
}
