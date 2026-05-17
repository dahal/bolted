package wsl2

import (
	"context"
	"strings"
	"testing"

	"github.com/dahal/bolted/internal/backend"
)

// TestBackend_ImplementsBackend pins that *Backend satisfies the
// interface even while it's still a stub. Spec 06 must not regress this.
func TestBackend_ImplementsBackend(t *testing.T) {
	var _ backend.Backend = New()
}

// TestBackend_AllMethodsReturnNotImplemented exercises every stub method
// so coverage stays honest: a stub that silently returns nil would be a
// nasty foot-gun for the spec-06 implementer.
func TestBackend_AllMethodsReturnNotImplemented(t *testing.T) {
	b := New()
	ctx := context.Background()

	checks := []struct {
		name string
		run  func() error
	}{
		{"EnsureVM", func() error { return b.EnsureVM(ctx, backend.VMSpec{}) }},
		{"StartVM", func() error { return b.StartVM(ctx) }},
		{"StopVM", func() error { return b.StopVM(ctx) }},
		{"IsRunning", func() error {
			ok, err := b.IsRunning(ctx)
			if ok {
				t.Error("IsRunning should report false from the stub")
			}
			return err
		}},
		{"Exec", func() error {
			res, err := b.Exec(ctx, []string{"echo"}, backend.ExecOpts{})
			if res.ExitCode != -1 {
				t.Errorf("Exec stub should report ExitCode=-1, got %d", res.ExitCode)
			}
			return err
		}},
		{"ForwardPort", func() error { return b.ForwardPort(ctx, 1, 2) }},
		{"UnforwardPort", func() error { return b.UnforwardPort(ctx, 2) }},
		{"DeleteVM", func() error { return b.DeleteVM(ctx) }},
	}

	for _, c := range checks {
		err := c.run()
		if err == nil {
			t.Errorf("%s: expected not-implemented error, got nil", c.name)
			continue
		}
		if !strings.Contains(err.Error(), "spec 06") {
			t.Errorf("%s: expected error to reference spec 06, got: %v", c.name, err)
		}
		if !strings.Contains(err.Error(), "wsl2") {
			t.Errorf("%s: expected error to mention wsl2, got: %v", c.name, err)
		}
	}
}
