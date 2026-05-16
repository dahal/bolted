package cli

import (
	"fmt"
	"io"
	"os"
)

// stderr is the stream the passthrough stub writes to. Tests swap this.
var stderr io.Writer = os.Stderr

// passthroughStub is the placeholder for spec 11's passthrough router. Any
// non-reserved subcommand routes here for now and exits non-zero so users
// (and tests) see that the feature is pending.
func passthroughStub(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "no command given")
		return 1
	}
	fmt.Fprintf(stderr,
		"passthrough %q: not yet implemented — see spec 11 (passthrough router)\n",
		args[0],
	)
	return 1
}
