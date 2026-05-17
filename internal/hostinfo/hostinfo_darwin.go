//go:build darwin

package hostinfo

import "golang.org/x/sys/unix"

// totalMemoryBytes returns physical RAM from the kernel's hw.memsize sysctl.
func totalMemoryBytes() (uint64, error) {
	return unix.SysctlUint64("hw.memsize")
}
