//go:build linux

package hostinfo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withProcMeminfo points procMeminfoPath at a fixture file for the duration
// of one test.
func withProcMeminfo(t *testing.T, contents string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "meminfo")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	orig := procMeminfoPath
	t.Cleanup(func() { procMeminfoPath = orig })
	procMeminfoPath = path
}

func TestTotalMemoryBytes_LinuxRealHost(t *testing.T) {
	bytes, err := totalMemoryBytes()
	if err != nil {
		t.Fatalf("unexpected error reading /proc/meminfo: %v", err)
	}
	if bytes < gib {
		t.Errorf("RAM = %d bytes, expected >= 1 GiB on a real host", bytes)
	}
}

func TestTotalMemoryBytes_LinuxFixtureOK(t *testing.T) {
	withProcMeminfo(t, "MemTotal:       16777216 kB\nMemFree:         1000 kB\n")
	bytes, err := totalMemoryBytes()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := uint64(16777216 * 1024); bytes != want {
		t.Errorf("bytes = %d, want %d", bytes, want)
	}
}

func TestTotalMemoryBytes_LinuxMissingFile(t *testing.T) {
	orig := procMeminfoPath
	t.Cleanup(func() { procMeminfoPath = orig })
	procMeminfoPath = "/nonexistent/path/to/meminfo"
	if _, err := totalMemoryBytes(); err == nil {
		t.Fatal("expected error for missing meminfo file")
	}
}

func TestTotalMemoryBytes_LinuxMalformedLine(t *testing.T) {
	withProcMeminfo(t, "MemTotal:\n")
	_, err := totalMemoryBytes()
	if err == nil || !strings.Contains(err.Error(), "malformed MemTotal") {
		t.Errorf("expected malformed error, got: %v", err)
	}
}

func TestTotalMemoryBytes_LinuxNonNumericValue(t *testing.T) {
	withProcMeminfo(t, "MemTotal: notanumber kB\n")
	_, err := totalMemoryBytes()
	if err == nil || !strings.Contains(err.Error(), "parse MemTotal") {
		t.Errorf("expected parse error, got: %v", err)
	}
}

func TestTotalMemoryBytes_LinuxNoMemTotalLine(t *testing.T) {
	withProcMeminfo(t, "MemFree: 1234 kB\n")
	_, err := totalMemoryBytes()
	if err == nil || !strings.Contains(err.Error(), "MemTotal not found") {
		t.Errorf("expected not-found error, got: %v", err)
	}
}
