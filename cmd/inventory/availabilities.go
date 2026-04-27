package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/captainbook/captainbook-cli/internal/inventory/gen"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/spf13/cobra"
)

// availabilitiesCmd builds the `inventory availabilities` subtree. Most
// resources are bound via bindCommands(); availabilities is special
// because of D38: bulk-update is split into 5 per-setting subcommands
// (capacity, booking-status, pricing, start-time, end-time), each typed
// cleanly. We hand-build the parent here and nest list/get/update + the
// `bulk-update` sub-tree under it.
func availabilitiesCmd(runner *Runner) *cobra.Command {
	parent := &cobra.Command{
		Use:   "availabilities",
		Short: "Manage availabilities (slots / sessions)",
	}
	bindCommands(parent, availabilitiesDefs(), runner)
	parent.AddCommand(bulkUpdateCmd(runner))
	return parent
}

// availabilitiesDefs returns the regular (non-bulk-update) command tree.
// Bulk-update lives in bulkUpdateCmd because it splits into 5 per-setting
// subcommands constructed dynamically via bulkUpdateDef. The split is
// expressed at the cobra layer; the per-setting CommandDefs share the
// same HTTP path and only differ in their typed flags + StaticForensic.
func availabilitiesDefs() []CommandDef {
	return []CommandDef{
		{
			Use: "list", Short: "List availabilities", Kind: KindRead,
			Verb: "GET", Path: "/availabilities", Ability: invpkg.Read,
			Flags: []FlagDef{
				{Name: "limit", Type: "int"},
				{Name: "cursor", Type: "string"},
				{Name: "product-option-id", Type: "string"},
				{Name: "from", Type: "string", Description: "Date from (YYYY-MM-DD)"},
				{Name: "to", Type: "string", Description: "Date to (YYYY-MM-DD)"},
				{Name: "has-capacity", Type: "bool", Description: "Only slots with remaining capacity"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				p := &gen.ListAvailabilitiesParams{}
				if v := args.FlagInt("limit"); v != 0 {
					p.Limit = &v
				}
				if v := args.FlagString("cursor"); v != "" {
					p.Cursor = &v
				}
				if v := args.FlagString("product-option-id"); v != "" {
					p.ProductOptionId = &v
				}
				if v := args.FlagString("from"); v != "" {
					d, err := parseDate(v)
					if err != nil {
						return nil, fmt.Errorf("--from: %w", err)
					}
					p.From = &d
				}
				if v := args.FlagString("to"); v != "" {
					d, err := parseDate(v)
					if err != nil {
						return nil, fmt.Errorf("--to: %w", err)
					}
					p.To = &d
				}
				if args.FlagSet("has-capacity") {
					b := args.FlagBool("has-capacity")
					p.HasCapacity = &b
				}
				resp, err := r.Client.ListAvailabilitiesWithResponse(ctx, p)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Availability", "")
			},
		},
		{
			Use: "get <id>", Short: "Show one availability", Kind: KindRead,
			Verb: "GET", Path: "/availabilities/{id}", Ability: invpkg.Read,
			PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.ShowAvailabilityWithResponse(ctx, id)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Availability", id)
			},
		},
		{
			Use: "update <id>", Short: "Update one availability", Kind: KindMutation,
			Verb: "PATCH", Path: "/availabilities/{id}", Ability: invpkg.Write,
			DryRunMode: DryRunBody, PositionalArgs: []string{"id"},
			Flags: []FlagDef{
				{Name: "capacity", Type: "int", Description: "Slot capacity"},
				{Name: "status", Type: "string", Description: "available|blocked|cancelled"},
			},
			ForensicFields: []string{"capacity", "status"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"capacity": "capacity",
					"status":   "status",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.UpdateAvailabilityWithBodyWithResponse(ctx, id, &gen.UpdateAvailabilityParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return nil, err
				}
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Availability", id)
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
		{
			Use: "restore <id>", Short: "Restore a soft-deleted availability",
			// Restoration is a PATCH against /availabilities/{id} with a
			// restore-shaped body (the spec has no dedicated restore
			// endpoint — see Long below). Verb tracks the wire request
			// so audit + access logs correlate.
			Kind: KindMutation, Verb: "PATCH", Path: "/availabilities/{id}",
			Ability: invpkg.Write, DryRunMode: DryRunBody,
			PositionalArgs: []string{"id"},
			Long: "The spec defines no dedicated /availabilities/{id}/restore endpoint; " +
				"restoration is performed via UpdateAvailability with a restore-shaped body. " +
				"Pass the body via --data; this command accepts no typed flags beyond --dry-run.",
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				// Availability "restore" is implemented at the spec level
				// as a generic update with a restore-shaped body; there is
				// no dedicated restore endpoint in the gen client today.
				// We reuse update to keep the surface consistent.
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, nil)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.UpdateAvailabilityWithBodyWithResponse(ctx, id, &gen.UpdateAvailabilityParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return nil, err
				}
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Availability", id)
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
	}
}

// bulkUpdateCmd builds the `availabilities bulk-update` parent + 5
// per-setting subcommands. Each typed subcommand constructs the
// BulkUpdateAvailabilityRequest body with the right `setting` discriminator
// + `new_value` shape.
//
// On 202, the runner emits "BULK_UPDATE_ACCEPTED bulk_update_id=<uuid>"
// to stderr (D31) and exits 0. Forensic fields capture (setting, from, to,
// product_option_id, new_value).
func bulkUpdateCmd(runner *Runner) *cobra.Command {
	parent := &cobra.Command{
		Use:   "bulk-update",
		Short: "Bulk-update availabilities (5 per-setting subcommands)",
		Long: "Each subcommand corresponds to one setting in the spec's BulkUpdateAvailabilityRequest " +
			"discriminator. The server returns 202 + bulk_update_id; the CLI emits the stderr signal " +
			"BULK_UPDATE_ACCEPTED bulk_update_id=<uuid> (D31) and exits 0.",
	}

	bindCommands(parent, []CommandDef{
		bulkUpdateDef("capacity",
			"Bulk update slot capacity",
			[]FlagDef{
				{Name: "operator", Type: "string", Required: true, Description: "SET|INCREASE_BY|DECREASE_BY"},
				{Name: "value", Type: "int", Required: true, Description: "Capacity value (or delta)"},
			},
			func(args RunArgs) (any, error) {
				return map[string]any{
					"operator": args.FlagString("operator"),
					"value":    args.FlagInt("value"),
				}, nil
			},
		),
		bulkUpdateDef("booking-status",
			"Bulk update bookable flag (open / close slots)",
			[]FlagDef{
				{Name: "is-bookable", Type: "bool", Required: true, Description: "true to open, false to close"},
			},
			func(args RunArgs) (any, error) {
				return map[string]any{
					"is_bookable": args.FlagBool("is-bookable"),
				}, nil
			},
		),
		bulkUpdateDef("pricing",
			"Bulk update pricing fares (replace specified tiers)",
			[]FlagDef{
				{Name: "fares", Type: "string", Required: true, Description: "JSON array: [{pricing_tier_id, amount}, ...]"},
			},
			func(args RunArgs) (any, error) {
				var fares []any
				if err := json.Unmarshal([]byte(args.FlagString("fares")), &fares); err != nil {
					return nil, fmt.Errorf("--fares: invalid JSON: %w", err)
				}
				return map[string]any{"fares": fares}, nil
			},
		),
		bulkUpdateDef("start-time",
			"Bulk update slot start time (and optional day-count for multi-day)",
			[]FlagDef{
				{Name: "start-time", Type: "string", Required: true, Description: "HH:MM"},
				{Name: "end-time", Type: "string", Required: true, Description: "HH:MM"},
				{Name: "day-count", Type: "int", Description: "Days the activity spans"},
			},
			func(args RunArgs) (any, error) {
				v := map[string]any{
					"start_time": args.FlagString("start-time"),
					"end_time":   args.FlagString("end-time"),
				}
				if args.FlagSet("day-count") {
					v["day_count"] = args.FlagInt("day-count")
				}
				return v, nil
			},
		),
		bulkUpdateDef("end-time",
			"Bulk update slot end time",
			[]FlagDef{
				{Name: "start-time", Type: "string", Required: true, Description: "HH:MM"},
				{Name: "end-time", Type: "string", Required: true, Description: "HH:MM"},
				{Name: "day-count", Type: "int", Description: "Days the activity spans"},
			},
			func(args RunArgs) (any, error) {
				v := map[string]any{
					"start_time": args.FlagString("start-time"),
					"end_time":   args.FlagString("end-time"),
				}
				if args.FlagSet("day-count") {
					v["day_count"] = args.FlagInt("day-count")
				}
				return v, nil
			},
		),
	}, runner)

	return parent
}

// bulkUpdateDef returns one CommandDef for an `availabilities bulk-update
// <setting>` subcommand. settingName is the kebab-case CLI form; the
// underlying spec setting is the same with `-` → `_`.
func bulkUpdateDef(settingName, short string, perSettingFlags []FlagDef, newValueFn func(RunArgs) (any, error)) CommandDef {
	// Common to every bulk-update setting: the temporal/scoping fields.
	commonFlags := []FlagDef{
		{Name: "from", Type: "string", Required: true, Description: "Date range start (YYYY-MM-DD)"},
		{Name: "to", Type: "string", Required: true, Description: "Date range end (YYYY-MM-DD)"},
		{Name: "product-option-id", Type: "string", Required: true, Description: "Target product option ID"},
	}
	flags := append([]FlagDef{}, commonFlags...)
	flags = append(flags, perSettingFlags...)

	// Forensic fields per D37 + plan §"async bulk-update": capture
	// setting, from, to, product_option_id, plus the per-setting
	// new_value fields.
	forensic := []string{"from", "to", "product-option-id"}
	for _, f := range perSettingFlags {
		forensic = append(forensic, f.Name)
	}

	specSetting := strings.ReplaceAll(settingName, "-", "_")

	return CommandDef{
		Use: settingName, Short: short,
		Kind: KindMutation, Verb: "POST", Path: "/availabilities/bulk-update",
		Ability: invpkg.Write, DryRunMode: DryRunBody,
		Flags:          flags,
		ForensicFields: forensic,
		// All 5 bulk-update subcommands hit the same HTTP path; the
		// `setting` discriminator is what distinguishes them. Static
		// forensic ensures audit_summary records which one ran.
		StaticForensic: map[string]any{"setting": specSetting},
		Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
			from, err := parseDate(args.FlagString("from"))
			if err != nil {
				return nil, fmt.Errorf("--from: %w", err)
			}
			to, err := parseDate(args.FlagString("to"))
			if err != nil {
				return nil, fmt.Errorf("--to: %w", err)
			}
			newValue, err := newValueFn(args)
			if err != nil {
				return nil, err
			}
			body := map[string]any{
				"setting":           specSetting,
				"from":              from.Format("2006-01-02"),
				"to":                to.Format("2006-01-02"),
				"product_option_id": args.FlagString("product-option-id"),
				"new_value":         newValue,
			}
			if args.DryRun {
				body["dry_run"] = true
			}
			raw, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			resp, err := r.Client.BulkUpdateAvailabilitiesWithBodyWithResponse(ctx, &gen.BulkUpdateAvailabilitiesParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(raw))
			if err != nil {
				return nil, err
			}
			res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Availability", "")
			if res != nil {
				res.WireBody = raw
			}
			return res, err
		},
	}
}

// _ ensures we keep the openapi_types import for future expansion (the
// Date type is reachable via parseDate but the symbol isn't named here).
var _ = openapi_types.Date{}

// _ keeps http.StatusAccepted in scope for documentation purposes; the
// 202-handling lives in ParseGenResponse.
var _ = http.StatusAccepted
