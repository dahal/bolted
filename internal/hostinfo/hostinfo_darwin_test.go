//go:build darwin

package hostinfo

import "testing"

func TestTotalMemoryBytes_Darwin(t *testing.T) {
	bytes, err := totalMemoryBytes()
	if err != nil {
		t.Fatalf("unexpected error reading hw.memsize: %v", err)
	}
	if bytes == 0 {
		t.Fatal("expected non-zero RAM from hw.memsize")
	}
	// Sanity: every modern Mac has at least 1 GiB of RAM.
	if bytes < gib {
		t.Errorf("RAM = %d bytes, expected >= 1 GiB", bytes)
	}
}
