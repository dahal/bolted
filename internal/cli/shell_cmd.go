package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/spf13/cobra"

	"github.com/dahal/bolted/internal/backend"
)

// defaultShellPath is used when bolted.yaml does not specify a custom
// shell (the `shell:` field lands with spec 15).
const defaultShellPath = "/bin/sh"

// stdinFn is the indirection point for the interactive stdin source. Tests
// swap it so they don't depend on a real terminal. Production wires it to
// os.Stdin.
var stdinFn = func() io.Reader { return os.Stdin }

// --- bolt shell ---------------------------------------------------------------

func newShellCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shell",
		Short: "Open an interactive shell inside the VM",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runShell(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	return cmd
}

func runShell(ctx context.Context, stdout io.Writer, stderr io.Writer) error {
	// "Never initialised" check — same shape as runStatus so users see a
	// consistent diagnostic.
	cfgPath := configPath()
	if _, err := statFn(cfgPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintln(stderr, "Bolted is not initialised. Run `bolt init` first.")
			return &exitError{code: exitLocked, err: errors.New("Bolted not initialised")}
		}
		return fmt.Errorf("stat config: %w", err)
	}

	cfg, err := loadConfigFn(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Refuse if stdin is not a TTY — an interactive shell on a closed
	// stdin is just a wedged process. Honour the same `isTerminalFn`
	// indirection used by the password prompter so tests can swap one
	// fake and cover both surfaces.
	if !isTerminalFn(int(os.Stdin.Fd())) {
		fmt.Fprintln(stderr, "bolt shell requires an interactive terminal")
		return &exitError{code: exitGeneric, err: errors.New("stdin is not a terminal")}
	}

	b, err := newBackendFn(backend.Config{Backend: cfg.Backend})
	if err != nil {
		return fmt.Errorf("backend init: %w", err)
	}

	// Refuse if the VM isn't running or the Bolted is locked. The
	// shell spec says exit 2 for the locked case.
	running, err := b.IsRunning(ctx)
	if err != nil {
		return fmt.Errorf("check VM state: %w", err)
	}
	if !running {
		fmt.Fprintln(stderr, "Bolted VM is not running. Run `bolt unlock` first.")
		return &exitError{code: exitLocked, err: errors.New("VM not running")}
	}
	if isLocked(ctx, b) {
		fmt.Fprintln(stderr, "Bolted is locked. Run `bolt unlock` first.")
		return &exitError{code: exitLocked, err: errors.New("Bolted locked")}
	}

	shellPath := shellFromConfig(cfg)

	res, err := b.Exec(ctx, []string{shellPath}, backend.ExecOpts{
		TTY:   true,
		Stdin: stdinFn(),
	})
	// Always passthrough whatever output the backend buffered (real TTY
	// implementations stream live; the contract still lets us recover
	// trailing data).
	if len(res.Stdout) > 0 {
		_, _ = stdout.Write(res.Stdout)
	}
	if len(res.Stderr) > 0 {
		_, _ = stderr.Write(res.Stderr)
	}
	if err != nil {
		return fmt.Errorf("exec shell: %w", err)
	}
	if res.ExitCode != 0 {
		return &exitError{code: res.ExitCode, err: fmt.Errorf("shell exited %d", res.ExitCode)}
	}
	return nil
}

// isLocked probes `ls /bolted/repos` inside the VM. A non-zero exit
// (or any exec error) is treated as "locked" so we never wedge the user
// because of an unrelated probe failure.
func isLocked(ctx context.Context, b backend.Backend) bool {
	res, err := b.Exec(ctx, []string{"ls", vmMountpoint}, backend.ExecOpts{})
	if err != nil {
		return true
	}
	return res.ExitCode != 0
}

// shellFromConfig returns the shell path to use. Once spec 15 adds a
// `Shell` field to config.Config we can read it here; until then every
// caller gets the documented fallback.
func shellFromConfig(_ interface{}) string {
	// Future (spec 15): if cfg.Shell != "" return cfg.Shell.
	return defaultShellPath
}
