package cmd

import (
	"errors"
	"fmt"
	"os"

	cmdinventory "github.com/captainbook/captainbook-cli/cmd/inventory"
	"github.com/captainbook/captainbook-cli/internal/api"
	"github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/spf13/cobra"
)

// Version is set via ldflags at build time.
var Version = "dev"

var (
	verbose     bool
	profileName string
	formatFlag  string
)

var rootCmd = &cobra.Command{
	Use:   "ceebee",
	Short: "CaptainBook Statistics CLI",
	Long:  "Query the CaptainBook Statistics API from the command line.",
}

func init() {
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Debug output to stderr")
	rootCmd.PersistentFlags().StringVar(&profileName, "profile", "", "Config profile to use")
	rootCmd.PersistentFlags().StringVarP(&formatFlag, "format", "f", "json", "Output format: json, table, csv")

	// PersistentPreRun on the root mirrors the global flags into the
	// inventory package so newRunner can resolve config + verbose without
	// reaching into rootCmd's flagset (decoupling cmd/root from
	// cmd/inventory). Lives here, runs once per invocation, before any
	// subcommand's RunE.
	rootCmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		cmdinventory.ProfileFlag = profileName
		cmdinventory.VerboseFlag = verbose
	}

	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(statsCmd())
	rootCmd.AddCommand(configCmd())
	rootCmd.AddCommand(auditCmd())
	rootCmd.AddCommand(completionCmd)
	rootCmd.AddCommand(cmdinventory.Cmd())
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("ceebee", Version)
	},
}

// Execute runs the root command. The error path renders, in order of
// precedence: typed exit errors (api.ExitError) → typed inventory errors that
// implement inventory.UserMessenger → fallback to err.Error(). UserMessenger
// support means inventory typed errors (IdempotencyConflictError, etc.) print
// their crisp UserMessage to stderr instead of the developer-facing Error().
func Execute() {
	rootCmd.SilenceErrors = true
	rootCmd.SilenceUsage = true
	if err := rootCmd.Execute(); err != nil {
		var exitErr *api.ExitError
		if errors.As(err, &exitErr) {
			fmt.Fprintln(os.Stderr, exitErr.Err)
			os.Exit(exitErr.Code)
		}
		var um inventory.UserMessenger
		if errors.As(err, &um) {
			fmt.Fprintln(os.Stderr, um.UserMessage())
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
