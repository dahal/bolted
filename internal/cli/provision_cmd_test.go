package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dahal/bolted/internal/backend"
	"github.com/dahal/bolted/internal/backend/mock"
	"github.com/dahal/bolted/internal/provision"
)

// withProvisionStubs swaps every provision-package indirection for one
// test and returns a struct so tests can record calls / inject errs.
type provStubs struct {
	loadProfile *provision.BoltedProfile
	loadErr     error
	cache       *provision.Cache
	cacheErr    error
	saveErr     error
	applyRes    *provision.Result
	applyErr    error
	checkDrift  bool
	checkSum    string
	checkErr    error
	fetchBytes  []byte
	fetchErr    error
	writeErr    error
	mkdirErr    error

	saveCalled  int
	applyCalled int
	fetchCalled int
	writeCalled int
	wroteBytes  []byte
	wrotePath   string
}

func (s *provStubs) install(t *testing.T) {
	t.Helper()
	origLoad := provisionLoadFn
	origCache := provisionLoadCacheFn
	origSave := provisionSaveCacheFn
	origApply := provisionApplyFn
	origCheck := provisionCheckFn
	origFetch := provisionFetchFn
	origWrite := provisionWriteFileFn
	origMkdir := provisionMkdirAllFn
	t.Cleanup(func() {
		provisionLoadFn = origLoad
		provisionLoadCacheFn = origCache
		provisionSaveCacheFn = origSave
		provisionApplyFn = origApply
		provisionCheckFn = origCheck
		provisionFetchFn = origFetch
		provisionWriteFileFn = origWrite
		provisionMkdirAllFn = origMkdir
	})
	provisionLoadFn = func(string) (*provision.BoltedProfile, error) {
		if s.loadErr != nil {
			return nil, s.loadErr
		}
		if s.loadProfile != nil {
			return s.loadProfile, nil
		}
		return &provision.BoltedProfile{}, nil
	}
	provisionLoadCacheFn = func(string) (*provision.Cache, error) {
		if s.cacheErr != nil {
			return nil, s.cacheErr
		}
		if s.cache != nil {
			return s.cache, nil
		}
		return provision.NewCache(), nil
	}
	provisionSaveCacheFn = func(string, *provision.Cache) error {
		s.saveCalled++
		return s.saveErr
	}
	provisionApplyFn = func(_ context.Context, _ backend.Backend, _ *provision.BoltedProfile, _ *provision.Cache, _ string, _ io.Writer) (*provision.Result, error) {
		s.applyCalled++
		if s.applyErr != nil {
			return s.applyRes, s.applyErr
		}
		if s.applyRes != nil {
			return s.applyRes, nil
		}
		return &provision.Result{}, nil
	}
	provisionCheckFn = func(backend.Backend, *provision.BoltedProfile, *provision.Cache) (bool, string, error) {
		return s.checkDrift, s.checkSum, s.checkErr
	}
	provisionFetchFn = func(string) ([]byte, error) {
		s.fetchCalled++
		return s.fetchBytes, s.fetchErr
	}
	provisionWriteFileFn = func(path string, data []byte, _ fs.FileMode) error {
		s.writeCalled++
		s.wrotePath = path
		s.wroteBytes = data
		return s.writeErr
	}
	provisionMkdirAllFn = func(string, fs.FileMode) error {
		return s.mkdirErr
	}
}

// unlocked returns a mock backend where IsRunning=true and the
// `ls /bolted/repos` probe returns exit 0 (so isLocked == false).
func unlockedMock() *mock.Mock {
	m := mock.New()
	m.IsRunningResult = true
	m.ExecResult = backend.ExecResult{ExitCode: 0}
	return m
}

// --- guards ---------------------------------------------------------------

func TestRunProvision_NotInitialised(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	(&provStubs{}).install(t)
	withStatStub(t, statMissing)

	var stderr bytes.Buffer
	err := runProvision(context.Background(), io.Discard, &stderr, provisionOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if exitCodeFromError(err) != exitLocked {
		t.Errorf("expected exit %d, got %d", exitLocked, exitCodeFromError(err))
	}
	if !strings.Contains(stderr.String(), "bolt init") {
		t.Errorf("expected hint, got %q", stderr.String())
	}
}

func TestRunProvision_StatGenericError(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	(&provStubs{}).install(t)
	want := errors.New("disk gone")
	withStatStub(t, func(string) (os.FileInfo, error) { return nil, want })

	err := runProvision(context.Background(), io.Discard, io.Discard, provisionOptions{})
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped err, got %v", err)
	}
}

func TestRunProvision_LoadConfigFails(t *testing.T) {
	want := errors.New("bad yaml")
	s := &lifecycleStubs{cfgErr: want}
	s.install(t)
	(&provStubs{}).install(t)
	withStatStub(t, statExists)

	err := runProvision(context.Background(), io.Discard, io.Discard, provisionOptions{})
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped err, got %v", err)
	}
}

func TestRunProvision_BackendInitFails(t *testing.T) {
	want := errors.New("be fail")
	s := &lifecycleStubs{backendErr: want}
	s.install(t)
	(&provStubs{}).install(t)
	withStatStub(t, statExists)

	err := runProvision(context.Background(), io.Discard, io.Discard, provisionOptions{})
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped err, got %v", err)
	}
}

func TestRunProvision_IsRunningError(t *testing.T) {
	m := mock.New()
	m.ErrIsRunning = errors.New("query failed")
	s := &lifecycleStubs{mockBE: m}
	s.install(t)
	(&provStubs{}).install(t)
	withStatStub(t, statExists)

	err := runProvision(context.Background(), io.Discard, io.Discard, provisionOptions{})
	if err == nil || !strings.Contains(err.Error(), "query failed") {
		t.Errorf("expected IsRunning error, got %v", err)
	}
}

func TestRunProvision_VMNotRunning(t *testing.T) {
	m := mock.New()
	m.IsRunningResult = false
	s := &lifecycleStubs{mockBE: m}
	s.install(t)
	(&provStubs{}).install(t)
	withStatStub(t, statExists)

	var stderr bytes.Buffer
	err := runProvision(context.Background(), io.Discard, &stderr, provisionOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if exitCodeFromError(err) != exitLocked {
		t.Errorf("expected exit %d, got %d", exitLocked, exitCodeFromError(err))
	}
	if !strings.Contains(stderr.String(), "bolt unlock") {
		t.Errorf("expected unlock hint, got %q", stderr.String())
	}
}

func TestRunProvision_BoltedLocked(t *testing.T) {
	m := mock.New()
	m.IsRunningResult = true
	m.ExecResult = backend.ExecResult{ExitCode: 1} // ls fails
	s := &lifecycleStubs{mockBE: m}
	s.install(t)
	(&provStubs{}).install(t)
	withStatStub(t, statExists)

	var stderr bytes.Buffer
	err := runProvision(context.Background(), io.Discard, &stderr, provisionOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if exitCodeFromError(err) != exitLocked {
		t.Errorf("expected exit %d, got %d", exitLocked, exitCodeFromError(err))
	}
	if !strings.Contains(stderr.String(), "bolt unlock") {
		t.Errorf("expected unlock hint, got %q", stderr.String())
	}
}

// --- --from --------------------------------------------------------------

func TestRunProvision_From_FetchFails(t *testing.T) {
	s := &lifecycleStubs{mockBE: unlockedMock()}
	s.install(t)
	ps := &provStubs{fetchErr: errors.New("net down")}
	ps.install(t)
	withStatStub(t, statExists)

	err := runProvision(context.Background(), io.Discard, io.Discard, provisionOptions{fromURL: "https://x"})
	if err == nil || !strings.Contains(err.Error(), "fetch") {
		t.Errorf("expected fetch err, got %v", err)
	}
}

func TestRunProvision_From_MkdirFails(t *testing.T) {
	s := &lifecycleStubs{mockBE: unlockedMock()}
	s.install(t)
	ps := &provStubs{fetchBytes: []byte("x"), mkdirErr: errors.New("mkdir fail")}
	ps.install(t)
	withStatStub(t, statExists)

	err := runProvision(context.Background(), io.Discard, io.Discard, provisionOptions{fromURL: "https://x"})
	if err == nil || !strings.Contains(err.Error(), "mkdir") {
		t.Errorf("expected mkdir err, got %v", err)
	}
}

func TestRunProvision_From_WriteFails(t *testing.T) {
	s := &lifecycleStubs{mockBE: unlockedMock()}
	s.install(t)
	ps := &provStubs{fetchBytes: []byte("x"), writeErr: errors.New("write fail")}
	ps.install(t)
	withStatStub(t, statExists)

	err := runProvision(context.Background(), io.Discard, io.Discard, provisionOptions{fromURL: "https://x"})
	if err == nil || !strings.Contains(err.Error(), "write bolted.yaml") {
		t.Errorf("expected write err, got %v", err)
	}
}

func TestRunProvision_From_OK_PersistsYAML(t *testing.T) {
	s := &lifecycleStubs{mockBE: unlockedMock()}
	s.install(t)
	body := []byte("features: []\n")
	ps := &provStubs{fetchBytes: body}
	ps.install(t)
	withStatStub(t, statExists)

	if err := runProvision(context.Background(), io.Discard, io.Discard, provisionOptions{fromURL: "https://x"}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if ps.writeCalled != 1 || string(ps.wroteBytes) != string(body) {
		t.Errorf("expected bolted.yaml written, got calls=%d body=%q", ps.writeCalled, ps.wroteBytes)
	}
	if !strings.HasSuffix(ps.wrotePath, filepath.Join(".bolted", "bolted.yaml")) &&
		!strings.HasSuffix(ps.wrotePath, "bolted.yaml") {
		t.Errorf("unexpected write path: %q", ps.wrotePath)
	}
}

// --- load / cache errors --------------------------------------------------

func TestRunProvision_NoBoltedYAML(t *testing.T) {
	s := &lifecycleStubs{mockBE: unlockedMock()}
	s.install(t)
	ps := &provStubs{loadErr: fs.ErrNotExist}
	ps.install(t)
	withStatStub(t, statExists)

	var stderr bytes.Buffer
	err := runProvision(context.Background(), io.Discard, &stderr, provisionOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if exitCodeFromError(err) != exitGeneric {
		t.Errorf("expected exit %d, got %d", exitGeneric, exitCodeFromError(err))
	}
	if !strings.Contains(stderr.String(), "--from") {
		t.Errorf("expected --from hint, got %q", stderr.String())
	}
}

func TestRunProvision_LoadFails_Generic(t *testing.T) {
	s := &lifecycleStubs{mockBE: unlockedMock()}
	s.install(t)
	ps := &provStubs{loadErr: errors.New("parse fail")}
	ps.install(t)
	withStatStub(t, statExists)

	err := runProvision(context.Background(), io.Discard, io.Discard, provisionOptions{})
	if err == nil || !strings.Contains(err.Error(), "load bolted.yaml") {
		t.Errorf("expected load err, got %v", err)
	}
}

func TestRunProvision_LoadCacheFails(t *testing.T) {
	s := &lifecycleStubs{mockBE: unlockedMock()}
	s.install(t)
	ps := &provStubs{cacheErr: errors.New("cache fail")}
	ps.install(t)
	withStatStub(t, statExists)

	err := runProvision(context.Background(), io.Discard, io.Discard, provisionOptions{})
	if err == nil || !strings.Contains(err.Error(), "load cache") {
		t.Errorf("expected load cache err, got %v", err)
	}
}

// --- --check --------------------------------------------------------------

func TestRunProvision_Check_InSync(t *testing.T) {
	s := &lifecycleStubs{mockBE: unlockedMock()}
	s.install(t)
	ps := &provStubs{}
	ps.install(t)
	withStatStub(t, statExists)

	var stdout bytes.Buffer
	err := runProvision(context.Background(), &stdout, io.Discard, provisionOptions{check: true})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(stdout.String(), "in sync") {
		t.Errorf("expected in-sync output, got %q", stdout.String())
	}
}

func TestRunProvision_Check_Drifted(t *testing.T) {
	s := &lifecycleStubs{mockBE: unlockedMock()}
	s.install(t)
	ps := &provStubs{checkDrift: true, checkSum: "features +x"}
	ps.install(t)
	withStatStub(t, statExists)

	var stdout bytes.Buffer
	err := runProvision(context.Background(), &stdout, io.Discard, provisionOptions{check: true})
	if err == nil {
		t.Fatal("expected drift error")
	}
	if exitCodeFromError(err) != exitGeneric {
		t.Errorf("expected exit %d, got %d", exitGeneric, exitCodeFromError(err))
	}
	if !strings.Contains(stdout.String(), "drift:") {
		t.Errorf("expected drift output, got %q", stdout.String())
	}
}

func TestRunProvision_Check_Error(t *testing.T) {
	s := &lifecycleStubs{mockBE: unlockedMock()}
	s.install(t)
	ps := &provStubs{checkErr: errors.New("check fail")}
	ps.install(t)
	withStatStub(t, statExists)

	err := runProvision(context.Background(), io.Discard, io.Discard, provisionOptions{check: true})
	if err == nil || !strings.Contains(err.Error(), "check drift") {
		t.Errorf("expected check drift err, got %v", err)
	}
}

// --- apply path ----------------------------------------------------------

func TestRunProvision_Apply_OK(t *testing.T) {
	s := &lifecycleStubs{mockBE: unlockedMock()}
	s.install(t)
	ps := &provStubs{applyRes: &provision.Result{
		FeaturesAdded:    []string{"a:1"},
		PackagesAdded:    []string{"jq"},
		GitConfigApplied: 2,
		ShellSet:         true,
		DotfilesChanged:  []string{".zshrc"},
	}}
	ps.install(t)
	withStatStub(t, statExists)

	var stdout bytes.Buffer
	if err := runProvision(context.Background(), &stdout, io.Discard, provisionOptions{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if ps.applyCalled != 1 || ps.saveCalled != 1 {
		t.Errorf("expected 1 apply + 1 save, got apply=%d save=%d", ps.applyCalled, ps.saveCalled)
	}
	out := stdout.String()
	for _, want := range []string{"features:  +1", "packages:  +1", "gitconfig: 2", "shell:     changed", "dotfiles:  1"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in stdout: %s", want, out)
		}
	}
}

func TestRunProvision_Apply_Error_StillSavesCache(t *testing.T) {
	s := &lifecycleStubs{mockBE: unlockedMock()}
	s.install(t)
	ps := &provStubs{applyErr: errors.New("apk fail")}
	ps.install(t)
	withStatStub(t, statExists)

	err := runProvision(context.Background(), io.Discard, io.Discard, provisionOptions{})
	if err == nil || !strings.Contains(err.Error(), "apply") {
		t.Errorf("expected apply err, got %v", err)
	}
	if ps.saveCalled != 1 {
		t.Errorf("expected cache save attempt on apply failure, got %d", ps.saveCalled)
	}
}

func TestRunProvision_SaveCacheError(t *testing.T) {
	s := &lifecycleStubs{mockBE: unlockedMock()}
	s.install(t)
	ps := &provStubs{saveErr: errors.New("save fail")}
	ps.install(t)
	withStatStub(t, statExists)

	err := runProvision(context.Background(), io.Discard, io.Discard, provisionOptions{})
	if err == nil || !strings.Contains(err.Error(), "save cache") {
		t.Errorf("expected save cache err, got %v", err)
	}
}

// --- Cobra plumbing ------------------------------------------------------

func TestNewProvisionCmd_FlagsRegistered(t *testing.T) {
	cmd := newProvisionCmd()
	if cmd.Flags().Lookup("check") == nil {
		t.Error("expected --check flag")
	}
	if cmd.Flags().Lookup("from") == nil {
		t.Error("expected --from flag")
	}
	if cmd.Use != "provision" {
		t.Errorf("expected Use=provision, got %q", cmd.Use)
	}
}

func TestProvisionCmd_RunE_HappyPath(t *testing.T) {
	s := &lifecycleStubs{mockBE: unlockedMock()}
	s.install(t)
	(&provStubs{}).install(t)
	withStatStub(t, statExists)

	cmd := newProvisionCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestBoltedYAMLPath_UsesBoltedDir(t *testing.T) {
	orig := boltedDirFn
	t.Cleanup(func() { boltedDirFn = orig })
	boltedDirFn = func() string { return "/abs/bolt" }

	if got, want := boltedYAMLPath(), filepath.Join("/abs/bolt", "bolted.yaml"); got != want {
		t.Errorf("boltedYAMLPath = %q, want %q", got, want)
	}
	if got, want := stateDirPath(), filepath.Join("/abs/bolt", "state"); got != want {
		t.Errorf("stateDirPath = %q, want %q", got, want)
	}
}

// --- coverage: default indirections ---------------------------------------
//
// The provisionXxxFn defaults thinly wrap provision.* functions; the
// production code path is exercised by every other test indirectly via
// the stub. These tests cover the unwrapped defaults so the indirection
// itself doesn't sit at zero coverage.

func TestProvisionDefaults_Wrap(t *testing.T) {
	// Restore originals after every override so we observe defaults.
	for _, fn := range []func(*testing.T){
		func(t *testing.T) {
			// Load default wraps provision.Load — missing file
			// returns a wrapped fs.ErrNotExist.
			_, err := provisionLoadFn(filepath.Join(t.TempDir(), "missing"))
			if err == nil {
				t.Error("expected err on missing yaml")
			}
		},
		func(t *testing.T) {
			// LoadCache default returns empty cache on missing.
			c, err := provisionLoadCacheFn(t.TempDir())
			if err != nil || c == nil {
				t.Errorf("LoadCache default: c=%v err=%v", c, err)
			}
		},
		func(t *testing.T) {
			err := provisionSaveCacheFn(t.TempDir(), provision.NewCache())
			if err != nil {
				t.Errorf("SaveCache default: %v", err)
			}
		},
		func(t *testing.T) {
			_, err := provisionApplyFn(context.Background(), mock.New(), &provision.BoltedProfile{}, provision.NewCache(), "", io.Discard)
			if err != nil {
				t.Errorf("Apply default: %v", err)
			}
		},
		func(t *testing.T) {
			_, _, err := provisionCheckFn(mock.New(), &provision.BoltedProfile{}, provision.NewCache())
			if err != nil {
				t.Errorf("Check default: %v", err)
			}
		},
		func(t *testing.T) {
			_, err := provisionFetchFn(filepath.Join(t.TempDir(), "missing"))
			if err == nil {
				t.Error("expected fetch err")
			}
		},
		func(t *testing.T) {
			dir := t.TempDir()
			if err := provisionMkdirAllFn(filepath.Join(dir, "x"), 0o700); err != nil {
				t.Errorf("MkdirAll default: %v", err)
			}
			if err := provisionWriteFileFn(filepath.Join(dir, "f"), []byte("x"), 0o600); err != nil {
				t.Errorf("WriteFile default: %v", err)
			}
		},
	} {
		fn(t)
	}
}
