package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dahal/bolted/internal/buildinfo"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the Bolted version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), buildinfo.String())
			return err
		},
	}
}
