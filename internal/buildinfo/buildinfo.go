// Package buildinfo exposes version and commit metadata embedded at build
// time via the linker's -X flag, with a runtime/debug fallback that makes
// `go run` and unit tests still report a sensible value.
package buildinfo

import (
	"fmt"
	"runtime/debug"
)

// These vars are set by `go build -ldflags`:
//
//	-X github.com/dahal/bolted/internal/buildinfo.Version=<tag>
//	-X github.com/dahal/bolted/internal/buildinfo.Commit=<sha>
var (
	Version = ""
	Commit  = ""
)

// String returns the canonical version line:
//
//	bolt vX.Y.Z (<short-commit>)
//
// When ldflags didn't set the vars, falls back to runtime/debug.ReadBuildInfo.
// As a final fallback, prints "(devel)" / "unknown".
func String() string {
	v, c := Version, Commit
	if v == "" || c == "" {
		dv, dc := fromDebug()
		if v == "" {
			v = dv
		}
		if c == "" {
			c = dc
		}
	}
	if v == "" {
		v = "(devel)"
	}
	if c == "" {
		c = "unknown"
	}
	return fmt.Sprintf("bolt %s (%s)", v, shortCommit(c))
}

func shortCommit(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}

// readBuildInfo is the indirection point that lets tests substitute a fake
// implementation, exercising fromDebug's branches that wouldn't otherwise
// fire from inside `go test`.
var readBuildInfo = debug.ReadBuildInfo

func fromDebug() (version, commit string) {
	info, ok := readBuildInfo()
	if !ok {
		return "", ""
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		version = info.Main.Version
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			commit = s.Value
		}
	}
	return version, commit
}
