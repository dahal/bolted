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
	"testing"

	"github.com/dahal/bolted/internal/backend"
	"github.com/dahal/bolted/internal/backend/mock"
	"github.com/dahal/bolted/internal/devcontainertrust"
)

// installTrustHashStub swaps hashConfigFn so trust tests don't need to
// drive a fake `cat` exec script.
func installTrustHashStub(t *testing.T, hash, summary string, err error) *trustHashCall {
	t.Helper()
	orig := hashConfigFn
	t.Cleanup(func() { hashConfigFn = orig })
	rec := &trustHashCall{}
	hashConfigFn = func(b backend.Backend, repoPath string) (string, string, error) {
		rec.calls++
		rec.lastBackend = b
		rec.lastRepoPath = repoPath
		return hash, summary, err
	}
	return rec
}

type trustHashCall struct {
	calls        int
	lastBackend  backend.Backend
	lastRepoPath string
}

// trustEnv installs lifecycleStubs + a scripted unlocked backend + a
// state-dir stub so runTrust's approval flow has a working state file.
type trustEnv struct {
	scripted *scriptedBackend
	stateDir string
}

func installTrustEnv(t *testing.T, execScript []backend.ExecResult, execErrs []error) *trustEnv {
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
	return &trustEnv{scripted: scripted, stateDir: dir}
}

// ---- runTrust: --revoke ---------------------------------------------------

func TestRunTrust_RevokeHappyPath(t *testing.T) {
	dir := withStateDirStub(t)
	// Pre-seed an approval so we can verify it gets cleared.
	store := devcontainertrust.NewStore(dir)
	if err := store.Approve("api", "h"); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	err := runTrust(context.Background(), io.Discard, &stderr, "api", trustOptions{revoke: true})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if store.Approved("api", "h") {
		t.Errorf("expected approval to be cleared")
	}
	if !strings.Contains(stderr.String(), "cleared") {
		t.Errorf("expected 'cleared' notice, got %q", stderr.String())
	}
}

func TestRunTrust_RevokeOnLoadErrorPropagates(t *testing.T) {
	dir := withStateDirStub(t)
	// Corrupt the file so Revoke's load() fails.
	if err := os.WriteFile(filepath.Join(dir, "devcontainer-trust.json"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runTrust(context.Background(), io.Discard, io.Discard, "api", trustOptions{revoke: true})
	if err == nil || !strings.Contains(err.Error(), "trust revoke") {
		t.Errorf("expected wrapped revoke error, got %v", err)
	}
}

func TestRunTrust_RevokeDoesNotTouchBackend(t *testing.T) {
	// Even when Bolted is "not initialised", --revoke should
	// succeed because it never reaches requireUnlockedBackend.
	dir := withStateDirStub(t)
	_ = dir
	// Don't install lifecycleStubs at all → no backend stubs.
	if err := runTrust(context.Background(), io.Discard, io.Discard, "api", trustOptions{revoke: true}); err != nil {
		t.Errorf("revoke should not need the backend, got %v", err)
	}
}

// ---- runTrust: approve (no flag) ------------------------------------------

func TestRunTrust_ApproveRequiresUnlocked(t *testing.T) {
	// statMissing → Bolted not initialised → exitLocked
	s := &lifecycleStubs{}
	s.install(t)
	withStatStub(t, statMissing)
	err := runTrust(context.Background(), io.Discard, io.Discard, "api", trustOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != exitLocked {
		t.Errorf("expected exit %d, got %d", exitLocked, code)
	}
}

func TestRunTrust_ApproveRepoNotFound(t *testing.T) {
	env := installTrustEnv(t, []backend.ExecResult{
		{ExitCode: 0}, // unlocked probe
		{ExitCode: 1}, // test -d repo
	}, nil)
	_ = env
	var stderr bytes.Buffer
	err := runTrust(context.Background(), io.Discard, &stderr, "missing", trustOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != exitRepoNotFound {
		t.Errorf("expected exit %d, got %d", exitRepoNotFound, code)
	}
}

func TestRunTrust_ApproveHappyPath(t *testing.T) {
	env := installTrustEnv(t, []backend.ExecResult{
		{ExitCode: 0}, // unlocked probe
		{ExitCode: 0}, // test -d
	}, nil)
	rec := installTrustHashStub(t, "abc123", "summary text", nil)

	var stderr bytes.Buffer
	if err := runTrust(context.Background(), io.Discard, &stderr, "api", trustOptions{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if rec.calls != 1 {
		t.Errorf("expected 1 hash call, got %d", rec.calls)
	}
	if rec.lastRepoPath != "/bolted/repos/api" {
		t.Errorf("hash called with %q", rec.lastRepoPath)
	}
	if !strings.Contains(stderr.String(), "abc123") {
		t.Errorf("expected hash in stderr, got %q", stderr.String())
	}
	// Verify the approval was actually persisted.
	store := devcontainertrust.NewStore(env.stateDir)
	if !store.Approved("api", "abc123") {
		t.Errorf("expected approval to be recorded")
	}
}

func TestRunTrust_ApproveNoConfig(t *testing.T) {
	_ = installTrustEnv(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	installTrustHashStub(t, "", "", devcontainertrust.ErrNoConfig)

	var stderr bytes.Buffer
	err := runTrust(context.Background(), io.Discard, &stderr, "api", trustOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, devcontainertrust.ErrNoConfig) {
		t.Errorf("expected wrapped ErrNoConfig, got %v", err)
	}
	if code := exitCodeFromError(err); code != exitGeneric {
		t.Errorf("expected exit %d, got %d", exitGeneric, code)
	}
	if !strings.Contains(stderr.String(), "nothing to approve") {
		t.Errorf("expected helpful message, got %q", stderr.String())
	}
}

func TestRunTrust_ApproveHashError(t *testing.T) {
	_ = installTrustEnv(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	installTrustHashStub(t, "", "", errors.New("exec boom"))
	err := runTrust(context.Background(), io.Discard, io.Discard, "api", trustOptions{})
	if err == nil || !strings.Contains(err.Error(), "hash devcontainer.json") {
		t.Errorf("expected wrapped hash error, got %v", err)
	}
}

func TestRunTrust_ApproveStoreSaveError(t *testing.T) {
	env := installTrustEnv(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	installTrustHashStub(t, "abc", "", nil)
	// Make the state dir read-only so the atomic write inside
	// Store.Approve fails.
	if err := os.Chmod(env.stateDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(env.stateDir, 0o700) })
	err := runTrust(context.Background(), io.Discard, io.Discard, "api", trustOptions{})
	if err == nil || !strings.Contains(err.Error(), "record approval") {
		t.Errorf("expected wrapped record-approval error, got %v", err)
	}
}

// ---- Cobra plumbing -------------------------------------------------------

func TestNewTrustCmd_FlagsRegistered(t *testing.T) {
	cmd := newTrustCmd()
	if cmd.Flags().Lookup("revoke") == nil {
		t.Error("expected --revoke flag")
	}
	if !strings.HasPrefix(cmd.Use, "trust") {
		t.Errorf("expected Use prefix 'trust', got %q", cmd.Use)
	}
}

func TestTrustCmd_RunE_DispatchesRunTrust(t *testing.T) {
	// Easiest path: --revoke against a temp state dir → succeeds without
	// touching the backend.
	dir := withStateDirStub(t)
	store := devcontainertrust.NewStore(dir)
	if err := store.Approve("api", "h"); err != nil {
		t.Fatal(err)
	}
	cmd := newTrustCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--revoke", "api"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if store.Approved("api", "h") {
		t.Errorf("expected approval cleared via cobra")
	}
}

func TestTrustCmd_RunE_RequiresRepoArg(t *testing.T) {
	cmd := newTrustCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{}) // no args → ExactArgs(1) error
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error from cobra args validator")
	}
}

func TestTrustCmd_RunE_DispatchesApprovePath(t *testing.T) {
	env := installTrustEnv(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	rec := installTrustHashStub(t, "h", "", nil)
	cmd := newTrustCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"api"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if rec.calls != 1 {
		t.Errorf("expected hash via cobra, got %d calls", rec.calls)
	}
	// Confirm it was persisted in the right place.
	data, err := os.ReadFile(filepath.Join(env.stateDir, "devcontainer-trust.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if m["api"] != "h" {
		t.Errorf("expected api=h, got %v", m)
	}
}

// ---- defensive: indirection defaults --------------------------------------

func TestNewTrustStoreFn_DefaultUsesStateDir(t *testing.T) {
	orig := stateDirFn
	t.Cleanup(func() { stateDirFn = orig })
	stateDirFn = func() string { return "/tmp/bolt-state" }
	s := newTrustStoreFn()
	if s == nil {
		t.Fatal("default returned nil")
	}
}

func TestHashConfigFn_DefaultDelegates(t *testing.T) {
	// We can't drive a real Backend.Exec here; instead, call the default
	// with a backend whose Exec is well-behaved enough to give us a
	// deterministic result. Use a fresh mock that returns ExitCode != 0
	// → ErrNoConfig path.
	m := mock.New()
	m.ExecResult = backend.ExecResult{ExitCode: 1}
	hash, summary, err := hashConfigFn(m, "/bolted/repos/api")
	if !errors.Is(err, devcontainertrust.ErrNoConfig) {
		t.Errorf("expected ErrNoConfig from default, got %v", err)
	}
	if hash != "" || summary != "" {
		t.Errorf("expected empty hash+summary, got %q %q", hash, summary)
	}
}
