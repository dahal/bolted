// trust_cmd.go implements `bolt trust <repo>` and `bolt trust --revoke <repo>`
// from spec 18. These commands manage the per-repo approval map in
// ~/.bolted/state/devcontainer-trust.json without bringing up a dev
// container. They're the manual flip-side of the interactive trust gate
// that runs inside `bolt dev` / `bolt exec` (the gate itself will be wired
// into those commands by a follow-up integration commit — see the
// package-level comment on internal/devcontainertrust).
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/dahal/bolted/internal/backend"
	"github.com/dahal/bolted/internal/devcontainertrust"
)

// newTrustStoreFn is the indirection point so tests can substitute a
// throwaway store rooted at a temp dir. Production opens the real store
// pointed at ~/.bolted/state/.
var newTrustStoreFn = func() *devcontainertrust.Store {
	return devcontainertrust.NewStore(stateDirFn())
}

// hashConfigFn is the indirection point for hashing a repo's
// devcontainer.json. Tests swap it so they don't have to script a fake
// `cat` exec.
var hashConfigFn = func(b backend.Backend, repoPath string) (string, string, error) {
	return devcontainertrust.HashConfig(b, repoPath)
}

type trustOptions struct {
	revoke bool
}

// newTrustCmd builds `bolt trust <repo>`. With no flags, it computes the
// current hash of <repo>'s devcontainer.json and records it as approved
// (the manual equivalent of saying "yes" to the interactive gate). With
// --revoke, it clears any recorded approval; the next `bolt dev` will
// re-prompt.
func newTrustCmd() *cobra.Command {
	opts := &trustOptions{}
	cmd := &cobra.Command{
		Use:   "trust <repo>",
		Short: "Approve (or with --revoke, un-approve) a repo's devcontainer.json",
		Long: "Records the sha256 of <repo>'s .devcontainer/devcontainer.json " +
			"in ~/.bolted/state/devcontainer-trust.json so subsequent " +
			"`bolt dev <repo>` invocations skip the trust prompt. " +
			"Use --revoke to clear the approval and re-prompt next time.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTrust(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], *opts)
		},
	}
	cmd.Flags().BoolVar(&opts.revoke, "revoke", false, "Clear the recorded approval for <repo>")
	return cmd
}

func runTrust(ctx context.Context, _ io.Writer, stderr io.Writer, repo string, opts trustOptions) error {
	store := newTrustStoreFn()

	// --revoke is the cheap path: no backend needed, just drop the
	// entry. The next dev/exec invocation will re-prompt.
	if opts.revoke {
		if err := store.Revoke(repo); err != nil {
			return fmt.Errorf("trust revoke: %w", err)
		}
		fmt.Fprintf(stderr, "approval for %q cleared.\n", repo)
		return nil
	}

	// Approve flow: require Bolted to be unlocked so we can read the
	// devcontainer.json out of the VM, hash it, and record it.
	b, _, err := requireUnlockedBackend(ctx, stderr)
	if err != nil {
		return err
	}
	if err := requireRepo(ctx, b, stderr, repo); err != nil {
		return err
	}

	hash, _, err := hashConfigFn(b, repoPath(repo))
	if err != nil {
		if errors.Is(err, devcontainertrust.ErrNoConfig) {
			fmt.Fprintf(stderr, "repo %q has no .devcontainer/devcontainer.json — nothing to approve.\n", repo)
			return &exitError{code: exitGeneric, err: err}
		}
		return fmt.Errorf("hash devcontainer.json: %w", err)
	}
	if err := store.Approve(repo, hash); err != nil {
		return fmt.Errorf("record approval: %w", err)
	}
	fmt.Fprintf(stderr, "approved devcontainer.json for %q (hash=%s).\n", repo, hash)
	return nil
}
