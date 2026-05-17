// Package state owns the JSON files under ~/.bolted/state/. It provides
// generic atomic read/write helpers plus thin typed accessors for the four
// files specs 13–18 will populate.
//
// All writes are atomic: we write to a temp file in the same directory,
// fsync it, and rename over the destination. A crash between the write and
// the rename leaves the original file intact. Reads are a single os.ReadFile
// + json.Unmarshal; there's no inter-process locking — writers serialise via
// the rename and readers see the old-or-new content, never a half-written
// blob.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// File names under ~/.bolted/state/. Kept here so callers don't drift on
// the exact spelling. The four files are populated by:
//
//	ports.json            → spec 14 (host-port allocations)
//	containers.json       → spec 13 (repo → running container map)
//	devcontainer-trust.json → spec 18 (approved devcontainer hashes)
//	provisioned.json      → spec 15 (cache of installed features)
const (
	PortsFile             = "ports.json"
	ContainersFile        = "containers.json"
	DevcontainerTrustFile = "devcontainer-trust.json"
	ProvisionedFile       = "provisioned.json"
)

// Placeholder value types. The consuming specs (13/14/15/18) will replace
// these with structured shapes; for now we use map[string]any so existing
// state files round-trip cleanly without requiring updates here.
type (
	// Ports is the on-disk shape of ports.json. Concrete schema lands in spec 14.
	Ports = map[string]any
	// Containers is the on-disk shape of containers.json. Schema in spec 13.
	Containers = map[string]any
	// DevcontainerTrust is the on-disk shape of devcontainer-trust.json. Schema in spec 18.
	DevcontainerTrust = map[string]any
	// Provisioned is the on-disk shape of provisioned.json. Schema in spec 15.
	Provisioned = map[string]any
)

// Indirection points so tests can simulate filesystem failures at each
// stage of WriteJSON. Production callers should never reassign these.
var (
	renameFn    = os.Rename
	createTemp  = os.CreateTemp
	mkdirAllFn  = os.MkdirAll
	chmodFn     = os.Chmod
	syncFile    = func(f *os.File) error { return f.Sync() }
	writeFile   = func(f *os.File, data []byte) (int, error) { return f.Write(data) }
	closeFile   = func(f *os.File) error { return f.Close() }
	marshalJSON = json.MarshalIndent
)

// ReadJSON reads path as JSON into a value of type T and returns it. A
// missing file returns the zero value of T and a wrapped fs.ErrNotExist;
// callers can detect this with errors.Is(err, fs.ErrNotExist) and fall back
// to defaults.
func ReadJSON[T any](path string) (T, error) {
	var zero T
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return zero, fmt.Errorf("state: read %s: %w", path, err)
		}
		return zero, fmt.Errorf("state: read %s: %w", path, err)
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return zero, fmt.Errorf("state: parse %s: %w", path, err)
	}
	return v, nil
}

// WriteJSON atomically writes v as pretty-printed JSON to path. The write
// sequence is:
//
//  1. Marshal v.
//  2. Create a temp file in the same directory as path (so the rename is on
//     the same filesystem and therefore atomic).
//  3. Write, fsync, and close the temp file.
//  4. Rename the temp file over path.
//  5. On any error after the temp file is created, remove the temp file so
//     it never lingers.
//
// If the rename fails, the original path is untouched.
func WriteJSON[T any](path string, v T) error {
	data, err := marshalJSON(v, "", "  ")
	if err != nil {
		return fmt.Errorf("state: marshal %s: %w", path, err)
	}
	dir := filepath.Dir(path)
	if err := mkdirAllFn(dir, 0o700); err != nil {
		return fmt.Errorf("state: mkdir %s: %w", dir, err)
	}
	tmp, err := createTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("state: create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	// Guarantee the temp file is cleaned up unless rename succeeds.
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := writeFile(tmp, data); err != nil {
		_ = closeFile(tmp)
		return fmt.Errorf("state: write temp %s: %w", tmpName, err)
	}
	if err := syncFile(tmp); err != nil {
		_ = closeFile(tmp)
		return fmt.Errorf("state: fsync temp %s: %w", tmpName, err)
	}
	if err := closeFile(tmp); err != nil {
		return fmt.Errorf("state: close temp %s: %w", tmpName, err)
	}
	if err := chmodFn(tmpName, 0o600); err != nil {
		return fmt.Errorf("state: chmod temp %s: %w", tmpName, err)
	}
	if err := renameFn(tmpName, path); err != nil {
		return fmt.Errorf("state: rename %s -> %s: %w", tmpName, path, err)
	}
	committed = true
	return nil
}

// ReadPorts loads ports.json from stateDir. Returns a wrapped fs.ErrNotExist
// if the file is missing.
func ReadPorts(stateDir string) (Ports, error) {
	return ReadJSON[Ports](filepath.Join(stateDir, PortsFile))
}

// WritePorts atomically replaces ports.json in stateDir.
func WritePorts(stateDir string, p Ports) error {
	return WriteJSON(filepath.Join(stateDir, PortsFile), p)
}

// ReadContainers loads containers.json from stateDir.
func ReadContainers(stateDir string) (Containers, error) {
	return ReadJSON[Containers](filepath.Join(stateDir, ContainersFile))
}

// WriteContainers atomically replaces containers.json in stateDir.
func WriteContainers(stateDir string, c Containers) error {
	return WriteJSON(filepath.Join(stateDir, ContainersFile), c)
}

// ReadDevcontainerTrust loads devcontainer-trust.json from stateDir.
func ReadDevcontainerTrust(stateDir string) (DevcontainerTrust, error) {
	return ReadJSON[DevcontainerTrust](filepath.Join(stateDir, DevcontainerTrustFile))
}

// WriteDevcontainerTrust atomically replaces devcontainer-trust.json in stateDir.
func WriteDevcontainerTrust(stateDir string, t DevcontainerTrust) error {
	return WriteJSON(filepath.Join(stateDir, DevcontainerTrustFile), t)
}

// ReadProvisioned loads provisioned.json from stateDir.
func ReadProvisioned(stateDir string) (Provisioned, error) {
	return ReadJSON[Provisioned](filepath.Join(stateDir, ProvisionedFile))
}

// WriteProvisioned atomically replaces provisioned.json in stateDir.
func WriteProvisioned(stateDir string, p Provisioned) error {
	return WriteJSON(filepath.Join(stateDir, ProvisionedFile), p)
}
