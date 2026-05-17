//go:build windows

package hostinfo

import "testing"

func TestTotalMemoryBytes_Windows(t *testing.T) {
	bytes, err := totalMemoryBytes()
	if err != nil {
		t.Fatalf("unexpected error from GlobalMemoryStatusEx: %v", err)
	}
	if bytes < gib {
		t.Errorf("RAM = %d bytes, expected >= 1 GiB on a real host", bytes)
	}
}
