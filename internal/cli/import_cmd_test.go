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
	"time"

	"github.com/dahal/bolted/internal/backend"
)

// fileInfoStub satisfies os.FileInfo for tests that need a non-nil
// info with a specific IsDir() result.
type fileInfoStub struct {
	name  string
	isDir bool
}

func (f fileInfoStub) Name() string       { return f.name }
func (f fileInfoStub) Size() int64        { return 0 }
func (f fileInfoStub) Mode() os.FileMode  { return 0 }
func (f fileInfoStub) ModTime() time.Time { return time.Time{} }
func (f fileInfoStub) IsDir() bool        { return f.isDir }
func (f fileInfoStub) Sys() interface{}   { return nil }

// withImportStdin swaps the import stdin source for canned input.
func withImportStdin(t *testing.T, body string) {
	t.Helper()
	orig := importStdinFn
	t.Cleanup(func() { importStdinFn = orig })
	importStdinFn = func() io.Reader { return strings.NewReader(body) }
}

func withImportStat(t *testing.T, fn func(string) (os.FileInfo, error)) {
	t.Helper()
	orig := importStatFn
	t.Cleanup(func() { importStatFn = orig })
	importStatFn = fn
}

func withHostReadFile(t *testing.T, fn func(string) ([]byte, error)) {
	t.Helper()
	orig := hostReadFileFn
	t.Cleanup(func() { hostReadFileFn = orig })
	hostReadFileFn = fn
}

// ---- shellEscapeSingle ---------------------------------------------------

func TestShellEscapeSingle_PlainPath(t *testing.T) {
	if got := shellEscapeSingle("/a/b/c"); got != "'/a/b/c'" {
		t.Errorf("got %q", got)
	}
}

func TestShellEscapeSingle_EmbeddedQuote(t *testing.T) {
	got := shellEscapeSingle("a'b")
	if got != `'a'\''b'` {
		t.Errorf("got %q", got)
	}
}

func TestShellEscapeSingle_SpaceAndDollar(t *testing.T) {
	got := shellEscapeSingle("/p with space/$var")
	if got != `'/p with space/$var'` {
		t.Errorf("got %q", got)
	}
}

// ---- runImport ------------------------------------------------------------

func TestRunImport_RequireUnlockedFails(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	withStatStub(t, statMissing)
	err := runImport(context.Background(), io.Discard, io.Discard, "/host", "api", "dest", importOptions{yes: true})
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != exitLocked {
		t.Errorf("expected exit %d, got %d", exitLocked, code)
	}
}

func TestRunImport_RepoNotFound(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0}, // unlocked
		{ExitCode: 1}, // test -d
	}, nil)
	err := runImport(context.Background(), io.Discard, io.Discard, "/host", "missing", "dest", importOptions{yes: true})
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != exitRepoNotFound {
		t.Errorf("expected exit %d, got %d", exitRepoNotFound, code)
	}
}

func TestRunImport_StatHostError(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	withImportStat(t, func(string) (os.FileInfo, error) { return nil, fs.ErrNotExist })
	err := runImport(context.Background(), io.Discard, io.Discard, "/missing", "api", "dest", importOptions{yes: true})
	if err == nil || !strings.Contains(err.Error(), "stat host path") {
		t.Errorf("expected stat err, got %v", err)
	}
}

func TestRunImport_DirectoryRejected(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	withImportStat(t, func(string) (os.FileInfo, error) {
		return fileInfoStub{name: "dir", isDir: true}, nil
	})
	err := runImport(context.Background(), io.Discard, io.Discard, "/host/dir", "api", "dest", importOptions{yes: true})
	if err == nil || !strings.Contains(err.Error(), "directory import is not yet supported") {
		t.Errorf("expected dir-rejection err, got %v", err)
	}
}

func TestRunImport_PromptAccept(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0}, // unlocked
		{ExitCode: 0}, // test -d
		{ExitCode: 0}, // sh -c "cat > ..."
	}, nil)
	withImportStat(t, func(string) (os.FileInfo, error) { return fileInfoStub{name: "f", isDir: false}, nil })
	withHostReadFile(t, func(string) ([]byte, error) { return []byte("payload"), nil })
	withImportStdin(t, "y\n")
	var stderr bytes.Buffer
	if err := runImport(context.Background(), io.Discard, &stderr, "/host", "api", "dest", importOptions{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(stderr.String(), "This will copy") {
		t.Errorf("expected prompt, got %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "imported") {
		t.Errorf("expected success line, got %q", stderr.String())
	}
	// Verify the last backend Exec was the sh -c with our payload on stdin.
	calls := ds.scripted.Mock.Calls
	last := calls[len(calls)-1]
	if last.Method != "Exec" {
		t.Fatalf("expected last call Exec, got %s", last.Method)
	}
	if len(last.Cmd) != 3 || last.Cmd[0] != "sh" || last.Cmd[1] != "-c" {
		t.Errorf("unexpected cmd: %v", last.Cmd)
	}
	if !strings.Contains(last.Cmd[2], "cat > ") {
		t.Errorf("expected cat > in payload: %v", last.Cmd[2])
	}
	if !strings.Contains(last.Cmd[2], "/bolted/repos/api/dest") {
		t.Errorf("expected vmDest in payload: %v", last.Cmd[2])
	}
	if last.ExecOpts.Stdin == nil {
		t.Errorf("expected Stdin set on Exec")
	}
}

func TestRunImport_PromptDecline(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	withImportStat(t, func(string) (os.FileInfo, error) { return fileInfoStub{}, nil })
	withImportStdin(t, "n\n")
	var stderr bytes.Buffer
	err := runImport(context.Background(), io.Discard, &stderr, "/host", "api", "dest", importOptions{})
	if err == nil {
		t.Fatal("expected abort")
	}
	if !strings.Contains(stderr.String(), "aborted") {
		t.Errorf("expected aborted msg, got %q", stderr.String())
	}
}

func TestRunImport_PromptReadError(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	withImportStat(t, func(string) (os.FileInfo, error) { return fileInfoStub{}, nil })
	orig := importStdinFn
	t.Cleanup(func() { importStdinFn = orig })
	importStdinFn = func() io.Reader { return errReader{err: errors.New("read boom")} }
	err := runImport(context.Background(), io.Discard, io.Discard, "/host", "api", "dest", importOptions{})
	if err == nil || !strings.Contains(err.Error(), "read confirmation") {
		t.Errorf("expected confirm read err, got %v", err)
	}
}

func TestRunImport_ReadHostError(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	withImportStat(t, func(string) (os.FileInfo, error) { return fileInfoStub{}, nil })
	withHostReadFile(t, func(string) ([]byte, error) { return nil, errors.New("io boom") })
	err := runImport(context.Background(), io.Discard, io.Discard, "/host", "api", "dest", importOptions{yes: true})
	if err == nil || !strings.Contains(err.Error(), "read host path") {
		t.Errorf("expected read err, got %v", err)
	}
}

func TestRunImport_BackendExecError(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, []error{nil, nil, errors.New("backend boom")})
	withImportStat(t, func(string) (os.FileInfo, error) { return fileInfoStub{}, nil })
	withHostReadFile(t, func(string) ([]byte, error) { return []byte("x"), nil })
	err := runImport(context.Background(), io.Discard, io.Discard, "/host", "api", "dest", importOptions{yes: true})
	if err == nil || !strings.Contains(err.Error(), "write to volume") {
		t.Errorf("expected exec err, got %v", err)
	}
}

func TestRunImport_BackendNonZeroExit(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
		{ExitCode: 1, Stderr: []byte("no perms")},
	}, nil)
	withImportStat(t, func(string) (os.FileInfo, error) { return fileInfoStub{}, nil })
	withHostReadFile(t, func(string) ([]byte, error) { return []byte("x"), nil })
	err := runImport(context.Background(), io.Discard, io.Discard, "/host", "api", "dest", importOptions{yes: true})
	if err == nil || !strings.Contains(err.Error(), "no perms") {
		t.Errorf("expected non-zero err with stderr, got %v", err)
	}
}

func TestRunImport_HappyPathReadsRealHostFile(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	dir := t.TempDir()
	src := filepath.Join(dir, "in.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runImport(context.Background(), io.Discard, io.Discard, src, "api", "into", importOptions{yes: true}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	// Confirm the Stdin reader was attached and the cmd referenced
	// the expected vm path.
	calls := ds.scripted.Mock.Calls
	last := calls[len(calls)-1]
	if !strings.Contains(last.Cmd[2], "/bolted/repos/api/into") {
		t.Errorf("expected vm dest in cmd, got %v", last.Cmd[2])
	}
	if last.ExecOpts.Stdin == nil {
		t.Errorf("expected Stdin set")
	}
}

// ---- Cobra plumbing -------------------------------------------------------

func TestNewImportCmd_FlagsRegistered(t *testing.T) {
	cmd := newImportCmd()
	if !strings.HasPrefix(cmd.Use, "import") {
		t.Errorf("expected Use prefix 'import', got %q", cmd.Use)
	}
	if cmd.Flags().Lookup("yes") == nil {
		t.Error("expected --yes flag")
	}
}

func TestImportCmd_RunE_RequiresThreeArgs(t *testing.T) {
	cmd := newImportCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"/host", "api"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected arg-count error")
	}
}

func TestImportCmd_RunE_Dispatch(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	withStatStub(t, statMissing)
	cmd := newImportCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"/host", "api", "dest"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error from runImport (not initialised)")
	}
}

// ---- defensive: seam defaults --------------------------------------------

func TestImportStdinFn_Default(t *testing.T) {
	if importStdinFn() == nil {
		t.Error("expected non-nil reader")
	}
}

func TestImportFsSeams_DefaultsAreSet(t *testing.T) {
	if hostReadFileFn == nil || importStatFn == nil {
		t.Error("expected default fs seams to be set")
	}
}
