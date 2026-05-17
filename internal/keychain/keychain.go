// Package keychain wraps the host OS credential store with a tiny,
// platform-neutral interface. Used by `bolt` to (optionally) cache the
// Bolted passphrase between unlocks.
//
// Design notes:
//
//   - We shell out to the platform's native CLI (`security` on macOS,
//     `cmdkey` on Windows) rather than linking to a CGo binding or a
//     third-party library. The platform CLI is preinstalled, stable
//     across decades, and avoids dragging a CGo toolchain into the
//     build. It also makes the implementation trivially mockable via
//     an injectable cmd runner.
//
//   - The package is split across `keychain_darwin.go`,
//     `keychain_windows.go` and `keychain_other.go` (each guarded by
//     `//go:build` tags). `System()` returns the platform-appropriate
//     `Store`; on unsupported platforms it returns a no-op store that
//     errors with `ErrNotFound` on Get.
//
//   - The wrapper never logs the secret. It is passed to the platform
//     CLI on argv (`security`) or stdin where supported. cmdkey on
//     Windows takes the password via /pass:<value>, which exposes it to
//     other processes on the same user — that is the only mechanism
//     cmdkey supports, so we accept it. Callers who care about that
//     class of attack should leave `keychain: false` in config.
package keychain

import "errors"

// Store is the minimal credential-store surface the CLI needs. Get
// returns the stored bytes, or ErrNotFound if no entry exists for the
// given (service, account) pair. Set creates or overwrites the entry.
// Delete removes the entry; deleting a missing entry is not an error.
type Store interface {
	Get(service, account string) ([]byte, error)
	Set(service, account string, secret []byte) error
	Delete(service, account string) error
}

// ErrNotFound is returned by Store.Get when no entry exists for the
// supplied (service, account) pair. Callers should compare with
// errors.Is.
var ErrNotFound = errors.New("keychain: entry not found")
