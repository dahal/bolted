package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dahal/bolted/internal/backend"
)

// statFn lets tests stub the config-file existence check used to detect
// "never initialized". Production wires it to os.Stat.
var statFn = os.Stat

// --- bolt status --------------------------------------------------------------

type statusOptions struct {
	jsonOut bool
}

// statusReport is the structured form of `bolt status`. It is also the JSON
// payload printed when --json is passed.
type statusReport struct {
	Initialized bool          `json:"initialized"`
	Locked      bool          `json:"locked"`
	VM          vmStatus      `json:"vm"`
	Repos       *reposStatus  `json:"repos,omitempty"`
	Containers  string        `json:"containers"`
}

type vmStatus struct {
	State       string `json:"state"`                  // "running" or "stopped"
	CPUs        int    `json:"cpus"`
	Memory      string `json:"memory"`
	Disk        string `json:"disk"`
	RSSMegabyte int    `json:"rss_mb,omitempty"`       // best-effort, 0 when unknown
}

type reposStatus struct {
	Count int      `json:"count"`
	Names []string `json:"names"`
}

func newStatusCmd() *cobra.Command {
	opts := &statusOptions{}
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show Bolted status (VM state, lock state, repos, containers)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runStatus(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), *opts)
		},
	}
	cmd.Flags().BoolVar(&opts.jsonOut, "json", false, "Emit machine-readable JSON instead of the human-readable summary")
	return cmd
}

func runStatus(ctx context.Context, stdout io.Writer, stderr io.Writer, opts statusOptions) error {
	// 1. Detect "never initialized" by probing for the config file. Load
	// itself happily returns defaults for a missing file, so it cannot
	// distinguish the two states.
	cfgPath := configPath()
	if _, err := statFn(cfgPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintln(stderr, "Bolted is not initialised. Run `bolt init` first.")
			return &exitError{code: exitLocked, err: errors.New("Bolted not initialised")}
		}
		return fmt.Errorf("stat config: %w", err)
	}

	cfg, err := loadConfigFn(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	b, err := newBackendFn(backend.Config{Backend: cfg.Backend})
	if err != nil {
		return fmt.Errorf("backend init: %w", err)
	}

	report := statusReport{
		Initialized: true,
		Containers:  "—",
		VM: vmStatus{
			CPUs:   cfg.VM.CPUs,
			Memory: cfg.VM.Memory,
			Disk:   cfg.VM.Disk,
			State:  "stopped",
		},
	}

	running, err := b.IsRunning(ctx)
	if err != nil {
		return fmt.Errorf("check VM state: %w", err)
	}
	if running {
		report.VM.State = "running"
	}

	// Lock state: if VM isn't running, definitionally locked. Otherwise
	// probe `ls /bolted/repos` — non-zero exit means the mount isn't
	// there (i.e. the volume hasn't been unlocked).
	report.Locked = true
	if running {
		res, err := b.Exec(ctx, []string{"ls", vmMountpoint}, backend.ExecOpts{})
		if err == nil && res.ExitCode == 0 {
			report.Locked = false
			// Parse repo names from stdout (one entry per line — `ls`
			// without -l, no colour). Empty stdout = no repos.
			report.Repos = parseRepoListing(res.Stdout)
		}
		// Best-effort RSS via `free -m`.
		if rss, ok := readVMRSS(ctx, b); ok {
			report.VM.RSSMegabyte = rss
		}
	}

	if opts.jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return fmt.Errorf("encode JSON: %w", err)
		}
		return nil
	}

	renderStatusHuman(stdout, report)

	// If locked, also nudge the user. Don't fail — `bolt status` should be
	// safe to call any time. The spec's "exit 2 with helpful message"
	// applies to the unrecoverable case (never-initialized).
	if report.Locked && running {
		fmt.Fprintln(stderr, "Bolted is locked. Run `bolt unlock` to access repos.")
	}
	return nil
}

// parseRepoListing turns `ls`-style stdout into a sorted, deduplicated
// reposStatus. Empty stdout (or pure whitespace) yields a zero-count entry.
func parseRepoListing(stdout []byte) *reposStatus {
	seen := map[string]struct{}{}
	names := []string{}
	for _, line := range strings.Split(string(stdout), "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	sort.Strings(names)
	return &reposStatus{Count: len(names), Names: names}
}

// readVMRSS runs `free -m` inside the VM and returns the "used" memory in
// MiB. Best-effort — returns ok=false on any parse / exec failure so the
// caller can simply omit the field.
func readVMRSS(ctx context.Context, b backend.Backend) (int, bool) {
	res, err := b.Exec(ctx, []string{"free", "-m"}, backend.ExecOpts{})
	if err != nil || res.ExitCode != 0 {
		return 0, false
	}
	// `free -m` layout (header + Mem: + Swap:):
	//                total        used        free      shared ...
	// Mem:            7891        1234        ...
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || !strings.HasPrefix(fields[0], "Mem:") {
			continue
		}
		used, err := strconv.Atoi(fields[2])
		if err != nil {
			return 0, false
		}
		return used, true
	}
	return 0, false
}

// renderStatusHuman writes the human-readable status summary to w.
func renderStatusHuman(w io.Writer, r statusReport) {
	lockState := "unlocked"
	if r.Locked {
		lockState = "locked"
	}
	fmt.Fprintf(w, "Lock:       %s\n", lockState)
	fmt.Fprintf(w, "VM:         %s (cpus=%d memory=%s disk=%s)\n",
		r.VM.State, r.VM.CPUs, r.VM.Memory, r.VM.Disk)
	if r.VM.RSSMegabyte > 0 {
		fmt.Fprintf(w, "VM RSS:     %d MiB\n", r.VM.RSSMegabyte)
	}
	if r.Repos != nil {
		fmt.Fprintf(w, "Repos:      %d\n", r.Repos.Count)
		for _, name := range r.Repos.Names {
			fmt.Fprintf(w, "  - %s\n", name)
		}
	} else {
		fmt.Fprintf(w, "Repos:      (unavailable — Bolted locked)\n")
	}
	fmt.Fprintf(w, "Containers: %s\n", r.Containers)
}
