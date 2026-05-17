package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dahal/bolted/internal/backend"
	"github.com/dahal/bolted/internal/portforward"
)

// ---- fake portsLister -----------------------------------------------------

type fakeLister struct {
	out map[string][]portforward.Mapping
	err error
}

func (f *fakeLister) List() (map[string][]portforward.Mapping, error) {
	return f.out, f.err
}

// withManagerStub swaps newManagerFn for the duration of one test.
func withManagerStub(t *testing.T, fl portsLister) {
	t.Helper()
	orig := newManagerFn
	t.Cleanup(func() { newManagerFn = orig })
	newManagerFn = func(_ backend.Backend, _ string) portsLister { return fl }
}

// ---- newPortsCmd ----------------------------------------------------------

func TestNewPortsCmd_FlagsRegistered(t *testing.T) {
	cmd := newPortsCmd()
	if cmd.Use != "ports" {
		t.Errorf("expected Use=ports, got %q", cmd.Use)
	}
	if cmd.Flags().Lookup("json") == nil {
		t.Error("expected --json flag")
	}
}

// ---- runPorts -------------------------------------------------------------

func TestRunPorts_RequireUnlockedFails(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	withStatStub(t, statMissing)
	err := runPorts(context.Background(), io.Discard, io.Discard, portsOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != exitLocked {
		t.Errorf("expected exit %d, got %d", exitLocked, code)
	}
}

func TestRunPorts_ListError(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{{ExitCode: 0}}, nil)
	withManagerStub(t, &fakeLister{err: errors.New("read boom")})
	err := runPorts(context.Background(), io.Discard, io.Discard, portsOptions{})
	if err == nil || !strings.Contains(err.Error(), "read ports") {
		t.Errorf("expected read err, got %v", err)
	}
}

func TestRunPorts_TableHappyPath(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{{ExitCode: 0}}, nil)
	withManagerStub(t, &fakeLister{out: map[string][]portforward.Mapping{
		"api": {{Repo: "api", HostPort: 8001, ContainerPort: 8000, Process: "uvicorn"}},
		"web": {{Repo: "web", HostPort: 3000, ContainerPort: 3000, Process: "vite"}},
	}})
	var stdout bytes.Buffer
	if err := runPorts(context.Background(), &stdout, io.Discard, portsOptions{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	out := stdout.String()
	for _, want := range []string{"REPO", "HOST PORT", "CONTAINER PORT", "PROCESS",
		"api", "8001", "8000", "uvicorn",
		"web", "3000", "vite"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q: %q", want, out)
		}
	}
	// Repos should be sorted (api before web).
	if strings.Index(out, "api") > strings.Index(out, "web") {
		t.Errorf("expected api before web, got %q", out)
	}
}

func TestRunPorts_JSONHappyPath(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{{ExitCode: 0}}, nil)
	withManagerStub(t, &fakeLister{out: map[string][]portforward.Mapping{
		"api": {{Repo: "api", HostPort: 8001, ContainerPort: 8000, Process: "uvicorn"}},
	}})
	var stdout bytes.Buffer
	if err := runPorts(context.Background(), &stdout, io.Discard, portsOptions{jsonOut: true}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	var entries []portsEntry
	if err := json.Unmarshal(stdout.Bytes(), &entries); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, stdout.String())
	}
	if len(entries) != 1 || entries[0].Repo != "api" || entries[0].HostPort != 8001 ||
		entries[0].ContainerPort != 8000 || entries[0].Process != "uvicorn" {
		t.Errorf("unexpected entries: %+v", entries)
	}
}

// jsonPortsErrWriter forces encode to fail.
type jsonPortsErrWriter struct{}

func (jsonPortsErrWriter) Write([]byte) (int, error) { return 0, errors.New("write boom") }

func TestRunPorts_JSONEncodeError(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{{ExitCode: 0}}, nil)
	withManagerStub(t, &fakeLister{out: map[string][]portforward.Mapping{
		"api": {{Repo: "api", HostPort: 8001, ContainerPort: 8000, Process: "x"}},
	}})
	err := runPorts(context.Background(), jsonPortsErrWriter{}, io.Discard, portsOptions{jsonOut: true})
	if err == nil || !strings.Contains(err.Error(), "encode JSON") {
		t.Errorf("expected encode err, got %v", err)
	}
}

func TestRunPorts_EmptyStillRendersHeader(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{{ExitCode: 0}}, nil)
	withManagerStub(t, &fakeLister{out: map[string][]portforward.Mapping{}})
	var stdout bytes.Buffer
	if err := runPorts(context.Background(), &stdout, io.Discard, portsOptions{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(stdout.String(), "REPO") {
		t.Errorf("expected header even with no rows, got %q", stdout.String())
	}
}

// ---- flattenPorts ---------------------------------------------------------

func TestFlattenPorts_SortsByRepoThenHostPort(t *testing.T) {
	got := flattenPorts(map[string][]portforward.Mapping{
		"web": {
			{Repo: "web", HostPort: 4000, ContainerPort: 4000},
			{Repo: "web", HostPort: 3000, ContainerPort: 3000},
		},
		"api": {
			{Repo: "api", HostPort: 8001, ContainerPort: 8000},
		},
	})
	want := []portsEntry{
		{Repo: "api", HostPort: 8001, ContainerPort: 8000},
		{Repo: "web", HostPort: 3000, ContainerPort: 3000},
		{Repo: "web", HostPort: 4000, ContainerPort: 4000},
	}
	if len(got) != len(want) {
		t.Fatalf("len mismatch: got %v want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("entry %d: got %+v want %+v", i, got[i], want[i])
		}
	}
}

// ---- renderPortsTable -----------------------------------------------------

func TestRenderPortsTable_HeaderAndRows(t *testing.T) {
	var buf bytes.Buffer
	renderPortsTable(&buf, []portsEntry{
		{Repo: "api", HostPort: 8001, ContainerPort: 8000, Process: "uvicorn"},
		{Repo: "web", HostPort: 3000, ContainerPort: 3000, Process: "vite"},
	})
	out := buf.String()
	for _, want := range []string{"REPO", "HOST PORT", "CONTAINER PORT", "PROCESS",
		"api", "8001", "uvicorn", "web", "3000", "vite"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in: %q", want, out)
		}
	}
}

// ---- Cobra integration ----------------------------------------------------

func TestPortsCmd_RunE_Dispatch(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	withStatStub(t, statMissing) // fail-fast inside runPorts
	cmd := newPortsCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error from runPorts (not initialised)")
	}
}

// ---- defensive: ensure newManagerFn default returns a real manager -------

func TestNewManagerFn_DefaultReturnsRealManager(t *testing.T) {
	m := newManagerFn(nil, "/tmp/x")
	if m == nil {
		t.Fatal("default newManagerFn returned nil")
	}
}
