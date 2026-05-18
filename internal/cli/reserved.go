package cli

// reservedSubcommands is the single source of truth for which command names
// belong to Bolted itself. Anything not in this list is routed to the
// passthrough handler (spec 11) and executed inside the VM verbatim.
//
// Keep this list alphabetised; the alphabetised invariant is enforced by
// TestReservedSubcommands_AlphabetisedAndUnique.
var reservedSubcommands = []string{
	"completion", // Cobra's built-in shell completion command
	"config",
	"dev",
	"exec",
	"export",
	"forward",
	"help", // Cobra's built-in help command
	"import",
	"init",
	"keychain",
	"lock",
	"logs",
	"ls",
	"passwd",
	"password", // alias for `passwd`; kept reserved so passthrough doesn't grab it
	"ports",
	"profiles",
	"provision",
	"restart",
	"rm",
	"shell",
	"status",
	"stop",
	"trust",
	"unforward",
	"unlock",
	"version",
}

// reservedSet enables O(1) lookups in isReserved.
var reservedSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(reservedSubcommands))
	for _, s := range reservedSubcommands {
		m[s] = struct{}{}
	}
	return m
}()

// isReserved reports whether name is a reserved Bolted subcommand.
func isReserved(name string) bool {
	_, ok := reservedSet[name]
	return ok
}

// ReservedSubcommands returns a copy of the reserved subcommand list. Useful
// for tests and for spec 11's passthrough router.
func ReservedSubcommands() []string {
	out := make([]string, len(reservedSubcommands))
	copy(out, reservedSubcommands)
	return out
}
