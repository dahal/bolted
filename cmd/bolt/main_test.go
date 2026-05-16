package main

import (
	"runtime"
	"testing"
)

func TestInvokedName(t *testing.T) {
	cases := map[string]string{
		"/usr/local/bin/workspace": "workspace",
		"/usr/local/bin/bolt":        "bolt",
		"workspace":                "workspace",
		"bolt":                       "bolt",
		"./dist/bolt":         "workspace",
		"bolt.exe":            "workspace",
		"bolt.exe":                   "bolt",
	}
	for in, want := range cases {
		if got := invokedName(in); got != want {
			t.Errorf("invokedName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInvokedName_WindowsPaths(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("backslash separators only meaningful on Windows")
	}
	cases := map[string]string{
		`C:\Program Files\bolt.exe`: "workspace",
		`C:\Program Files\bolt.exe`:        "bolt",
	}
	for in, want := range cases {
		if got := invokedName(in); got != want {
			t.Errorf("invokedName(%q) = %q, want %q", in, got, want)
		}
	}
}
