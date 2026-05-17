package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sort"
	"strings"
	"testing"

	"github.com/dahal/bolted/internal/backend"
)

// ---- runStop --------------------------------------------------------------

func TestRunStop_ConflictingFlags(t *testing.T) {
	err := runStop(context.Background(), io.Discard, io.Discard, []string{"api"}, stopOptions{all: true})
	if err == nil || !strings.Contains(err.Error(), "cannot combine") {
		t.Errorf("expected conflict error, got %v", err)
	}
}

func TestRunStop_MissingArgWithoutAll(t *testing.T) {
	err := runStop(context.Background(), io.Discard, io.Discard, nil, stopOptions{})
	if err == nil || !strings.Contains(err.Error(), "repo name") {
		t.Errorf("expected missing-arg error, got %v", err)
	}
}

func TestRunStop_RequireUnlockedFails(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	withStatStub(t, statMissing)
	err := runStop(context.Background(), io.Discard, io.Discard, []string{"api"}, stopOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != exitLocked {
		t.Errorf("expected exit %d, got %d", exitLocked, code)
	}
}

func TestRunStop_ContainersReadError(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0}, // unlocked probe
	}, nil)
	if err := writeJunk(ds.stateDir); err != nil {
		t.Fatal(err)
	}
	err := runStop(context.Background(), io.Discard, io.Discard, []string{"api"}, stopOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRunStop_RepoNotFound(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0}, // unlocked
		{ExitCode: 1}, // test -d
	}, nil)
	err := runStop(context.Background(), io.Discard, io.Discard, []string{"missing"}, stopOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != exitRepoNotFound {
		t.Errorf("expected exit %d, got %d", exitRepoNotFound, code)
	}
}

func TestRunStop_NoRecordedContainer(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0}, // unlocked
		{ExitCode: 0}, // test -d
	}, nil)
	var stderr bytes.Buffer
	if err := runStop(context.Background(), io.Discard, &stderr, []string{"api"}, stopOptions{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(stderr.String(), "nothing to stop") {
		t.Errorf("expected friendly message, got %q", stderr.String())
	}
}

func TestRunStop_HappyPath(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0}, // unlocked
		{ExitCode: 0}, // test -d
	}, nil)
	if err := recordContainer("api", "abc"); err != nil {
		t.Fatal(err)
	}
	if err := runStop(context.Background(), io.Discard, io.Discard, []string{"api"}, stopOptions{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(ds.runner.downCalls) != 1 || ds.runner.downCalls[0] != "abc" {
		t.Errorf("expected Down on abc, got %v", ds.runner.downCalls)
	}
	m, _ := readContainers()
	if _, ok := m["api"]; ok {
		t.Errorf("api should be removed from containers.json, got %v", m)
	}
}

func TestRunStop_DownError(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	if err := recordContainer("api", "abc"); err != nil {
		t.Fatal(err)
	}
	ds.runner.downErr = errors.New("down boom")
	err := runStop(context.Background(), io.Discard, io.Discard, []string{"api"}, stopOptions{})
	if err == nil || !strings.Contains(err.Error(), "stop") {
		t.Errorf("expected down error, got %v", err)
	}
}

func TestRunStop_ForgetError(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	if err := recordContainer("api", "abc"); err != nil {
		t.Fatal(err)
	}
	// Chmod state dir read-only AFTER recording to force write fail.
	if err := chmodReadOnly(ds.stateDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = chmodReadWrite(ds.stateDir) })
	err := runStop(context.Background(), io.Discard, io.Discard, []string{"api"}, stopOptions{})
	if err == nil || !strings.Contains(err.Error(), "containers.json") {
		t.Errorf("expected forget error, got %v", err)
	}
}

func TestRunStop_AllWithNoContainers(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
	}, nil)
	var stderr bytes.Buffer
	if err := runStop(context.Background(), io.Discard, &stderr, nil, stopOptions{all: true}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(stderr.String(), "no running containers") {
		t.Errorf("expected friendly message, got %q", stderr.String())
	}
}

func TestRunStop_AllStopsEvery(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
	}, nil)
	_ = recordContainer("api", "id-api")
	_ = recordContainer("web", "id-web")
	if err := runStop(context.Background(), io.Discard, io.Discard, nil, stopOptions{all: true}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	got := append([]string(nil), ds.runner.downCalls...)
	sort.Strings(got)
	want := []string{"id-api", "id-web"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("downCalls=%v, want %v", got, want)
	}
	m, _ := readContainers()
	if len(m) != 0 {
		t.Errorf("expected containers cleared, got %v", m)
	}
}

// ---- Cobra plumbing -------------------------------------------------------

func TestNewStopCmd_FlagsRegistered(t *testing.T) {
	cmd := newStopCmd()
	if cmd.Flags().Lookup("all") == nil {
		t.Error("expected --all flag")
	}
}

func TestStopCmd_RunE_Dispatch(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	_ = recordContainer("api", "id-api")
	cmd := newStopCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"api"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(ds.runner.downCalls) != 1 {
		t.Errorf("expected Down, got %v", ds.runner.downCalls)
	}
}

func TestStopCmd_RunE_TooManyArgs(t *testing.T) {
	cmd := newStopCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"a", "b"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected arg-count error")
	}
}
