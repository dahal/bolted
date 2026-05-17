// Integration tests for the WSL2 backend. Skipped unless both of the
// following hold:
//
//   - the host OS is Windows (the only platform where wsl.exe exists);
//   - the BOLTED_E2E environment variable is set to "1".
//
// The tests additionally require a rootfs tar to import. Its location
// is read from the BOLTED_WSL_ROOTFS env var (must be an absolute
// path to a tar built by spec 07's `vm:build`). If the variable is
// unset the suite skips with a clear message.
//
// We deliberately use a per-suite distro name (bolted-e2e-<ts>) so a
// crashed run never collides with a developer's primary Bolted
// distro.

package wsl2

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dahal/bolted/internal/backend"
)

// skipUnlessE2E centralises the skip rule so each test reads cleanly.
func skipUnlessE2E(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("e2e tests require a Windows host with WSL2 installed")
	}
	if os.Getenv("BOLTED_E2E") != "1" {
		t.Skip("set BOLTED_E2E=1 to opt into wsl2 integration tests")
	}
}

// e2eRootfsPath returns the path the suite was configured to import,
// skipping the test if it's unset.
func e2eRootfsPath(t *testing.T) string {
	t.Helper()
	p := os.Getenv("BOLTED_WSL_ROOTFS")
	if p == "" {
		t.Skip("set BOLTED_WSL_ROOTFS to the absolute path of a rootfs tar")
	}
	if _, err := os.Stat(p); err != nil {
		t.Skipf("BOLTED_WSL_ROOTFS=%q is not readable: %v", p, err)
	}
	return p
}

// e2eBackend constructs a Backend with a uniquely-named distro and a
// fresh installDir under t.TempDir(). The caller is responsible for
// calling DeleteVM in a defer / cleanup.
func e2eBackend(t *testing.T) *Backend {
	t.Helper()
	name := fmt.Sprintf("bolted-e2e-%d", time.Now().UnixNano())
	return NewWithOptions(Options{
		Name:       name,
		InstallDir: t.TempDir(),
		RootfsPath: e2eRootfsPath(t),
	})
}

// TestE2E_FullLifecycle imports, starts, runs a command, stops, and
// unregisters the distro. Covers acceptance criterion 2 of spec 06.
func TestE2E_FullLifecycle(t *testing.T) {
	skipUnlessE2E(t)
	b := e2eBackend(t)
	ctx := context.Background()

	if err := b.EnsureVM(ctx, backend.VMSpec{CPUs: 2, MemoryMB: 2048, DiskGB: 20}); err != nil {
		t.Fatalf("EnsureVM: %v", err)
	}
	t.Cleanup(func() {
		_ = b.StopVM(context.Background())
		_ = b.DeleteVM(context.Background())
	})

	if err := b.StartVM(ctx); err != nil {
		t.Fatalf("StartVM: %v", err)
	}

	ok, err := b.IsRunning(ctx)
	if err != nil {
		t.Fatalf("IsRunning: %v", err)
	}
	if !ok {
		t.Fatal("IsRunning should report true after StartVM")
	}

	res, err := b.Exec(ctx, []string{"echo", "hello"}, backend.ExecOpts{})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(string(res.Stdout), "hello") {
		t.Errorf("stdout = %q, want hello", res.Stdout)
	}

	if err := b.StopVM(ctx); err != nil {
		t.Fatalf("StopVM: %v", err)
	}
}

// TestE2E_MissingRootfs verifies acceptance criterion 3: a missing
// rootfs surfaces an actionable error. We point the backend at a
// non-existent tar inside a temp dir so the test never touches a real
// distro.
func TestE2E_MissingRootfs(t *testing.T) {
	skipUnlessE2E(t)
	b := NewWithOptions(Options{
		Name:       fmt.Sprintf("bolted-e2e-missing-%d", time.Now().UnixNano()),
		InstallDir: t.TempDir(),
		RootfsPath: "/no/such/rootfs.tar",
	})
	err := b.EnsureVM(context.Background(), backend.VMSpec{})
	if err == nil {
		t.Fatal("expected error when rootfs is missing")
	}
	if !strings.Contains(err.Error(), "rootfs tar not found") {
		t.Errorf("error should mention missing rootfs, got: %v", err)
	}
}
