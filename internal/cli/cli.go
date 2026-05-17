// Package cli implements the Bolted CLI command tree.
//
// Execute is the single entrypoint called from cmd/bolt/main.go. It pre-
// routes invocations: anything whose first positional argument is not a
// reserved subcommand falls through to the passthrough handler (a placeholder
// in this spec; full implementation in spec 11). Everything else is dispatched
// through Cobra in newRootCmd.
package cli

import "os"

// Execute runs the CLI with the given invocation name (e.g. "bolt" (typically)
// "bolt") and arguments. It returns the process exit code.
func Execute(name string, args []string) int {
	if isPassthrough(args) {
		return passthroughStub(args)
	}
	cmd := newRootCmd(name)
	cmd.SetArgs(args)
	cmd.SetOut(os.Stdout)
	cmd.SetErr(os.Stderr)
	if err := cmd.Execute(); err != nil {
		// Cobra has already rendered the error to stderr. Translate
		// typed errors (e.g. *exitError) into specific exit codes so
		// scripts can branch on them.
		return exitCodeFromError(err)
	}
	return 0
}

// isPassthrough reports whether the first argument is a non-reserved command,
// in which case spec 11's passthrough router takes over (a placeholder stub
// in this spec).
//
// Deliberately simple: we only look at args[0]. Cases where flags precede the
// command (e.g. `bolt --log-level debug git clone …`) defer to Cobra and are
// handled fully in spec 11 — implementing flag-value awareness here would
// duplicate Cobra's parser.
func isPassthrough(args []string) bool {
	if len(args) == 0 {
		return false
	}
	first := args[0]
	if first == "" || first == "--" {
		return false
	}
	if first[0] == '-' {
		return false
	}
	return !isReserved(first)
}
