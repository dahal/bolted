// Package backend defines the per-OS Linux VM abstraction used by the rest
// of the Bolted CLI. Implementations live in sibling packages
// (internal/backend/lima for Mac, internal/backend/wsl2 for Windows) and a
// recording mock lives in internal/backend/mock for use in tests.
//
// Everything above this interface — encryption UX, devcontainer
// orchestration, CLI commands — is shared, OS-agnostic code. The factory in
// factory.go selects the right backend based on runtime.GOOS plus the
// optional `backend:` config override.
package backend

import (
	"context"
	"io"
)

// Backend is the contract every per-OS VM implementation satisfies. Methods
// are synchronous; streaming/async exec is deliberately out of scope (see
// spec 02 § Non-goals).
type Backend interface {
	// Preflight runs cheap host checks that must pass before any
	// expensive or irreversible work happens — typically "is the
	// per-OS VM tooling installed and reachable?". Callers invoke it
	// before prompting for passwords or doing any side-effecting work,
	// so a missing dependency surfaces before the user has paid any
	// cost. Idempotent and side-effect-free.
	Preflight(ctx context.Context) error

	// EnsureVM creates the VM if it does not already exist using the given
	// spec. Idempotent: calling it on an existing VM is a no-op (it does
	// NOT reconfigure CPUs/memory/disk — re-creation is a separate flow).
	EnsureVM(ctx context.Context, spec VMSpec) error

	// StartVM boots the VM. No-op if already running.
	StartVM(ctx context.Context) error

	// StopVM gracefully shuts the VM down. No-op if already stopped.
	StopVM(ctx context.Context) error

	// IsRunning reports whether the VM is currently booted.
	IsRunning(ctx context.Context) (bool, error)

	// Exec runs cmd inside the VM and returns the captured result. The
	// caller controls stdin / cwd / env via opts. Permission checks are
	// the caller's responsibility — Exec trusts what it is given.
	Exec(ctx context.Context, cmd []string, opts ExecOpts) (ExecResult, error)

	// ForwardPort exposes a port from inside the VM to the host. If
	// hostPort is already in use the implementation should return an
	// error rather than silently rebinding.
	ForwardPort(ctx context.Context, guestPort, hostPort int) error

	// UnforwardPort tears down a previously established forward. No-op
	// if hostPort was not forwarded.
	UnforwardPort(ctx context.Context, hostPort int) error

	// DeleteVM destroys the VM and any backend-managed state. Does NOT
	// touch encrypted volumes — those are managed at a higher layer.
	DeleteVM(ctx context.Context) error
}

// VMSpec describes the resources requested for a VM. Sizing logic lives in
// the caller (see brainstorm 02-architecture § Resource sizing); the
// backend just enforces what it is told.
type VMSpec struct {
	// CPUs is the number of virtual CPUs to allocate.
	CPUs int
	// MemoryMB is the static RAM allocation in megabytes.
	MemoryMB int
	// DiskGB is the maximum sparse disk size in gigabytes.
	DiskGB int
}

// ExecOpts controls how a command is executed inside the VM. The zero value
// is valid: it runs in the VM's default cwd with no extra env, no stdin,
// and no TTY.
type ExecOpts struct {
	// Cwd is the working directory inside the VM. Empty means "use the
	// backend's default".
	Cwd string
	// Env is a list of KEY=VALUE strings appended to the VM's
	// environment.
	Env []string
	// Stdin, if non-nil, is piped to the command's standard input.
	Stdin io.Reader
	// TTY requests a pseudo-tty for the command (interactive shells).
	TTY bool
}

// ExecResult is what Exec returns once the command has finished. Stdout and
// Stderr are fully buffered — streaming is out of scope for the MVP.
type ExecResult struct {
	// Stdout captures the command's standard output.
	Stdout []byte
	// Stderr captures the command's standard error.
	Stderr []byte
	// ExitCode is the process exit status. Implementations should set
	// this to -1 if the command failed to start.
	ExitCode int
}
