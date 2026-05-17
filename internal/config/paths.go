// Package config owns ~/.bolted/config.yaml: the on-disk schema, the
// defaults, the loader, the saver, and the validation rules.
//
// The package is the single source of truth for the BoltedDir layout
// (paths.go), the user-visible YAML schema (config.go), and the units and
// path-normalisation rules every other package relies on (validate.go).
//
// All other packages should depend on this one rather than re-deriving any of
// the above.
package config

import (
	"os"
	"path/filepath"
)

// boltedHomeEnv is the env var that, when set and non-empty, overrides the
// default BoltedDir location. Documented in BoltedDir.
const boltedHomeEnv = "BOLTED_HOME"

// userHomeDir is an indirection point so tests can substitute a fake
// implementation when HOME is missing or unreadable.
var userHomeDir = os.UserHomeDir

// BoltedDir returns the absolute on-disk root for Bolted state:
//
//   - If the BOLTED_HOME environment variable is set and non-empty, its
//     value is returned verbatim (callers are expected to pass an absolute
//     path; we do not expand or normalise it here).
//   - Otherwise the conventional <home>/.bolted is returned, where <home>
//     comes from os.UserHomeDir.
//
// If the home directory cannot be determined and BOLTED_HOME is unset,
// BoltedDir falls back to ".bolted" (a relative path) so callers always
// get a non-empty string. Production code should detect a missing HOME via
// other means; this fallback exists only so the function has a total
// signature.
func BoltedDir() string {
	if v := os.Getenv(boltedHomeEnv); v != "" {
		return v
	}
	home, err := userHomeDir()
	if err != nil || home == "" {
		return ".bolted"
	}
	return filepath.Join(home, ".bolted")
}
