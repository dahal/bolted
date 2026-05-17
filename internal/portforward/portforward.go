// Package portforward owns the per-repo TCP port-forwarding lifecycle for
// running dev containers.
//
// The Manager is intentionally small and stateless beyond ports.json: it
// detects in-container listeners, allocates a host port (preferring the
// container's number, falling back to the next free port in a 100-port
// window if the host already has it), asks the backend to install the
// forward, and persists the mapping to ~/.bolted/state/ports.json via
// internal/state.
//
// Why a Manager rather than free functions: the multi-step Detect →
// Allocate → ForwardPort → persist sequence needs a shared state-dir and
// Backend handle, and a struct makes it natural to test the parts
// independently (parseSsOutput, Allocate, the persistence helpers).
//
// Integration with `bolt dev` — once spec 14's PR merges, dev_cmd.go's
// success path (after runner.Up returns a container id) should call
// Manager.DetectAndForward(ctx, repo, id). On `bolt stop <repo>` (and on
// `bolt lock`), the lifecycle should call Manager.Teardown(repo). The
// central wiring is deliberately out of scope here so this package stays
// importable without modifying dev_cmd.go.
package portforward

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/dahal/bolted/internal/backend"
	"github.com/dahal/bolted/internal/state"
)

// allocationWindow is the max number of ports we'll probe past the
// container's requested port before giving up. Spec 14 § Allocation rule
// pins this at 100.
const allocationWindow = 100

// Mapping is one persisted port-forward record. It is the type the CLI
// renders in `bolt ports` and the on-disk shape stored in ports.json
// (Repo is implicit from the map key).
type Mapping struct {
	// Repo is the Bolted repo this mapping belongs to. Populated by
	// List(); on-disk the repo lives in the parent map key.
	Repo string `json:"-"`
	// HostPort is the port allocated on the host (potentially remapped).
	HostPort int `json:"host_port"`
	// ContainerPort is the port the in-container process is listening on.
	ContainerPort int `json:"container_port"`
	// Process is the best-effort process name (parsed from `ss -tlnp`).
	Process string `json:"process"`
}

// ContainerBinding is one entry parsed out of `ss -tlnp`. Internal —
// callers see Mapping after Allocate/DetectAndForward runs.
type ContainerBinding struct {
	// Port is the listening TCP port.
	Port int
	// Process is the best-effort process name.
	Process string
}

// Manager orchestrates port detection, allocation, forwarding, and
// teardown for one Bolted instance.
type Manager struct {
	b        backend.Backend
	stateDir string
}

// New returns a Manager backed by b (used for in-container ss probes and
// for ForwardPort / UnforwardPort) and persisting to stateDir/ports.json.
func New(b backend.Backend, stateDir string) *Manager {
	return &Manager{b: b, stateDir: stateDir}
}

// DetectAndForward inspects the named container for listening TCP ports
// (via `ss -tlnp`), allocates a host port for each, asks the backend to
// install the forward, and persists the result to ports.json. The
// returned mappings are in the order detected. Any per-binding error is
// returned (the partial work is still persisted) so the caller can
// surface a friendly message.
//
// containerID is unused by the current implementation: detection runs
// against the backend with Exec — the central wiring will swap this for
// `podman exec <id>` once the dev command integrates the Manager. The
// argument is part of the public API up-front so the integration patch
// is a one-liner.
func (m *Manager) DetectAndForward(ctx context.Context, repo, containerID string) ([]Mapping, error) {
	_ = containerID // reserved for the dev_cmd.go integration; see doc comment.
	res, err := m.b.Exec(ctx, []string{"ss", "-tlnp"}, backend.ExecOpts{Cwd: "/"})
	if err != nil {
		return nil, fmt.Errorf("ss probe: %w", err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("ss probe: exit %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	bindings := parseSsOutput(res.Stdout)
	var out []Mapping
	for _, bind := range bindings {
		hostPort, _, allocErr := m.Allocate(ctx, repo, bind.Port)
		if allocErr != nil {
			return out, fmt.Errorf("allocate %d: %w", bind.Port, allocErr)
		}
		mapping := Mapping{
			Repo:          repo,
			HostPort:      hostPort,
			ContainerPort: bind.Port,
			Process:       bind.Process,
		}
		if err := m.persist(repo, mapping); err != nil {
			return out, fmt.Errorf("persist mapping: %w", err)
		}
		out = append(out, mapping)
	}
	return out, nil
}

// Allocate picks a host port for containerPort and asks the backend to
// install the forward. It first tries containerPort itself; on conflict
// (already in ports.json or backend.ForwardPort returns any error) it
// walks forward through [containerPort+1, containerPort+allocationWindow]
// and returns the first port that takes. remapped reports whether the
// returned host port differs from containerPort.
func (m *Manager) Allocate(ctx context.Context, repo string, containerPort int) (int, bool, error) {
	taken, err := m.takenHostPorts()
	if err != nil {
		return 0, false, err
	}
	tryPort := func(p int) bool {
		if taken[p] {
			return false
		}
		if err := m.b.ForwardPort(ctx, containerPort, p); err != nil {
			return false
		}
		return true
	}
	if tryPort(containerPort) {
		return containerPort, false, nil
	}
	for p := containerPort + 1; p <= containerPort+allocationWindow; p++ {
		if tryPort(p) {
			return p, true, nil
		}
	}
	return 0, false, fmt.Errorf("no free host port in [%d, %d]", containerPort+1, containerPort+allocationWindow)
}

// Teardown removes every forward registered to repo: it calls
// UnforwardPort for each host port (best-effort) and rewrites ports.json
// without the repo's entry. The first UnforwardPort error is returned
// after attempting the rest so a single flake doesn't leave half the
// state behind.
func (m *Manager) Teardown(ctx context.Context, repo string) error {
	store, err := m.readStore()
	if err != nil {
		return err
	}
	entries, ok := store[repo]
	if !ok {
		return nil
	}
	var firstErr error
	for _, e := range entries {
		if err := m.b.UnforwardPort(ctx, e.HostPort); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("unforward %d: %w", e.HostPort, err)
		}
	}
	delete(store, repo)
	if err := m.writeStore(store); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// List returns the persisted mappings grouped by repo. Each Mapping has
// Repo populated. A missing ports.json returns an empty map and no
// error: the file simply hasn't been created yet.
func (m *Manager) List() (map[string][]Mapping, error) {
	store, err := m.readStore()
	if err != nil {
		return nil, err
	}
	out := make(map[string][]Mapping, len(store))
	for repo, entries := range store {
		cp := make([]Mapping, len(entries))
		for i, e := range entries {
			cp[i] = Mapping{
				Repo:          repo,
				HostPort:      e.HostPort,
				ContainerPort: e.ContainerPort,
				Process:       e.Process,
			}
		}
		out[repo] = cp
	}
	return out, nil
}

// --- ports.json helpers ----------------------------------------------------

// persistedEntry is the on-disk shape for one record inside the per-repo
// list in ports.json. Mapping.Repo is implicit (parent key) so we keep
// it off the JSON.
type persistedEntry struct {
	HostPort      int    `json:"host_port"`
	ContainerPort int    `json:"container_port"`
	Process       string `json:"process"`
}

// portsPath returns the absolute path to ports.json under the manager's
// state directory.
func (m *Manager) portsPath() string {
	return filepath.Join(m.stateDir, state.PortsFile)
}

// readStore loads ports.json. A missing file is treated as an empty
// store, matching the convention used by containers.json in dev_cmd.go.
func (m *Manager) readStore() (map[string][]persistedEntry, error) {
	raw, err := state.ReadJSON[map[string]json.RawMessage](m.portsPath())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string][]persistedEntry{}, nil
		}
		return nil, err
	}
	out := make(map[string][]persistedEntry, len(raw))
	for repo, payload := range raw {
		var entries []persistedEntry
		if err := json.Unmarshal(payload, &entries); err != nil {
			return nil, fmt.Errorf("portforward: parse repo %q in ports.json: %w", repo, err)
		}
		out[repo] = entries
	}
	return out, nil
}

// writeStore round-trips through map[string]any so the on-disk shape
// matches what state.WriteJSON produces for other files.
func (m *Manager) writeStore(store map[string][]persistedEntry) error {
	payload := make(map[string]any, len(store))
	for k, v := range store {
		payload[k] = v
	}
	return state.WriteJSON(m.portsPath(), payload)
}

// persist appends one mapping into the per-repo list in ports.json.
// Existing entries with the same host port for the same repo are
// replaced rather than duplicated.
func (m *Manager) persist(repo string, mapping Mapping) error {
	store, err := m.readStore()
	if err != nil {
		return err
	}
	entries := store[repo]
	replaced := false
	for i, e := range entries {
		if e.HostPort == mapping.HostPort {
			entries[i] = persistedEntry{
				HostPort:      mapping.HostPort,
				ContainerPort: mapping.ContainerPort,
				Process:       mapping.Process,
			}
			replaced = true
			break
		}
	}
	if !replaced {
		entries = append(entries, persistedEntry{
			HostPort:      mapping.HostPort,
			ContainerPort: mapping.ContainerPort,
			Process:       mapping.Process,
		})
	}
	store[repo] = entries
	return m.writeStore(store)
}

// takenHostPorts returns the set of host ports already claimed by any
// repo in ports.json. Used by Allocate to skip ports a sibling repo
// owns.
func (m *Manager) takenHostPorts() (map[int]bool, error) {
	store, err := m.readStore()
	if err != nil {
		return nil, err
	}
	out := map[int]bool{}
	for _, entries := range store {
		for _, e := range entries {
			out[e.HostPort] = true
		}
	}
	return out, nil
}

// --- ss parsing ------------------------------------------------------------

// parseSsOutput walks the output of `ss -tlnp` and returns one
// ContainerBinding per externally-reachable TCP listener. We
// deliberately:
//
//   - skip the header line (first column is non-numeric "State");
//   - skip loopback bindings (127.0.0.1, ::1) — those don't escape the
//     container's network namespace, so forwarding them would be a
//     no-op or worse;
//   - deduplicate ports — most daemons bind both 0.0.0.0 and ::, which
//     would otherwise show up as two records;
//   - prefer the first parseable process name (`users:(("name",…))`)
//     for each port, falling back to "" when ss couldn't read /proc.
//
// Malformed lines are skipped silently — `ss` output varies subtly
// between Alpine / Ubuntu / busybox builds and we'd rather miss one
// binding than refuse to forward any.
func parseSsOutput(b []byte) []ContainerBinding {
	lines := strings.Split(string(b), "\n")
	type seenEntry struct {
		idx     int
		process string
	}
	seen := map[int]seenEntry{}
	order := []int{}
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		// `ss -tlnp` columns: State Recv-Q Send-Q LocalAddr:Port PeerAddr:Port Process
		// On busybox / Alpine: State Recv-Q Send-Q LocalAddr:Port PeerAddr:Port
		// We need at least the Local Address field.
		if len(fields) < 4 {
			continue
		}
		if !looksNumeric(fields[1]) || !looksNumeric(fields[2]) {
			// Header line ("State Recv-Q Send-Q ...") or anything else
			// without numeric queue counts is skipped.
			continue
		}
		port, host, ok := extractHostPort(fields[3])
		if !ok {
			continue
		}
		if isLoopback(host) {
			continue
		}
		process := extractProcess(line)
		if entry, dup := seen[port]; dup {
			// Prefer the first non-empty process name we've seen for
			// this port.
			if entry.process == "" && process != "" {
				entry.process = process
				seen[port] = entry
			}
			continue
		}
		seen[port] = seenEntry{idx: len(order), process: process}
		order = append(order, port)
	}
	out := make([]ContainerBinding, 0, len(order))
	for _, p := range order {
		out = append(out, ContainerBinding{Port: p, Process: seen[p].process})
	}
	return out
}

// looksNumeric reports whether s is a non-empty all-digits run.
func looksNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// extractHostPort splits an "address:port" pair as `ss` formats it and
// returns the integer port plus the literal host portion. IPv6 forms
// like `[::]:8080` and `[::1]:8080` are handled. Returns ok=false on
// any parse failure so the caller can skip the line.
func extractHostPort(s string) (int, string, bool) {
	// Bracketed IPv6: [host]:port
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end < 0 || end+2 > len(s) || s[end+1] != ':' {
			return 0, "", false
		}
		host := s[1:end]
		portStr := s[end+2:]
		port, err := strconv.Atoi(portStr)
		if err != nil {
			return 0, "", false
		}
		return port, host, true
	}
	idx := strings.LastIndex(s, ":")
	if idx < 0 {
		return 0, "", false
	}
	host := s[:idx]
	portStr := s[idx+1:]
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, "", false
	}
	return port, host, true
}

// isLoopback reports whether host (the literal address from
// extractHostPort) refers to a loopback interface. Empty host (e.g.
// "*:8080") is treated as wildcard — NOT loopback.
func isLoopback(host string) bool {
	switch host {
	case "127.0.0.1", "::1":
		return true
	}
	return false
}

// extractProcess pulls the first process name out of an ss `users:(…)`
// column. Returns "" if the column is absent or unparseable (which is
// the common case for non-root ss probes).
func extractProcess(line string) string {
	idx := strings.Index(line, `users:((`)
	if idx < 0 {
		return ""
	}
	rest := line[idx+len(`users:((`):]
	// rest starts with `"<name>",pid=…)` — read until the closing quote.
	if !strings.HasPrefix(rest, `"`) {
		return ""
	}
	rest = rest[1:]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// SortedRepos returns the repo keys of m sorted alphabetically. Exposed
// because the CLI renderer wants a deterministic order and there's no
// other natural home for the helper.
func SortedRepos(m map[string][]Mapping) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
