// logs_cmd.go implements `bolt logs <repo> [-f]`. It streams the named
// repo's dev container logs by shelling out to `podman logs` inside
// the VM. Container naming follows the devcontainer package's
// `bolted-<repo>` convention so the command works even if the
// container record in containers.json has drifted — what matters is
// that podman knows the name.
package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/dahal/bolted/internal/backend"
)

type logsOptions struct {
	follow bool
}

func newLogsCmd() *cobra.Command {
	opts := &logsOptions{}
	cmd := &cobra.Command{
		Use:   "logs <repo>",
		Short: "Stream container logs for <repo>",
		Long: "Streams the dev container's stdout/stderr via `podman logs`. " +
			"Pass -f to follow the log until interrupted.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogs(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], *opts)
		},
	}
	cmd.Flags().BoolVarP(&opts.follow, "follow", "f", false, "Follow the log output (like `tail -f`)")
	return cmd
}

// containerNameForRepo returns the canonical `bolted-<repo>` name
// the devcontainer package uses when it brings the container up. The
// devcontainer package keeps its own helper private, so we re-derive
// the name here from the repo basename to avoid a new export.
func containerNameForRepo(repo string) string {
	return "bolted-" + repo
}

func runLogs(ctx context.Context, stdout, stderr io.Writer, repo string, opts logsOptions) error {
	b, _, err := requireUnlockedBackend(ctx, stderr)
	if err != nil {
		return err
	}
	if err := requireRepo(ctx, b, stderr, repo); err != nil {
		return err
	}

	args := []string{"podman", "logs"}
	if opts.follow {
		args = append(args, "-f")
	}
	args = append(args, containerNameForRepo(repo))

	res, err := b.Exec(ctx, args, backend.ExecOpts{})
	// Flush whatever output podman produced even on error so users see
	// the partial log rather than a bare diagnostic.
	if len(res.Stdout) > 0 {
		_, _ = stdout.Write(res.Stdout)
	}
	if len(res.Stderr) > 0 {
		_, _ = stderr.Write(res.Stderr)
	}
	if err != nil {
		return fmt.Errorf("podman logs: %w", err)
	}
	if res.ExitCode != 0 {
		return &exitError{code: res.ExitCode, err: fmt.Errorf("podman logs: exit %d", res.ExitCode)}
	}
	return nil
}
