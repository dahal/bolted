package state

import (
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteReadJSON_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.json")
	in := map[string]any{"a": "b", "n": float64(1)}
	if err := WriteJSON(path, in); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	out, err := ReadJSON[map[string]any](path)
	if err != nil {
		t.Fatalf("ReadJSON: %v", err)
	}
	if out["a"] != "b" {
		t.Errorf("a = %v, want b", out["a"])
	}
}

func TestReadJSON_MissingFile(t *testing.T) {
	dir := t.TempDir()
	_, err := ReadJSON[map[string]any](filepath.Join(dir, "nope.json"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected fs.ErrNotExist, got %v", err)
	}
}

func TestReadJSON_OtherReadError(t *testing.T) {
	// Pass a directory path; ReadFile returns EISDIR (not ErrNotExist).
	dir := t.TempDir()
	_, err := ReadJSON[map[string]any](dir)
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Errorf("did not expect ErrNotExist, got %v", err)
	}
}

func TestReadJSON_BadJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := ReadJSON[map[string]any](path); err == nil {
		t.Fatal("expected JSON parse error")
	}
}

func TestWriteJSON_MarshalFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.json")
	// math.NaN cannot be marshalled to JSON.
	if err := WriteJSON(path, map[string]any{"n": math.NaN()}); err == nil {
		t.Fatal("expected marshal error for NaN")
	}
}

func TestWriteJSON_MkdirFailure(t *testing.T) {
	// Create a regular file where the directory should live; MkdirAll fails.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := WriteJSON(filepath.Join(blocker, "child.json"), map[string]any{}); err == nil {
		t.Fatal("expected mkdir error")
	}
}

func TestWriteJSON_AtomicWriteLeavesOriginalOnRenameFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.json")
	// Seed an existing file.
	original := []byte(`{"original":true}` + "\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Force rename to fail.
	prev := renameFn
	t.Cleanup(func() { renameFn = prev })
	renameFn = func(string, string) error { return errors.New("simulated rename failure") }

	if err := WriteJSON(path, map[string]any{"new": true}); err == nil {
		t.Fatal("expected rename failure to surface")
	}
	// Original must still be intact.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(original) {
		t.Errorf("original file mutated: %q", got)
	}
	// No orphaned temp files in the directory.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("orphaned temp file: %s", e.Name())
		}
	}
}

func TestWriteJSON_CreateTempFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.json")
	prev := createTemp
	t.Cleanup(func() { createTemp = prev })
	createTemp = func(string, string) (*os.File, error) { return nil, errors.New("boom") }
	if err := WriteJSON(path, map[string]any{}); err == nil {
		t.Fatal("expected createTemp error")
	}
}

func TestWriteJSON_WriteFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.json")
	prev := writeFile
	t.Cleanup(func() { writeFile = prev })
	writeFile = func(*os.File, []byte) (int, error) { return 0, errors.New("boom") }
	if err := WriteJSON(path, map[string]any{"a": 1}); err == nil {
		t.Fatal("expected write error")
	}
}

func TestWriteJSON_SyncFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.json")
	prev := syncFile
	t.Cleanup(func() { syncFile = prev })
	syncFile = func(*os.File) error { return errors.New("boom") }
	if err := WriteJSON(path, map[string]any{"a": 1}); err == nil {
		t.Fatal("expected sync error")
	}
}

func TestWriteJSON_CloseFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.json")
	prev := closeFile
	t.Cleanup(func() { closeFile = prev })
	closeFile = func(f *os.File) error { _ = f.Close(); return errors.New("boom") }
	if err := WriteJSON(path, map[string]any{"a": 1}); err == nil {
		t.Fatal("expected close error")
	}
}

func TestWriteJSON_ChmodFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.json")
	prev := chmodFn
	t.Cleanup(func() { chmodFn = prev })
	chmodFn = func(string, os.FileMode) error { return errors.New("boom") }
	if err := WriteJSON(path, map[string]any{"a": 1}); err == nil {
		t.Fatal("expected chmod error")
	}
}

func TestWriteJSON_PortsContainersTrustProvisioned(t *testing.T) {
	dir := t.TempDir()
	ports := Ports{"3000": "web-app"}
	if err := WritePorts(dir, ports); err != nil {
		t.Fatalf("WritePorts: %v", err)
	}
	gotPorts, err := ReadPorts(dir)
	if err != nil {
		t.Fatalf("ReadPorts: %v", err)
	}
	if gotPorts["3000"] != "web-app" {
		t.Errorf("ports round-trip mismatch: %v", gotPorts)
	}

	containers := Containers{"api": "bolted-api"}
	if err := WriteContainers(dir, containers); err != nil {
		t.Fatalf("WriteContainers: %v", err)
	}
	gotC, err := ReadContainers(dir)
	if err != nil {
		t.Fatalf("ReadContainers: %v", err)
	}
	if gotC["api"] != "bolted-api" {
		t.Errorf("containers round-trip mismatch: %v", gotC)
	}

	trust := DevcontainerTrust{"sha256:abc": true}
	if err := WriteDevcontainerTrust(dir, trust); err != nil {
		t.Fatalf("WriteDevcontainerTrust: %v", err)
	}
	gotT, err := ReadDevcontainerTrust(dir)
	if err != nil {
		t.Fatalf("ReadDevcontainerTrust: %v", err)
	}
	if gotT["sha256:abc"] != true {
		t.Errorf("trust round-trip mismatch: %v", gotT)
	}

	prov := Provisioned{"node": "20.10.0"}
	if err := WriteProvisioned(dir, prov); err != nil {
		t.Fatalf("WriteProvisioned: %v", err)
	}
	gotP, err := ReadProvisioned(dir)
	if err != nil {
		t.Fatalf("ReadProvisioned: %v", err)
	}
	if gotP["node"] != "20.10.0" {
		t.Errorf("provisioned round-trip mismatch: %v", gotP)
	}
}
