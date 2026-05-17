// Package lima — end-to-end tests.
//
// These tests exercise the REAL limactl binary against a REAL Lima VM
// on the host. They are gated on the BOLTED_E2E=1 environment
// variable so the default `go test ./...` run on CI or a developer
// laptop without Lima stays green.
//
// Prerequisites (skip the test otherwise):
//
//   - macOS host
//   - `brew install lima` (Lima 1.0+)
//   - BOLTED_E2E=1 in the environment
//
// The tests create a throwaway instance under a temp dataDir and tear
// it down on cleanup; they should leave no Lima state behind on a
// successful run. A failed run may leave a half-configured instance
// — use `limactl delete --force bolted-e2e` to clean up.
package lima

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dahal/bolted/internal/backend"
)

// e2eVMName is a dedicated name so the tests never collide with a
// developer's real "bolted" instance.
const e2eVMName = "bolted-e2e"

// skipUnlessE2E returns early unless BOLTED_E2E=1.
func skipUnlessE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("BOLTED_E2E") != "1" {
		t.Skip("set BOLTED_E2E=1 to run Lima end-to-end tests (requires `brew install lima`)")
	}
}

// TestE2E_LifecycleEchoStopDelete drives the full create -> start ->
// echo -> stop -> delete cycle. Run time is multi-minute because
// Lima downloads the Alpine image on first boot.
func TestE2E_LifecycleEchoStopDelete(t *testing.T) {
	skipUnlessE2E(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	b := NewWithOptions(Options{Name: e2eVMName, DataDir: t.TempDir()})
	t.Cleanup(func() {
		// Best-effort teardown if a sub-step fails mid-test.
		_ = b.DeleteVM(context.Background())
	})
	spec := backend.VMSpec{CPUs: 2, MemoryMB: 2048, DiskGB: 20}
	if err := b.EnsureVM(ctx, spec); err != nil {
		t.Fatalf("EnsureVM: %v", err)
	}
	if err := b.StartVM(ctx); err != nil {
		t.Fatalf("StartVM: %v", err)
	}
	running, err := b.IsRunning(ctx)
	if err != nil || !running {
		t.Fatalf("IsRunning: running=%v err=%v", running, err)
	}
	res, err := b.Exec(ctx, []string{"echo", "hello"}, backend.ExecOpts{})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(string(res.Stdout), "hello") {
		t.Errorf("unexpected stdout: %q", res.Stdout)
	}
	if err := b.StopVM(ctx); err != nil {
		t.Fatalf("StopVM: %v", err)
	}
	if err := b.DeleteVM(ctx); err != nil {
		t.Fatalf("DeleteVM: %v", err)
	}
}
