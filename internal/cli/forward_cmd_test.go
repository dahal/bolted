package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dahal/bolted/internal/backend"
)

// ---- fake allocator -------------------------------------------------------

type fakeAllocator struct {
	hostPort int
	remap    bool
	err      error
	calls    []fakeAllocatorCall
}

type fakeAllocatorCall struct {
	repo          string
	containerPort int
}

func (f *fakeAllocator) Allocate(_ context.Context, repo string, containerPort int) (int, bool, error) {
	f.calls = append(f.calls, fakeAllocatorCall{repo: repo, containerPort: containerPort})
	if f.err != nil {
		return 0, false, f.err
	}
	return f.hostPort, f.remap, nil
}

func withAllocatorStub(t *testing.T, fa *fakeAllocator) {
	t.Helper()
	orig := newAllocatorFn
	t.Cleanup(func() { newAllocatorFn = orig })
	newAllocatorFn = func(_ backend.Backend, _ string) allocator { return fa }
}

// ---- portsRecord on-disk shape -------------------------------------------

func TestPortsRecord_JSONShape(t *testing.T) {
	// host_port / container_port / process — must match portforward.persistedEntry.
	rec := portsRecord{HostPort: 8001, ContainerPort: 8000, Process: "uvicorn"}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"host_port", "container_port", "process"} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing JSON key %q in %s", key, data)
		}
	}
}

func TestPortsFilePath_UsesStateDir(t *testing.T) {
	orig := stateDirFn
	t.Cleanup(func() { stateDirFn = orig })
	stateDirFn = func() string { return "/tmp/x/state" }
	want := filepath.Join("/tmp/x/state", "ports.json")
	if got := portsFilePath(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestReadPortsStore_MissingReturnsEmpty(t *testing.T) {
	withStateDirStub(t)
	store, err := readPortsStore()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(store) != 0 {
		t.Errorf("expected empty, got %v", store)
	}
}

func TestReadPortsStore_ParsesEntries(t *testing.T) {
	dir := withStateDirStub(t)
	raw := `{"api":[{"host_port":8001,"container_port":8000,"process":"uvicorn"}]}`
	if err := os.WriteFile(filepath.Join(dir, "ports.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := readPortsStore()
	if err != nil {
		t.Fatal(err)
	}
	if len(store["api"]) != 1 || store["api"][0].HostPort != 8001 || store["api"][0].ContainerPort != 8000 ||
		store["api"][0].Process != "uvicorn" {
		t.Errorf("unexpected entries: %v", store)
	}
}

func TestReadPortsStore_ParseError(t *testing.T) {
	dir := withStateDirStub(t)
	if err := os.WriteFile(filepath.Join(dir, "ports.json"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPortsStore(); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestReadPortsStore_InnerParseError(t *testing.T) {
	// Top-level parses as map[string]json.RawMessage but the inner
	// payload isn't an array — exercises the per-repo unmarshal error.
	dir := withStateDirStub(t)
	if err := os.WriteFile(filepath.Join(dir, "ports.json"), []byte(`{"api":"oops"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPortsStore(); err == nil || !strings.Contains(err.Error(), "parse repo") {
		t.Errorf("expected per-repo parse error, got %v", err)
	}
}

func TestWritePortsStore_RoundTrips(t *testing.T) {
	withStateDirStub(t)
	in := portsStore{"api": {{HostPort: 8001, ContainerPort: 8000, Process: "x"}}}
	if err := writePortsStore(in); err != nil {
		t.Fatal(err)
	}
	out, err := readPortsStore()
	if err != nil {
		t.Fatal(err)
	}
	if len(out["api"]) != 1 || out["api"][0].HostPort != 8001 {
		t.Errorf("round-trip mismatch: %v", out)
	}
}

// ---- upsertForward / removeForward ----------------------------------------

func TestUpsertForward_AppendAndReplace(t *testing.T) {
	store := portsStore{}
	upsertForward(store, "api", portsRecord{HostPort: 8001, ContainerPort: 8000})
	if len(store["api"]) != 1 {
		t.Fatalf("expected 1 entry, got %v", store)
	}
	// Append a different host port.
	upsertForward(store, "api", portsRecord{HostPort: 9000, ContainerPort: 9000})
	if len(store["api"]) != 2 {
		t.Fatalf("expected 2 entries, got %v", store)
	}
	// Replace the first one (same host port).
	upsertForward(store, "api", portsRecord{HostPort: 8001, ContainerPort: 8080, Process: "new"})
	if len(store["api"]) != 2 {
		t.Errorf("expected still 2 entries, got %v", store)
	}
	if store["api"][0].ContainerPort != 8080 || store["api"][0].Process != "new" {
		t.Errorf("expected replacement, got %v", store["api"][0])
	}
}

func TestRemoveForward_DropsMatching(t *testing.T) {
	store := portsStore{"api": {
		{HostPort: 8001, ContainerPort: 8000},
		{HostPort: 9001, ContainerPort: 9000},
	}}
	host, ok := removeForward(store, "api", 8000)
	if !ok || host != 8001 {
		t.Errorf("expected host=8001 ok=true, got host=%d ok=%v", host, ok)
	}
	if len(store["api"]) != 1 || store["api"][0].ContainerPort != 9000 {
		t.Errorf("expected only 9000 left, got %v", store)
	}
}

func TestRemoveForward_LastEntryDeletesRepoKey(t *testing.T) {
	store := portsStore{"api": {{HostPort: 8001, ContainerPort: 8000}}}
	host, ok := removeForward(store, "api", 8000)
	if !ok || host != 8001 {
		t.Errorf("expected ok host=8001, got %d/%v", host, ok)
	}
	if _, present := store["api"]; present {
		t.Errorf("expected api key gone, got %v", store)
	}
}

func TestRemoveForward_NoRepo(t *testing.T) {
	store := portsStore{}
	if _, ok := removeForward(store, "api", 8000); ok {
		t.Errorf("expected ok=false for missing repo")
	}
}

func TestRemoveForward_NoMatchingPort(t *testing.T) {
	store := portsStore{"api": {{HostPort: 8001, ContainerPort: 8000}}}
	if _, ok := removeForward(store, "api", 9999); ok {
		t.Errorf("expected ok=false for missing port")
	}
}

// ---- runForward -----------------------------------------------------------

func TestRunForward_RequireUnlockedFails(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	withStatStub(t, statMissing)
	err := runForward(context.Background(), io.Discard, io.Discard, "api", 8000, forwardOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != exitLocked {
		t.Errorf("expected exit %d, got %d", exitLocked, code)
	}
}

func TestRunForward_RepoNotFound(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0}, // unlocked probe
		{ExitCode: 1}, // test -d
	}, nil)
	err := runForward(context.Background(), io.Discard, io.Discard, "missing", 8000, forwardOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != exitRepoNotFound {
		t.Errorf("expected exit %d, got %d", exitRepoNotFound, code)
	}
}

func TestRunForward_ToFlagCallsBackend(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0}, // unlocked probe
		{ExitCode: 0}, // test -d
	}, nil)
	if err := runForward(context.Background(), io.Discard, io.Discard, "api", 8000, forwardOptions{to: 18000}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	// ForwardPort call should be recorded on the mock with the explicit ports.
	var seen bool
	for _, c := range ds.scripted.Mock.Calls {
		if c.Method == "ForwardPort" && c.GuestPort == 8000 && c.HostPort == 18000 {
			seen = true
		}
	}
	if !seen {
		t.Errorf("expected ForwardPort(8000,18000), got %v", ds.scripted.Mock.Calls)
	}
	// Persistence: ports.json must contain the record.
	store, err := readPortsStore()
	if err != nil {
		t.Fatal(err)
	}
	if len(store["api"]) != 1 || store["api"][0].HostPort != 18000 || store["api"][0].ContainerPort != 8000 {
		t.Errorf("expected persisted record host=18000 container=8000, got %v", store)
	}
}

func TestRunForward_ToFlagErrorsOnConflict(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	ds.scripted.Mock.ErrForwardPort = errors.New("port in use")
	err := runForward(context.Background(), io.Discard, io.Discard, "api", 8000, forwardOptions{to: 18000})
	if err == nil || !strings.Contains(err.Error(), "8000 -> 18000") {
		t.Errorf("expected wrapped forward err, got %v", err)
	}
}

func TestRunForward_AllocateCallsManager(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	fa := &fakeAllocator{hostPort: 8001, remap: true}
	withAllocatorStub(t, fa)
	if err := runForward(context.Background(), io.Discard, io.Discard, "api", 8000, forwardOptions{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(fa.calls) != 1 || fa.calls[0].repo != "api" || fa.calls[0].containerPort != 8000 {
		t.Errorf("expected Allocate(api,8000), got %v", fa.calls)
	}
	// Persistence with the host port the allocator returned.
	store, _ := readPortsStore()
	if len(store["api"]) != 1 || store["api"][0].HostPort != 8001 {
		t.Errorf("expected host=8001 persisted, got %v", store)
	}
}

func TestRunForward_AllocateError(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	withAllocatorStub(t, &fakeAllocator{err: errors.New("nothing free")})
	err := runForward(context.Background(), io.Discard, io.Discard, "api", 8000, forwardOptions{})
	if err == nil || !strings.Contains(err.Error(), "allocate host port") {
		t.Errorf("expected wrapped allocate err, got %v", err)
	}
}

func TestRunForward_ReadPortsError(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	// Corrupt ports.json so readPortsStore fails after Allocate succeeds.
	if err := os.WriteFile(filepath.Join(ds.stateDir, "ports.json"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	withAllocatorStub(t, &fakeAllocator{hostPort: 8001})
	err := runForward(context.Background(), io.Discard, io.Discard, "api", 8000, forwardOptions{})
	if err == nil || !strings.Contains(err.Error(), "read ports") {
		t.Errorf("expected read err, got %v", err)
	}
}

func TestRunForward_WritePortsError(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	withAllocatorStub(t, &fakeAllocator{hostPort: 8001})
	// Make state dir read-only so the atomic write fails.
	if err := chmodReadOnly(ds.stateDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = chmodReadWrite(ds.stateDir) })
	err := runForward(context.Background(), io.Discard, io.Discard, "api", 8000, forwardOptions{})
	if err == nil || !strings.Contains(err.Error(), "persist ports") {
		t.Errorf("expected persist err, got %v", err)
	}
}

// ---- runUnforward ---------------------------------------------------------

func TestRunUnforward_RequireUnlockedFails(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	withStatStub(t, statMissing)
	err := runUnforward(context.Background(), io.Discard, io.Discard, "api", 8000)
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != exitLocked {
		t.Errorf("expected exit %d, got %d", exitLocked, code)
	}
}

func TestRunUnforward_ReadPortsError(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
	}, nil)
	if err := os.WriteFile(filepath.Join(ds.stateDir, "ports.json"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runUnforward(context.Background(), io.Discard, io.Discard, "api", 8000)
	if err == nil || !strings.Contains(err.Error(), "read ports") {
		t.Errorf("expected read err, got %v", err)
	}
}

func TestRunUnforward_NoMatchingPort(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0}, // unlocked probe
	}, nil)
	var stderr strings.Builder
	err := runUnforward(context.Background(), io.Discard, &stderr, "api", 8000)
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != exitGeneric {
		t.Errorf("expected exit %d, got %d", exitGeneric, code)
	}
	if !strings.Contains(stderr.String(), "no forward") {
		t.Errorf("expected friendly diag, got %q", stderr.String())
	}
}

func TestRunUnforward_HappyPath(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
	}, nil)
	// Seed ports.json with a record we can remove.
	store := portsStore{"api": {{HostPort: 18000, ContainerPort: 8000}}}
	if err := writePortsStore(store); err != nil {
		t.Fatal(err)
	}
	if err := runUnforward(context.Background(), io.Discard, io.Discard, "api", 8000); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	// Backend.UnforwardPort should have been called with 18000.
	var seen bool
	for _, c := range ds.scripted.Mock.Calls {
		if c.Method == "UnforwardPort" && c.HostPort == 18000 {
			seen = true
		}
	}
	if !seen {
		t.Errorf("expected UnforwardPort(18000), got %v", ds.scripted.Mock.Calls)
	}
	// Persistence: entry should be gone.
	got, _ := readPortsStore()
	if _, present := got["api"]; present {
		t.Errorf("expected api dropped, got %v", got)
	}
}

func TestRunUnforward_BackendError(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
	}, nil)
	_ = writePortsStore(portsStore{"api": {{HostPort: 18000, ContainerPort: 8000}}})
	ds.scripted.Mock.ErrUnforwardPort = errors.New("ssh dead")
	err := runUnforward(context.Background(), io.Discard, io.Discard, "api", 8000)
	if err == nil || !strings.Contains(err.Error(), "unforward host:18000") {
		t.Errorf("expected wrapped err, got %v", err)
	}
}

func TestRunUnforward_WritePortsError(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
	}, nil)
	_ = writePortsStore(portsStore{"api": {{HostPort: 18000, ContainerPort: 8000}}})
	// Make state dir read-only AFTER seeding so UnforwardPort succeeds
	// but writePortsStore fails.
	if err := chmodReadOnly(ds.stateDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = chmodReadWrite(ds.stateDir) })
	err := runUnforward(context.Background(), io.Discard, io.Discard, "api", 8000)
	if err == nil || !strings.Contains(err.Error(), "persist ports") {
		t.Errorf("expected persist err, got %v", err)
	}
}

// ---- Cobra plumbing -------------------------------------------------------

func TestNewForwardCmd_FlagsRegistered(t *testing.T) {
	cmd := newForwardCmd()
	if !strings.HasPrefix(cmd.Use, "forward") {
		t.Errorf("expected Use to start with forward, got %q", cmd.Use)
	}
	if cmd.Flags().Lookup("to") == nil {
		t.Error("expected --to flag")
	}
}

func TestForwardCmd_RunE_BadPort(t *testing.T) {
	cmd := newForwardCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"api", "not-a-port"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "invalid port") {
		t.Errorf("expected port parse err, got %v", err)
	}
}

func TestForwardCmd_RunE_RequiresTwoArgs(t *testing.T) {
	cmd := newForwardCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"api"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected arg-count error")
	}
}

func TestForwardCmd_RunE_Dispatch(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	withStatStub(t, statMissing) // fail-fast inside runForward
	cmd := newForwardCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"api", "8000"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error from runForward (not initialised)")
	}
}

func TestNewUnforwardCmd_Construction(t *testing.T) {
	cmd := newUnforwardCmd()
	if !strings.HasPrefix(cmd.Use, "unforward") {
		t.Errorf("expected Use to start with unforward, got %q", cmd.Use)
	}
}

func TestUnforwardCmd_RunE_BadPort(t *testing.T) {
	cmd := newUnforwardCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"api", "not-a-port"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "invalid port") {
		t.Errorf("expected port parse err, got %v", err)
	}
}

func TestUnforwardCmd_RunE_RequiresTwoArgs(t *testing.T) {
	cmd := newUnforwardCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"api"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected arg-count error")
	}
}

func TestUnforwardCmd_RunE_Dispatch(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	withStatStub(t, statMissing) // fail-fast inside runUnforward
	cmd := newUnforwardCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"api", "8000"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error")
	}
}

// ---- defensive: default newAllocatorFn returns a real Manager ------------

func TestNewAllocatorFn_DefaultReturnsRealManager(t *testing.T) {
	a := newAllocatorFn(nil, "/tmp/x")
	if a == nil {
		t.Fatal("default newAllocatorFn returned nil")
	}
}
