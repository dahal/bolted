// Package factory wires the Backend interface to its concrete
// implementations. It lives in a sub-package (rather than alongside the
// interface in internal/backend) so that lima / wsl2 / mock can each
// import internal/backend without creating an import cycle: the factory
// is the only place that knows about all three.
//
// Callers typically import this package once at program startup and pass
// the resulting Backend down. Tests can either use the mock backend
// directly (internal/backend/mock) or call New with Config{Backend:
// "mock"}.
package factory

import (
	"fmt"
	"runtime"

	"github.com/dahal/bolted/internal/backend"
	"github.com/dahal/bolted/internal/backend/lima"
	"github.com/dahal/bolted/internal/backend/mock"
	"github.com/dahal/bolted/internal/backend/wsl2"
)

// runtimeGOOS is the indirection point for OS detection. Tests overwrite
// it to exercise every selection branch on whatever platform the suite
// happens to run on.
var runtimeGOOS = runtime.GOOS

// New returns the Backend implementation chosen by cfg. The selection is:
//
//   - cfg.Backend == "" or "auto":  Lima on darwin, WSL2 on windows,
//     error on any other OS.
//   - cfg.Backend == "lima":        always Lima, regardless of host OS.
//   - cfg.Backend == "wsl2":        always WSL2, regardless of host OS.
//   - cfg.Backend == "mock":        the recording mock from
//     internal/backend/mock.
//
// Any other value returns an error listing the accepted options.
func New(cfg backend.Config) (backend.Backend, error) {
	switch cfg.Backend {
	case "", "auto":
		return newAuto()
	case "lima":
		return lima.New(), nil
	case "wsl2":
		return wsl2.New(), nil
	case "mock":
		return mock.New(), nil
	default:
		return nil, fmt.Errorf(
			"backend: unknown value %q (want auto|lima|wsl2|mock)",
			cfg.Backend,
		)
	}
}

// newAuto picks an implementation based on runtimeGOOS.
func newAuto() (backend.Backend, error) {
	switch runtimeGOOS {
	case "darwin":
		return lima.New(), nil
	case "windows":
		return wsl2.New(), nil
	default:
		return nil, fmt.Errorf(
			"backend: unsupported OS %q (Bolted currently runs on darwin and windows; Linux is post-MVP)",
			runtimeGOOS,
		)
	}
}
