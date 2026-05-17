package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// stubSpec maps a stub command name to the spec that will implement it. The
// stub error includes this pointer so a user (or agent) hitting an
// unimplemented command knows where to look.
var stubSpec = map[string]string{
	"dev":       "spec 13",
	"exec":      "spec 13",
	"stop":      "spec 13",
	"ls":        "spec 13",
	"rm":        "spec 13",
	"ports":     "spec 14",
	"forward":   "spec 20",
	"unforward": "spec 20",
	"provision": "spec 15",
	"profiles":  "spec 16",
	"trust":     "spec 18",
	"logs":      "spec 20",
	"export":    "spec 20",
	"import":    "spec 20",
	"restart":   "spec 20",
	"config":    "spec 03",
}

// commandsProvidedByCobra are reserved names we do NOT register stubs for:
// Cobra provides them itself.
var commandsProvidedByCobra = map[string]bool{
	"help":       true,
	"completion": true,
}

// registerSubcommands attaches every reserved subcommand to the root command.
// Implemented commands (currently only "version") replace the corresponding
// stub. Cobra-provided commands are skipped (Cobra adds them automatically).
func registerSubcommands(root *cobra.Command) {
	root.AddCommand(newVersionCmd())
	root.AddCommand(newInitCmd())
	root.AddCommand(newUnlockCmd())
	root.AddCommand(newLockCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newShellCmd())
	root.AddCommand(newPasswdCmd())
	root.AddCommand(newKeychainCmd())

	implemented := map[string]bool{
		"version":  true,
		"init":     true,
		"unlock":   true,
		"lock":     true,
		"status":   true,
		"shell":    true,
		"passwd":   true,
		"keychain": true,
	}
	for _, name := range reservedSubcommands {
		if implemented[name] || commandsProvidedByCobra[name] {
			continue
		}
		root.AddCommand(newStubCmd(name))
	}
}

func newStubCmd(name string) *cobra.Command {
	spec := stubSpec[name]
	short := "Not yet implemented"
	if spec != "" {
		short = "Not yet implemented (" + spec + ")"
	}
	return &cobra.Command{
		Use:   name,
		Short: short,
		RunE: func(_ *cobra.Command, _ []string) error {
			if spec != "" {
				return fmt.Errorf("%s: not yet implemented — see %s", name, spec)
			}
			return fmt.Errorf("%s: not yet implemented", name)
		},
	}
}
