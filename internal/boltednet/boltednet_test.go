package boltednet

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/dahal/bolted/internal/backend"
	"github.com/dahal/bolted/internal/backend/mock"
)

// scriptedBackend is a per-call-controllable backend.Backend. Each
// Exec pops the next scriptedResp off the queue; an empty queue yields
// the zero value (matches mock.Mock's default behaviour). Mirrors the
// helper used by the devcontainer package so callers familiar with one
// can read the other without context-switching.
type scriptedBackend struct {
	mu    sync.Mutex
	calls []recordedExec
	queue []scriptedResp
}

type recordedExec struct {
	cmd  []string
	opts backend.ExecOpts
}

type scriptedResp struct {
	res backend.ExecResult
	err error
}

func (s *scriptedBackend) push(r scriptedResp)          { s.queue = append(s.queue, r) }
func (s *scriptedBackend) ok()                          { s.push(scriptedResp{}) }
func (s *scriptedBackend) okStdout(stdout string)       { s.push(scriptedResp{res: backend.ExecResult{Stdout: []byte(stdout)}}) }
func (s *scriptedBackend) fail(exit int, stderr string) { s.push(scriptedResp{res: backend.ExecResult{ExitCode: exit, Stderr: []byte(stderr)}}) }
func (s *scriptedBackend) failErr(err error)            { s.push(scriptedResp{err: err}) }

func (s *scriptedBackend) Exec(_ context.Context, cmd []string, opts backend.ExecOpts) (backend.ExecResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, recordedExec{cmd: append([]string(nil), cmd...), opts: opts})
	if len(s.queue) == 0 {
		return backend.ExecResult{}, nil
	}
	r := s.queue[0]
	s.queue = s.queue[1:]
	return r.res, r.err
}

// Unused stubs so scriptedBackend satisfies backend.Backend.
func (s *scriptedBackend) EnsureVM(context.Context, backend.VMSpec) error { return nil }
func (s *scriptedBackend) StartVM(context.Context) error                  { return nil }
func (s *scriptedBackend) StopVM(context.Context) error                   { return nil }
func (s *scriptedBackend) IsRunning(context.Context) (bool, error)        { return false, nil }
func (s *scriptedBackend) ForwardPort(context.Context, int, int) error    { return nil }
func (s *scriptedBackend) UnforwardPort(context.Context, int) error       { return nil }
func (s *scriptedBackend) DeleteVM(context.Context) error                 { return nil }

var _ backend.Backend = (*scriptedBackend)(nil)

// firstCmd flattens recorded calls to the leading argv token for
// concise sequence assertions.
func firstCmd(calls []recordedExec) []string {
	out := make([]string, len(calls))
	for i, c := range calls {
		out[i] = strings.Join(c.cmd, " ")
	}
	return out
}

// TestNetworkNameConstant guards the spec contract: the literal must
// stay `bolted-net` because it appears in user-facing docs and is
// the hostname suffix devs rely on.
func TestNetworkNameConstant(t *testing.T) {
	if NetworkName != "bolted-net" {
		t.Fatalf("NetworkName: got %q want %q", NetworkName, "bolted-net")
	}
}

// TestExistsPresent verifies a positive match: when `podman network ls`
// includes our name (alongside other networks), Exists returns true.
func TestExistsPresent(t *testing.T) {
	be := &scriptedBackend{}
	be.okStdout("podman\nbridge\nbolted-net\n")

	present, err := Exists(context.Background(), be)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !present {
		t.Fatal("expected Exists=true, got false")
	}
	wantCmd := []string{"podman", "network", "ls", "--format", "{{.Name}}"}
	if !reflect.DeepEqual(be.calls[0].cmd, wantCmd) {
		t.Fatalf("ls argv: got %v want %v", be.calls[0].cmd, wantCmd)
	}
}

// TestExistsAbsent verifies Exists returns false when the network is
// not in the listing.
func TestExistsAbsent(t *testing.T) {
	be := &scriptedBackend{}
	be.okStdout("podman\nbridge\n")
	present, err := Exists(context.Background(), be)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if present {
		t.Fatal("expected Exists=false, got true")
	}
}

// TestExistsBackendError surfaces the err != nil branch of the ls
// probe.
func TestExistsBackendError(t *testing.T) {
	be := &scriptedBackend{}
	be.failErr(errors.New("vm gone"))
	_, err := Exists(context.Background(), be)
	if err == nil || !strings.Contains(err.Error(), "vm gone") {
		t.Fatalf("want backend error wrapped, got %v", err)
	}
}

// TestExistsNonZeroExit surfaces the exit != 0 branch of the ls probe
// (e.g. podman not yet ready).
func TestExistsNonZeroExit(t *testing.T) {
	be := &scriptedBackend{}
	be.fail(1, "podman: command not found")
	_, err := Exists(context.Background(), be)
	if err == nil || !strings.Contains(err.Error(), "command not found") {
		t.Fatalf("want exit-failure wrapped, got %v", err)
	}
}

// TestEnsureAlreadyPresent verifies Ensure is a no-op when the
// network already exists — only the ls probe runs, no create.
func TestEnsureAlreadyPresent(t *testing.T) {
	be := &scriptedBackend{}
	be.okStdout("bolted-net\n")
	if err := Ensure(context.Background(), be); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(be.calls) != 1 {
		t.Fatalf("expected only the ls probe, got %d calls: %v", len(be.calls), firstCmd(be.calls))
	}
}

// TestEnsureCreatesWhenMissing covers the create path: ls reports
// missing, then podman network create runs.
func TestEnsureCreatesWhenMissing(t *testing.T) {
	be := &scriptedBackend{}
	be.okStdout("podman\n") // ls: bolted-net not present
	be.ok()                 // create succeeds
	if err := Ensure(context.Background(), be); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(be.calls) != 2 {
		t.Fatalf("expected ls+create, got %d calls: %v", len(be.calls), firstCmd(be.calls))
	}
	wantCreate := []string{"podman", "network", "create", "bolted-net"}
	if !reflect.DeepEqual(be.calls[1].cmd, wantCreate) {
		t.Fatalf("create argv: got %v want %v", be.calls[1].cmd, wantCreate)
	}
}

// TestEnsureExistsProbeError surfaces the early-return branch when the
// Exists probe itself fails.
func TestEnsureExistsProbeError(t *testing.T) {
	be := &scriptedBackend{}
	be.failErr(errors.New("ls broken"))
	err := Ensure(context.Background(), be)
	if err == nil || !strings.Contains(err.Error(), "ls broken") {
		t.Fatalf("want probe error surfaced, got %v", err)
	}
}

// TestEnsureCreateFailure surfaces a non-zero exit from
// `podman network create` (e.g. a duplicate-name race or denied perms).
func TestEnsureCreateFailure(t *testing.T) {
	be := &scriptedBackend{}
	be.okStdout("")                                   // ls: empty -> missing
	be.fail(125, "network bolted-net already used") // create fails
	err := Ensure(context.Background(), be)
	if err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("want create failure wrapped, got %v", err)
	}
}

// TestEnsureCreateBackendError surfaces the err != nil branch of the
// create call (separate from the non-zero-exit branch).
func TestEnsureCreateBackendError(t *testing.T) {
	be := &scriptedBackend{}
	be.okStdout("") // ls: missing
	be.failErr(errors.New("vm crashed mid-create"))
	err := Ensure(context.Background(), be)
	if err == nil || !strings.Contains(err.Error(), "vm crashed") {
		t.Fatalf("want create backend error surfaced, got %v", err)
	}
}

// TestDeleteSuccess covers the happy path of removing the network.
func TestDeleteSuccess(t *testing.T) {
	be := &scriptedBackend{}
	be.ok()
	if err := Delete(context.Background(), be); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	wantRm := []string{"podman", "network", "rm", "bolted-net"}
	if !reflect.DeepEqual(be.calls[0].cmd, wantRm) {
		t.Fatalf("rm argv: got %v want %v", be.calls[0].cmd, wantRm)
	}
}

// TestDeleteFailure surfaces a non-zero exit from `podman network rm`.
func TestDeleteFailure(t *testing.T) {
	be := &scriptedBackend{}
	be.fail(1, "network in use")
	err := Delete(context.Background(), be)
	if err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("want delete failure wrapped, got %v", err)
	}
}

// TestDeleteBackendError surfaces the err != nil branch of the rm
// call.
func TestDeleteBackendError(t *testing.T) {
	be := &scriptedBackend{}
	be.failErr(errors.New("vm offline"))
	err := Delete(context.Background(), be)
	if err == nil || !strings.Contains(err.Error(), "vm offline") {
		t.Fatalf("want rm backend error surfaced, got %v", err)
	}
}

// TestWrapExecAllBranches pins every formatting branch of wrapExec so
// every statement in the switch is exercised.
func TestWrapExecAllBranches(t *testing.T) {
	cases := []struct {
		name string
		res  backend.ExecResult
		err  error
		want string
	}{
		{"err+stderr", backend.ExecResult{Stderr: []byte(" boom ")}, errors.New("orig"), "boltednet: op: orig: boom"},
		{"err only", backend.ExecResult{}, errors.New("orig"), "boltednet: op: orig"},
		{"stderr only", backend.ExecResult{ExitCode: 7, Stderr: []byte("nope")}, nil, "boltednet: op: exit 7: nope"},
		{"nothing", backend.ExecResult{ExitCode: 9}, nil, "boltednet: op: exit 9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wrapExec("op", tc.res, tc.err).Error()
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

// TestMockBackendInteroperability is a sanity check that the package
// also works against the recorded mock in internal/backend/mock —
// matches the convention used by sibling packages.
func TestMockBackendInteroperability(t *testing.T) {
	m := mock.New()
	// Ls returns empty stdout → network missing → Ensure attempts create.
	if err := Ensure(context.Background(), m); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(m.Calls) != 2 {
		t.Fatalf("expected ls+create on mock, got %v", m.Methods())
	}
	if m.Calls[0].Method != "Exec" || m.Calls[1].Method != "Exec" {
		t.Fatalf("expected both Exec, got %v", m.Methods())
	}
	wantCreate := []string{"podman", "network", "create", NetworkName}
	if !reflect.DeepEqual(m.Calls[1].Cmd, wantCreate) {
		t.Fatalf("create argv: got %v", m.Calls[1].Cmd)
	}
}
