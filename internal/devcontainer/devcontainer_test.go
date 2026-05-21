package devcontainer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/dahal/bolted/internal/backend"
	"github.com/dahal/bolted/internal/backend/mock"
)

// scriptedBackend is a per-call-controllable backend.Backend. The
// mock.Mock in internal/backend/mock returns the same canned response
// for every Exec, which is fine for the "did the right call happen"
// check at the end of the suite but not enough for the up/down/exec
// sequences that need different responses per step.
//
// Each Exec pops the next scriptedResp off the queue. An empty queue
// yields the zero value (matches mock.Mock's default behaviour).
type scriptedBackend struct {
	mu    sync.Mutex
	calls []recordedExec
	queue []scriptedResp
}

type recordedExec struct {
	cmd  []string
	opts backend.ExecOpts
}

type scriptedResp struct {
	res backend.ExecResult
	err error
}

func (s *scriptedBackend) push(r scriptedResp)                                 { s.queue = append(s.queue, r) }
func (s *scriptedBackend) ok()                                                 { s.push(scriptedResp{}) }
func (s *scriptedBackend) okStdout(stdout string)                              { s.push(scriptedResp{res: backend.ExecResult{Stdout: []byte(stdout)}}) }
func (s *scriptedBackend) fail(exit int, stderr string)                        { s.push(scriptedResp{res: backend.ExecResult{ExitCode: exit, Stderr: []byte(stderr)}}) }
func (s *scriptedBackend) failErr(err error)                                   { s.push(scriptedResp{err: err}) }

func (s *scriptedBackend) Exec(_ context.Context, cmd []string, opts backend.ExecOpts) (backend.ExecResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, recordedExec{cmd: append([]string(nil), cmd...), opts: opts})
	if len(s.queue) == 0 {
		return backend.ExecResult{}, nil
	}
	r := s.queue[0]
	s.queue = s.queue[1:]
	return r.res, r.err
}

// Unused stubs so scriptedBackend satisfies backend.Backend.
func (s *scriptedBackend) Preflight(context.Context) error                { return nil }
func (s *scriptedBackend) EnsureVM(context.Context, backend.VMSpec) error { return nil }
func (s *scriptedBackend) StartVM(context.Context) error                  { return nil }
func (s *scriptedBackend) StopVM(context.Context) error                   { return nil }
func (s *scriptedBackend) IsRunning(context.Context) (bool, error)        { return false, nil }
func (s *scriptedBackend) ForwardPort(context.Context, int, int) error    { return nil }
func (s *scriptedBackend) UnforwardPort(context.Context, int) error       { return nil }
func (s *scriptedBackend) DeleteVM(context.Context) error                 { return nil }

var _ backend.Backend = (*scriptedBackend)(nil)

// firstArgs flattens recorded calls to the leading argv token for
// concise sequence assertions in tests.
func firstArgs(calls []recordedExec) []string {
	out := make([]string, len(calls))
	for i, c := range calls {
		out[i] = c.cmd[0]
	}
	return out
}

func TestContainerName(t *testing.T) {
	cases := map[string]string{
		"/bolted/repos/foo":   "bolted-foo",
		"/bolted/repos/bar/":  "bolted-bar",
		"baz":                    "bolted-baz",
		"/a/b/c/d-with-dashes":   "bolted-d-with-dashes",
	}
	for in, want := range cases {
		if got := containerName(in); got != want {
			t.Errorf("containerName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewDefaultsDevcontainerPath(t *testing.T) {
	r := New(&scriptedBackend{}, Options{})
	if r.defaultPath != "/bolted/default-devcontainer.json" {
		t.Fatalf("default path: got %q", r.defaultPath)
	}
}

func TestNewOverridesDevcontainerPath(t *testing.T) {
	r := New(&scriptedBackend{}, Options{DefaultDevcontainerPath: "/custom/path.json"})
	if r.defaultPath != "/custom/path.json" {
		t.Fatalf("override path: got %q", r.defaultPath)
	}
}

// TestUpSuccessRepoOwnDevcontainer covers the happy path where the
// repo ships its own devcontainer.json: probe, ps (no collision),
// test -f (exists), devcontainer up. The CLI's success JSON is
// parsed to extract the containerId.
func TestUpSuccessRepoOwnDevcontainer(t *testing.T) {
	be := &scriptedBackend{}
	be.ok()                                        // which devcontainer
	be.ok()                                        // podman ps (no match — empty stdout)
	be.ok()                                        // test -f .devcontainer/devcontainer.json (exists)
	be.okStdout(`{"outcome":"success","containerId":"abc123"}`) // devcontainer up

	r := New(be, Options{})
	id, err := r.Up(context.Background(), "/bolted/repos/foo", UpOpts{})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if id != "abc123" {
		t.Fatalf("containerID: got %q want %q", id, "abc123")
	}

	wantSeq := []string{"which", "podman", "test", "devcontainer"}
	if got := firstArgs(be.calls); !reflect.DeepEqual(got, wantSeq) {
		t.Fatalf("call sequence: got %v want %v", got, wantSeq)
	}

	upCall := be.calls[3]
	wantUp := []string{
		"devcontainer", "--docker-path=podman", "up",
		"--workspace-folder", "/bolted/repos/foo",
		"--id-label", "bolted.name=bolted-foo",
	}
	if !reflect.DeepEqual(upCall.cmd, wantUp) {
		t.Fatalf("up argv: got %v want %v", upCall.cmd, wantUp)
	}
}

// TestUpDefaultDevcontainerFallback covers the "repo has no
// .devcontainer/devcontainer.json" case: test -f returns exit 1 and
// the up call must include `--config <DefaultDevcontainerPath>`.
func TestUpDefaultDevcontainerFallback(t *testing.T) {
	be := &scriptedBackend{}
	be.ok()                                                              // which devcontainer
	be.ok()                                                              // podman ps (no match)
	be.fail(1, "")                                                       // test -f (absent)
	be.okStdout(`{"outcome":"success","containerId":"xyz"}`)             // devcontainer up

	r := New(be, Options{DefaultDevcontainerPath: "/etc/bolt/default.json"})
	id, err := r.Up(context.Background(), "/repos/bar", UpOpts{})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if id != "xyz" {
		t.Fatalf("id: got %q", id)
	}

	upCall := be.calls[3]
	joined := strings.Join(upCall.cmd, " ")
	if !strings.Contains(joined, "--config /etc/bolt/default.json") {
		t.Fatalf("expected --config in argv, got: %v", upCall.cmd)
	}
}

// TestUpContainerExists verifies that a podman ps result containing
// the expected name triggers ErrContainerExists and skips the actual
// `devcontainer up` call.
func TestUpContainerExists(t *testing.T) {
	be := &scriptedBackend{}
	be.ok()                            // which devcontainer
	be.okStdout("bolted-foo\n")     // podman ps reports our container

	r := New(be, Options{})
	_, err := r.Up(context.Background(), "/repos/foo", UpOpts{})
	if !errors.Is(err, ErrContainerExists) {
		t.Fatalf("want ErrContainerExists, got %v", err)
	}
	// Should NOT have called test -f or devcontainer up.
	if len(be.calls) != 2 {
		t.Fatalf("expected probe+ps only, got %d calls: %v", len(be.calls), firstArgs(be.calls))
	}
}

// TestUpDevcontainerCLIMissing covers the case where the CLI is
// absent AND npm is absent too — we should bail with
// ErrDevcontainerMissing and a `bolt provision` hint.
func TestUpDevcontainerCLIMissing(t *testing.T) {
	be := &scriptedBackend{}
	be.fail(1, "") // which devcontainer (missing)
	be.fail(1, "") // which npm        (missing)

	r := New(be, Options{})
	_, err := r.Up(context.Background(), "/repos/foo", UpOpts{})
	if !errors.Is(err, ErrDevcontainerMissing) {
		t.Fatalf("want ErrDevcontainerMissing, got %v", err)
	}
	if !strings.Contains(err.Error(), "bolt provision") {
		t.Fatalf("expected `bolt provision` hint, got %q", err.Error())
	}
}

// TestUpInstallsCLIWhenMissing covers the auto-install path: probe
// finds devcontainer missing, npm is present, npm install -g
// @devcontainers/cli succeeds.
func TestUpInstallsCLIWhenMissing(t *testing.T) {
	be := &scriptedBackend{}
	be.fail(1, "")                                          // which devcontainer (missing)
	be.ok()                                                  // which npm (present)
	be.ok()                                                  // npm install -g
	be.ok()                                                  // podman ps
	be.fail(1, "")                                           // test -f (absent)
	be.okStdout(`{"outcome":"success","containerId":"id1"}`) // devcontainer up

	r := New(be, Options{})
	id, err := r.Up(context.Background(), "/repos/x", UpOpts{})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if id != "id1" {
		t.Fatalf("id: got %q", id)
	}
	npmCall := be.calls[2]
	wantNpm := []string{"npm", "install", "-g", "@devcontainers/cli"}
	if !reflect.DeepEqual(npmCall.cmd, wantNpm) {
		t.Fatalf("npm install argv: got %v want %v", npmCall.cmd, wantNpm)
	}
}

// TestUpInstallFailureNonZero covers the install-failed-with-stderr
// branch of installFailureDetail.
func TestUpInstallFailureNonZero(t *testing.T) {
	be := &scriptedBackend{}
	be.fail(1, "")                  // which devcontainer
	be.ok()                          // which npm
	be.fail(1, "EACCES write /usr") // npm install -g

	r := New(be, Options{})
	_, err := r.Up(context.Background(), "/repos/x", UpOpts{})
	if !errors.Is(err, ErrDevcontainerMissing) {
		t.Fatalf("want ErrDevcontainerMissing, got %v", err)
	}
	if !strings.Contains(err.Error(), "EACCES") {
		t.Fatalf("expected stderr folded in, got %q", err.Error())
	}
}

// TestUpInstallFailureBackendError covers the err != nil branch in
// installFailureDetail.
func TestUpInstallFailureBackendError(t *testing.T) {
	be := &scriptedBackend{}
	be.fail(1, "")                       // which devcontainer
	be.ok()                               // which npm
	be.failErr(errors.New("vm crashed")) // npm install -g

	r := New(be, Options{})
	_, err := r.Up(context.Background(), "/repos/x", UpOpts{})
	if !errors.Is(err, ErrDevcontainerMissing) {
		t.Fatalf("want ErrDevcontainerMissing, got %v", err)
	}
	if !strings.Contains(err.Error(), "vm crashed") {
		t.Fatalf("expected backend error folded in, got %q", err.Error())
	}
}

// TestUpInstallFailureExitNoStderr covers the "exit nonzero, no
// stderr" branch in installFailureDetail.
func TestUpInstallFailureExitNoStderr(t *testing.T) {
	be := &scriptedBackend{}
	be.fail(1, "")                                                      // which devcontainer
	be.ok()                                                              // which npm
	be.push(scriptedResp{res: backend.ExecResult{ExitCode: 5}})         // npm install -g — silent failure

	r := New(be, Options{})
	_, err := r.Up(context.Background(), "/repos/x", UpOpts{})
	if !errors.Is(err, ErrDevcontainerMissing) {
		t.Fatalf("want ErrDevcontainerMissing, got %v", err)
	}
	if !strings.Contains(err.Error(), "exit 5") {
		t.Fatalf("expected exit code in message, got %q", err.Error())
	}
}

// TestUpInstallIdempotent verifies that ensureCLI runs at most once
// per Runner instance — a second Up call should NOT re-probe.
func TestUpInstallIdempotent(t *testing.T) {
	be := &scriptedBackend{}
	// First Up: probe + ps + test + up.
	be.ok()
	be.ok()
	be.fail(1, "")
	be.okStdout(`{"outcome":"success","containerId":"id1"}`)
	// Second Up: NO probe expected — just ps + test + up.
	be.ok()
	be.fail(1, "")
	be.okStdout(`{"outcome":"success","containerId":"id2"}`)

	r := New(be, Options{})
	if _, err := r.Up(context.Background(), "/repos/a", UpOpts{}); err != nil {
		t.Fatalf("Up #1: %v", err)
	}
	if _, err := r.Up(context.Background(), "/repos/b", UpOpts{}); err != nil {
		t.Fatalf("Up #2: %v", err)
	}

	// Count `which devcontainer` invocations across all recorded calls.
	probes := 0
	for _, c := range be.calls {
		if len(c.cmd) >= 2 && c.cmd[0] == "which" && c.cmd[1] == "devcontainer" {
			probes++
		}
	}
	if probes != 1 {
		t.Fatalf("expected 1 install probe across two Ups, got %d", probes)
	}
}

// TestUpInstallErrorIsCached verifies the failure path of ensureCLI
// is also memoised — once it fails, subsequent calls return the same
// error without re-running the probe.
func TestUpInstallErrorIsCached(t *testing.T) {
	be := &scriptedBackend{}
	be.fail(1, "") // which devcontainer
	be.fail(1, "") // which npm

	r := New(be, Options{})
	_, err1 := r.Up(context.Background(), "/repos/a", UpOpts{})
	_, err2 := r.Up(context.Background(), "/repos/a", UpOpts{})
	if !errors.Is(err1, ErrDevcontainerMissing) || !errors.Is(err2, ErrDevcontainerMissing) {
		t.Fatalf("want ErrDevcontainerMissing both times, got %v / %v", err1, err2)
	}
	// Only the first call should have produced any backend traffic.
	if len(be.calls) != 2 {
		t.Fatalf("expected cached failure to skip second probe, got calls: %v", firstArgs(be.calls))
	}
}

func TestUpValidationEmptyRepo(t *testing.T) {
	r := New(&scriptedBackend{}, Options{})
	if _, err := r.Up(context.Background(), "", UpOpts{}); err == nil {
		t.Fatal("expected validation error for empty repoPath")
	}
}

// TestUpPsBackendError surfaces the `podman ps` failure path.
func TestUpPsBackendError(t *testing.T) {
	be := &scriptedBackend{}
	be.ok()                              // which devcontainer
	be.failErr(errors.New("podman ded")) // podman ps

	r := New(be, Options{})
	_, err := r.Up(context.Background(), "/repos/x", UpOpts{})
	if err == nil || !strings.Contains(err.Error(), "podman ded") {
		t.Fatalf("want podman error wrapped, got %v", err)
	}
}

// TestUpCLIFailure surfaces a non-zero exit from `devcontainer up`.
func TestUpCLIFailure(t *testing.T) {
	be := &scriptedBackend{}
	be.ok()                                            // which devcontainer
	be.ok()                                            // podman ps
	be.ok()                                            // test -f
	be.fail(1, "container build failed: invalid base") // devcontainer up

	r := New(be, Options{})
	_, err := r.Up(context.Background(), "/repos/x", UpOpts{})
	if err == nil || !strings.Contains(err.Error(), "container build failed") {
		t.Fatalf("want CLI failure, got %v", err)
	}
}

// TestUpParseFailure exercises the "stdout had no JSON we could
// parse" branch.
func TestUpParseFailure(t *testing.T) {
	be := &scriptedBackend{}
	be.ok()                                  // which devcontainer
	be.ok()                                  // podman ps
	be.ok()                                  // test -f
	be.okStdout("not json at all\nstill not") // devcontainer up

	r := New(be, Options{})
	_, err := r.Up(context.Background(), "/repos/x", UpOpts{})
	if err == nil || !strings.Contains(err.Error(), "containerId") {
		t.Fatalf("want parse failure mentioning containerId, got %v", err)
	}
}

// TestUpParseEmptyContainerID exercises the case where the JSON
// parses but containerId is empty (CLI changed shape, bug).
func TestUpParseEmptyContainerID(t *testing.T) {
	be := &scriptedBackend{}
	be.ok() // which devcontainer
	be.ok() // podman ps
	be.ok() // test -f
	be.okStdout(`{"outcome":"success"}`)

	r := New(be, Options{})
	_, err := r.Up(context.Background(), "/repos/x", UpOpts{})
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
}

// TestParseContainerIDMalformedJSONLine drives the json.Unmarshal
// error path via the jsonUnmarshal indirection — a real-world
// devcontainer CLI never emits a `{`-prefixed line that fails to
// parse, but we want the defensive branch covered.
func TestParseContainerIDMalformedJSONLine(t *testing.T) {
	prev := jsonUnmarshal
	t.Cleanup(func() { jsonUnmarshal = prev })
	jsonUnmarshal = func([]byte, any) error { return errors.New("forced parse failure") }
	_, err := parseContainerID([]byte("{\"this looks like json but isn't\"}"))
	if err == nil {
		t.Fatal("expected error when every JSON line fails to parse")
	}
}

// TestParseContainerIDIgnoresNonJSONLines covers the "skip non-JSON
// noise" branch by giving a stdout where the JSON line is preceded
// by log chatter.
func TestParseContainerIDIgnoresNonJSONLines(t *testing.T) {
	stdout := []byte("starting devcontainer...\npulling image...\n  {\"outcome\":\"success\",\"containerId\":\"deadbeef\"}\n")
	id, err := parseContainerID(stdout)
	if err != nil {
		t.Fatalf("parseContainerID: %v", err)
	}
	if id != "deadbeef" {
		t.Fatalf("id: got %q", id)
	}
}

// TestParseContainerIDSanityWithRealUnmarshal pins the production
// json.Unmarshal indirection actually works on a realistic payload.
func TestParseContainerIDSanityWithRealUnmarshal(t *testing.T) {
	// Sanity: jsonUnmarshal is the real thing at test start.
	var sink struct{ A int }
	if err := jsonUnmarshal([]byte(`{"a":1}`), &sink); err != nil {
		t.Fatalf("real json.Unmarshal failed: %v", err)
	}
	_ = json.Unmarshal // keep the import live regardless of indirection state
}

func TestDownSuccess(t *testing.T) {
	be := &scriptedBackend{}
	be.ok()
	r := New(be, Options{})
	if err := r.Down(context.Background(), "abc123"); err != nil {
		t.Fatalf("Down: %v", err)
	}
	wantCmd := []string{"podman", "rm", "-f", "abc123"}
	if !reflect.DeepEqual(be.calls[0].cmd, wantCmd) {
		t.Fatalf("Down argv: got %v want %v", be.calls[0].cmd, wantCmd)
	}
}

func TestDownFailure(t *testing.T) {
	be := &scriptedBackend{}
	be.fail(1, "no such container")
	r := New(be, Options{})
	err := r.Down(context.Background(), "ghost")
	if err == nil || !strings.Contains(err.Error(), "no such container") {
		t.Fatalf("want failure with stderr folded, got %v", err)
	}
}

func TestDownValidation(t *testing.T) {
	r := New(&scriptedBackend{}, Options{})
	if err := r.Down(context.Background(), ""); err == nil {
		t.Fatal("expected empty-containerID error")
	}
}

func TestExecSuccessMinimal(t *testing.T) {
	be := &scriptedBackend{}
	be.push(scriptedResp{res: backend.ExecResult{Stdout: []byte("hi\n"), ExitCode: 0}})
	r := New(be, Options{})
	res, err := r.Exec(context.Background(), "abc", []string{"echo", "hi"}, ExecOpts{})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if string(res.Stdout) != "hi\n" || res.ExitCode != 0 {
		t.Fatalf("result: %+v", res)
	}
	wantCmd := []string{
		"devcontainer", "--docker-path=podman", "exec",
		"--container-id", "abc",
		"echo", "hi",
	}
	if !reflect.DeepEqual(be.calls[0].cmd, wantCmd) {
		t.Fatalf("Exec argv: got %v want %v", be.calls[0].cmd, wantCmd)
	}
}

func TestExecPassesOptions(t *testing.T) {
	be := &scriptedBackend{}
	be.ok()
	r := New(be, Options{})
	_, err := r.Exec(context.Background(), "abc", []string{"sh", "-c", "true"}, ExecOpts{
		Cwd: "/bolted/repos/foo",
		Env: []string{"FOO=1", "BAR=2"},
		TTY: true,
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	call := be.calls[0]
	wantCmd := []string{
		"devcontainer", "--docker-path=podman", "exec",
		"--container-id", "abc",
		"--workspace-folder", "/bolted/repos/foo",
		"--remote-env", "FOO=1",
		"--remote-env", "BAR=2",
		"sh", "-c", "true",
	}
	if !reflect.DeepEqual(call.cmd, wantCmd) {
		t.Fatalf("argv: got %v want %v", call.cmd, wantCmd)
	}
	if !call.opts.TTY {
		t.Fatal("expected TTY=true forwarded to backend")
	}
}

func TestExecBackendError(t *testing.T) {
	be := &scriptedBackend{}
	be.failErr(errors.New("vm gone"))
	r := New(be, Options{})
	res, err := r.Exec(context.Background(), "abc", []string{"echo"}, ExecOpts{})
	if err == nil || !strings.Contains(err.Error(), "vm gone") {
		t.Fatalf("want backend error wrapped, got %v", err)
	}
	// Even on error the partial result should be returned.
	_ = res
}

// TestExecNonZeroExitNotAnError documents the intentional choice
// that a non-zero exit from the in-container command is surfaced via
// ExitCode rather than as a Go error — callers (e.g. the CLI) need
// the exit code intact to propagate it to the user's shell.
func TestExecNonZeroExitNotAnError(t *testing.T) {
	be := &scriptedBackend{}
	be.push(scriptedResp{res: backend.ExecResult{ExitCode: 7, Stderr: []byte("nope")}})
	r := New(be, Options{})
	res, err := r.Exec(context.Background(), "abc", []string{"false"}, ExecOpts{})
	if err != nil {
		t.Fatalf("Exec should not wrap non-zero exit as an error: %v", err)
	}
	if res.ExitCode != 7 {
		t.Fatalf("ExitCode: got %d want 7", res.ExitCode)
	}
	if string(res.Stderr) != "nope" {
		t.Fatalf("Stderr: got %q", res.Stderr)
	}
}

func TestExecValidation(t *testing.T) {
	r := New(&scriptedBackend{}, Options{})
	if _, err := r.Exec(context.Background(), "", []string{"echo"}, ExecOpts{}); err == nil {
		t.Fatal("expected empty-containerID error")
	}
	if _, err := r.Exec(context.Background(), "abc", nil, ExecOpts{}); err == nil {
		t.Fatal("expected empty-cmd error")
	}
}

func TestBuildSuccessRepoOwnDevcontainer(t *testing.T) {
	be := &scriptedBackend{}
	be.ok() // which devcontainer
	be.ok() // test -f (present)
	be.ok() // devcontainer build

	r := New(be, Options{})
	if err := r.Build(context.Background(), "/repos/foo"); err != nil {
		t.Fatalf("Build: %v", err)
	}

	wantBuild := []string{
		"devcontainer", "--docker-path=podman", "build",
		"--workspace-folder", "/repos/foo",
	}
	if !reflect.DeepEqual(be.calls[2].cmd, wantBuild) {
		t.Fatalf("build argv: got %v want %v", be.calls[2].cmd, wantBuild)
	}
}

func TestBuildFallback(t *testing.T) {
	be := &scriptedBackend{}
	be.ok()        // which devcontainer
	be.fail(1, "") // test -f (absent)
	be.ok()        // devcontainer build

	r := New(be, Options{DefaultDevcontainerPath: "/d.json"})
	if err := r.Build(context.Background(), "/repos/foo"); err != nil {
		t.Fatalf("Build: %v", err)
	}
	joined := strings.Join(be.calls[2].cmd, " ")
	if !strings.Contains(joined, "--config /d.json") {
		t.Fatalf("expected --config fallback, got: %v", be.calls[2].cmd)
	}
}

func TestBuildFailure(t *testing.T) {
	be := &scriptedBackend{}
	be.ok()                  // which devcontainer
	be.ok()                  // test -f
	be.fail(2, "image error") // devcontainer build
	r := New(be, Options{})
	err := r.Build(context.Background(), "/repos/foo")
	if err == nil || !strings.Contains(err.Error(), "image error") {
		t.Fatalf("want build failure, got %v", err)
	}
}

func TestBuildValidation(t *testing.T) {
	r := New(&scriptedBackend{}, Options{})
	if err := r.Build(context.Background(), ""); err == nil {
		t.Fatal("expected empty-repoPath error")
	}
}

// TestBuildInstallFailurePropagates ensures Build surfaces install
// errors (it shares ensureCLI with Up).
func TestBuildInstallFailurePropagates(t *testing.T) {
	be := &scriptedBackend{}
	be.fail(1, "") // which devcontainer
	be.fail(1, "") // which npm
	r := New(be, Options{})
	err := r.Build(context.Background(), "/repos/x")
	if !errors.Is(err, ErrDevcontainerMissing) {
		t.Fatalf("want ErrDevcontainerMissing, got %v", err)
	}
}

// TestHaveBackendError covers the err != nil branch in have().
func TestHaveBackendError(t *testing.T) {
	be := &scriptedBackend{}
	be.failErr(errors.New("vm not ready")) // which devcontainer
	be.failErr(errors.New("vm not ready")) // which npm
	r := New(be, Options{})
	_, err := r.Up(context.Background(), "/repos/x", UpOpts{})
	if !errors.Is(err, ErrDevcontainerMissing) {
		t.Fatalf("want ErrDevcontainerMissing, got %v", err)
	}
}

// TestHasOwnDevcontainerBackendError covers the backend-error
// branch of hasOwnDevcontainer (treated as "no fallback needed
// to the install path; just absent so use the default config").
func TestHasOwnDevcontainerBackendError(t *testing.T) {
	be := &scriptedBackend{}
	be.ok()                              // which devcontainer
	be.ok()                              // podman ps
	be.failErr(errors.New("test broke")) // test -f
	be.okStdout(`{"outcome":"success","containerId":"id"}`)

	r := New(be, Options{DefaultDevcontainerPath: "/d.json"})
	if _, err := r.Up(context.Background(), "/repos/x", UpOpts{}); err != nil {
		t.Fatalf("Up: %v", err)
	}
	// Up should have fallen back to the default config.
	joined := strings.Join(be.calls[3].cmd, " ")
	if !strings.Contains(joined, "--config /d.json") {
		t.Fatalf("expected fallback config when test -f errors, got: %v", be.calls[3].cmd)
	}
}

// TestWrapExecAllBranches pins every formatting branch of wrapExec.
func TestWrapExecAllBranches(t *testing.T) {
	cases := []struct {
		name string
		res  backend.ExecResult
		err  error
		want string
	}{
		{"err+stderr", backend.ExecResult{Stderr: []byte(" boom ")}, errors.New("orig"), "devcontainer: op: orig: boom"},
		{"err only", backend.ExecResult{}, errors.New("orig"), "devcontainer: op: orig"},
		{"stderr only", backend.ExecResult{ExitCode: 7, Stderr: []byte("nope")}, nil, "devcontainer: op: exit 7: nope"},
		{"nothing", backend.ExecResult{ExitCode: 9}, nil, "devcontainer: op: exit 9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wrapExec("op", tc.res, tc.err).Error()
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

// TestErrMsgNil covers the defensive nil branch in errMsg.
func TestErrMsgNil(t *testing.T) {
	if errMsg(nil) != "" {
		t.Fatal("expected empty string for nil")
	}
	if errMsg(errors.New("x")) != "x" {
		t.Fatal("expected error string passed through")
	}
}

// TestMockBackendInteroperability mirrors the volume package's
// sanity check that the runner works with the recorded mock — the
// scriptedBackend above is local; the spec requires
// interoperability with internal/backend/mock.
func TestMockBackendInteroperability(t *testing.T) {
	m := mock.New()
	// Make `which devcontainer` succeed so we skip the install path.
	m.ExecResult = backend.ExecResult{}
	r := New(m, Options{})
	// Down doesn't require ensureCLI so it's the simplest exercise.
	if err := r.Down(context.Background(), "abc"); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if len(m.Calls) != 1 || m.Calls[0].Method != "Exec" {
		t.Fatalf("expected one Exec call on mock, got %+v", m.Calls)
	}
	if !reflect.DeepEqual(m.Calls[0].Cmd, []string{"podman", "rm", "-f", "abc"}) {
		t.Fatalf("argv: got %v", m.Calls[0].Cmd)
	}
}

// keep io imported even if no other test uses it directly — the
// scriptedBackend in earlier iterations consumed stdin and we may
// add similar logic later; cheap insurance against churn.
var _ = io.Discard

// TestUpAttachToNetworkSuccess covers the spec-19 post-Up wiring:
// when UpOpts.NetworkName is set the runner issues a
// `podman network connect <network> <id>` call after parsing the
// container id, and returns that id unchanged.
func TestUpAttachToNetworkSuccess(t *testing.T) {
	be := &scriptedBackend{}
	be.ok()                                                  // which devcontainer
	be.ok()                                                  // podman ps
	be.ok()                                                  // test -f
	be.okStdout(`{"outcome":"success","containerId":"abc"}`) // devcontainer up
	be.ok()                                                  // podman network connect

	r := New(be, Options{})
	id, err := r.Up(context.Background(), "/repos/foo", UpOpts{NetworkName: "bolted-net"})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if id != "abc" {
		t.Fatalf("id: got %q want %q", id, "abc")
	}
	connect := be.calls[4]
	wantConnect := []string{"podman", "network", "connect", "bolted-net", "abc"}
	if !reflect.DeepEqual(connect.cmd, wantConnect) {
		t.Fatalf("connect argv: got %v want %v", connect.cmd, wantConnect)
	}
}

// TestUpAttachToNetworkFailure covers the post-Up connect call
// failing — Up must surface the error rather than silently dropping
// it (an unattached container would otherwise sit on the wrong
// network, which is exactly the bug spec 19 is preventing).
func TestUpAttachToNetworkFailure(t *testing.T) {
	be := &scriptedBackend{}
	be.ok()                                                  // which devcontainer
	be.ok()                                                  // podman ps
	be.ok()                                                  // test -f
	be.okStdout(`{"outcome":"success","containerId":"abc"}`) // devcontainer up
	be.fail(125, "no such network: bolted-net")           // podman network connect

	r := New(be, Options{})
	_, err := r.Up(context.Background(), "/repos/foo", UpOpts{NetworkName: "bolted-net"})
	if err == nil || !strings.Contains(err.Error(), "no such network") {
		t.Fatalf("want connect failure surfaced, got %v", err)
	}
}

// TestUpNoNetworkSkipsAttach pins the additive contract: the empty
// NetworkName must NOT trigger the connect call so existing callers
// stay byte-for-byte identical to pre-spec-19 behaviour.
func TestUpNoNetworkSkipsAttach(t *testing.T) {
	be := &scriptedBackend{}
	be.ok()                                                   // which devcontainer
	be.ok()                                                   // podman ps
	be.ok()                                                   // test -f
	be.okStdout(`{"outcome":"success","containerId":"abc"}`)  // devcontainer up

	r := New(be, Options{})
	if _, err := r.Up(context.Background(), "/repos/foo", UpOpts{}); err != nil {
		t.Fatalf("Up: %v", err)
	}
	for _, c := range be.calls {
		if len(c.cmd) >= 3 && c.cmd[0] == "podman" && c.cmd[1] == "network" && c.cmd[2] == "connect" {
			t.Fatalf("unexpected network connect call: %v", c.cmd)
		}
	}
}

// TestAttachToNetworkSuccess exercises the helper directly so the
// happy path is covered even outside the Up flow (other callers may
// want to re-attach a container after a restart, for example).
func TestAttachToNetworkSuccess(t *testing.T) {
	be := &scriptedBackend{}
	be.ok()
	if err := attachToNetwork(context.Background(), be, "cid", "bolted-net"); err != nil {
		t.Fatalf("attachToNetwork: %v", err)
	}
	wantCmd := []string{"podman", "network", "connect", "bolted-net", "cid"}
	if !reflect.DeepEqual(be.calls[0].cmd, wantCmd) {
		t.Fatalf("argv: got %v want %v", be.calls[0].cmd, wantCmd)
	}
}

// TestAttachToNetworkValidation covers the two argument-validation
// branches (empty containerID, empty network).
func TestAttachToNetworkValidation(t *testing.T) {
	if err := attachToNetwork(context.Background(), &scriptedBackend{}, "", "net"); err == nil {
		t.Fatal("expected empty-containerID error")
	}
	if err := attachToNetwork(context.Background(), &scriptedBackend{}, "cid", ""); err == nil {
		t.Fatal("expected empty-network error")
	}
}

// TestAttachToNetworkBackendError surfaces the backend-level failure
// branch of the connect call.
func TestAttachToNetworkBackendError(t *testing.T) {
	be := &scriptedBackend{}
	be.failErr(errors.New("vm offline"))
	err := attachToNetwork(context.Background(), be, "cid", "net")
	if err == nil || !strings.Contains(err.Error(), "vm offline") {
		t.Fatalf("want backend error wrapped, got %v", err)
	}
}
