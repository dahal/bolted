//go:build windows

package hostinfo

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// totalMemoryBytes calls the Win32 GlobalMemoryStatusEx API for physical RAM.
func totalMemoryBytes() (uint64, error) {
	var status windows.MemoryStatusEx
	status.Length = uint32(unsafe.Sizeof(status))
	if err := windows.GlobalMemoryStatusEx(&status); err != nil {
		return 0, fmt.Errorf("GlobalMemoryStatusEx: %w", err)
	}
	return status.TotalPhys, nil
}
