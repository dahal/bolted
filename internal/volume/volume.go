// Package volume wraps the LUKS2 lifecycle for the Bolted's encrypted
// repo volume. All disk operations run inside the Bolted VM via
// backend.Backend.Exec — cryptsetup, mkfs.ext4, mount, umount — so the
// host process never sees the on-disk key material directly.
//
// # Security posture
//
// Passwords flow into cryptsetup over stdin (the `--key-file=-` form),
// never as an argv argument. Argv would be visible to anyone with
// /proc/<pid>/cmdline access (any local process under the same uid, and
// historically `ps` listings) and would also leak into shell history
// when scripts piece commands together. Stdin sidesteps both.
//
// Volume retains no password state. Every method takes the password as
// a parameter, hands it straight to Backend.Exec via an in-memory
// bytes.Reader, and returns. The caller is responsible for zeroing the
// buffer once the call returns.
//
// # KDF choice
//
// LUKS2 is used with the Argon2id KDF (memory-hard, GPU-resistant).
// We rely on cryptsetup's auto-benchmark to pick PBKDF parameters that
// target ~1 second of work on the executing host — Volume deliberately
// does not pass --pbkdf-memory / --pbkdf-iterations / --pbkdf-parallel
// overrides, because (a) the upstream defaults are already sensible
// and tuned to the SI standard, and (b) hand-tuning would lock us to
// numbers that age badly as hardware improves. The only parameter we
// pin is --pbkdf=argon2id itself (defending against a future cryptsetup
// default change to a different algorithm).
//
// # Mapper name
//
// Each Volume owns one mapper name (default "bolted"), translating
// to /dev/mapper/<name> inside the VM. Tests can supply a different
// name via Options to avoid clashing with a real install when an e2e
// test runs against a shared VM.
package volume

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/dahal/bolted/internal/backend"
)

// ErrBadPassword is returned by Open (and the keyslot helpers) when
// cryptsetup rejects the supplied passphrase. Callers should check
// errors.Is(err, ErrBadPassword) and surface a friendly message.
var ErrBadPassword = errors.New("bad password")

// defaultName is the mapper name used when Options.Name is empty. It
// matches the design in brainstorm 05-encryption.md (the unlocked
// device shows up as /dev/mapper/bolted).
const defaultName = "bolted"

// Device represents an unlocked LUKS mapping inside the VM. The string
// value is the mapper name (e.g. "bolted") — the full device path
// inside the VM is /dev/mapper/<name>.
type Device string

// Path returns the absolute device path inside the VM for the mapping.
// Equivalent to "/dev/mapper/" + string(d).
func (d Device) Path() string { return "/dev/mapper/" + string(d) }

// Options configures New. The zero value is valid; defaults are
// documented per field.
type Options struct {
	// Name overrides the LUKS mapper name. Empty means "bolted".
	Name string
}

// Volume is the LUKS lifecycle handle. It is safe to construct one per
// CLI invocation; there is no long-lived state to share. Volume does
// NOT cache passwords — every operation accepts the password as a
// parameter and forwards it to the backend over stdin.
type Volume struct {
	// backend is the VM execution surface. All cryptsetup / mount /
	// mkfs invocations route through backend.Exec.
	backend backend.Backend
	// name is the mapper name, equal to defaultName unless overridden
	// via Options.Name.
	name string
}

// New returns a Volume bound to the given backend and options.
func New(b backend.Backend, opts Options) *Volume {
	name := opts.Name
	if name == "" {
		name = defaultName
	}
	return &Volume{backend: b, name: name}
}

// Name returns the mapper name this Volume operates against. Exposed
// for tests and for callers that want to log the underlying device.
func (v *Volume) Name() string { return v.name }

// Create initialises a fresh encrypted volume at imagePath (a path
// inside the VM filesystem):
//
//  1. mkdir -p <dirname(imagePath)>  — first-run, /bolted does not
//     exist in the guest yet and truncate won't create parents.
//  2. truncate -s <sizeBytes> <imagePath>  — sparse backing file.
//  3. cryptsetup luksFormat --type luks2 --pbkdf argon2id --key-file=-
//     <imagePath>  — LUKS2 header + first keyslot, password on stdin.
//  4. cryptsetup open --key-file=- <imagePath> <name>  — brief unlock
//     so we can put a filesystem on the mapper.
//  5. mkfs.ext4 -q /dev/mapper/<name>  — ext4 filesystem inside the
//     LUKS container.
//  6. cryptsetup close <name>  — drop the mapping; Open will re-create
//     it later.
//
// sizeBytes is the maximum sparse size — the backing file consumes
// only as many host bytes as have actually been written. The caller
// is responsible for zeroing the password buffer after Create returns;
// Volume does not retain it.
func (v *Volume) Create(ctx context.Context, imagePath string, sizeBytes int64, password []byte) error {
	if imagePath == "" {
		return errors.New("volume: Create: imagePath is empty")
	}
	if sizeBytes <= 0 {
		return errors.New("volume: Create: sizeBytes must be positive")
	}
	if len(password) == 0 {
		return errors.New("volume: Create: password is empty")
	}

	if dir := path.Dir(imagePath); dir != "" && dir != "/" && dir != "." {
		if err := v.exec(ctx, "mkdir", []string{
			"mkdir", "-p", dir,
		}, nil); err != nil {
			return err
		}
	}

	if err := v.exec(ctx, "truncate", []string{
		"truncate", "-s", strconv.FormatInt(sizeBytes, 10), imagePath,
	}, nil); err != nil {
		return err
	}

	if err := v.execWithPassword(ctx, "luksFormat", []string{
		"cryptsetup", "luksFormat",
		"--type", "luks2",
		"--pbkdf", "argon2id",
		"--batch-mode",
		"--key-file=-",
		imagePath,
	}, password); err != nil {
		return err
	}

	if err := v.execWithPassword(ctx, "open", []string{
		"cryptsetup", "open", "--key-file=-", imagePath, v.name,
	}, password); err != nil {
		return err
	}

	if err := v.exec(ctx, "mkfs.ext4", []string{
		"mkfs.ext4", "-q", "/dev/mapper/" + v.name,
	}, nil); err != nil {
		// Best-effort tear-down so a Create failure doesn't leave the
		// mapping live; we don't surface a close error here because
		// the mkfs failure is the actionable one.
		_ = v.exec(ctx, "close", []string{
			"cryptsetup", "close", v.name,
		}, nil)
		return err
	}

	if err := v.exec(ctx, "close", []string{
		"cryptsetup", "close", v.name,
	}, nil); err != nil {
		return err
	}
	return nil
}

// Open unlocks the LUKS volume into /dev/mapper/<name>. On a wrong
// password the returned error is wrapped with ErrBadPassword so
// errors.Is(err, ErrBadPassword) holds. The Device returned uses the
// configured mapper name.
func (v *Volume) Open(ctx context.Context, imagePath string, password []byte) (Device, error) {
	if imagePath == "" {
		return "", errors.New("volume: Open: imagePath is empty")
	}
	if len(password) == 0 {
		return "", errors.New("volume: Open: password is empty")
	}
	res, err := v.runWithPassword(ctx, []string{
		"cryptsetup", "open", "--key-file=-", imagePath, v.name,
	}, password)
	if err != nil || res.ExitCode != 0 {
		if isBadPassword(res, err) {
			return "", fmt.Errorf("cryptsetup open %s: %w", imagePath, ErrBadPassword)
		}
		return "", wrapExec("open", res, err)
	}
	return Device(v.name), nil
}

// Mount mounts an already-opened Device at mountpoint inside the VM.
// It runs `mkdir -p <mountpoint>` first so callers don't need to
// pre-create the directory.
func (v *Volume) Mount(ctx context.Context, dev Device, mountpoint string) error {
	if dev == "" {
		return errors.New("volume: Mount: device is empty")
	}
	if mountpoint == "" {
		return errors.New("volume: Mount: mountpoint is empty")
	}
	if err := v.exec(ctx, "mkdir", []string{"mkdir", "-p", mountpoint}, nil); err != nil {
		return err
	}
	if err := v.exec(ctx, "mount", []string{"mount", dev.Path(), mountpoint}, nil); err != nil {
		return err
	}
	return nil
}

// Unmount unmounts mountpoint inside the VM. The call is idempotent:
// a "not mounted" stderr from umount is treated as success so a
// caller can safely retry after a partial teardown.
func (v *Volume) Unmount(ctx context.Context, mountpoint string) error {
	if mountpoint == "" {
		return errors.New("volume: Unmount: mountpoint is empty")
	}
	res, err := v.backend.Exec(ctx, []string{"umount", mountpoint}, backend.ExecOpts{Sudo: true})
	if err == nil && res.ExitCode == 0 {
		return nil
	}
	if isNotMounted(res) {
		return nil
	}
	return wrapExec("umount", res, err)
}

// Close removes the LUKS mapping. The kernel evicts the master key
// from its keyring as a side-effect, restoring the at-rest guarantee.
func (v *Volume) Close(ctx context.Context, dev Device) error {
	if dev == "" {
		return errors.New("volume: Close: device is empty")
	}
	return v.exec(ctx, "close", []string{
		"cryptsetup", "close", string(dev),
	}, nil)
}

// AddKeyslot adds a new passphrase to a second LUKS keyslot. The
// existing passphrase authorises the change. cryptsetup expects the
// existing key first on stdin, then the new key — both terminated by
// newlines — which is exactly the wire format we use here.
//
// Used by spec 17's `bolt passwd` flow (add-new-then-remove-old).
// Volume does not retain either password after the call returns.
func (v *Volume) AddKeyslot(ctx context.Context, imagePath string, existing, new []byte) error {
	if imagePath == "" {
		return errors.New("volume: AddKeyslot: imagePath is empty")
	}
	if len(existing) == 0 {
		return errors.New("volume: AddKeyslot: existing password is empty")
	}
	if len(new) == 0 {
		return errors.New("volume: AddKeyslot: new password is empty")
	}
	stdin := buildPasswordStdin(existing, new)
	res, err := v.backend.Exec(ctx, []string{
		"cryptsetup", "luksAddKey",
		"--pbkdf", "argon2id",
		"--batch-mode",
		"--key-file=-",
		"--keyfile-size", strconv.Itoa(len(existing)),
		imagePath,
	}, backend.ExecOpts{Sudo: true, Stdin: stdin})
	if err != nil || res.ExitCode != 0 {
		if isBadPassword(res, err) {
			return fmt.Errorf("cryptsetup luksAddKey %s: %w", imagePath, ErrBadPassword)
		}
		return wrapExec("luksAddKey", res, err)
	}
	return nil
}

// RemoveKeyslot removes the slot containing password. Pair with
// AddKeyslot for safe rotation.
func (v *Volume) RemoveKeyslot(ctx context.Context, imagePath string, password []byte) error {
	if imagePath == "" {
		return errors.New("volume: RemoveKeyslot: imagePath is empty")
	}
	if len(password) == 0 {
		return errors.New("volume: RemoveKeyslot: password is empty")
	}
	res, err := v.runWithPassword(ctx, []string{
		"cryptsetup", "luksRemoveKey", "--batch-mode", "--key-file=-", imagePath,
	}, password)
	if err != nil || res.ExitCode != 0 {
		if isBadPassword(res, err) {
			return fmt.Errorf("cryptsetup luksRemoveKey %s: %w", imagePath, ErrBadPassword)
		}
		return wrapExec("luksRemoveKey", res, err)
	}
	return nil
}

// exec runs cmd via the backend with no stdin and returns an error if
// either the backend reports a failure or the command exits non-zero.
// opName is used to build a human-readable error message.
func (v *Volume) exec(ctx context.Context, opName string, cmd []string, _ []byte) error {
	res, err := v.backend.Exec(ctx, cmd, backend.ExecOpts{Sudo: true})
	if err != nil || res.ExitCode != 0 {
		return wrapExec(opName, res, err)
	}
	return nil
}

// execWithPassword runs cmd piping password as stdin and returns an
// error tagged with opName when the backend or cryptsetup itself
// reports failure. A bad-password exit is translated to ErrBadPassword.
func (v *Volume) execWithPassword(ctx context.Context, opName string, cmd []string, password []byte) error {
	res, err := v.runWithPassword(ctx, cmd, password)
	if err != nil || res.ExitCode != 0 {
		if isBadPassword(res, err) {
			return fmt.Errorf("cryptsetup %s: %w", opName, ErrBadPassword)
		}
		return wrapExec(opName, res, err)
	}
	return nil
}

// runWithPassword is the lowest-level helper for "exec with password on
// stdin". The password is wrapped in a bytes.Reader so it lives only as
// long as the Exec call; we deliberately do NOT close over it from a
// goroutine.
func (v *Volume) runWithPassword(ctx context.Context, cmd []string, password []byte) (backend.ExecResult, error) {
	return v.backend.Exec(ctx, cmd, backend.ExecOpts{Sudo: true, Stdin: bytes.NewReader(password)})
}

// buildPasswordStdin assembles the two-password stdin payload that
// `cryptsetup luksAddKey --key-file=-` expects: the existing key
// (its length is passed via --keyfile-size), immediately followed by
// the new key.
func buildPasswordStdin(existing, new []byte) *bytes.Reader {
	buf := make([]byte, 0, len(existing)+len(new))
	buf = append(buf, existing...)
	buf = append(buf, new...)
	return bytes.NewReader(buf)
}

// isBadPassword pattern-matches cryptsetup's response to a wrong
// passphrase. cryptsetup exits with code 2 ("no permission /
// authentication failure") and emits one of a handful of recognisable
// stderr lines depending on locale and version. We check the exit code
// first (cheap, locale-independent) and then fall back to a stderr
// substring match for robustness against future exit-code reshuffles.
func isBadPassword(res backend.ExecResult, _ error) bool {
	if res.ExitCode == 2 {
		return true
	}
	low := strings.ToLower(string(res.Stderr))
	switch {
	case strings.Contains(low, "no key available with this passphrase"):
		return true
	case strings.Contains(low, "no usable keyslot is available"):
		return true
	case strings.Contains(low, "no permission") && strings.Contains(low, "passphrase"):
		return true
	}
	return false
}

// isNotMounted detects the umount(8) "not mounted" stderr so Unmount
// can be idempotent. Linux umount uses two slightly different
// phrasings ("not mounted" and "not currently mounted") depending on
// version; both are caught here.
func isNotMounted(res backend.ExecResult) bool {
	low := strings.ToLower(string(res.Stderr))
	if strings.Contains(low, "not mounted") {
		return true
	}
	if strings.Contains(low, "not currently mounted") {
		return true
	}
	return false
}

// wrapExec folds an Exec failure (either a backend-level error or a
// non-zero exit) into a single error with the operation name and any
// stderr the tool produced. The wrapped error preserves the original
// backend error so errors.Unwrap / errors.Is keep working.
func wrapExec(opName string, res backend.ExecResult, err error) error {
	stderr := strings.TrimSpace(string(res.Stderr))
	switch {
	case err != nil && stderr != "":
		return fmt.Errorf("volume: %s: %w: %s", opName, err, stderr)
	case err != nil:
		return fmt.Errorf("volume: %s: %w", opName, err)
	case stderr != "":
		return fmt.Errorf("volume: %s: exit %d: %s", opName, res.ExitCode, stderr)
	default:
		return fmt.Errorf("volume: %s: exit %d", opName, res.ExitCode)
	}
}
