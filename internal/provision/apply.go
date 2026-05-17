package provision

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dahal/bolted/internal/backend"
)

// Result is the summary returned by Apply. Counts are exact; Duration is
// wall-clock for the full apply, including the no-op fast path.
type Result struct {
	// FeaturesAdded is the list of features installed this run.
	FeaturesAdded []string
	// FeaturesRemoved is the list of features removed this run.
	FeaturesRemoved []string
	// PackagesAdded is the list of apk packages installed this run.
	PackagesAdded []string
	// PackagesRemoved is the list of apk packages removed this run.
	PackagesRemoved []string
	// GitConfigApplied is the number of `git config --global` keys
	// re-issued this run (we always re-issue every key — cheap and
	// idempotent — so this equals len(profile.GitConfig)).
	GitConfigApplied int
	// ShellSet is true if `chsh` was invoked this run.
	ShellSet bool
	// DotfilesChanged is the list of dotfile-relative-paths whose
	// content was (re)written this run.
	DotfilesChanged []string
	// DotfilesOverwritten is the subset of DotfilesChanged that
	// existed in the VM before the copy and was therefore overwritten.
	DotfilesOverwritten []string
	// Duration is the wall-clock time for the apply.
	Duration time.Duration
}

// Indirection points for source-file IO and the system clock. Tests
// substitute these to drive specific code paths without touching the
// real filesystem or clock.
var (
	// readDotfileFn returns the source bytes and mode bits for a
	// dotfile path on the host. Default: os.ReadFile + os.Stat.
	// Tests substitute this wholesale; the production default has
	// only one error surface (any IO failure → return as-is).
	readDotfileFn = defaultReadDotfile
	// nowFn lets tests pin Duration for deterministic assertions.
	nowFn = time.Now
)

// defaultReadDotfile reads path and returns its bytes and unix
// permission bits. We Stat first (cheap, validates the file exists and
// gives us the mode), then ReadFile. Both calls route through the
// statFn / readFileFn indirection so tests can drive each error path
// independently.
func defaultReadDotfile(path string) ([]byte, fs.FileMode, error) {
	st, err := statFn(path)
	if err != nil {
		return nil, 0, err
	}
	data, err := readFileFn(path)
	if err != nil {
		return nil, 0, err
	}
	return data, st.Mode().Perm(), nil
}

// statFn is the os.Stat indirection used by defaultReadDotfile. Held
// at package scope (not as a closure inside defaultReadDotfile) so
// tests can swap it cleanly via t.Cleanup.
var statFn = os.Stat

// Apply reconciles the VM state with profile, mutating cache in-place
// to reflect what was actually applied. It is idempotent: re-running
// Apply with the same profile and cache is a no-op (every diff is
// empty, every dotfile digest matches).
//
// Pipeline:
//
//  1. Diff features → install missing, remove deleted (via
//     `devcontainer features install <ref>` and our own remove helper).
//  2. Diff packages → `apk add` / `apk del`.
//  3. Re-issue every gitconfig key via `git config --global` (always —
//     deciding "this key already has this value" is expensive and the
//     command itself is dirt cheap).
//  4. `chsh -s <shell>` if profile.Shell differs from cache.Shell.
//  5. For each dotfile: hash the source; skip if the cached hash
//     matches; otherwise probe the destination, warn-and-record if it
//     already exists, then stream the bytes into the VM via
//     Backend.Exec stdin (`tee` writes the content, then `chmod`).
//
// dotfilesBaseDir is the directory the profile.Dotfiles paths are
// relative to (typically filepath.Dir of the bolted.yaml file). If a
// dotfile path is absolute we use it verbatim; relative paths are
// joined onto dotfilesBaseDir.
//
// stdout receives one human-readable progress line per VM operation
// plus a "warning: overwriting <path>" line for each replaced dotfile.
// Passing nil is allowed: messages are discarded.
func Apply(
	ctx context.Context,
	b backend.Backend,
	profile *BoltedProfile,
	cache *Cache,
	dotfilesBaseDir string,
	stdout io.Writer,
) (*Result, error) {
	if profile == nil {
		return nil, fmt.Errorf("provision: Apply: nil profile")
	}
	if cache == nil {
		return nil, fmt.Errorf("provision: Apply: nil cache")
	}
	if b == nil {
		return nil, fmt.Errorf("provision: Apply: nil backend")
	}
	if stdout == nil {
		stdout = io.Discard
	}

	start := nowFn()
	res := &Result{}

	// 1. Features.
	featAdded, featRemoved := diffStrings(cache.Features, profile.Features)
	for _, ref := range featAdded {
		fmt.Fprintf(stdout, "installing feature %s\n", ref)
		if err := installFeature(ctx, b, ref); err != nil {
			return res, err
		}
	}
	for _, ref := range featRemoved {
		fmt.Fprintf(stdout, "removing feature %s\n", ref)
		if err := removeFeature(ctx, b, ref); err != nil {
			return res, err
		}
	}
	res.FeaturesAdded = featAdded
	res.FeaturesRemoved = featRemoved
	cache.Features = append([]string(nil), profile.Features...)

	// 2. Packages.
	pkgAdded, pkgRemoved := diffStrings(cache.Packages, profile.Packages)
	if len(pkgAdded) > 0 {
		fmt.Fprintf(stdout, "apk add %s\n", strings.Join(pkgAdded, " "))
		if err := apkAdd(ctx, b, pkgAdded); err != nil {
			return res, err
		}
	}
	if len(pkgRemoved) > 0 {
		fmt.Fprintf(stdout, "apk del %s\n", strings.Join(pkgRemoved, " "))
		if err := apkDel(ctx, b, pkgRemoved); err != nil {
			return res, err
		}
	}
	res.PackagesAdded = pkgAdded
	res.PackagesRemoved = pkgRemoved
	cache.Packages = append([]string(nil), profile.Packages...)

	// 3. GitConfig — always re-apply, sorted for stable output.
	gitKeys := make([]string, 0, len(profile.GitConfig))
	for k := range profile.GitConfig {
		gitKeys = append(gitKeys, k)
	}
	sort.Strings(gitKeys)
	for _, k := range gitKeys {
		v := profile.GitConfig[k]
		fmt.Fprintf(stdout, "git config --global %s\n", k)
		if err := gitConfigSet(ctx, b, k, v); err != nil {
			return res, err
		}
		res.GitConfigApplied++
	}
	cache.GitConfig = map[string]string{}
	for k, v := range profile.GitConfig {
		cache.GitConfig[k] = v
	}

	// 4. Shell. Apply only when the desired value differs from the
	// cached value.
	if profile.Shell != "" && profile.Shell != cache.Shell {
		fmt.Fprintf(stdout, "chsh -s %s\n", profile.Shell)
		if err := chsh(ctx, b, profile.Shell); err != nil {
			return res, err
		}
		cache.Shell = profile.Shell
		res.ShellSet = true
	}

	// 5. Dotfiles.
	for _, rel := range profile.Dotfiles {
		src := rel
		if !filepath.IsAbs(src) {
			src = filepath.Join(dotfilesBaseDir, rel)
		}
		data, mode, err := readDotfileFn(src)
		if err != nil {
			return res, fmt.Errorf("provision: read dotfile %s: %w", src, err)
		}
		sum := sha256.Sum256(data)
		digest := hex.EncodeToString(sum[:])
		if cache.Dotfiles[rel] == digest {
			continue
		}
		// Destination inside the VM. We resolve $HOME on the VM
		// side (via the shell that runs `tee`) rather than hard-
		// coding /root or /home/<user> on the host — the VM image
		// owns the user model, and either root or a non-root wsl
		// user is fine because `tee` runs as whatever user
		// backend.Exec runs commands as.
		dst := vmDotfilePath(rel)
		existed, err := remoteExists(ctx, b, dst)
		if err != nil {
			return res, err
		}
		if existed {
			fmt.Fprintf(stdout, "warning: overwriting %s\n", dst)
			res.DotfilesOverwritten = append(res.DotfilesOverwritten, rel)
		}
		if err := copyDotfile(ctx, b, dst, data, mode); err != nil {
			return res, err
		}
		cache.Dotfiles[rel] = digest
		res.DotfilesChanged = append(res.DotfilesChanged, rel)
	}

	res.Duration = nowFn().Sub(start)
	return res, nil
}

// --- VM-side primitives ----------------------------------------------------
//
// Each of these wraps a single backend.Backend.Exec call. They live in
// this file (not a separate vm.go) because they are private and tightly
// coupled to Apply's pipeline; pulling them out would just spread the
// surface.

// installFeature runs `devcontainer features install <ref>` inside the
// VM. Output is captured; on failure we surface stderr so the user can
// see what the upstream CLI complained about.
func installFeature(ctx context.Context, b backend.Backend, ref string) error {
	res, err := b.Exec(ctx, []string{"devcontainer", "features", "install", ref}, backend.ExecOpts{})
	return wrapExec("install feature "+ref, res, err)
}

// removeFeature is best-effort: the devcontainer CLI has no "uninstall"
// verb at time of writing, so we shell out to a small rm that drops the
// install marker dir maintained by the features tooling. The on-disk
// binaries the feature installed remain until the next image rebuild;
// removing them precisely would require feature-specific logic which we
// don't have. This is enough to make `Check` report "not present".
func removeFeature(ctx context.Context, b backend.Backend, ref string) error {
	res, err := b.Exec(ctx, []string{
		"rm", "-rf", filepath.Join("/var/lib/devcontainer-features", featureSlug(ref)),
	}, backend.ExecOpts{})
	return wrapExec("remove feature "+ref, res, err)
}

// featureSlug turns a feature OCI ref into a filesystem-safe slug used
// as the install-marker directory name. `ghcr.io/foo/bar:1` becomes
// `ghcr.io_foo_bar_1`.
func featureSlug(ref string) string {
	repl := strings.NewReplacer("/", "_", ":", "_")
	return repl.Replace(ref)
}

// apkAdd installs every name in one `apk add` invocation so the package
// manager can resolve dependencies as a single transaction.
func apkAdd(ctx context.Context, b backend.Backend, names []string) error {
	args := append([]string{"apk", "add", "--no-cache"}, names...)
	res, err := b.Exec(ctx, args, backend.ExecOpts{})
	return wrapExec("apk add", res, err)
}

// apkDel mirrors apkAdd.
func apkDel(ctx context.Context, b backend.Backend, names []string) error {
	args := append([]string{"apk", "del"}, names...)
	res, err := b.Exec(ctx, args, backend.ExecOpts{})
	return wrapExec("apk del", res, err)
}

// gitConfigSet issues `git config --global <key> <value>`. We pass the
// value as a separate argv element (not interpolated) so a value with
// shell metacharacters can't escape.
func gitConfigSet(ctx context.Context, b backend.Backend, key, value string) error {
	res, err := b.Exec(ctx, []string{"git", "config", "--global", key, value}, backend.ExecOpts{})
	return wrapExec("git config "+key, res, err)
}

// chsh sets the login shell. We invoke `chsh -s <shell>` with no
// explicit user; the VM image runs commands as either root (lima
// default) or a single wsl user — `chsh` defaults to the current user,
// which is the one we want either way.
func chsh(ctx context.Context, b backend.Backend, shell string) error {
	res, err := b.Exec(ctx, []string{"chsh", "-s", shell}, backend.ExecOpts{})
	return wrapExec("chsh", res, err)
}

// remoteExists probes whether a path is present in the VM. Non-zero
// exit = absent (the usual `test -e` convention); any backend-level
// error is surfaced so the caller can fail cleanly rather than guessing.
func remoteExists(ctx context.Context, b backend.Backend, path string) (bool, error) {
	// We run `sh -c 'test -e <expanded>'` because we want $HOME
	// expansion on the VM side. Argv-style `test -e $HOME/x` would
	// pass the literal "$HOME/x" through to the test binary.
	res, err := b.Exec(ctx, []string{"sh", "-c", "test -e " + path}, backend.ExecOpts{})
	if err != nil {
		return false, fmt.Errorf("provision: probe %s: %w", path, err)
	}
	return res.ExitCode == 0, nil
}

// copyDotfile writes data to dstPath inside the VM, preserving mode.
// The bytes are streamed via Backend.Exec stdin so we avoid quoting /
// length limits on argv. Sequence:
//
//  1. `sh -c 'mkdir -p "$(dirname <dst>)" && tee <dst> > /dev/null'`
//     with data piped to stdin.
//  2. `chmod <octal> <dst>` to set the source's permission bits.
//
// We separate steps so a partial write (`tee` succeeded, `chmod`
// failed) is still observable: the file exists and the next provision
// will retry the chmod on the same digest.
func copyDotfile(ctx context.Context, b backend.Backend, dstPath string, data []byte, mode fs.FileMode) error {
	script := `mkdir -p "$(dirname ` + dstPath + `)" && tee ` + dstPath + ` > /dev/null`
	res, err := b.Exec(ctx, []string{"sh", "-c", script}, backend.ExecOpts{Stdin: bytes.NewReader(data)})
	if err := wrapExec("write "+dstPath, res, err); err != nil {
		return err
	}
	res, err = b.Exec(ctx, []string{"chmod", fmt.Sprintf("%o", mode.Perm()), dstPath}, backend.ExecOpts{})
	return wrapExec("chmod "+dstPath, res, err)
}

// vmDotfilePath returns the destination path inside the VM for a
// dotfile-relative-path. The result is a shell-expandable path (we
// always run it through `sh -c` so `$HOME` gets resolved by the VM).
func vmDotfilePath(rel string) string {
	return `"$HOME"/` + rel
}

// wrapExec folds an Exec failure into a single error tagged with the
// operation name. Mirrors the helper in internal/devcontainer.
func wrapExec(op string, res backend.ExecResult, err error) error {
	stderr := strings.TrimSpace(string(res.Stderr))
	switch {
	case err != nil && stderr != "":
		return fmt.Errorf("provision: %s: %w: %s", op, err, stderr)
	case err != nil:
		return fmt.Errorf("provision: %s: %w", op, err)
	case res.ExitCode != 0 && stderr != "":
		return fmt.Errorf("provision: %s: exit %d: %s", op, res.ExitCode, stderr)
	case res.ExitCode != 0:
		return fmt.Errorf("provision: %s: exit %d", op, res.ExitCode)
	default:
		return nil
	}
}
