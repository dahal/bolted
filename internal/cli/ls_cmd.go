// ls_cmd.go implements `bolt ls` and `bolt ls --json`. It lists every
// repo under /bolted/repos and joins that listing against
// containers.json + a live `podman ps` probe so users can see at a
// glance which repos have running dev containers. Size is reported
// via `du -sh` inside the VM.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/dahal/bolted/internal/backend"
)

type lsOptions struct {
	jsonOut bool
}

// repoEntry is the per-repo row reported by `bolt ls`. JSON tags are the
// machine-readable schema; the human renderer pulls the same fields.
type repoEntry struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "running" | "stopped"
	Size   string `json:"size"`   // human-readable ("420M", "1.2G")
}

func newLsCmd() *cobra.Command {
	opts := &lsOptions{}
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List repos with their container status and size",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLs(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), *opts)
		},
	}
	cmd.Flags().BoolVar(&opts.jsonOut, "json", false, "Emit machine-readable JSON instead of a table")
	return cmd
}

func runLs(ctx context.Context, stdout, stderr io.Writer, opts lsOptions) error {
	b, _, err := requireUnlockedBackend(ctx, stderr)
	if err != nil {
		return err
	}

	names, err := listRepoNames(ctx, b)
	if err != nil {
		return err
	}

	containers, err := readContainers()
	if err != nil {
		return err
	}
	running, err := runningContainerIDs(ctx, b)
	if err != nil {
		return err
	}

	entries := make([]repoEntry, 0, len(names))
	for _, name := range names {
		size, _ := repoSize(ctx, b, name)
		status := "stopped"
		if id, ok := containers[name]; ok && running[id] {
			status = "running"
		}
		entries = append(entries, repoEntry{
			Name:   name,
			Status: status,
			Size:   size,
		})
	}

	if opts.jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(entries); err != nil {
			return fmt.Errorf("encode JSON: %w", err)
		}
		return nil
	}

	renderLsTable(stdout, entries)
	return nil
}

// listRepoNames probes `ls /bolted/repos` and returns the sorted,
// deduplicated repo names. Empty stdout → empty slice; we never
// fabricate names.
func listRepoNames(ctx context.Context, b backend.Backend) ([]string, error) {
	res, err := b.Exec(ctx, []string{"ls", vmMountpoint}, backend.ExecOpts{})
	if err != nil {
		return nil, fmt.Errorf("list repos: %w", err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("list repos: exit %d", res.ExitCode)
	}
	seen := map[string]struct{}{}
	out := []string{}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// repoSize returns a human-readable size string for the named repo
// via `du -sh`. Failure is non-fatal — the caller treats "—" as the
// fallback so a single unreadable repo doesn't sink the whole table.
func repoSize(ctx context.Context, b backend.Backend, name string) (string, error) {
	res, err := b.Exec(ctx, []string{"du", "-sh", repoPath(name)}, backend.ExecOpts{})
	if err != nil || res.ExitCode != 0 {
		return "—", fmt.Errorf("du failed for %s", name)
	}
	// `du -sh` prints "<size>\t<path>\n"; we only want the size.
	out := strings.TrimSpace(string(res.Stdout))
	if out == "" {
		return "—", nil
	}
	if tab := strings.IndexAny(out, " \t"); tab != -1 {
		out = out[:tab]
	}
	return out, nil
}

// renderLsTable writes a tab-aligned NAME / STATUS / SIZE table. We
// match the layout sketched in spec 13 (without the PORTS column —
// spec 14 owns ports).
func renderLsTable(w io.Writer, entries []repoEntry) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSTATUS\tSIZE")
	for _, e := range entries {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", e.Name, e.Status, e.Size)
	}
	_ = tw.Flush()
}
