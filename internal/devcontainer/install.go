package devcontainer

import (
	"context"
	"fmt"

	"github.com/dahal/bolted/internal/backend"
)

// ensureCLI makes sure the devcontainer CLI is available inside the
// VM. The work happens at most once per *runner instance — a
// successful probe is cached in installOnce; a failure is cached too,
// so retrying Up after a failed install returns the same actionable
// error rather than re-paying the install cost on every attempt.
//
// Strategy:
//
//  1. `which devcontainer` — exit 0 means we're done.
//  2. `which npm` — if npm is also missing, return
//     ErrDevcontainerMissing wrapped with a `bolt provision` hint.
//  3. `npm install -g @devcontainers/cli` — best-effort install. A
//     non-zero exit is reported with stderr folded in so the user
//     sees the real npm complaint (permissions, registry, etc.).
func (r *runner) ensureCLI(ctx context.Context) error {
	r.installOnce.Do(func() {
		r.installErr = r.doEnsureCLI(ctx)
	})
	return r.installErr
}

// doEnsureCLI implements the actual probe/install logic. Split out
// from ensureCLI so the sync.Once wrapper stays tiny and so the
// installable steps are testable as a single sequence.
func (r *runner) doEnsureCLI(ctx context.Context) error {
	if r.have(ctx, "devcontainer") {
		return nil
	}
	if !r.have(ctx, "npm") {
		return fmt.Errorf("%w: npm is not available either — run `bolt provision` to install it", ErrDevcontainerMissing)
	}
	res, err := r.backend.Exec(ctx, []string{
		"npm", "install", "-g", "@devcontainers/cli",
	}, backend.ExecOpts{})
	if err != nil || res.ExitCode != 0 {
		return fmt.Errorf("%w: %s", ErrDevcontainerMissing, installFailureDetail(res, err))
	}
	return nil
}

// have returns true if `which <tool>` exits zero inside the VM. A
// backend-level error is treated as "missing" so the caller falls
// through to the install-or-error path — a real VM failure will
// resurface there with a useful error.
func (r *runner) have(ctx context.Context, tool string) bool {
	res, err := r.backend.Exec(ctx, []string{"which", tool}, backend.ExecOpts{})
	if err != nil {
		return false
	}
	return res.ExitCode == 0
}

// installFailureDetail formats the install-failure suffix. Kept
// separate so ensureCLI stays readable and so we can unit-test the
// formatting branches directly.
func installFailureDetail(res backend.ExecResult, err error) string {
	if err != nil {
		return errMsg(err)
	}
	if len(res.Stderr) > 0 {
		return fmt.Sprintf("npm install failed (exit %d): %s", res.ExitCode, res.Stderr)
	}
	return fmt.Sprintf("npm install failed (exit %d)", res.ExitCode)
}

// errMsg is a tiny indirection so a nil error doesn't panic the
// installFailureDetail caller. Kept private; the callers already
// guard against nil but defence-in-depth is cheap.
func errMsg(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
