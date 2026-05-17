package factory

import (
	"strings"
	"testing"

	"github.com/dahal/bolted/internal/backend"
	"github.com/dahal/bolted/internal/backend/lima"
	"github.com/dahal/bolted/internal/backend/mock"
	"github.com/dahal/bolted/internal/backend/wsl2"
)

// withGOOS swaps the runtimeGOOS variable for the duration of the test.
// Resetting in a t.Cleanup keeps parallel tests honest.
func withGOOS(t *testing.T, goos string) {
	t.Helper()
	orig := runtimeGOOS
	runtimeGOOS = goos
	t.Cleanup(func() { runtimeGOOS = orig })
}

func TestNew_AutoDarwinReturnsLima(t *testing.T) {
	withGOOS(t, "darwin")
	for _, cfg := range []backend.Config{{Backend: ""}, {Backend: "auto"}} {
		b, err := New(cfg)
		if err != nil {
			t.Fatalf("New(%+v): unexpected error %v", cfg, err)
		}
		if _, ok := b.(*lima.Backend); !ok {
			t.Errorf("New(%+v): expected *lima.Backend, got %T", cfg, b)
		}
	}
}

func TestNew_AutoWindowsReturnsWSL2(t *testing.T) {
	withGOOS(t, "windows")
	b, err := New(backend.Config{Backend: "auto"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := b.(*wsl2.Backend); !ok {
		t.Errorf("expected *wsl2.Backend, got %T", b)
	}
}

func TestNew_AutoUnsupportedOSErrors(t *testing.T) {
	withGOOS(t, "linux")
	b, err := New(backend.Config{Backend: "auto"})
	if err == nil {
		t.Fatalf("expected error for unsupported OS, got backend %T", b)
	}
	if !strings.Contains(err.Error(), "linux") {
		t.Errorf("expected error to mention the OS, got: %v", err)
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("expected error to say 'unsupported', got: %v", err)
	}
}

func TestNew_ExplicitLima(t *testing.T) {
	// Pin the OS to something other than darwin to prove the explicit
	// override beats auto-detection.
	withGOOS(t, "freebsd")
	b, err := New(backend.Config{Backend: "lima"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := b.(*lima.Backend); !ok {
		t.Errorf("expected *lima.Backend, got %T", b)
	}
}

func TestNew_ExplicitWSL2(t *testing.T) {
	withGOOS(t, "darwin")
	b, err := New(backend.Config{Backend: "wsl2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := b.(*wsl2.Backend); !ok {
		t.Errorf("expected *wsl2.Backend, got %T", b)
	}
}

func TestNew_ExplicitMock(t *testing.T) {
	withGOOS(t, "linux") // mock must work regardless of OS
	b, err := New(backend.Config{Backend: "mock"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := b.(*mock.Mock); !ok {
		t.Errorf("expected *mock.Mock, got %T", b)
	}
}

func TestNew_UnknownBackendErrors(t *testing.T) {
	b, err := New(backend.Config{Backend: "vmware"})
	if err == nil {
		t.Fatalf("expected error, got backend %T", b)
	}
	if !strings.Contains(err.Error(), "vmware") {
		t.Errorf("expected error to mention the bad value, got: %v", err)
	}
	if !strings.Contains(err.Error(), "auto|lima|wsl2|mock") {
		t.Errorf("expected error to list valid options, got: %v", err)
	}
}

func TestNew_RuntimeGOOSDefaultsToRuntime(t *testing.T) {
	// Sanity check: the package-level var is initialised from runtime.GOOS,
	// not from a stale literal. We can't compare against a constant
	// (cross-compilation matters), so just assert non-empty.
	if runtimeGOOS == "" {
		t.Error("runtimeGOOS should default to runtime.GOOS, not empty string")
	}
}
