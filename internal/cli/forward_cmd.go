// forward_cmd.go implements `bolt forward <repo> <port> [--to <host-port>]`
// and its inverse `bolt unforward <repo> <port>`. Both commands operate
// on the same ports.json file that `internal/portforward` writes when
// it auto-detects listeners — the on-disk shape here mirrors that
// package's persistedEntry so the records `bolt ports` renders stay
// consistent regardless of who created them.
//
// Persistence is handled in this file (rather than reusing the
// internal portforward.Manager.persist helper, which is unexported) so
// the forward / unforward path stays self-contained. The Allocate
// (auto host-port) path still delegates to portforward.Manager so the
// allocation-window probe stays in one place.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/dahal/bolted/internal/backend"
	"github.com/dahal/bolted/internal/portforward"
	"github.com/dahal/bolted/internal/state"
)

// allocator is the slice of portforward.Manager the explicit-forward
// path actually needs. Keeping it tiny lets tests substitute a fake
// without depending on the full Manager surface.
type allocator interface {
	Allocate(ctx context.Context, repo string, containerPort int) (int, bool, error)
}

// newAllocatorFn is the indirection point that lets tests swap a fake
// allocator in for the real *portforward.Manager. Production wires the
// real constructor with the Bolted state dir.
var newAllocatorFn = func(b backend.Backend, stateDir string) allocator {
	return portforward.New(b, stateDir)
}

// --- on-disk shape ---------------------------------------------------------

// portsRecord mirrors portforward.persistedEntry. We re-declare it here
// because the canonical type is unexported; the on-disk JSON tags are
// the contract every reader/writer must agree on.
type portsRecord struct {
	HostPort      int    `json:"host_port"`
	ContainerPort int    `json:"container_port"`
	Process       string `json:"process"`
}

// portsStore is the in-memory shape of ports.json: repo → list of
// records. Matches the layout written by internal/portforward so the
// CLI commands here interoperate with `bolt ports`.
type portsStore map[string][]portsRecord

// portsPath returns the absolute path to ports.json under the shared
// state directory.
func portsFilePath() string {
	return filepath.Join(stateDirFn(), state.PortsFile)
}

// readPortsStore loads ports.json. A missing file returns an empty
// store and no error — the file simply hasn't been created yet, which
// is fine on a fresh Bolted install.
func readPortsStore() (portsStore, error) {
	raw, err := state.ReadJSON[map[string]json.RawMessage](portsFilePath())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return portsStore{}, nil
		}
		return nil, err
	}
	out := make(portsStore, len(raw))
	for repo, payload := range raw {
		var entries []portsRecord
		if err := json.Unmarshal(payload, &entries); err != nil {
			return nil, fmt.Errorf("parse repo %q in ports.json: %w", repo, err)
		}
		out[repo] = entries
	}
	return out, nil
}

// writePortsStore atomically replaces ports.json. Round-trips through
// map[string]any to match the schema state.WriteJSON expects.
func writePortsStore(store portsStore) error {
	payload := make(map[string]any, len(store))
	for k, v := range store {
		payload[k] = v
	}
	return state.WriteJSON(portsFilePath(), payload)
}

// upsertForward inserts (or replaces) a record under repo. Replacement
// keys on HostPort so re-forwarding the same host port doesn't yield
// a duplicate row in `bolt ports`.
func upsertForward(store portsStore, repo string, rec portsRecord) {
	entries := store[repo]
	for i, e := range entries {
		if e.HostPort == rec.HostPort {
			entries[i] = rec
			store[repo] = entries
			return
		}
	}
	store[repo] = append(entries, rec)
}

// removeForward drops the entry for repo whose ContainerPort matches.
// Returns the host port we removed (so the caller can call
// UnforwardPort) and ok=false if no such entry existed.
func removeForward(store portsStore, repo string, containerPort int) (int, bool) {
	entries, ok := store[repo]
	if !ok {
		return 0, false
	}
	for i, e := range entries {
		if e.ContainerPort == containerPort {
			hostPort := e.HostPort
			store[repo] = append(entries[:i], entries[i+1:]...)
			if len(store[repo]) == 0 {
				delete(store, repo)
			}
			return hostPort, true
		}
	}
	return 0, false
}

// --- bolt forward ------------------------------------------------------------

type forwardOptions struct {
	to int
}

func newForwardCmd() *cobra.Command {
	opts := &forwardOptions{}
	cmd := &cobra.Command{
		Use:   "forward <repo> <container-port>",
		Short: "Forward a container port to the host (overrides auto-allocation)",
		Long: "Installs an explicit port forward for the named repo's dev container. " +
			"By default the host port matches the container port (with auto-bump on collision); " +
			"--to pins an exact host port and errors out if that port is busy.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			port, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("forward: invalid port %q: %w", args[1], err)
			}
			return runForward(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], port, *opts)
		},
	}
	cmd.Flags().IntVar(&opts.to, "to", 0, "Pin the host port to use (default: match container port with auto-bump on conflict)")
	return cmd
}

func runForward(ctx context.Context, _, stderr io.Writer, repo string, containerPort int, opts forwardOptions) error {
	b, _, err := requireUnlockedBackend(ctx, stderr)
	if err != nil {
		return err
	}
	if err := requireRepo(ctx, b, stderr, repo); err != nil {
		return err
	}

	var hostPort int
	if opts.to > 0 {
		// Explicit host port: do not auto-bump on conflict; the user
		// asked for this exact port and we should surface the failure.
		if err := b.ForwardPort(ctx, containerPort, opts.to); err != nil {
			return fmt.Errorf("forward %d -> %d: %w", containerPort, opts.to, err)
		}
		hostPort = opts.to
	} else {
		alloc := newAllocatorFn(b, stateDirFn())
		got, _, err := alloc.Allocate(ctx, repo, containerPort)
		if err != nil {
			return fmt.Errorf("allocate host port: %w", err)
		}
		hostPort = got
	}

	store, err := readPortsStore()
	if err != nil {
		return fmt.Errorf("read ports: %w", err)
	}
	upsertForward(store, repo, portsRecord{
		HostPort:      hostPort,
		ContainerPort: containerPort,
		// Process is unknown at explicit-forward time; `bolt ports`
		// renders the blank cell as an empty string.
	})
	if err := writePortsStore(store); err != nil {
		return fmt.Errorf("persist ports: %w", err)
	}
	fmt.Fprintf(stderr, "forwarded %s container:%d -> host:%d\n", repo, containerPort, hostPort)
	return nil
}

// --- bolt unforward ----------------------------------------------------------

func newUnforwardCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unforward <repo> <container-port>",
		Short: "Drop a previously established port forward for <repo>",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			port, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("unforward: invalid port %q: %w", args[1], err)
			}
			return runUnforward(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], port)
		},
	}
	return cmd
}

func runUnforward(ctx context.Context, _, stderr io.Writer, repo string, containerPort int) error {
	b, _, err := requireUnlockedBackend(ctx, stderr)
	if err != nil {
		return err
	}
	store, err := readPortsStore()
	if err != nil {
		return fmt.Errorf("read ports: %w", err)
	}
	hostPort, ok := removeForward(store, repo, containerPort)
	if !ok {
		fmt.Fprintf(stderr, "no forward for %s container:%d.\n", repo, containerPort)
		return &exitError{code: exitGeneric, err: fmt.Errorf("no forward for %s:%d", repo, containerPort)}
	}
	if err := b.UnforwardPort(ctx, hostPort); err != nil {
		return fmt.Errorf("unforward host:%d: %w", hostPort, err)
	}
	if err := writePortsStore(store); err != nil {
		return fmt.Errorf("persist ports: %w", err)
	}
	fmt.Fprintf(stderr, "removed forward %s container:%d -> host:%d\n", repo, containerPort, hostPort)
	return nil
}
