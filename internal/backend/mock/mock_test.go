package mock

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/dahal/bolted/internal/backend"
)

// TestMock_ImplementsBackend is a compile-time-style check that *Mock
// satisfies the Backend interface. The package-level var _ assertion in
// mock.go already enforces this; this test pins it explicitly so a
// refactor that drops a method fails with a clear test name.
func TestMock_ImplementsBackend(t *testing.T) {
	var _ backend.Backend = New()
}

// TestMock_RecordsEveryMethodInOrder drives every Backend method against
// a fresh mock and asserts the recorded call sequence + per-call payload.
// This is the headline test: it's why the mock exists.
func TestMock_RecordsEveryMethodInOrder(t *testing.T) {
	m := New()
	ctx := context.Background()
	spec := backend.VMSpec{CPUs: 4, MemoryMB: 8192, DiskGB: 50}
	cmd := []string{"echo", "hi"}
	opts := backend.ExecOpts{Cwd: "/bolted", Env: []string{"FOO=bar"}, TTY: true}

	if err := m.EnsureVM(ctx, spec); err != nil {
		t.Fatalf("EnsureVM: %v", err)
	}
	if err := m.StartVM(ctx); err != nil {
		t.Fatalf("StartVM: %v", err)
	}
	running, err := m.IsRunning(ctx)
	if err != nil {
		t.Fatalf("IsRunning: %v", err)
	}
	if running {
		t.Errorf("expected zero-value IsRunningResult=false, got true")
	}
	if _, err := m.Exec(ctx, cmd, opts); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if err := m.ForwardPort(ctx, 3000, 13000); err != nil {
		t.Fatalf("ForwardPort: %v", err)
	}
	if err := m.UnforwardPort(ctx, 13000); err != nil {
		t.Fatalf("UnforwardPort: %v", err)
	}
	if err := m.StopVM(ctx); err != nil {
		t.Fatalf("StopVM: %v", err)
	}
	if err := m.DeleteVM(ctx); err != nil {
		t.Fatalf("DeleteVM: %v", err)
	}

	wantMethods := []string{
		"EnsureVM", "StartVM", "IsRunning", "Exec",
		"ForwardPort", "UnforwardPort", "StopVM", "DeleteVM",
	}
	if got := m.Methods(); !reflect.DeepEqual(got, wantMethods) {
		t.Errorf("Methods() = %v, want %v", got, wantMethods)
	}

	if len(m.Calls) != len(wantMethods) {
		t.Fatalf("Calls len = %d, want %d", len(m.Calls), len(wantMethods))
	}

	if !reflect.DeepEqual(m.Calls[0].VMSpec, spec) {
		t.Errorf("EnsureVM.VMSpec = %+v, want %+v", m.Calls[0].VMSpec, spec)
	}
	if !reflect.DeepEqual(m.Calls[3].Cmd, cmd) {
		t.Errorf("Exec.Cmd = %v, want %v", m.Calls[3].Cmd, cmd)
	}
	if !reflect.DeepEqual(m.Calls[3].ExecOpts, opts) {
		t.Errorf("Exec.ExecOpts = %+v, want %+v", m.Calls[3].ExecOpts, opts)
	}
	if m.Calls[4].GuestPort != 3000 || m.Calls[4].HostPort != 13000 {
		t.Errorf("ForwardPort ports = (%d,%d), want (3000,13000)",
			m.Calls[4].GuestPort, m.Calls[4].HostPort)
	}
	if m.Calls[5].HostPort != 13000 {
		t.Errorf("UnforwardPort.HostPort = %d, want 13000", m.Calls[5].HostPort)
	}

	// Context flowed through.
	for _, c := range m.Calls {
		if c.Ctx == nil {
			t.Errorf("call %s lost its context", c.Method)
		}
	}
}

// TestMock_CannedErrorsAreReturned makes sure every Err* field plumbs
// through to its method.
func TestMock_CannedErrorsAreReturned(t *testing.T) {
	sentinel := errors.New("canned")
	m := &Mock{
		ErrEnsureVM:      sentinel,
		ErrStartVM:       sentinel,
		ErrStopVM:        sentinel,
		ErrIsRunning:     sentinel,
		ErrExec:          sentinel,
		ErrForwardPort:   sentinel,
		ErrUnforwardPort: sentinel,
		ErrDeleteVM:      sentinel,
	}
	ctx := context.Background()
	if err := m.EnsureVM(ctx, backend.VMSpec{}); !errors.Is(err, sentinel) {
		t.Errorf("EnsureVM err = %v", err)
	}
	if err := m.StartVM(ctx); !errors.Is(err, sentinel) {
		t.Errorf("StartVM err = %v", err)
	}
	if err := m.StopVM(ctx); !errors.Is(err, sentinel) {
		t.Errorf("StopVM err = %v", err)
	}
	if _, err := m.IsRunning(ctx); !errors.Is(err, sentinel) {
		t.Errorf("IsRunning err = %v", err)
	}
	if _, err := m.Exec(ctx, nil, backend.ExecOpts{}); !errors.Is(err, sentinel) {
		t.Errorf("Exec err = %v", err)
	}
	if err := m.ForwardPort(ctx, 1, 2); !errors.Is(err, sentinel) {
		t.Errorf("ForwardPort err = %v", err)
	}
	if err := m.UnforwardPort(ctx, 2); !errors.Is(err, sentinel) {
		t.Errorf("UnforwardPort err = %v", err)
	}
	if err := m.DeleteVM(ctx); !errors.Is(err, sentinel) {
		t.Errorf("DeleteVM err = %v", err)
	}
}

// TestMock_CannedResultsAreReturned covers the non-error fields:
// IsRunningResult and ExecResult.
func TestMock_CannedResultsAreReturned(t *testing.T) {
	want := backend.ExecResult{
		Stdout:   []byte("hello"),
		Stderr:   []byte("warn"),
		ExitCode: 7,
	}
	m := &Mock{
		IsRunningResult: true,
		ExecResult:      want,
	}
	ctx := context.Background()

	running, err := m.IsRunning(ctx)
	if err != nil {
		t.Fatalf("IsRunning: %v", err)
	}
	if !running {
		t.Error("expected IsRunning=true")
	}

	got, err := m.Exec(ctx, []string{"x"}, backend.ExecOpts{})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if string(got.Stdout) != "hello" || string(got.Stderr) != "warn" || got.ExitCode != 7 {
		t.Errorf("Exec result = %+v, want %+v", got, want)
	}
}

// TestMock_Reset clears recorded calls but leaves canned errors/results
// intact so a configured mock can be reused across sub-cases.
func TestMock_Reset(t *testing.T) {
	m := &Mock{IsRunningResult: true, ErrEnsureVM: errors.New("x")}
	_ = m.EnsureVM(context.Background(), backend.VMSpec{})
	if len(m.Calls) != 1 {
		t.Fatalf("setup: expected 1 call, got %d", len(m.Calls))
	}
	m.Reset()
	if len(m.Calls) != 0 {
		t.Errorf("Reset should clear Calls, got len=%d", len(m.Calls))
	}
	if m.ErrEnsureVM == nil {
		t.Error("Reset should leave ErrEnsureVM alone")
	}
	if !m.IsRunningResult {
		t.Error("Reset should leave IsRunningResult alone")
	}
}

// TestMock_MethodsEmptyOnFreshMock guards against a regression where
// Methods() panics on an empty Calls slice.
func TestMock_MethodsEmptyOnFreshMock(t *testing.T) {
	m := New()
	got := m.Methods()
	if got == nil {
		t.Error("Methods() should return non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("expected empty Methods on fresh mock, got %v", got)
	}
}

// TestCall_Struct documents the Call struct's fields by constructing one
// of each variant. Pure compile-time sanity check.
func TestCall_Struct(t *testing.T) {
	c := Call{
		Method:    "Exec",
		Ctx:       context.Background(),
		VMSpec:    backend.VMSpec{CPUs: 2},
		Cmd:       []string{"ls"},
		ExecOpts:  backend.ExecOpts{Cwd: "/"},
		GuestPort: 80,
		HostPort:  8080,
	}
	if c.Method != "Exec" {
		t.Fail()
	}
	// Touch every field so a future field addition that breaks struct
	// literal initialisation fails this test loudly.
	if c.Ctx == nil || c.VMSpec.CPUs == 0 || c.Cmd == nil ||
		c.ExecOpts.Cwd == "" || c.GuestPort == 0 || c.HostPort == 0 {
		t.Errorf("unexpected zero field in %+v", c)
	}
	if !strings.Contains(c.Method, "Exec") {
		t.Fail()
	}
}
