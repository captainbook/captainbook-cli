package cmd

import (
	"fmt"
	"os"

	"github.com/captainbook/captainbook-cli/internal/config"
	"github.com/spf13/cobra"
)

func configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage API configuration profiles",
	}

	cmd.AddCommand(configAddCmd())
	cmd.AddCommand(configListCmd)
	cmd.AddCommand(configRemoveCmd)
	cmd.AddCommand(configUseCmd)

	return cmd
}

func configAddCmd() *cobra.Command {
	var url, token string

	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add or update a configuration profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if url == "" {
				return fmt.Errorf("--url is required")
			}
			if token == "" {
				return fmt.Errorf("--token is required")
			}

			if err := config.AddProfile(name, url, token); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "Profile %q saved.\n", name)
			return nil
		},
	}

	cmd.Flags().StringVar(&url, "url", "", "API base URL (required)")
	cmd.Flags().StringVar(&token, "token", "", "API bearer token (required)")
	_ = cmd.MarkFlagRequired("url")
	_ = cmd.MarkFlagRequired("token")

	return cmd
}

var configListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all configuration profiles",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.ListProfiles()
		if err != nil {
			return err
		}

		if len(cfg.Profiles) == 0 {
			fmt.Println("No profiles configured.")
			return nil
		}

		for name, p := range cfg.Profiles {
			marker := " "
			if name == cfg.DefaultProfile {
				marker = "*"
			}
			fmt.Printf("%s %s\t%s\n", marker, name, p.URL)
		}
		return nil
	},
}

var configRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a configuration profile",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := config.RemoveProfile(args[0]); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Profile %q removed.\n", args[0])
		return nil
	},
}

var configUseCmd = &cobra.Command{
	Use:   "use <name>",
	Short: "Set the default configuration profile",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := config.SetDefault(args[0]); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Default profile set to %q.\n", args[0])
		return nil
	},
}
