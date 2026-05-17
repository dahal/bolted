//go:build !darwin && !windows

package keychain

// System returns a no-op Store on platforms without a supported
// credential manager (Linux, FreeBSD, …). Get always returns
// ErrNotFound so callers fall back to a prompt; Set / Delete are
// no-ops. This keeps the rest of the CLI compiling and running on
// these targets without forcing a build-tag explosion at every call
// site.
func System() Store { return noopStore{} }

// noopStore is the unsupported-platform implementation.
type noopStore struct{}

// Get always reports the entry as missing.
func (noopStore) Get(string, string) ([]byte, error) { return nil, ErrNotFound }

// Set silently accepts but stores nothing.
func (noopStore) Set(string, string, []byte) error { return nil }

// Delete silently accepts; nothing to remove.
func (noopStore) Delete(string, string) error { return nil }
