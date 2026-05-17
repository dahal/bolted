package cli

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/dahal/bolted/internal/backend"
	"github.com/dahal/bolted/internal/backend/mock"
	"github.com/dahal/bolted/internal/config"
)

// scriptedBackend (per-Exec-result fake with recording) is declared in
// status_cmd_test.go and reused throughout the passthrough tests below.
// Tests populate execScript / execErrs in lockstep with the Exec call
// sequence: index 0 is the lock probe, index 1+ are passthrough Execs.

// --- Stubs shared across passthrough tests --------------------------------

type passthroughStubs struct {
	tempDir string

	// statErr controls statFn behaviour. nil = file exists.
	statErr error

	// loadErr makes loadConfigFn return this error.
	loadErr error

	// backendErr makes newBackendFn return this error.
	backendErr error

	// be is the backend returned by newBackendFn. Tests configure it via
	// .execScript and .execErrs before install().
	be *scriptedBackend

	// isTerminal controls passthroughIsTerminalFn.
	isTerminal bool

	// stdin is the reader returned by passthroughStdinFn.
	stdin io.Reader

	// stdout / stderr capture the streams the router writes to.
	stdout *bytes.Buffer
	stderr *bytes.Buffer
}

func (s *passthroughStubs) install(t *testing.T) {
	t.Helper()
	if s.tempDir == "" {
		s.tempDir = t.TempDir()
	}
	if s.be == nil {
		s.be = &scriptedBackend{Mock: mock.New()}
	}
	if s.stdin == nil {
		s.stdin = strings.NewReader("")
	}
	if s.stdout == nil {
		s.stdout = &bytes.Buffer{}
	}
	if s.stderr == nil {
		s.stderr = &bytes.Buffer{}
	}

	origStat := statFn
	origLoad := loadConfigFn
	origBE := newBackendFn
	origWS := boltedDirFn
	origStdin := passthroughStdinFn
	origIsTerm := passthroughIsTerminalFn
	origStdout := stdout
	origStderr := stderr
	t.Cleanup(func() {
		statFn = origStat
		loadConfigFn = origLoad
		newBackendFn = origBE
		boltedDirFn = origWS
		passthroughStdinFn = origStdin
		passthroughIsTerminalFn = origIsTerm
		stdout = origStdout
		stderr = origStderr
	})

	boltedDirFn = func() string { return s.tempDir }
	statFn = func(_ string) (os.FileInfo, error) {
		if s.statErr != nil {
			return nil, s.statErr
		}
		return os.Stat(s.tempDir)
	}
	loadConfigFn = func(_ string) (*config.Config, error) {
		if s.loadErr != nil {
			return nil, s.loadErr
		}
		return config.NewDefault(), nil
	}
	newBackendFn = func(_ backend.Config) (backend.Backend, error) {
		if s.backendErr != nil {
			return nil, s.backendErr
		}
		return s.be, nil
	}
	passthroughStdinFn = func() io.Reader { return s.stdin }
	passthroughIsTerminalFn = func() bool { return s.isTerminal }
	stdout = s.stdout
	stderr = s.stderr
}

// unlockedScript returns an execScript whose first step (the probe) reports
// "unlocked" and whose remaining steps are the caller's command results.
func unlockedScript(cmdResults ...backend.ExecResult) []backend.ExecResult {
	out := make([]backend.ExecResult, 0, 1+len(cmdResults))
	out = append(out, backend.ExecResult{ExitCode: 0})
	out = append(out, cmdResults...)
	return out
}

// callAt returns the Cmd / ExecOpts of the i-th Exec call recorded on the
// embedded mock.
func (s *passthroughStubs) callAt(i int) (cmd []string, opts backend.ExecOpts) {
	s.be.Mu.Lock()
	defer s.be.Mu.Unlock()
	idx := -1
	for _, c := range s.be.Calls {
		if c.Method != "Exec" {
			continue
		}
		idx++
		if idx == i {
			return c.Cmd, c.ExecOpts
		}
	}
	return nil, backend.ExecOpts{}
}

// execCallCount returns how many Exec calls were recorded.
func (s *passthroughStubs) execCallCount() int {
	s.be.Mu.Lock()
	defer s.be.Mu.Unlock()
	n := 0
	for _, c := range s.be.Calls {
		if c.Method == "Exec" {
			n++
		}
	}
	return n
}

// ---- parsePassthroughArgs -------------------------------------------------

func TestParsePassthroughArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantCwd string
		wantCmd []string
		wantErr bool
	}{
		{"empty", nil, "", nil, false},
		{"plain command", []string{"git", "status"}, "", []string{"git", "status"}, false},
		{"cwd long form", []string{"--cwd", "repo", "git", "status"}, "repo", []string{"git", "status"}, false},
		{"cwd equals form", []string{"--cwd=/abs", "go", "test"}, "/abs", []string{"go", "test"}, false},
		{"cwd missing value", []string{"--cwd"}, "", nil, true},
		{"double-dash forces literal", []string{"--", "ls", "/etc"}, "", []string{"ls", "/etc"}, false},
		{"double-dash empty cmd", []string{"--"}, "", []string{}, false},
		{"cwd then double-dash", []string{"--cwd", "repo", "--", "ls"}, "repo", []string{"ls"}, false},
		{"unknown flag terminates scan", []string{"-v", "git"}, "", []string{"-v", "git"}, false},
		{"cwd before unknown flag", []string{"--cwd", "x", "-v", "git"}, "x", []string{"-v", "git"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cwd, cmd, err := parsePassthroughArgs(c.args)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got cwd=%q cmd=%v", cwd, cmd)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cwd != c.wantCwd {
				t.Errorf("cwd: got %q, want %q", cwd, c.wantCwd)
			}
			if !stringSliceEq(cmd, c.wantCmd) {
				t.Errorf("cmd: got %v, want %v", cmd, c.wantCmd)
			}
		})
	}
}

func stringSliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---- passthroughStub entrypoint ------------------------------------------

func TestPassthroughStub_DelegatesToRun(t *testing.T) {
	s := &passthroughStubs{
		be: &scriptedBackend{
			Mock:       mock.New(),
			execScript: unlockedScript(backend.ExecResult{ExitCode: 0}),
		},
	}
	s.install(t)
	if code := passthroughStub([]string{"git", "--version"}); code != 0 {
		t.Errorf("expected exit 0, got %d (stderr=%q)", code, s.stderr.String())
	}
}

// ---- passthroughRun happy paths ------------------------------------------

func TestPassthroughRun_HappyPath(t *testing.T) {
	s := &passthroughStubs{be: &scriptedBackend{
		Mock: mock.New(),
		execScript: unlockedScript(
			backend.ExecResult{ExitCode: 0, Stdout: []byte("git version 2.42\n")},
		),
	}}
	s.install(t)

	code := passthroughRun([]string{"git", "--version"})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr=%q)", code, s.stderr.String())
	}
	if got := s.stdout.String(); !strings.Contains(got, "git version 2.42") {
		t.Errorf("expected stdout forwarded, got %q", got)
	}
	if n := s.execCallCount(); n != 2 {
		t.Fatalf("expected 2 Exec calls, got %d", n)
	}
	probeCmd, _ := s.callAt(0)
	if !stringSliceEq(probeCmd, []string{"test", "-d", vmMountpoint}) {
		t.Errorf("probe cmd: got %v", probeCmd)
	}
	cmd, opts := s.callAt(1)
	if !stringSliceEq(cmd, []string{"git", "--version"}) {
		t.Errorf("cmd: got %v", cmd)
	}
	if opts.Cwd != vmMountpoint {
		t.Errorf("expected default cwd %q, got %q", vmMountpoint, opts.Cwd)
	}
}

func TestPassthroughRun_CwdRelative(t *testing.T) {
	s := &passthroughStubs{be: &scriptedBackend{
		Mock:       mock.New(),
		execScript: unlockedScript(backend.ExecResult{ExitCode: 0}),
	}}
	s.install(t)

	code := passthroughRun([]string{"--cwd", "myrepo", "git", "status"})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr=%q)", code, s.stderr.String())
	}
	_, opts := s.callAt(1)
	want := vmMountpoint + "/myrepo"
	if opts.Cwd != want {
		t.Errorf("expected cwd %q, got %q", want, opts.Cwd)
	}
}

func TestPassthroughRun_CwdAbsolute(t *testing.T) {
	s := &passthroughStubs{be: &scriptedBackend{
		Mock:       mock.New(),
		execScript: unlockedScript(backend.ExecResult{ExitCode: 0}),
	}}
	s.install(t)

	code := passthroughRun([]string{"--cwd=/tmp", "ls"})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr=%q)", code, s.stderr.String())
	}
	if _, opts := s.callAt(1); opts.Cwd != "/tmp" {
		t.Errorf("expected absolute cwd /tmp, got %q", opts.Cwd)
	}
}

func TestPassthroughRun_DoubleDashLiteralCommand(t *testing.T) {
	s := &passthroughStubs{be: &scriptedBackend{
		Mock:       mock.New(),
		execScript: unlockedScript(backend.ExecResult{ExitCode: 0}),
	}}
	s.install(t)

	code := passthroughRun([]string{"--", "ls", "/etc"})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr=%q)", code, s.stderr.String())
	}
	cmd, _ := s.callAt(1)
	if !stringSliceEq(cmd, []string{"ls", "/etc"}) {
		t.Errorf("expected literal ls /etc, got %v", cmd)
	}
}

func TestPassthroughRun_ExitCodePropagates(t *testing.T) {
	// AC 5: `bolt false` returns 1.
	s := &passthroughStubs{be: &scriptedBackend{
		Mock:       mock.New(),
		execScript: unlockedScript(backend.ExecResult{ExitCode: 1}),
	}}
	s.install(t)
	if code := passthroughRun([]string{"false"}); code != 1 {
		t.Errorf("expected exit 1, got %d", code)
	}
}

func TestPassthroughRun_StderrForwarded(t *testing.T) {
	s := &passthroughStubs{be: &scriptedBackend{
		Mock: mock.New(),
		execScript: unlockedScript(
			backend.ExecResult{ExitCode: 2, Stderr: []byte("inner stderr\n")},
		),
	}}
	s.install(t)
	_ = passthroughRun([]string{"git", "bogus"})
	if got := s.stderr.String(); !strings.Contains(got, "inner stderr") {
		t.Errorf("expected stderr forwarded, got %q", got)
	}
}

func TestPassthroughRun_TTYWhenStdinIsTerminal(t *testing.T) {
	s := &passthroughStubs{
		isTerminal: true,
		be: &scriptedBackend{
			Mock:       mock.New(),
			execScript: unlockedScript(backend.ExecResult{ExitCode: 0}),
		},
	}
	s.install(t)
	_ = passthroughRun([]string{"vim"})
	if _, opts := s.callAt(1); !opts.TTY {
		t.Error("expected TTY=true when host stdin is a terminal")
	}
}

func TestPassthroughRun_NoTTYWhenStdinIsPipe(t *testing.T) {
	s := &passthroughStubs{
		isTerminal: false,
		be: &scriptedBackend{
			Mock:       mock.New(),
			execScript: unlockedScript(backend.ExecResult{ExitCode: 0}),
		},
	}
	s.install(t)
	_ = passthroughRun([]string{"cat"})
	if _, opts := s.callAt(1); opts.TTY {
		t.Error("expected TTY=false when host stdin is not a terminal")
	}
}

func TestPassthroughRun_StdinForwarded(t *testing.T) {
	in := strings.NewReader("piped data")
	s := &passthroughStubs{
		stdin: in,
		be: &scriptedBackend{
			Mock:       mock.New(),
			execScript: unlockedScript(backend.ExecResult{ExitCode: 0}),
		},
	}
	s.install(t)
	_ = passthroughRun([]string{"cat"})
	if _, opts := s.callAt(1); opts.Stdin != in {
		t.Errorf("expected stdin reader passed through, got %v", opts.Stdin)
	}
}

// ---- error / failure paths -----------------------------------------------

func TestPassthroughRun_NoCommandGiven(t *testing.T) {
	s := &passthroughStubs{}
	s.install(t)
	if code := passthroughRun(nil); code != exitGeneric {
		t.Errorf("expected exit %d, got %d", exitGeneric, code)
	}
	if got := s.stderr.String(); !strings.Contains(got, "no command given") {
		t.Errorf("expected friendly message, got %q", got)
	}
}

func TestPassthroughRun_ParseError(t *testing.T) {
	s := &passthroughStubs{}
	s.install(t)
	if code := passthroughRun([]string{"--cwd"}); code != exitGeneric {
		t.Errorf("expected exit %d for parse error, got %d", exitGeneric, code)
	}
	if got := s.stderr.String(); !strings.Contains(got, "--cwd requires a value") {
		t.Errorf("expected parse error message, got %q", got)
	}
}

func TestPassthroughRun_NoCommandAfterCwd(t *testing.T) {
	s := &passthroughStubs{}
	s.install(t)
	if code := passthroughRun([]string{"--cwd", "repo"}); code != exitGeneric {
		t.Errorf("expected exit %d, got %d", exitGeneric, code)
	}
	if got := s.stderr.String(); !strings.Contains(got, "no command given") {
		t.Errorf("expected no-command message, got %q", got)
	}
}

func TestPassthroughRun_NotInitialised(t *testing.T) {
	s := &passthroughStubs{statErr: fs.ErrNotExist}
	s.install(t)
	if code := passthroughRun([]string{"git", "--version"}); code != exitLocked {
		t.Errorf("expected exit %d, got %d", exitLocked, code)
	}
	if got := s.stderr.String(); !strings.Contains(got, "not initialised") {
		t.Errorf("expected not-initialised message, got %q", got)
	}
}

func TestPassthroughRun_StatOtherError(t *testing.T) {
	s := &passthroughStubs{statErr: errors.New("permission denied")}
	s.install(t)
	if code := passthroughRun([]string{"git"}); code != exitGeneric {
		t.Errorf("expected exit %d, got %d", exitGeneric, code)
	}
	if got := s.stderr.String(); !strings.Contains(got, "stat config") {
		t.Errorf("expected stat error, got %q", got)
	}
}

func TestPassthroughRun_LoadConfigFails(t *testing.T) {
	s := &passthroughStubs{loadErr: errors.New("yaml broken")}
	s.install(t)
	if code := passthroughRun([]string{"git"}); code != exitGeneric {
		t.Errorf("expected exit %d, got %d", exitGeneric, code)
	}
	if got := s.stderr.String(); !strings.Contains(got, "load config") {
		t.Errorf("expected load-config error, got %q", got)
	}
}

func TestPassthroughRun_BackendInitFails(t *testing.T) {
	s := &passthroughStubs{backendErr: errors.New("no backend")}
	s.install(t)
	if code := passthroughRun([]string{"git"}); code != exitGeneric {
		t.Errorf("expected exit %d, got %d", exitGeneric, code)
	}
	if got := s.stderr.String(); !strings.Contains(got, "backend init") {
		t.Errorf("expected backend init error, got %q", got)
	}
}

func TestPassthroughRun_ProbeFails(t *testing.T) {
	s := &passthroughStubs{be: &scriptedBackend{
		Mock:     mock.New(),
		execErrs: []error{errors.New("probe blew up")},
	}}
	s.install(t)
	if code := passthroughRun([]string{"git"}); code != exitGeneric {
		t.Errorf("expected exit %d, got %d", exitGeneric, code)
	}
	if got := s.stderr.String(); !strings.Contains(got, "probe Bolted state") {
		t.Errorf("expected probe error, got %q", got)
	}
}

func TestPassthroughRun_LockedExitsCode2(t *testing.T) {
	// AC 6.
	s := &passthroughStubs{be: &scriptedBackend{
		Mock:       mock.New(),
		execScript: []backend.ExecResult{{ExitCode: 1}},
	}}
	s.install(t)
	if code := passthroughRun([]string{"git"}); code != exitLocked {
		t.Errorf("expected exit %d, got %d", exitLocked, code)
	}
	if got := s.stderr.String(); !strings.Contains(got, "locked") {
		t.Errorf("expected locked message, got %q", got)
	}
}

func TestPassthroughRun_ExecError(t *testing.T) {
	s := &passthroughStubs{be: &scriptedBackend{
		Mock:       mock.New(),
		execScript: []backend.ExecResult{{ExitCode: 0}, {}},
		execErrs:   []error{nil, errors.New("inner exec failed")},
	}}
	s.install(t)
	if code := passthroughRun([]string{"git"}); code != exitGeneric {
		t.Errorf("expected exit %d, got %d", exitGeneric, code)
	}
	if got := s.stderr.String(); !strings.Contains(got, "exec") {
		t.Errorf("expected exec error message, got %q", got)
	}
}

// ---- isPassthrough (spec 11 routing rules) ------------------------------

// These cases cover the spec-11 additions to cli.go's isPassthrough that
// the legacy table in cli_test.go does not exercise (the --cwd handling
// and the "all args consumed, no command" tail).

func TestIsPassthrough_Spec11(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"cwd long form before non-reserved", []string{"--cwd", "repo", "git", "status"}, true},
		{"cwd long form before reserved", []string{"--cwd", "repo", "init"}, false},
		{"cwd equals form before non-reserved", []string{"--cwd=/abs", "go", "test"}, true},
		{"cwd equals form before reserved", []string{"--cwd=/abs", "init"}, false},
		{"only --cwd no command", []string{"--cwd", "repo"}, true},
		{"only --cwd=val no command", []string{"--cwd=/abs"}, true},
		{"only --", []string{"--"}, true},
		{"cwd then double-dash then reserved", []string{"--cwd", "repo", "--", "ls"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isPassthrough(c.args); got != c.want {
				t.Errorf("isPassthrough(%v) = %v, want %v", c.args, got, c.want)
			}
		})
	}
}

// ---- default indirection ------------------------------------------------

// Cover the production indirection functions so the file hits 100%.
// passthroughStdinFn and passthroughIsTerminalFn are swapped at install()
// time in the other tests — call the defaults here.

func TestPassthroughDefaults_StdinFn(t *testing.T) {
	got := passthroughStdinFn()
	if got != os.Stdin {
		t.Errorf("expected os.Stdin, got %v", got)
	}
}

func TestPassthroughDefaults_IsTerminalFn(t *testing.T) {
	// We can't reliably assert true/false (depends on test runner),
	// but calling it must not panic.
	_ = passthroughIsTerminalFn()
}
