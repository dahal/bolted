package cli

import (
	"bytes"
	"context"
	"encoding/json"
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

// ---- statFn stubbing -------------------------------------------------------

// withStatStub swaps statFn for the duration of one test.
func withStatStub(t *testing.T, fn func(string) (os.FileInfo, error)) {
	t.Helper()
	orig := statFn
	t.Cleanup(func() { statFn = orig })
	statFn = fn
}

// statExists is a convenience: pretend the path exists. The returned
// os.FileInfo is nil (no caller in status_cmd inspects it).
func statExists(string) (os.FileInfo, error) { return nil, nil }

// statMissing returns fs.ErrNotExist for any path.
func statMissing(string) (os.FileInfo, error) { return nil, fs.ErrNotExist }

// ---- runStatus -------------------------------------------------------------

func TestRunStatus_NotInitialised(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	withStatStub(t, statMissing)

	var stderr bytes.Buffer
	err := runStatus(context.Background(), io.Discard, &stderr, statusOptions{})
	if err == nil {
		t.Fatal("expected exit-2 error")
	}
	if code := exitCodeFromError(err); code != exitLocked {
		t.Errorf("expected exit code %d, got %d (err=%v)", exitLocked, code, err)
	}
	if !strings.Contains(stderr.String(), "bolt init") {
		t.Errorf("expected 'bolt init' hint, got: %q", stderr.String())
	}
}

func TestRunStatus_StatError(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	want := errors.New("permission denied")
	withStatStub(t, func(string) (os.FileInfo, error) { return nil, want })

	err := runStatus(context.Background(), io.Discard, io.Discard, statusOptions{})
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped stat error, got %v", err)
	}
}

func TestRunStatus_LoadConfigFails(t *testing.T) {
	want := errors.New("bad yaml")
	s := &lifecycleStubs{cfgErr: want}
	s.install(t)
	withStatStub(t, statExists)

	err := runStatus(context.Background(), io.Discard, io.Discard, statusOptions{})
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped load error, got %v", err)
	}
}

func TestRunStatus_BackendInitFails(t *testing.T) {
	want := errors.New("be init")
	s := &lifecycleStubs{backendErr: want}
	s.install(t)
	withStatStub(t, statExists)

	err := runStatus(context.Background(), io.Discard, io.Discard, statusOptions{})
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped backend error, got %v", err)
	}
}

func TestRunStatus_IsRunningFails(t *testing.T) {
	want := errors.New("is running")
	s := &lifecycleStubs{mockBE: mock.New()}
	s.mockBE.ErrIsRunning = want
	s.install(t)
	withStatStub(t, statExists)

	err := runStatus(context.Background(), io.Discard, io.Discard, statusOptions{})
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped IsRunning error, got %v", err)
	}
}

func TestRunStatus_VMStoppedShowsLocked(t *testing.T) {
	s := &lifecycleStubs{mockBE: mock.New()}
	s.mockBE.IsRunningResult = false
	s.install(t)
	withStatStub(t, statExists)

	var stdout bytes.Buffer
	if err := runStatus(context.Background(), &stdout, io.Discard, statusOptions{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "Lock:       locked") {
		t.Errorf("expected locked summary, got: %q", out)
	}
	if !strings.Contains(out, "VM:         stopped") {
		t.Errorf("expected stopped state, got: %q", out)
	}
	// No Exec call should be made when the VM is stopped.
	for _, m := range s.mockBE.Methods() {
		if m == "Exec" {
			t.Errorf("did not expect Exec when VM stopped, methods=%v", s.mockBE.Methods())
		}
	}
}

func TestRunStatus_VMRunningButLocked(t *testing.T) {
	// IsRunning=true but the `ls /bolted/repos` probe returns a non-
	// zero exit code → "locked" branch.
	m := mock.New()
	m.IsRunningResult = true
	m.ExecResult = backend.ExecResult{ExitCode: 2, Stderr: []byte("ls: not found")}
	s := &lifecycleStubs{mockBE: m}
	s.install(t)
	withStatStub(t, statExists)

	var stdout, stderr bytes.Buffer
	if err := runStatus(context.Background(), &stdout, &stderr, statusOptions{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(stdout.String(), "Lock:       locked") {
		t.Errorf("expected locked, got: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Repos:      (unavailable") {
		t.Errorf("expected unavailable repos line, got: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "bolt unlock") {
		t.Errorf("expected unlock hint on stderr, got: %q", stderr.String())
	}
}

// scriptedBackend wraps a Mock to vary Exec results per call. The Mock
// itself only knows about a single ExecResult; status' "ls" + "free" need
// distinct returns.
type scriptedBackend struct {
	*mock.Mock
	execScript []backend.ExecResult
	execErrs   []error
	execCalls  int
}

func (s *scriptedBackend) Exec(ctx context.Context, cmd []string, opts backend.ExecOpts) (backend.ExecResult, error) {
	_, _ = s.Mock.Exec(ctx, cmd, opts) // record on the mock
	idx := s.execCalls
	s.execCalls++
	var res backend.ExecResult
	if idx < len(s.execScript) {
		res = s.execScript[idx]
	}
	var err error
	if idx < len(s.execErrs) {
		err = s.execErrs[idx]
	}
	return res, err
}

func TestRunStatus_UnlockedListsReposAndRSS(t *testing.T) {
	scripted := &scriptedBackend{
		Mock: mock.New(),
		execScript: []backend.ExecResult{
			// ls /bolted/repos
			{ExitCode: 0, Stdout: []byte("alpha\nbeta\nbeta\n  \ncharlie\n")},
			// free -m
			{ExitCode: 0, Stdout: []byte("              total        used        free\nMem:           7891        1234         500\nSwap:             0           0           0\n")},
		},
	}
	scripted.Mock.IsRunningResult = true

	origNew := newBackendFn
	t.Cleanup(func() { newBackendFn = origNew })
	newBackendFn = func(_ backend.Config) (backend.Backend, error) { return scripted, nil }

	// Set up the rest with the standard stubs (we override newBackendFn
	// after install).
	s := &lifecycleStubs{}
	s.install(t)
	newBackendFn = func(_ backend.Config) (backend.Backend, error) { return scripted, nil }
	withStatStub(t, statExists)

	var stdout bytes.Buffer
	if err := runStatus(context.Background(), &stdout, io.Discard, statusOptions{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "Lock:       unlocked") {
		t.Errorf("expected unlocked, got: %q", out)
	}
	if !strings.Contains(out, "- alpha") || !strings.Contains(out, "- beta") || !strings.Contains(out, "- charlie") {
		t.Errorf("expected sorted dedup repos in output: %q", out)
	}
	// beta should appear once even though the listing contained it twice.
	if strings.Count(out, "- beta") != 1 {
		t.Errorf("expected beta exactly once, got: %q", out)
	}
	if !strings.Contains(out, "VM RSS:     1234 MiB") {
		t.Errorf("expected RSS line, got: %q", out)
	}
}

func TestRunStatus_RSSExecError_OmitsField(t *testing.T) {
	scripted := &scriptedBackend{
		Mock: mock.New(),
		execScript: []backend.ExecResult{
			{ExitCode: 0, Stdout: []byte("")}, // ls ok, no repos
			{},                                // free result (ignored due to err)
		},
		execErrs: []error{nil, errors.New("free not installed")},
	}
	scripted.Mock.IsRunningResult = true

	s := &lifecycleStubs{}
	s.install(t)
	newBackendFn = func(_ backend.Config) (backend.Backend, error) { return scripted, nil }
	withStatStub(t, statExists)

	var stdout bytes.Buffer
	if err := runStatus(context.Background(), &stdout, io.Discard, statusOptions{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if strings.Contains(stdout.String(), "VM RSS:") {
		t.Errorf("expected no RSS line when free errored, got: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Repos:      0") {
		t.Errorf("expected zero repo count, got: %q", stdout.String())
	}
}

func TestRunStatus_RSSBadOutput_OmitsField(t *testing.T) {
	cases := []backend.ExecResult{
		// Non-zero exit
		{ExitCode: 1, Stdout: []byte("nope")},
		// No Mem: line
		{ExitCode: 0, Stdout: []byte("garbage\n")},
		// Mem: line but unparseable used field
		{ExitCode: 0, Stdout: []byte("Mem: total notanumber free\n")},
	}
	for _, freeRes := range cases {
		scripted := &scriptedBackend{
			Mock: mock.New(),
			execScript: []backend.ExecResult{
				{ExitCode: 0, Stdout: []byte("")}, // ls ok
				freeRes,
			},
		}
		scripted.Mock.IsRunningResult = true

		s := &lifecycleStubs{}
		s.install(t)
		newBackendFn = func(_ backend.Config) (backend.Backend, error) { return scripted, nil }
		withStatStub(t, statExists)

		var stdout bytes.Buffer
		if err := runStatus(context.Background(), &stdout, io.Discard, statusOptions{}); err != nil {
			t.Fatalf("unexpected: %v", err)
		}
		if strings.Contains(stdout.String(), "VM RSS:") {
			t.Errorf("free=%+v: expected omitted RSS, got: %q", freeRes, stdout.String())
		}
	}
}

func TestRunStatus_JSONOutput(t *testing.T) {
	scripted := &scriptedBackend{
		Mock: mock.New(),
		execScript: []backend.ExecResult{
			{ExitCode: 0, Stdout: []byte("alpha\nbeta\n")},
			{ExitCode: 0, Stdout: []byte("Mem: 7891 1234 500\n")},
		},
	}
	scripted.Mock.IsRunningResult = true

	s := &lifecycleStubs{}
	s.install(t)
	newBackendFn = func(_ backend.Config) (backend.Backend, error) { return scripted, nil }
	withStatStub(t, statExists)

	var stdout bytes.Buffer
	if err := runStatus(context.Background(), &stdout, io.Discard, statusOptions{jsonOut: true}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	var report statusReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, stdout.String())
	}
	if report.Locked || !report.Initialized {
		t.Errorf("unexpected state: %+v", report)
	}
	if report.VM.State != "running" || report.VM.RSSMegabyte != 1234 {
		t.Errorf("unexpected VM block: %+v", report.VM)
	}
	if report.Repos == nil || report.Repos.Count != 2 {
		t.Errorf("unexpected repos: %+v", report.Repos)
	}
	if report.Containers != "—" {
		t.Errorf("unexpected containers: %q", report.Containers)
	}
}

// jsonErrWriter forces a JSON encode failure: json.Encoder writes the
// buffered output in chunks, so an erroring writer surfaces an Encode err.
type jsonErrWriter struct{}

func (jsonErrWriter) Write([]byte) (int, error) { return 0, errors.New("write fail") }

func TestRunStatus_JSONEncodeError(t *testing.T) {
	s := &lifecycleStubs{mockBE: mock.New()}
	s.mockBE.IsRunningResult = false
	s.install(t)
	withStatStub(t, statExists)

	err := runStatus(context.Background(), jsonErrWriter{}, io.Discard, statusOptions{jsonOut: true})
	if err == nil || !strings.Contains(err.Error(), "encode JSON") {
		t.Errorf("expected encode error, got %v", err)
	}
}

// ---- parseRepoListing -----------------------------------------------------

func TestParseRepoListing(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", []string{}},
		{"whitespace only", "  \n\t\n", []string{}},
		{"single", "alpha\n", []string{"alpha"}},
		{"sorted dedupe", "beta\nalpha\nalpha\n", []string{"alpha", "beta"}},
		{"trailing space", "  foo \n bar\n", []string{"bar", "foo"}},
	}
	for _, tc := range cases {
		got := parseRepoListing([]byte(tc.in))
		if got == nil {
			t.Fatalf("%s: nil result", tc.name)
		}
		if got.Count != len(tc.want) {
			t.Errorf("%s: count=%d, want %d (names=%v)", tc.name, got.Count, len(tc.want), got.Names)
		}
		if strings.Join(got.Names, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s: names=%v, want %v", tc.name, got.Names, tc.want)
		}
	}
}

// ---- Cobra plumbing -------------------------------------------------------

func TestNewStatusCmd_FlagsRegistered(t *testing.T) {
	cmd := newStatusCmd()
	if cmd.Flags().Lookup("json") == nil {
		t.Error("expected --json flag")
	}
	if cmd.Use != "status" {
		t.Errorf("expected Use=status, got %q", cmd.Use)
	}
}

func TestStatusCmd_RunE_HappyPath(t *testing.T) {
	s := &lifecycleStubs{mockBE: mock.New()}
	s.mockBE.IsRunningResult = false
	s.install(t)
	withStatStub(t, statExists)

	cmd := newStatusCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

// ---- defensive: vmStatus zero VM block --------------------------------------

func TestVMStatus_RSSOmitemptyIsHonored(t *testing.T) {
	// With RSS=0 the JSON should not contain "rss_mb".
	b, err := json.Marshal(vmStatus{State: "stopped", CPUs: 1, Memory: "1GB", Disk: "1GB"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "rss_mb") {
		t.Errorf("expected omitempty to drop rss_mb, got: %s", string(b))
	}
}

// ---- defensive: a config value still drives JSON (config.Backend) ---------

func TestRunStatus_PassesBackendFromConfig(t *testing.T) {
	wantBackend := "lima"
	cfg := config.NewDefault()
	cfg.Backend = wantBackend
	cfg.VM = config.VMConfig{Memory: "8GB", CPUs: 2, Disk: "20GB"}

	var seen string
	origNew := newBackendFn
	t.Cleanup(func() { newBackendFn = origNew })

	s := &lifecycleStubs{cfg: cfg, mockBE: mock.New()}
	s.install(t)
	newBackendFn = func(c backend.Config) (backend.Backend, error) {
		seen = c.Backend
		return s.mockBE, nil
	}
	withStatStub(t, statExists)

	if err := runStatus(context.Background(), io.Discard, io.Discard, statusOptions{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if seen != wantBackend {
		t.Errorf("expected backend %q passed through, got %q", wantBackend, seen)
	}
}
