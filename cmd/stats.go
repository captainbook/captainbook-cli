package cmd

import (
	"context"
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

	// Common flags (defaults computed at execution time, not init time)
	cmd.Flags().String("from", "", "Period start date (YYYY-MM-DD, default: 30 days ago)")
	cmd.Flags().String("to", "", "Period end date (YYYY-MM-DD, default: today)")
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
			return &api.ExitError{Err: err, Code: api.ExitConfig}
		}

		client := api.NewClient(resolved.URL, resolved.Token)
		client.Verbose = verbose
		client.VerboseW = os.Stderr

		from, _ := cmd.Flags().GetString("from")
		to, _ := cmd.Flags().GetString("to")
		if from == "" {
			from = defaultFrom()
		}
		if to == "" {
			to = defaultTo()
		}
		granularity, _ := cmd.Flags().GetString("granularity")
		compareFrom, _ := cmd.Flags().GetString("compare-from")
		compareTo, _ := cmd.Flags().GetString("compare-to")
		compareShorthand, _ := cmd.Flags().GetString("compare")

		// Validate format before making the API call
		if !output.ValidFormat(formatFlag) {
			return &api.ExitError{
				Err:  fmt.Errorf("Unknown format %q (use json, table, or csv)", formatFlag),
				Code: api.ExitValidation,
			}
		}

		// Validate granularity
		if err := validateGranularity(granularity); err != nil {
			return &api.ExitError{Err: err, Code: api.ExitValidation}
		}

		// Validate date range (max 365 days)
		if err := validateDateRange(from, to); err != nil {
			return &api.ExitError{Err: err, Code: api.ExitValidation}
		}

		// Validate comparison flags
		if compareShorthand != "" {
			if compareFrom != "" || compareTo != "" {
				return &api.ExitError{
					Err:  fmt.Errorf("Cannot use --compare with --compare-from/--compare-to"),
					Code: api.ExitValidation,
				}
			}
			var err error
			compareFrom, compareTo, err = compare.Resolve(compareShorthand, from, to)
			if err != nil {
				return &api.ExitError{Err: err, Code: api.ExitValidation}
			}
		} else if (compareFrom == "") != (compareTo == "") {
			return &api.ExitError{
				Err:  fmt.Errorf("--compare-from and --compare-to must be used together"),
				Code: api.ExitValidation,
			}
		}

		// Validate enum flags
		if err := validateEnumFlags(cmd, ep); err != nil {
			return &api.ExitError{Err: err, Code: api.ExitValidation}
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
			return &api.ExitError{Err: err, Code: api.ExitCodeFor(err)}
		}

		if err := output.Format(os.Stdout, body, formatFlag); err != nil {
			return &api.ExitError{
				Err:  &api.JSONParseError{Err: err},
				Code: api.ExitJSONParse,
			}
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

var validGranularities = []string{"day", "week", "month", "quarter", "year"}

func validateGranularity(g string) error {
	for _, v := range validGranularities {
		if g == v {
			return nil
		}
	}
	return fmt.Errorf("invalid granularity %q (use %s)", g, strings.Join(validGranularities, ", "))
}

func validateEnumFlags(cmd *cobra.Command, ep *api.Endpoint) error {
	for _, f := range ep.ExtraFlags {
		if f.Type != "string" || len(f.Enum) == 0 {
			continue
		}
		v, _ := cmd.Flags().GetString(f.Name)
		if v == "" {
			continue
		}
		valid := false
		for _, allowed := range f.Enum {
			if v == allowed {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("invalid value %q for --%s (use %s)", v, f.Name, strings.Join(f.Enum, ", "))
		}
	}
	return nil
}

func joinEnum(values []string) string {
	return strings.Join(values, "|")
}
