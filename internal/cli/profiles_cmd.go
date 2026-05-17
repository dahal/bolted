// profiles_cmd.go implements `bolt profiles` — a read-only listing of
// the vendored starter profiles a user can pass to `bolt init --profile`.
// All data lives in internal/profiles; this file is pure presentation.
package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/dahal/bolted/internal/profiles"
)

// profilesLister is an indirection point so the test can swap in a
// deterministic fixture without depending on the actual embedded
// profile set.
var profilesLister = profiles.List

type profilesOptions struct {
	jsonOut bool
}

func newProfilesCmd() *cobra.Command {
	opts := &profilesOptions{}
	cmd := &cobra.Command{
		Use:   "profiles",
		Short: "List the starter bolted.yaml profiles available to `bolt init --profile`",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runProfiles(cmd.OutOrStdout(), *opts)
		},
	}
	cmd.Flags().BoolVar(&opts.jsonOut, "json", false, "Emit machine-readable JSON instead of the human-readable listing")
	return cmd
}

func runProfiles(stdout io.Writer, opts profilesOptions) error {
	entries := profilesLister()

	if opts.jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(entries); err != nil {
			return fmt.Errorf("encode JSON: %w", err)
		}
		return nil
	}

	for _, e := range entries {
		if _, err := fmt.Fprintf(stdout, "%s — %s\n", e.Name, e.Description); err != nil {
			return fmt.Errorf("write listing: %w", err)
		}
	}
	return nil
}
