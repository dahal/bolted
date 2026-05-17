package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Validate checks every field and returns the first error found. Validate is
// safe to call on a Config that has just had defaults applied; in that state
// every required field is populated.
//
// Side effect: Validate normalises path-valued fields in place — leading "~"
// and "$HOME" are expanded to the absolute home directory. This is by design:
// downstream callers should never have to re-expand paths.
func (c *Config) Validate() error {
	if _, err := ParseSize(c.VM.Memory); err != nil {
		return fmt.Errorf("vm.memory: %w", err)
	}
	if _, err := ParseSize(c.VM.Disk); err != nil {
		return fmt.Errorf("vm.disk: %w", err)
	}
	if c.VM.CPUs < 1 {
		return fmt.Errorf("vm.cpus: must be >= 1, got %d", c.VM.CPUs)
	}
	switch c.Backend {
	case "auto", "lima", "wsl2":
		// ok
	default:
		return fmt.Errorf("backend: unknown value %q (want auto, lima, or wsl2)", c.Backend)
	}
	expanded, err := NormalizePath(c.DefaultDevcontainer)
	if err != nil {
		return fmt.Errorf("default_devcontainer: %w", err)
	}
	c.DefaultDevcontainer = expanded
	return nil
}

// sizeUnits maps a unit suffix to its byte multiplier. KB/MB/GB/TB use the
// binary IEC interpretation (1024-based) which matches the original "8GB
// means 8 GiB" expectation users have in this domain (Lima/WSL2 docs both
// use the same convention).
var sizeUnits = []struct {
	suffix string
	mult   int64
}{
	{"TB", 1024 * 1024 * 1024 * 1024},
	{"GB", 1024 * 1024 * 1024},
	{"MB", 1024 * 1024},
	{"KB", 1024},
	{"B", 1},
}

// ParseSize parses a human-readable size string ("8GB", "512MB", "1024KB",
// "1B") into a byte count.
//
// Rules:
//   - Suffix is required and case-insensitive. A bare number is rejected.
//   - Whitespace inside the string (e.g. "8 GB", "8 gigs") is rejected.
//   - The numeric prefix must be a non-negative integer; fractional sizes
//     are rejected on purpose — they're a footgun (what's "0.5MB" rounded to?)
//     and the upstream tools accept only whole units.
//   - The result must fit in an int64.
func ParseSize(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	if strings.ContainsAny(s, " \t\n\r") {
		return 0, fmt.Errorf("invalid size %q: contains whitespace", s)
	}
	upper := strings.ToUpper(s)
	for _, u := range sizeUnits {
		if strings.HasSuffix(upper, u.suffix) {
			numPart := strings.TrimSuffix(upper, u.suffix)
			if numPart == "" {
				return 0, fmt.Errorf("invalid size %q: missing number", s)
			}
			n, err := strconv.ParseInt(numPart, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid size %q: %w", s, err)
			}
			if n < 0 {
				return 0, fmt.Errorf("invalid size %q: negative", s)
			}
			// Overflow check.
			if n > 0 && n > (1<<62)/u.mult {
				return 0, fmt.Errorf("invalid size %q: overflows int64", s)
			}
			return n * u.mult, nil
		}
	}
	return 0, fmt.Errorf("invalid size %q: missing unit suffix (want B, KB, MB, GB, or TB)", s)
}

// homeDir is an indirection point so tests can inject a fake home directory.
var homeDir = os.UserHomeDir

// NormalizePath expands a leading "~" or "$HOME" segment to the user's home
// directory and returns the cleaned absolute path. A non-prefixed path is
// returned cleaned but not resolved (callers can decide whether to require
// absoluteness).
//
// Rules:
//   - Empty string is an error.
//   - "~" alone, "~/foo", "$HOME", and "$HOME/foo" all expand.
//   - "~otheruser/..." (lookup of another user's home) is NOT supported — it
//     requires cgo on macOS and adds surprises; callers that need it should
//     pass the absolute path directly.
//   - The result is run through filepath.Clean.
func NormalizePath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	switch {
	case p == "~":
		h, err := homeDir()
		if err != nil {
			return "", fmt.Errorf("expand ~: %w", err)
		}
		return filepath.Clean(h), nil
	case strings.HasPrefix(p, "~/"):
		h, err := homeDir()
		if err != nil {
			return "", fmt.Errorf("expand ~: %w", err)
		}
		return filepath.Clean(filepath.Join(h, p[2:])), nil
	case strings.HasPrefix(p, "~"):
		// Catches "~otheruser/..." — not supported.
		return "", fmt.Errorf("unsupported ~user prefix in %q", p)
	case p == "$HOME":
		h, err := homeDir()
		if err != nil {
			return "", fmt.Errorf("expand $HOME: %w", err)
		}
		return filepath.Clean(h), nil
	case strings.HasPrefix(p, "$HOME/"):
		h, err := homeDir()
		if err != nil {
			return "", fmt.Errorf("expand $HOME: %w", err)
		}
		return filepath.Clean(filepath.Join(h, p[len("$HOME/"):])), nil
	}
	return filepath.Clean(p), nil
}
