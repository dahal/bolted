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
)

// withExportStdin swaps the export stdin source for canned input.
func withExportStdin(t *testing.T, body string) {
	t.Helper()
	orig := exportStdinFn
	t.Cleanup(func() { exportStdinFn = orig })
	exportStdinFn = func() io.Reader { return strings.NewReader(body) }
}

// withHostStat swaps the host stat seam.
func withHostStat(t *testing.T, fn func(string) (os.FileInfo, error)) {
	t.Helper()
	orig := hostStatFn
	t.Cleanup(func() { hostStatFn = orig })
	hostStatFn = fn
}

// withHostWriteFile swaps the host write seam.
func withHostWriteFile(t *testing.T, fn func(string, []byte, os.FileMode) error) {
	t.Helper()
	orig := hostWriteFileFn
	t.Cleanup(func() { hostWriteFileFn = orig })
	hostWriteFileFn = fn
}

// ---- isVMDir --------------------------------------------------------------

func TestIsVMDir_ExecExitZero(t *testing.T) {
	be := &scriptedBackend{
		Mock:       mock.New(),
		execScript: []backend.ExecResult{{ExitCode: 0}},
	}
	ok, err := isVMDir(context.Background(), be, "/dir")
	if err != nil || !ok {
		t.Errorf("expected ok=true err=nil, got %v %v", ok, err)
	}
}

func TestIsVMDir_ExecExitNonZero(t *testing.T) {
	be := &scriptedBackend{
		Mock:       mock.New(),
		execScript: []backend.ExecResult{{ExitCode: 1}},
	}
	ok, err := isVMDir(context.Background(), be, "/file")
	if err != nil || ok {
		t.Errorf("expected ok=false err=nil, got %v %v", ok, err)
	}
}

func TestIsVMDir_ExecError(t *testing.T) {
	be := &scriptedBackend{
		Mock:     mock.New(),
		execErrs: []error{errors.New("boom")},
	}
	if _, err := isVMDir(context.Background(), be, "/x"); err == nil {
		t.Error("expected error")
	}
}

// ---- runExport ------------------------------------------------------------

func TestRunExport_RequireUnlockedFails(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	withStatStub(t, statMissing)
	err := runExport(context.Background(), io.Discard, io.Discard, "api", "/p", "/host", exportOptions{yes: true})
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != exitLocked {
		t.Errorf("expected exit %d, got %d", exitLocked, code)
	}
}

func TestRunExport_RepoNotFound(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0}, // unlocked
		{ExitCode: 1}, // test -d
	}, nil)
	err := runExport(context.Background(), io.Discard, io.Discard, "missing", "/p", "/host", exportOptions{yes: true})
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != exitRepoNotFound {
		t.Errorf("expected exit %d, got %d", exitRepoNotFound, code)
	}
}

func TestRunExport_IsVMDirProbeError(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0}, // unlocked
		{ExitCode: 0}, // test -d (repo)
	}, []error{nil, nil, errors.New("probe boom")})
	err := runExport(context.Background(), io.Discard, io.Discard, "api", "/p", "/host", exportOptions{yes: true})
	if err == nil || !strings.Contains(err.Error(), "probe /p") {
		t.Errorf("expected probe err, got %v", err)
	}
}

func TestRunExport_DirectoryRejected(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0}, // unlocked
		{ExitCode: 0}, // test -d (repo exists)
		{ExitCode: 0}, // test -d (vm-path is a dir)
	}, nil)
	err := runExport(context.Background(), io.Discard, io.Discard, "api", "/some/dir", "/host", exportOptions{yes: true})
	if err == nil || !strings.Contains(err.Error(), "directory export is not yet supported") {
		t.Errorf("expected dir-rejection err, got %v", err)
	}
}

func TestRunExport_HostExistsNoForce(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
		{ExitCode: 1}, // test -d on vm-path: not a dir
	}, nil)
	withHostStat(t, statExists) // pretend host path exists
	err := runExport(context.Background(), io.Discard, io.Discard, "api", "/p", "/host", exportOptions{yes: true})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected exists err, got %v", err)
	}
}

func TestRunExport_HostExistsWithForce(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
		{ExitCode: 1},                                  // not a dir
		{ExitCode: 0, Stdout: []byte("payload bytes")}, // cat
	}, nil)
	withHostStat(t, statExists)
	var wrote []byte
	withHostWriteFile(t, func(_ string, data []byte, _ os.FileMode) error {
		wrote = append([]byte(nil), data...)
		return nil
	})
	if err := runExport(context.Background(), io.Discard, io.Discard, "api", "/p", "/host", exportOptions{yes: true, force: true}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if string(wrote) != "payload bytes" {
		t.Errorf("expected payload bytes written, got %q", wrote)
	}
}

func TestRunExport_HostStatOtherError(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
		{ExitCode: 1},
	}, nil)
	withHostStat(t, func(string) (os.FileInfo, error) { return nil, errors.New("io boom") })
	err := runExport(context.Background(), io.Discard, io.Discard, "api", "/p", "/host", exportOptions{yes: true})
	if err == nil || !strings.Contains(err.Error(), "stat host dest") {
		t.Errorf("expected stat err, got %v", err)
	}
}

func TestRunExport_PromptAccept(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
		{ExitCode: 1},                          // not a dir
		{ExitCode: 0, Stdout: []byte("hi\n")}, // cat
	}, nil)
	withHostStat(t, func(string) (os.FileInfo, error) { return nil, fs.ErrNotExist })
	withExportStdin(t, "y\n")
	var captured []byte
	withHostWriteFile(t, func(_ string, data []byte, _ os.FileMode) error {
		captured = append([]byte(nil), data...)
		return nil
	})
	var stderr bytes.Buffer
	if err := runExport(context.Background(), io.Discard, &stderr, "api", "/file", "/host", exportOptions{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if string(captured) != "hi\n" {
		t.Errorf("expected hi written, got %q", captured)
	}
	if !strings.Contains(stderr.String(), "This will copy") {
		t.Errorf("expected confirm prompt, got %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "exported") {
		t.Errorf("expected success line, got %q", stderr.String())
	}
}

func TestRunExport_PromptDecline(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
		{ExitCode: 1},
	}, nil)
	withHostStat(t, func(string) (os.FileInfo, error) { return nil, fs.ErrNotExist })
	withExportStdin(t, "n\n")
	var stderr bytes.Buffer
	err := runExport(context.Background(), io.Discard, &stderr, "api", "/file", "/host", exportOptions{})
	if err == nil {
		t.Fatal("expected abort error")
	}
	if !strings.Contains(stderr.String(), "aborted") {
		t.Errorf("expected aborted msg, got %q", stderr.String())
	}
}

func TestRunExport_PromptReadError(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
		{ExitCode: 1},
	}, nil)
	withHostStat(t, func(string) (os.FileInfo, error) { return nil, fs.ErrNotExist })
	orig := exportStdinFn
	t.Cleanup(func() { exportStdinFn = orig })
	exportStdinFn = func() io.Reader { return errReader{err: errors.New("read boom")} }
	err := runExport(context.Background(), io.Discard, io.Discard, "api", "/p", "/host", exportOptions{})
	if err == nil || !strings.Contains(err.Error(), "read confirmation") {
		t.Errorf("expected confirm read err, got %v", err)
	}
}

func TestRunExport_CatExecError(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
		{ExitCode: 1},
	}, []error{nil, nil, nil, errors.New("cat boom")})
	withHostStat(t, func(string) (os.FileInfo, error) { return nil, fs.ErrNotExist })
	err := runExport(context.Background(), io.Discard, io.Discard, "api", "/p", "/host", exportOptions{yes: true})
	if err == nil || !strings.Contains(err.Error(), "read /p") {
		t.Errorf("expected cat err, got %v", err)
	}
}

func TestRunExport_CatNonZeroExit(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
		{ExitCode: 1},
		{ExitCode: 2, Stderr: []byte("no such file")},
	}, nil)
	withHostStat(t, func(string) (os.FileInfo, error) { return nil, fs.ErrNotExist })
	err := runExport(context.Background(), io.Discard, io.Discard, "api", "/p", "/host", exportOptions{yes: true})
	if err == nil || !strings.Contains(err.Error(), "read /p") || !strings.Contains(err.Error(), "no such file") {
		t.Errorf("expected exit-2 err, got %v", err)
	}
}

func TestRunExport_HostWriteError(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
		{ExitCode: 1},
		{ExitCode: 0, Stdout: []byte("data")},
	}, nil)
	withHostStat(t, func(string) (os.FileInfo, error) { return nil, fs.ErrNotExist })
	withHostWriteFile(t, func(string, []byte, os.FileMode) error { return errors.New("write boom") })
	err := runExport(context.Background(), io.Discard, io.Discard, "api", "/p", "/host", exportOptions{yes: true})
	if err == nil || !strings.Contains(err.Error(), "write host dest") {
		t.Errorf("expected write err, got %v", err)
	}
}

func TestRunExport_HappyPathWithRealHostWrite(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
		{ExitCode: 1},                          // not a dir
		{ExitCode: 0, Stdout: []byte("hi!\n")}, // cat
	}, nil)
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.txt")
	if err := runExport(context.Background(), io.Discard, io.Discard, "api", "/p", dest, exportOptions{yes: true}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read out: %v", err)
	}
	if string(data) != "hi!\n" {
		t.Errorf("got %q, want hi!\\n", data)
	}
}

// ---- Cobra plumbing -------------------------------------------------------

func TestNewExportCmd_FlagsRegistered(t *testing.T) {
	cmd := newExportCmd()
	if !strings.HasPrefix(cmd.Use, "export") {
		t.Errorf("expected Use prefix 'export', got %q", cmd.Use)
	}
	if cmd.Flags().Lookup("yes") == nil {
		t.Error("expected --yes flag")
	}
	if cmd.Flags().Lookup("force") == nil {
		t.Error("expected --force flag")
	}
}

func TestExportCmd_RunE_RequiresThreeArgs(t *testing.T) {
	cmd := newExportCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"api", "/p"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected arg-count error")
	}
}

func TestExportCmd_RunE_Dispatch(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	withStatStub(t, statMissing) // fail-fast inside runExport
	cmd := newExportCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"api", "/p", "/host"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error from runExport (not initialised)")
	}
}

// ---- defensive: default seams not nil ------------------------------------

func TestExportStdinFn_Default(t *testing.T) {
	if exportStdinFn() == nil {
		t.Error("expected non-nil reader")
	}
}

func TestHostFsSeams_DefaultsAreSet(t *testing.T) {
	if hostWriteFileFn == nil || hostStatFn == nil {
		t.Error("expected default host-fs seams to be set")
	}
}
