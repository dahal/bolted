// Package boltednet manages the shared podman bridge network that
// every Bolted dev container joins so repos can reach each other by
// name (e.g. `curl http://bolted-api:8000/` from another container).
//
// # Why a dedicated network
//
// Podman's default bridge does not provide built-in name resolution
// across containers. A user-defined bridge does — netavark / the dnsname
// plugin auto-populates an internal DNS so `bolted-<repo>` resolves
// to the container's IP. We create exactly one such network per VM
// (`bolted-net`) and attach every `bolt dev` container to it on Up.
//
// # Boundary
//
// This package owns ONLY the network lifecycle. The post-Up step that
// connects a specific container to the network lives in the
// devcontainer package (`devcontainer.attachToNetwork`) because the
// devcontainer CLI doesn't expose a `--network` flag — see spec 19.
//
// # Integration point (wired by parent task, NOT here)
//
// `internal/cli/dev_cmd.go` is expected to:
//
//  1. Call `boltednet.Ensure(ctx, backend)` before `devcontainer.Up`.
//  2. Pass `devcontainer.UpOpts{NetworkName: boltednet.NetworkName}`
//     so the post-Up attach runs.
//
// Spec 19 explicitly DEFERS that wiring; only the building blocks live
// here.
package boltednet

import (
	"context"
	"fmt"
	"strings"

	"github.com/dahal/bolted/internal/backend"
)

// NetworkName is the canonical podman network name shared by every
// Bolted dev container. Exported so callers can pass it into
// devcontainer.UpOpts without re-declaring the literal.
const NetworkName = "bolted-net"

// Ensure creates the bolted-net podman network inside the VM if it
// does not already exist. Idempotent: a no-op when the network is
// already present.
//
// Sequence:
//
//  1. `podman network ls --format '{{.Name}}'` — list existing nets.
//  2. If our name appears, return nil.
//  3. Otherwise `podman network create bolted-net`.
//
// Any backend-level error or non-zero exit on either step is wrapped
// with the operation name so callers can route the underlying cause.
func Ensure(ctx context.Context, b backend.Backend) error {
	present, err := Exists(ctx, b)
	if err != nil {
		return err
	}
	if present {
		return nil
	}
	res, err := b.Exec(ctx, []string{
		"podman", "network", "create", NetworkName,
	}, backend.ExecOpts{})
	if err != nil || res.ExitCode != 0 {
		return wrapExec("network create", res, err)
	}
	return nil
}

// Exists reports whether the bolted-net podman network is present
// inside the VM. Uses the same `podman network ls` probe Ensure does
// but never attempts creation.
func Exists(ctx context.Context, b backend.Backend) (bool, error) {
	res, err := b.Exec(ctx, []string{
		"podman", "network", "ls", "--format", "{{.Name}}",
	}, backend.ExecOpts{})
	if err != nil || res.ExitCode != 0 {
		return false, wrapExec("network ls", res, err)
	}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		if strings.TrimSpace(line) == NetworkName {
			return true, nil
		}
	}
	return false, nil
}

// Delete removes the bolted-net podman network inside the VM. Used
// for teardown / cleanup flows; NOT wired into the normal user path.
// A non-zero exit is surfaced as an error so callers can distinguish a
// genuine cleanup failure from a missing-network case (use Exists
// first if you need that distinction).
func Delete(ctx context.Context, b backend.Backend) error {
	res, err := b.Exec(ctx, []string{
		"podman", "network", "rm", NetworkName,
	}, backend.ExecOpts{})
	if err != nil || res.ExitCode != 0 {
		return wrapExec("network rm", res, err)
	}
	return nil
}

// wrapExec folds an Exec failure (backend-level error or non-zero
// exit) into a single error tagged with the operation name. Mirrors
// the shape used in internal/devcontainer and internal/volume for
// consistent error formatting across the codebase.
func wrapExec(opName string, res backend.ExecResult, err error) error {
	stderr := strings.TrimSpace(string(res.Stderr))
	switch {
	case err != nil && stderr != "":
		return fmt.Errorf("boltednet: %s: %w: %s", opName, err, stderr)
	case err != nil:
		return fmt.Errorf("boltednet: %s: %w", opName, err)
	case stderr != "":
		return fmt.Errorf("boltednet: %s: exit %d: %s", opName, res.ExitCode, stderr)
	default:
		return fmt.Errorf("boltednet: %s: exit %d", opName, res.ExitCode)
	}
}
