// Package hostinfo detects host hardware (RAM, CPU count) and applies the
// auto-sizing formula documented in .claude/brainstorm/02-architecture.md
// § Resource sizing.
package hostinfo

import (
	"fmt"
	"runtime"

	"github.com/dahal/bolted/internal/config"
)

// Formula constants (bytes / cores). Kept as vars so tests can reason about
// them by name rather than re-deriving from magic numbers.
const (
	gib uint64 = 1 << 30

	minRAMBytes uint64 = 4 * gib
	maxRAMBytes uint64 = 16 * gib

	minCPUs = 2
	maxCPUs = 8

	defaultDisk = "50GB"
)

// totalMemoryFn is the indirection point that lets tests inject host RAM
// without touching the OS-specific implementations.
var totalMemoryFn = totalMemoryBytes

// numCPUFn is the indirection point for the CPU count.
var numCPUFn = runtime.NumCPU

// DetectDefaults returns sized VM defaults for the current host.
//
//	RAM  = max(4 GiB, min(16 GiB, 25% of host RAM))
//	CPUs = max(2,     min(8,      50% of host cores))
//	Disk = 50 GB (sparse, grows on demand)
//
// Wraps the pure DefaultsFromHostInfo with live host detection.
func DetectDefaults() (config.VMConfig, error) {
	ram, err := totalMemoryFn()
	if err != nil {
		return config.VMConfig{}, fmt.Errorf("read host memory: %w", err)
	}
	return DefaultsFromHostInfo(ram, numCPUFn()), nil
}

// DefaultsFromHostInfo applies the auto-sizing formula to explicit RAM /
// CPU inputs. Exposed (and exported) so tests and callers can reason about
// the formula without mocking the OS.
func DefaultsFromHostInfo(ramBytes uint64, hostCPUs int) config.VMConfig {
	return config.VMConfig{
		Memory: formatGB(clampRAM(ramBytes)),
		CPUs:   clampCPUs(hostCPUs),
		Disk:   defaultDisk,
	}
}

// clampRAM applies the 25% / floor / ceiling rule to host RAM, returning a
// byte count.
func clampRAM(ramBytes uint64) uint64 {
	target := ramBytes / 4 // 25%
	if target < minRAMBytes {
		return minRAMBytes
	}
	if target > maxRAMBytes {
		return maxRAMBytes
	}
	return target
}

// clampCPUs applies the 50% / floor / ceiling rule to host CPU count.
func clampCPUs(host int) int {
	if host <= 0 {
		return minCPUs
	}
	target := host / 2 // 50%
	if target < minCPUs {
		return minCPUs
	}
	if target > maxCPUs {
		return maxCPUs
	}
	return target
}

// formatGB renders a byte count as a whole-GiB string ("8GB"). Truncates;
// the clamped values always land on a clean GiB boundary so no precision
// is lost in practice.
func formatGB(bytes uint64) string {
	return fmt.Sprintf("%dGB", bytes/gib)
}
