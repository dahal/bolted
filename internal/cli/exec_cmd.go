// exec_cmd.go implements `bolt exec <repo> <cmd...>`: a one-shot,
// non-interactive command in the repo's dev container. If the
// container isn't running yet we transparently bring it up first so
// users don't have to remember to `bolt dev <repo>` in advance.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/dahal/bolted/internal/devcontainer"
)

func newExecCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec <repo> <cmd> [args...]",
		Short: "Run a one-shot command inside the repo's dev container",
		Long: "Runs <cmd> inside the named repo's dev container, non-interactively. " +
			"The container is started on demand if it isn't already running.",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExec(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], args[1:])
		},
	}
	return cmd
}

func runExec(ctx context.Context, stdout, stderr io.Writer, repo string, command []string) error {
	if len(command) == 0 {
		return errors.New("exec: no command given")
	}
	b, _, err := requireUnlockedBackend(ctx, stderr)
	if err != nil {
		return err
	}
	if err := requireRepo(ctx, b, stderr, repo); err != nil {
		return err
	}

	runner := newRunnerFn(b, devcontainer.Options{})

	// Bring the container up if we have no record of it.
	containerID, err := storedContainerID(repo)
	if err != nil {
		return err
	}
	if containerID == "" {
		id, err := runner.Up(ctx, repoPath(repo), devcontainer.UpOpts{})
		if err != nil {
			return fmt.Errorf("devcontainer up: %w", err)
		}
		if err := recordContainer(repo, id); err != nil {
			return fmt.Errorf("persist container id: %w", err)
		}
		containerID = id
	}

	res, err := runner.Exec(ctx, containerID, command, devcontainer.ExecOpts{
		Cwd: repoPath(repo),
	})
	if len(res.Stdout) > 0 {
		_, _ = stdout.Write(res.Stdout)
	}
	if len(res.Stderr) > 0 {
		_, _ = stderr.Write(res.Stderr)
	}
	if err != nil {
		return fmt.Errorf("exec in container: %w", err)
	}
	if res.ExitCode != 0 {
		return &exitError{code: res.ExitCode, err: fmt.Errorf("command exited %d", res.ExitCode)}
	}
	return nil
}
