package portforward

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/dahal/bolted/internal/backend"
	"github.com/dahal/bolted/internal/backend/mock"
	"github.com/dahal/bolted/internal/state"
)

// ---- backend fakes --------------------------------------------------------

// scriptedBackend wraps a Mock and lets a test pre-program a per-call
// Exec result + per-call ForwardPort error. ForwardPort errors are
// keyed by host port — if a port is in forwardErrPorts the call fails,
// otherwise it succeeds.
type scriptedBackend struct {
	*mock.Mock

	execResults []backend.ExecResult
	execErrs    []error
	execCalls   int

	// forwardErrPorts maps host port → error to return.
	forwardErrPorts map[int]error
	// forwardCalls records (guest, host) tuples in order.
	forwardCalls []struct{ guest, host int }

	// unforwardErrPorts maps host port → error to return.
	unforwardErrPorts map[int]error
	unforwardCalls    []int

	mu sync.Mutex
}

func (s *scriptedBackend) Exec(ctx context.Context, cmd []string, opts backend.ExecOpts) (backend.ExecResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.Mock.Exec(ctx, cmd, opts)
	idx := s.execCalls
	s.execCalls++
	var res backend.ExecResult
	if idx < len(s.execResults) {
		res = s.execResults[idx]
	}
	var err error
	if idx < len(s.execErrs) {
		err = s.execErrs[idx]
	}
	return res, err
}

func (s *scriptedBackend) ForwardPort(ctx context.Context, guest, host int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.Mock.ForwardPort(ctx, guest, host)
	s.forwardCalls = append(s.forwardCalls, struct{ guest, host int }{guest, host})
	if err, ok := s.forwardErrPorts[host]; ok {
		return err
	}
	return nil
}

func (s *scriptedBackend) UnforwardPort(ctx context.Context, host int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.Mock.UnforwardPort(ctx, host)
	s.unforwardCalls = append(s.unforwardCalls, host)
	if err, ok := s.unforwardErrPorts[host]; ok {
		return err
	}
	return nil
}

func newScripted() *scriptedBackend {
	return &scriptedBackend{
		Mock:              mock.New(),
		forwardErrPorts:   map[int]error{},
		unforwardErrPorts: map[int]error{},
	}
}

// ---- parseSsOutput --------------------------------------------------------

func TestParseSsOutput_BasicHeaderAndBindings(t *testing.T) {
	out := []byte(`State    Recv-Q Send-Q Local Address:Port Peer Address:Port Process
LISTEN   0      128    0.0.0.0:8000       0.0.0.0:*         users:(("python",pid=1,fd=3))
LISTEN   0      128    *:3000             *:*               users:(("node",pid=2,fd=5))
`)
	got := parseSsOutput(out)
	if len(got) != 2 {
		t.Fatalf("expected 2 bindings, got %v", got)
	}
	if got[0].Port != 8000 || got[0].Process != "python" {
		t.Errorf("unexpected first binding: %+v", got[0])
	}
	if got[1].Port != 3000 || got[1].Process != "node" {
		t.Errorf("unexpected second binding: %+v", got[1])
	}
}

func TestParseSsOutput_SkipsLoopback(t *testing.T) {
	out := []byte(`State    Recv-Q Send-Q Local Address:Port Peer Address:Port Process
LISTEN   0      128    127.0.0.1:6379     0.0.0.0:*         users:(("redis-server",pid=5,fd=4))
LISTEN   0      128    [::1]:5432         [::]:*            users:(("postgres",pid=6,fd=4))
LISTEN   0      128    0.0.0.0:8000       0.0.0.0:*         users:(("python",pid=1,fd=3))
`)
	got := parseSsOutput(out)
	if len(got) != 1 {
		t.Fatalf("expected loopback skipped: %v", got)
	}
	if got[0].Port != 8000 {
		t.Errorf("expected only port 8000, got %+v", got[0])
	}
}

func TestParseSsOutput_DedupesIPv4AndIPv6(t *testing.T) {
	out := []byte(`LISTEN 0 128 0.0.0.0:8080 0.0.0.0:* users:(("svc",pid=1,fd=3))
LISTEN 0 128 [::]:8080 [::]:* users:(("svc",pid=1,fd=4))
`)
	got := parseSsOutput(out)
	if len(got) != 1 {
		t.Fatalf("expected dedupe, got %v", got)
	}
	if got[0].Port != 8080 || got[0].Process != "svc" {
		t.Errorf("unexpected: %+v", got[0])
	}
}

func TestParseSsOutput_PreservesProcessFromSecondIfFirstEmpty(t *testing.T) {
	out := []byte(`LISTEN 0 128 0.0.0.0:8080 0.0.0.0:*
LISTEN 0 128 [::]:8080 [::]:* users:(("svc",pid=1,fd=4))
`)
	got := parseSsOutput(out)
	if len(got) != 1 || got[0].Process != "svc" {
		t.Errorf("expected process picked up from IPv6 row, got %+v", got)
	}
}

func TestParseSsOutput_KeepsFirstProcessWhenBothPresent(t *testing.T) {
	out := []byte(`LISTEN 0 128 0.0.0.0:8080 0.0.0.0:* users:(("alpha",pid=1,fd=3))
LISTEN 0 128 [::]:8080 [::]:* users:(("beta",pid=1,fd=4))
`)
	got := parseSsOutput(out)
	if len(got) != 1 || got[0].Process != "alpha" {
		t.Errorf("expected first process retained, got %+v", got)
	}
}

func TestParseSsOutput_SkipsHeader(t *testing.T) {
	out := []byte("State Recv-Q Send-Q Local Address:Port Peer Address:Port\n")
	if got := parseSsOutput(out); len(got) != 0 {
		t.Errorf("expected no bindings, got %v", got)
	}
}

func TestParseSsOutput_SkipsBlankAndMalformed(t *testing.T) {
	out := []byte(`
nonsense
LISTEN 0 128 garbage users:(("x",pid=1,fd=3))
LISTEN 0 128 0.0.0.0:abc users:(("x",pid=1,fd=3))
LISTEN x x 0.0.0.0:80
LISTEN 0 128 [::]bad:80
LISTEN 0 128 [:::80
LISTEN 0 128 [::]:notnum
LISTEN 0 128 0.0.0.0:80
LISTEN 0 128 noport
`)
	got := parseSsOutput(out)
	if len(got) != 1 || got[0].Port != 80 {
		t.Errorf("expected only :80 to survive parsing, got %+v", got)
	}
}

func TestParseSsOutput_TooFewFields(t *testing.T) {
	if got := parseSsOutput([]byte("LISTEN 0 128\n")); len(got) != 0 {
		t.Errorf("expected nothing, got %v", got)
	}
}

func TestParseSsOutput_WildcardAddrNotLoopback(t *testing.T) {
	out := []byte("LISTEN 0 128 *:9999 *:*\n")
	got := parseSsOutput(out)
	if len(got) != 1 || got[0].Port != 9999 {
		t.Errorf("wildcard should not be filtered, got %+v", got)
	}
}

// ---- looksNumeric / extractHostPort / isLoopback / extractProcess --------

func TestLooksNumeric(t *testing.T) {
	cases := map[string]bool{
		"":    false,
		"0":   true,
		"123": true,
		"12a": false,
		"abc": false,
	}
	for in, want := range cases {
		if got := looksNumeric(in); got != want {
			t.Errorf("looksNumeric(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestExtractHostPort(t *testing.T) {
	cases := []struct {
		in       string
		wantPort int
		wantHost string
		wantOK   bool
	}{
		{"0.0.0.0:80", 80, "0.0.0.0", true},
		{"[::]:80", 80, "::", true},
		{"[::1]:5432", 5432, "::1", true},
		{"noport", 0, "", false},
		{"[oops", 0, "", false},
		{"[::]bad", 0, "", false},
		{"[::]:notnum", 0, "", false},
		{"0.0.0.0:notnum", 0, "", false},
		{"[::]", 0, "", false},
	}
	for _, c := range cases {
		port, host, ok := extractHostPort(c.in)
		if port != c.wantPort || host != c.wantHost || ok != c.wantOK {
			t.Errorf("extractHostPort(%q) = (%d,%q,%v); want (%d,%q,%v)",
				c.in, port, host, ok, c.wantPort, c.wantHost, c.wantOK)
		}
	}
}

func TestIsLoopback(t *testing.T) {
	if !isLoopback("127.0.0.1") || !isLoopback("::1") {
		t.Error("loopback addresses not recognised")
	}
	if isLoopback("0.0.0.0") || isLoopback("") || isLoopback("*") {
		t.Error("non-loopback addresses misclassified")
	}
}

func TestExtractProcess(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`LISTEN ... users:(("python",pid=1,fd=3))`, "python"},
		{`LISTEN ... users:(()` /* malformed */, ""},
		{`LISTEN ... users:((badquote))`, ""},
		{`no users column`, ""},
		{`users:(("never-ending`, ""},
	}
	for _, c := range cases {
		if got := extractProcess(c.in); got != c.want {
			t.Errorf("extractProcess(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ---- New / portsPath ------------------------------------------------------

func TestNew_StoresArgs(t *testing.T) {
	be := newScripted()
	m := New(be, "/tmp/xyz")
	if m.b != be {
		t.Error("backend not stored")
	}
	if m.portsPath() != filepath.Join("/tmp/xyz", state.PortsFile) {
		t.Errorf("unexpected ports path: %q", m.portsPath())
	}
}

// ---- Allocate -------------------------------------------------------------

func TestAllocate_SameHostPort(t *testing.T) {
	be := newScripted()
	m := New(be, t.TempDir())
	host, remapped, err := m.Allocate(context.Background(), "api", 8000)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if host != 8000 || remapped {
		t.Errorf("got host=%d remapped=%v; want 8000/false", host, remapped)
	}
	if len(be.forwardCalls) != 1 || be.forwardCalls[0].host != 8000 {
		t.Errorf("unexpected forward calls: %+v", be.forwardCalls)
	}
}

func TestAllocate_RemapsWhenForwardFails(t *testing.T) {
	be := newScripted()
	be.forwardErrPorts[8000] = errors.New("address in use")
	m := New(be, t.TempDir())
	host, remapped, err := m.Allocate(context.Background(), "api", 8000)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if host != 8001 || !remapped {
		t.Errorf("got host=%d remapped=%v; want 8001/true", host, remapped)
	}
}

func TestAllocate_SkipsPortsClaimedInStore(t *testing.T) {
	dir := t.TempDir()
	// Pre-populate ports.json so 8000 is already taken by some other repo.
	if err := state.WriteJSON(filepath.Join(dir, state.PortsFile), map[string]any{
		"web": []any{map[string]any{"host_port": 8000, "container_port": 8000, "process": "vite"}},
	}); err != nil {
		t.Fatal(err)
	}
	be := newScripted()
	m := New(be, dir)
	host, remapped, err := m.Allocate(context.Background(), "api", 8000)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if host != 8001 || !remapped {
		t.Errorf("expected 8001 (remap), got host=%d remapped=%v", host, remapped)
	}
	// We should NOT have asked the backend to forward 8000 — the store
	// said it was taken, so we walk straight to 8001.
	for _, c := range be.forwardCalls {
		if c.host == 8000 {
			t.Errorf("expected to skip 8000, got calls %+v", be.forwardCalls)
		}
	}
}

func TestAllocate_ExhaustsWindow(t *testing.T) {
	be := newScripted()
	for p := 8000; p <= 8000+allocationWindow; p++ {
		be.forwardErrPorts[p] = errors.New("nope")
	}
	m := New(be, t.TempDir())
	_, _, err := m.Allocate(context.Background(), "api", 8000)
	if err == nil || !strings.Contains(err.Error(), "no free host port") {
		t.Errorf("expected exhaustion error, got %v", err)
	}
}

func TestAllocate_StoreReadError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, state.PortsFile), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	be := newScripted()
	m := New(be, dir)
	if _, _, err := m.Allocate(context.Background(), "api", 8000); err == nil {
		t.Fatal("expected store read error")
	}
}

// ---- DetectAndForward -----------------------------------------------------

func TestDetectAndForward_HappyPath(t *testing.T) {
	be := newScripted()
	be.execResults = []backend.ExecResult{{
		ExitCode: 0,
		Stdout: []byte(`State Recv-Q Send-Q Local Address:Port Peer Address:Port Process
LISTEN 0 128 0.0.0.0:8000 0.0.0.0:* users:(("python",pid=1,fd=3))
LISTEN 0 128 0.0.0.0:3000 0.0.0.0:* users:(("node",pid=2,fd=5))
`),
	}}
	dir := t.TempDir()
	m := New(be, dir)
	got, err := m.DetectAndForward(context.Background(), "api", "container-id")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 mappings, got %+v", got)
	}
	if got[0].HostPort != 8000 || got[0].ContainerPort != 8000 || got[0].Process != "python" {
		t.Errorf("unexpected first mapping: %+v", got[0])
	}
	// Ensure persistence happened.
	store, err := m.readStore()
	if err != nil {
		t.Fatal(err)
	}
	if len(store["api"]) != 2 {
		t.Errorf("expected api persisted twice, got %+v", store)
	}
}

func TestDetectAndForward_SsExecError(t *testing.T) {
	be := newScripted()
	be.execErrs = []error{errors.New("exec boom")}
	m := New(be, t.TempDir())
	_, err := m.DetectAndForward(context.Background(), "api", "id")
	if err == nil || !strings.Contains(err.Error(), "ss probe") {
		t.Errorf("expected exec err, got %v", err)
	}
}

func TestDetectAndForward_SsNonZeroExit(t *testing.T) {
	be := newScripted()
	be.execResults = []backend.ExecResult{{ExitCode: 1, Stderr: []byte("no perms")}}
	m := New(be, t.TempDir())
	_, err := m.DetectAndForward(context.Background(), "api", "id")
	if err == nil || !strings.Contains(err.Error(), "ss probe") {
		t.Errorf("expected exit err, got %v", err)
	}
}

func TestDetectAndForward_AllocateError(t *testing.T) {
	be := newScripted()
	be.execResults = []backend.ExecResult{{
		ExitCode: 0,
		Stdout: []byte(`LISTEN 0 128 0.0.0.0:8000 0.0.0.0:* users:(("p",pid=1,fd=3))
`),
	}}
	// Make every port in the window fail.
	for p := 8000; p <= 8000+allocationWindow; p++ {
		be.forwardErrPorts[p] = errors.New("nope")
	}
	m := New(be, t.TempDir())
	got, err := m.DetectAndForward(context.Background(), "api", "id")
	if err == nil || !strings.Contains(err.Error(), "allocate") {
		t.Errorf("expected allocate err, got %v / %+v", err, got)
	}
}

// TestDetectAndForward_PersistError tickles the persist branch by
// making the state directory read-only after the manager has already
// successfully allocated.
func TestDetectAndForward_PersistError(t *testing.T) {
	dir := t.TempDir()
	be := newScripted()
	be.execResults = []backend.ExecResult{{
		ExitCode: 0,
		Stdout:   []byte(`LISTEN 0 128 0.0.0.0:8000 0.0.0.0:* users:(("p",pid=1,fd=3))` + "\n"),
	}}
	m := New(be, dir)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	_, err := m.DetectAndForward(context.Background(), "api", "id")
	if err == nil || !strings.Contains(err.Error(), "persist mapping") {
		t.Errorf("expected persist error, got %v", err)
	}
}

// ---- Teardown -------------------------------------------------------------

func TestTeardown_RemovesMappingsAndCallsUnforward(t *testing.T) {
	dir := t.TempDir()
	be := newScripted()
	m := New(be, dir)
	if err := m.persist("api", Mapping{HostPort: 8000, ContainerPort: 8000, Process: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := m.persist("api", Mapping{HostPort: 9000, ContainerPort: 9000, Process: "y"}); err != nil {
		t.Fatal(err)
	}
	if err := m.persist("web", Mapping{HostPort: 3000, ContainerPort: 3000, Process: "z"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Teardown(context.Background(), "api"); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	sort.Ints(be.unforwardCalls)
	if len(be.unforwardCalls) != 2 || be.unforwardCalls[0] != 8000 || be.unforwardCalls[1] != 9000 {
		t.Errorf("unexpected unforward calls: %v", be.unforwardCalls)
	}
	store, _ := m.readStore()
	if _, ok := store["api"]; ok {
		t.Errorf("expected api gone, got %+v", store)
	}
	if _, ok := store["web"]; !ok {
		t.Errorf("expected web retained, got %+v", store)
	}
}

func TestTeardown_NoEntryIsNoop(t *testing.T) {
	be := newScripted()
	m := New(be, t.TempDir())
	if err := m.Teardown(context.Background(), "absent"); err != nil {
		t.Errorf("unexpected: %v", err)
	}
	if len(be.unforwardCalls) != 0 {
		t.Errorf("expected no unforward calls, got %v", be.unforwardCalls)
	}
}

func TestTeardown_StoreReadError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, state.PortsFile), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	be := newScripted()
	m := New(be, dir)
	if err := m.Teardown(context.Background(), "api"); err == nil {
		t.Fatal("expected error")
	}
}

func TestTeardown_FirstUnforwardErrorReported(t *testing.T) {
	dir := t.TempDir()
	be := newScripted()
	be.unforwardErrPorts[8000] = errors.New("unforward boom")
	m := New(be, dir)
	if err := m.persist("api", Mapping{HostPort: 8000, ContainerPort: 8000}); err != nil {
		t.Fatal(err)
	}
	if err := m.persist("api", Mapping{HostPort: 9000, ContainerPort: 9000}); err != nil {
		t.Fatal(err)
	}
	err := m.Teardown(context.Background(), "api")
	if err == nil || !strings.Contains(err.Error(), "unforward 8000") {
		t.Errorf("expected unforward err, got %v", err)
	}
	// Both ports should have been attempted regardless.
	if len(be.unforwardCalls) != 2 {
		t.Errorf("expected both attempted, got %v", be.unforwardCalls)
	}
}

func TestTeardown_WriteErrorReportedWhenUnforwardOK(t *testing.T) {
	dir := t.TempDir()
	be := newScripted()
	m := New(be, dir)
	if err := m.persist("api", Mapping{HostPort: 8000, ContainerPort: 8000}); err != nil {
		t.Fatal(err)
	}
	// Make subsequent writes fail by removing write permission on dir
	// after readStore has cached file contents.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	err := m.Teardown(context.Background(), "api")
	if err == nil {
		t.Fatal("expected write error")
	}
}

// ---- List -----------------------------------------------------------------

func TestList_EmptyWhenNoFile(t *testing.T) {
	be := newScripted()
	m := New(be, t.TempDir())
	got, err := m.List()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestList_HappyPath(t *testing.T) {
	dir := t.TempDir()
	be := newScripted()
	m := New(be, dir)
	if err := m.persist("api", Mapping{HostPort: 8001, ContainerPort: 8000, Process: "uvicorn"}); err != nil {
		t.Fatal(err)
	}
	if err := m.persist("web", Mapping{HostPort: 3000, ContainerPort: 3000, Process: "vite"}); err != nil {
		t.Fatal(err)
	}
	got, err := m.List()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(got) != 2 || len(got["api"]) != 1 || len(got["web"]) != 1 {
		t.Fatalf("unexpected shape: %+v", got)
	}
	if got["api"][0].Repo != "api" || got["api"][0].HostPort != 8001 {
		t.Errorf("unexpected api mapping: %+v", got["api"][0])
	}
}

func TestList_ReadError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, state.PortsFile), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	be := newScripted()
	m := New(be, dir)
	if _, err := m.List(); err == nil {
		t.Fatal("expected error")
	}
}

// ---- persist edge cases ---------------------------------------------------

func TestPersist_ReplacesSameHostPort(t *testing.T) {
	be := newScripted()
	m := New(be, t.TempDir())
	if err := m.persist("api", Mapping{HostPort: 8000, ContainerPort: 8000, Process: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := m.persist("api", Mapping{HostPort: 8000, ContainerPort: 8000, Process: "new"}); err != nil {
		t.Fatal(err)
	}
	store, _ := m.readStore()
	if len(store["api"]) != 1 || store["api"][0].Process != "new" {
		t.Errorf("expected single replaced entry, got %+v", store)
	}
}

func TestPersist_ReadError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, state.PortsFile), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	be := newScripted()
	m := New(be, dir)
	if err := m.persist("api", Mapping{HostPort: 8000}); err == nil {
		t.Fatal("expected error")
	}
}

// ---- readStore edge cases -------------------------------------------------

func TestReadStore_PerRepoParseError(t *testing.T) {
	dir := t.TempDir()
	// Outer JSON is valid (map[string]json.RawMessage), inner payload
	// for "api" is not a list — should bubble through the inner unmarshal.
	raw := `{"api": 42}`
	if err := os.WriteFile(filepath.Join(dir, state.PortsFile), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	be := newScripted()
	m := New(be, dir)
	if _, err := m.readStore(); err == nil || !strings.Contains(err.Error(), "parse repo") {
		t.Errorf("expected parse error, got %v", err)
	}
}

// ---- takenHostPorts -------------------------------------------------------

func TestTakenHostPorts_Aggregates(t *testing.T) {
	be := newScripted()
	m := New(be, t.TempDir())
	if err := m.persist("api", Mapping{HostPort: 8000, ContainerPort: 8000}); err != nil {
		t.Fatal(err)
	}
	if err := m.persist("api", Mapping{HostPort: 9000, ContainerPort: 9000}); err != nil {
		t.Fatal(err)
	}
	if err := m.persist("web", Mapping{HostPort: 3000, ContainerPort: 3000}); err != nil {
		t.Fatal(err)
	}
	taken, err := m.takenHostPorts()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []int{8000, 9000, 3000} {
		if !taken[p] {
			t.Errorf("expected %d taken, got %v", p, taken)
		}
	}
}

func TestTakenHostPorts_ReadError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, state.PortsFile), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	be := newScripted()
	m := New(be, dir)
	if _, err := m.takenHostPorts(); err == nil {
		t.Fatal("expected error")
	}
}

// ---- writeStore -----------------------------------------------------------

func TestWriteStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	be := newScripted()
	m := New(be, dir)
	in := map[string][]persistedEntry{
		"api": {{HostPort: 8001, ContainerPort: 8000, Process: "uvicorn"}},
	}
	if err := m.writeStore(in); err != nil {
		t.Fatal(err)
	}
	// Round-trip through the on-disk JSON.
	data, err := os.ReadFile(filepath.Join(dir, state.PortsFile))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string][]persistedEntry
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, string(data))
	}
	if len(raw["api"]) != 1 || raw["api"][0].Process != "uvicorn" {
		t.Errorf("unexpected on-disk shape: %+v", raw)
	}
}

// ---- SortedRepos ----------------------------------------------------------

func TestSortedRepos(t *testing.T) {
	got := SortedRepos(map[string][]Mapping{
		"web":   nil,
		"api":   nil,
		"infra": nil,
	})
	want := []string{"api", "infra", "web"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
}
