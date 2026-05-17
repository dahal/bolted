// Package wsl2 implements backend.Backend for Windows using WSL2 as the
// Linux VM provider. Every operation shells out to wsl.exe (and netsh.exe
// for the rare port-forward that can't ride on WSL's automatic localhost
// bridging). See spec 06 for the full design.
//
// The package compiles on every Go-supported OS so the rest of the
// codebase (and `go vet ./...` from a darwin dev machine) stays happy.
// Methods that actually need to call wsl.exe guard themselves with a
// runtime.GOOS == "windows" check via requireWindows; the underlying
// runner abstraction can be swapped out in tests on any platform.
package wsl2

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/dahal/bolted/internal/backend"
	"github.com/dahal/bolted/internal/config"
)

// defaultDistroName is the WSL distribution name Bolted registers. It
// is the value passed to `wsl --distribution <name>` (a.k.a. `-d <name>`)
// and shows up in `wsl --list`.
const defaultDistroName = "bolted"

// defaultRootfsRelPath is the rootfs tar location relative to the
// Bolted root when the caller doesn't override it. Spec 07's
// `vm:build` task is expected to drop the tar at this path.
const defaultRootfsRelPath = "vm/rootfs.tar"

// Backend is the Windows WSL2 implementation of backend.Backend. The
// zero value is not usable; construct one via New or NewWithOptions.
type Backend struct {
	// name is the WSL distribution name (passed to `wsl -d <name>`).
	name string
	// installDir is the host directory WSL will store the distro's VHD
	// under (passed to `wsl --import`). Also holds Bolted-managed
	// metadata (ports.json, the per-distro .wslconfig hint).
	installDir string
	// rootfsPath is the absolute path to the rootfs tar consumed by
	// `wsl --import`. Built by spec 07's vm:build pipeline.
	rootfsPath string
	// runner is the indirection point for external process invocation.
	// Swappable in tests.
	runner runner
}

// Options configures NewWithOptions. The zero value yields the same
// defaults as New: distro name "bolted", install dir under
// $BOLTED_HOME/vm/wsl, rootfs path under $BOLTED_HOME/vm/rootfs.tar,
// and the real wsl.exe runner.
type Options struct {
	// Name overrides the distro name. Empty means "bolted".
	Name string
	// InstallDir overrides the host install directory. Empty means
	// <BoltedDir>/vm/wsl.
	InstallDir string
	// RootfsPath overrides the rootfs tar location. Empty means
	// <BoltedDir>/vm/rootfs.tar.
	RootfsPath string
	// Runner overrides the external-command runner. Nil means the
	// production realRunner.
	Runner runner
}

// New returns a Backend wired with production defaults: distro name
// "bolted", install dir under <BoltedDir>/vm/wsl, rootfs path
// under <BoltedDir>/vm/rootfs.tar, real wsl.exe runner.
//
// The factory (internal/backend/factory) calls New() with no args, so
// the no-arg form must continue to work. Callers that need to override
// any field should use NewWithOptions.
func New() *Backend {
	return NewWithOptions(Options{})
}

// NewWithOptions returns a Backend with the given overrides applied on
// top of the defaults documented on Options.
func NewWithOptions(opts Options) *Backend {
	name := opts.Name
	if name == "" {
		name = defaultDistroName
	}
	installDir := opts.InstallDir
	if installDir == "" {
		installDir = filepath.Join(config.BoltedDir(), "vm", "wsl")
	}
	rootfs := opts.RootfsPath
	if rootfs == "" {
		rootfs = filepath.Join(config.BoltedDir(), defaultRootfsRelPath)
	}
	r := opts.Runner
	if r == nil {
		r = realRunner{}
	}
	return &Backend{
		name:       name,
		installDir: installDir,
		rootfsPath: rootfs,
		runner:     r,
	}
}

// requireWindowsFn is the indirection point for the OS guard. Tests
// swap it out to exercise the wsl.exe call paths from a non-Windows
// host. Production code never touches it.
var requireWindowsFn = defaultRequireWindows

// currentGOOS is a second indirection layer so the success branch of
// defaultRequireWindows is reachable from unit tests on non-Windows hosts.
var currentGOOS = runtime.GOOS

// defaultRequireWindows returns nil when running on a Windows host. On
// any other GOOS it returns an error explaining that the WSL2 backend
// can only execute on Windows. The check is *runtime* (not build-time)
// so the rest of the codebase can still build, vet and unit-test on
// macOS or Linux.
func defaultRequireWindows() error {
	if currentGOOS == "windows" {
		return nil
	}
	return fmt.Errorf("wsl2 backend: cannot run on %s; the WSL2 backend only works on Windows", currentGOOS)
}

// requireWindows is the package-internal entrypoint; it always delegates
// to requireWindowsFn so tests can override.
func requireWindows() error { return requireWindowsFn() }

// requireWSL probes for `wsl.exe` via `wsl --version`. A missing or
// erroring binary becomes a user-facing message pointing at the install
// command. Only called from EnsureVM and StartVM — the heavy operations
// where missing WSL is a real failure; methods like UnforwardPort that
// can complete from on-disk state alone don't require it.
func (b *Backend) requireWSL(ctx context.Context) error {
	if _, err := b.runner.Run(ctx, "wsl.exe", "--version"); err != nil {
		return fmt.Errorf("wsl2 backend: WSL2 is not installed or not on PATH; install it with `wsl --install` (run as Administrator), then retry. underlying error: %w", err)
	}
	return nil
}

// EnsureVM imports the Bolted distro if it doesn't already exist.
// Idempotent: an existing distro of the configured name is left alone
// (per the backend.Backend contract; reconfiguring resources is a
// separate flow). The per-distro .wslconfig hint is always (re-)written
// regardless of whether the import ran.
func (b *Backend) EnsureVM(ctx context.Context, spec backend.VMSpec) error {
	if err := requireWindows(); err != nil {
		return err
	}
	if err := b.requireWSL(ctx); err != nil {
		return err
	}

	exists, err := b.distroExists(ctx)
	if err != nil {
		return err
	}

	if !exists {
		if _, statErr := os.Stat(b.rootfsPath); statErr != nil {
			return fmt.Errorf("wsl2 backend: rootfs tar not found at %s (build it with `task vm:build`): %w", b.rootfsPath, statErr)
		}
		if err := os.MkdirAll(b.installDir, 0o755); err != nil {
			return fmt.Errorf("wsl2 backend: create install dir: %w", err)
		}
		if _, err := b.runner.Run(ctx, "wsl.exe", "--import", b.name, b.installDir, b.rootfsPath, "--version", "2"); err != nil {
			return fmt.Errorf("wsl2 backend: import distro %q: %w", b.name, err)
		}
	}

	// Always (re-)write the per-distro .wslconfig hint so it stays
	// in sync with the requested spec. Warning is intentionally
	// surfaced via stderr; we don't have a logger plumbed through.
	if spec.MemoryMB > 0 && spec.CPUs > 0 {
		globalExists := globalWSLConfigExists()
		_, warn, err := writeWSLConfigHint(b.installDir, spec.MemoryMB, spec.CPUs, globalExists)
		if err != nil {
			return fmt.Errorf("wsl2 backend: write .wslconfig hint: %w", err)
		}
		if warn != "" {
			fmt.Fprintln(os.Stderr, "wsl2 backend warning:", warn)
		}
	}
	return nil
}

// StartVM forces the distro to boot. WSL2 distros auto-start on first
// `wsl` invocation, so we trigger that with a no-op `true`. Any error
// surfaced by wsl.exe is returned as-is.
func (b *Backend) StartVM(ctx context.Context) error {
	if err := requireWindows(); err != nil {
		return err
	}
	if err := b.requireWSL(ctx); err != nil {
		return err
	}
	if _, err := b.runner.Run(ctx, "wsl.exe", "-d", b.name, "--", "true"); err != nil {
		return fmt.Errorf("wsl2 backend: start distro %q: %w", b.name, err)
	}
	return nil
}

// StopVM terminates the distro. Per the backend contract this is a
// no-op when the distro is already stopped — wsl.exe itself treats
// terminating a stopped distro as success, so we just propagate.
func (b *Backend) StopVM(ctx context.Context) error {
	if err := requireWindows(); err != nil {
		return err
	}
	if _, err := b.runner.Run(ctx, "wsl.exe", "--terminate", b.name); err != nil {
		return fmt.Errorf("wsl2 backend: terminate distro %q: %w", b.name, err)
	}
	return nil
}

// IsRunning parses `wsl --list --running --quiet` and looks for the
// configured distro name. The output may be UTF-16LE; decodeWSLOutput
// handles both encodings.
func (b *Backend) IsRunning(ctx context.Context) (bool, error) {
	if err := requireWindows(); err != nil {
		return false, err
	}
	out, err := b.runner.Run(ctx, "wsl.exe", "--list", "--running", "--quiet")
	if err != nil {
		return false, fmt.Errorf("wsl2 backend: list running distros: %w", err)
	}
	return containsDistro(decodeWSLOutput(out), b.name), nil
}

// distroExists parses `wsl --list --quiet` to determine whether the
// configured distro is registered (regardless of whether it's currently
// running).
func (b *Backend) distroExists(ctx context.Context) (bool, error) {
	out, err := b.runner.Run(ctx, "wsl.exe", "--list", "--quiet")
	if err != nil {
		return false, fmt.Errorf("wsl2 backend: list distros: %w", err)
	}
	return containsDistro(decodeWSLOutput(out), b.name), nil
}

// containsDistro returns true iff one of the whitespace-separated
// tokens in s equals name. WSL's `--quiet` output puts one distro per
// line; we tolerate trailing whitespace, NULs already stripped by
// decodeWSLOutput.
func containsDistro(s, name string) bool {
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == name {
			return true
		}
	}
	return false
}

// Exec runs cmd inside the distro and returns the captured stdout /
// stderr / exit code. cmd is concatenated and handed to `sh -c` inside
// the distro so callers can use shell features (pipes, redirection,
// $-expansion) naturally.
//
// opts honoured:
//   - Cwd  → `--cd <dir>` flag (supported in modern WSL builds);
//   - Env  → `KEY=VALUE ...` prefix on the `sh -c` payload;
//   - Stdin → piped via runner.RunWithStdin;
//   - TTY → currently ignored. wsl.exe will allocate a TTY when stdin
//     is a terminal anyway, so explicit handling is deferred until a
//     real need arrives.
func (b *Backend) Exec(ctx context.Context, cmd []string, opts backend.ExecOpts) (backend.ExecResult, error) {
	if err := requireWindows(); err != nil {
		return backend.ExecResult{ExitCode: -1}, err
	}
	if len(cmd) == 0 {
		return backend.ExecResult{ExitCode: -1}, errors.New("wsl2 backend: Exec requires at least one cmd argument")
	}

	args := []string{"-d", b.name}
	if opts.Cwd != "" {
		args = append(args, "--cd", opts.Cwd)
	}
	args = append(args, "--", "sh", "-c", formatEnv(opts.Env)+joinCmd(cmd))

	var (
		stdout []byte
		err    error
	)
	if opts.Stdin != nil {
		stdout, err = b.runner.RunWithStdin(ctx, opts.Stdin, "wsl.exe", args...)
	} else {
		stdout, err = b.runner.Run(ctx, "wsl.exe", args...)
	}

	res := backend.ExecResult{Stdout: stdout}
	if err != nil {
		// Extract the exit code from *exec.ExitError if present;
		// otherwise -1.
		res.ExitCode = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		}
		// Capture any wrapped stderr for the caller via Stderr.
		var ce *cmdError
		if errors.As(err, &ce) {
			res.Stderr = bytes.TrimRight(ce.stderr, "\r\n")
		}
		return res, err
	}
	return res, nil
}

// joinCmd quotes each token of cmd and joins them with spaces so the
// result is safe to pass to `sh -c`. Single-word commands skip the
// quoting cost.
func joinCmd(cmd []string) string {
	if len(cmd) == 1 {
		return cmd[0]
	}
	parts := make([]string, len(cmd))
	for i, c := range cmd {
		parts[i] = shellQuote(c)
	}
	return strings.Join(parts, " ")
}

// ForwardPort records a host→guest port mapping and, if hostPort !=
// guestPort, sets up a `netsh interface portproxy` rule so the host
// port reaches 127.0.0.1:<guestPort> inside the distro. When the ports
// match (the common case), WSL2's automatic localhost bridging makes
// the guest port reachable on the host as-is and no proxy rule is
// installed — the mapping is recorded for `bolt ports` clarity only.
func (b *Backend) ForwardPort(ctx context.Context, guestPort, hostPort int) error {
	if err := requireWindows(); err != nil {
		return err
	}
	if guestPort <= 0 || hostPort <= 0 {
		return fmt.Errorf("wsl2 backend: ports must be positive (got guest=%d host=%d)", guestPort, hostPort)
	}
	mappings, err := loadPortMappings(b.installDir)
	if err != nil {
		return err
	}
	mappings.Mappings[itoa(hostPort)] = guestPort
	if err := savePortMappings(b.installDir, mappings); err != nil {
		return err
	}
	if hostPort == guestPort {
		// WSL2 auto-forwards loopback binds — no netsh rule needed.
		return nil
	}
	if _, err := b.runner.Run(ctx,
		"netsh.exe", "interface", "portproxy", "add", "v4tov4",
		"listenport="+itoa(hostPort),
		"listenaddress=0.0.0.0",
		"connectport="+itoa(guestPort),
		"connectaddress=127.0.0.1",
	); err != nil {
		return fmt.Errorf("wsl2 backend: add portproxy %d→%d: %w", hostPort, guestPort, err)
	}
	return nil
}

// UnforwardPort removes hostPort from the tracking file and the netsh
// portproxy table. It is a no-op when hostPort was never forwarded.
func (b *Backend) UnforwardPort(ctx context.Context, hostPort int) error {
	if err := requireWindows(); err != nil {
		return err
	}
	if hostPort <= 0 {
		return fmt.Errorf("wsl2 backend: host port must be positive (got %d)", hostPort)
	}
	mappings, err := loadPortMappings(b.installDir)
	if err != nil {
		return err
	}
	key := itoa(hostPort)
	guestPort, tracked := mappings.Mappings[key]
	if !tracked {
		return nil
	}
	delete(mappings.Mappings, key)
	if err := savePortMappings(b.installDir, mappings); err != nil {
		return err
	}
	if hostPort == guestPort {
		// Nothing to undo — there was no netsh rule.
		return nil
	}
	if _, err := b.runner.Run(ctx,
		"netsh.exe", "interface", "portproxy", "delete", "v4tov4",
		"listenport="+itoa(hostPort),
		"listenaddress=0.0.0.0",
	); err != nil {
		return fmt.Errorf("wsl2 backend: delete portproxy %d: %w", hostPort, err)
	}
	return nil
}

// DeleteVM unregisters the distro. WSL deletes the underlying VHD as
// part of unregister, so nothing further is required on the wsl.exe
// side; the per-distro metadata files under installDir are left in
// place so a subsequent `EnsureVM` re-uses them.
func (b *Backend) DeleteVM(ctx context.Context) error {
	if err := requireWindows(); err != nil {
		return err
	}
	if _, err := b.runner.Run(ctx, "wsl.exe", "--unregister", b.name); err != nil {
		return fmt.Errorf("wsl2 backend: unregister distro %q: %w", b.name, err)
	}
	return nil
}

// globalWSLConfigExists reports whether %USERPROFILE%\.wslconfig is
// present. On non-Windows hosts (tests, dev machines) it returns false
// because the env var isn't meaningful there.
func globalWSLConfigExists() bool {
	profile := os.Getenv("USERPROFILE")
	if profile == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(profile, ".wslconfig"))
	return err == nil
}

// itoa is a tiny convenience to avoid importing strconv in three
// places. Localised here so it doesn't leak into the package's public
// surface area.
func itoa(n int) string { return fmt.Sprintf("%d", n) }

// compile-time check: *Backend satisfies backend.Backend.
var _ backend.Backend = (*Backend)(nil)
