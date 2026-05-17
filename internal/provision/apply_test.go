package provision

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dahal/bolted/internal/backend"
	"github.com/dahal/bolted/internal/backend/mock"
)

// --- scripted backend ------------------------------------------------------
//
// The mock.Mock returns the same ExecResult/ErrExec for every call.
// Apply makes many calls with different expected outcomes, so we wrap
// it with a per-call scripted response. Each script entry maps "did
// this Exec match my predicate?" to "return this (res, err)".

type execStep struct {
	// match is a predicate over (cmd, opts). If nil, every call
	// matches. The first matching step in order is consumed.
	match func(cmd []string, opts backend.ExecOpts) bool
	res   backend.ExecResult
	err   error
	// stdinSeen captures the stdin that was passed (if non-nil) so
	// tests can assert against it.
	stdinSeen *[]byte
}

type scriptedBackend struct {
	*mock.Mock
	steps   []execStep
	stepIdx int
	t       *testing.T
}

func (s *scriptedBackend) Exec(ctx context.Context, cmd []string, opts backend.ExecOpts) (backend.ExecResult, error) {
	_, _ = s.Mock.Exec(ctx, cmd, opts)
	for i := s.stepIdx; i < len(s.steps); i++ {
		step := s.steps[i]
		if step.match == nil || step.match(cmd, opts) {
			s.stepIdx = i + 1
			if step.stdinSeen != nil && opts.Stdin != nil {
				data, _ := io.ReadAll(opts.Stdin)
				*step.stdinSeen = data
			}
			return step.res, step.err
		}
	}
	s.t.Fatalf("scriptedBackend: no step matches cmd=%v", cmd)
	return backend.ExecResult{}, nil
}

// matchPrefix returns a predicate matching commands whose argv begins
// with prefix. Convenient for "any apk add ..." style steps.
func matchPrefix(prefix ...string) func([]string, backend.ExecOpts) bool {
	return func(cmd []string, _ backend.ExecOpts) bool {
		if len(cmd) < len(prefix) {
			return false
		}
		for i, p := range prefix {
			if cmd[i] != p {
				return false
			}
		}
		return true
	}
}

// matchExact returns a predicate matching an exact argv.
func matchExact(want ...string) func([]string, backend.ExecOpts) bool {
	return func(cmd []string, _ backend.ExecOpts) bool {
		return reflect.DeepEqual(cmd, want)
	}
}

// okStep is a shorthand for "matches predicate, returns success".
func okStep(match func([]string, backend.ExecOpts) bool) execStep {
	return execStep{match: match}
}

// --- guard tests -----------------------------------------------------------

func TestApply_NilProfile(t *testing.T) {
	_, err := Apply(context.Background(), &mock.Mock{}, nil, NewCache(), "", nil)
	if err == nil || !strings.Contains(err.Error(), "nil profile") {
		t.Errorf("expected nil-profile err, got %v", err)
	}
}

func TestApply_NilCache(t *testing.T) {
	_, err := Apply(context.Background(), &mock.Mock{}, &BoltedProfile{}, nil, "", nil)
	if err == nil || !strings.Contains(err.Error(), "nil cache") {
		t.Errorf("expected nil-cache err, got %v", err)
	}
}

func TestApply_NilBackend(t *testing.T) {
	_, err := Apply(context.Background(), nil, &BoltedProfile{}, NewCache(), "", nil)
	if err == nil || !strings.Contains(err.Error(), "nil backend") {
		t.Errorf("expected nil-backend err, got %v", err)
	}
}

func TestApply_NilStdout_NoCrash(t *testing.T) {
	// Empty profile and cache → no exec calls, no writes — must not
	// panic on a nil stdout (we substitute io.Discard).
	res, err := Apply(context.Background(), mock.New(), &BoltedProfile{}, NewCache(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res == nil {
		t.Fatal("nil result")
	}
}

// --- happy paths -----------------------------------------------------------

func TestApply_AddFeatureAndPackage_FromEmpty(t *testing.T) {
	pin := time.Unix(0, 0)
	withNow(t, func() time.Time {
		now := pin
		pin = pin.Add(2 * time.Second)
		return now
	})

	sb := &scriptedBackend{
		Mock: mock.New(),
		t:    t,
		steps: []execStep{
			okStep(matchExact("devcontainer", "features", "install", "ghcr.io/foo:1")),
			okStep(matchPrefix("apk", "add")),
		},
	}
	profile := &BoltedProfile{
		Features: []string{"ghcr.io/foo:1"},
		Packages: []string{"jq"},
	}
	cache := NewCache()
	var out bytes.Buffer
	res, err := Apply(context.Background(), sb, profile, cache, "", &out)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !sliceEqual(res.FeaturesAdded, []string{"ghcr.io/foo:1"}) {
		t.Errorf("FeaturesAdded = %v", res.FeaturesAdded)
	}
	if !sliceEqual(res.PackagesAdded, []string{"jq"}) {
		t.Errorf("PackagesAdded = %v", res.PackagesAdded)
	}
	if !strings.Contains(out.String(), "installing feature") {
		t.Errorf("stdout = %q", out.String())
	}
	if !strings.Contains(out.String(), "apk add jq") {
		t.Errorf("stdout = %q", out.String())
	}
	if res.Duration != 2*time.Second {
		t.Errorf("Duration = %v", res.Duration)
	}
}

func TestApply_RemoveFeatureAndPackage(t *testing.T) {
	sb := &scriptedBackend{
		Mock: mock.New(),
		t:    t,
		steps: []execStep{
			okStep(matchPrefix("rm", "-rf")),
			okStep(matchPrefix("apk", "del")),
		},
	}
	cache := NewCache()
	cache.Features = []string{"ghcr.io/foo:1"}
	cache.Packages = []string{"jq"}
	profile := &BoltedProfile{}
	var out bytes.Buffer
	res, err := Apply(context.Background(), sb, profile, cache, "", &out)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !sliceEqual(res.FeaturesRemoved, []string{"ghcr.io/foo:1"}) {
		t.Errorf("FeaturesRemoved = %v", res.FeaturesRemoved)
	}
	if !sliceEqual(res.PackagesRemoved, []string{"jq"}) {
		t.Errorf("PackagesRemoved = %v", res.PackagesRemoved)
	}
	if !strings.Contains(out.String(), "removing feature") {
		t.Errorf("missing 'removing feature' in stdout: %q", out.String())
	}
}

func TestApply_Idempotent_NoOpSecondRun(t *testing.T) {
	sb := &scriptedBackend{Mock: mock.New(), t: t}
	cache := NewCache()
	cache.Features = []string{"a:1"}
	cache.Packages = []string{"jq"}
	cache.Shell = "zsh"
	profile := &BoltedProfile{
		Features: []string{"a:1"},
		Packages: []string{"jq"},
		Shell:    "zsh",
	}
	res, err := Apply(context.Background(), sb, profile, cache, "", io.Discard)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.FeaturesAdded) > 0 || len(res.FeaturesRemoved) > 0 {
		t.Errorf("expected no feature change, got %+v", res)
	}
	if res.ShellSet {
		t.Errorf("expected ShellSet false on no-op")
	}
}

func TestApply_GitConfig_AlwaysReapplies(t *testing.T) {
	sb := &scriptedBackend{
		Mock: mock.New(),
		t:    t,
		steps: []execStep{
			okStep(matchPrefix("git", "config", "--global", "user.email")),
			okStep(matchPrefix("git", "config", "--global", "user.name")),
		},
	}
	cache := NewCache()
	cache.GitConfig = map[string]string{"user.email": "old@x"}
	profile := &BoltedProfile{
		GitConfig: map[string]string{"user.email": "me@x", "user.name": "Me"},
	}
	res, err := Apply(context.Background(), sb, profile, cache, "", io.Discard)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.GitConfigApplied != 2 {
		t.Errorf("GitConfigApplied = %d, want 2", res.GitConfigApplied)
	}
	if cache.GitConfig["user.email"] != "me@x" || cache.GitConfig["user.name"] != "Me" {
		t.Errorf("cache.GitConfig = %v", cache.GitConfig)
	}
}

func TestApply_ShellSet_OnDrift(t *testing.T) {
	sb := &scriptedBackend{
		Mock: mock.New(),
		t:    t,
		steps: []execStep{
			okStep(matchExact("chsh", "-s", "zsh")),
		},
	}
	cache := NewCache()
	cache.Shell = "sh"
	profile := &BoltedProfile{Shell: "zsh"}
	res, err := Apply(context.Background(), sb, profile, cache, "", io.Discard)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !res.ShellSet || cache.Shell != "zsh" {
		t.Errorf("expected ShellSet + cache update, res=%+v cache.Shell=%q", res, cache.Shell)
	}
}

func TestApply_ShellEmpty_NeverApplies(t *testing.T) {
	sb := &scriptedBackend{Mock: mock.New(), t: t}
	cache := NewCache()
	cache.Shell = "sh"
	profile := &BoltedProfile{}
	res, err := Apply(context.Background(), sb, profile, cache, "", io.Discard)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.ShellSet {
		t.Errorf("expected ShellSet false")
	}
}

// --- dotfiles -------------------------------------------------------------

func TestApply_Dotfile_Copies_AndChmods(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, ".zshrc")
	if err := os.WriteFile(src, []byte("alias ll='ls -la'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdinSeen []byte
	sb := &scriptedBackend{
		Mock: mock.New(),
		t:    t,
		steps: []execStep{
			// remoteExists probe: file does not exist (exit 1).
			{match: matchPrefix("sh", "-c"), res: backend.ExecResult{ExitCode: 1}},
			// tee write
			{match: matchPrefix("sh", "-c"), stdinSeen: &stdinSeen},
			// chmod
			okStep(matchPrefix("chmod")),
		},
	}
	profile := &BoltedProfile{Dotfiles: []string{".zshrc"}}
	cache := NewCache()
	var out bytes.Buffer
	res, err := Apply(context.Background(), sb, profile, cache, dir, &out)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !sliceEqual(res.DotfilesChanged, []string{".zshrc"}) {
		t.Errorf("DotfilesChanged = %v", res.DotfilesChanged)
	}
	if len(res.DotfilesOverwritten) != 0 {
		t.Errorf("expected no overwrite, got %v", res.DotfilesOverwritten)
	}
	if string(stdinSeen) != "alias ll='ls -la'\n" {
		t.Errorf("stdin = %q", stdinSeen)
	}
	if cache.Dotfiles[".zshrc"] == "" {
		t.Error("expected cache entry for .zshrc")
	}
}

func TestApply_Dotfile_Overwrite_WarnsAndRecords(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, ".zshrc")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	sb := &scriptedBackend{
		Mock: mock.New(),
		t:    t,
		steps: []execStep{
			// remoteExists probe: exists (exit 0)
			{match: matchPrefix("sh", "-c"), res: backend.ExecResult{ExitCode: 0}},
			// tee
			okStep(matchPrefix("sh", "-c")),
			// chmod
			okStep(matchPrefix("chmod")),
		},
	}
	profile := &BoltedProfile{Dotfiles: []string{".zshrc"}}
	var out bytes.Buffer
	_, err := Apply(context.Background(), sb, profile, NewCache(), dir, &out)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !strings.Contains(out.String(), "warning: overwriting") {
		t.Errorf("expected overwrite warning, got %q", out.String())
	}
}

func TestApply_Dotfile_SkipsWhenDigestMatches(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, ".zshrc")
	body := []byte("hello\n")
	if err := os.WriteFile(src, body, 0o600); err != nil {
		t.Fatal(err)
	}
	// Seed cache with the correct digest so Apply takes the skip path.
	cache := NewCache()
	// We can compute the digest by running Apply once and inspecting,
	// but for determinism just use the same call shape.
	_, err := Apply(context.Background(), &scriptedBackend{
		Mock: mock.New(),
		t:    t,
		steps: []execStep{
			{match: matchPrefix("sh", "-c"), res: backend.ExecResult{ExitCode: 1}},
			okStep(matchPrefix("sh", "-c")),
			okStep(matchPrefix("chmod")),
		},
	}, &BoltedProfile{Dotfiles: []string{".zshrc"}}, cache, dir, io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	// Now Apply again. With a scriptedBackend that has zero steps,
	// any Exec call would Fatal — so the skip path is implicit.
	sb := &scriptedBackend{Mock: mock.New(), t: t}
	res, err := Apply(context.Background(), sb, &BoltedProfile{Dotfiles: []string{".zshrc"}}, cache, dir, io.Discard)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.DotfilesChanged) != 0 {
		t.Errorf("expected no change on second apply, got %v", res.DotfilesChanged)
	}
}

func TestApply_Dotfile_AbsolutePath(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "abs.txt")
	if err := os.WriteFile(abs, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	sb := &scriptedBackend{
		Mock: mock.New(),
		t:    t,
		steps: []execStep{
			{match: matchPrefix("sh", "-c"), res: backend.ExecResult{ExitCode: 1}},
			okStep(matchPrefix("sh", "-c")),
			okStep(matchPrefix("chmod")),
		},
	}
	profile := &BoltedProfile{Dotfiles: []string{abs}}
	if _, err := Apply(context.Background(), sb, profile, NewCache(), "", io.Discard); err != nil {
		t.Fatalf("Apply: %v", err)
	}
}

func TestApply_Dotfile_ReadError(t *testing.T) {
	orig := readDotfileFn
	t.Cleanup(func() { readDotfileFn = orig })
	wantErr := errors.New("disk gone")
	readDotfileFn = func(string) ([]byte, fs.FileMode, error) {
		return nil, 0, wantErr
	}
	profile := &BoltedProfile{Dotfiles: []string{".zshrc"}}
	_, err := Apply(context.Background(), mock.New(), profile, NewCache(), "/tmp", io.Discard)
	if err == nil || !errors.Is(err, wantErr) {
		t.Errorf("expected wrapped read err, got %v", err)
	}
}

// --- error propagation -----------------------------------------------------

func TestApply_InstallFeature_Error(t *testing.T) {
	sb := &scriptedBackend{
		Mock: mock.New(),
		t:    t,
		steps: []execStep{
			{match: nil, err: errors.New("net down")},
		},
	}
	_, err := Apply(context.Background(), sb, &BoltedProfile{Features: []string{"a:1"}}, NewCache(), "", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "install feature") {
		t.Errorf("expected install feature err, got %v", err)
	}
}

func TestApply_RemoveFeature_Error(t *testing.T) {
	sb := &scriptedBackend{
		Mock: mock.New(),
		t:    t,
		steps: []execStep{
			{match: matchPrefix("rm"), res: backend.ExecResult{ExitCode: 1, Stderr: []byte("nope")}},
		},
	}
	cache := NewCache()
	cache.Features = []string{"a:1"}
	_, err := Apply(context.Background(), sb, &BoltedProfile{}, cache, "", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "remove feature") {
		t.Errorf("expected remove feature err, got %v", err)
	}
}

func TestApply_ApkAdd_Error(t *testing.T) {
	sb := &scriptedBackend{
		Mock: mock.New(),
		t:    t,
		steps: []execStep{
			{match: matchPrefix("apk", "add"), res: backend.ExecResult{ExitCode: 1}},
		},
	}
	_, err := Apply(context.Background(), sb, &BoltedProfile{Packages: []string{"jq"}}, NewCache(), "", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "apk add") {
		t.Errorf("expected apk add err, got %v", err)
	}
}

func TestApply_ApkDel_Error(t *testing.T) {
	sb := &scriptedBackend{
		Mock: mock.New(),
		t:    t,
		steps: []execStep{
			{match: matchPrefix("apk", "del"), err: errors.New("locked")},
		},
	}
	cache := NewCache()
	cache.Packages = []string{"jq"}
	_, err := Apply(context.Background(), sb, &BoltedProfile{}, cache, "", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "apk del") {
		t.Errorf("expected apk del err, got %v", err)
	}
}

func TestApply_GitConfig_Error(t *testing.T) {
	sb := &scriptedBackend{
		Mock: mock.New(),
		t:    t,
		steps: []execStep{
			{match: matchPrefix("git", "config"), err: errors.New("git missing")},
		},
	}
	profile := &BoltedProfile{GitConfig: map[string]string{"a": "b"}}
	_, err := Apply(context.Background(), sb, profile, NewCache(), "", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "git config") {
		t.Errorf("expected git config err, got %v", err)
	}
}

func TestApply_Chsh_Error(t *testing.T) {
	sb := &scriptedBackend{
		Mock: mock.New(),
		t:    t,
		steps: []execStep{
			{match: matchPrefix("chsh"), err: errors.New("nope")},
		},
	}
	_, err := Apply(context.Background(), sb, &BoltedProfile{Shell: "zsh"}, NewCache(), "", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "chsh") {
		t.Errorf("expected chsh err, got %v", err)
	}
}

func TestApply_RemoteExists_Error(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, ".zshrc")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	sb := &scriptedBackend{
		Mock: mock.New(),
		t:    t,
		steps: []execStep{
			{match: matchPrefix("sh", "-c"), err: errors.New("vm gone")},
		},
	}
	_, err := Apply(context.Background(), sb, &BoltedProfile{Dotfiles: []string{".zshrc"}}, NewCache(), dir, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "probe") {
		t.Errorf("expected probe err, got %v", err)
	}
}

func TestApply_CopyDotfile_TeeError(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, ".zshrc")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	sb := &scriptedBackend{
		Mock: mock.New(),
		t:    t,
		steps: []execStep{
			// remoteExists: absent
			{match: matchPrefix("sh", "-c"), res: backend.ExecResult{ExitCode: 1}},
			// tee: fail
			{match: matchPrefix("sh", "-c"), res: backend.ExecResult{ExitCode: 1, Stderr: []byte("disk full")}},
		},
	}
	_, err := Apply(context.Background(), sb, &BoltedProfile{Dotfiles: []string{".zshrc"}}, NewCache(), dir, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "write") {
		t.Errorf("expected write err, got %v", err)
	}
}

func TestApply_CopyDotfile_ChmodError(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, ".zshrc")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	sb := &scriptedBackend{
		Mock: mock.New(),
		t:    t,
		steps: []execStep{
			{match: matchPrefix("sh", "-c"), res: backend.ExecResult{ExitCode: 1}}, // probe
			okStep(matchPrefix("sh", "-c")), // tee
			{match: matchPrefix("chmod"), err: errors.New("chmod fail")},
		},
	}
	_, err := Apply(context.Background(), sb, &BoltedProfile{Dotfiles: []string{".zshrc"}}, NewCache(), dir, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "chmod") {
		t.Errorf("expected chmod err, got %v", err)
	}
}

// --- wrapExec error formatting --------------------------------------------

func TestWrapExec_Variants(t *testing.T) {
	cases := []struct {
		name string
		res  backend.ExecResult
		err  error
		want string
	}{
		{"nil err nil exit", backend.ExecResult{ExitCode: 0}, nil, ""},
		{"err only", backend.ExecResult{}, errors.New("x"), "test: x"},
		{"err + stderr", backend.ExecResult{Stderr: []byte("oops")}, errors.New("x"), "test: x: oops"},
		{"exit + stderr", backend.ExecResult{ExitCode: 1, Stderr: []byte("nope")}, nil, "exit 1: nope"},
		{"exit only", backend.ExecResult{ExitCode: 1}, nil, "exit 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wrapExec("test", tc.res, tc.err)
			if tc.want == "" {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
				return
			}
			if got == nil || !strings.Contains(got.Error(), tc.want) {
				t.Errorf("err %v, want substring %q", got, tc.want)
			}
		})
	}
}

func TestFeatureSlug(t *testing.T) {
	got := featureSlug("ghcr.io/devcontainers/features/github-cli:1")
	want := "ghcr.io_devcontainers_features_github-cli_1"
	if got != want {
		t.Errorf("featureSlug = %q, want %q", got, want)
	}
}

func TestVMDotfilePath(t *testing.T) {
	got := vmDotfilePath(".zshrc")
	if !strings.Contains(got, ".zshrc") || !strings.Contains(got, "HOME") {
		t.Errorf("vmDotfilePath = %q", got)
	}
}

// --- helpers --------------------------------------------------------------

func withNow(t *testing.T, fn func() time.Time) {
	t.Helper()
	orig := nowFn
	t.Cleanup(func() { nowFn = orig })
	nowFn = fn
}

// defaultReadDotfile covers the production reader for coverage (the
// override-based tests above only exercise the indirection).
func TestDefaultReadDotfile_OK(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "f")
	if err := os.WriteFile(src, []byte("hello"), 0o640); err != nil {
		t.Fatal(err)
	}
	data, mode, err := defaultReadDotfile(src)
	if err != nil {
		t.Fatalf("defaultReadDotfile: %v", err)
	}
	if string(data) != "hello" || mode.Perm() != 0o640 {
		t.Errorf("got data=%q mode=%o", data, mode)
	}
}

func TestDefaultReadDotfile_ReadError(t *testing.T) {
	_, _, err := defaultReadDotfile(filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Error("expected error")
	}
}

func TestDefaultReadDotfile_ReadAfterStatError(t *testing.T) {
	// Stub the readFileFn so Stat succeeds but the read fails: this
	// is the post-stat error path that's otherwise unreachable.
	dir := t.TempDir()
	src := filepath.Join(dir, "g")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	orig := readFileFn
	t.Cleanup(func() { readFileFn = orig })
	readFileFn = func(string) ([]byte, error) { return nil, errors.New("read fail") }
	_, _, err := defaultReadDotfile(src)
	if err == nil || !strings.Contains(err.Error(), "read fail") {
		t.Errorf("expected read fail, got %v", err)
	}
}
