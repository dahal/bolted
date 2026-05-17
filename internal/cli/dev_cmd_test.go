package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dahal/bolted/internal/backend"
	"github.com/dahal/bolted/internal/backend/mock"
	"github.com/dahal/bolted/internal/devcontainer"
	"github.com/dahal/bolted/internal/state"
)

// ---- fake devcontainer.Runner ---------------------------------------------

// fakeRunner is a recording devcontainer.Runner. Each method honours
// its err* / result* knob; defaults are success-with-zero-output.
type fakeRunner struct {
	mu sync.Mutex

	upID     string
	upErr    error
	upCalls  []string // repoPaths Up was called with

	downErr   error
	downCalls []string // containerIDs Down was called with

	execResult devcontainer.ExecResult
	execErr    error
	// execResultByCmd lets tests vary the result per-cmd (matched on
	// the first token). Falls back to execResult when nothing
	// matches.
	execResultByCmd map[string]devcontainer.ExecResult
	execCalls       []fakeRunnerExecCall

	buildErr error
}

type fakeRunnerExecCall struct {
	containerID string
	cmd         []string
	opts        devcontainer.ExecOpts
}

func (f *fakeRunner) Up(_ context.Context, repoPath string, _ devcontainer.UpOpts) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upCalls = append(f.upCalls, repoPath)
	if f.upErr != nil {
		return "", f.upErr
	}
	if f.upID == "" {
		return "container-id-" + filepath.Base(repoPath), nil
	}
	return f.upID, nil
}

func (f *fakeRunner) Down(_ context.Context, containerID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.downCalls = append(f.downCalls, containerID)
	return f.downErr
}

func (f *fakeRunner) Exec(_ context.Context, containerID string, cmd []string, opts devcontainer.ExecOpts) (devcontainer.ExecResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execCalls = append(f.execCalls, fakeRunnerExecCall{containerID: containerID, cmd: append([]string(nil), cmd...), opts: opts})
	if f.execResultByCmd != nil && len(cmd) > 0 {
		if res, ok := f.execResultByCmd[cmd[0]]; ok {
			return res, f.execErr
		}
	}
	return f.execResult, f.execErr
}

func (f *fakeRunner) Build(_ context.Context, _ string) error { return f.buildErr }

// withRunnerStub swaps newRunnerFn for the duration of one test and
// neutralises the trust gate. Existing dev/exec tests never had to deal
// with spec 18's prompt; opting into the real gate is done by individual
// tests that re-assign trustGateFn after calling this helper.
func withRunnerStub(t *testing.T, fr *fakeRunner) {
	t.Helper()
	orig := newRunnerFn
	t.Cleanup(func() { newRunnerFn = orig })
	newRunnerFn = func(_ backend.Backend, _ devcontainer.Options) devcontainer.Runner { return fr }

	origTrust := trustGateFn
	t.Cleanup(func() { trustGateFn = origTrust })
	trustGateFn = func(context.Context, backend.Backend, string, string, io.Reader, io.Writer, bool) error { return nil }
}

// withStateDirStub points the state-dir helper at a temp directory.
func withStateDirStub(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig := stateDirFn
	t.Cleanup(func() { stateDirFn = orig })
	stateDirFn = func() string { return dir }
	return dir
}

// ---- containers.json helpers ----------------------------------------------

func TestContainersPath_UsesStateDir(t *testing.T) {
	orig := stateDirFn
	t.Cleanup(func() { stateDirFn = orig })
	stateDirFn = func() string { return "/tmp/x/state" }
	want := filepath.Join("/tmp/x/state", "containers.json")
	if got := containersPath(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStateDirFn_DefaultIsBoltedState(t *testing.T) {
	origWS := boltedDirFn
	t.Cleanup(func() { boltedDirFn = origWS })
	boltedDirFn = func() string { return "/tmp/bolt" }
	if got := stateDirFn(); got != "/tmp/bolt/state" {
		t.Errorf("got %q, want /tmp/bolt/state", got)
	}
}

func TestReadContainers_MissingFileReturnsEmpty(t *testing.T) {
	withStateDirStub(t)
	m, err := readContainers()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("expected empty map, got %v", m)
	}
}

func TestReadContainers_ParsesStringEntries(t *testing.T) {
	dir := withStateDirStub(t)
	raw := `{"api":"abc","web":"def","bogus":42}`
	if err := os.WriteFile(filepath.Join(dir, "containers.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := readContainers()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if m["api"] != "abc" || m["web"] != "def" {
		t.Errorf("missing string entries: %v", m)
	}
	if _, ok := m["bogus"]; ok {
		t.Errorf("non-string entry should be skipped, got %v", m)
	}
}

func TestReadContainers_ParseError(t *testing.T) {
	dir := withStateDirStub(t)
	if err := os.WriteFile(filepath.Join(dir, "containers.json"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readContainers(); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestWriteContainers_RoundTrips(t *testing.T) {
	withStateDirStub(t)
	in := map[string]string{"api": "abc", "web": "def"}
	if err := writeContainers(in); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	out, err := readContainers()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if out["api"] != "abc" || out["web"] != "def" {
		t.Errorf("round-trip mismatch: %v", out)
	}
}

func TestRecordAndForgetContainer(t *testing.T) {
	withStateDirStub(t)
	if err := recordContainer("api", "abc"); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := recordContainer("web", "def"); err != nil {
		t.Fatalf("record: %v", err)
	}
	m, _ := readContainers()
	if m["api"] != "abc" || m["web"] != "def" {
		t.Errorf("expected both entries, got %v", m)
	}
	if err := forgetContainer("api"); err != nil {
		t.Fatalf("forget: %v", err)
	}
	m, _ = readContainers()
	if _, ok := m["api"]; ok {
		t.Errorf("expected api to be gone, got %v", m)
	}
	// Forgetting a non-existent entry is a no-op.
	if err := forgetContainer("nope"); err != nil {
		t.Errorf("forget non-existent: %v", err)
	}
}

func TestRecordContainer_ReadError(t *testing.T) {
	dir := withStateDirStub(t)
	if err := os.WriteFile(filepath.Join(dir, "containers.json"), []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recordContainer("api", "abc"); err == nil {
		t.Fatal("expected read error to bubble up")
	}
}

func TestForgetContainer_ReadError(t *testing.T) {
	dir := withStateDirStub(t)
	if err := os.WriteFile(filepath.Join(dir, "containers.json"), []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := forgetContainer("api"); err == nil {
		t.Fatal("expected read error to bubble up")
	}
}

func TestStoredContainerID(t *testing.T) {
	withStateDirStub(t)
	if id, err := storedContainerID("absent"); err != nil || id != "" {
		t.Errorf("absent: got %q,%v", id, err)
	}
	_ = recordContainer("api", "abc")
	if id, err := storedContainerID("api"); err != nil || id != "abc" {
		t.Errorf("present: got %q,%v", id, err)
	}
}

func TestStoredContainerID_ReadError(t *testing.T) {
	dir := withStateDirStub(t)
	if err := os.WriteFile(filepath.Join(dir, "containers.json"), []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := storedContainerID("api"); err == nil {
		t.Fatal("expected error")
	}
}

func TestRepoPath(t *testing.T) {
	if got := repoPath("api"); got != "/bolted/repos/api" {
		t.Errorf("got %q", got)
	}
}

// ---- requireUnlockedBackend -----------------------------------------------

func TestRequireUnlockedBackend_NotInitialised(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	withStatStub(t, statMissing)
	var stderr bytes.Buffer
	_, _, err := requireUnlockedBackend(context.Background(), &stderr)
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != exitLocked {
		t.Errorf("expected exit %d, got %d", exitLocked, code)
	}
	if !strings.Contains(stderr.String(), "bolt init") {
		t.Errorf("expected hint, got: %q", stderr.String())
	}
}

func TestRequireUnlockedBackend_StatGenericError(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	want := errors.New("io error")
	withStatStub(t, func(string) (os.FileInfo, error) { return nil, want })
	_, _, err := requireUnlockedBackend(context.Background(), io.Discard)
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped stat err, got %v", err)
	}
}

func TestRequireUnlockedBackend_LoadConfigFails(t *testing.T) {
	want := errors.New("bad yaml")
	s := &lifecycleStubs{cfgErr: want}
	s.install(t)
	withStatStub(t, statExists)
	_, _, err := requireUnlockedBackend(context.Background(), io.Discard)
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped load err, got %v", err)
	}
}

func TestRequireUnlockedBackend_BackendInitFails(t *testing.T) {
	want := errors.New("be fail")
	s := &lifecycleStubs{backendErr: want}
	s.install(t)
	withStatStub(t, statExists)
	_, _, err := requireUnlockedBackend(context.Background(), io.Discard)
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped backend err, got %v", err)
	}
}

func TestRequireUnlockedBackend_IsRunningFails(t *testing.T) {
	want := errors.New("is running")
	s := &lifecycleStubs{mockBE: mock.New()}
	s.mockBE.ErrIsRunning = want
	s.install(t)
	withStatStub(t, statExists)
	_, _, err := requireUnlockedBackend(context.Background(), io.Discard)
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped IsRunning err, got %v", err)
	}
}

func TestRequireUnlockedBackend_VMNotRunning(t *testing.T) {
	s := &lifecycleStubs{mockBE: mock.New()}
	s.mockBE.IsRunningResult = false
	s.install(t)
	withStatStub(t, statExists)
	var stderr bytes.Buffer
	_, _, err := requireUnlockedBackend(context.Background(), &stderr)
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != exitLocked {
		t.Errorf("expected exit %d, got %d", exitLocked, code)
	}
	if !strings.Contains(stderr.String(), "bolt unlock") {
		t.Errorf("expected unlock hint: %q", stderr.String())
	}
}

func TestRequireUnlockedBackend_Locked(t *testing.T) {
	scripted := &scriptedBackend{
		Mock:       mock.New(),
		execScript: []backend.ExecResult{{ExitCode: 2}}, // ls probe fails
	}
	scripted.Mock.IsRunningResult = true
	s := &lifecycleStubs{}
	s.install(t)
	newBackendFn = func(_ backend.Config) (backend.Backend, error) { return scripted, nil }
	withStatStub(t, statExists)
	var stderr bytes.Buffer
	_, _, err := requireUnlockedBackend(context.Background(), &stderr)
	if err == nil {
		t.Fatal("expected locked error")
	}
	if code := exitCodeFromError(err); code != exitLocked {
		t.Errorf("expected exit %d, got %d", exitLocked, code)
	}
}

func TestRequireUnlockedBackend_HappyPath(t *testing.T) {
	scripted := &scriptedBackend{
		Mock:       mock.New(),
		execScript: []backend.ExecResult{{ExitCode: 0}},
	}
	scripted.Mock.IsRunningResult = true
	s := &lifecycleStubs{}
	s.install(t)
	newBackendFn = func(_ backend.Config) (backend.Backend, error) { return scripted, nil }
	withStatStub(t, statExists)
	b, cfg, err := requireUnlockedBackend(context.Background(), io.Discard)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if b == nil || cfg == nil {
		t.Errorf("expected backend + config, got %v %v", b, cfg)
	}
}

// ---- repoExists / requireRepo ---------------------------------------------

func TestRepoExists_True(t *testing.T) {
	scripted := &scriptedBackend{
		Mock:       mock.New(),
		execScript: []backend.ExecResult{{ExitCode: 0}},
	}
	ok, err := repoExists(context.Background(), scripted, "api")
	if err != nil || !ok {
		t.Errorf("expected ok=true err=nil, got %v %v", ok, err)
	}
}

func TestRepoExists_False(t *testing.T) {
	scripted := &scriptedBackend{
		Mock:       mock.New(),
		execScript: []backend.ExecResult{{ExitCode: 1}},
	}
	ok, err := repoExists(context.Background(), scripted, "missing")
	if err != nil || ok {
		t.Errorf("expected ok=false err=nil, got %v %v", ok, err)
	}
}

func TestRepoExists_ExecError(t *testing.T) {
	scripted := &scriptedBackend{
		Mock:     mock.New(),
		execErrs: []error{errors.New("boom")},
	}
	_, err := repoExists(context.Background(), scripted, "api")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRequireRepo_NotFoundExits5(t *testing.T) {
	scripted := &scriptedBackend{
		Mock:       mock.New(),
		execScript: []backend.ExecResult{{ExitCode: 1}},
	}
	var stderr bytes.Buffer
	err := requireRepo(context.Background(), scripted, &stderr, "missing")
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != exitRepoNotFound {
		t.Errorf("expected exit %d, got %d", exitRepoNotFound, code)
	}
	if !strings.Contains(stderr.String(), `"missing"`) {
		t.Errorf("expected friendly msg, got: %q", stderr.String())
	}
}

func TestRequireRepo_HappyPath(t *testing.T) {
	scripted := &scriptedBackend{
		Mock:       mock.New(),
		execScript: []backend.ExecResult{{ExitCode: 0}},
	}
	if err := requireRepo(context.Background(), scripted, io.Discard, "api"); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestRequireRepo_ExecErrorPropagates(t *testing.T) {
	scripted := &scriptedBackend{
		Mock:     mock.New(),
		execErrs: []error{errors.New("boom")},
	}
	err := requireRepo(context.Background(), scripted, io.Discard, "api")
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---- runningContainerIDs --------------------------------------------------

func TestRunningContainerIDs_ParsesOutput(t *testing.T) {
	scripted := &scriptedBackend{
		Mock:       mock.New(),
		execScript: []backend.ExecResult{{ExitCode: 0, Stdout: []byte("abc\ndef\n  \n\tghi\n")}},
	}
	ids, err := runningContainerIDs(context.Background(), scripted)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !ids["abc"] || !ids["def"] || !ids["ghi"] {
		t.Errorf("missing entries: %v", ids)
	}
}

func TestRunningContainerIDs_ExecError(t *testing.T) {
	scripted := &scriptedBackend{
		Mock:     mock.New(),
		execErrs: []error{errors.New("boom")},
	}
	if _, err := runningContainerIDs(context.Background(), scripted); err == nil {
		t.Fatal("expected error")
	}
}

func TestRunningContainerIDs_NonZeroExit(t *testing.T) {
	scripted := &scriptedBackend{
		Mock:       mock.New(),
		execScript: []backend.ExecResult{{ExitCode: 1}},
	}
	ids, err := runningContainerIDs(context.Background(), scripted)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected empty map, got %v", ids)
	}
}

// ---- runDev ---------------------------------------------------------------

// devSetup wires lifecycleStubs + a scripted backend + a fake runner +
// a state dir for the dev / exec / stop / rm / ls happy paths.
type devSetup struct {
	scripted *scriptedBackend
	runner   *fakeRunner
	stateDir string
}

// installDevSetup installs all stubs for an end-to-end bolt dev test. The
// caller can mutate scripted/runner before calling runDev / runExec / …
func installDevSetup(t *testing.T, execScript []backend.ExecResult, execErrs []error) *devSetup {
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
	dir := withStateDirStub(t)
	fr := &fakeRunner{}
	withRunnerStub(t, fr)
	return &devSetup{scripted: scripted, runner: fr, stateDir: dir}
}

func TestRunDev_RequireUnlockedFails(t *testing.T) {
	// Easiest path: Bolted not initialised.
	s := &lifecycleStubs{}
	s.install(t)
	withStatStub(t, statMissing)
	err := runDev(context.Background(), io.Discard, io.Discard, "api", nil, devOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != exitLocked {
		t.Errorf("expected exit %d, got %d", exitLocked, code)
	}
}

func TestRunDev_RepoNotFound(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0}, // unlocked probe
		{ExitCode: 1}, // test -d
	}, nil)
	_ = ds
	var stderr bytes.Buffer
	err := runDev(context.Background(), io.Discard, &stderr, "missing", nil, devOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != exitRepoNotFound {
		t.Errorf("expected exit %d, got %d", exitRepoNotFound, code)
	}
}

func TestRunDev_StoredIDReadError(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	// Corrupt containers.json so storedContainerID errors.
	if err := os.WriteFile(filepath.Join(ds.stateDir, "containers.json"), []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runDev(context.Background(), io.Discard, io.Discard, "api", nil, devOptions{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestRunDev_UpFails(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	ds.runner.upErr = errors.New("up failed")
	err := runDev(context.Background(), io.Discard, io.Discard, "api", nil, devOptions{})
	if err == nil || !strings.Contains(err.Error(), "devcontainer up") {
		t.Errorf("expected up error, got %v", err)
	}
}

func TestRunDev_RecordContainerError(t *testing.T) {
	// Force writeContainers to fail without breaking the prior
	// storedContainerID read. We seed an empty containers.json, then
	// chmod the state dir read-only so the temp-file creation step
	// of the atomic write fails.
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	if err := os.WriteFile(filepath.Join(ds.stateDir, "containers.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ds.stateDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ds.stateDir, 0o700) })
	err := runDev(context.Background(), io.Discard, io.Discard, "api", nil, devOptions{})
	if err == nil || !strings.Contains(err.Error(), "persist container id") {
		t.Errorf("expected persist error, got %v", err)
	}
}

func TestRunDev_OneShotCommandHappyPath(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0}, // unlocked probe
		{ExitCode: 0}, // test -d
	}, nil)
	ds.runner.execResult = devcontainer.ExecResult{Stdout: []byte("ok\n"), Stderr: []byte("warn\n")}
	var stdout, stderr bytes.Buffer
	if err := runDev(context.Background(), &stdout, &stderr, "api", []string{"npm", "test"}, devOptions{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(stdout.String(), "ok") {
		t.Errorf("expected stdout, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "warn") {
		t.Errorf("expected stderr, got %q", stderr.String())
	}
	if len(ds.runner.execCalls) != 1 {
		t.Fatalf("expected 1 exec, got %d", len(ds.runner.execCalls))
	}
	call := ds.runner.execCalls[0]
	if call.cmd[0] != "npm" || call.cmd[1] != "test" {
		t.Errorf("unexpected cmd: %v", call.cmd)
	}
	if call.opts.TTY {
		t.Errorf("one-shot exec should not request a TTY")
	}
}

func TestRunDev_OneShotCommandExecError(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	ds.runner.execErr = errors.New("boom")
	err := runDev(context.Background(), io.Discard, io.Discard, "api", []string{"npm"}, devOptions{})
	if err == nil || !strings.Contains(err.Error(), "exec in container") {
		t.Errorf("expected exec error, got %v", err)
	}
}

func TestRunDev_OneShotCommandNonZeroExitPropagates(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	ds.runner.execResult = devcontainer.ExecResult{ExitCode: 7, Stdout: []byte("out"), Stderr: []byte("err")}
	var stdout, stderr bytes.Buffer
	err := runDev(context.Background(), &stdout, &stderr, "api", []string{"false"}, devOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != 7 {
		t.Errorf("expected exit 7, got %d", code)
	}
	if !strings.Contains(stdout.String(), "out") || !strings.Contains(stderr.String(), "err") {
		t.Errorf("expected output flushed before exit, got stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunDev_DetachReturnsAfterUp(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	var stderr bytes.Buffer
	if err := runDev(context.Background(), io.Discard, &stderr, "api", nil, devOptions{detach: true}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(ds.runner.upCalls) != 1 {
		t.Errorf("expected one Up call, got %d", len(ds.runner.upCalls))
	}
	if len(ds.runner.execCalls) != 0 {
		t.Errorf("expected no Exec calls in detach mode, got %d", len(ds.runner.execCalls))
	}
	if !strings.Contains(stderr.String(), "running") {
		t.Errorf("expected confirmation message, got %q", stderr.String())
	}
}

func TestRunDev_InteractiveShellHappyPath(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	ds.runner.execResult = devcontainer.ExecResult{ExitCode: 0, Stdout: []byte("bye\n"), Stderr: []byte("see ya\n")}
	var stdout, stderr bytes.Buffer
	if err := runDev(context.Background(), &stdout, &stderr, "api", nil, devOptions{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(ds.runner.execCalls) != 1 {
		t.Fatalf("expected 1 Exec call, got %d", len(ds.runner.execCalls))
	}
	if !ds.runner.execCalls[0].opts.TTY {
		t.Errorf("expected TTY=true on shell exec")
	}
	if !strings.Contains(stdout.String(), "bye") || !strings.Contains(stderr.String(), "see ya") {
		t.Errorf("expected output passthrough, got stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunDev_AttachExistingContainer(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	// Pre-populate containers.json with a known id so Up is skipped.
	if err := recordContainer("api", "existing-id"); err != nil {
		t.Fatal(err)
	}
	if err := runDev(context.Background(), io.Discard, io.Discard, "api", nil, devOptions{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(ds.runner.upCalls) != 0 {
		t.Errorf("expected no Up call on attach, got %d", len(ds.runner.upCalls))
	}
	if len(ds.runner.execCalls) != 1 {
		t.Fatalf("expected 1 Exec, got %d", len(ds.runner.execCalls))
	}
	if ds.runner.execCalls[0].containerID != "existing-id" {
		t.Errorf("expected container=existing-id, got %q", ds.runner.execCalls[0].containerID)
	}
}

func TestRunDev_InteractiveShellExecError(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	ds.runner.execErr = errors.New("shell wedge")
	err := runDev(context.Background(), io.Discard, io.Discard, "api", nil, devOptions{})
	if err == nil || !strings.Contains(err.Error(), "attach shell") {
		t.Errorf("expected shell error, got %v", err)
	}
}

func TestRunDev_InteractiveShellNonZeroExit(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	ds.runner.execResult = devcontainer.ExecResult{ExitCode: 9}
	err := runDev(context.Background(), io.Discard, io.Discard, "api", nil, devOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != 9 {
		t.Errorf("expected exit 9, got %d", code)
	}
}

// ---- Cobra plumbing -------------------------------------------------------

func TestNewDevCmd_FlagsRegistered(t *testing.T) {
	cmd := newDevCmd()
	if cmd.Flags().Lookup("detach") == nil {
		t.Error("expected --detach flag")
	}
	if cmd.Use == "" || !strings.HasPrefix(cmd.Use, "dev") {
		t.Errorf("expected Use prefix 'dev', got %q", cmd.Use)
	}
}

func TestDevCmd_RunE_DispatchesRunDev(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	withStatStub(t, statMissing) // fail-fast inside runDev
	cmd := newDevCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"api"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error from runDev (not initialised)")
	}
}

func TestDevCmd_RunE_PassesTrailingCommand(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	ds.runner.execResult = devcontainer.ExecResult{ExitCode: 0}
	cmd := newDevCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"api", "--", "npm", "test"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(ds.runner.execCalls) != 1 {
		t.Fatalf("expected 1 exec, got %d", len(ds.runner.execCalls))
	}
	got := ds.runner.execCalls[0].cmd
	if len(got) != 2 || got[0] != "npm" || got[1] != "test" {
		t.Errorf("expected [npm test] after --, got %v", got)
	}
}

func TestDevCmd_RunE_NoTrailingCommand(t *testing.T) {
	// Without a `--`, ArgsLenAtDash returns -1 and runDev gets a nil
	// command slice (interactive shell path).
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	cmd := newDevCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"api"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(ds.runner.upCalls) != 1 {
		t.Errorf("expected Up to fire, got %d", len(ds.runner.upCalls))
	}
}

// ---- defensive: ensure newRunnerFn default is real impl ------------------

func TestNewRunnerFn_DefaultReturnsRealRunner(t *testing.T) {
	r := newRunnerFn(nil, devcontainer.Options{})
	if r == nil {
		t.Fatal("default newRunnerFn returned nil")
	}
}

// ---- ensure state.ContainersFile is what we use --------------------------

func TestContainers_FilenameMatchesStatePackage(t *testing.T) {
	if state.ContainersFile != "containers.json" {
		t.Errorf("state pkg renamed ContainersFile to %q", state.ContainersFile)
	}
}

// Ensure JSON shape round-trips through the schema-tagged map we use.
func TestWriteContainers_JSONShape(t *testing.T) {
	dir := withStateDirStub(t)
	_ = recordContainer("api", "abc")
	data, err := os.ReadFile(filepath.Join(dir, "containers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if m["api"] != "abc" {
		t.Errorf("unexpected json: %s", string(data))
	}
}
