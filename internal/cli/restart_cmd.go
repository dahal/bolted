// restart_cmd.go implements `bolt restart`. It cycles the VM
// (StopVM → StartVM) and is the command users run after editing
// `vm.memory` / `vm.cpus` in config.yaml — the backend treats those
// fields as boot-time settings.
//
// Before stopping the VM we lock Bolted if it is currently
// unlocked. That mirrors what `bolt lock` does (unmount volume, evict
// the LUKS key) so the encrypted volume isn't left in a half-open
// state when the VM goes away. The lock is best-effort: if it fails
// (e.g. because there are stale mounts the volume package can't tear
// down cleanly), we surface a stderr warning but still attempt the
// restart, since the alternative — refusing to cycle the VM — is the
// worse user experience.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/spf13/cobra"

	"github.com/dahal/bolted/internal/backend"
)

func newRestartCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restart",
		Short: "Stop and start the Bolted VM (apply vm.memory / vm.cpus changes)",
		Long: "Cycles the underlying VM. The Bolted is locked first if " +
			"currently unlocked so the encrypted volume is unmounted cleanly. " +
			"Useful after editing vm.memory or vm.cpus in config.yaml.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runRestart(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	return cmd
}

func runRestart(ctx context.Context, stdout, stderr io.Writer) error {
	// Need at least a config + backend to know what VM to talk to.
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

	running, err := b.IsRunning(ctx)
	if err != nil {
		return fmt.Errorf("check VM state: %w", err)
	}
	if !running {
		// Nothing to stop; just start the VM and return.
		if err := b.StartVM(ctx); err != nil {
			return fmt.Errorf("start VM: %w", err)
		}
		fmt.Fprintln(stderr, "Bolted VM started.")
		return nil
	}

	// If Bolted is unlocked, lock it first so the encrypted
	// volume is unmounted cleanly. Reuse runLock — it owns the right
	// sequencing for unmount + LUKS close.
	if !isLocked(ctx, b) {
		if err := runLock(ctx, stdout, stderr, lockOptions{stopVM: false}); err != nil {
			// Best-effort: surface the warning but continue. If the
			// volume can't unmount we're going to lose state on the
			// next boot regardless; bailing here doesn't help the
			// user recover.
			fmt.Fprintf(stderr, "warning: pre-restart lock failed: %v\n", err)
		}
	}

	if err := b.StopVM(ctx); err != nil {
		return fmt.Errorf("stop VM: %w", err)
	}
	if err := b.StartVM(ctx); err != nil {
		return fmt.Errorf("start VM: %w", err)
	}
	fmt.Fprintln(stderr, "Bolted VM restarted. Run `bolt unlock` to remount the volume.")
	return nil
}
