// ports_cmd.go implements `bolt ports` and `bolt ports --json`. It reads
// the persisted host → container mappings out of ports.json and renders
// them. The forward lifecycle itself (detect, allocate, persist, tear
// down) lives in internal/portforward; this file is purely the
// presentation layer plus the gating that every lifecycle command
// shares (requireUnlockedBackend + stateDirFn from dev_cmd.go).
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/dahal/bolted/internal/backend"
	"github.com/dahal/bolted/internal/portforward"
)

// newManagerFn is the indirection point that lets tests swap a fake
// portforward.Manager for the real one. Production calls the real
// constructor with the backend and the shared state directory.
var newManagerFn = func(b backend.Backend, stateDir string) portsLister {
	return portforward.New(b, stateDir)
}

// portsLister is the slice of *portforward.Manager `bolt ports` actually
// uses. Accepting an interface here means the test can implement just
// List() instead of reaching for the full Manager API.
type portsLister interface {
	List() (map[string][]portforward.Mapping, error)
}

type portsOptions struct {
	jsonOut bool
}

// portsEntry is the per-mapping row reported by `bolt ports`. JSON tags
// are the machine-readable schema; the human renderer pulls the same
// fields. Mirrors the shape spec 14 § bolt ports specifies.
type portsEntry struct {
	Repo          string `json:"repo"`
	HostPort      int    `json:"host_port"`
	ContainerPort int    `json:"container_port"`
	Process       string `json:"process"`
}

func newPortsCmd() *cobra.Command {
	opts := &portsOptions{}
	cmd := &cobra.Command{
		Use:   "ports",
		Short: "List active container → host port forwards",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPorts(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), *opts)
		},
	}
	cmd.Flags().BoolVar(&opts.jsonOut, "json", false, "Emit machine-readable JSON instead of a table")
	return cmd
}

func runPorts(ctx context.Context, stdout, stderr io.Writer, opts portsOptions) error {
	b, _, err := requireUnlockedBackend(ctx, stderr)
	if err != nil {
		return err
	}
	mgr := newManagerFn(b, stateDirFn())
	store, err := mgr.List()
	if err != nil {
		return fmt.Errorf("read ports: %w", err)
	}
	entries := flattenPorts(store)
	if opts.jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(entries); err != nil {
			return fmt.Errorf("encode JSON: %w", err)
		}
		return nil
	}
	renderPortsTable(stdout, entries)
	return nil
}

// flattenPorts turns the nested map into a flat, deterministic slice
// (repo asc, then host port asc) so the table output is stable.
func flattenPorts(store map[string][]portforward.Mapping) []portsEntry {
	out := []portsEntry{}
	repos := make([]string, 0, len(store))
	for k := range store {
		repos = append(repos, k)
	}
	sort.Strings(repos)
	for _, repo := range repos {
		mappings := append([]portforward.Mapping(nil), store[repo]...)
		sort.Slice(mappings, func(i, j int) bool {
			return mappings[i].HostPort < mappings[j].HostPort
		})
		for _, m := range mappings {
			out = append(out, portsEntry{
				Repo:          repo,
				HostPort:      m.HostPort,
				ContainerPort: m.ContainerPort,
				Process:       m.Process,
			})
		}
	}
	return out
}

// renderPortsTable writes the human-readable table.
// Layout from spec 14: REPO / HOST PORT / CONTAINER PORT / PROCESS.
func renderPortsTable(w io.Writer, entries []portsEntry) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "REPO\tHOST PORT\tCONTAINER PORT\tPROCESS")
	for _, e := range entries {
		fmt.Fprintf(tw, "%s\t%d\t%d\t%s\n", e.Repo, e.HostPort, e.ContainerPort, e.Process)
	}
	_ = tw.Flush()
}
