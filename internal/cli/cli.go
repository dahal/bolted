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

// isPassthrough reports whether the args should be handled by spec 11's
// passthrough router instead of Cobra.
//
// Routing rules:
//   - Empty args → Cobra (prints help).
//   - A leading `--` forces passthrough regardless of what follows
//     (AC 4: `bolt -- ls /etc` runs system ls, NOT the reserved `bolt ls`).
//   - Passthrough flags handled at this layer — `--cwd <path>` and
//     `--cwd=<path>` — are skipped over before deciding. This means
//     `bolt --cwd foo git status` correctly routes to passthrough.
//   - Any other leading flag (e.g. `--log-level`, `-h`) defers to Cobra
//     so Bolted's own global flags keep working. Spec 11 does NOT
//     try to replicate Cobra's full flag parser here.
//   - Otherwise the first positional arg decides: reserved → Cobra,
//     anything else → passthrough.
func isPassthrough(args []string) bool {
	if len(args) == 0 {
		return false
	}
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "" {
			return false
		}
		if a == "--" {
			// Force passthrough; the router strips the `--` and
			// treats the rest as a literal command.
			return true
		}
		if a == "--cwd" {
			// Consume the value too. If the value is missing we
			// still route to passthrough so the router can emit a
			// consistent error message.
			i += 2
			continue
		}
		if len(a) > len("--cwd=") && a[:len("--cwd=")] == "--cwd=" {
			i++
			continue
		}
		if a[0] == '-' {
			return false
		}
		return !isReserved(a)
	}
	// All args were passthrough flags with no command — let the router
	// surface the "no command given" diagnostic.
	return true
}
