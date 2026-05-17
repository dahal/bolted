package wsl2

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dahal/bolted/internal/backend"
)

// fakeRunner is the unit-test stand-in for realRunner. It records every
// invocation and serves canned responses from a per-command queue.
//
// Calls are popped in FIFO order so a single test can stage a sequence
// (e.g. "first --list returns empty, second --list returns the
// distro"). When the queue for a name is empty, the runner returns
// ("", nil) so a test that only cares about "did this call happen?"
// can stay terse.
type fakeRunner struct {
	calls     []fakeCall
	responses map[string][]fakeResp
}

// fakeCall is one recorded invocation.
type fakeCall struct {
	Name  string
	Args  []string
	Stdin []byte
}

// fakeResp is one queued response.
type fakeResp struct {
	Stdout []byte
	Err    error
}

// newFakeRunner returns a runner with an empty response queue.
func newFakeRunner() *fakeRunner {
	return &fakeRunner{responses: map[string][]fakeResp{}}
}

// queue appends a canned response for invocations of name.
func (f *fakeRunner) queue(name string, stdout string, err error) {
	f.responses[name] = append(f.responses[name], fakeResp{
		Stdout: []byte(stdout),
		Err:    err,
	})
}

// next pops the head of name's response queue, or returns ("", nil) on
// empty.
func (f *fakeRunner) next(name string) fakeResp {
	q := f.responses[name]
	if len(q) == 0 {
		return fakeResp{}
	}
	f.responses[name] = q[1:]
	return q[0]
}

// Run implements runner.
func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, fakeCall{Name: name, Args: append([]string(nil), args...)})
	r := f.next(name)
	return r.Stdout, r.Err
}

// RunWithStdin implements runner.
func (f *fakeRunner) RunWithStdin(_ context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
	var buf []byte
	if stdin != nil {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return nil, err
		}
		buf = b
	}
	f.calls = append(f.calls, fakeCall{Name: name, Args: append([]string(nil), args...), Stdin: buf})
	r := f.next(name)
	return r.Stdout, r.Err
}

// lastCall returns the most recently recorded call.
func (f *fakeRunner) lastCall() fakeCall {
	if len(f.calls) == 0 {
		return fakeCall{}
	}
	return f.calls[len(f.calls)-1]
}

// callsFor returns every recorded call whose Name == name.
func (f *fakeRunner) callsFor(name string) []fakeCall {
	var out []fakeCall
	for _, c := range f.calls {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

// passWindowsGuard installs a temporary requireWindowsFn override that
// always returns nil, so tests on darwin can exercise the full code
// paths of methods otherwise gated by the OS check. The original
// function is restored when the test ends.
func passWindowsGuard(t *testing.T) {
	t.Helper()
	orig := requireWindowsFn
	requireWindowsFn = func() error { return nil }
	t.Cleanup(func() { requireWindowsFn = orig })
}

// newTestBackend wires up a Backend whose installDir is a per-test temp
// directory, whose runner is the fake, and whose rootfsPath points at a
// file we seed. Tests that want the rootfs absent can wipe it.
func newTestBackend(t *testing.T) (*Backend, *fakeRunner) {
	t.Helper()
	dir := t.TempDir()
	rootfs := filepath.Join(dir, "rootfs.tar")
	if err := os.WriteFile(rootfs, []byte("fake rootfs"), 0o644); err != nil {
		t.Fatalf("seed rootfs: %v", err)
	}
	r := newFakeRunner()
	b := NewWithOptions(Options{
		Name:       "bolted-test",
		InstallDir: dir,
		RootfsPath: rootfs,
		Runner:     r,
	})
	return b, r
}

// ---------------------------------------------------------------------
// New / NewWithOptions
// ---------------------------------------------------------------------

func TestNew_Defaults(t *testing.T) {
	b := New()
	if b.name != defaultDistroName {
		t.Errorf("default name = %q, want %q", b.name, defaultDistroName)
	}
	if b.installDir == "" {
		t.Error("default installDir should be non-empty")
	}
	if b.rootfsPath == "" {
		t.Error("default rootfsPath should be non-empty")
	}
	if b.runner == nil {
		t.Error("default runner should be non-nil")
	}
	if _, ok := b.runner.(realRunner); !ok {
		t.Errorf("default runner type = %T, want realRunner", b.runner)
	}
}

func TestNewWithOptions_AllOverridesApplied(t *testing.T) {
	r := newFakeRunner()
	b := NewWithOptions(Options{
		Name:       "alt",
		InstallDir: "/tmp/boltl-alt",
		RootfsPath: "/tmp/rootfs.tar",
		Runner:     r,
	})
	if b.name != "alt" {
		t.Errorf("name = %q", b.name)
	}
	if b.installDir != "/tmp/boltl-alt" {
		t.Errorf("installDir = %q", b.installDir)
	}
	if b.rootfsPath != "/tmp/rootfs.tar" {
		t.Errorf("rootfsPath = %q", b.rootfsPath)
	}
	if b.runner != r {
		t.Errorf("runner = %v, want the fake runner", b.runner)
	}
}

func TestNewWithOptions_PartialOverridesKeepDefaults(t *testing.T) {
	b := NewWithOptions(Options{Name: "only-name"})
	if b.name != "only-name" {
		t.Errorf("name = %q", b.name)
	}
	if b.installDir == "" || b.rootfsPath == "" {
		t.Errorf("expected defaults for installDir / rootfsPath, got %q / %q", b.installDir, b.rootfsPath)
	}
	if _, ok := b.runner.(realRunner); !ok {
		t.Errorf("expected realRunner default, got %T", b.runner)
	}
}

func TestBackend_ImplementsInterface(t *testing.T) {
	var _ backend.Backend = New()
	var _ backend.Backend = NewWithOptions(Options{})
}

// ---------------------------------------------------------------------
// requireWindows guard
// ---------------------------------------------------------------------

func TestRequireWindows_DefaultBehaviour(t *testing.T) {
	// Don't install the test override: this exercises the real
	// defaultRequireWindows.
	err := defaultRequireWindows()
	if runtime.GOOS == "windows" {
		if err != nil {
			t.Errorf("on windows defaultRequireWindows should return nil, got %v", err)
		}
	} else {
		if err == nil {
			t.Fatalf("on %s defaultRequireWindows should return an error", runtime.GOOS)
		}
		if !strings.Contains(err.Error(), runtime.GOOS) {
			t.Errorf("error %q should mention %s", err, runtime.GOOS)
		}
		if !strings.Contains(err.Error(), "Windows") {
			t.Errorf("error %q should mention Windows", err)
		}
	}
}

// TestGuardFiresOnNonWindows verifies the public methods all return the
// OS-guard error on non-windows hosts before invoking the runner. On
// windows the guard passes so the runner does get called; we cover
// that path via TestGuardOverride_AllowsExecutionOnDarwin below.
func TestGuardFiresOnNonWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("non-windows guard test")
	}
	// No passWindowsGuard call here — we want the real guard.
	b, r := newTestBackend(t)
	ctx := context.Background()

	checks := []struct {
		name string
		op   func() error
	}{
		{"EnsureVM", func() error { return b.EnsureVM(ctx, backend.VMSpec{}) }},
		{"StartVM", func() error { return b.StartVM(ctx) }},
		{"StopVM", func() error { return b.StopVM(ctx) }},
		{"IsRunning", func() error {
			_, err := b.IsRunning(ctx)
			return err
		}},
		{"Exec", func() error {
			_, err := b.Exec(ctx, []string{"echo"}, backend.ExecOpts{})
			return err
		}},
		{"ForwardPort", func() error { return b.ForwardPort(ctx, 1, 1) }},
		{"UnforwardPort", func() error { return b.UnforwardPort(ctx, 1) }},
		{"DeleteVM", func() error { return b.DeleteVM(ctx) }},
	}
	for _, c := range checks {
		err := c.op()
		if err == nil {
			t.Errorf("%s: expected guard error, got nil", c.name)
		}
		if err != nil && !strings.Contains(err.Error(), "wsl2 backend") {
			t.Errorf("%s: error should be the wsl2 guard, got: %v", c.name, err)
		}
	}
	if len(r.calls) != 0 {
		t.Errorf("guard should fire before any runner call, got: %+v", r.calls)
	}
}

// ---------------------------------------------------------------------
// requireWSL
// ---------------------------------------------------------------------

func TestRequireWSL_Present(t *testing.T) {
	b, r := newTestBackend(t)
	r.queue("wsl.exe", "WSL version: 2.0.0\n", nil)
	if err := b.requireWSL(context.Background()); err != nil {
		t.Errorf("requireWSL = %v, want nil", err)
	}
	got := r.lastCall()
	if got.Name != "wsl.exe" || len(got.Args) != 1 || got.Args[0] != "--version" {
		t.Errorf("unexpected call %+v", got)
	}
}

func TestRequireWSL_Missing(t *testing.T) {
	b, r := newTestBackend(t)
	r.queue("wsl.exe", "", errors.New(`exec: "wsl.exe": not found`))
	err := b.requireWSL(context.Background())
	if err == nil {
		t.Fatal("requireWSL should error when wsl.exe is missing")
	}
	if !strings.Contains(err.Error(), "wsl --install") {
		t.Errorf("error should mention `wsl --install`, got: %v", err)
	}
}

// ---------------------------------------------------------------------
// EnsureVM (with guard bypassed so we can run on darwin)
// ---------------------------------------------------------------------

func TestEnsureVM_ImportsWhenAbsent(t *testing.T) {
	passWindowsGuard(t)
	b, r := newTestBackend(t)
	r.queue("wsl.exe", "WSL version: 2.0.0\n", nil) // --version
	r.queue("wsl.exe", "", nil)                     // --list --quiet → empty
	r.queue("wsl.exe", "", nil)                     // --import

	if err := b.EnsureVM(context.Background(), backend.VMSpec{CPUs: 2, MemoryMB: 4096, DiskGB: 50}); err != nil {
		t.Fatalf("EnsureVM: %v", err)
	}

	// Confirm the import call shape.
	calls := r.callsFor("wsl.exe")
	if len(calls) != 3 {
		t.Fatalf("expected 3 wsl.exe calls, got %d: %+v", len(calls), calls)
	}
	want := []string{"--import", b.name, b.installDir, b.rootfsPath, "--version", "2"}
	if !equalStrings(calls[2].Args, want) {
		t.Errorf("import args = %v, want %v", calls[2].Args, want)
	}
	// .wslconfig hint written.
	if _, err := os.Stat(filepath.Join(b.installDir, ".wslconfig")); err != nil {
		t.Errorf("expected .wslconfig hint, got: %v", err)
	}
}

func TestEnsureVM_SkipsImportWhenPresent(t *testing.T) {
	passWindowsGuard(t)
	b, r := newTestBackend(t)
	r.queue("wsl.exe", "WSL version: 2.0.0\n", nil) // --version
	r.queue("wsl.exe", b.name+"\n", nil)            // --list → distro present

	if err := b.EnsureVM(context.Background(), backend.VMSpec{CPUs: 2, MemoryMB: 4096}); err != nil {
		t.Fatalf("EnsureVM: %v", err)
	}
	// Should have made exactly 2 wsl.exe calls — no --import.
	calls := r.callsFor("wsl.exe")
	if len(calls) != 2 {
		t.Errorf("expected 2 wsl.exe calls (no import), got %d: %+v", len(calls), calls)
	}
	for _, c := range calls {
		for _, a := range c.Args {
			if a == "--import" {
				t.Errorf("did not expect --import when distro exists: %+v", c)
			}
		}
	}
}

func TestEnsureVM_PrintsWarningWhenGlobalConfigExists(t *testing.T) {
	passWindowsGuard(t)
	// Seed USERPROFILE with a .wslconfig so globalWSLConfigExists()
	// returns true and EnsureVM takes the warn-print branch.
	profile := t.TempDir()
	if err := os.WriteFile(filepath.Join(profile, ".wslconfig"), []byte("# noop\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	orig, hadOrig := os.LookupEnv("USERPROFILE")
	_ = os.Setenv("USERPROFILE", profile)
	t.Cleanup(func() {
		if hadOrig {
			_ = os.Setenv("USERPROFILE", orig)
		} else {
			_ = os.Unsetenv("USERPROFILE")
		}
	})

	b, r := newTestBackend(t)
	r.queue("wsl.exe", "WSL version: 2.0.0\n", nil)
	r.queue("wsl.exe", b.name+"\n", nil) // distro exists
	if err := b.EnsureVM(context.Background(), backend.VMSpec{CPUs: 2, MemoryMB: 4096}); err != nil {
		t.Fatalf("EnsureVM: %v", err)
	}
	// We don't capture stderr here — Fprintln to os.Stderr is fine for
	// a real run, and coverage on the warn-print line is all we need.
}

func TestEnsureVM_SkipsWSLConfigHintWhenSpecIsZero(t *testing.T) {
	passWindowsGuard(t)
	b, r := newTestBackend(t)
	r.queue("wsl.exe", "WSL version: 2.0.0\n", nil)
	r.queue("wsl.exe", b.name+"\n", nil) // distro exists

	if err := b.EnsureVM(context.Background(), backend.VMSpec{}); err != nil {
		t.Fatalf("EnsureVM: %v", err)
	}
	if _, err := os.Stat(filepath.Join(b.installDir, ".wslconfig")); err == nil {
		t.Error("expected no .wslconfig hint for zero VMSpec")
	}
}

func TestEnsureVM_MissingRootfs(t *testing.T) {
	passWindowsGuard(t)
	b, r := newTestBackend(t)
	if err := os.Remove(b.rootfsPath); err != nil {
		t.Fatalf("remove rootfs: %v", err)
	}
	r.queue("wsl.exe", "WSL version: 2.0.0\n", nil) // --version
	r.queue("wsl.exe", "", nil)                     // --list → empty

	err := b.EnsureVM(context.Background(), backend.VMSpec{})
	if err == nil {
		t.Fatal("expected error when rootfs is missing")
	}
	if !strings.Contains(err.Error(), "rootfs tar not found") {
		t.Errorf("error should mention missing rootfs, got: %v", err)
	}
}

func TestEnsureVM_PropagatesRequireWSLError(t *testing.T) {
	passWindowsGuard(t)
	b, r := newTestBackend(t)
	r.queue("wsl.exe", "", errors.New("not found"))
	err := b.EnsureVM(context.Background(), backend.VMSpec{})
	if err == nil || !strings.Contains(err.Error(), "wsl --install") {
		t.Errorf("error should mention `wsl --install`, got: %v", err)
	}
}

func TestEnsureVM_PropagatesListError(t *testing.T) {
	passWindowsGuard(t)
	b, r := newTestBackend(t)
	r.queue("wsl.exe", "WSL version: 2.0.0\n", nil)
	r.queue("wsl.exe", "", errors.New("list failed"))
	err := b.EnsureVM(context.Background(), backend.VMSpec{})
	if err == nil || !strings.Contains(err.Error(), "list distros") {
		t.Errorf("expected list error, got: %v", err)
	}
}

func TestEnsureVM_PropagatesImportError(t *testing.T) {
	passWindowsGuard(t)
	b, r := newTestBackend(t)
	r.queue("wsl.exe", "WSL version: 2.0.0\n", nil) // --version
	r.queue("wsl.exe", "", nil)                     // --list → empty
	r.queue("wsl.exe", "", errors.New("import failed"))
	err := b.EnsureVM(context.Background(), backend.VMSpec{})
	if err == nil || !strings.Contains(err.Error(), "import distro") {
		t.Errorf("expected import error, got: %v", err)
	}
}

func TestEnsureVM_WSLConfigWriteError(t *testing.T) {
	passWindowsGuard(t)
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based test does not apply on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file mode bits")
	}
	dir := t.TempDir()
	// Pre-create .wslconfig as a read-only file so writeWSLConfigHint
	// fails on WriteFile.
	target := filepath.Join(dir, ".wslconfig")
	if err := os.WriteFile(target, []byte("old"), 0o400); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(target, 0o644) })
	if err := os.WriteFile(target, []byte("probe"), 0o400); err == nil {
		t.Skip("filesystem does not enforce read-only mode bits")
	}
	rootfs := filepath.Join(dir, "rootfs.tar")
	if err := os.WriteFile(rootfs, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed rootfs: %v", err)
	}
	r := newFakeRunner()
	r.queue("wsl.exe", "WSL version: 2.0.0\n", nil)
	r.queue("wsl.exe", "bolted-test\n", nil) // distro exists → skip import
	b := NewWithOptions(Options{
		Name:       "bolted-test",
		InstallDir: dir,
		RootfsPath: rootfs,
		Runner:     r,
	})
	err := b.EnsureVM(context.Background(), backend.VMSpec{CPUs: 2, MemoryMB: 4096})
	if err == nil {
		t.Fatal("expected write .wslconfig hint error")
	}
	if !strings.Contains(err.Error(), ".wslconfig hint") {
		t.Errorf("error should mention .wslconfig hint, got: %v", err)
	}
}

func TestEnsureVM_InstallDirMkdirError(t *testing.T) {
	passWindowsGuard(t)
	// Build a Backend whose installDir cannot be created (a regular
	// file at the path blocks MkdirAll).
	parent := t.TempDir()
	blocker := filepath.Join(parent, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	rootfs := filepath.Join(parent, "rootfs.tar")
	if err := os.WriteFile(rootfs, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed rootfs: %v", err)
	}
	r := newFakeRunner()
	r.queue("wsl.exe", "WSL version: 2.0.0\n", nil) // --version
	r.queue("wsl.exe", "", nil)                     // --list → empty
	b := NewWithOptions(Options{
		Name:       "bolted-test",
		InstallDir: filepath.Join(blocker, "child"),
		RootfsPath: rootfs,
		Runner:     r,
	})
	err := b.EnsureVM(context.Background(), backend.VMSpec{})
	if err == nil || !strings.Contains(err.Error(), "create install dir") {
		t.Errorf("expected MkdirAll error, got: %v", err)
	}
}

// ---------------------------------------------------------------------
// StartVM / StopVM
// ---------------------------------------------------------------------

func TestStartVM_OK(t *testing.T) {
	passWindowsGuard(t)
	b, r := newTestBackend(t)
	r.queue("wsl.exe", "WSL version: 2.0.0\n", nil) // --version
	r.queue("wsl.exe", "", nil)                     // -d <name> -- true
	if err := b.StartVM(context.Background()); err != nil {
		t.Fatalf("StartVM: %v", err)
	}
	last := r.lastCall()
	want := []string{"-d", b.name, "--", "true"}
	if !equalStrings(last.Args, want) {
		t.Errorf("StartVM args = %v, want %v", last.Args, want)
	}
}

func TestStartVM_PropagatesError(t *testing.T) {
	passWindowsGuard(t)
	b, r := newTestBackend(t)
	r.queue("wsl.exe", "WSL version: 2.0.0\n", nil)
	r.queue("wsl.exe", "", errors.New("boot failed"))
	err := b.StartVM(context.Background())
	if err == nil || !strings.Contains(err.Error(), "start distro") {
		t.Errorf("expected start distro error, got: %v", err)
	}
}

func TestStartVM_RequireWSLFailure(t *testing.T) {
	passWindowsGuard(t)
	b, r := newTestBackend(t)
	r.queue("wsl.exe", "", errors.New("not found"))
	err := b.StartVM(context.Background())
	if err == nil || !strings.Contains(err.Error(), "wsl --install") {
		t.Errorf("expected requireWSL error, got: %v", err)
	}
}

func TestStopVM_OK(t *testing.T) {
	passWindowsGuard(t)
	b, r := newTestBackend(t)
	r.queue("wsl.exe", "", nil)
	if err := b.StopVM(context.Background()); err != nil {
		t.Fatalf("StopVM: %v", err)
	}
	last := r.lastCall()
	want := []string{"--terminate", b.name}
	if !equalStrings(last.Args, want) {
		t.Errorf("StopVM args = %v, want %v", last.Args, want)
	}
}

func TestStopVM_PropagatesError(t *testing.T) {
	passWindowsGuard(t)
	b, r := newTestBackend(t)
	r.queue("wsl.exe", "", errors.New("boom"))
	err := b.StopVM(context.Background())
	if err == nil || !strings.Contains(err.Error(), "terminate distro") {
		t.Errorf("expected terminate error, got: %v", err)
	}
}

// ---------------------------------------------------------------------
// IsRunning / distroExists
// ---------------------------------------------------------------------

func TestIsRunning_True(t *testing.T) {
	passWindowsGuard(t)
	b, r := newTestBackend(t)
	r.queue("wsl.exe", b.name+"\n", nil)
	ok, err := b.IsRunning(context.Background())
	if err != nil {
		t.Fatalf("IsRunning: %v", err)
	}
	if !ok {
		t.Error("expected IsRunning=true")
	}
}

func TestIsRunning_False(t *testing.T) {
	passWindowsGuard(t)
	b, r := newTestBackend(t)
	r.queue("wsl.exe", "Ubuntu\n", nil)
	ok, err := b.IsRunning(context.Background())
	if err != nil {
		t.Fatalf("IsRunning: %v", err)
	}
	if ok {
		t.Error("expected IsRunning=false when our distro is not in the list")
	}
}

func TestIsRunning_HandlesUTF16Output(t *testing.T) {
	passWindowsGuard(t)
	b, r := newTestBackend(t)
	// Build a UTF-16LE encoding of the distro name with BOM.
	body := []byte{0xFF, 0xFE}
	for _, c := range b.name + "\n" {
		body = append(body, byte(c), byte(c>>8))
	}
	r.queue("wsl.exe", string(body), nil)
	ok, err := b.IsRunning(context.Background())
	if err != nil {
		t.Fatalf("IsRunning: %v", err)
	}
	if !ok {
		t.Error("expected IsRunning=true with UTF-16LE input")
	}
}

func TestIsRunning_RunnerError(t *testing.T) {
	passWindowsGuard(t)
	b, r := newTestBackend(t)
	r.queue("wsl.exe", "", errors.New("boom"))
	_, err := b.IsRunning(context.Background())
	if err == nil || !strings.Contains(err.Error(), "list running distros") {
		t.Errorf("expected list error, got: %v", err)
	}
}

func TestContainsDistro(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want bool
	}{
		{"empty", "", false},
		{"single match", "bolted\n", true},
		{"single non-match", "Ubuntu\n", false},
		{"multi-line match", "Ubuntu\nbolted\nDebian\n", true},
		{"trailing whitespace", "   bolted   \n", true},
		{"substring should not match", "bolted-dev\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := containsDistro(c.s, "bolted"); got != c.want {
				t.Errorf("containsDistro(%q) = %v, want %v", c.s, got, c.want)
			}
		})
	}
}

func TestDistroExists_RunnerError(t *testing.T) {
	passWindowsGuard(t)
	b, r := newTestBackend(t)
	r.queue("wsl.exe", "", errors.New("boom"))
	_, err := b.distroExists(context.Background())
	if err == nil || !strings.Contains(err.Error(), "list distros") {
		t.Errorf("expected list distros error, got: %v", err)
	}
}

// ---------------------------------------------------------------------
// Exec
// ---------------------------------------------------------------------

func TestExec_EmptyCmd(t *testing.T) {
	passWindowsGuard(t)
	b, _ := newTestBackend(t)
	res, err := b.Exec(context.Background(), nil, backend.ExecOpts{})
	if err == nil {
		t.Fatal("expected empty-cmd error")
	}
	if res.ExitCode != -1 {
		t.Errorf("exit code = %d, want -1", res.ExitCode)
	}
}

func TestExec_BuildsExpectedArgs(t *testing.T) {
	passWindowsGuard(t)
	b, r := newTestBackend(t)
	r.queue("wsl.exe", "ok\n", nil)
	res, err := b.Exec(context.Background(), []string{"echo", "ok"}, backend.ExecOpts{
		Cwd: "/work",
		Env: []string{"FOO=bar"},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !bytes.Equal(res.Stdout, []byte("ok\n")) {
		t.Errorf("stdout = %q, want ok", res.Stdout)
	}
	got := r.lastCall().Args
	wantHead := []string{"-d", b.name, "--cd", "/work", "--", "sh", "-c"}
	if len(got) < len(wantHead)+1 {
		t.Fatalf("too few args: %v", got)
	}
	if !equalStrings(got[:len(wantHead)], wantHead) {
		t.Errorf("head = %v, want %v", got[:len(wantHead)], wantHead)
	}
	payload := got[len(wantHead)]
	if !strings.Contains(payload, "FOO=") {
		t.Errorf("payload missing env: %q", payload)
	}
	if !strings.Contains(payload, "echo") {
		t.Errorf("payload missing cmd: %q", payload)
	}
}

func TestExec_NoCwdNoEnv(t *testing.T) {
	passWindowsGuard(t)
	b, r := newTestBackend(t)
	r.queue("wsl.exe", "hi\n", nil)
	if _, err := b.Exec(context.Background(), []string{"echo", "hi"}, backend.ExecOpts{}); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	got := r.lastCall().Args
	// No --cd flag.
	for _, a := range got {
		if a == "--cd" {
			t.Errorf("--cd should not appear when Cwd is empty: %v", got)
		}
	}
}

func TestExec_WithStdin(t *testing.T) {
	passWindowsGuard(t)
	b, r := newTestBackend(t)
	r.queue("wsl.exe", "hello", nil)
	res, err := b.Exec(context.Background(), []string{"cat"}, backend.ExecOpts{
		Stdin: strings.NewReader("hello"),
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if string(res.Stdout) != "hello" {
		t.Errorf("stdout = %q", res.Stdout)
	}
	if string(r.lastCall().Stdin) != "hello" {
		t.Errorf("stdin captured = %q, want hello", r.lastCall().Stdin)
	}
}

func TestExec_PropagatesPlainError(t *testing.T) {
	passWindowsGuard(t)
	b, r := newTestBackend(t)
	r.queue("wsl.exe", "", errors.New("boom"))
	res, err := b.Exec(context.Background(), []string{"true"}, backend.ExecOpts{})
	if err == nil {
		t.Fatal("expected error")
	}
	if res.ExitCode != -1 {
		t.Errorf("exit code = %d, want -1 for non-exit-error", res.ExitCode)
	}
}

func TestExec_PropagatesCmdError(t *testing.T) {
	passWindowsGuard(t)
	b, r := newTestBackend(t)
	wrapped := &cmdError{underlying: errors.New("exit status 5"), stderr: []byte("oh no\n")}
	r.queue("wsl.exe", "partial", wrapped)
	res, _ := b.Exec(context.Background(), []string{"false"}, backend.ExecOpts{})
	if string(res.Stderr) != "oh no" {
		t.Errorf("expected stderr to be propagated, got %q", res.Stderr)
	}
}

// TestCmdError_Error covers the Error() formatter on cmdError. It is
// reachable from production callers via fmt.Errorf("…: %w", err) only
// when something invokes .Error(); the unit test exercises both
// branches (with and without stderr).
func TestCmdError_Error(t *testing.T) {
	withStderr := &cmdError{underlying: errors.New("exit status 1"), stderr: []byte("oh no\n")}
	if got := withStderr.Error(); got != "exit status 1: oh no" {
		t.Errorf("Error() = %q, want %q", got, "exit status 1: oh no")
	}
	noStderr := &cmdError{underlying: errors.New("exit status 1"), stderr: nil}
	if got := noStderr.Error(); got != "exit status 1" {
		t.Errorf("Error() = %q, want %q", got, "exit status 1")
	}
}

// TestCmdError_Unwrap covers Unwrap to make sure errors.Is/As work
// through the wrapper.
func TestCmdError_Unwrap(t *testing.T) {
	base := errors.New("base")
	wrapped := &cmdError{underlying: base, stderr: nil}
	if !errors.Is(wrapped, base) {
		t.Error("errors.Is should reach the underlying error via Unwrap")
	}
}

// TestExec_ExitCodeFromExitError drives a real *exec.ExitError through
// Exec (wrapped in cmdError) to cover the errors.As(&exitErr) branch.
func TestExec_ExitCodeFromExitError(t *testing.T) {
	passWindowsGuard(t)
	// Generate a genuine *exec.ExitError by running a host process
	// that exits non-zero.
	cmd := exec.Command("sh", "-c", "exit 7")
	runErr := cmd.Run()
	if runErr == nil {
		t.Skip("could not synthesise a non-zero exit on this host")
	}
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) {
		t.Skipf("expected *exec.ExitError, got %T", runErr)
	}
	b, r := newTestBackend(t)
	r.queue("wsl.exe", "partial-stdout", &cmdError{underlying: runErr, stderr: []byte("bad\n")})
	res, err := b.Exec(context.Background(), []string{"false"}, backend.ExecOpts{})
	if err == nil {
		t.Fatal("expected Exec to surface the error")
	}
	if res.ExitCode != 7 {
		t.Errorf("ExitCode = %d, want 7", res.ExitCode)
	}
	if string(res.Stderr) != "bad" {
		t.Errorf("Stderr = %q, want %q", res.Stderr, "bad")
	}
	if string(res.Stdout) != "partial-stdout" {
		t.Errorf("Stdout = %q, want partial-stdout", res.Stdout)
	}
}

// ---------------------------------------------------------------------
// ForwardPort / UnforwardPort
// ---------------------------------------------------------------------

func TestForwardPort_InvalidPorts(t *testing.T) {
	passWindowsGuard(t)
	b, _ := newTestBackend(t)
	for _, c := range []struct{ guest, host int }{
		{0, 1}, {1, 0}, {-1, 1}, {1, -1},
	} {
		err := b.ForwardPort(context.Background(), c.guest, c.host)
		if err == nil {
			t.Errorf("ForwardPort(%d,%d) should error", c.guest, c.host)
		}
	}
}

func TestForwardPort_SamePort_NoNetshCall(t *testing.T) {
	passWindowsGuard(t)
	b, r := newTestBackend(t)
	if err := b.ForwardPort(context.Background(), 3000, 3000); err != nil {
		t.Fatalf("ForwardPort: %v", err)
	}
	if len(r.callsFor("netsh.exe")) != 0 {
		t.Errorf("did not expect netsh calls when ports match: %+v", r.calls)
	}
	m, err := loadPortMappings(b.installDir)
	if err != nil {
		t.Fatalf("loadPortMappings: %v", err)
	}
	if m.Mappings["3000"] != 3000 {
		t.Errorf("mapping not persisted: %+v", m)
	}
}

func TestForwardPort_DifferentPort_InvokesNetsh(t *testing.T) {
	passWindowsGuard(t)
	b, r := newTestBackend(t)
	r.queue("netsh.exe", "", nil)
	if err := b.ForwardPort(context.Background(), 3000, 8080); err != nil {
		t.Fatalf("ForwardPort: %v", err)
	}
	netshCalls := r.callsFor("netsh.exe")
	if len(netshCalls) != 1 {
		t.Fatalf("expected 1 netsh call, got %d", len(netshCalls))
	}
	joined := strings.Join(netshCalls[0].Args, " ")
	if !strings.Contains(joined, "listenport=8080") {
		t.Errorf("expected listenport=8080, got %s", joined)
	}
	if !strings.Contains(joined, "connectport=3000") {
		t.Errorf("expected connectport=3000, got %s", joined)
	}
	if !strings.Contains(joined, "add") {
		t.Errorf("expected add in args, got %s", joined)
	}
}

func TestForwardPort_NetshError(t *testing.T) {
	passWindowsGuard(t)
	b, r := newTestBackend(t)
	r.queue("netsh.exe", "", errors.New("admin required"))
	err := b.ForwardPort(context.Background(), 3000, 8080)
	if err == nil || !strings.Contains(err.Error(), "portproxy") {
		t.Errorf("expected portproxy error, got: %v", err)
	}
}

func TestForwardPort_LoadMappingsError(t *testing.T) {
	passWindowsGuard(t)
	dir := t.TempDir()
	// Force loadPortMappings to fail by making ports.json a directory.
	if err := os.Mkdir(filepath.Join(dir, portsFile), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	b := NewWithOptions(Options{
		Name:       "bolted-test",
		InstallDir: dir,
		RootfsPath: filepath.Join(t.TempDir(), "rootfs.tar"),
		Runner:     newFakeRunner(),
	})
	err := b.ForwardPort(context.Background(), 3000, 3000)
	if err == nil {
		t.Fatal("expected load error")
	}
}

func TestForwardPort_SaveMappingsError(t *testing.T) {
	passWindowsGuard(t)
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based test does not apply on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file mode bits")
	}
	dir := t.TempDir()
	// Pre-seed ports.json read-only so loadPortMappings succeeds but
	// the subsequent savePortMappings's WriteFile fails.
	if err := savePortMappings(dir, portMappings{Mappings: map[string]int{}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.Chmod(filepath.Join(dir, portsFile), 0o400); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, portsFile), 0o644) })
	if err := os.WriteFile(filepath.Join(dir, portsFile), []byte("probe"), 0o400); err == nil {
		t.Skip("filesystem does not enforce read-only mode bits")
	}
	b := NewWithOptions(Options{
		Name:       "bolted-test",
		InstallDir: dir,
		RootfsPath: filepath.Join(t.TempDir(), "rootfs.tar"),
		Runner:     newFakeRunner(),
	})
	err := b.ForwardPort(context.Background(), 3000, 3000)
	if err == nil {
		t.Fatal("expected save error")
	}
}

func TestUnforwardPort_InvalidPort(t *testing.T) {
	passWindowsGuard(t)
	b, _ := newTestBackend(t)
	if err := b.UnforwardPort(context.Background(), 0); err == nil {
		t.Error("UnforwardPort(0) should error")
	}
	if err := b.UnforwardPort(context.Background(), -1); err == nil {
		t.Error("UnforwardPort(-1) should error")
	}
}

func TestUnforwardPort_NoOpWhenUntracked(t *testing.T) {
	passWindowsGuard(t)
	b, r := newTestBackend(t)
	if err := b.UnforwardPort(context.Background(), 9999); err != nil {
		t.Errorf("untracked port should be a no-op, got %v", err)
	}
	if len(r.callsFor("netsh.exe")) != 0 {
		t.Errorf("unexpected netsh call: %+v", r.calls)
	}
}

func TestUnforwardPort_DeletesNetshAndPersists(t *testing.T) {
	passWindowsGuard(t)
	b, r := newTestBackend(t)
	if err := savePortMappings(b.installDir, portMappings{Mappings: map[string]int{"8080": 3000}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	r.queue("netsh.exe", "", nil)
	if err := b.UnforwardPort(context.Background(), 8080); err != nil {
		t.Fatalf("UnforwardPort: %v", err)
	}
	m, err := loadPortMappings(b.installDir)
	if err != nil {
		t.Fatalf("loadPortMappings: %v", err)
	}
	if _, ok := m.Mappings["8080"]; ok {
		t.Error("mapping should have been removed")
	}
	netshCalls := r.callsFor("netsh.exe")
	if len(netshCalls) != 1 {
		t.Fatalf("expected 1 netsh.exe delete call, got %d", len(netshCalls))
	}
	if !strings.Contains(strings.Join(netshCalls[0].Args, " "), "delete") {
		t.Errorf("expected delete in args: %v", netshCalls[0].Args)
	}
}

func TestUnforwardPort_SamePort_NoNetshDelete(t *testing.T) {
	passWindowsGuard(t)
	b, r := newTestBackend(t)
	if err := savePortMappings(b.installDir, portMappings{Mappings: map[string]int{"3000": 3000}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := b.UnforwardPort(context.Background(), 3000); err != nil {
		t.Fatalf("UnforwardPort: %v", err)
	}
	if len(r.callsFor("netsh.exe")) != 0 {
		t.Errorf("unexpected netsh call when ports were equal: %+v", r.calls)
	}
}

func TestUnforwardPort_NetshError(t *testing.T) {
	passWindowsGuard(t)
	b, r := newTestBackend(t)
	if err := savePortMappings(b.installDir, portMappings{Mappings: map[string]int{"8080": 3000}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	r.queue("netsh.exe", "", errors.New("admin required"))
	err := b.UnforwardPort(context.Background(), 8080)
	if err == nil || !strings.Contains(err.Error(), "portproxy") {
		t.Errorf("expected portproxy error, got: %v", err)
	}
}

func TestUnforwardPort_LoadMappingsError(t *testing.T) {
	passWindowsGuard(t)
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, portsFile), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	b := NewWithOptions(Options{
		Name:       "bolted-test",
		InstallDir: dir,
		RootfsPath: filepath.Join(t.TempDir(), "rootfs.tar"),
		Runner:     newFakeRunner(),
	})
	err := b.UnforwardPort(context.Background(), 3000)
	if err == nil {
		t.Fatal("expected load error")
	}
}

func TestUnforwardPort_SaveMappingsError(t *testing.T) {
	passWindowsGuard(t)
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based test does not apply on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file mode bits")
	}
	dir := t.TempDir()
	if err := savePortMappings(dir, portMappings{Mappings: map[string]int{"3000": 3000}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Make ports.json itself read-only. macOS / Linux honour file
	// mode bits for non-root users, so the subsequent WriteFile fails
	// with EACCES.
	if err := os.Chmod(filepath.Join(dir, portsFile), 0o400); err != nil {
		t.Fatalf("chmod file: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, portsFile), 0o644) })
	// Probe: confirm WriteFile actually fails on this fs / user.
	if err := os.WriteFile(filepath.Join(dir, portsFile), []byte("probe"), 0o644); err == nil {
		t.Skip("filesystem does not enforce read-only mode bits for this user")
	}
	b := NewWithOptions(Options{
		Name:       "bolted-test",
		InstallDir: dir,
		RootfsPath: filepath.Join(t.TempDir(), "rootfs.tar"),
		Runner:     newFakeRunner(),
	})
	err := b.UnforwardPort(context.Background(), 3000)
	if err == nil {
		t.Fatal("expected save error from read-only ports.json")
	}
}

// ---------------------------------------------------------------------
// DeleteVM
// ---------------------------------------------------------------------

func TestDeleteVM_OK(t *testing.T) {
	passWindowsGuard(t)
	b, r := newTestBackend(t)
	r.queue("wsl.exe", "", nil)
	if err := b.DeleteVM(context.Background()); err != nil {
		t.Fatalf("DeleteVM: %v", err)
	}
	want := []string{"--unregister", b.name}
	if !equalStrings(r.lastCall().Args, want) {
		t.Errorf("DeleteVM args = %v, want %v", r.lastCall().Args, want)
	}
}

func TestDeleteVM_PropagatesError(t *testing.T) {
	passWindowsGuard(t)
	b, r := newTestBackend(t)
	r.queue("wsl.exe", "", errors.New("boom"))
	err := b.DeleteVM(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unregister distro") {
		t.Errorf("expected unregister error, got: %v", err)
	}
}

// ---------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------

func TestJoinCmd(t *testing.T) {
	if got := joinCmd([]string{"ls"}); got != "ls" {
		t.Errorf("single = %q", got)
	}
	if got := joinCmd([]string{"echo", "hello world"}); got != "'echo' 'hello world'" {
		t.Errorf("multi = %q", got)
	}
	if got := joinCmd([]string{"echo", "it's"}); got != `'echo' 'it'\''s'` {
		t.Errorf("quote escape = %q", got)
	}
}

func TestFormatEnv(t *testing.T) {
	if got := formatEnv(nil); got != "" {
		t.Errorf("nil env = %q, want empty", got)
	}
	if got := formatEnv([]string{}); got != "" {
		t.Errorf("empty env = %q, want empty", got)
	}
	if got := formatEnv([]string{"FOO=bar"}); got != "FOO='bar' " {
		t.Errorf("single env = %q", got)
	}
	if got := formatEnv([]string{"FOO=bar baz"}); got != "FOO='bar baz' " {
		t.Errorf("env with spaces = %q", got)
	}
	if got := formatEnv([]string{"FOO=bar", "BAZ=qux"}); got != "FOO='bar' BAZ='qux' " {
		t.Errorf("multi env = %q", got)
	}
	if got := formatEnv([]string{"BAD"}); got != "" {
		t.Errorf("malformed env should be skipped, got %q", got)
	}
	if got := formatEnv([]string{"=val"}); got != "" {
		t.Errorf("empty-key env should be skipped, got %q", got)
	}
}

func TestShellQuote(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "''"},
		{"hello", "'hello'"},
		{"hello world", "'hello world'"},
		{"it's", `'it'\''s'`},
	}
	for _, c := range cases {
		if got := shellQuote(c.in); got != c.want {
			t.Errorf("shellQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestItoa(t *testing.T) {
	if got := itoa(42); got != "42" {
		t.Errorf("itoa(42) = %q", got)
	}
	if got := itoa(-1); got != "-1" {
		t.Errorf("itoa(-1) = %q", got)
	}
}

func TestDefaultRequireWindows_OnWindows(t *testing.T) {
	orig := currentGOOS
	t.Cleanup(func() { currentGOOS = orig })
	currentGOOS = "windows"
	if err := defaultRequireWindows(); err != nil {
		t.Errorf("expected nil on windows, got %v", err)
	}
}

func TestDefaultRequireWindows_OnNonWindows(t *testing.T) {
	orig := currentGOOS
	t.Cleanup(func() { currentGOOS = orig })
	currentGOOS = "darwin"
	err := defaultRequireWindows()
	if err == nil {
		t.Fatal("expected error on non-windows")
	}
	if !strings.Contains(err.Error(), "darwin") {
		t.Errorf("expected error to mention the GOOS, got: %v", err)
	}
}

func TestGlobalWSLConfigExists(t *testing.T) {
	orig, hadOrig := os.LookupEnv("USERPROFILE")
	t.Cleanup(func() {
		if hadOrig {
			_ = os.Setenv("USERPROFILE", orig)
		} else {
			_ = os.Unsetenv("USERPROFILE")
		}
	})

	_ = os.Unsetenv("USERPROFILE")
	if globalWSLConfigExists() {
		t.Error("with empty USERPROFILE, should report false")
	}

	dir := t.TempDir()
	_ = os.Setenv("USERPROFILE", dir)
	if globalWSLConfigExists() {
		t.Error("with no .wslconfig file, should report false")
	}

	if err := os.WriteFile(filepath.Join(dir, ".wslconfig"), []byte("# noop\n"), 0o644); err != nil {
		t.Fatalf("seed .wslconfig: %v", err)
	}
	if !globalWSLConfigExists() {
		t.Error("with .wslconfig present, should report true")
	}
}

// equalStrings is a tiny slice-equality helper used by several tests.
func equalStrings(a, b []string) bool {
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
