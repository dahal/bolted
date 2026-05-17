// export_cmd.go implements `bolt export <repo> <vm-path> <host-dest>`.
// The flow is deliberately conservative because we're moving bytes
// out of the encrypted volume onto the host filesystem:
//
//  1. Confirm the operation interactively (unless --yes). Mention
//     both the source (vm-path) and destination (host-dest) so the
//     user can spot a typo before bytes leak.
//  2. Refuse to overwrite an existing host path without --force —
//     the same posture `cp` takes when invoked with no -f.
//  3. Reject directory exports for MVP. `tar c | tar x` would work
//     but adds error surface (partial extracts, permissions) that
//     isn't justified for the first cut. Spec 20 § Non-goals covers
//     bulk export as post-MVP.
//
// The bytes are read via `cat <vm-path>` through Backend.Exec — the
// VM's cat streams the file to stdout, which the backend captures
// into ExecResult.Stdout. We then write that buffer to the host file.
// For very large files this is suboptimal (full buffer in memory), but
// the alternative (streaming through a goroutine) requires extending
// the Backend interface, which is out of scope here.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dahal/bolted/internal/backend"
)

// exportStdinFn is the indirection point that lets tests inject canned
// answers into the confirmation prompt. Production wires it to the
// shared stdinFn so `bolt export` matches the rest of the CLI.
var exportStdinFn = func() io.Reader { return stdinFn() }

// hostWriteFileFn / hostStatFn are the file-system seam tests use to
// avoid touching real paths. Production wires them to os.* directly.
var (
	hostWriteFileFn = os.WriteFile
	hostStatFn      = os.Stat
)

type exportOptions struct {
	yes   bool
	force bool
}

func newExportCmd() *cobra.Command {
	opts := &exportOptions{}
	cmd := &cobra.Command{
		Use:   "export <repo> <vm-path> <host-dest>",
		Short: "Copy a file from the encrypted volume to the host",
		Long: "Copies the named file from the repo's directory inside the " +
			"encrypted volume to a host path. Refuses to overwrite an existing " +
			"host path without --force. Skips the confirmation prompt with --yes.",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExport(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], args[1], args[2], *opts)
		},
	}
	cmd.Flags().BoolVarP(&opts.yes, "yes", "y", false, "Skip the confirmation prompt")
	cmd.Flags().BoolVar(&opts.force, "force", false, "Overwrite the host destination if it already exists")
	return cmd
}

func runExport(ctx context.Context, _, stderr io.Writer, repo, vmPath, hostDest string, opts exportOptions) error {
	b, _, err := requireUnlockedBackend(ctx, stderr)
	if err != nil {
		return err
	}
	if err := requireRepo(ctx, b, stderr, repo); err != nil {
		return err
	}

	// Refuse directory sources up-front so we don't waste an Exec
	// roundtrip on something we won't support.
	if dir, err := isVMDir(ctx, b, vmPath); err != nil {
		return fmt.Errorf("probe %s: %w", vmPath, err)
	} else if dir {
		return fmt.Errorf("export: %q is a directory; directory export is not yet supported", vmPath)
	}

	// Host overwrite check: if the destination exists and --force isn't
	// set, bail before we touch anything.
	if _, err := hostStatFn(hostDest); err == nil {
		if !opts.force {
			return fmt.Errorf("export: host path %q already exists (use --force to overwrite)", hostDest)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat host dest: %w", err)
	}

	if !opts.yes {
		fmt.Fprintf(stderr, "This will copy %s from the encrypted volume to your host at %s. Continue? [y/N] ", vmPath, hostDest)
		answer, err := readLine(exportStdinFn())
		if err != nil {
			return fmt.Errorf("read confirmation: %w", err)
		}
		if !isAffirmative(answer) {
			fmt.Fprintln(stderr, "aborted.")
			return &exitError{code: exitGeneric, err: errors.New("user aborted")}
		}
	}

	res, err := b.Exec(ctx, []string{"cat", vmPath}, backend.ExecOpts{})
	if err != nil {
		return fmt.Errorf("read %s: %w", vmPath, err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("read %s: exit %d: %s", vmPath, res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	if err := hostWriteFileFn(hostDest, res.Stdout, 0o600); err != nil {
		return fmt.Errorf("write host dest: %w", err)
	}
	fmt.Fprintf(stderr, "exported %s -> %s (%d bytes).\n", vmPath, hostDest, len(res.Stdout))
	return nil
}

// isVMDir reports whether vmPath inside the VM is a directory. Uses
// `test -d` (exit 0 = yes). A backend-level error is propagated so the
// caller can surface it; non-zero exit codes are treated as "not a
// directory" (which also covers the "doesn't exist" case — `cat` will
// then produce the real diagnostic).
func isVMDir(ctx context.Context, b backend.Backend, vmPath string) (bool, error) {
	res, err := b.Exec(ctx, []string{"test", "-d", vmPath}, backend.ExecOpts{})
	if err != nil {
		return false, err
	}
	return res.ExitCode == 0, nil
}
