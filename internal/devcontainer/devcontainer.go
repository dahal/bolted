// Package devcontainer wraps the upstream `@devcontainers/cli` so the
// rest of the Bolted CLI can bring per-repo dev containers up and
// down without speaking the CLI's argv conventions directly. Every
// shell-out is routed through backend.Backend.Exec so it lands inside
// the Bolted VM where podman lives.
//
// # Container naming
//
// Containers are named `bolted-<repo>` where <repo> is the basename
// of the repo path. The naming is deliberately deterministic so that
// `podman logs bolted-<repo>` always works and so duplicate Up
// calls can be detected by simple name collision (returns
// ErrContainerExists).
//
// # Default devcontainer fallback
//
// If a repo lacks a `.devcontainer/devcontainer.json` we fall back to
// a Bolted-supplied default (Options.DefaultDevcontainerPath, see
// the constant defaultDevcontainerPath). The probe runs inside the VM
// — the host never has to mount the repo to make the decision.
//
// # CLI install
//
// `@devcontainers/cli` is not part of the VM base image. The first
// time Up runs we probe for it via `which devcontainer`; if it is
// missing we attempt `npm install -g @devcontainers/cli`. If npm is
// also missing the error wraps ErrDevcontainerMissing with a hint
// about `bolt provision`.
package devcontainer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/dahal/bolted/internal/backend"
)

// ErrContainerExists is returned by Up when a container with the
// expected `bolted-<repo>` name is already running. Callers should
// surface a friendly "already up — use bolt exec instead" message.
var ErrContainerExists = errors.New("devcontainer: container already exists")

// ErrDevcontainerMissing is returned by Up (and exported for callers
// to check via errors.Is) when the devcontainer CLI is not present in
// the VM and we could not install it (e.g. npm is also missing).
var ErrDevcontainerMissing = errors.New("devcontainer: @devcontainers/cli is not installed")

// defaultDevcontainerPath is the in-VM path to the Bolted-supplied
// default devcontainer.json used when a repo doesn't ship its own.
// Spec 15 will wire this through config; for now it is a constant
// overridable via Options.DefaultDevcontainerPath.
const defaultDevcontainerPath = "/bolted/default-devcontainer.json"

// dockerPathFlag is the single `--docker-path` argument we pass to
// every devcontainer invocation so the CLI talks to podman rather
// than docker. Held as a constant so tests can pin the wire format.
const dockerPathFlag = "--docker-path=podman"

// Options configures New. The zero value is valid; defaults are
// documented per field.
type Options struct {
	// DefaultDevcontainerPath overrides the in-VM path to the
	// fallback devcontainer.json used when a repo lacks one.
	// Empty falls back to defaultDevcontainerPath.
	DefaultDevcontainerPath string
}

// UpOpts controls Runner.Up. Additive: new fields land here without
// changing the Runner interface so callers that ignore a field stay
// source-compatible.
type UpOpts struct {
	// NetworkName, if non-empty, names a podman network the started
	// container will be connected to via a post-Up
	// `podman network connect` call (the devcontainer CLI has no
	// equivalent flag — see attachToNetwork). Empty means "do not
	// attach to any extra network", matching the historical zero-
	// value behaviour. Spec 19 introduced this field so dev containers
	// can join the shared `bolted-net` bridge for inter-repo DNS.
	NetworkName string
}

// ExecOpts controls Runner.Exec — forwarded directly into the
// devcontainer CLI's `exec` subcommand. Cwd is the container-side
// working directory; Env is appended to the container environment;
// TTY requests an interactive pseudo-tty.
type ExecOpts struct {
	// Cwd is the working directory inside the container. Empty
	// means "use the container's default".
	Cwd string
	// Env is a list of KEY=VALUE strings forwarded into the
	// container environment for the exec.
	Env []string
	// TTY requests an interactive pseudo-tty.
	TTY bool
}

// ExecResult mirrors backend.ExecResult but is re-exported so callers
// don't need to import the backend package just to inspect output.
type ExecResult struct {
	// Stdout captures the command's standard output.
	Stdout []byte
	// Stderr captures the command's standard error.
	Stderr []byte
	// ExitCode is the process exit status; -1 if the command failed
	// to start.
	ExitCode int
}

// Runner is the contract every devcontainer implementation satisfies.
// The concrete *runner in this package is the production type; tests
// may swap a fake.
type Runner interface {
	// Up starts the dev container for the given repo. Returns the
	// container id reported by the CLI. If a container with the
	// expected name already exists, returns ErrContainerExists.
	Up(ctx context.Context, repoPath string, opts UpOpts) (containerID string, err error)
	// Down stops and removes the named container.
	Down(ctx context.Context, containerID string) error
	// Exec runs cmd inside the named container.
	Exec(ctx context.Context, containerID string, cmd []string, opts ExecOpts) (ExecResult, error)
	// Build builds the image for the dev container without starting
	// it. Useful for warming caches.
	Build(ctx context.Context, repoPath string) error
}

// runner is the production implementation. It is private — callers
// receive a *runner from New() typed as the Runner interface for
// substitution in tests, but the concrete type is fine to use
// directly when needed (e.g. for the test-only fields).
type runner struct {
	// backend is the VM execution surface; every devcontainer
	// invocation routes through backend.Exec.
	backend backend.Backend
	// defaultPath is the in-VM path used when a repo lacks its
	// own .devcontainer/devcontainer.json.
	defaultPath string
	// installOnce gates the install probe so the cost is paid at
	// most once per runner instance.
	installOnce sync.Once
	// installErr captures whatever the probe / install attempt
	// produced so concurrent / repeated callers see the same
	// outcome.
	installErr error
}

// New returns a Runner ready to drive the devcontainer CLI inside the
// VM exposed by b. Options overrides defaults; the zero Options is
// valid.
func New(b backend.Backend, opts Options) *runner {
	r := &runner{backend: b, defaultPath: opts.DefaultDevcontainerPath}
	if r.defaultPath == "" {
		r.defaultPath = defaultDevcontainerPath
	}
	return r
}

// Up brings up the dev container for repoPath. Sequence:
//
//  1. Ensure the devcontainer CLI is installed (probe + maybe install).
//  2. Refuse to proceed if a container with our name is already running.
//  3. Pick between the repo's own devcontainer.json and the fallback.
//  4. Shell out to `devcontainer up …` and parse the container id.
//  5. If opts.NetworkName is set, attach the container to that podman
//     network — the devcontainer CLI has no first-class flag for this
//     so the connect happens out-of-band (see attachToNetwork).
func (r *runner) Up(ctx context.Context, repoPath string, opts UpOpts) (string, error) {
	if repoPath == "" {
		return "", errors.New("devcontainer: Up: repoPath is empty")
	}
	if err := r.ensureCLI(ctx); err != nil {
		return "", err
	}

	name := containerName(repoPath)
	exists, err := r.containerExists(ctx, name)
	if err != nil {
		return "", err
	}
	if exists {
		return "", fmt.Errorf("%w: %s", ErrContainerExists, name)
	}

	args := []string{
		"devcontainer", dockerPathFlag, "up",
		"--workspace-folder", repoPath,
		"--id-label", "bolted.name=" + name,
	}
	if !r.hasOwnDevcontainer(ctx, repoPath) {
		args = append(args, "--config", r.defaultPath)
	}

	res, err := r.backend.Exec(ctx, args, backend.ExecOpts{})
	if err != nil || res.ExitCode != 0 {
		return "", wrapExec("up", res, err)
	}
	id, err := parseContainerID(res.Stdout)
	if err != nil {
		return "", fmt.Errorf("devcontainer: up: %w", err)
	}
	if opts.NetworkName != "" {
		if err := attachToNetwork(ctx, r.backend, id, opts.NetworkName); err != nil {
			return "", err
		}
	}
	return id, nil
}

// attachToNetwork connects an already-running container to an existing
// podman network via `podman network connect <network> <id>`.
//
// Spec 19 needs every Bolted container to join `bolted-net` so
// that repo A can reach repo B at `http://bolted-<b>:<port>`. The
// devcontainer CLI's `up` subcommand does not expose a `--network`
// flag (it derives the network from devcontainer.json features and the
// CLI's own defaults), so the cleanest place to wire this is a
// post-Up podman call against the container id we just received.
//
// Exposed at package scope (not a method) so it can be unit-tested
// directly with a scripted backend without standing up a full Runner.
// Callers should ensure the named network exists first (see
// `internal/boltednet.Ensure`) — this helper does not create it.
func attachToNetwork(ctx context.Context, b backend.Backend, containerID, network string) error {
	if containerID == "" {
		return errors.New("devcontainer: attachToNetwork: containerID is empty")
	}
	if network == "" {
		return errors.New("devcontainer: attachToNetwork: network is empty")
	}
	res, err := b.Exec(ctx, []string{
		"podman", "network", "connect", network, containerID,
	}, backend.ExecOpts{})
	if err != nil || res.ExitCode != 0 {
		return wrapExec("network connect", res, err)
	}
	return nil
}

// Down stops and removes the named container via `podman rm -f`.
// The devcontainer CLI itself has no first-class teardown sub-command
// at the time of writing, so we go through podman directly — which is
// fine because the container was created with a predictable name.
func (r *runner) Down(ctx context.Context, containerID string) error {
	if containerID == "" {
		return errors.New("devcontainer: Down: containerID is empty")
	}
	res, err := r.backend.Exec(ctx, []string{
		"podman", "rm", "-f", containerID,
	}, backend.ExecOpts{})
	if err != nil || res.ExitCode != 0 {
		return wrapExec("down", res, err)
	}
	return nil
}

// Exec runs cmd inside an already-running container via the
// devcontainer CLI. The CLI handles attaching to the right namespace
// and applying the dev container's environment.
func (r *runner) Exec(ctx context.Context, containerID string, cmd []string, opts ExecOpts) (ExecResult, error) {
	if containerID == "" {
		return ExecResult{}, errors.New("devcontainer: Exec: containerID is empty")
	}
	if len(cmd) == 0 {
		return ExecResult{}, errors.New("devcontainer: Exec: cmd is empty")
	}

	args := []string{
		"devcontainer", dockerPathFlag, "exec",
		"--container-id", containerID,
	}
	if opts.Cwd != "" {
		args = append(args, "--workspace-folder", opts.Cwd)
	}
	for _, kv := range opts.Env {
		args = append(args, "--remote-env", kv)
	}
	args = append(args, cmd...)

	res, err := r.backend.Exec(ctx, args, backend.ExecOpts{TTY: opts.TTY})
	if err != nil {
		return ExecResult{
			Stdout:   res.Stdout,
			Stderr:   res.Stderr,
			ExitCode: res.ExitCode,
		}, wrapExec("exec", res, err)
	}
	return ExecResult{
		Stdout:   res.Stdout,
		Stderr:   res.Stderr,
		ExitCode: res.ExitCode,
	}, nil
}

// Build invokes `devcontainer build` for repoPath. Used to warm the
// image cache without actually starting a container (e.g. for CI or
// pre-flight checks).
func (r *runner) Build(ctx context.Context, repoPath string) error {
	if repoPath == "" {
		return errors.New("devcontainer: Build: repoPath is empty")
	}
	if err := r.ensureCLI(ctx); err != nil {
		return err
	}
	args := []string{
		"devcontainer", dockerPathFlag, "build",
		"--workspace-folder", repoPath,
	}
	if !r.hasOwnDevcontainer(ctx, repoPath) {
		args = append(args, "--config", r.defaultPath)
	}
	res, err := r.backend.Exec(ctx, args, backend.ExecOpts{})
	if err != nil || res.ExitCode != 0 {
		return wrapExec("build", res, err)
	}
	return nil
}

// containerExists asks podman whether a container with the given
// name is present. The `--filter name=<n> --format {{.Names}}` form
// works regardless of running/stopped state — we treat "exists at
// all" as a collision because re-Up'ing a stopped container is also
// a logic error (user should `bolt start` / `bolt dev` against the
// existing one).
func (r *runner) containerExists(ctx context.Context, name string) (bool, error) {
	res, err := r.backend.Exec(ctx, []string{
		"podman", "ps", "-a",
		"--filter", "name=^" + name + "$",
		"--format", "{{.Names}}",
	}, backend.ExecOpts{})
	if err != nil || res.ExitCode != 0 {
		return false, wrapExec("ps", res, err)
	}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		if strings.TrimSpace(line) == name {
			return true, nil
		}
	}
	return false, nil
}

// hasOwnDevcontainer probes whether the repo ships its own
// devcontainer.json. We use `test -f` (exit 0 = present, exit 1 =
// absent) inside the VM. Any backend-level error is treated as
// "absent" so we fall back to the default — a noisy fallback is
// better than failing Up because of a transient probe error.
func (r *runner) hasOwnDevcontainer(ctx context.Context, repoPath string) bool {
	path := filepath.Join(repoPath, ".devcontainer", "devcontainer.json")
	res, err := r.backend.Exec(ctx, []string{"test", "-f", path}, backend.ExecOpts{})
	if err != nil {
		return false
	}
	return res.ExitCode == 0
}

// parseContainerID extracts the container id from the JSON the
// devcontainer CLI prints on success. The CLI emits a single-line
// JSON object like {"outcome":"success","containerId":"abc…"}; we
// scan stdout line by line so trailing log noise doesn't break the
// parse.
func parseContainerID(stdout []byte) (string, error) {
	type envelope struct {
		Outcome     string `json:"outcome"`
		ContainerID string `json:"containerId"`
	}
	for _, line := range strings.Split(string(stdout), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var env envelope
		if err := jsonUnmarshal([]byte(line), &env); err != nil {
			continue
		}
		if env.ContainerID != "" {
			return env.ContainerID, nil
		}
	}
	return "", errors.New("no containerId in devcontainer output")
}

// jsonUnmarshal is the indirection point that lets tests force the
// "every line fails to parse" branch in parseContainerID — without it
// the only way to hit that branch is malformed-but-JSON-shaped input,
// which is awkward to construct.
var jsonUnmarshal = json.Unmarshal

// containerName returns the canonical container name for repoPath.
// Exposed at package scope (not a method) because the CLI surface
// in spec 13 also needs to compute it without holding a Runner.
func containerName(repoPath string) string {
	return "bolted-" + filepath.Base(repoPath)
}

// wrapExec folds an Exec failure (backend-level error or non-zero
// exit) into a single error tagged with the operation name. Mirrors
// internal/volume's helper of the same shape so error formatting is
// consistent across the codebase.
func wrapExec(opName string, res backend.ExecResult, err error) error {
	stderr := strings.TrimSpace(string(res.Stderr))
	switch {
	case err != nil && stderr != "":
		return fmt.Errorf("devcontainer: %s: %w: %s", opName, err, stderr)
	case err != nil:
		return fmt.Errorf("devcontainer: %s: %w", opName, err)
	case stderr != "":
		return fmt.Errorf("devcontainer: %s: exit %d: %s", opName, res.ExitCode, stderr)
	default:
		return fmt.Errorf("devcontainer: %s: exit %d", opName, res.ExitCode)
	}
}
