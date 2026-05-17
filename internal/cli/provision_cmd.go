package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/dahal/bolted/internal/backend"
	"github.com/dahal/bolted/internal/provision"
)

// Indirection points so tests can substitute the provision-package
// drivers without spinning up a real VM or hitting the network. Each
// is a thin wrapper around the corresponding provision.* function so
// the production wiring is one-liner-trivial.
var (
	provisionLoadFn = func(path string) (*provision.BoltedProfile, error) {
		return provision.Load(path)
	}
	provisionLoadCacheFn = func(stateDir string) (*provision.Cache, error) {
		return provision.LoadCache(stateDir)
	}
	provisionSaveCacheFn = func(stateDir string, c *provision.Cache) error {
		return provision.SaveCache(stateDir, c)
	}
	provisionApplyFn = func(ctx context.Context, b backend.Backend, p *provision.BoltedProfile, c *provision.Cache, baseDir string, stdout io.Writer) (*provision.Result, error) {
		return provision.Apply(ctx, b, p, c, baseDir, stdout)
	}
	provisionCheckFn = func(b backend.Backend, p *provision.BoltedProfile, c *provision.Cache) (bool, string, error) {
		return provision.Check(b, p, c)
	}
	provisionFetchFn = func(urlOrPath string) ([]byte, error) {
		return provision.FetchYAML(urlOrPath)
	}
	provisionWriteFileFn = func(path string, data []byte, perm fs.FileMode) error {
		return os.WriteFile(path, data, perm)
	}
	provisionMkdirAllFn = func(path string, perm fs.FileMode) error {
		return os.MkdirAll(path, perm)
	}
)

// --- bolt provision -----------------------------------------------------------

type provisionOptions struct {
	check   bool
	fromURL string
}

// boltedYAMLPath is the conventional location of bolted.yaml:
// `~/.bolted/bolted.yaml`. Documented in brainstorm 07; no
// config-yaml entry is needed — the convention is the source of truth.
func boltedYAMLPath() string {
	return filepath.Join(boltedDirFn(), "bolted.yaml")
}

// stateDirPath returns `~/.bolted/state`, the directory state.WriteJSON
// writes provisioned.json into.
func stateDirPath() string {
	return filepath.Join(boltedDirFn(), "state")
}

func newProvisionCmd() *cobra.Command {
	opts := &provisionOptions{}
	cmd := &cobra.Command{
		Use:   "provision",
		Short: "Apply bolted.yaml (install features, packages, gitconfig, dotfiles)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runProvision(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), *opts)
		},
	}
	cmd.Flags().BoolVar(&opts.check, "check", false, "Check for drift between bolted.yaml and the installed state; exit non-zero on drift")
	cmd.Flags().StringVar(&opts.fromURL, "from", "", "Fetch bolted.yaml from a URL (https://) or local path before provisioning")
	return cmd
}

func runProvision(ctx context.Context, stdout, stderr io.Writer, opts provisionOptions) error {
	// "Never initialised" check, matching status / shell.
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

	// Refuse if the Bolted is locked. Same pattern as shell — the
	// VM must be running AND the volume mounted; both are prerequisites
	// for any `apk` / `chsh` / dotfile-copy call to land somewhere real.
	running, err := b.IsRunning(ctx)
	if err != nil {
		return fmt.Errorf("check VM state: %w", err)
	}
	if !running {
		fmt.Fprintln(stderr, "Bolted VM is not running. Run `bolt unlock` first.")
		return &exitError{code: exitLocked, err: errors.New("VM not running")}
	}
	if isLocked(ctx, b) {
		fmt.Fprintln(stderr, "Bolted is locked. Run `bolt unlock` first.")
		return &exitError{code: exitLocked, err: errors.New("Bolted locked")}
	}

	// --from: fetch and persist before doing anything else.
	if opts.fromURL != "" {
		data, err := provisionFetchFn(opts.fromURL)
		if err != nil {
			return fmt.Errorf("fetch bolted.yaml: %w", err)
		}
		if err := provisionMkdirAllFn(boltedDirFn(), 0o700); err != nil {
			return fmt.Errorf("mkdir BoltedDir: %w", err)
		}
		if err := provisionWriteFileFn(boltedYAMLPath(), data, 0o600); err != nil {
			return fmt.Errorf("write bolted.yaml: %w", err)
		}
	}

	yamlPath := boltedYAMLPath()
	profile, err := provisionLoadFn(yamlPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintln(stderr, "no bolted.yaml found. Create one at "+yamlPath+" or run with --from <url|path>.")
			return &exitError{code: exitGeneric, err: errors.New("no bolted.yaml")}
		}
		return fmt.Errorf("load bolted.yaml: %w", err)
	}

	cache, err := provisionLoadCacheFn(stateDirPath())
	if err != nil {
		return fmt.Errorf("load cache: %w", err)
	}

	if opts.check {
		drifted, summary, err := provisionCheckFn(b, profile, cache)
		if err != nil {
			return fmt.Errorf("check drift: %w", err)
		}
		if !drifted {
			fmt.Fprintln(stdout, "in sync")
			return nil
		}
		fmt.Fprintln(stdout, "drift: "+summary)
		return &exitError{code: exitGeneric, err: errors.New("drift detected")}
	}

	baseDir := filepath.Dir(yamlPath)
	res, err := provisionApplyFn(ctx, b, profile, cache, baseDir, stdout)
	if err != nil {
		// Best-effort cache save so partial progress is recorded.
		_ = provisionSaveCacheFn(stateDirPath(), cache)
		return fmt.Errorf("apply: %w", err)
	}
	if err := provisionSaveCacheFn(stateDirPath(), cache); err != nil {
		return fmt.Errorf("save cache: %w", err)
	}
	renderProvisionSummary(stdout, res)
	return nil
}

// renderProvisionSummary writes a compact one-block summary of the
// apply result. Mirrors the style of renderStatusHuman.
func renderProvisionSummary(w io.Writer, r *provision.Result) {
	fmt.Fprintln(w, "---")
	fmt.Fprintf(w, "features:  +%d -%d\n", len(r.FeaturesAdded), len(r.FeaturesRemoved))
	fmt.Fprintf(w, "packages:  +%d -%d\n", len(r.PackagesAdded), len(r.PackagesRemoved))
	fmt.Fprintf(w, "gitconfig: %d keys\n", r.GitConfigApplied)
	if r.ShellSet {
		fmt.Fprintln(w, "shell:     changed")
	}
	fmt.Fprintf(w, "dotfiles:  %d changed (%d overwritten)\n", len(r.DotfilesChanged), len(r.DotfilesOverwritten))
	fmt.Fprintf(w, "elapsed:   %s\n", r.Duration)
}
