// Package lima will hold the macOS Lima/limactl implementation of
// backend.Backend. This file is a stub: every method returns
// errors.New("lima backend: not implemented — see spec 05"). The real
// implementation lands in spec 05.
//
// The stub exists so the factory in internal/backend has something to
// hand back on darwin and so the rest of the codebase can be wired up
// against a complete (if non-functional) backend.Backend today.
package lima

import (
	"context"
	"errors"

	"github.com/dahal/bolted/internal/backend"
)

// errNotImplemented is returned by every method on the stub Backend.
// Centralised so tests can assert against a single sentinel and so the
// "see spec 05" pointer stays consistent.
var errNotImplemented = errors.New("lima backend: not implemented — see spec 05")

// Backend is the macOS Lima implementation of backend.Backend. The struct
// is intentionally empty in this spec; spec 05 will add the limactl
// client, instance name, and any config it needs.
type Backend struct{}

// New returns a fresh Lima backend. It accepts no arguments today; spec
// 05 will introduce a config struct if/when it needs one.
func New() *Backend { return &Backend{} }

// EnsureVM is a stub — see spec 05.
func (b *Backend) EnsureVM(_ context.Context, _ backend.VMSpec) error {
	return errNotImplemented
}

// StartVM is a stub — see spec 05.
func (b *Backend) StartVM(_ context.Context) error { return errNotImplemented }

// StopVM is a stub — see spec 05.
func (b *Backend) StopVM(_ context.Context) error { return errNotImplemented }

// IsRunning is a stub — see spec 05.
func (b *Backend) IsRunning(_ context.Context) (bool, error) {
	return false, errNotImplemented
}

// Exec is a stub — see spec 05.
func (b *Backend) Exec(_ context.Context, _ []string, _ backend.ExecOpts) (backend.ExecResult, error) {
	return backend.ExecResult{ExitCode: -1}, errNotImplemented
}

// ForwardPort is a stub — see spec 05.
func (b *Backend) ForwardPort(_ context.Context, _, _ int) error {
	return errNotImplemented
}

// UnforwardPort is a stub — see spec 05.
func (b *Backend) UnforwardPort(_ context.Context, _ int) error {
	return errNotImplemented
}

// DeleteVM is a stub — see spec 05.
func (b *Backend) DeleteVM(_ context.Context) error { return errNotImplemented }
