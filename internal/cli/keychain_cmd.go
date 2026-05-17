package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// newKeychainCmd builds the `bolt keychain` command tree. Today the only
// subcommand is `forget`, which removes any cached Bolted password
// from the OS keychain. The parent has no RunE — running `bolt keychain`
// alone prints usage via Cobra's default help handler.
func newKeychainCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "keychain",
		Short: "Manage the OS-keychain cache for the Bolted password",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newKeychainForgetCmd())
	return cmd
}

// newKeychainForgetCmd builds `bolt keychain forget`.
func newKeychainForgetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "forget",
		Short: "Remove the Bolted password from the OS keychain",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runKeychainForget(cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
}

// runKeychainForget removes the cached Bolted password. Deletion
// is idempotent in every backend we ship (mac / windows / noop), so a
// "no such entry" return is treated as success — the user's intent
// ("make sure it's gone") is satisfied either way.
func runKeychainForget(_ io.Writer, stderr io.Writer) error {
	store := newKeychainStoreFn()
	if err := store.Delete(keychainServiceName, keychainAccountName); err != nil {
		return fmt.Errorf("keychain forget: %w", err)
	}
	fmt.Fprintln(stderr, "Bolted password removed from keychain.")
	return nil
}
