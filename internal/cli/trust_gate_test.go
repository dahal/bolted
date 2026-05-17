package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dahal/bolted/internal/backend"
	"github.com/dahal/bolted/internal/backend/mock"
	"github.com/dahal/bolted/internal/devcontainertrust"
)

// withTrustGateMockBackend builds a mock backend whose `cat` (used by
// HashConfig) returns the supplied bytes (or a non-zero exit when
// contents is empty, triggering ErrNoConfig).
func withTrustGateMockBackend(contents string) *mock.Mock {
	m := mock.New()
	if contents == "" {
		m.ExecResult = backend.ExecResult{ExitCode: 1, Stderr: []byte("cat: no such file")}
	} else {
		m.ExecResult = backend.ExecResult{ExitCode: 0, Stdout: []byte(contents)}
	}
	return m
}

func withConfirmStub(t *testing.T, fn func(io.Reader, io.Writer, string) (bool, error)) {
	t.Helper()
	orig := confirmTrustFn
	t.Cleanup(func() { confirmTrustFn = orig })
	confirmTrustFn = fn
}

func withStateDirToTemp(t *testing.T) {
	t.Helper()
	orig := stateDirFn
	t.Cleanup(func() { stateDirFn = orig })
	dir := t.TempDir()
	stateDirFn = func() string { return dir }
}

func TestRealTrustGate_NoConfigSkipsGracefully(t *testing.T) {
	withStateDirToTemp(t)
	b := withTrustGateMockBackend("") // cat returns non-zero
	if err := realTrustGate(context.Background(), b, "api", "/bolted/repos/api", nil, io.Discard, false); err != nil {
		t.Errorf("expected no-op when devcontainer.json missing, got %v", err)
	}
}

func TestRealTrustGate_AlreadyApproved(t *testing.T) {
	withStateDirToTemp(t)
	b := withTrustGateMockBackend(`{"image": "alpine"}`)
	// Seed approval via auto-approve.
	if err := realTrustGate(context.Background(), b, "api", "/bolted/repos/api", nil, io.Discard, true); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Now a regular call should short-circuit on store.Approved.
	withConfirmStub(t, func(io.Reader, io.Writer, string) (bool, error) {
		t.Fatal("Confirm should not be called when already approved")
		return false, nil
	})
	if err := realTrustGate(context.Background(), b, "api", "/bolted/repos/api", nil, io.Discard, false); err != nil {
		t.Errorf("expected approved-path success, got %v", err)
	}
}

func TestRealTrustGate_AutoApprovePersists(t *testing.T) {
	withStateDirToTemp(t)
	b := withTrustGateMockBackend(`{"image": "alpine"}`)
	if err := realTrustGate(context.Background(), b, "api", "/bolted/repos/api", nil, io.Discard, true); err != nil {
		t.Fatalf("auto-approve: %v", err)
	}
	store := devcontainertrust.NewStore(stateDirFn())
	hash, _, _ := devcontainertrust.HashConfig(b, "/bolted/repos/api")
	if !store.Approved("api", hash) {
		t.Error("expected hash approved after auto-approve")
	}
}

func TestRealTrustGate_HashError(t *testing.T) {
	withStateDirToTemp(t)
	wantErr := errors.New("backend dead")
	m := mock.New()
	m.ErrExec = wantErr
	err := realTrustGate(context.Background(), m, "api", "/bolted/repos/api", nil, io.Discard, false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("expected wrapped backend error, got %v", err)
	}
}

func TestRealTrustGate_ConfirmYesPersists(t *testing.T) {
	withStateDirToTemp(t)
	b := withTrustGateMockBackend(`{"image": "alpine"}`)
	withConfirmStub(t, func(io.Reader, io.Writer, string) (bool, error) { return true, nil })

	if err := realTrustGate(context.Background(), b, "api", "/bolted/repos/api", nil, io.Discard, false); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	store := devcontainertrust.NewStore(stateDirFn())
	hash, _, _ := devcontainertrust.HashConfig(b, "/bolted/repos/api")
	if !store.Approved("api", hash) {
		t.Error("expected approval to persist")
	}
}

func TestRealTrustGate_ConfirmNoRejects(t *testing.T) {
	withStateDirToTemp(t)
	b := withTrustGateMockBackend(`{"image": "alpine"}`)
	withConfirmStub(t, func(io.Reader, io.Writer, string) (bool, error) { return false, nil })

	err := realTrustGate(context.Background(), b, "api", "/bolted/repos/api", nil, io.Discard, false)
	if err == nil {
		t.Fatal("expected error on rejection")
	}
	if !strings.Contains(err.Error(), "not approved") {
		t.Errorf("expected not-approved error, got %v", err)
	}
}

func TestRealTrustGate_ConfirmErrorWraps(t *testing.T) {
	withStateDirToTemp(t)
	b := withTrustGateMockBackend(`{"image": "alpine"}`)
	wantErr := errors.New("simulated stdin error")
	withConfirmStub(t, func(io.Reader, io.Writer, string) (bool, error) { return false, wantErr })

	err := realTrustGate(context.Background(), b, "api", "/bolted/repos/api", nil, io.Discard, false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("expected wrapped Confirm error, got %v", err)
	}
}

// ---- Integration with runDev / runExec ------------------------------------
//
// Most existing dev/exec tests rely on withRunnerStub which neutralises
// trustGateFn. The tests below opt back into a stub trust gate that
// rejects, to prove runDev / runExec surface the gate's verdict.

func TestRunDev_TrustGateRejectSurfaces(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0}, // requireUnlockedBackend probe
		{ExitCode: 0}, // requireRepo probe
	}, nil)
	_ = ds
	trustGateFn = func(context.Context, backend.Backend, string, string, io.Reader, io.Writer, bool) error {
		return errors.New("devcontainer.json not approved")
	}
	err := runDev(context.Background(), io.Discard, io.Discard, "api", nil, devOptions{})
	if err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Errorf("expected trust gate rejection, got %v", err)
	}
}

func TestRunExec_TrustGateRejectSurfaces(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0},
	}, nil)
	_ = ds
	trustGateFn = func(context.Context, backend.Backend, string, string, io.Reader, io.Writer, bool) error {
		return errors.New("devcontainer.json not approved")
	}
	err := runExec(context.Background(), io.Discard, io.Discard, "api", []string{"echo"}, execOptions{})
	if err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Errorf("expected trust gate rejection, got %v", err)
	}
}
