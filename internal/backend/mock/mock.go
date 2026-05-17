// Package mock provides an in-memory backend.Backend implementation that
// records every call it receives. Tests across other specs use it to assert
// that the right backend methods get called in the right order without
// spinning up a real VM.
//
// The mock is intentionally minimal: it has no concept of state (it will
// happily report IsRunning=false even after StartVM). Tests that need
// stateful behaviour should drive it via the Result* / Err* fields below
// or wrap it in a thin fixture of their own.
package mock

import (
	"context"
	"sync"

	"github.com/dahal/bolted/internal/backend"
)

// Call records a single method invocation against the mock. Fields are
// populated based on Method; unused fields stay zero-valued.
type Call struct {
	// Method is the Backend method name ("EnsureVM", "Exec", …).
	Method string
	// Ctx is the context the caller passed in. Stored mostly so tests
	// can assert deadlines / values flowed through correctly.
	Ctx context.Context
	// VMSpec is set for EnsureVM.
	VMSpec backend.VMSpec
	// Cmd is set for Exec.
	Cmd []string
	// ExecOpts is set for Exec.
	ExecOpts backend.ExecOpts
	// GuestPort is set for ForwardPort.
	GuestPort int
	// HostPort is set for ForwardPort / UnforwardPort.
	HostPort int
}

// Mock is a recording backend.Backend. The zero value is ready to use:
// every method returns a zero result and no error.
type Mock struct {
	// Mu guards Calls and the result/error fields. Exported so tests can
	// take it when reading from multiple goroutines.
	Mu sync.Mutex

	// Calls is the ordered list of invocations the mock has received.
	Calls []Call

	// ExecResult is returned verbatim by Exec.
	ExecResult backend.ExecResult
	// IsRunningResult is returned by IsRunning.
	IsRunningResult bool

	// Per-method canned errors. Leave nil to return success.
	ErrEnsureVM      error
	ErrStartVM       error
	ErrStopVM        error
	ErrIsRunning     error
	ErrExec          error
	ErrForwardPort   error
	ErrUnforwardPort error
	ErrDeleteVM      error
}

// New returns a fresh mock ready to be plugged into code expecting a
// backend.Backend.
func New() *Mock { return &Mock{} }

// record appends c to m.Calls under the mutex.
func (m *Mock) record(c Call) {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	m.Calls = append(m.Calls, c)
}

// EnsureVM records the call and returns ErrEnsureVM.
func (m *Mock) EnsureVM(ctx context.Context, spec backend.VMSpec) error {
	m.record(Call{Method: "EnsureVM", Ctx: ctx, VMSpec: spec})
	return m.ErrEnsureVM
}

// StartVM records the call and returns ErrStartVM.
func (m *Mock) StartVM(ctx context.Context) error {
	m.record(Call{Method: "StartVM", Ctx: ctx})
	return m.ErrStartVM
}

// StopVM records the call and returns ErrStopVM.
func (m *Mock) StopVM(ctx context.Context) error {
	m.record(Call{Method: "StopVM", Ctx: ctx})
	return m.ErrStopVM
}

// IsRunning records the call and returns IsRunningResult / ErrIsRunning.
func (m *Mock) IsRunning(ctx context.Context) (bool, error) {
	m.record(Call{Method: "IsRunning", Ctx: ctx})
	return m.IsRunningResult, m.ErrIsRunning
}

// Exec records the call and returns ExecResult / ErrExec.
func (m *Mock) Exec(ctx context.Context, cmd []string, opts backend.ExecOpts) (backend.ExecResult, error) {
	m.record(Call{Method: "Exec", Ctx: ctx, Cmd: cmd, ExecOpts: opts})
	return m.ExecResult, m.ErrExec
}

// ForwardPort records the call and returns ErrForwardPort.
func (m *Mock) ForwardPort(ctx context.Context, guestPort, hostPort int) error {
	m.record(Call{
		Method:    "ForwardPort",
		Ctx:       ctx,
		GuestPort: guestPort,
		HostPort:  hostPort,
	})
	return m.ErrForwardPort
}

// UnforwardPort records the call and returns ErrUnforwardPort.
func (m *Mock) UnforwardPort(ctx context.Context, hostPort int) error {
	m.record(Call{Method: "UnforwardPort", Ctx: ctx, HostPort: hostPort})
	return m.ErrUnforwardPort
}

// DeleteVM records the call and returns ErrDeleteVM.
func (m *Mock) DeleteVM(ctx context.Context) error {
	m.record(Call{Method: "DeleteVM", Ctx: ctx})
	return m.ErrDeleteVM
}

// Methods returns the ordered list of method names called against the
// mock. Convenience for tests that only care about the sequence.
func (m *Mock) Methods() []string {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	out := make([]string, len(m.Calls))
	for i, c := range m.Calls {
		out[i] = c.Method
	}
	return out
}

// Reset clears the recorded calls. The result/error fields are left
// untouched so a test can reuse a configured mock across sub-cases.
func (m *Mock) Reset() {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	m.Calls = nil
}

// Compile-time guarantee that *Mock satisfies backend.Backend.
var _ backend.Backend = (*Mock)(nil)
