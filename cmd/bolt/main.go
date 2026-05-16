// Command bolt is the entrypoint for the Bolted CLI.
//
// The same binary serves the invocation `bolt` — the alias is
// name detection lets help text reflect the binary name as installed.
// 
// 
package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/dahal/bolted/internal/cli"
)

func main() {
	os.Exit(cli.Execute(invokedName(os.Args[0]), os.Args[1:]))
}

// invokedName returns the base command name (without directory or .exe suffix).
func invokedName(arg0 string) string {
	name := filepath.Base(arg0)
	if ext := filepath.Ext(name); ext != "" {
		name = strings.TrimSuffix(name, ext)
	}
	return name
}
