// import_cmd.go implements `bolt import <host-path> <repo> <repo-dest>`.
// This is the mirror of `bolt export` — bytes flow the other direction,
// from the host file system into the encrypted volume. The confirm
// prompt is even more important here than for export because the
// user is opting in to mixing host bytes (potentially untrusted)
// with the Bolted volume.
//
// Implementation: read the host file in full, pipe it to
// `sh -c "cat > <vm-dest>"` via Backend.Exec's Stdin. Using `cat` (vs
// `tee`) keeps the surface small and avoids accidentally printing the
// payload to a captured stdout. Directories aren't supported for the
// MVP — see export_cmd.go for the same rationale.
package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dahal/bolted/internal/backend"
)

// importStdinFn is the indirection point for the confirmation prompt
// stdin source. Production wires it to the shared stdinFn.
var importStdinFn = func() io.Reader { return stdinFn() }

// Host-side filesystem seams so tests can avoid touching real files
// when exercising error paths.
var (
	hostReadFileFn = os.ReadFile
	importStatFn   = os.Stat
)

type importOptions struct {
	yes bool
}

func newImportCmd() *cobra.Command {
	opts := &importOptions{}
	cmd := &cobra.Command{
		Use:   "import <host-path> <repo> <repo-dest>",
		Short: "Copy a host file into the encrypted volume",
		Long: "Copies the named host file into the repo's directory inside " +
			"the encrypted volume. Skips the confirmation prompt with --yes.",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runImport(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], args[1], args[2], *opts)
		},
	}
	cmd.Flags().BoolVarP(&opts.yes, "yes", "y", false, "Skip the confirmation prompt")
	return cmd
}

func runImport(ctx context.Context, _, stderr io.Writer, hostPath, repo, repoDest string, opts importOptions) error {
	b, _, err := requireUnlockedBackend(ctx, stderr)
	if err != nil {
		return err
	}
	if err := requireRepo(ctx, b, stderr, repo); err != nil {
		return err
	}

	// Reject directory sources up-front so we never tell the user
	// "complete" after copying nothing.
	info, err := importStatFn(hostPath)
	if err != nil {
		return fmt.Errorf("stat host path: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("import: %q is a directory; directory import is not yet supported", hostPath)
	}

	vmDest := path.Join(repoPath(repo), repoDest)

	if !opts.yes {
		fmt.Fprintf(stderr, "This will copy %s from your host into the encrypted volume at %s. Continue? [y/N] ", hostPath, vmDest)
		answer, err := readLine(importStdinFn())
		if err != nil {
			return fmt.Errorf("read confirmation: %w", err)
		}
		if !isAffirmative(answer) {
			fmt.Fprintln(stderr, "aborted.")
			return &exitError{code: exitGeneric, err: errors.New("user aborted")}
		}
	}

	data, err := hostReadFileFn(hostPath)
	if err != nil {
		return fmt.Errorf("read host path: %w", err)
	}

	res, err := b.Exec(ctx, []string{"sh", "-c", "cat > " + shellEscapeSingle(vmDest)}, backend.ExecOpts{
		Stdin: bytes.NewReader(data),
	})
	if err != nil {
		return fmt.Errorf("write to volume: %w", err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("write to volume: exit %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	fmt.Fprintf(stderr, "imported %s -> %s (%d bytes).\n", hostPath, vmDest, len(data))
	return nil
}

// shellEscapeSingle wraps s in single quotes for safe inclusion in a
// `sh -c` payload. Embedded single quotes are escaped by closing the
// quote, inserting an escaped single, and re-opening (`'\''`). This is
// the canonical POSIX quoting trick — keeps even paths with quotes,
// spaces, or `$` safe.
func shellEscapeSingle(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
