package cmd

import (
	"context"
	"fmt"
	"os"
	"slices"
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

	// Only the filters this metric actually applies. The server REFUSES an
	// unsupported filter with 400 rather than ignoring it, so offering one here
	// would turn a deliberate refusal into a routine user error.
	for _, f := range ep.Filters {
		fd, ok := api.FilterDefs[f]
		if !ok {
			continue
		}
		name := api.FlagName(f)
		desc := fd.Desc
		if len(fd.Enum) > 0 {
			desc += " [" + joinEnum(fd.Enum) + "]"
		}
		switch fd.Type {
		case "int":
			cmd.Flags().Int(name, 0, desc)
		default:
			cmd.Flags().String(name, "", desc)
		}
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
		if verbose {
			fmt.Fprintf(os.Stderr, "→ Using %s\n", resolved.Source)
		}

		from, _ := cmd.Flags().GetString("from")
		to, _ := cmd.Flags().GetString("to")
		// An omitted bound is left ABSENT so the server applies its own default.
		//
		// The CLI used to fill both from time.Now() on the machine running it, but
		// the spec defines every date as a day in the ACCOUNT's timezone and
		// documents its own defaults in those terms ("30 days before the account's
		// today"). Computing them host-side meant a host and a tenant on different
		// calendar days silently got a window shifted by a day — and, because the
		// values were then always sent, the server's correct defaults could never
		// apply.
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

		// FORMAT is checked per bound, whatever else is present. Leaving omitted
		// bounds absent meant the range check below no longer runs when only one
		// was given — and with it went the only local check on that one bound, so
		// `--from 2026-13-45` became a round trip to learn what the CLI already
		// knew.
		for _, b := range []struct{ flag, value string }{{"from", from}, {"to", to}} {
			if b.value == "" {
				continue
			}
			if err := validateDateFormat(b.flag, b.value); err != nil {
				return &api.ExitError{Err: err, Code: api.ExitValidation}
			}
		}

		// Ordering and the 365-day ceiling need BOTH bounds: there is nothing to
		// compare one against, and the server owns the other end.
		if from != "" && to != "" {
			if err := validateDateRange(from, to); err != nil {
				return &api.ExitError{Err: err, Code: api.ExitValidation}
			}
		}

		// Validate comparison flags
		if compareShorthand != "" {
			if compareFrom != "" || compareTo != "" {
				return &api.ExitError{
					Err:  fmt.Errorf("Cannot use --compare with --compare-from/--compare-to"),
					Code: api.ExitValidation,
				}
			}
			// Resolving a shorthand needs a concrete period to offset FROM. With a
			// bound absent the only options are to guess it host-side — the bug
			// just removed above — or to say so. Saying so is honest and the fix
			// is one flag away.
			if from == "" || to == "" {
				return &api.ExitError{
					Err: fmt.Errorf(
						"--compare %s needs an explicit period: pass --from and --to "+
							"(the default period is resolved in the account's timezone by the server, "+
							"so it cannot be offset locally)", compareShorthand),
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

		// The comparison period gets the same checks as the primary one. It was
		// only checked for being supplied in pairs: format, ordering and the
		// 365-day ceiling went unvalidated, so a reversed or malformed comparison
		// window was forwarded and came back as a server-side refusal for
		// something the CLI already knew was wrong.
		if compareFrom != "" {
			if err := validateDateRange(compareFrom, compareTo); err != nil {
				return &api.ExitError{
					Err:  fmt.Errorf("comparison period: %w", err),
					Code: api.ExitValidation,
				}
			}
		}

		// Validate enum flags
		if err := validateEnumFlags(cmd, ep); err != nil {
			return &api.ExitError{Err: err, Code: api.ExitValidation}
		}

		// Collect the per-metric filters and the endpoint-specific extras.
		//
		// Both loops distinguish "flag absent" from "flag set to a zero-ish
		// value". An absent flag means unfiltered, which is correct. A flag the
		// caller explicitly set to "" or to 0 is a mistake, and dropping it
		// silently returns a WIDER result set presented as a filtered one — the
		// same failure as sending a query key the server never accepted.
		extra := make(map[string]string)
		for _, f := range ep.Filters {
			fd, ok := api.FilterDefs[f]
			if !ok {
				continue
			}
			name := api.FlagName(f)
			if !cmd.Flags().Changed(name) {
				continue
			}
			switch fd.Type {
			case "int":
				v, _ := cmd.Flags().GetInt(name)
				if v < 1 {
					return &api.ExitError{
						Err:  fmt.Errorf("--%s must be >= 1 (got %d)", name, v),
						Code: api.ExitValidation,
					}
				}
				extra[f] = strconv.Itoa(v)
			default:
				v, _ := cmd.Flags().GetString(name)
				if v == "" {
					return &api.ExitError{
						Err:  fmt.Errorf("--%s was given an empty value; omit the flag to leave the result unfiltered", name),
						Code: api.ExitValidation,
					}
				}
				if len(fd.Enum) > 0 && !slices.Contains(fd.Enum, v) {
					return &api.ExitError{
						Err:  enumError(v, name, fd.Enum),
						Code: api.ExitValidation,
					}
				}
				extra[f] = v
			}
		}
		for _, f := range ep.ExtraFlags {
			if !cmd.Flags().Changed(f.Name) {
				continue
			}
			switch f.Type {
			case "string":
				v, _ := cmd.Flags().GetString(f.Name)
				if v == "" {
					return &api.ExitError{
						Err:  fmt.Errorf("--%s was given an empty value; omit the flag to leave the result unfiltered", f.Name),
						Code: api.ExitValidation,
					}
				}
				extra[f.Name] = v
			case "int":
				v, _ := cmd.Flags().GetInt(f.Name)
				// Min == 0 means UNBOUNDED, exactly as FlagDef.Min does in the
				// inventory lane. It previously defaulted to 1 here, so the same
				// field name meant opposite things in the two lanes and any future
				// statistics int flag where 0 is meaningful would be refused.
				if f.Min != 0 && v < f.Min {
					return &api.ExitError{
						Err:  fmt.Errorf("--%s must be >= %d (got %d)", f.Name, f.Min, v),
						Code: api.ExitValidation,
					}
				}
				// The spec's upper bound, refused locally rather than spent on a
				// round trip that comes back as a validation failure for a value
				// the flag's own help text already called out of range.
				if f.Max > 0 && v > f.Max {
					return &api.ExitError{
						Err:  fmt.Errorf("--%s must be <= %d (got %d)", f.Name, f.Max, v),
						Code: api.ExitValidation,
					}
				}
				extra[f.Name] = strconv.Itoa(v)
			case "bool":
				v, _ := cmd.Flags().GetBool(f.Name)
				if v {
					extra[f.Name] = "true"
				}
			}
		}

		params := &api.QueryParams{
			From:        from,
			To:          to,
			Granularity: granularity,
			CompareFrom: compareFrom,
			CompareTo:   compareTo,
			Extra:       extra,
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

// validateDateFormat checks one bound in isolation, so a malformed date is caught
// whether or not its partner was supplied.
func validateDateFormat(flag, value string) error {
	if _, err := time.Parse("2006-01-02", value); err != nil {
		return fmt.Errorf("invalid --%s date %q: %w", flag, value, err)
	}
	return nil
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
		if !slices.Contains(f.Enum, v) {
			return enumError(v, f.Name, f.Enum)
		}
	}
	return nil
}

// enumError is the one place the invalid-enum-value message is written. It had
// two independent copies, in the two loops that validate enums for the same
// command, so a wording change (or making matching case-insensitive) had to be
// made twice or the two halves of one command's validation diverged.
func enumError(value, flag string, allowed []string) error {
	return fmt.Errorf("invalid value %q for --%s (use %s)", value, flag, strings.Join(allowed, ", "))
}

func joinEnum(values []string) string {
	return strings.Join(values, "|")
}
