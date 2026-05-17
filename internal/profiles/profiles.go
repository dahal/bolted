// Package profiles vendors a handful of opinionated `bolted.yaml`
// starter templates so `bolt init --profile <name>` can drop a sensible
// configuration into ~/.bolted/bolted.yaml without the user
// authoring yaml from scratch.
//
// The yaml documents live under internal/profiles/files/ and are
// compiled into the binary via go:embed (see embed.go). Each profile
// has a one-line human description in descriptions.go.
//
// API surface kept intentionally tiny:
//
//   - Names()            — sorted list of available profile names.
//   - Description(name)  — one-line summary, "" if unknown.
//   - Get(name)          — raw yaml bytes ready to write to disk.
//   - List()             — convenience: [{name, description}, ...] for
//                          callers that want to render a table.
//
// The package has no runtime dependencies; everything it needs is
// already inside the binary at compile time.
package profiles

import (
	"fmt"
	"io/fs"
	"sort"
)

// readEmbeddedFn is an indirection so tests can drive the
// "should-be-impossible" read failure branch of Get. Production
// callers should never reassign it.
var readEmbeddedFn fs.ReadFileFS = profilesFS

// Entry is one row in the List() return value: a profile name paired
// with its short human-readable description. Tagged for JSON so
// `bolt profiles --json` can encode it directly.
type Entry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Names returns the profile names in stable sorted order. The set is
// driven by descriptions (see descriptions.go) — a profile without a
// description is invisible here.
func Names() []string {
	out := make([]string, 0, len(descriptions))
	for name := range descriptions {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Description returns the one-line summary for the named profile, or
// "" if no such profile exists. Callers that need an error-on-unknown
// should use Get instead.
func Description(name string) string {
	return descriptions[name]
}

// List returns every profile as a sorted slice of {name, description}
// entries. Designed for the `bolt profiles` listing.
func List() []Entry {
	names := Names()
	out := make([]Entry, 0, len(names))
	for _, name := range names {
		out = append(out, Entry{Name: name, Description: descriptions[name]})
	}
	return out
}

// Get returns the raw bytes of the embedded bolted.yaml for the
// named profile, ready to write to ~/.bolted/bolted.yaml. An
// unknown name returns a friendly error listing the available choices.
func Get(name string) ([]byte, error) {
	if _, ok := descriptions[name]; !ok {
		return nil, fmt.Errorf("profiles: unknown profile %q (available: %v)", name, Names())
	}
	path := "files/" + name + ".yaml"
	data, err := readEmbeddedFn.ReadFile(path)
	if err != nil {
		// Should be impossible in production — names are validated
		// against descriptions, and every described profile ships
		// a yaml. Tests use the indirection point to drive this
		// branch.
		return nil, fmt.Errorf("profiles: read embedded %s: %w", path, err)
	}
	return data, nil
}
