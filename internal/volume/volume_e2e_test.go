package volume

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/dahal/bolted/internal/backend"
	"github.com/dahal/bolted/internal/backend/factory"
)

// TestVolumeLifecycleE2E exercises the full LUKS lifecycle against a
// real backend: create → open → mount → write → unmount → close →
// re-open → re-mount → verify the file is still there.
//
// Gated behind:
//   - BOLTED_E2E=1 environment variable (opt-in only).
//   - GOOS == "linux"             (dm-crypt + cryptsetup require a
//                                 real Linux kernel; we don't expect
//                                 this to pass on macOS hosts).
//
// To run on a Linux box with cryptsetup installed:
//
//	BOLTED_E2E=1 go test -run TestVolumeLifecycleE2E \
//	    ./internal/volume/...
//
// The test uses the factory's "mock" backend's *real* sibling — well,
// strictly: on Linux we let factory.New return whatever auto-pick gives
// us. That covers the rare case where someone develops directly in the
// VM. CI on darwin / windows will simply skip.
func TestVolumeLifecycleE2E(t *testing.T) {
	if os.Getenv("BOLTED_E2E") != "1" {
		t.Skip("e2e: set BOLTED_E2E=1 to enable")
	}
	if runtime.GOOS != "linux" {
		t.Skipf("e2e: requires a Linux host with cryptsetup; got %s", runtime.GOOS)
	}

	be, err := factory.New(backend.Config{Backend: "auto"})
	if err != nil {
		t.Fatalf("factory.New: %v", err)
	}

	dir := t.TempDir()
	img := filepath.Join(dir, "volume.img")
	mountpoint := filepath.Join(dir, "mnt")
	password := []byte("e2e-passphrase-do-not-reuse")

	// We use a unique mapper name per test run so concurrent runs on
	// the same host don't collide.
	name := "bolt-e2e-" + filepath.Base(dir)
	v := New(be, Options{Name: name})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if err := v.Create(ctx, img, 32<<20, password); err != nil {
		t.Fatalf("Create: %v", err)
	}

	dev, err := v.Open(ctx, img, password)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := v.Mount(ctx, dev, mountpoint); err != nil {
		_ = v.Close(ctx, dev)
		t.Fatalf("Mount: %v", err)
	}

	hello := filepath.Join(mountpoint, "hello.txt")
	want := []byte("encrypted hello, world\n")
	if err := os.WriteFile(hello, want, 0o644); err != nil {
		_ = v.Unmount(ctx, mountpoint)
		_ = v.Close(ctx, dev)
		t.Fatalf("WriteFile: %v", err)
	}

	if err := v.Unmount(ctx, mountpoint); err != nil {
		_ = v.Close(ctx, dev)
		t.Fatalf("Unmount: %v", err)
	}
	if err := v.Close(ctx, dev); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Re-open and verify persistence.
	dev2, err := v.Open(ctx, img, password)
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer func() { _ = v.Close(ctx, dev2) }()
	if err := v.Mount(ctx, dev2, mountpoint); err != nil {
		t.Fatalf("re-Mount: %v", err)
	}
	defer func() { _ = v.Unmount(ctx, mountpoint) }()

	got, err := os.ReadFile(hello)
	if err != nil {
		t.Fatalf("ReadFile after re-open: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("payload mismatch: got %q want %q", got, want)
	}

	// Bad password rejected on re-open.
	if _, err := v.Open(ctx, img, []byte("definitely-wrong")); err == nil {
		t.Fatal("Open with wrong password should fail")
	}
}
