// Package lima implements backend.Backend for macOS by shelling out to
// Lima's limactl CLI. It expects Lima 1.0+ on PATH (install via
// `brew install lima`). All filesystem state — the lima.yaml template
// rendered from VMSpec plus the persisted port-forward request list —
// lives under <BoltedDir>/vm.
//
// The implementation is split across three files so each concern stays
// small:
//
//   - lima.go    Backend type and the backend.Backend method surface.
//   - runner.go  the runner interface + realRunner (os/exec wrapper)
//     that makes every limactl invocation injectable for tests.
//   - config.go  lima.yaml templating, limactl ls --json parsing, and
//     the port-forward tracking file used by ForwardPort /
//     UnforwardPort.
//
// Dynamic port forwarding is a known weak spot: older Lima releases do
// not ship a stable `limactl port-forward` subcommand. ForwardPort
// therefore persists the request to a tracking file and best-effort
// attempts a `limactl forward` invocation; failures are swallowed and
// the caller is expected to rely on the YAML-embedded portForwards on
// the next StartVM. The richer dynamic-forward design lives in spec 14.
package lima

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dahal/bolted/internal/backend"
	"github.com/dahal/bolted/internal/config"
)

// defaultVMName is the Lima instance name used when the caller does not
// supply one explicitly via NewWithOptions. Mirrors the rest of the
// codebase which references a single "bolted" VM per host.
const defaultVMName = "bolted"

// Backend is the macOS Lima implementation of backend.Backend. The zero
// value is NOT usable; construct via New or NewWithOptions.
type Backend struct {
	// name is the Lima instance name (limactl --name) this backend
	// owns. Defaults to defaultVMName.
	name string
	// dataDir is the directory under BoltedDir() where lima.yaml and
	// the port-forward tracking file are persisted.
	dataDir string
	// runner is the indirection through which every limactl invocation
	// goes. Tests substitute a fakeRunner; production uses realRunner.
	runner runner
}

// Options configures NewWithOptions. The zero value is valid and gives
// the same Backend as New().
type Options struct {
	// Name overrides the Lima instance name. Empty means defaultVMName.
	Name string
	// DataDir overrides the on-disk directory for lima.yaml and the
	// port-forward tracking file. Empty means <BoltedDir>/vm.
	DataDir string
	// Runner overrides the limactl invoker. Empty means realRunner —
	// production behaviour.
	Runner runner
}

// New returns a Backend with defaults: name="bolted", dataDir under
// BoltedDir()/vm, and realRunner shelling out to limactl. This is
// what factory.New hands back on darwin.
func New() *Backend {
	return NewWithOptions(Options{})
}

// NewWithOptions returns a Backend customised by opts. Empty fields in
// opts fall back to the same defaults New uses.
func NewWithOptions(opts Options) *Backend {
	name := opts.Name
	if name == "" {
		name = defaultVMName
	}
	dataDir := opts.DataDir
	if dataDir == "" {
		dataDir = filepath.Join(config.BoltedDir(), "vm")
	}
	r := opts.Runner
	if r == nil {
		r = realRunner{}
	}
	return &Backend{name: name, dataDir: dataDir, runner: r}
}

// Preflight satisfies backend.Backend. Currently just delegates to
// requireLima; future checks (QEMU on PATH, free disk for spec.Disk,
// host virtualization) can be appended here and reported together.
func (b *Backend) Preflight(ctx context.Context) error {
	return b.requireLima(ctx)
}

// requireLima checks that limactl is available on PATH and returns a
// clear actionable error if not. Only call this from EnsureVM / StartVM
// so unit tests that don't need Lima can still exercise the rest of the
// surface via a fakeRunner.
func (b *Backend) requireLima(ctx context.Context) error {
	if _, err := b.runner.Run(ctx, "limactl", "--version"); err != nil {
		return fmt.Errorf(
			"lima backend: limactl not available — install Lima first (`brew install lima`): %w",
			err,
		)
	}
	return nil
}

// EnsureVM renders a lima.yaml from spec, persists it under dataDir,
// and runs `limactl create` if the named instance does not already
// exist. Idempotent: an existing instance is left untouched (sizing
// changes require an explicit DeleteVM + EnsureVM, matching the
// backend.Backend docstring).
func (b *Backend) EnsureVM(ctx context.Context, spec backend.VMSpec) error {
	if err := b.requireLima(ctx); err != nil {
		return err
	}
	if err := os.MkdirAll(b.dataDir, 0o755); err != nil {
		return fmt.Errorf("lima backend: create data dir: %w", err)
	}
	yamlPath := filepath.Join(b.dataDir, "lima.yaml")
	forwards, err := loadForwards(b.dataDir)
	if err != nil {
		return fmt.Errorf("lima backend: load forwards: %w", err)
	}
	if err := writeLimaYAML(yamlPath, spec, forwards); err != nil {
		return fmt.Errorf("lima backend: write lima.yaml: %w", err)
	}
	exists, err := b.vmExists(ctx)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	if _, err := b.runner.Run(
		ctx, "limactl", "create", "--tty=false",
		"--name="+b.name, yamlPath,
	); err != nil {
		return fmt.Errorf("lima backend: create vm: %w", err)
	}
	return nil
}

// StartVM boots the instance. No-op when IsRunning already reports
// true. Returns an error from requireLima if Lima is missing.
func (b *Backend) StartVM(ctx context.Context) error {
	if err := b.requireLima(ctx); err != nil {
		return err
	}
	running, err := b.IsRunning(ctx)
	if err != nil {
		return err
	}
	if running {
		return nil
	}
	if _, err := b.runner.Run(ctx, "limactl", "start", b.name); err != nil {
		return fmt.Errorf("lima backend: start vm: %w", err)
	}
	return nil
}

// StopVM gracefully shuts the VM down. No-op when already stopped.
func (b *Backend) StopVM(ctx context.Context) error {
	running, err := b.IsRunning(ctx)
	if err != nil {
		return err
	}
	if !running {
		return nil
	}
	if _, err := b.runner.Run(ctx, "limactl", "stop", b.name); err != nil {
		return fmt.Errorf("lima backend: stop vm: %w", err)
	}
	return nil
}

// IsRunning reports whether the instance is currently in the Running
// state per `limactl ls --json`. A missing instance is reported as
// (false, nil) — callers that need "does it exist?" should use vmExists
// instead (internal).
func (b *Backend) IsRunning(ctx context.Context) (bool, error) {
	inst, err := b.listInstance(ctx)
	if err != nil {
		return false, err
	}
	if inst == nil {
		return false, nil
	}
	return strings.EqualFold(inst.Status, "Running"), nil
}

// Exec runs cmd inside the VM via `limactl shell`. It honours all
// ExecOpts: Cwd via `sh -c "cd <cwd> && exec <cmd>"`, Env via KEY=VAL
// prefix, TTY via -t, Stdin via RunWithStdin. The returned ExecResult
// always reports an ExitCode; a -1 means the command failed to start.
func (b *Backend) Exec(ctx context.Context, cmd []string, opts backend.ExecOpts) (backend.ExecResult, error) {
	if len(cmd) == 0 {
		return backend.ExecResult{ExitCode: -1}, errors.New("lima backend: exec: empty command")
	}
	args := []string{"shell"}
	if opts.TTY {
		args = append(args, "-t")
	}
	args = append(args, b.name, "--")

	// We always wrap in `sh -c` so env-prefix + cwd-prefix compose
	// cleanly without needing to know whether the caller's cmd is a
	// single binary or a shell pipeline. Cost is one extra fork per
	// Exec, which is dwarfed by the limactl shell setup itself.
	args = append(args, "sh", "-c", buildShellCommand(cmd, opts))

	var (
		stdout []byte
		err    error
	)
	if opts.Stdin != nil {
		stdout, err = b.runner.RunWithStdin(ctx, opts.Stdin, "limactl", args...)
	} else {
		stdout, err = b.runner.Run(ctx, "limactl", args...)
	}
	res := backend.ExecResult{Stdout: stdout}
	if err == nil {
		res.ExitCode = 0
		return res, nil
	}
	var ee *exitError
	if errors.As(err, &ee) {
		res.Stderr = ee.Stderr
		res.ExitCode = ee.ExitCode
		// We surface the *result* but still return the error so the
		// caller can decide whether a non-zero exit is fatal.
		return res, nil
	}
	res.ExitCode = -1
	return res, fmt.Errorf("lima backend: exec: %w", err)
}

// ForwardPort persists the (hostPort -> guestPort) mapping under
// dataDir so that the next EnsureVM/StartVM cycle re-renders lima.yaml
// with it embedded. It additionally best-effort invokes Lima's runtime
// `limactl forward` subcommand; failure there is swallowed because not
// every Lima version ships it. The persisted state is the source of
// truth, not the live invocation.
func (b *Backend) ForwardPort(ctx context.Context, guestPort, hostPort int) error {
	if err := os.MkdirAll(b.dataDir, 0o755); err != nil {
		return fmt.Errorf("lima backend: create data dir: %w", err)
	}
	forwards, err := loadForwards(b.dataDir)
	if err != nil {
		return fmt.Errorf("lima backend: load forwards: %w", err)
	}
	for _, f := range forwards {
		if f.HostPort == hostPort && f.GuestPort != guestPort {
			return fmt.Errorf(
				"lima backend: host port %d already forwarded to guest %d",
				hostPort, f.GuestPort,
			)
		}
	}
	forwards = upsertForward(forwards, portForward{GuestPort: guestPort, HostPort: hostPort})
	if err := saveForwards(b.dataDir, forwards); err != nil {
		return fmt.Errorf("lima backend: persist forwards: %w", err)
	}
	// Best-effort runtime forward. Older Lima versions do not have a
	// `forward` subcommand; we ignore the error and rely on the YAML.
	_, _ = b.runner.Run(
		ctx, "limactl", "forward", b.name,
		fmt.Sprintf("%d", hostPort),
		fmt.Sprintf("%d", guestPort),
	)
	return nil
}

// UnforwardPort removes hostPort from the tracking file. If Lima
// supports `unforward`, the call is attempted but its failure is
// swallowed for the same reason as ForwardPort. Removing a host port
// that was never forwarded is a no-op.
func (b *Backend) UnforwardPort(ctx context.Context, hostPort int) error {
	forwards, err := loadForwards(b.dataDir)
	if err != nil {
		return fmt.Errorf("lima backend: load forwards: %w", err)
	}
	filtered := removeForward(forwards, hostPort)
	if len(filtered) == len(forwards) {
		return nil
	}
	if err := saveForwards(b.dataDir, filtered); err != nil {
		return fmt.Errorf("lima backend: persist forwards: %w", err)
	}
	_, _ = b.runner.Run(
		ctx, "limactl", "unforward", b.name,
		fmt.Sprintf("%d", hostPort),
	)
	return nil
}

// DeleteVM destroys the Lima instance. Uses --force so a running VM
// gets stopped first; the caller's DeleteVM contract does not promise
// graceful shutdown. The dataDir contents (lima.yaml, forwards) are
// left in place — they're cheap to regenerate and re-creating a VM is
// a common flow.
func (b *Backend) DeleteVM(ctx context.Context) error {
	if _, err := b.runner.Run(ctx, "limactl", "delete", b.name, "--force"); err != nil {
		return fmt.Errorf("lima backend: delete vm: %w", err)
	}
	return nil
}

// vmExists reports whether `limactl ls --json` knows about b.name.
// Used by EnsureVM to make creation idempotent.
func (b *Backend) vmExists(ctx context.Context) (bool, error) {
	inst, err := b.listInstance(ctx)
	if err != nil {
		return false, err
	}
	return inst != nil, nil
}

// listInstance returns the parsed entry for b.name from `limactl ls
// --json`, or nil if not present. Errors propagate from the runner or
// the JSON parser.
func (b *Backend) listInstance(ctx context.Context) (*limaInstance, error) {
	out, err := b.runner.Run(ctx, "limactl", "ls", "--json")
	if err != nil {
		return nil, fmt.Errorf("lima backend: list vms: %w", err)
	}
	insts, err := parseInstances(out)
	if err != nil {
		return nil, fmt.Errorf("lima backend: parse vm list: %w", err)
	}
	for i := range insts {
		if insts[i].Name == b.name {
			return &insts[i], nil
		}
	}
	return nil, nil
}

// buildShellCommand assembles the body passed to `sh -c` inside the
// VM. It applies env prefixes and an optional `cd` so a single helper
// covers every ExecOpts combination.
func buildShellCommand(cmd []string, opts backend.ExecOpts) string {
	var b strings.Builder
	for _, kv := range opts.Env {
		b.WriteString(kv)
		b.WriteByte(' ')
	}
	if opts.Cwd != "" {
		b.WriteString("cd ")
		b.WriteString(shellQuote(opts.Cwd))
		b.WriteString(" && exec ")
	} else {
		b.WriteString("exec ")
	}
	for i, a := range cmd {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(shellQuote(a))
	}
	return b.String()
}

// shellQuote wraps s in single quotes, escaping any embedded single
// quotes via the canonical '\'' dance. Avoids pulling in shellwords for
// one routine.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
