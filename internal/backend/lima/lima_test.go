package lima

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dahal/bolted/internal/backend"
)

// fakeRunner is the test double for the runner interface. It records
// every Run/RunWithStdin invocation and returns canned output keyed by
// the first arg. The mutex matches the mock backend's idiom so tests
// reading Calls from a goroutine stay race-free under -race.
type fakeRunner struct {
	mu sync.Mutex
	// calls captures every invocation in order.
	calls []fakeCall
	// responses is a sequence of canned outputs. Each Run / RunWithStdin
	// pops the head; an empty queue returns zero output and nil error.
	responses []fakeResponse
	// matcher, when set, lets a test override the FIFO behaviour and
	// inspect args before returning. Useful for tests that need to
	// branch on which limactl subcommand was invoked.
	matcher func(args []string) fakeResponse
}

// fakeCall records one invocation.
type fakeCall struct {
	// name is the binary the production code asked us to invoke
	// (always "limactl" here).
	name string
	// args is the full argv tail passed to runner.Run.
	args []string
	// stdin captures the stdin payload from RunWithStdin. Nil for Run.
	stdin []byte
}

// fakeResponse is one canned reply.
type fakeResponse struct {
	// stdout is the bytes returned as stdout.
	stdout []byte
	// err is the error returned. May be a plain error or *exitError.
	err error
}

// Run satisfies runner. Records the call and pops the head response.
func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeCall{name: name, args: append([]string(nil), args...)})
	return f.next(args)
}

// RunWithStdin satisfies runner. Reads stdin into the call record so
// tests can assert on what was piped, then pops the head response.
func (f *fakeRunner) RunWithStdin(_ context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
	var buf []byte
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		buf = b
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeCall{name: name, args: append([]string(nil), args...), stdin: buf})
	return f.next(args)
}

// next returns the next canned response. Falls back to the matcher
// when the queue is empty; if no matcher is set returns (nil, nil).
func (f *fakeRunner) next(args []string) ([]byte, error) {
	if len(f.responses) > 0 {
		r := f.responses[0]
		f.responses = f.responses[1:]
		return r.stdout, r.err
	}
	if f.matcher != nil {
		r := f.matcher(args)
		return r.stdout, r.err
	}
	return nil, nil
}

// callsSnapshot returns a copy of the recorded calls under the mutex.
func (f *fakeRunner) callsSnapshot() []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// newTestBackend wires a Backend onto a fresh tempdir + the given
// runner. Centralises the setup boilerplate across tests.
func newTestBackend(t *testing.T, r runner) *Backend {
	t.Helper()
	dir := t.TempDir()
	return NewWithOptions(Options{Name: "bolted", DataDir: dir, Runner: r})
}

// TestBackend_ImplementsBackend pins the interface satisfaction so a
// future refactor that drops a method breaks at compile time.
func TestBackend_ImplementsBackend(t *testing.T) {
	var _ backend.Backend = New()
}

// TestNew_Defaults documents that the zero-Options constructor fills
// in the same defaults the no-arg New() exposes — they must agree.
func TestNew_Defaults(t *testing.T) {
	a := New()
	b := NewWithOptions(Options{})
	if a.name != b.name {
		t.Errorf("name mismatch: %q vs %q", a.name, b.name)
	}
	if a.dataDir != b.dataDir {
		t.Errorf("dataDir mismatch: %q vs %q", a.dataDir, b.dataDir)
	}
	if a.runner == nil || b.runner == nil {
		t.Fatal("runner must default to non-nil")
	}
	if a.name != "bolted"{
		t.Errorf("expected default name 'bolted', got %q", a.name)
	}
}

// TestNewWithOptions_Overrides pins that supplied Options take effect.
func TestNewWithOptions_Overrides(t *testing.T) {
	r := &fakeRunner{}
	b := NewWithOptions(Options{Name: "custom", DataDir: "/tmp/abc", Runner: r})
	if b.name != "custom" {
		t.Errorf("name override ignored: %q", b.name)
	}
	if b.dataDir != "/tmp/abc" {
		t.Errorf("dataDir override ignored: %q", b.dataDir)
	}
	if b.runner != r {
		t.Errorf("runner override ignored")
	}
}

// TestRequireLima_Success verifies the happy path: limactl --version
// succeeds, requireLima returns nil.
func TestRequireLima_Success(t *testing.T) {
	r := &fakeRunner{responses: []fakeResponse{{stdout: []byte("limactl version 1.0.0\n")}}}
	b := newTestBackend(t, r)
	if err := b.requireLima(context.Background()); err != nil {
		t.Fatalf("requireLima: %v", err)
	}
	calls := r.callsSnapshot()
	if len(calls) != 1 || calls[0].name != "limactl" || calls[0].args[0] != "--version" {
		t.Errorf("unexpected calls: %+v", calls)
	}
}

// TestRequireLima_Missing verifies the actionable install hint fires
// when limactl is not on PATH.
func TestRequireLima_Missing(t *testing.T) {
	r := &fakeRunner{responses: []fakeResponse{{err: errors.New("exec: limactl: not found")}}}
	b := newTestBackend(t, r)
	err := b.requireLima(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "brew install lima") {
		t.Errorf("error missing install hint: %v", err)
	}
}

// TestEnsureVM_CreatesWhenMissing covers the new-VM path: requireLima
// succeeds, `limactl ls --json` returns no match, `limactl create`
// runs. The rendered lima.yaml must exist after.
func TestEnsureVM_CreatesWhenMissing(t *testing.T) {
	r := &fakeRunner{
		responses: []fakeResponse{
			{stdout: []byte("limactl version 1.0.0\n")},     // --version
			{stdout: []byte(`{"name":"other","status":"Stopped"}` + "\n")}, // ls
			{stdout: []byte("created\n")},                   // create
		},
	}
	b := newTestBackend(t, r)
	spec := backend.VMSpec{CPUs: 4, MemoryMB: 4096, DiskGB: 50}
	if err := b.EnsureVM(context.Background(), spec); err != nil {
		t.Fatalf("EnsureVM: %v", err)
	}
	yamlPath := filepath.Join(b.dataDir, "lima.yaml")
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("expected lima.yaml at %s: %v", yamlPath, err)
	}
	for _, want := range []string{"cpus: 4", "memory: 4096MiB", "disk: 50GiB", "mounts: []", "system: false", "user: false"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("lima.yaml missing %q in:\n%s", want, data)
		}
	}
	if strings.Contains(string(data), "mountType") {
		t.Errorf("lima.yaml must not declare mountType (Lima rejects any value when mounts is empty); got:\n%s", data)
	}
	calls := r.callsSnapshot()
	if len(calls) != 3 {
		t.Fatalf("expected 3 calls, got %d: %+v", len(calls), calls)
	}
	if calls[2].args[0] != "create" || calls[2].args[2] != "--name=bolted" {
		t.Errorf("unexpected create invocation: %+v", calls[2])
	}
}

// TestEnsureVM_Idempotent verifies an existing instance short-circuits
// the create call.
func TestEnsureVM_Idempotent(t *testing.T) {
	r := &fakeRunner{
		responses: []fakeResponse{
			{stdout: []byte("limactl version 1.0.0\n")},
			{stdout: []byte(`{"name":"bolted","status":"Stopped"}` + "\n")},
		},
	}
	b := newTestBackend(t, r)
	if err := b.EnsureVM(context.Background(), backend.VMSpec{CPUs: 2, MemoryMB: 2048, DiskGB: 20}); err != nil {
		t.Fatalf("EnsureVM: %v", err)
	}
	calls := r.callsSnapshot()
	if len(calls) != 2 {
		t.Errorf("expected 2 calls (version + ls), got %d: %+v", len(calls), calls)
	}
}

// TestEnsureVM_RequireLimaError surfaces the requireLima failure.
func TestEnsureVM_RequireLimaError(t *testing.T) {
	r := &fakeRunner{responses: []fakeResponse{{err: errors.New("no lima")}}}
	b := newTestBackend(t, r)
	err := b.EnsureVM(context.Background(), backend.VMSpec{})
	if err == nil || !strings.Contains(err.Error(), "brew install lima") {
		t.Errorf("expected install hint, got: %v", err)
	}
}

// TestEnsureVM_MkdirError forces os.MkdirAll to fail by pointing
// dataDir at a path under an existing regular file.
func TestEnsureVM_MkdirError(t *testing.T) {
	tmp := t.TempDir()
	blocker := filepath.Join(tmp, "blocker")
	if err := os.WriteFile(blocker, []byte("file"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	r := &fakeRunner{responses: []fakeResponse{{stdout: []byte("ok")}}}
	b := NewWithOptions(Options{
		Name:    "bolted",
		DataDir: filepath.Join(blocker, "sub"),
		Runner:  r,
	})
	err := b.EnsureVM(context.Background(), backend.VMSpec{})
	if err == nil || !strings.Contains(err.Error(), "create data dir") {
		t.Errorf("expected mkdir error, got: %v", err)
	}
}

// TestEnsureVM_LoadForwardsError covers the load-forwards failure path
// by leaving the file with malformed JSON.
func TestEnsureVM_LoadForwardsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "portForwards.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	r := &fakeRunner{responses: []fakeResponse{{stdout: []byte("ok")}}}
	b := NewWithOptions(Options{Name: "bolted", DataDir: dir, Runner: r})
	err := b.EnsureVM(context.Background(), backend.VMSpec{})
	if err == nil || !strings.Contains(err.Error(), "load forwards") {
		t.Errorf("expected load-forwards error, got: %v", err)
	}
}

// TestEnsureVM_WriteYAMLError forces the yaml write to fail by making
// lima.yaml a directory.
func TestEnsureVM_WriteYAMLError(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "lima.yaml"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	r := &fakeRunner{responses: []fakeResponse{{stdout: []byte("ok")}}}
	b := NewWithOptions(Options{Name: "bolted", DataDir: dir, Runner: r})
	err := b.EnsureVM(context.Background(), backend.VMSpec{})
	if err == nil || !strings.Contains(err.Error(), "write lima.yaml") {
		t.Errorf("expected write error, got: %v", err)
	}
}

// TestEnsureVM_ListError surfaces a failure from `limactl ls --json`.
func TestEnsureVM_ListError(t *testing.T) {
	r := &fakeRunner{
		responses: []fakeResponse{
			{stdout: []byte("ok")},                 // version
			{err: errors.New("list boom")},         // ls
		},
	}
	b := newTestBackend(t, r)
	err := b.EnsureVM(context.Background(), backend.VMSpec{})
	if err == nil || !strings.Contains(err.Error(), "list vms") {
		t.Errorf("expected list error, got: %v", err)
	}
}

// TestEnsureVM_ParseListError forces the JSON decoder to fail.
func TestEnsureVM_ParseListError(t *testing.T) {
	r := &fakeRunner{
		responses: []fakeResponse{
			{stdout: []byte("ok")},
			{stdout: []byte("not json")},
		},
	}
	b := newTestBackend(t, r)
	err := b.EnsureVM(context.Background(), backend.VMSpec{})
	if err == nil || !strings.Contains(err.Error(), "parse vm list") {
		t.Errorf("expected parse error, got: %v", err)
	}
}

// TestEnsureVM_CreateError surfaces a failure from `limactl create`.
func TestEnsureVM_CreateError(t *testing.T) {
	r := &fakeRunner{
		responses: []fakeResponse{
			{stdout: []byte("ok")},
			{stdout: []byte("")},
			{err: errors.New("create boom")},
		},
	}
	b := newTestBackend(t, r)
	err := b.EnsureVM(context.Background(), backend.VMSpec{})
	if err == nil || !strings.Contains(err.Error(), "create vm") {
		t.Errorf("expected create error, got: %v", err)
	}
}

// TestStartVM_StartsWhenStopped covers the not-yet-running path.
func TestStartVM_StartsWhenStopped(t *testing.T) {
	r := &fakeRunner{
		responses: []fakeResponse{
			{stdout: []byte("ok")},                                                   // version
			{stdout: []byte(`{"name":"bolted","status":"Stopped"}` + "\n")},       // ls
			{stdout: []byte("started")},                                              // start
		},
	}
	b := newTestBackend(t, r)
	if err := b.StartVM(context.Background()); err != nil {
		t.Fatalf("StartVM: %v", err)
	}
	calls := r.callsSnapshot()
	if calls[2].args[0] != "start" || calls[2].args[1] != "bolted"{
		t.Errorf("unexpected start call: %+v", calls[2])
	}
}

// TestStartVM_NoOpWhenRunning covers the already-running branch.
func TestStartVM_NoOpWhenRunning(t *testing.T) {
	r := &fakeRunner{
		responses: []fakeResponse{
			{stdout: []byte("ok")},
			{stdout: []byte(`{"name":"bolted","status":"Running"}` + "\n")},
		},
	}
	b := newTestBackend(t, r)
	if err := b.StartVM(context.Background()); err != nil {
		t.Fatalf("StartVM: %v", err)
	}
	if len(r.callsSnapshot()) != 2 {
		t.Errorf("expected start to be skipped, got %d calls", len(r.callsSnapshot()))
	}
}

// TestStartVM_RequireLimaError surfaces the install hint.
func TestStartVM_RequireLimaError(t *testing.T) {
	r := &fakeRunner{responses: []fakeResponse{{err: errors.New("no lima")}}}
	b := newTestBackend(t, r)
	err := b.StartVM(context.Background())
	if err == nil || !strings.Contains(err.Error(), "brew install lima") {
		t.Errorf("expected install hint, got: %v", err)
	}
}

// TestStartVM_IsRunningError surfaces a failure from the IsRunning
// check.
func TestStartVM_IsRunningError(t *testing.T) {
	r := &fakeRunner{
		responses: []fakeResponse{
			{stdout: []byte("ok")},
			{err: errors.New("ls boom")},
		},
	}
	b := newTestBackend(t, r)
	err := b.StartVM(context.Background())
	if err == nil || !strings.Contains(err.Error(), "list vms") {
		t.Errorf("expected list error, got: %v", err)
	}
}

// TestStartVM_StartError surfaces a failure from `limactl start`.
func TestStartVM_StartError(t *testing.T) {
	r := &fakeRunner{
		responses: []fakeResponse{
			{stdout: []byte("ok")},
			{stdout: []byte(`{"name":"bolted","status":"Stopped"}` + "\n")},
			{err: errors.New("start boom")},
		},
	}
	b := newTestBackend(t, r)
	err := b.StartVM(context.Background())
	if err == nil || !strings.Contains(err.Error(), "start vm") {
		t.Errorf("expected start error, got: %v", err)
	}
}

// TestStopVM_StopsWhenRunning covers the running path.
func TestStopVM_StopsWhenRunning(t *testing.T) {
	r := &fakeRunner{
		responses: []fakeResponse{
			{stdout: []byte(`{"name":"bolted","status":"Running"}` + "\n")},
			{stdout: []byte("stopped")},
		},
	}
	b := newTestBackend(t, r)
	if err := b.StopVM(context.Background()); err != nil {
		t.Fatalf("StopVM: %v", err)
	}
	calls := r.callsSnapshot()
	if calls[1].args[0] != "stop" {
		t.Errorf("unexpected stop call: %+v", calls[1])
	}
}

// TestStopVM_NoOpWhenStopped covers the not-running branch.
func TestStopVM_NoOpWhenStopped(t *testing.T) {
	r := &fakeRunner{
		responses: []fakeResponse{
			{stdout: []byte(`{"name":"bolted","status":"Stopped"}` + "\n")},
		},
	}
	b := newTestBackend(t, r)
	if err := b.StopVM(context.Background()); err != nil {
		t.Fatalf("StopVM: %v", err)
	}
	if len(r.callsSnapshot()) != 1 {
		t.Errorf("expected stop to be skipped")
	}
}

// TestStopVM_IsRunningError covers the IsRunning failure branch.
func TestStopVM_IsRunningError(t *testing.T) {
	r := &fakeRunner{responses: []fakeResponse{{err: errors.New("ls boom")}}}
	b := newTestBackend(t, r)
	err := b.StopVM(context.Background())
	if err == nil || !strings.Contains(err.Error(), "list vms") {
		t.Errorf("expected list error, got: %v", err)
	}
}

// TestStopVM_StopError surfaces a failure from `limactl stop`.
func TestStopVM_StopError(t *testing.T) {
	r := &fakeRunner{
		responses: []fakeResponse{
			{stdout: []byte(`{"name":"bolted","status":"Running"}` + "\n")},
			{err: errors.New("stop boom")},
		},
	}
	b := newTestBackend(t, r)
	err := b.StopVM(context.Background())
	if err == nil || !strings.Contains(err.Error(), "stop vm") {
		t.Errorf("expected stop error, got: %v", err)
	}
}

// TestIsRunning_True covers the running branch via case-insensitive
// match on the status field.
func TestIsRunning_True(t *testing.T) {
	r := &fakeRunner{responses: []fakeResponse{{stdout: []byte(`{"name":"bolted","status":"running"}` + "\n")}}}
	b := newTestBackend(t, r)
	ok, err := b.IsRunning(context.Background())
	if err != nil {
		t.Fatalf("IsRunning: %v", err)
	}
	if !ok {
		t.Errorf("expected running")
	}
}

// TestIsRunning_FalseWhenMissing covers the missing-instance branch:
// the contract says "false, nil" not "error".
func TestIsRunning_FalseWhenMissing(t *testing.T) {
	r := &fakeRunner{responses: []fakeResponse{{stdout: []byte("")}}}
	b := newTestBackend(t, r)
	ok, err := b.IsRunning(context.Background())
	if err != nil {
		t.Fatalf("IsRunning: %v", err)
	}
	if ok {
		t.Errorf("expected not running")
	}
}

// TestIsRunning_FalseWhenStopped covers the explicit-stopped branch.
func TestIsRunning_FalseWhenStopped(t *testing.T) {
	r := &fakeRunner{responses: []fakeResponse{{stdout: []byte(`{"name":"bolted","status":"Stopped"}` + "\n")}}}
	b := newTestBackend(t, r)
	ok, err := b.IsRunning(context.Background())
	if err != nil {
		t.Fatalf("IsRunning: %v", err)
	}
	if ok {
		t.Errorf("expected not running")
	}
}

// TestIsRunning_Error surfaces a runner error from `limactl ls`.
func TestIsRunning_Error(t *testing.T) {
	r := &fakeRunner{responses: []fakeResponse{{err: errors.New("ls boom")}}}
	b := newTestBackend(t, r)
	_, err := b.IsRunning(context.Background())
	if err == nil || !strings.Contains(err.Error(), "list vms") {
		t.Errorf("expected list error, got: %v", err)
	}
}

// TestExec_Empty rejects an empty command up front rather than running
// limactl with no args.
func TestExec_Empty(t *testing.T) {
	r := &fakeRunner{}
	b := newTestBackend(t, r)
	res, err := b.Exec(context.Background(), nil, backend.ExecOpts{})
	if err == nil || !strings.Contains(err.Error(), "empty command") {
		t.Errorf("expected empty command error, got: %v", err)
	}
	if res.ExitCode != -1 {
		t.Errorf("expected ExitCode=-1, got %d", res.ExitCode)
	}
}

// TestExec_Success covers the happy path: limactl shell returns
// stdout, exit code is 0.
func TestExec_Success(t *testing.T) {
	r := &fakeRunner{responses: []fakeResponse{{stdout: []byte("hello\n")}}}
	b := newTestBackend(t, r)
	res, err := b.Exec(context.Background(), []string{"echo", "hello"}, backend.ExecOpts{})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if string(res.Stdout) != "hello\n" {
		t.Errorf("stdout: %q", res.Stdout)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode: %d", res.ExitCode)
	}
	call := r.callsSnapshot()[0]
	// shell + bolted + -- + sh + -c + body
	if call.args[0] != "shell" || call.args[1] != "bolted" || call.args[2] != "--" || call.args[3] != "sh" || call.args[4] != "-c" {
		t.Errorf("unexpected shell invocation: %+v", call)
	}
}

// TestExec_TTY pins that opts.TTY adds the -t flag in the right slot.
func TestExec_TTY(t *testing.T) {
	r := &fakeRunner{responses: []fakeResponse{{stdout: []byte("")}}}
	b := newTestBackend(t, r)
	_, err := b.Exec(context.Background(), []string{"sh"}, backend.ExecOpts{TTY: true})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	call := r.callsSnapshot()[0]
	if call.args[0] != "shell" || call.args[1] != "-t" || call.args[2] != "bolted"{
		t.Errorf("missing -t in: %+v", call)
	}
}

// TestExec_CwdAndEnv pins that cwd and env compose correctly inside
// the sh -c body.
func TestExec_CwdAndEnv(t *testing.T) {
	r := &fakeRunner{responses: []fakeResponse{{stdout: []byte("")}}}
	b := newTestBackend(t, r)
	_, err := b.Exec(context.Background(), []string{"pwd"}, backend.ExecOpts{
		Cwd: "/bolted/repos/foo",
		Env: []string{"FOO=bar", "BAZ=qux"},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	body := r.callsSnapshot()[0].args[5]
	for _, want := range []string{"FOO=bar", "BAZ=qux", "cd '/bolted/repos/foo'", "exec 'pwd'"} {
		if !strings.Contains(body, want) {
			t.Errorf("body %q missing %q", body, want)
		}
	}
}

// TestExec_Stdin pipes a payload and verifies the runner saw it.
func TestExec_Stdin(t *testing.T) {
	r := &fakeRunner{responses: []fakeResponse{{stdout: []byte("")}}}
	b := newTestBackend(t, r)
	_, err := b.Exec(context.Background(), []string{"cat"}, backend.ExecOpts{
		Stdin: strings.NewReader("payload"),
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if string(r.callsSnapshot()[0].stdin) != "payload" {
		t.Errorf("stdin not piped, got: %q", r.callsSnapshot()[0].stdin)
	}
}

// TestExec_NonZeroExit surfaces stderr and exit code via *exitError
// without returning an error to the caller (non-zero is data, not a
// fault).
func TestExec_NonZeroExit(t *testing.T) {
	r := &fakeRunner{responses: []fakeResponse{{
		stdout: []byte("out"),
		err:    &exitError{ExitCode: 7, Stderr: []byte("bad"), Cause: errors.New("exit status 7")},
	}}}
	b := newTestBackend(t, r)
	res, err := b.Exec(context.Background(), []string{"false"}, backend.ExecOpts{})
	if err != nil {
		t.Errorf("expected nil err on non-zero exit, got: %v", err)
	}
	if res.ExitCode != 7 || string(res.Stderr) != "bad" || string(res.Stdout) != "out" {
		t.Errorf("unexpected result: %+v", res)
	}
}

// TestExec_StartError covers the failed-to-start branch: a plain
// error (not *exitError) becomes ExitCode=-1 and is propagated.
func TestExec_StartError(t *testing.T) {
	r := &fakeRunner{responses: []fakeResponse{{err: errors.New("fork failed")}}}
	b := newTestBackend(t, r)
	res, err := b.Exec(context.Background(), []string{"x"}, backend.ExecOpts{})
	if err == nil || !strings.Contains(err.Error(), "exec") {
		t.Errorf("expected exec error, got: %v", err)
	}
	if res.ExitCode != -1 {
		t.Errorf("expected ExitCode=-1, got %d", res.ExitCode)
	}
}

// TestForwardPort_PersistsAndCalls covers the happy path and asserts
// the tracking file plus the best-effort limactl forward invocation.
func TestForwardPort_PersistsAndCalls(t *testing.T) {
	r := &fakeRunner{responses: []fakeResponse{{stdout: []byte("ok")}}}
	b := newTestBackend(t, r)
	if err := b.ForwardPort(context.Background(), 3000, 3000); err != nil {
		t.Fatalf("ForwardPort: %v", err)
	}
	forwards, err := loadForwards(b.dataDir)
	if err != nil {
		t.Fatalf("loadForwards: %v", err)
	}
	if len(forwards) != 1 || forwards[0].GuestPort != 3000 || forwards[0].HostPort != 3000 {
		t.Errorf("unexpected forwards: %+v", forwards)
	}
	call := r.callsSnapshot()[0]
	if call.args[0] != "forward" || call.args[2] != "3000" {
		t.Errorf("unexpected limactl call: %+v", call)
	}
}

// TestForwardPort_Conflict rejects rebinding a host port to a different
// guest port.
func TestForwardPort_Conflict(t *testing.T) {
	r := &fakeRunner{responses: []fakeResponse{{stdout: []byte("ok")}, {stdout: []byte("ok")}}}
	b := newTestBackend(t, r)
	if err := b.ForwardPort(context.Background(), 3000, 3000); err != nil {
		t.Fatalf("first ForwardPort: %v", err)
	}
	err := b.ForwardPort(context.Background(), 4000, 3000)
	if err == nil || !strings.Contains(err.Error(), "already forwarded") {
		t.Errorf("expected conflict error, got: %v", err)
	}
}

// TestForwardPort_RebindSameGuest is the no-conflict re-add: same
// host+guest pair should succeed and stay a single entry.
func TestForwardPort_RebindSameGuest(t *testing.T) {
	r := &fakeRunner{responses: []fakeResponse{{stdout: []byte("ok")}, {stdout: []byte("ok")}}}
	b := newTestBackend(t, r)
	if err := b.ForwardPort(context.Background(), 3000, 3000); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := b.ForwardPort(context.Background(), 3000, 3000); err != nil {
		t.Fatalf("second: %v", err)
	}
	forwards, _ := loadForwards(b.dataDir)
	if len(forwards) != 1 {
		t.Errorf("expected 1 forward, got %d", len(forwards))
	}
}

// TestForwardPort_MkdirError forces the data dir mkdir to fail.
func TestForwardPort_MkdirError(t *testing.T) {
	tmp := t.TempDir()
	blocker := filepath.Join(tmp, "blocker")
	if err := os.WriteFile(blocker, []byte("file"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	b := NewWithOptions(Options{DataDir: filepath.Join(blocker, "sub"), Runner: &fakeRunner{}})
	err := b.ForwardPort(context.Background(), 1, 1)
	if err == nil || !strings.Contains(err.Error(), "create data dir") {
		t.Errorf("expected mkdir error, got: %v", err)
	}
}

// TestForwardPort_LoadError covers the load-forwards failure branch.
func TestForwardPort_LoadError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "portForwards.json"), []byte("{bad"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	b := NewWithOptions(Options{DataDir: dir, Runner: &fakeRunner{}})
	err := b.ForwardPort(context.Background(), 1, 1)
	if err == nil || !strings.Contains(err.Error(), "load forwards") {
		t.Errorf("expected load error, got: %v", err)
	}
}

// TestForwardPort_SaveError forces saveForwards to fail by writing
// the tracking file with a read-only mode and then chmodding the
// parent directory so the rewrite cannot succeed.
func TestForwardPort_SaveError(t *testing.T) {
	dir := t.TempDir()
	// Start with no existing tracking file so loadForwards yields the
	// missing-file branch (empty slice, nil error). Then make the dir
	// non-writable so the subsequent WriteFile inside saveForwards
	// fails.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	b := NewWithOptions(Options{DataDir: dir, Runner: &fakeRunner{}})
	err := b.ForwardPort(context.Background(), 1, 1)
	if err == nil || !strings.Contains(err.Error(), "persist forwards") {
		t.Errorf("expected persist error, got: %v", err)
	}
}

// TestUnforwardPort_Removes covers the happy path: a known host port
// is removed and the tracking file shrinks.
func TestUnforwardPort_Removes(t *testing.T) {
	r := &fakeRunner{matcher: func([]string) fakeResponse { return fakeResponse{} }}
	b := newTestBackend(t, r)
	if err := saveForwards(b.dataDir, []portForward{{GuestPort: 3000, HostPort: 3000}, {GuestPort: 4000, HostPort: 4000}}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := b.UnforwardPort(context.Background(), 3000); err != nil {
		t.Fatalf("UnforwardPort: %v", err)
	}
	forwards, _ := loadForwards(b.dataDir)
	if len(forwards) != 1 || forwards[0].HostPort != 4000 {
		t.Errorf("unexpected forwards after unforward: %+v", forwards)
	}
}

// TestUnforwardPort_NoOp leaves the file untouched and does not call
// limactl when the host port is unknown.
func TestUnforwardPort_NoOp(t *testing.T) {
	r := &fakeRunner{}
	b := newTestBackend(t, r)
	if err := b.UnforwardPort(context.Background(), 9999); err != nil {
		t.Fatalf("UnforwardPort: %v", err)
	}
	if len(r.callsSnapshot()) != 0 {
		t.Errorf("expected zero limactl calls for unknown port")
	}
}

// TestUnforwardPort_LoadError covers the load-forwards failure branch.
func TestUnforwardPort_LoadError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "portForwards.json"), []byte("{bad"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	b := NewWithOptions(Options{DataDir: dir, Runner: &fakeRunner{}})
	err := b.UnforwardPort(context.Background(), 1)
	if err == nil || !strings.Contains(err.Error(), "load forwards") {
		t.Errorf("expected load error, got: %v", err)
	}
}

// TestUnforwardPort_SaveError forces saveForwards to fail. We seed a
// tracking file, then make the file itself read-only AND its parent
// directory non-writable so the WriteFile in saveForwards (which
// O_TRUNCs the existing file) cannot succeed.
func TestUnforwardPort_SaveError(t *testing.T) {
	dir := t.TempDir()
	if err := saveForwards(dir, []portForward{{GuestPort: 1, HostPort: 1}}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.Chmod(filepath.Join(dir, forwardsFileName), 0o400); err != nil {
		t.Fatalf("chmod file: %v", err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(dir, 0o755)
		_ = os.Chmod(filepath.Join(dir, forwardsFileName), 0o644)
	})
	b := NewWithOptions(Options{DataDir: dir, Runner: &fakeRunner{}})
	err := b.UnforwardPort(context.Background(), 1)
	if err == nil || !strings.Contains(err.Error(), "persist forwards") {
		t.Errorf("expected persist error, got: %v", err)
	}
}

// TestDeleteVM_Success covers the happy path: --force flag is present.
func TestDeleteVM_Success(t *testing.T) {
	r := &fakeRunner{responses: []fakeResponse{{stdout: []byte("deleted")}}}
	b := newTestBackend(t, r)
	if err := b.DeleteVM(context.Background()); err != nil {
		t.Fatalf("DeleteVM: %v", err)
	}
	call := r.callsSnapshot()[0]
	if call.args[0] != "delete" || call.args[1] != "bolted" || call.args[2] != "--force" {
		t.Errorf("unexpected delete invocation: %+v", call)
	}
}

// TestDeleteVM_Error surfaces a failure from `limactl delete`.
func TestDeleteVM_Error(t *testing.T) {
	r := &fakeRunner{responses: []fakeResponse{{err: errors.New("delete boom")}}}
	b := newTestBackend(t, r)
	err := b.DeleteVM(context.Background())
	if err == nil || !strings.Contains(err.Error(), "delete vm") {
		t.Errorf("expected delete error, got: %v", err)
	}
}

// TestShellQuote_Edge documents the quoting rules: empty string maps
// to '', embedded single quotes are escaped via '\''.
func TestShellQuote_Edge(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "''"},
		{"abc", "'abc'"},
		{"a'b", `'a'\''b'`},
	}
	for _, c := range cases {
		got := shellQuote(c.in)
		if got != c.want {
			t.Errorf("shellQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestBuildShellCommand_NoCwd covers the bare exec branch (no cd).
func TestBuildShellCommand_NoCwd(t *testing.T) {
	got := buildShellCommand([]string{"echo", "hi"}, backend.ExecOpts{})
	if !strings.HasPrefix(got, "exec ") {
		t.Errorf("expected exec prefix, got %q", got)
	}
}

// TestParseInstances_BadJSON exercises the malformed-line branch.
func TestParseInstances_BadJSON(t *testing.T) {
	_, err := parseInstances([]byte("{not json"))
	if err == nil {
		t.Fatal("expected parse error")
	}
}

// TestParseInstances_Empty pins that empty input is a valid no-op.
func TestParseInstances_Empty(t *testing.T) {
	out, err := parseInstances(nil)
	if err != nil {
		t.Fatalf("parseInstances: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty, got %d", len(out))
	}
}

// TestParseInstances_Multi covers >1 instance, common in real output.
func TestParseInstances_Multi(t *testing.T) {
	raw := []byte(`{"name":"a","status":"Running"}` + "\n" + `{"name":"b","status":"Stopped"}` + "\n")
	out, err := parseInstances(raw)
	if err != nil {
		t.Fatalf("parseInstances: %v", err)
	}
	if len(out) != 2 || out[0].Name != "a" || out[1].Status != "Stopped" {
		t.Errorf("unexpected parse: %+v", out)
	}
}

// TestLoadForwards_Missing reports an empty slice for a missing file
// (not an error).
func TestLoadForwards_Missing(t *testing.T) {
	dir := t.TempDir()
	out, err := loadForwards(dir)
	if err != nil {
		t.Fatalf("loadForwards: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty, got %d", len(out))
	}
}

// TestLoadForwards_Empty reports an empty slice for an empty file.
func TestLoadForwards_Empty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, forwardsFileName), nil, 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	out, err := loadForwards(dir)
	if err != nil {
		t.Fatalf("loadForwards: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty, got %d", len(out))
	}
}

// TestLoadForwards_ReadError forces a non-NotExist read error by
// making the path a directory.
func TestLoadForwards_ReadError(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, forwardsFileName), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err := loadForwards(dir)
	if err == nil || !strings.Contains(err.Error(), "read forwards") {
		t.Errorf("expected read error, got: %v", err)
	}
}

// TestLoadForwards_BadJSON exercises the malformed-JSON branch.
func TestLoadForwards_BadJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, forwardsFileName), []byte("{bad"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err := loadForwards(dir)
	if err == nil || !strings.Contains(err.Error(), "parse forwards") {
		t.Errorf("expected parse error, got: %v", err)
	}
}

// TestWriteLimaYAML_MarshalError exercises the otherwise-unreachable
// yaml.Marshal failure branch by swapping the injected marshaller.
func TestWriteLimaYAML_MarshalError(t *testing.T) {
	orig := yamlMarshal
	yamlMarshal = func(any) ([]byte, error) { return nil, errors.New("yaml boom") }
	t.Cleanup(func() { yamlMarshal = orig })
	err := writeLimaYAML(filepath.Join(t.TempDir(), "lima.yaml"), backend.VMSpec{}, nil)
	if err == nil || !strings.Contains(err.Error(), "marshal lima.yaml") {
		t.Errorf("expected marshal error, got: %v", err)
	}
}

// TestSaveForwards_MarshalError mirrors the YAML marshal test for the
// JSON path used by saveForwards.
func TestSaveForwards_MarshalError(t *testing.T) {
	orig := jsonMarshalIndent
	jsonMarshalIndent = func(any, string, string) ([]byte, error) { return nil, errors.New("json boom") }
	t.Cleanup(func() { jsonMarshalIndent = orig })
	err := saveForwards(t.TempDir(), []portForward{{HostPort: 1}})
	if err == nil || !strings.Contains(err.Error(), "marshal forwards") {
		t.Errorf("expected marshal error, got: %v", err)
	}
}

// TestWriteLimaYAML_WithForwards pins the loop body that copies
// portForwards into the rendered YAML.
func TestWriteLimaYAML_WithForwards(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lima.yaml")
	err := writeLimaYAML(path, backend.VMSpec{CPUs: 1, MemoryMB: 512, DiskGB: 5}, []portForward{
		{GuestPort: 3000, HostPort: 3000},
		{GuestPort: 8080, HostPort: 18080},
	})
	if err != nil {
		t.Fatalf("writeLimaYAML: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, want := range []string{"guestPort: 3000", "hostPort: 3000", "guestPort: 8080", "hostPort: 18080"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("rendered yaml missing %q:\n%s", want, data)
		}
	}
}

// TestSaveForwards_MkdirError exercises the dataDir mkdir failure
// branch by pointing saveForwards at a path under an existing file.
func TestSaveForwards_MkdirError(t *testing.T) {
	tmp := t.TempDir()
	blocker := filepath.Join(tmp, "blocker")
	if err := os.WriteFile(blocker, []byte("file"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	err := saveForwards(filepath.Join(blocker, "sub"), []portForward{{HostPort: 1}})
	if err == nil || !strings.Contains(err.Error(), "create dir") {
		t.Errorf("expected create dir error, got: %v", err)
	}
}

// TestSaveForwards_Sorted pins the diff-friendly host-port sort.
func TestSaveForwards_Sorted(t *testing.T) {
	dir := t.TempDir()
	if err := saveForwards(dir, []portForward{{HostPort: 9000}, {HostPort: 3000}}); err != nil {
		t.Fatalf("saveForwards: %v", err)
	}
	out, _ := loadForwards(dir)
	if len(out) != 2 || out[0].HostPort != 3000 || out[1].HostPort != 9000 {
		t.Errorf("not sorted: %+v", out)
	}
}

// TestUpsertForward_Replace pins that an existing host port is
// rebound (not duplicated).
func TestUpsertForward_Replace(t *testing.T) {
	out := upsertForward([]portForward{{HostPort: 1, GuestPort: 10}}, portForward{HostPort: 1, GuestPort: 20})
	if len(out) != 1 || out[0].GuestPort != 20 {
		t.Errorf("expected rebind, got %+v", out)
	}
}

// TestRemoveForward_Missing leaves the slice unchanged when no entry
// matches.
func TestRemoveForward_Missing(t *testing.T) {
	in := []portForward{{HostPort: 1}, {HostPort: 2}}
	out := removeForward(in, 999)
	if len(out) != 2 {
		t.Errorf("expected unchanged, got %+v", out)
	}
}

// TestRealRunner_Smoke exercises the realRunner indirection on a
// trivial command so its construction site is covered. The test does
// NOT depend on Lima.
func TestRealRunner_Smoke(t *testing.T) {
	r := realRunner{}
	out, err := r.Run(context.Background(), "sh", "-c", "echo smoke")
	if err != nil {
		t.Fatalf("realRunner.Run: %v", err)
	}
	if !strings.Contains(string(out), "smoke") {
		t.Errorf("unexpected stdout: %q", out)
	}
}

// TestRealRunner_StdinSmoke exercises RunWithStdin.
func TestRealRunner_StdinSmoke(t *testing.T) {
	r := realRunner{}
	out, err := r.RunWithStdin(context.Background(), strings.NewReader("hello"), "cat")
	if err != nil {
		t.Fatalf("realRunner.RunWithStdin: %v", err)
	}
	if string(out) != "hello" {
		t.Errorf("unexpected stdout: %q", out)
	}
}

// TestRealRunner_ExitError pins the exitError wrapping behaviour: a
// non-zero exit yields *exitError with the captured stderr/exit code.
func TestRealRunner_ExitError(t *testing.T) {
	r := realRunner{}
	_, err := r.Run(context.Background(), "sh", "-c", "echo oops 1>&2; exit 3")
	if err == nil {
		t.Fatal("expected error")
	}
	var ee *exitError
	if !errors.As(err, &ee) {
		t.Fatalf("expected *exitError, got %T: %v", err, err)
	}
	if ee.ExitCode != 3 {
		t.Errorf("exit code: %d", ee.ExitCode)
	}
	if !strings.Contains(string(ee.Stderr), "oops") {
		t.Errorf("stderr: %q", ee.Stderr)
	}
	// Cover exitError.Error / Unwrap.
	if ee.Error() == "" {
		t.Error("expected non-empty Error()")
	}
	if errors.Unwrap(ee) == nil {
		t.Error("expected non-nil Unwrap()")
	}
}

// TestRealRunner_StartError surfaces a non-exit failure (binary
// missing) which must NOT come back as *exitError.
func TestRealRunner_StartError(t *testing.T) {
	r := realRunner{}
	_, err := r.Run(context.Background(), "/no/such/binary/anywhere", "x")
	if err == nil {
		t.Fatal("expected error")
	}
	var ee *exitError
	if errors.As(err, &ee) {
		t.Errorf("did not expect *exitError for start failure, got: %v", err)
	}
}
