package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/captainbook/captainbook-cli/internal/api"
	"github.com/captainbook/captainbook-cli/internal/compare"
	"github.com/captainbook/captainbook-cli/internal/config"
	"github.com/captainbook/captainbook-cli/internal/output"
	"github.com/spf13/cobra"
)

func statsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Query statistics endpoints",
		Long:  "Query CaptainBook Statistics API endpoints. Use a subcommand for each endpoint.",
	}

	for i := range api.Endpoints {
		ep := &api.Endpoints[i]
		cmd.AddCommand(makeEndpointCmd(ep))
	}

	return cmd
}

func makeEndpointCmd(ep *api.Endpoint) *cobra.Command {
	cmd := &cobra.Command{
		Use:   ep.Name,
		Short: ep.Description,
		RunE:  makeRunFunc(ep),
	}

	// Common flags
	cmd.Flags().String("from", defaultFrom(), "Period start date (YYYY-MM-DD)")
	cmd.Flags().String("to", defaultTo(), "Period end date (YYYY-MM-DD)")
	cmd.Flags().String("granularity", "day", "Time series bucket: day|week|month|quarter|year")

	if !ep.HasExcludedFlag("business_unit_id") {
		cmd.Flags().Int("business-unit-id", 0, "Filter by business unit")
	}
	if !ep.HasExcludedFlag("product_id") {
		cmd.Flags().Int("product-id", 0, "Filter by product")
	}

	cmd.Flags().String("compare-from", "", "Comparison period start (YYYY-MM-DD)")
	cmd.Flags().String("compare-to", "", "Comparison period end (YYYY-MM-DD)")
	cmd.Flags().String("compare", "", "Comparison shorthand: previous|year-ago")

	// Endpoint-specific extra flags
	for _, f := range ep.ExtraFlags {
		switch f.Type {
		case "string":
			desc := f.Desc
			if len(f.Enum) > 0 {
				desc += " [" + joinEnum(f.Enum) + "]"
			}
			cmd.Flags().String(f.Name, f.Default, desc)
		case "int":
			def := 0
			if f.Default != "" {
				def, _ = strconv.Atoi(f.Default)
			}
			cmd.Flags().Int(f.Name, def, f.Desc)
		case "bool":
			cmd.Flags().Bool(f.Name, false, f.Desc)
		}
	}

	return cmd
}

func makeRunFunc(ep *api.Endpoint) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		resolved, err := config.Resolve(profileName)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(api.ExitConfig)
		}

		client := api.NewClient(resolved.URL, resolved.Token)
		client.Verbose = verbose
		client.VerboseW = os.Stderr

		from, _ := cmd.Flags().GetString("from")
		to, _ := cmd.Flags().GetString("to")
		granularity, _ := cmd.Flags().GetString("granularity")
		compareFrom, _ := cmd.Flags().GetString("compare-from")
		compareTo, _ := cmd.Flags().GetString("compare-to")
		compareShorthand, _ := cmd.Flags().GetString("compare")

		// Validate date range (max 365 days)
		if err := validateDateRange(from, to); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(api.ExitValidation)
		}

		// Handle comparison shorthand
		if compareShorthand != "" {
			if compareFrom != "" || compareTo != "" {
				fmt.Fprintln(os.Stderr, "Cannot use --compare with --compare-from/--compare-to")
				os.Exit(api.ExitValidation)
			}
			var err error
			compareFrom, compareTo, err = compare.Resolve(compareShorthand, from, to)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(api.ExitValidation)
			}
		}

		var businessUnitID, productID int
		if cmd.Flags().Lookup("business-unit-id") != nil {
			businessUnitID, _ = cmd.Flags().GetInt("business-unit-id")
		}
		if cmd.Flags().Lookup("product-id") != nil {
			productID, _ = cmd.Flags().GetInt("product-id")
		}

		// Collect extra flags
		extra := make(map[string]string)
		for _, f := range ep.ExtraFlags {
			switch f.Type {
			case "string":
				v, _ := cmd.Flags().GetString(f.Name)
				if v != "" {
					extra[f.Name] = v
				}
			case "int":
				v, _ := cmd.Flags().GetInt(f.Name)
				if v != 0 {
					extra[f.Name] = strconv.Itoa(v)
				}
			case "bool":
				v, _ := cmd.Flags().GetBool(f.Name)
				if v {
					extra[f.Name] = "true"
				}
			}
		}

		params := &api.QueryParams{
			From:           from,
			To:             to,
			Granularity:    granularity,
			BusinessUnitID: businessUnitID,
			ProductID:      productID,
			CompareFrom:    compareFrom,
			CompareTo:      compareTo,
			Extra:          extra,
		}

		ctx := context.Background()
		body, err := client.Do(ctx, ep, params)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(api.ExitCodeFor(err))
		}

		// Check for empty data
		var envelope api.StatisticsResponse
		if json.Unmarshal(body, &envelope) == nil {
			if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
				fmt.Fprintln(os.Stderr, "No data returned for this period.")
			}
		}

		if err := output.Format(os.Stdout, body, formatFlag); err != nil {
			fmtErr := &api.JSONParseError{Err: err}
			fmt.Fprintln(os.Stderr, fmtErr)
			os.Exit(api.ExitCodeFor(fmtErr))
		}

		return nil
	}
}

func defaultFrom() string {
	return time.Now().AddDate(0, 0, -30).Format("2006-01-02")
}

func defaultTo() string {
	return time.Now().Format("2006-01-02")
}

func validateDateRange(from, to string) error {
	fromDate, err := time.Parse("2006-01-02", from)
	if err != nil {
		return fmt.Errorf("invalid from date %q: %w", from, err)
	}
	toDate, err := time.Parse("2006-01-02", to)
	if err != nil {
		return fmt.Errorf("invalid to date %q: %w", to, err)
	}
	if toDate.Before(fromDate) {
		return fmt.Errorf("to date %s is before from date %s", to, from)
	}
	if toDate.Sub(fromDate).Hours()/24 > 365 {
		return fmt.Errorf("date range exceeds 365 days (from %s to %s)", from, to)
	}
	return nil
}

func joinEnum(values []string) string {
	return strings.Join(values, "|")
}
