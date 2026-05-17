// stop_cmd.go implements `bolt stop <repo>` and `bolt stop --all`. Each
// stop calls Runner.Down (which removes the container — devcontainer
// CLI has no first-class pause/stop subcommand) and drops the entry
// from containers.json. The repo's data on the encrypted volume is
// untouched; only the running container is reaped.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/spf13/cobra"

	"github.com/dahal/bolted/internal/devcontainer"
)

type stopOptions struct {
	all bool
}

func newStopCmd() *cobra.Command {
	opts := &stopOptions{}
	cmd := &cobra.Command{
		Use:   "stop [<repo>]",
		Short: "Stop a repo's dev container (or all of them with --all)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStop(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args, *opts)
		},
	}
	cmd.Flags().BoolVar(&opts.all, "all", false, "Stop every running Bolted container")
	return cmd
}

func runStop(ctx context.Context, _, stderr io.Writer, args []string, opts stopOptions) error {
	// Mutually-exclusive arg shape: either --all (no repo arg) or
	// exactly one repo.
	if opts.all && len(args) > 0 {
		return errors.New("stop: cannot combine --all with a repo argument")
	}
	if !opts.all && len(args) == 0 {
		return errors.New("stop: provide a repo name or --all")
	}

	b, _, err := requireUnlockedBackend(ctx, stderr)
	if err != nil {
		return err
	}
	runner := newRunnerFn(b, devcontainer.Options{})

	containers, err := readContainers()
	if err != nil {
		return err
	}

	var targets []string
	if opts.all {
		for name := range containers {
			targets = append(targets, name)
		}
		sort.Strings(targets) // deterministic for tests / output
	} else {
		repo := args[0]
		// `bolt stop <repo>` against a repo that doesn't exist on disk
		// is exit-5 just like the other lifecycle commands.
		if err := requireRepo(ctx, b, stderr, repo); err != nil {
			return err
		}
		if _, ok := containers[repo]; !ok {
			fmt.Fprintf(stderr, "no running container for %q (nothing to stop).\n", repo)
			return nil
		}
		targets = []string{repo}
	}

	if len(targets) == 0 {
		fmt.Fprintln(stderr, "no running containers.")
		return nil
	}

	for _, name := range targets {
		id := containers[name]
		if err := runner.Down(ctx, id); err != nil {
			return fmt.Errorf("stop %q: %w", name, err)
		}
		if err := forgetContainer(name); err != nil {
			return fmt.Errorf("update containers.json: %w", err)
		}
		fmt.Fprintf(stderr, "stopped %q.\n", name)
	}
	return nil
}
