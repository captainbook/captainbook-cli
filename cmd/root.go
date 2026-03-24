package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/captainbook/captainbook-cli/internal/api"
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

	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(statsCmd())
	rootCmd.AddCommand(configCmd())
	rootCmd.AddCommand(completionCmd)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("ceebee", Version)
	},
}

// Execute runs the root command.
func Execute() {
	rootCmd.SilenceErrors = true
	rootCmd.SilenceUsage = true
	if err := rootCmd.Execute(); err != nil {
		var exitErr *api.ExitError
		if errors.As(err, &exitErr) {
			fmt.Fprintln(os.Stderr, exitErr.Err)
			os.Exit(exitErr.Code)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
