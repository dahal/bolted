package hostinfo

import (
	"errors"
	"testing"

	"github.com/dahal/bolted/internal/config"
)

func TestDefaultsFromHostInfo(t *testing.T) {
	cases := []struct {
		name    string
		ramGB   uint64
		cpus    int
		wantMem string
		wantCPU int
	}{
		{"32GB / 10 cores (M2 Pro)", 32, 10, "8GB", 5},
		{"16GB / 8 cores (mid laptop)", 16, 8, "4GB", 4},
		{"64GB / 16 cores (high-end)", 64, 16, "16GB", 8},
		{"4GB / 2 cores (low-end floor)", 4, 2, "4GB", 2},
		{"1GB / 1 core (below floor)", 1, 1, "4GB", 2},
		{"24GB / 6 cores (mid)", 24, 6, "6GB", 3},
		{"48GB / 12 cores (high)", 48, 12, "12GB", 6},
		{"96GB / 32 cores (huge — ram capped at 16, cpu capped at 8)", 96, 32, "16GB", 8},
		{"0 cores (defensive floor)", 16, 0, "4GB", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			vm := DefaultsFromHostInfo(c.ramGB*gib, c.cpus)
			if vm.Memory != c.wantMem {
				t.Errorf("Memory = %q, want %q", vm.Memory, c.wantMem)
			}
			if vm.CPUs != c.wantCPU {
				t.Errorf("CPUs = %d, want %d", vm.CPUs, c.wantCPU)
			}
			if vm.Disk != defaultDisk {
				t.Errorf("Disk = %q, want %q", vm.Disk, defaultDisk)
			}
		})
	}
}

func TestDefaultsFromHostInfo_OutputParseableByConfig(t *testing.T) {
	// The Memory string we produce must round-trip through config.ParseSize.
	vm := DefaultsFromHostInfo(32*gib, 10)
	if _, err := config.ParseSize(vm.Memory); err != nil {
		t.Fatalf("config.ParseSize(%q): %v", vm.Memory, err)
	}
}

func TestClampRAM(t *testing.T) {
	cases := map[uint64]uint64{
		0:        minRAMBytes,
		1 * gib:  minRAMBytes,
		16 * gib: minRAMBytes, // 25% = 4 GiB exactly, hits floor
		32 * gib: 8 * gib,
		64 * gib: maxRAMBytes,    // 25% = 16 GiB, exactly the ceiling
		96 * gib: maxRAMBytes,    // 25% = 24 GiB → capped at 16
	}
	for in, want := range cases {
		if got := clampRAM(in); got != want {
			t.Errorf("clampRAM(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestClampCPUs(t *testing.T) {
	cases := map[int]int{
		-1: minCPUs,
		0:  minCPUs,
		1:  minCPUs, // 50% = 0 → floor
		2:  minCPUs, // 50% = 1 → floor
		4:  2,       // 50% = 2 — exactly the floor
		8:  4,
		10: 5,
		16: maxCPUs, // 50% = 8 — exactly the ceiling
		32: maxCPUs, // 50% = 16 → capped at 8
	}
	for in, want := range cases {
		if got := clampCPUs(in); got != want {
			t.Errorf("clampCPUs(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestFormatGB(t *testing.T) {
	cases := map[uint64]string{
		0:        "0GB",
		1 * gib:  "1GB",
		8 * gib:  "8GB",
		16 * gib: "16GB",
	}
	for in, want := range cases {
		if got := formatGB(in); got != want {
			t.Errorf("formatGB(%d) = %q, want %q", in, got, want)
		}
	}
}

// withTotalMemoryFn lets a test swap the host-RAM source.
func withTotalMemoryFn(t *testing.T, fn func() (uint64, error)) {
	t.Helper()
	orig := totalMemoryFn
	t.Cleanup(func() { totalMemoryFn = orig })
	totalMemoryFn = fn
}

// withNumCPUFn lets a test swap the CPU-count source.
func withNumCPUFn(t *testing.T, fn func() int) {
	t.Helper()
	orig := numCPUFn
	t.Cleanup(func() { numCPUFn = orig })
	numCPUFn = fn
}

func TestDetectDefaults_OK(t *testing.T) {
	withTotalMemoryFn(t, func() (uint64, error) { return 32 * gib, nil })
	withNumCPUFn(t, func() int { return 10 })
	vm, err := DetectDefaults()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vm.Memory != "8GB" || vm.CPUs != 5 {
		t.Errorf("got Memory=%q CPUs=%d, want 8GB / 5", vm.Memory, vm.CPUs)
	}
}

func TestDetectDefaults_HostMemoryError(t *testing.T) {
	wantErr := errors.New("simulated sysctl failure")
	withTotalMemoryFn(t, func() (uint64, error) { return 0, wantErr })
	withNumCPUFn(t, func() int { return 4 })
	_, err := DetectDefaults()
	if err == nil {
		t.Fatal("expected error from host memory read")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("expected wrapped sentinel, got: %v", err)
	}
}

func TestDetectDefaults_RealHost(t *testing.T) {
	// Smoke: actually call the OS implementation. Confirms the per-OS
	// totalMemoryBytes returns something plausible on the build host.
	vm, err := DetectDefaults()
	if err != nil {
		t.Fatalf("DetectDefaults on real host failed: %v", err)
	}
	if vm.Memory == "" || vm.CPUs == 0 || vm.Disk == "" {
		t.Errorf("expected populated VMConfig, got %+v", vm)
	}
}
