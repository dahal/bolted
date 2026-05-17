//go:build linux

package hostinfo

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// procMeminfoPath is the file we read for MemTotal. Indirection lets tests
// point this at a fixture file.
var procMeminfoPath = "/proc/meminfo"

// totalMemoryBytes parses /proc/meminfo's MemTotal line (which is in KiB).
func totalMemoryBytes() (uint64, error) {
	f, err := os.Open(procMeminfoPath)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", procMeminfoPath, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, fmt.Errorf("malformed MemTotal line: %q", line)
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse MemTotal value %q: %w", fields[1], err)
		}
		return kb * 1024, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("scan %s: %w", procMeminfoPath, err)
	}
	return 0, errors.New("MemTotal not found in /proc/meminfo")
}
