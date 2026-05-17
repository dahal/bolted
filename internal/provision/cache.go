package provision

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"

	"github.com/dahal/bolted/internal/state"
)

// Cache is the in-memory representation of provisioned.json: the set of
// things the apply loop believes are currently installed in the VM. It
// holds the same shape as BoltedProfile so diffs are
// trivial set-vs-set comparisons, plus a per-dotfile hash so we can
// detect content drift on dotfiles even when the list is unchanged.
//
// We intentionally do NOT cache the gitconfig or shell — applying them
// is cheap and idempotent enough that re-issuing the commands every run
// is fine.
type Cache struct {
	// Features is the set of feature OCI refs we last installed,
	// stored as a sorted slice on disk for stable diffs.
	Features []string `json:"features"`
	// Packages is the set of apk package names we last installed.
	Packages []string `json:"packages"`
	// Dotfiles maps dotfile-relative-path to a sha256 hex digest of
	// the source content at last apply time. A missing entry means
	// "never copied"; a present-but-stale digest means "copy again".
	Dotfiles map[string]string `json:"dotfiles"`
	// Shell records the shell that was last applied. Used by Check
	// to detect drift without re-running `chsh`.
	Shell string `json:"shell"`
	// GitConfig records the last-applied gitconfig.
	GitConfig map[string]string `json:"gitconfig"`
}

// NewCache returns a Cache with non-nil maps so callers can write to
// them unconditionally.
func NewCache() *Cache {
	return &Cache{
		Dotfiles:  map[string]string{},
		GitConfig: map[string]string{},
	}
}

// CachePath returns the canonical on-disk location of provisioned.json
// for the given BoltedDir. Equivalent to
// filepath.Join(stateDir, state.ProvisionedFile) but exported so
// callers can compose paths without importing internal/state directly.
func CachePath(stateDir string) string {
	return filepath.Join(stateDir, state.ProvisionedFile)
}

// LoadCache reads the cache from stateDir. A missing file returns a
// fresh, empty cache and a nil error — the first `bolt provision` ever
// run on a Bolted instance has nothing cached, and that is not a failure.
func LoadCache(stateDir string) (*Cache, error) {
	raw, err := state.ReadJSON[Cache](CachePath(stateDir))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return NewCache(), nil
		}
		return nil, fmt.Errorf("provision: load cache: %w", err)
	}
	c := &raw
	if c.Dotfiles == nil {
		c.Dotfiles = map[string]string{}
	}
	if c.GitConfig == nil {
		c.GitConfig = map[string]string{}
	}
	return c, nil
}

// SaveCache atomically writes the cache to stateDir. Mutates c by
// sorting its Features and Packages slices so the on-disk form is
// stable across runs.
func SaveCache(stateDir string, c *Cache) error {
	if c == nil {
		return fmt.Errorf("provision: SaveCache: nil cache")
	}
	sort.Strings(c.Features)
	sort.Strings(c.Packages)
	if err := state.WriteJSON(CachePath(stateDir), c); err != nil {
		return fmt.Errorf("provision: save cache: %w", err)
	}
	return nil
}

// diffStrings returns (added, removed): items in want but not in have,
// and items in have but not in want. Both slices are sorted for stable
// output.
func diffStrings(have, want []string) (added, removed []string) {
	haveSet := map[string]struct{}{}
	wantSet := map[string]struct{}{}
	for _, s := range have {
		haveSet[s] = struct{}{}
	}
	for _, s := range want {
		wantSet[s] = struct{}{}
	}
	for s := range wantSet {
		if _, ok := haveSet[s]; !ok {
			added = append(added, s)
		}
	}
	for s := range haveSet {
		if _, ok := wantSet[s]; !ok {
			removed = append(removed, s)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}
