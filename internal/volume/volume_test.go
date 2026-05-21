package volume

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/dahal/bolted/internal/backend"
	"github.com/dahal/bolted/internal/backend/mock"
)

// scriptedBackend is a test backend that lets each test enqueue a
// sequence of ExecResult / error pairs to be returned in order by
// Exec. The mock package's recorder is fine for "all calls succeed
// the same way", but we need per-call control for the Create sequence
// and for failure-injection tests, so we layer this on top of it.
//
// The backend.Backend methods that volume.Volume does not call are
// stubbed out to satisfy the interface; tests don't touch them.
type scriptedBackend struct {
	mu sync.Mutex
	// calls records each Exec invocation in order. The Stdin reader
	// is drained immediately so tests can assert on the bytes.
	calls []recordedExec
	// queue is the FIFO of canned responses. An empty queue causes
	// Exec to return (zero ExecResult, nil) — same as mock.Mock.
	queue []scriptedResp
}

type recordedExec struct {
	cmd   []string
	stdin []byte
	opts  backend.ExecOpts
}

type scriptedResp struct {
	res backend.ExecResult
	err error
}

func (s *scriptedBackend) push(r scriptedResp) { s.queue = append(s.queue, r) }

func (s *scriptedBackend) ok()         { s.push(scriptedResp{}) }
func (s *scriptedBackend) okN(n int)   { for i := 0; i < n; i++ { s.ok() } }

func (s *scriptedBackend) Exec(_ context.Context, cmd []string, opts backend.ExecOpts) (backend.ExecResult, error) {
	var stdin []byte
	if opts.Stdin != nil {
		b, _ := io.ReadAll(opts.Stdin)
		stdin = b
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, recordedExec{cmd: append([]string(nil), cmd...), stdin: stdin, opts: opts})
	if len(s.queue) == 0 {
		return backend.ExecResult{}, nil
	}
	r := s.queue[0]
	s.queue = s.queue[1:]
	return r.res, r.err
}

// Unused stubs so scriptedBackend satisfies backend.Backend.
func (s *scriptedBackend) Preflight(context.Context) error                { return nil }
func (s *scriptedBackend) EnsureVM(context.Context, backend.VMSpec) error { return nil }
func (s *scriptedBackend) StartVM(context.Context) error                  { return nil }
func (s *scriptedBackend) StopVM(context.Context) error                   { return nil }
func (s *scriptedBackend) IsRunning(context.Context) (bool, error)        { return false, nil }
func (s *scriptedBackend) ForwardPort(context.Context, int, int) error    { return nil }
func (s *scriptedBackend) UnforwardPort(context.Context, int) error       { return nil }
func (s *scriptedBackend) DeleteVM(context.Context) error                 { return nil }

var _ backend.Backend = (*scriptedBackend)(nil)

// pw is shorthand for a non-empty password buffer in tests.
func pw(s string) []byte { return []byte(s) }

// cmdsOf flattens recorded calls to "first-arg" identifiers so tests
// can assert ordering without re-typing the full argv each time.
func cmdsOf(calls []recordedExec) []string {
	out := make([]string, len(calls))
	for i, c := range calls {
		out[i] = c.cmd[0]
	}
	return out
}

func TestNewDefaultsName(t *testing.T) {
	v := New(mock.New(), Options{})
	if v.Name() != "bolted" {
		t.Fatalf("default name: got %q want %q", v.Name(), "bolted")
	}
}

func TestNewOverridesName(t *testing.T) {
	v := New(mock.New(), Options{Name: "custom"})
	if v.Name() != "custom" {
		t.Fatalf("override name: got %q", v.Name())
	}
}

func TestDevicePath(t *testing.T) {
	if got := Device("foo").Path(); got != "/dev/mapper/foo" {
		t.Fatalf("Device.Path: got %q", got)
	}
}

func TestCreateSequence(t *testing.T) {
	be := &scriptedBackend{}
	be.okN(6) // mkdir, truncate, luksFormat, open, mkfs.ext4, close
	v := New(be, Options{})

	if err := v.Create(context.Background(), "/var/img.luks", 1<<30, pw("hunter2")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	want := []string{"mkdir", "truncate", "cryptsetup", "cryptsetup", "mkfs.ext4", "cryptsetup"}
	if got := cmdsOf(be.calls); !reflect.DeepEqual(got, want) {
		t.Fatalf("Create call order: got %v want %v", got, want)
	}

	// mkdir must create the parent dir of imagePath.
	mkdir := be.calls[0]
	if !reflect.DeepEqual(mkdir.cmd, []string{"mkdir", "-p", "/var"}) {
		t.Fatalf("mkdir argv unexpected: %v", mkdir.cmd)
	}

	// luksFormat: argv must NOT contain the password; stdin MUST.
	luksFormat := be.calls[2]
	if !reflect.DeepEqual(luksFormat.cmd, []string{
		"cryptsetup", "luksFormat",
		"--type", "luks2",
		"--pbkdf", "argon2id",
		"--batch-mode",
		"--key-file=-",
		"/var/img.luks",
	}) {
		t.Fatalf("luksFormat argv unexpected: %v", luksFormat.cmd)
	}
	if string(luksFormat.stdin) != "hunter2" {
		t.Fatalf("luksFormat stdin: got %q want %q", luksFormat.stdin, "hunter2")
	}
	for _, a := range luksFormat.cmd {
		if strings.Contains(a, "hunter2") {
			t.Fatalf("password leaked into luksFormat argv: %v", luksFormat.cmd)
		}
	}

	// open inside Create: also password on stdin.
	open := be.calls[3]
	if open.cmd[1] != "open" || string(open.stdin) != "hunter2" {
		t.Fatalf("Create.open call wrong: cmd=%v stdin=%q", open.cmd, open.stdin)
	}

	// mkfs.ext4 must target the mapper path.
	mkfs := be.calls[4]
	if !reflect.DeepEqual(mkfs.cmd, []string{"mkfs.ext4", "-q", "/dev/mapper/bolted"}) {
		t.Fatalf("mkfs.ext4 argv unexpected: %v", mkfs.cmd)
	}

	// close: no password on stdin (it's a no-key operation).
	closeCall := be.calls[5]
	if !reflect.DeepEqual(closeCall.cmd, []string{"cryptsetup", "close", "bolted"}) {
		t.Fatalf("close argv unexpected: %v", closeCall.cmd)
	}
	if closeCall.stdin != nil {
		t.Fatalf("close stdin should be nil, got %q", closeCall.stdin)
	}
}

func TestCreateValidation(t *testing.T) {
	v := New(&scriptedBackend{}, Options{})
	cases := []struct {
		name string
		path string
		size int64
		pw   []byte
		want string
	}{
		{"empty path", "", 1, pw("p"), "imagePath is empty"},
		{"zero size", "/img", 0, pw("p"), "sizeBytes must be positive"},
		{"negative size", "/img", -1, pw("p"), "sizeBytes must be positive"},
		{"empty password", "/img", 1, nil, "password is empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := v.Create(context.Background(), tc.path, tc.size, tc.pw)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestCreateTruncateFailure(t *testing.T) {
	be := &scriptedBackend{}
	be.push(scriptedResp{res: backend.ExecResult{Stderr: []byte("no space"), ExitCode: 1}})
	v := New(be, Options{})
	err := v.Create(context.Background(), "/img", 1, pw("p"))
	if err == nil || !strings.Contains(err.Error(), "truncate") {
		t.Fatalf("expected truncate failure, got %v", err)
	}
}

func TestCreateLuksFormatFailure(t *testing.T) {
	be := &scriptedBackend{}
	be.ok() // truncate
	be.push(scriptedResp{err: errors.New("backend dead")})
	v := New(be, Options{})
	err := v.Create(context.Background(), "/img", 1, pw("p"))
	if err == nil || !strings.Contains(err.Error(), "luksFormat") {
		t.Fatalf("expected luksFormat failure, got %v", err)
	}
}

func TestCreateOpenFailure(t *testing.T) {
	be := &scriptedBackend{}
	be.ok() // truncate
	be.ok() // luksFormat
	be.push(scriptedResp{res: backend.ExecResult{ExitCode: 1, Stderr: []byte("device busy")}})
	v := New(be, Options{})
	err := v.Create(context.Background(), "/img", 1, pw("p"))
	if err == nil || !strings.Contains(err.Error(), "open") {
		t.Fatalf("expected open failure, got %v", err)
	}
}

func TestCreateMkfsFailureTriggersClose(t *testing.T) {
	be := &scriptedBackend{}
	be.ok()                                                                          // truncate
	be.ok()                                                                          // luksFormat
	be.ok()                                                                          // open
	be.push(scriptedResp{res: backend.ExecResult{ExitCode: 1, Stderr: []byte("bad")}}) // mkfs.ext4
	be.ok()                                                                          // best-effort close
	v := New(be, Options{})
	err := v.Create(context.Background(), "/img", 1, pw("p"))
	if err == nil || !strings.Contains(err.Error(), "mkfs.ext4") {
		t.Fatalf("expected mkfs.ext4 failure, got %v", err)
	}
	if len(be.calls) != 5 {
		t.Fatalf("expected 5 calls (truncate, luksFormat, open, mkfs, close), got %d", len(be.calls))
	}
	if be.calls[4].cmd[1] != "close" {
		t.Fatalf("expected tear-down close, got %v", be.calls[4].cmd)
	}
}

// TestCreateOpenBadPassword exercises the execWithPassword bad-password
// branch via Create. A legitimate Create flow won't trigger this (we
// just minted the key), but if cryptsetup ever reports it for some
// reason the call still needs to surface ErrBadPassword cleanly so
// the error machinery is consistent across every helper.
func TestCreateOpenBadPassword(t *testing.T) {
	be := &scriptedBackend{}
	be.ok() // truncate
	be.ok() // luksFormat
	be.push(scriptedResp{res: backend.ExecResult{ExitCode: 2, Stderr: []byte("No key available with this passphrase")}})
	v := New(be, Options{})
	err := v.Create(context.Background(), "/img", 1, pw("p"))
	if !errors.Is(err, ErrBadPassword) {
		t.Fatalf("want ErrBadPassword, got %v", err)
	}
}

func TestCreateFinalCloseFailure(t *testing.T) {
	be := &scriptedBackend{}
	be.ok() // truncate
	be.ok() // luksFormat
	be.ok() // open
	be.ok() // mkfs.ext4
	be.push(scriptedResp{res: backend.ExecResult{ExitCode: 1, Stderr: []byte("stuck")}})
	v := New(be, Options{})
	err := v.Create(context.Background(), "/img", 1, pw("p"))
	if err == nil || !strings.Contains(err.Error(), "close") {
		t.Fatalf("expected close failure, got %v", err)
	}
}

func TestOpenSuccess(t *testing.T) {
	be := &scriptedBackend{}
	be.ok()
	v := New(be, Options{Name: "bolt"})
	dev, err := v.Open(context.Background(), "/img", pw("hunter2"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if dev != "bolt" {
		t.Fatalf("Open: got device %q want %q", dev, "bolt")
	}
	if len(be.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(be.calls))
	}
	call := be.calls[0]
	if !reflect.DeepEqual(call.cmd, []string{
		"cryptsetup", "open", "--key-file=-", "/img", "bolt",
	}) {
		t.Fatalf("Open argv unexpected: %v", call.cmd)
	}
	if string(call.stdin) != "hunter2" {
		t.Fatalf("Open stdin: got %q", call.stdin)
	}
}

func TestOpenBadPasswordExitCode(t *testing.T) {
	be := &scriptedBackend{}
	be.push(scriptedResp{res: backend.ExecResult{ExitCode: 2, Stderr: []byte("anything")}})
	v := New(be, Options{})
	_, err := v.Open(context.Background(), "/img", pw("wrong"))
	if !errors.Is(err, ErrBadPassword) {
		t.Fatalf("want errors.Is(err, ErrBadPassword), got %v", err)
	}
}

func TestOpenBadPasswordByStderr(t *testing.T) {
	cases := []string{
		"No key available with this passphrase.",
		"No usable keyslot is available.",
		"No permission to access this passphrase slot.",
	}
	for _, msg := range cases {
		t.Run(msg, func(t *testing.T) {
			be := &scriptedBackend{}
			be.push(scriptedResp{res: backend.ExecResult{ExitCode: 5, Stderr: []byte(msg)}})
			v := New(be, Options{})
			_, err := v.Open(context.Background(), "/img", pw("wrong"))
			if !errors.Is(err, ErrBadPassword) {
				t.Fatalf("want ErrBadPassword for stderr %q, got %v", msg, err)
			}
		})
	}
}

func TestOpenGenericFailure(t *testing.T) {
	be := &scriptedBackend{}
	be.push(scriptedResp{res: backend.ExecResult{ExitCode: 3, Stderr: []byte("device missing")}})
	v := New(be, Options{})
	_, err := v.Open(context.Background(), "/img", pw("ok"))
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, ErrBadPassword) {
		t.Fatalf("did not expect ErrBadPassword, got %v", err)
	}
	if !strings.Contains(err.Error(), "device missing") {
		t.Fatalf("want stderr folded in, got %v", err)
	}
}

func TestOpenBackendError(t *testing.T) {
	be := &scriptedBackend{}
	be.push(scriptedResp{err: errors.New("vm down")})
	v := New(be, Options{})
	_, err := v.Open(context.Background(), "/img", pw("ok"))
	if err == nil || !strings.Contains(err.Error(), "vm down") {
		t.Fatalf("want backend error wrapped, got %v", err)
	}
}

func TestOpenValidation(t *testing.T) {
	v := New(&scriptedBackend{}, Options{})
	if _, err := v.Open(context.Background(), "", pw("p")); err == nil {
		t.Fatal("expected empty-path error")
	}
	if _, err := v.Open(context.Background(), "/img", nil); err == nil {
		t.Fatal("expected empty-password error")
	}
}

func TestMountCreatesDirBeforeMount(t *testing.T) {
	be := &scriptedBackend{}
	be.okN(2)
	v := New(be, Options{})
	if err := v.Mount(context.Background(), Device("bolted"), "/mnt/bolt"); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if len(be.calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(be.calls))
	}
	if !reflect.DeepEqual(be.calls[0].cmd, []string{"mkdir", "-p", "/mnt/bolt"}) {
		t.Fatalf("first call should be mkdir -p, got %v", be.calls[0].cmd)
	}
	if !reflect.DeepEqual(be.calls[1].cmd, []string{"mount", "/dev/mapper/bolted", "/mnt/bolt"}) {
		t.Fatalf("second call should be mount, got %v", be.calls[1].cmd)
	}
}

func TestMountMkdirFailure(t *testing.T) {
	be := &scriptedBackend{}
	be.push(scriptedResp{res: backend.ExecResult{ExitCode: 1, Stderr: []byte("perm")}})
	v := New(be, Options{})
	if err := v.Mount(context.Background(), Device("bolt"), "/mnt"); err == nil {
		t.Fatal("expected mkdir failure")
	}
}

func TestMountMountFailure(t *testing.T) {
	be := &scriptedBackend{}
	be.ok() // mkdir
	be.push(scriptedResp{res: backend.ExecResult{ExitCode: 32, Stderr: []byte("already mounted")}})
	v := New(be, Options{})
	err := v.Mount(context.Background(), Device("bolt"), "/mnt")
	if err == nil || !strings.Contains(err.Error(), "mount") {
		t.Fatalf("expected mount failure, got %v", err)
	}
}

func TestMountValidation(t *testing.T) {
	v := New(&scriptedBackend{}, Options{})
	if err := v.Mount(context.Background(), "", "/mnt"); err == nil {
		t.Fatal("expected empty-device error")
	}
	if err := v.Mount(context.Background(), Device("bolt"), ""); err == nil {
		t.Fatal("expected empty-mountpoint error")
	}
}

func TestUnmountSuccess(t *testing.T) {
	be := &scriptedBackend{}
	be.ok()
	v := New(be, Options{})
	if err := v.Unmount(context.Background(), "/mnt/bolt"); err != nil {
		t.Fatalf("Unmount: %v", err)
	}
	if !reflect.DeepEqual(be.calls[0].cmd, []string{"umount", "/mnt/bolt"}) {
		t.Fatalf("got %v", be.calls[0].cmd)
	}
}

func TestUnmountIdempotentNotMounted(t *testing.T) {
	cases := []string{
		"umount: /mnt: not mounted.",
		"umount: /mnt: not currently mounted.",
	}
	for _, msg := range cases {
		t.Run(msg, func(t *testing.T) {
			be := &scriptedBackend{}
			be.push(scriptedResp{res: backend.ExecResult{ExitCode: 32, Stderr: []byte(msg)}})
			v := New(be, Options{})
			if err := v.Unmount(context.Background(), "/mnt"); err != nil {
				t.Fatalf("Unmount should be idempotent on %q, got %v", msg, err)
			}
		})
	}
}

func TestUnmountFailure(t *testing.T) {
	be := &scriptedBackend{}
	be.push(scriptedResp{res: backend.ExecResult{ExitCode: 1, Stderr: []byte("device busy")}})
	v := New(be, Options{})
	err := v.Unmount(context.Background(), "/mnt")
	if err == nil || !strings.Contains(err.Error(), "device busy") {
		t.Fatalf("want failure with stderr folded, got %v", err)
	}
}

func TestUnmountBackendError(t *testing.T) {
	be := &scriptedBackend{}
	be.push(scriptedResp{err: errors.New("vm gone")})
	v := New(be, Options{})
	err := v.Unmount(context.Background(), "/mnt")
	if err == nil || !strings.Contains(err.Error(), "vm gone") {
		t.Fatalf("want backend error wrapped, got %v", err)
	}
}

func TestUnmountValidation(t *testing.T) {
	v := New(&scriptedBackend{}, Options{})
	if err := v.Unmount(context.Background(), ""); err == nil {
		t.Fatal("expected empty-mountpoint error")
	}
}

func TestCloseSuccess(t *testing.T) {
	be := &scriptedBackend{}
	be.ok()
	v := New(be, Options{})
	if err := v.Close(context.Background(), Device("bolted")); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !reflect.DeepEqual(be.calls[0].cmd, []string{"cryptsetup", "close", "bolted"}) {
		t.Fatalf("got %v", be.calls[0].cmd)
	}
}

func TestCloseFailure(t *testing.T) {
	be := &scriptedBackend{}
	be.push(scriptedResp{res: backend.ExecResult{ExitCode: 1, Stderr: []byte("in use")}})
	v := New(be, Options{})
	err := v.Close(context.Background(), Device("bolt"))
	if err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("want failure, got %v", err)
	}
}

func TestCloseValidation(t *testing.T) {
	v := New(&scriptedBackend{}, Options{})
	if err := v.Close(context.Background(), ""); err == nil {
		t.Fatal("expected empty-device error")
	}
}

func TestAddKeyslotSuccess(t *testing.T) {
	be := &scriptedBackend{}
	be.ok()
	v := New(be, Options{})
	if err := v.AddKeyslot(context.Background(), "/img", pw("old-pass"), pw("new")); err != nil {
		t.Fatalf("AddKeyslot: %v", err)
	}
	call := be.calls[0]
	wantCmd := []string{
		"cryptsetup", "luksAddKey",
		"--pbkdf", "argon2id",
		"--batch-mode",
		"--key-file=-",
		"--keyfile-size", "8",
		"/img",
	}
	if !reflect.DeepEqual(call.cmd, wantCmd) {
		t.Fatalf("AddKeyslot argv: got %v want %v", call.cmd, wantCmd)
	}
	if string(call.stdin) != "old-passnew" {
		t.Fatalf("AddKeyslot stdin: got %q want %q", call.stdin, "old-passnew")
	}
	for _, a := range call.cmd {
		if strings.Contains(a, "old-pass") || strings.Contains(a, "new") && a != "--keyfile-size" {
			// `--keyfile-size` is the only argv token containing "new"
			// substrings legitimately (e.g. "8"). We just want to confirm
			// the passwords themselves never appear.
			if a == "old-pass" || a == "new" {
				t.Fatalf("password leaked into argv: %v", call.cmd)
			}
		}
	}
}

func TestAddKeyslotBadPassword(t *testing.T) {
	be := &scriptedBackend{}
	be.push(scriptedResp{res: backend.ExecResult{ExitCode: 2, Stderr: []byte("No key available with this passphrase")}})
	v := New(be, Options{})
	err := v.AddKeyslot(context.Background(), "/img", pw("wrong"), pw("new"))
	if !errors.Is(err, ErrBadPassword) {
		t.Fatalf("want ErrBadPassword, got %v", err)
	}
}

func TestAddKeyslotGenericFailure(t *testing.T) {
	be := &scriptedBackend{}
	be.push(scriptedResp{res: backend.ExecResult{ExitCode: 4, Stderr: []byte("io error")}})
	v := New(be, Options{})
	err := v.AddKeyslot(context.Background(), "/img", pw("ok"), pw("new"))
	if err == nil || errors.Is(err, ErrBadPassword) {
		t.Fatalf("want generic failure, got %v", err)
	}
	if !strings.Contains(err.Error(), "io error") {
		t.Fatalf("want stderr folded, got %v", err)
	}
}

func TestAddKeyslotValidation(t *testing.T) {
	v := New(&scriptedBackend{}, Options{})
	cases := []struct {
		name, path string
		existing, new []byte
	}{
		{"empty path", "", pw("a"), pw("b")},
		{"empty existing", "/img", nil, pw("b")},
		{"empty new", "/img", pw("a"), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := v.AddKeyslot(context.Background(), tc.path, tc.existing, tc.new); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestRemoveKeyslotSuccess(t *testing.T) {
	be := &scriptedBackend{}
	be.ok()
	v := New(be, Options{})
	if err := v.RemoveKeyslot(context.Background(), "/img", pw("hunter2")); err != nil {
		t.Fatalf("RemoveKeyslot: %v", err)
	}
	call := be.calls[0]
	wantCmd := []string{"cryptsetup", "luksRemoveKey", "--batch-mode", "--key-file=-", "/img"}
	if !reflect.DeepEqual(call.cmd, wantCmd) {
		t.Fatalf("RemoveKeyslot argv: got %v", call.cmd)
	}
	if string(call.stdin) != "hunter2" {
		t.Fatalf("RemoveKeyslot stdin: got %q", call.stdin)
	}
}

func TestRemoveKeyslotBadPassword(t *testing.T) {
	be := &scriptedBackend{}
	be.push(scriptedResp{res: backend.ExecResult{ExitCode: 2, Stderr: []byte("No key available")}})
	v := New(be, Options{})
	err := v.RemoveKeyslot(context.Background(), "/img", pw("wrong"))
	if !errors.Is(err, ErrBadPassword) {
		t.Fatalf("want ErrBadPassword, got %v", err)
	}
}

func TestRemoveKeyslotGenericFailure(t *testing.T) {
	be := &scriptedBackend{}
	be.push(scriptedResp{res: backend.ExecResult{ExitCode: 4, Stderr: []byte("io error")}})
	v := New(be, Options{})
	err := v.RemoveKeyslot(context.Background(), "/img", pw("ok"))
	if err == nil || errors.Is(err, ErrBadPassword) {
		t.Fatalf("want generic failure, got %v", err)
	}
	if !strings.Contains(err.Error(), "io error") {
		t.Fatalf("want stderr folded, got %v", err)
	}
}

func TestRemoveKeyslotValidation(t *testing.T) {
	v := New(&scriptedBackend{}, Options{})
	if err := v.RemoveKeyslot(context.Background(), "", pw("p")); err == nil {
		t.Fatal("expected empty-path error")
	}
	if err := v.RemoveKeyslot(context.Background(), "/img", nil); err == nil {
		t.Fatal("expected empty-password error")
	}
}

// TestMockBackendInteroperability is a sanity check that the package
// works with the project's recorded backend.Backend mock (as the spec
// requires), not just the scripted variant we use for richer tests.
func TestMockBackendInteroperability(t *testing.T) {
	m := mock.New()
	v := New(m, Options{})
	dev, err := v.Open(context.Background(), "/img", pw("hunter2"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if dev != "bolted"{
		t.Fatalf("device: got %q", dev)
	}
	if len(m.Calls) != 1 || m.Calls[0].Method != "Exec" {
		t.Fatalf("expected one Exec call on mock, got %+v", m.Calls)
	}
	if m.Calls[0].ExecOpts.Stdin == nil {
		t.Fatal("expected mock to receive password via Stdin")
	}
}

// TestWrapExecAllBranches exercises every formatting branch of
// wrapExec so the helper hits 100% coverage even on inputs the rest
// of the suite doesn't naturally produce.
func TestWrapExecAllBranches(t *testing.T) {
	cases := []struct {
		name  string
		res   backend.ExecResult
		err   error
		want  string
	}{
		{"err+stderr", backend.ExecResult{Stderr: []byte(" boom ")}, errors.New("orig"), "volume: op: orig: boom"},
		{"err only", backend.ExecResult{}, errors.New("orig"), "volume: op: orig"},
		{"stderr only", backend.ExecResult{ExitCode: 7, Stderr: []byte("nope")}, nil, "volume: op: exit 7: nope"},
		{"nothing", backend.ExecResult{ExitCode: 9}, nil, "volume: op: exit 9"},
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

// TestBuildPasswordStdinDirectly covers the helper directly; the
// keyslot tests exercise it through AddKeyslot but a direct test
// pins down the wire format independently.
func TestBuildPasswordStdinDirectly(t *testing.T) {
	r := buildPasswordStdin([]byte("old"), []byte("new"))
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(b) != "oldnew" {
		t.Fatalf("got %q want %q", b, "oldnew")
	}
}
