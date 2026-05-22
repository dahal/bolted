//go:build windows

package hostinfo

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// memoryStatusEx mirrors the Win32 MEMORYSTATUSEX struct expected by
// GlobalMemoryStatusEx. golang.org/x/sys/windows does not export either
// the struct or the proc, so we declare both manually here.
type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
)

// totalMemoryBytes calls the Win32 GlobalMemoryStatusEx API for physical RAM.
// ret == 0 indicates failure; LastError flows back through err.
func totalMemoryBytes() (uint64, error) {
	var status memoryStatusEx
	status.Length = uint32(unsafe.Sizeof(status))
	ret, _, err := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&status)))
	if ret == 0 {
		return 0, fmt.Errorf("GlobalMemoryStatusEx: %w", err)
	}
	return status.TotalPhys, nil
}
