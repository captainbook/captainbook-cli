package inventory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/captainbook/captainbook-cli/internal/inventory/gen"
	"github.com/spf13/cobra"
)

// Shared date-range flag descriptions. The Availability list / bulk-delete
// / bulk-update endpoints all use a half-open `[from, to)` window per the
// CLI v1 spec — `from` inclusive, `to` exclusive. Operators have shipped
// duplicate boundary-day slots by reading `--to` as inclusive, so every
// flag that takes one of these dates points at the same string.
const (
	availFromDesc = "Date range start, inclusive (YYYY-MM-DD). Range is half-open [from, to)."
	availToDesc   = "Date range end, exclusive (YYYY-MM-DD). Range is half-open [from, to)."
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
	bindCommands(parent, []CommandDef{bulkDeleteDef()}, runner)
	return parent
}

// bulkDeleteDef is the synchronous `availabilities bulk-delete` command.
// Mirrors the bulk-update filter shape (product_option_id + half-open
// [from, to)) but the response carries `total_deleted` directly — there's
// no async job, no stderr signal, no polling. 409
// AVAILABILITY_HAS_CONFIRMED_BOOKING aborts the entire request before any
// row is touched.
func bulkDeleteDef() CommandDef {
	return CommandDef{
		Use: "bulk-delete", Short: "Bulk soft-delete availabilities across a date range (synchronous)",
		Kind: KindMutation, Verb: "POST", Path: "/availabilities/bulk-delete",
		Ability: invpkg.Write, DryRunMode: DryRunBody,
		Long: "Soft-deletes every availability of one product option in [from, to). " +
			"Synchronous — response carries `total_deleted`. 409 " +
			"AVAILABILITY_HAS_CONFIRMED_BOOKING is returned if any matched row has " +
			"a confirmed Booking attached (entire request rejected, no rows touched); " +
			"`error.details.total_blocked` + `sample_availability_ids` (up to 20) " +
			"identify the blockers. The confirmed-booking precheck runs even on " +
			"--dry-run.",
		Flags: []FlagDef{
			{Name: "product-option-id", Type: "string", Required: true, Description: "Target product option ID"},
			{Name: "from", Type: "string", Required: true, Description: availFromDesc},
			{Name: "to", Type: "string", Required: true, Description: availToDesc},
		},
		ForensicFields: []string{"product-option-id", "from", "to"},
		Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
			from, err := parseDate(args.FlagString("from"))
			if err != nil {
				return nil, fmt.Errorf("--from: %w", err)
			}
			to, err := parseDate(args.FlagString("to"))
			if err != nil {
				return nil, fmt.Errorf("--to: %w", err)
			}
			// Mirror bulk-update: start with --data (if any) as the base,
			// then overlay typed fields. Typed flags always WIN so the
			// body shape stays correct even if --data tries to override
			// them.
			body := map[string]any{}
			if len(args.RawData) > 0 {
				if err := json.Unmarshal(args.RawData, &body); err != nil {
					return nil, fmt.Errorf("--data: invalid JSON: %w", err)
				}
			}
			body["product_option_id"] = args.FlagString("product-option-id")
			body["from"] = from.Format("2006-01-02")
			body["to"] = to.Format("2006-01-02")
			if args.DryRun {
				body["dry_run"] = true
			}
			raw, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			resp, err := r.Client.BulkDeleteAvailabilitiesWithBodyWithResponse(ctx, &gen.BulkDeleteAvailabilitiesParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(raw))
			if err != nil {
				return &RunResult{WireBody: raw}, err
			}
			res, perr := ParseGenResponse(resp.Body, resp.HTTPResponse, "Availability", "")
			if res != nil {
				res.WireBody = raw
			}
			return res, perr
		},
	}
}

// dryRunBodyEditor returns a RequestEditorFn that attaches a JSON body to
// a request the gen client built without one. Used by `availabilities
// delete <id>` to send `{"dry_run": true}` on the DELETE — the spec
// supports a dry-run body but doesn't formally declare a requestBody, so
// codegen produces no *WithBody variant. Without this we couldn't preview
// the soft-delete (or trigger the 409 confirmed-booking precheck) before
// committing.
func dryRunBodyEditor(body []byte) gen.RequestEditorFn {
	return func(_ context.Context, req *http.Request) error {
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}
		req.ContentLength = int64(len(body))
		req.Header.Set("Content-Type", "application/json")
		return nil
	}
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
				{Name: "from", Type: "string", Description: availFromDesc},
				{Name: "to", Type: "string", Description: availToDesc},
				{Name: "has-capacity", Type: "bool", Description: "Only slots with remaining capacity"},
				{Name: "since", Type: "string", Description: "ISO 8601 lower-bound on updated_at"},
				{Name: "include-pricing", Type: "bool", Description: "Embed pricing_tiers[] with effective amount overlay (default false)"},
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
				if v := args.FlagString("since"); v != "" {
					t, err := time.Parse(time.RFC3339, v)
					if err != nil {
						return nil, fmt.Errorf("--since: invalid RFC3339 timestamp: %w", err)
					}
					p.Since = &t
				}
				if args.FlagSet("include-pricing") {
					b := args.FlagBool("include-pricing")
					p.IncludePricing = &b
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
			Flags: []FlagDef{
				{Name: "include-pricing", Type: "bool", Description: "Embed pricing_tiers[] with effective amount overlay (default false)"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				p := &gen.ShowAvailabilityParams{}
				if args.FlagSet("include-pricing") {
					b := args.FlagBool("include-pricing")
					p.IncludePricing = &b
				}
				resp, err := r.Client.ShowAvailabilityWithResponse(ctx, id, p)
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
				if err != nil { return &RunResult{WireBody: body}, err }
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Availability", id)
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
		{
			Use: "create-rule", Short: "Generate availabilities from a recurrence rule",
			Kind: KindMutation, Verb: "POST", Path: "/availability-rules",
			Ability: invpkg.Write, DryRunMode: DryRunBody,
			Long: "One-shot generator: takes a weekday-based recurrence rule and dispatches " +
				"CreateBatchAvailabilityJob (the same job the dashboard uses) to materialize " +
				"Availability rows. The rule itself is NOT stored; once the job runs, query " +
				"materialized rows via `availabilities list`. " +
				"--weekdays uses PHP's format('w'): Sunday=0, Saturday=6. " +
				"For datetime products --start-time and --end-time are required; for date " +
				"products both are ignored. --dry-run returns total_matched + first 3 [from,to] " +
				"pairs so an agent can preview the slot count before committing. If the date " +
				"range × weekdays matches zero days, returns 200 status=no_op (no dispatch).",
			Flags: []FlagDef{
				{Name: "product-option-id", Type: "string", Required: true, Description: "Target product option"},
				{Name: "start-date", Type: "string", Required: true, Description: "Recurrence window start (YYYY-MM-DD)"},
				{Name: "end-date", Type: "string", Required: true, Description: "Recurrence window end (YYYY-MM-DD)"},
				{Name: "weekdays", Type: "intSlice", Required: true, Description: "Days of week (0=Sun .. 6=Sat); comma-separated"},
				{Name: "start-time", Type: "string", Description: "HH:MM (datetime products only)"},
				{Name: "end-time", Type: "string", Description: "HH:MM (datetime products only)"},
				{Name: "add-days-count", Type: "int", Description: "Multi-day events: extra days added to the `to` timestamp"},
			},
			ForensicFields: []string{"product-option-id", "start-date", "end-date", "weekdays", "start-time", "end-time"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				// Client-side range gate. The server rejects out-of-range
				// weekdays with 422, but EU operators (Monday-first muscle
				// memory) routinely pass `7` for Sunday — fail loudly here
				// before the request hits the wire.
				if wd, ok := args.Flags["weekdays"].([]int); ok {
					for _, d := range wd {
						if d < 0 || d > 6 {
							return nil, fmt.Errorf("--weekdays: %d out of range (must be 0..6, where Sunday=0 and Saturday=6)", d)
						}
					}
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"product-option-id": "product_option_id",
					"start-date":        "start_date",
					"end-date":          "end_date",
					"weekdays":          "weekdays",
					"start-time":        "start_time",
					"end-time":          "end_time",
					"add-days-count":    "add_days_count",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.CreateAvailabilityRuleWithBodyWithResponse(ctx, &gen.CreateAvailabilityRuleParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Availability", "")
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
		{
			Use: "delete <id>", Short: "Soft-delete one availability", Kind: KindMutation,
			Verb: "DELETE", Path: "/availabilities/{id}", Ability: invpkg.Write,
			DryRunMode: DryRunBody, PositionalArgs: []string{"id"},
			Long: "Soft-deletes the availability (sets deleted_at). 409 " +
				"AVAILABILITY_HAS_CONFIRMED_BOOKING is returned if any confirmed " +
				"Booking is attached — cancel or move the bookings first. The " +
				"confirmed-booking precheck runs even on --dry-run. For multi-row " +
				"deletes spanning a date range, use `bulk-delete`.",
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				// Spec doesn't declare a requestBody for DELETE but the
				// server reads `dry_run: true` from a JSON body. On real
				// delete we send no body at all (matches the 204 contract);
				// on dry-run we attach the body via reqEditor so audit
				// body_sha256 reflects what hit the wire. --data, if
				// supplied, is overlaid on the dry-run body so users can
				// push debug fields onto the wire (matches every other
				// mutation closure). On a real delete --data is ignored
				// because the spec does not accept a body there.
				var wireBody []byte
				editors := []gen.RequestEditorFn{}
				if args.DryRun {
					body := map[string]any{}
					if len(args.RawData) > 0 {
						if err := json.Unmarshal(args.RawData, &body); err != nil {
							return nil, fmt.Errorf("--data: invalid JSON: %w", err)
						}
					}
					body["dry_run"] = true
					raw, mErr := json.Marshal(body)
					if mErr != nil {
						return nil, mErr
					}
					wireBody = raw
					editors = append(editors, dryRunBodyEditor(wireBody))
				}
				resp, err := r.Client.DeleteAvailabilityWithResponse(ctx, id, &gen.DeleteAvailabilityParams{IdempotencyKey: args.IdempotencyKeyUUID}, editors...)
				if err != nil {
					return &RunResult{WireBody: wireBody}, err
				}
				res, perr := ParseGenResponse(resp.Body, resp.HTTPResponse, "Availability", id)
				if res != nil {
					res.WireBody = wireBody
				}
				return res, perr
			},
		},
		// NOTE: no restore command. The spec exposes DELETE (soft-delete via
		// `deleted_at`) and `bulk-delete`, but the Availability schema does
		// NOT surface `deleted_at`, the list endpoint has no
		// `?include_trashed=` parameter, and there is no
		// /availabilities/{id}/restore operation. There's currently no
		// supported way to undo a soft-delete from the CLI.
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
				{Name: "operator", Type: "string", Required: true, Description: "set_to|increase_by|decrease_by"},
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
				// Defensive: even though Required:true means cobra will reject
				// invocations without --is-bookable, we still gate on FlagSet
				// before reading the value. If Required is ever relaxed,
				// FlagBool would silently return false (cobra default) and
				// quietly close every slot in range.
				if !args.FlagSet("is-bookable") {
					return nil, fmt.Errorf("--is-bookable is required")
				}
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
		// start-time and end-time share spec's TimeValue shape (start_time +
		// end_time + optional day_count) — only the `setting` discriminator
		// differs. Build both from the same flag list and closure so a fix
		// to one path can't drift from the other.
		bulkUpdateDef("start-time", "Bulk update slot start time (and optional day-count for multi-day)", timeValueFlags, timeValueNewValue),
		bulkUpdateDef("end-time", "Bulk update slot end time", timeValueFlags, timeValueNewValue),
	}, runner)

	return parent
}

// timeValueFlags / timeValueNewValue back both the start-time and end-time
// bulk-update subcommands. The spec's TimeValue shape is identical for
// `setting=start_time` and `setting=end_time` (start_time + end_time +
// optional day_count); only the discriminator differs, so we share the
// flag list and the body-builder closure.
var timeValueFlags = []FlagDef{
	{Name: "start-time", Type: "string", Required: true, Description: "HH:MM"},
	{Name: "end-time", Type: "string", Required: true, Description: "HH:MM"},
	{Name: "day-count", Type: "int", Description: "Days the activity spans"},
}

func timeValueNewValue(args RunArgs) (any, error) {
	v := map[string]any{
		"start_time": args.FlagString("start-time"),
		"end_time":   args.FlagString("end-time"),
	}
	if args.FlagSet("day-count") {
		v["day_count"] = args.FlagInt("day-count")
	}
	return v, nil
}

// bulkUpdateDef returns one CommandDef for an `availabilities bulk-update
// <setting>` subcommand. settingName is the kebab-case CLI form; the
// underlying spec setting is the same with `-` → `_`.
func bulkUpdateDef(settingName, short string, perSettingFlags []FlagDef, newValueFn func(RunArgs) (any, error)) CommandDef {
	// Common to every bulk-update setting: the temporal/scoping fields.
	commonFlags := []FlagDef{
		{Name: "from", Type: "string", Required: true, Description: availFromDesc},
		{Name: "to", Type: "string", Required: true, Description: availToDesc},
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
			// Start with --data (if any) as the base, then overlay typed
			// fields. Matches JSONBodyFromArgs ordering used by every other
			// mutation. Typed flags + the static `setting` discriminator
			// always WIN so the subcommand's body shape stays correct even
			// if --data tries to override them.
			body := map[string]any{}
			if len(args.RawData) > 0 {
				if err := json.Unmarshal(args.RawData, &body); err != nil {
					return nil, fmt.Errorf("--data: invalid JSON: %w", err)
				}
			}
			body["setting"] = specSetting
			body["from"] = from.Format("2006-01-02")
			body["to"] = to.Format("2006-01-02")
			body["product_option_id"] = args.FlagString("product-option-id")
			body["new_value"] = newValue
			if args.DryRun {
				body["dry_run"] = true
			}
			raw, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			resp, err := r.Client.BulkUpdateAvailabilitiesWithBodyWithResponse(ctx, &gen.BulkUpdateAvailabilitiesParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(raw))
			if err != nil {
				return &RunResult{WireBody: raw}, err
			}
			res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Availability", "")
			if res != nil {
				res.WireBody = raw
			}
			return res, err
		},
	}
}
