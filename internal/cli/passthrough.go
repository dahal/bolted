package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"

	"github.com/dahal/bolted/internal/backend"
)

// stderr is the stream the passthrough router writes its own diagnostics to.
// Tests swap this.
var stderr io.Writer = os.Stderr

// stdout is the stream passthrough copies the inner command's stdout to.
// Tests swap this.
var stdout io.Writer = os.Stdout

// passthroughStdinFn returns the reader piped to the inner command as stdin.
// Tests swap it so they don't depend on a real terminal.
var passthroughStdinFn = func() io.Reader { return os.Stdin }

// passthroughIsTerminalFn reports whether the host stdin is a TTY. Tests swap
// it; production reuses isTerminalFn (which itself delegates to term.IsTerminal).
var passthroughIsTerminalFn = func() bool {
	return isTerminalFn(int(os.Stdin.Fd()))
}

// passthroughStub is the entrypoint Execute calls when the first arg isn't a
// reserved subcommand (or when the user used `--` to force passthrough). The
// name is preserved so cli.go's Execute can call it without a rename round-trip.
func passthroughStub(args []string) int {
	return passthroughRun(args)
}

// passthroughRun implements spec 11: run the user's command verbatim inside
// the VM. The args slice is everything after the program name (the same shape
// Execute receives). It returns the exit code to surface as bolt's own.
//
// Argument parsing:
//   - A leading `--` is stripped and everything after it is treated as the
//     literal command (this is the escape hatch from AC 4: `bolt -- ls /etc`).
//   - Otherwise `--cwd <path>` (long form only, in either `--cwd path` or
//     `--cwd=path` shape) is honoured if it appears BEFORE the command name.
//     Anything we don't recognise terminates the flag scan and becomes the
//     inner command. Tracking only `--cwd` keeps us out of Cobra's lane and
//     means user flags like `git --version` flow through untouched.
func passthroughRun(args []string) int {
	cwd, cmd, perr := parsePassthroughArgs(args)
	if perr != nil {
		fmt.Fprintf(stderr, "bolt: %v (see spec 11 — passthrough router)\n", perr)
		return exitGeneric
	}
	if len(cmd) == 0 {
		fmt.Fprintln(stderr, "bolt: no command given (see spec 11 — passthrough router)")
		return exitGeneric
	}

	// Default cwd is /bolted/repos. A relative --cwd is rooted at the
	// default; absolute paths win as-is.
	target := vmMountpoint
	if cwd != "" {
		if path.IsAbs(cwd) {
			target = cwd
		} else {
			target = path.Join(vmMountpoint, cwd)
		}
	}

	ctx := context.Background()

	// "Never initialised" check — same shape as runStatus / runShell so
	// users see a consistent diagnostic.
	cfgPath := configPath()
	if _, err := statFn(cfgPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintln(stderr, "Bolted is not initialised. Run `bolt init` first (see spec 11 — passthrough router).")
			return exitLocked
		}
		fmt.Fprintf(stderr, "bolt: stat config: %v (see spec 11 — passthrough router)\n", err)
		return exitGeneric
	}

	cfg, err := loadConfigFn(cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "bolt: load config: %v (see spec 11 — passthrough router)\n", err)
		return exitGeneric
	}

	b, err := newBackendFn(backend.Config{Backend: cfg.Backend})
	if err != nil {
		fmt.Fprintf(stderr, "bolt: backend init: %v (see spec 11 — passthrough router)\n", err)
		return exitGeneric
	}

	// Lock probe: the spec says "exit 0 = unlocked, anything else = locked".
	// We use `test -d /bolted/repos` so the probe is cheap and stays
	// silent on stdout (no stray output to confuse callers).
	probe, perr := b.Exec(ctx, []string{"test", "-d", vmMountpoint}, backend.ExecOpts{})
	if perr != nil {
		fmt.Fprintf(stderr, "bolt: probe Bolted state: %v (see spec 11 — passthrough router)\n", perr)
		return exitGeneric
	}
	if probe.ExitCode != 0 {
		fmt.Fprintln(stderr, "Bolted is locked. Run `bolt unlock` first (see spec 11 — passthrough router).")
		return exitLocked
	}

	// Build ExecOpts. TTY follows the host stdin's terminal-ness so
	// interactive commands (e.g. `bolt vim file.go`) get a pty inside the VM.
	opts := backend.ExecOpts{
		Cwd:   target,
		Stdin: passthroughStdinFn(),
		TTY:   passthroughIsTerminalFn(),
	}

	res, err := b.Exec(ctx, cmd, opts)
	// Always forward whatever the backend buffered — even on error — so
	// users see partial output from a crashing program.
	if len(res.Stdout) > 0 {
		_, _ = stdout.Write(res.Stdout)
	}
	if len(res.Stderr) > 0 {
		_, _ = stderr.Write(res.Stderr)
	}
	if err != nil {
		fmt.Fprintf(stderr, "bolt: exec %q: %v (see spec 11 — passthrough router)\n", cmd[0], err)
		return exitGeneric
	}
	// The inner command's exit code becomes bolt's exit code (AC 5).
	return res.ExitCode
}

// parsePassthroughArgs extracts an optional --cwd value and returns the
// remaining argv as the command to execute. A leading `--` is consumed and
// everything after it is the literal command (no further flag parsing).
//
// Returns an error if --cwd is given without a value.
func parsePassthroughArgs(args []string) (cwd string, cmd []string, err error) {
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "--":
			// Escape hatch: everything after `--` is the literal command.
			return cwd, args[i+1:], nil
		case a == "--cwd":
			if i+1 >= len(args) {
				return "", nil, errors.New("--cwd requires a value")
			}
			cwd = args[i+1]
			i += 2
		case len(a) > len("--cwd=") && a[:len("--cwd=")] == "--cwd=":
			cwd = a[len("--cwd="):]
			i++
		default:
			// First non-recognised token is the start of the command.
			return cwd, args[i:], nil
		}
	}
	return cwd, nil, nil
}
