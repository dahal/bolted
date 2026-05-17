// Package devcontainertrust implements the first-use approval gate for a
// repo's .devcontainer/devcontainer.json (spec 18). Before a container is
// brought up the caller computes a sha256 of the merged config, checks it
// against ~/.bolted/state/devcontainer-trust.json, and — on a miss — shows
// the user a summary and asks for explicit y/N confirmation.
//
// INTEGRATION POINT: this package is intentionally NOT wired into the
// lifecycle commands yet. Wire as the FIRST step of `bolt dev` / `bolt exec`
// runner.Up — see internal/cli/dev_cmd.go's runDev() function. The hash check
// must happen before any `devcontainer up` call so a hostile config never
// runs. The matching `--trust` flag on `bolt dev`/`bolt exec` should call
// Store.Approve directly to skip the interactive prompt for CI use.
package devcontainertrust

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/dahal/bolted/internal/state"
)

// ErrNoConfig is returned by HashConfig when the repo has no
// .devcontainer/devcontainer.json file. Callers can treat the absence of a
// devcontainer as "nothing to gate" — there is no untrusted code path
// because no devcontainer config is consumed.
var ErrNoConfig = errors.New("devcontainertrust: no devcontainer.json")

// ErrNoTTY is returned by Confirm when the supplied reader is not an
// interactive terminal. The caller should surface a clear "use --trust to
// approve non-interactively" diagnostic.
var ErrNoTTY = errors.New("devcontainertrust: stdin is not a terminal")

// Store is a thin wrapper around devcontainer-trust.json. The on-disk
// schema is a flat map of repo basename → hex sha256 of the approved
// devcontainer.json. Writes are atomic via internal/state.
type Store struct {
	// stateDir is the directory holding devcontainer-trust.json. Typically
	// ~/.bolted/state/.
	stateDir string
}

// NewStore returns a Store backed by stateDir/devcontainer-trust.json.
// The file does not need to exist; an absent file is treated as an empty
// approval set.
func NewStore(stateDir string) *Store {
	return &Store{stateDir: stateDir}
}

// path returns the full path to devcontainer-trust.json.
func (s *Store) path() string {
	return filepath.Join(s.stateDir, state.DevcontainerTrustFile)
}

// load reads the on-disk map, treating a missing file as empty.
func (s *Store) load() (map[string]string, error) {
	raw, err := state.ReadJSON[map[string]any](s.path())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		// Tolerate hand-edited files with non-string entries by
		// skipping them rather than failing wholesale.
		if hex, ok := v.(string); ok {
			out[k] = hex
		}
	}
	return out, nil
}

// save atomically replaces devcontainer-trust.json with m.
func (s *Store) save(m map[string]string) error {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return state.WriteJSON(s.path(), out)
}

// Approved reports whether the given (repo, hash) pair matches a previously
// approved entry. Both arguments must match: a different hash for the same
// repo (i.e. devcontainer.json was edited) returns false so the caller can
// re-prompt.
func (s *Store) Approved(repo, hash string) bool {
	m, err := s.load()
	if err != nil {
		return false
	}
	stored, ok := m[repo]
	if !ok {
		return false
	}
	return stored == hash
}

// Approve records hash as the approved sha256 for repo, replacing any
// existing entry. The write is atomic.
func (s *Store) Approve(repo, hash string) error {
	if repo == "" {
		return fmt.Errorf("devcontainertrust: approve: empty repo")
	}
	m, err := s.load()
	if err != nil {
		return err
	}
	m[repo] = hash
	return s.save(m)
}

// Revoke clears any recorded approval for repo. Revoking an unknown repo is
// a no-op (the user's intent — "make sure it isn't trusted" — is satisfied
// either way).
func (s *Store) Revoke(repo string) error {
	m, err := s.load()
	if err != nil {
		return err
	}
	if _, ok := m[repo]; !ok {
		return nil
	}
	delete(m, repo)
	return s.save(m)
}
