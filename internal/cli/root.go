package cli

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"
)

// newRootCmd builds the root cobra.Command for the given invocation name.
// Subcommands are registered via registerSubcommands so each spec can plug in
// its own commands as they land.
func newRootCmd(invoked string) *cobra.Command {
	var logLevel string
	cmd := &cobra.Command{
		Use:           invoked,
		Short:         "A password-locked, encrypted Linux dev environment CLI",
		Long:          longDescription(invoked),
		SilenceUsage:  true,
		SilenceErrors: false,
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			return configureLogger(logLevel)
		},
	}
	cmd.PersistentFlags().StringVar(&logLevel, "log-level", "warn",
		"Log level (debug|info|warn|error)")
	registerSubcommands(cmd)
	return cmd
}

func longDescription(invoked string) string {
	return fmt.Sprintf(
		"%s gives you a password-locked, encrypted Linux dev environment on "+
			"Mac and Windows. Run normal dev commands prefixed with %q and "+
			"they execute inside an isolated, encrypted VM.",
		invoked, invoked,
	)
}

// configureLogger initialises slog's default handler from a string level.
func configureLogger(level string) error {
	lvl, err := parseLogLevel(level)
	if err != nil {
		return err
	}
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	slog.SetDefault(slog.New(h))
	return nil
}

func parseLogLevel(s string) (slog.Level, error) {
	switch s {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level %q (want debug|info|warn|error)", s)
	}
}
