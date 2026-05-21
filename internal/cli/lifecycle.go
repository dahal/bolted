package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/dahal/bolted/internal/backend"
	"github.com/dahal/bolted/internal/backend/factory"
	"github.com/dahal/bolted/internal/config"
	"github.com/dahal/bolted/internal/hostinfo"
	"github.com/dahal/bolted/internal/profiles"
	"github.com/dahal/bolted/internal/volume"
)

// Exit codes used by the lifecycle commands. Match the values documented
// in brainstorm/04-cli-design.md § Exit codes.
const (
	exitOK            = 0
	exitGeneric       = 1
	exitLocked        = 2
	exitBadPassword   = 3
	exitVMNotRunning  = 4 // unused at this layer, kept for completeness
	exitRepoNotFound  = 5 // unused at this layer, kept for completeness
)

// volumeOps is the slice of *volume.Volume the lifecycle commands actually
// use. Defining it here (and not in package volume) keeps the accept-
// interfaces / return-concrete pattern intact and lets tests inject a
// stub without exporting more from internal/volume.
type volumeOps interface {
	Create(ctx context.Context, imagePath string, sizeBytes int64, password []byte) error
	Open(ctx context.Context, imagePath string, password []byte) (volume.Device, error)
	Mount(ctx context.Context, dev volume.Device, mountpoint string) error
	Unmount(ctx context.Context, mountpoint string) error
	Close(ctx context.Context, dev volume.Device) error
}

// Defaults wired to real implementations. Tests override via the
// withLifecycleDeps helper.
var (
	newBackendFn = func(cfg backend.Config) (backend.Backend, error) {
		return factory.New(cfg)
	}
	newVolumeFn = func(b backend.Backend, opts volume.Options) volumeOps {
		return volume.New(b, opts)
	}
	detectDefaultsFn = hostinfo.DetectDefaults
	loadConfigFn     = config.Load
	saveConfigFn     = func(path string, c *config.Config) error { return c.Save(path) }
	boltedDirFn   = config.BoltedDir
	newPrompterFn    = func() passwordPrompter { return newTTYPrompter() }

	// profilesGetFn / profileWriteFileFn are the spec-16 init wiring.
	// Tests substitute them to drive the --profile branches without
	// touching the real embedded fs or the disk.
	profilesGetFn       = profiles.Get
	profilesNamesFn     = profiles.Names
	profileWriteFileFn  = os.WriteFile
)

// Paths inside the VM. Kept const so init/unlock/lock all agree.
const (
	// vmVolumeImagePath is the LUKS image file inside the VM. The host
	// presents the sparse file as a block-backed file the VM reads
	// directly via cryptsetup (cryptsetup attaches a loop device).
	vmVolumeImagePath = "/bolted/volume.img"
	vmMountpoint      = "/bolted/repos"
	vmMapperName      = "bolted"
)

// configPath returns ~/.bolted/config.yaml using the BoltedDir helper.
func configPath() string { return filepath.Join(boltedDirFn(), "config.yaml") }

// memoryMBFromString converts a config memory string like "8GB" to
// megabytes for backend.VMSpec.MemoryMB.
func memoryMBFromString(memory string) (int, error) {
	bytes, err := config.ParseSize(memory)
	if err != nil {
		return 0, err
	}
	return int(bytes / (1024 * 1024)), nil
}

// diskGBFromString converts a config disk string like "50GB" to gigabytes.
func diskGBFromString(disk string) (int, error) {
	bytes, err := config.ParseSize(disk)
	if err != nil {
		return 0, err
	}
	return int(bytes / (1024 * 1024 * 1024)), nil
}

// vmSpecFromConfig builds a backend.VMSpec from a config.VMConfig.
func vmSpecFromConfig(vm config.VMConfig) (backend.VMSpec, error) {
	memMB, err := memoryMBFromString(vm.Memory)
	if err != nil {
		return backend.VMSpec{}, fmt.Errorf("memory %q: %w", vm.Memory, err)
	}
	diskGB, err := diskGBFromString(vm.Disk)
	if err != nil {
		return backend.VMSpec{}, fmt.Errorf("disk %q: %w", vm.Disk, err)
	}
	return backend.VMSpec{
		CPUs:     vm.CPUs,
		MemoryMB: memMB,
		DiskGB:   diskGB,
	}, nil
}

// --- bolt init ----------------------------------------------------------------

type initOptions struct {
	profile        string
	fromURL        string
	passwordStdin  bool
	insecurePassword bool
}

func newInitCmd() *cobra.Command {
	opts := &initOptions{}
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialise Bolted (creates encrypted volume, sized VM)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInit(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), *opts)
		},
	}
	cmd.Flags().StringVar(&opts.profile, "profile", "", "Starter profile to drop into ~/.bolted/bolted.yaml (see `bolt profiles`)")
	cmd.Flags().StringVar(&opts.fromURL, "from", "", "Fetch bolted.yaml from URL or path (spec 15 — not yet implemented)")
	cmd.Flags().BoolVar(&opts.passwordStdin, "password-stdin", false, "Read the new password from stdin instead of prompting")
	cmd.Flags().BoolVar(&opts.insecurePassword, "insecure-password", false, "Skip the password-strength warning (spec 17)")
	return cmd
}

func runInit(ctx context.Context, _ io.Writer, stderr io.Writer, opts initOptions) error {
	// 1. Detect host hardware → sized defaults.
	vm, err := detectDefaultsFn()
	if err != nil {
		return fmt.Errorf("detect host hardware: %w", err)
	}

	cfg := config.NewDefault()
	cfg.VM = vm

	// 2. Build the backend and run its preflight before any irreversible
	// or sensitive work. The password prompt is the most security-
	// sensitive input in the tool — we don't want to ask for it only to
	// discover the backend can't run. The same backend instance is
	// reused below for EnsureVM/StartVM.
	b, err := newBackendFn(backend.Config{Backend: cfg.Backend})
	if err != nil {
		return fmt.Errorf("backend init: %w", err)
	}
	if err := b.Preflight(ctx); err != nil {
		return fmt.Errorf("preflight: %w", err)
	}

	// 3. Read new password (twice with confirm if interactive; once from
	// stdin if --password-stdin).
	prompter := newPrompterFn()
	password, err := readNewPassword(prompter, opts.passwordStdin)
	if err != nil {
		return err
	}
	defer zero(password)

	// Future: spec 17 will gate `opts.insecurePassword` on a real strength
	// check. For now we accept everything; flag is reserved.
	_ = opts.insecurePassword

	// --profile: validate up-front so a typo fails before we spin up
	// the VM. The actual write happens after saveConfig, which creates
	// the BoltedDir as a side effect of writing config.yaml.
	var profileYAML []byte
	if opts.profile != "" {
		data, err := profilesGetFn(opts.profile)
		if err != nil {
			return fmt.Errorf("unknown profile %q (available: %v)", opts.profile, profilesNamesFn())
		}
		profileYAML = data
	}

	// Future: spec 15 will wire --from end-to-end during init. For now
	// the flag is accepted but ignored with a stderr notice so users
	// know not to rely on it yet.
	if opts.fromURL != "" {
		fmt.Fprintln(stderr, "note: --from is accepted but not yet applied during init (see spec 15)")
	}

	// 4. Save the sized config.
	if err := saveConfigFn(configPath(), cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	// Drop the starter bolted.yaml next to config.yaml. We do this
	// AFTER saveConfig so the BoltedDir is guaranteed to exist —
	// Save creates ~/.bolted (0o700) on its first call. Provisioning
	// is a separate explicit step — `bolt provision` after `bolt unlock`.
	if profileYAML != nil {
		yamlPath := filepath.Join(boltedDirFn(), "bolted.yaml")
		if err := profileWriteFileFn(yamlPath, profileYAML, 0o600); err != nil {
			return fmt.Errorf("write profile %s: %w", yamlPath, err)
		}
		fmt.Fprintf(stderr, "wrote bolted.yaml from profile %q. Run `bolt provision` after `bolt unlock` to apply it.\n", opts.profile)
	}

	// 5. EnsureVM + StartVM on the already-preflighted backend.
	spec, err := vmSpecFromConfig(cfg.VM)
	if err != nil {
		return err
	}
	if err := b.EnsureVM(ctx, spec); err != nil {
		return fmt.Errorf("ensure VM: %w", err)
	}
	if err := b.StartVM(ctx); err != nil {
		return fmt.Errorf("start VM: %w", err)
	}

	// 6. Create the encrypted volume inside the VM. Reuse the already-
	// validated disk size from the spec (no second ParseSize round-trip).
	v := newVolumeFn(b, volume.Options{Name: vmMapperName})
	diskBytes := int64(spec.DiskGB) * 1024 * 1024 * 1024
	if err := v.Create(ctx, vmVolumeImagePath, diskBytes, password); err != nil {
		return fmt.Errorf("create encrypted volume: %w", err)
	}

	// 7. Next steps.
	fmt.Fprintln(stderr, "bolt initialised. Next: `bolt unlock` to mount the volume.")
	return nil
}

// readNewPassword wraps the prompter for the init flow: --password-stdin
// reads one line, otherwise prompt twice with confirmation.
func readNewPassword(p passwordPrompter, fromStdin bool) ([]byte, error) {
	if fromStdin {
		return p.FromStdin()
	}
	return p.PromptTwiceConfirm("New Bolted password: ")
}

// --- bolt unlock --------------------------------------------------------------

type unlockOptions struct {
	passwordStdin bool
}

func newUnlockCmd() *cobra.Command {
	opts := &unlockOptions{}
	cmd := &cobra.Command{
		Use:   "unlock",
		Short: "Unlock Bolted (prompt for password, mount the encrypted volume)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUnlock(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), *opts)
		},
	}
	cmd.Flags().BoolVar(&opts.passwordStdin, "password-stdin", false, "Read the password from stdin instead of prompting")
	return cmd
}

func runUnlock(ctx context.Context, _ io.Writer, stderr io.Writer, opts unlockOptions) error {
	cfg, err := loadConfigFn(configPath())
	if err != nil {
		return fmt.Errorf("load config: %w (run `bolt init` first?)", err)
	}

	prompter := newPrompterFn()
	password, err := readExistingPassword(prompter, opts.passwordStdin)
	if err != nil {
		return err
	}
	defer zero(password)

	b, err := newBackendFn(backend.Config{Backend: cfg.Backend})
	if err != nil {
		return fmt.Errorf("backend init: %w", err)
	}

	// Ensure VM is running so cryptsetup can run inside it.
	running, err := b.IsRunning(ctx)
	if err != nil {
		return fmt.Errorf("check VM state: %w", err)
	}
	if !running {
		if err := b.StartVM(ctx); err != nil {
			return fmt.Errorf("start VM: %w", err)
		}
	}

	v := newVolumeFn(b, volume.Options{Name: vmMapperName})
	dev, err := v.Open(ctx, vmVolumeImagePath, password)
	if err != nil {
		if errors.Is(err, volume.ErrBadPassword) {
			fmt.Fprintln(stderr, "wrong password")
			return &exitError{code: exitBadPassword, err: err}
		}
		return fmt.Errorf("open volume: %w", err)
	}
	if err := v.Mount(ctx, dev, vmMountpoint); err != nil {
		// Best-effort: close the LUKS mapping so we don't leave the
		// Bolted half-unlocked.
		_ = v.Close(ctx, dev)
		return fmt.Errorf("mount volume: %w", err)
	}

	// Chown the mountpoint to the VM's regular user. Without this,
	// every passthrough write (bolt git clone, etc.) hits Permission
	// denied — Mount runs as root and the ext4 root inode inherits
	// root:root. id runs unescalated so it returns the regular user's
	// uid/gid, then sudo elevates only the chown. The ownership is
	// stored on the inode, so this persists across future unlocks; the
	// re-run on every unlock is idempotent.
	if res, err := b.Exec(ctx, []string{
		"sh", "-c", `sudo chown "$(id -u):$(id -g)" "$0"`, vmMountpoint,
	}, backend.ExecOpts{}); err != nil || res.ExitCode != 0 {
		_ = v.Unmount(ctx, vmMountpoint)
		_ = v.Close(ctx, dev)
		return fmt.Errorf("chown mountpoint: %w (stderr: %s)", err, res.Stderr)
	}

	fmt.Fprintln(stderr, "Bolted unlocked.")
	return nil
}

// readExistingPassword wraps the prompter for the unlock flow: one shot
// (no confirm). --password-stdin reads one line.
func readExistingPassword(p passwordPrompter, fromStdin bool) ([]byte, error) {
	if fromStdin {
		return p.FromStdin()
	}
	return p.Prompt("Bolted password: ")
}

// --- bolt lock ----------------------------------------------------------------

type lockOptions struct {
	stopVM bool
}

func newLockCmd() *cobra.Command {
	opts := &lockOptions{}
	cmd := &cobra.Command{
		Use:   "lock",
		Short: "Lock Bolted (unmount volume, evict the key, optionally stop the VM)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLock(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), *opts)
		},
	}
	cmd.Flags().BoolVar(&opts.stopVM, "stop-vm", false, "Also stop the VM (default keeps it running for fast re-unlock)")
	return cmd
}

func runLock(ctx context.Context, _ io.Writer, stderr io.Writer, opts lockOptions) error {
	cfg, err := loadConfigFn(configPath())
	if err != nil {
		return fmt.Errorf("load config: %w (run `bolt init` first?)", err)
	}

	b, err := newBackendFn(backend.Config{Backend: cfg.Backend})
	if err != nil {
		return fmt.Errorf("backend init: %w", err)
	}

	// Future (spec 13): stop running dev containers here. For now, log
	// and continue.
	// TODO(spec-13): stop running containers before unmount.

	v := newVolumeFn(b, volume.Options{Name: vmMapperName})
	if err := v.Unmount(ctx, vmMountpoint); err != nil {
		return fmt.Errorf("unmount volume: %w", err)
	}
	if err := v.Close(ctx, volume.Device(vmMapperName)); err != nil {
		return fmt.Errorf("close LUKS mapping: %w", err)
	}
	if opts.stopVM {
		if err := b.StopVM(ctx); err != nil {
			return fmt.Errorf("stop VM: %w", err)
		}
	}
	fmt.Fprintln(stderr, "Bolted locked.")
	return nil
}

// exitError lets a RunE carry both an underlying error and a specific
// process exit code. Execute (in cli.go) recognises it.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *exitError) Unwrap() error { return e.err }

// exitCodeFromError extracts a custom exit code if err is (or wraps) an
// *exitError, else returns exitGeneric.
func exitCodeFromError(err error) int {
	if err == nil {
		return exitOK
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	return exitGeneric
}
