// dev_cmd.go implements `bolt dev <repo>` and houses the shared helpers
// (state-dir resolution, the containers.json reader/writer, the
// runner indirection, the locked / repo-not-found gates) that the
// other lifecycle commands in this spec also use. Keeping the helpers
// here avoids spreading tiny one-line files across the package; every
// other lifecycle command in this spec depends on `dev` conceptually
// (you cannot stop/list/remove something you never started), so this
// is the natural home.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/dahal/bolted/internal/backend"
	"github.com/dahal/bolted/internal/config"
	"github.com/dahal/bolted/internal/devcontainer"
	"github.com/dahal/bolted/internal/devcontainertrust"
	"github.com/dahal/bolted/internal/state"
)

// --- shared lifecycle helpers ----------------------------------------------

// newRunnerFn is the indirection point that lets tests swap a fake
// devcontainer.Runner in place of the real CLI wrapper. Production
// just calls devcontainer.New.
var newRunnerFn = func(b backend.Backend, opts devcontainer.Options) devcontainer.Runner {
	return devcontainer.New(b, opts)
}

// trustGateFn is the seam for the spec-18 devcontainer trust gate. It
// runs before any devcontainer Up. If the repo's .devcontainer/devcontainer.json
// has changed (or was never approved), it prompts the user; --trust
// auto-approves without prompting.
//
// Tests stub this to a no-op via withRunnerStub so the existing dev/exec
// flows aren't forced through an interactive prompt.
var trustGateFn = realTrustGate

// confirmTrustFn is the injectable Confirm prompt. The real one
// requires an interactive TTY; tests substitute it.
var confirmTrustFn = devcontainertrust.Confirm

// realTrustGate is the production implementation. Returns nil when the
// repo has no devcontainer.json (nothing to gate) or when the current
// hash is already approved.
func realTrustGate(_ context.Context, b backend.Backend, repo, repoFullPath string, stdin io.Reader, stderrW io.Writer, autoApprove bool) error {
	hash, summary, err := devcontainertrust.HashConfig(b, repoFullPath)
	if err != nil {
		if errors.Is(err, devcontainertrust.ErrNoConfig) {
			return nil // nothing to gate
		}
		return fmt.Errorf("hash devcontainer config: %w", err)
	}
	store := devcontainertrust.NewStore(stateDirFn())
	if store.Approved(repo, hash) {
		return nil
	}
	if autoApprove {
		return store.Approve(repo, hash)
	}
	ok, err := confirmTrustFn(stdin, stderrW, summary)
	if err != nil {
		return fmt.Errorf("trust confirm: %w", err)
	}
	if !ok {
		return errors.New("devcontainer.json not approved (run `bolt trust <repo>` to approve)")
	}
	return store.Approve(repo, hash)
}

// stateDirFn returns the directory holding the JSON state files
// (containers.json, ports.json, …). Indirection so tests can point at
// a temp dir without changing BOLTED_HOME.
var stateDirFn = func() string {
	return filepath.Join(boltedDirFn(), "state")
}

// containersPath returns the absolute path to containers.json.
func containersPath() string {
	return filepath.Join(stateDirFn(), state.ContainersFile)
}

// readContainers loads containers.json as a repo → containerID map.
// A missing file is treated as an empty map: the file doesn't exist
// until at least one `bolt dev` lands. Any other error (parse failure,
// permission denied) propagates so callers can surface it.
func readContainers() (map[string]string, error) {
	raw, err := state.ReadJSON[map[string]any](containersPath())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		// Tolerate non-string entries by skipping them rather than
		// failing wholesale — defensive against hand-edited files.
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out, nil
}

// writeContainers atomically replaces containers.json with the given
// map. Round-trips through map[string]any to match the schema the
// state package declares.
func writeContainers(m map[string]string) error {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return state.WriteJSON(containersPath(), out)
}

// recordContainer merges a single repo → id mapping into containers.json.
func recordContainer(repo, id string) error {
	m, err := readContainers()
	if err != nil {
		return err
	}
	m[repo] = id
	return writeContainers(m)
}

// forgetContainer drops repo from containers.json. No-op if absent.
func forgetContainer(repo string) error {
	m, err := readContainers()
	if err != nil {
		return err
	}
	if _, ok := m[repo]; !ok {
		return nil
	}
	delete(m, repo)
	return writeContainers(m)
}

// repoPath returns the canonical in-VM path for the named repo.
func repoPath(name string) string { return path.Join(vmMountpoint, name) }

// requireUnlockedBackend is the shared entry-gate every lifecycle
// command needs: it confirms Bolted is initialised, loads the
// config, builds the backend, and verifies the VM is running and the
// volume is mounted. On any failure it writes a friendly diagnostic
// to stderr and returns an *exitError with the right exit code so the
// caller can `return err` and let Execute translate it.
func requireUnlockedBackend(ctx context.Context, stderr io.Writer) (backend.Backend, *config.Config, error) {
	cfgPath := configPath()
	if _, err := statFn(cfgPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintln(stderr, "Bolted is not initialised. Run `bolt init` first.")
			return nil, nil, &exitError{code: exitLocked, err: errors.New("Bolted not initialised")}
		}
		return nil, nil, fmt.Errorf("stat config: %w", err)
	}
	cfg, err := loadConfigFn(cfgPath)
	if err != nil {
		return nil, nil, fmt.Errorf("load config: %w", err)
	}
	b, err := newBackendFn(backend.Config{Backend: cfg.Backend})
	if err != nil {
		return nil, nil, fmt.Errorf("backend init: %w", err)
	}
	running, err := b.IsRunning(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("check VM state: %w", err)
	}
	if !running {
		fmt.Fprintln(stderr, "Bolted VM is not running. Run `bolt unlock` first.")
		return nil, nil, &exitError{code: exitLocked, err: errors.New("VM not running")}
	}
	if isLocked(ctx, b) {
		fmt.Fprintln(stderr, "Bolted is locked. Run `bolt unlock` first.")
		return nil, nil, &exitError{code: exitLocked, err: errors.New("Bolted locked")}
	}
	return b, cfg, nil
}

// repoExists reports whether the named repo directory exists inside
// the VM mountpoint. Uses `test -d` so we never accidentally print
// directory contents.
func repoExists(ctx context.Context, b backend.Backend, repo string) (bool, error) {
	res, err := b.Exec(ctx, []string{"test", "-d", repoPath(repo)}, backend.ExecOpts{})
	if err != nil {
		return false, fmt.Errorf("probe repo %q: %w", repo, err)
	}
	return res.ExitCode == 0, nil
}

// requireRepo wraps repoExists with the canonical "exit 5" diagnostic
// so every lifecycle command surfaces the same error shape.
func requireRepo(ctx context.Context, b backend.Backend, stderr io.Writer, repo string) error {
	exists, err := repoExists(ctx, b, repo)
	if err != nil {
		return err
	}
	if !exists {
		fmt.Fprintf(stderr, "repo %q not found in Bolted.\n", repo)
		return &exitError{code: exitRepoNotFound, err: fmt.Errorf("repo not found: %s", repo)}
	}
	return nil
}

// storedContainerID returns the recorded container id for repo from
// containers.json, or "" if there is no record. This is the file we
// treat as the source of truth — if the user deleted the container
// out-of-band, the Exec downstream will fail with a clear error.
func storedContainerID(repo string) (string, error) {
	m, err := readContainers()
	if err != nil {
		return "", err
	}
	return m[repo], nil
}

// runningContainerIDs returns the set of currently-running Bolted
// container ids reported by `podman ps`. Used by `bolt ls` to decide
// per-repo status. Filters by name prefix to scope the query.
func runningContainerIDs(ctx context.Context, b backend.Backend) (map[string]bool, error) {
	res, err := b.Exec(ctx, []string{
		"podman", "ps",
		"--filter", "name=bolted-",
		"--format", "{{.ID}}",
	}, backend.ExecOpts{})
	if err != nil {
		return nil, fmt.Errorf("probe containers: %w", err)
	}
	if res.ExitCode != 0 {
		return map[string]bool{}, nil
	}
	out := map[string]bool{}
	cur := []byte{}
	flush := func() {
		// Trim leading and trailing whitespace so podman's column
		// padding and any leading tab don't end up in the key.
		for len(cur) > 0 && (cur[len(cur)-1] == ' ' || cur[len(cur)-1] == '\t' || cur[len(cur)-1] == '\r') {
			cur = cur[:len(cur)-1]
		}
		for len(cur) > 0 && (cur[0] == ' ' || cur[0] == '\t' || cur[0] == '\r') {
			cur = cur[1:]
		}
		if len(cur) > 0 {
			out[string(cur)] = true
		}
		cur = cur[:0]
	}
	for _, ch := range res.Stdout {
		if ch == '\n' {
			flush()
			continue
		}
		cur = append(cur, ch)
	}
	flush()
	return out, nil
}

// --- bolt dev -----------------------------------------------------------------

type devOptions struct {
	detach bool
	trust  bool
}

func newDevCmd() *cobra.Command {
	opts := &devOptions{}
	cmd := &cobra.Command{
		Use:   "dev <repo> [-- <cmd>...]",
		Short: "Start (or attach to) the dev container for <repo>",
		Long: "Brings up the dev container for <repo> on first use, then attaches " +
			"an interactive shell. Subsequent invocations attach a new shell to " +
			"the running container. Use `-- <cmd>` to run a one-shot command.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Split args at `--` so we can distinguish `bolt dev api` (shell)
			// from `bolt dev api -- npm test` (one-shot).
			repo := args[0]
			var rest []string
			if dash := cmd.ArgsLenAtDash(); dash >= 0 {
				rest = args[dash:]
			}
			return runDev(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), repo, rest, *opts)
		},
	}
	cmd.Flags().BoolVarP(&opts.detach, "detach", "d", false, "Start the container without attaching a shell")
	cmd.Flags().BoolVar(&opts.trust, "trust", false, "Auto-approve the repo's devcontainer.json without prompting (spec 18)")
	return cmd
}

func runDev(ctx context.Context, stdout, stderr io.Writer, repo string, command []string, opts devOptions) error {
	b, _, err := requireUnlockedBackend(ctx, stderr)
	if err != nil {
		return err
	}
	if err := requireRepo(ctx, b, stderr, repo); err != nil {
		return err
	}

	// Spec 18 — gate on devcontainer.json approval before any Up. The
	// gate is a no-op for repos without .devcontainer/devcontainer.json.
	if err := trustGateFn(ctx, b, repo, repoPath(repo), stdinFn(), stderr, opts.trust); err != nil {
		return err
	}

	runner := newRunnerFn(b, devcontainer.Options{})

	// Step 1: get or start the container. containers.json is the
	// authoritative record of "we asked devcontainer to bring this
	// repo up". A blank entry means we have never started it (or
	// `bolt stop` removed it).
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

	// Step 2a: explicit `-- <cmd>` — one-shot, exit with its code.
	if len(command) > 0 {
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

	// Step 2b: --detach — return now, leave the container running.
	if opts.detach {
		fmt.Fprintf(stderr, "container for %q is running (id=%s).\n", repo, containerID)
		return nil
	}

	// Step 2c: attach an interactive shell.
	shellPath := shellFromConfig(nil)
	res, err := runner.Exec(ctx, containerID, []string{shellPath}, devcontainer.ExecOpts{
		Cwd: repoPath(repo),
		TTY: true,
	})
	if len(res.Stdout) > 0 {
		_, _ = stdout.Write(res.Stdout)
	}
	if len(res.Stderr) > 0 {
		_, _ = stderr.Write(res.Stderr)
	}
	if err != nil {
		return fmt.Errorf("attach shell: %w", err)
	}
	if res.ExitCode != 0 {
		return &exitError{code: res.ExitCode, err: fmt.Errorf("shell exited %d", res.ExitCode)}
	}
	return nil
}

