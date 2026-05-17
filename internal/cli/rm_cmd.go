// rm_cmd.go implements `bolt rm <repo>`. The sequence is:
//
//  1. Stop the container if running.
//  2. Prompt for "y" confirmation (skippable with --yes).
//  3. `rm -rf /bolted/repos/<repo>` inside the VM.
//
// Step 3 is intentionally crude — the repo dir is rebuilt by `bolt git
// clone <url>` anyway, so a future undelete is not a goal.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dahal/bolted/internal/backend"
	"github.com/dahal/bolted/internal/devcontainer"
)

// rmStdinFn is the indirection point that lets tests feed canned
// answers into the confirmation prompt. Production wires it to the
// existing stdinFn so both `bolt shell` and `bolt rm` share the same
// stdin source.
var rmStdinFn = func() io.Reader { return stdinFn() }

type rmOptions struct {
	yes bool
}

func newRmCmd() *cobra.Command {
	opts := &rmOptions{}
	cmd := &cobra.Command{
		Use:   "rm <repo>",
		Short: "Remove a repo from Bolted (stops its container, deletes the directory)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRm(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], *opts)
		},
	}
	cmd.Flags().BoolVarP(&opts.yes, "yes", "y", false, "Skip the confirmation prompt")
	return cmd
}

func runRm(ctx context.Context, _, stderr io.Writer, repo string, opts rmOptions) error {
	b, _, err := requireUnlockedBackend(ctx, stderr)
	if err != nil {
		return err
	}
	if err := requireRepo(ctx, b, stderr, repo); err != nil {
		return err
	}

	// 1. Stop the container if a record exists.
	containers, err := readContainers()
	if err != nil {
		return err
	}
	if id, ok := containers[repo]; ok {
		runner := newRunnerFn(b, devcontainer.Options{})
		if err := runner.Down(ctx, id); err != nil {
			return fmt.Errorf("stop container: %w", err)
		}
		if err := forgetContainer(repo); err != nil {
			return fmt.Errorf("update containers.json: %w", err)
		}
	}

	// 2. Confirm. Refuse to remove on any answer other than y/yes
	// (case-insensitive). --yes skips the whole prompt.
	if !opts.yes {
		fmt.Fprintf(stderr, "remove repo %q from Bolted? (y/N) ", repo)
		answer, err := readLine(rmStdinFn())
		if err != nil {
			return fmt.Errorf("read confirmation: %w", err)
		}
		if !isAffirmative(answer) {
			fmt.Fprintln(stderr, "aborted.")
			return &exitError{code: exitGeneric, err: errors.New("user aborted")}
		}
	}

	// 3. Actually delete. `rm -rf` returns non-zero only if the path
	// is in some unrecoverable state; the typical "already gone"
	// case exits 0 because we passed -f.
	res, err := b.Exec(ctx, []string{"rm", "-rf", repoPath(repo)}, backend.ExecOpts{})
	if err != nil {
		return fmt.Errorf("rm repo: %w", err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("rm repo: exit %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	fmt.Fprintf(stderr, "removed %q.\n", repo)
	return nil
}

// readLine pulls a single line off r. EOF without any data is treated
// as an empty answer (rather than an error) so piping `</dev/null`
// yields a clean "aborted" rather than a noisy stack-style failure.
func readLine(r io.Reader) (string, error) {
	br := bufio.NewReader(r)
	line, err := br.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// isAffirmative reports whether s is "y" or "yes" (case-insensitive
// and trimmed). Anything else, including blank input, is a no.
func isAffirmative(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "y", "yes":
		return true
	}
	return false
}
