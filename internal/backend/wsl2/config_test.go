package wsl2

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderWSLConfig(t *testing.T) {
	body := renderWSLConfig(8192, 4)
	if !strings.Contains(body, "memory=8192MB") {
		t.Errorf("missing memory line:\n%s", body)
	}
	if !strings.Contains(body, "processors=4") {
		t.Errorf("missing processors line:\n%s", body)
	}
	if !strings.Contains(body, "[wsl2]") {
		t.Errorf("missing [wsl2] section header:\n%s", body)
	}
}

func TestWriteWSLConfigHint_NoGlobal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested") // ensure MkdirAll runs
	path, warn, err := writeWSLConfigHint(dir, 4096, 2, false)
	if err != nil {
		t.Fatalf("writeWSLConfigHint: %v", err)
	}
	if warn != "" {
		t.Errorf("expected empty warn, got %q", warn)
	}
	if filepath.Base(path) != ".wslconfig" {
		t.Errorf("path basename = %q, want .wslconfig", filepath.Base(path))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read .wslconfig: %v", err)
	}
	if !strings.Contains(string(data), "memory=4096MB") {
		t.Errorf("body missing memory line:\n%s", data)
	}
}

func TestWriteWSLConfigHint_WithGlobalWarns(t *testing.T) {
	dir := t.TempDir()
	path, warn, err := writeWSLConfigHint(dir, 4096, 2, true)
	if err != nil {
		t.Fatalf("writeWSLConfigHint: %v", err)
	}
	if warn == "" {
		t.Error("expected warning")
	}
	if !strings.Contains(warn, path) {
		t.Errorf("warning should reference the path it wrote, got %q", warn)
	}
}

func TestWriteWSLConfigHint_WriteFileError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file mode bits")
	}
	dir := t.TempDir()
	// Pre-create the destination as a read-only file so WriteFile fails
	// with EACCES.
	target := filepath.Join(dir, ".wslconfig")
	if err := os.WriteFile(target, []byte("old"), 0o400); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(target, 0o644) })
	// Probe: if WriteFile succeeds anyway (some filesystems), skip.
	if err := os.WriteFile(target, []byte("probe"), 0o400); err == nil {
		t.Skip("filesystem does not enforce read-only mode bits")
	}
	_, _, err := writeWSLConfigHint(dir, 4096, 2, false)
	if err == nil {
		t.Fatal("expected WriteFile error")
	}
	if !strings.Contains(err.Error(), "write") {
		t.Errorf("error should mention write, got: %v", err)
	}
}

func TestWriteWSLConfigHint_MkdirError(t *testing.T) {
	// Force MkdirAll to fail by pointing at a path that already exists
	// as a *file*.
	parent := t.TempDir()
	collision := filepath.Join(parent, "blocker")
	if err := os.WriteFile(collision, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	// installDir would now be <collision>/wsl, but <collision> is a
	// file, so MkdirAll fails.
	_, _, err := writeWSLConfigHint(filepath.Join(collision, "wsl"), 4096, 2, false)
	if err == nil {
		t.Fatal("expected MkdirAll to fail")
	}
}

func TestLoadPortMappings_MissingFile(t *testing.T) {
	dir := t.TempDir()
	m, err := loadPortMappings(dir)
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if len(m.Mappings) != 0 {
		t.Errorf("expected empty map, got %+v", m.Mappings)
	}
}

func TestLoadPortMappings_BadJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, portsFile), []byte("not-json"), 0o644); err != nil {
		t.Fatalf("seed bad json: %v", err)
	}
	_, err := loadPortMappings(dir)
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestLoadPortMappings_NullMappings(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, portsFile), []byte(`{"mappings": null}`), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	m, err := loadPortMappings(dir)
	if err != nil {
		t.Fatalf("loadPortMappings: %v", err)
	}
	if m.Mappings == nil {
		t.Error("Mappings should be initialised to an empty map, not nil")
	}
}

func TestSavePortMappings_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := portMappings{Mappings: map[string]int{
		"3000": 3000,
		"8081": 8080,
	}}
	if err := savePortMappings(dir, in); err != nil {
		t.Fatalf("savePortMappings: %v", err)
	}
	out, err := loadPortMappings(dir)
	if err != nil {
		t.Fatalf("loadPortMappings: %v", err)
	}
	if len(out.Mappings) != 2 || out.Mappings["3000"] != 3000 || out.Mappings["8081"] != 8080 {
		t.Errorf("round-trip mismatch: %+v", out)
	}
}

func TestSavePortMappings_NilMapBecomesEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := savePortMappings(dir, portMappings{Mappings: nil}); err != nil {
		t.Fatalf("savePortMappings: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, portsFile))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), `"mappings": {}`) {
		t.Errorf("expected empty map literal in JSON, got:\n%s", data)
	}
}

func TestSavePortMappings_MkdirError(t *testing.T) {
	parent := t.TempDir()
	collision := filepath.Join(parent, "blocker")
	if err := os.WriteFile(collision, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	err := savePortMappings(filepath.Join(collision, "wsl"), portMappings{})
	if err == nil {
		t.Fatal("expected MkdirAll to fail")
	}
}

func TestSavePortMappings_StableOrdering(t *testing.T) {
	// Stable on-disk output is documented behaviour; assert by writing
	// the same set twice and checking byte equality.
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	in := portMappings{Mappings: map[string]int{
		"9000": 9000,
		"3000": 3000,
		"5000": 5000,
		"7000": 7000,
	}}
	if err := savePortMappings(dir1, in); err != nil {
		t.Fatalf("save 1: %v", err)
	}
	if err := savePortMappings(dir2, in); err != nil {
		t.Fatalf("save 2: %v", err)
	}
	a, _ := os.ReadFile(filepath.Join(dir1, portsFile))
	b, _ := os.ReadFile(filepath.Join(dir2, portsFile))
	if string(a) != string(b) {
		t.Errorf("on-disk output not stable:\n%s\n--vs--\n%s", a, b)
	}
}

func TestLoadPortMappings_ReadError(t *testing.T) {
	// Point load at a directory (not a file) so ReadFile returns a
	// non-IsNotExist error.
	dir := t.TempDir()
	// Create the ports.json *as a directory* to force the error.
	if err := os.Mkdir(filepath.Join(dir, portsFile), 0o755); err != nil {
		t.Fatalf("seed dir: %v", err)
	}
	_, err := loadPortMappings(dir)
	if err == nil {
		t.Fatal("expected read error when ports.json is a dir")
	}
}

func TestSavePortMappings_MarshalError(t *testing.T) {
	orig := jsonMarshalIndent
	t.Cleanup(func() { jsonMarshalIndent = orig })
	sentinel := errors.New("simulated marshal failure")
	jsonMarshalIndent = func(any, string, string) ([]byte, error) {
		return nil, sentinel
	}
	err := savePortMappings(t.TempDir(), portMappings{Mappings: map[string]int{"3000": 3000}})
	if err == nil {
		t.Fatal("expected error from injected marshal failure")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel, got: %v", err)
	}
}
