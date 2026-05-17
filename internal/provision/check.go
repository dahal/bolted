package provision

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/dahal/bolted/internal/backend"
)

// Check compares profile to cache and reports whether Bolted
// has drifted from the desired state. It is read-only: it never
// mutates cache, never calls Backend.Exec, and never touches the host
// filesystem (callers that need a content-hash check on dotfiles
// should run Apply with a no-op stdout — the spec scopes Check to
// "what the cache says", which is fast and good enough for shell
// prompts and CI gating).
//
// The backend argument is reserved for a future "ask the VM directly"
// extension; today we only consult the cache. We accept it now so
// callers don't need to change their signatures later.
//
// Returns (drifted, human-summary, error). The summary is a one-line
// human-readable diff suitable for printing under `bolt provision
// --check`; on the "in sync" path it is the empty string.
func Check(_ backend.Backend, profile *BoltedProfile, cache *Cache) (bool, string, error) {
	if profile == nil {
		return false, "", fmt.Errorf("provision: Check: nil profile")
	}
	if cache == nil {
		return false, "", fmt.Errorf("provision: Check: nil cache")
	}

	featAdded, featRemoved := diffStrings(cache.Features, profile.Features)
	pkgAdded, pkgRemoved := diffStrings(cache.Packages, profile.Packages)

	shellDrift := profile.Shell != "" && profile.Shell != cache.Shell
	gitDrift := !gitConfigEqual(profile.GitConfig, cache.GitConfig)
	dotfileDrift := dotfilesDrifted(profile.Dotfiles, cache.Dotfiles)

	drifted := len(featAdded) > 0 ||
		len(featRemoved) > 0 ||
		len(pkgAdded) > 0 ||
		len(pkgRemoved) > 0 ||
		shellDrift ||
		gitDrift ||
		len(dotfileDrift) > 0

	if !drifted {
		return false, "", nil
	}

	var parts []string
	if len(featAdded) > 0 {
		parts = append(parts, "features +"+strings.Join(featAdded, ","))
	}
	if len(featRemoved) > 0 {
		parts = append(parts, "features -"+strings.Join(featRemoved, ","))
	}
	if len(pkgAdded) > 0 {
		parts = append(parts, "packages +"+strings.Join(pkgAdded, ","))
	}
	if len(pkgRemoved) > 0 {
		parts = append(parts, "packages -"+strings.Join(pkgRemoved, ","))
	}
	if shellDrift {
		parts = append(parts, fmt.Sprintf("shell %s→%s", cache.Shell, profile.Shell))
	}
	if gitDrift {
		parts = append(parts, "gitconfig")
	}
	if len(dotfileDrift) > 0 {
		parts = append(parts, "dotfiles "+strings.Join(dotfileDrift, ","))
	}

	return true, strings.Join(parts, "; "), nil
}

// gitConfigEqual returns true if both maps have the same key/value
// pairs. Stable wrapper around reflect.DeepEqual so the rest of the
// code reads naturally.
func gitConfigEqual(a, b map[string]string) bool {
	if len(a) == 0 && len(b) == 0 {
		// reflect.DeepEqual(map(nil), map{}) is false; we want true.
		return true
	}
	return reflect.DeepEqual(a, b)
}

// dotfilesDrifted reports which dotfile entries from the profile have
// no matching cache entry. We can't hash source bytes here without
// host IO (which Check declines to do), so the contract is "drifted iff
// the profile lists a dotfile the cache has never seen". The first
// Apply seeds the cache; subsequent same-content runs report no drift.
func dotfilesDrifted(want []string, cached map[string]string) []string {
	var out []string
	for _, rel := range want {
		if _, ok := cached[rel]; !ok {
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out
}
