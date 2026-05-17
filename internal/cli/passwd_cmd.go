package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/dahal/bolted/internal/backend"
	"github.com/dahal/bolted/internal/keychain"
	"github.com/dahal/bolted/internal/volume"
)

// keychainServiceName is the service identifier used for entries we
// store in the OS credential manager. Stable across versions so an
// upgrade doesn't orphan keychain entries.
const keychainServiceName = "bolted"

// keychainAccountName is the per-user account identifier. The CLI only
// supports a single Bolted instance per user today (config lives in
// ~/.bolted), so a fixed account name is sufficient.
const keychainAccountName = "default"

// passwdVolume is the slice of *volume.Volume the passwd command
// actually uses. Mirrors the lifecycle.go `volumeOps` indirection but
// adds the keyslot methods (which `bolt unlock` doesn't need) — keeping
// them on a separate interface means lifecycle_test.go's fakeVolume
// doesn't have to implement them.
type passwdVolume interface {
	Open(ctx context.Context, imagePath string, password []byte) (volume.Device, error)
	Close(ctx context.Context, dev volume.Device) error
	AddKeyslot(ctx context.Context, imagePath string, existing, new []byte) error
	RemoveKeyslot(ctx context.Context, imagePath string, password []byte) error
}

// newPasswdVolumeFn is the test injection point for the wider volume
// interface. Defaults to wrapping volume.New (which returns *Volume,
// satisfying passwdVolume).
var newPasswdVolumeFn = func(b backend.Backend, opts volume.Options) passwdVolume {
	return volume.New(b, opts)
}

// newKeychainStoreFn is the test injection point for the OS keychain.
// Defaults to keychain.System (platform-appropriate store). Tests
// inject a fake to assert Set / Delete behaviour without touching the
// real keychain.
var newKeychainStoreFn = keychain.System

// newPasswdCmd builds the `bolt passwd` command. The command performs an
// atomic LUKS slot rotation: add the new key, verify it unlocks, then
// remove the old. On any step we error and bail without removing the
// old slot, so a partial failure never leaves the user locked out.
func newPasswdCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "passwd",
		Short: "Change the Bolted passphrase (LUKS slot rotation)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPasswd(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
}

// runPasswd implements the password-rotation flow. Steps:
//
//  1. Load config; init backend.
//  2. Prompt for the existing password (one shot, no echo).
//  3. Dry-run verify by opening the LUKS volume and immediately
//     closing it. Surfaces a wrong-password error with exit code 3.
//  4. Prompt for the new password twice (confirmation).
//  5. AddKeyslot(existing, new). LUKS now has TWO valid slots — the
//     user can still unlock with the old password if anything below
//     fails.
//  6. Dry-run verify the new key actually works (Open+Close again).
//     If this fails we surface the error — the user can re-run
//     `passwd` without losing data because the old slot is still in
//     place.
//  7. RemoveKeyslot(existing). Only now do we drop the old key. If
//     this fails we report it but the volume is still usable with
//     either key.
//  8. If config.Keychain is true, update the stored secret with the
//     new password. Failures here are surfaced as warnings — the
//     rotation already succeeded.
func runPasswd(ctx context.Context, _ io.Writer, stderr io.Writer) error {
	cfg, err := loadConfigFn(configPath())
	if err != nil {
		return fmt.Errorf("load config: %w (run `bolt init` first?)", err)
	}

	b, err := newBackendFn(backend.Config{Backend: cfg.Backend})
	if err != nil {
		return fmt.Errorf("backend init: %w", err)
	}

	prompter := newPrompterFn()

	// Step 2: read existing password.
	existing, err := prompter.Prompt("Current Bolted password: ")
	if err != nil {
		return err
	}
	defer zero(existing)

	v := newPasswdVolumeFn(b, volume.Options{Name: vmMapperName})

	// Step 3: dry-run verify the current password.
	dev, err := v.Open(ctx, vmVolumeImagePath, existing)
	if err != nil {
		if errors.Is(err, volume.ErrBadPassword) {
			fmt.Fprintln(stderr, "wrong password")
			return &exitError{code: exitBadPassword, err: err}
		}
		return fmt.Errorf("verify current password: %w", err)
	}
	// Drop the mapping immediately. We only needed proof of authn,
	// not a live mount.
	if err := v.Close(ctx, dev); err != nil {
		return fmt.Errorf("close after verify: %w", err)
	}

	// Step 4: read new password (with confirmation).
	newPW, err := prompter.PromptTwiceConfirm("New Bolted password: ")
	if err != nil {
		return err
	}
	defer zero(newPW)

	// Step 5: add the new slot. After this point both passwords work.
	if err := v.AddKeyslot(ctx, vmVolumeImagePath, existing, newPW); err != nil {
		return fmt.Errorf("add new keyslot: %w", err)
	}

	// Step 6: verify the new key really unlocks. Bail early without
	// removing the old slot if anything is amiss.
	dev, err = v.Open(ctx, vmVolumeImagePath, newPW)
	if err != nil {
		return fmt.Errorf("verify new password: %w (old password still works; re-run `bolt passwd`)", err)
	}
	if err := v.Close(ctx, dev); err != nil {
		return fmt.Errorf("close after new-key verify: %w", err)
	}

	// Step 7: only now remove the old slot.
	if err := v.RemoveKeyslot(ctx, vmVolumeImagePath, existing); err != nil {
		return fmt.Errorf("remove old keyslot: %w (both passwords currently work; re-run `bolt passwd`)", err)
	}

	// Step 8: update keychain entry if opt-in is on.
	if cfg.Keychain {
		store := newKeychainStoreFn()
		if err := store.Set(keychainServiceName, keychainAccountName, newPW); err != nil {
			// Non-fatal: the rotation succeeded. Warn but exit 0.
			fmt.Fprintf(stderr, "warning: rotation succeeded but keychain update failed: %v\n", err)
		}
	}

	fmt.Fprintln(stderr, "Bolted password changed.")
	return nil
}
