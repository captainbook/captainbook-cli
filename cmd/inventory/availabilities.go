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
	openapi_types "github.com/oapi-codegen/runtime/types"
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

// availabilityIDsDesc is the `--availability-ids` flag description shared by
// bulk-update and bulk-delete. Both endpoints mean the same thing by it, and
// the sentence operators actually need — reach for ids whenever the request
// contains an exception — is the one the spec repeats on both.
// availabilityIDsMaxItems mirrors the spec's `maxItems` on the
// `availability_ids` array of both bulk endpoints. Named for the same reason
// companionsMaxItems is: the cap appears in the gate, in the refusal message
// and in the flag description, and a spec bump must not be able to move one
// without the others — so every one of those three reads it from here.
const availabilityIDsMaxItems = 1000

// availabilityIDsDesc is the `--availability-ids` flag description shared by
// bulk-update and bulk-delete. Both endpoints mean the same thing by it, and
// the sentence operators actually need — reach for ids whenever the request
// contains an exception — is the one the spec repeats on both.
var availabilityIDsDesc = fmt.Sprintf("The exact departures to act on, comma-separated (max %d; may span product options, ",
	availabilityIDsMaxItems) +
	"except on bulk-update pricing, which refuses a list spanning more than one option). " +
	"Mutually exclusive with --product-option-id/--from/--to. Use this whenever the request has an " +
	"exception in it: \"delete the duplicates that rule just created, keep the ones that were already " +
	"there\" cannot be written as a date range, because old and new sit on the same option inside the " +
	"same dates"

// productOptionScopeDesc is the `--product-option-id` description for the two
// bulk endpoints, where the flag is one third of the range scope rather than a
// standalone filter.
const productOptionScopeDesc = "Target product option ID (with --from/--to)"

// availabilityScopeFlags are the scoping flags shared by
// /availabilities/bulk-update and /availabilities/bulk-delete. Neither
// endpoint can mark any of them Required at the cobra level: the spec
// requires exactly ONE of the two scopes, which cobra cannot express.
// resolveAvailabilityScope enforces it instead.
//
// Takes no parameters on purpose. An earlier spelling passed the
// product-option description in, and both call sites filled it with the same
// literal — which is the duplicated string the extraction existed to remove,
// reintroduced one level up, where the next edit to one endpoint's help text
// would silently diverge the two.
func availabilityScopeFlags() []FlagDef {
	return []FlagDef{
		{Name: "from", Type: "string", Description: availFromDesc},
		{Name: "to", Type: "string", Description: availToDesc},
		{Name: "product-option-id", Type: "string", Description: productOptionScopeDesc},
		{Name: "availability-ids", Type: "intSlice", Description: availabilityIDsDesc},
	}
}

// parseAvailabilityRange parses and validates the date-range scope shared by
// /availabilities/bulk-update and /availabilities/bulk-delete.
//
// `to` is EXCLUSIVE and must be strictly after `from` on BOTH endpoints. The
// schemas say so only for bulk-update, which is how an earlier version of this
// file came to apply the gate on one endpoint and argue, in a comment, that
// the other deliberately had no such rule. The spec's changelog is explicit
// that it does — cli-v1.yaml, the 1.22.0 entry:
//
//	POST /availabilities/bulk-delete also refuses `from` equal to `to` with a
//	422, matching /availabilities/bulk-update. `to` is EXCLUSIVE on both, so
//	that range matched nothing and the delete answered 200 with
//	total_deleted: 0 — success, having removed nothing.
//
// So the equal-dates call was never "a request the server accepts": it is a
// 422, and the reason it is refused there is the reason it must be refused
// here — on a destructive endpoint, a typo that answers 200 having deleted
// nothing is the worst available outcome. One shared parser means the two
// endpoints cannot drift on it again.
func parseAvailabilityRange(args RunArgs) (from, to openapi_types.Date, err error) {
	from, err = parseDate(args.FlagString("from"))
	if err != nil {
		return from, to, fmt.Errorf("--from: %w", err)
	}
	to, err = parseDate(args.FlagString("to"))
	if err != nil {
		return from, to, fmt.Errorf("--to: %w", err)
	}
	if !to.Time.After(from.Time) {
		return from, to, fmt.Errorf("--to must be strictly after --from (the range is half-open, so one day means --to = the NEXT day)")
	}
	return from, to, nil
}

// resolveAvailabilityScope enforces the "exactly one scope" rule that
// /availabilities/bulk-update (since spec 1.19.0) and
// /availabilities/bulk-delete (since 1.32.0) share: either
// `availability_ids`, or `product_option_id` + `from` + `to`. Sending both
// is a 422 server-side and sending neither is a 422; these gates mirror
// those refusals rather than spending a round trip to discover them.
//
// It returns the validated id list and whether the id scope is in force; the
// range branch is parsed by parseAvailabilityRange, which both endpoints share
// because both document the same half-open rule.
//
// Extracted rather than copied because the copy is where this kind of gate
// goes wrong: the --product-id/--availability-id exclusion in
// pricing_tiers.go read two int flags with FlagString and could never fire,
// with the compiler, both drift directions and the whole suite green.
func resolveAvailabilityScope(args RunArgs) (ids []int, useIDs bool, err error) {
	idsSet := args.FlagSet("availability-ids")
	rangeFlags := []string{"product-option-id", "from", "to"}
	var rangeGiven []string
	for _, f := range rangeFlags {
		if args.FlagSet(f) {
			rangeGiven = append(rangeGiven, "--"+f)
		}
	}
	switch {
	case idsSet && len(rangeGiven) > 0:
		return nil, false, fmt.Errorf("--availability-ids is mutually exclusive with %s: pass one scope or the other, not both",
			strings.Join(rangeGiven, ", "))
	case !idsSet && len(rangeGiven) == 0:
		return nil, false, fmt.Errorf("one scope is required: either --availability-ids, or all of --product-option-id, --from and --to")
	case !idsSet && len(rangeGiven) < len(rangeFlags):
		return nil, false, fmt.Errorf("the date-range scope needs all of --product-option-id, --from and --to (got %s)",
			strings.Join(rangeGiven, ", "))
	}
	if !idsSet {
		return nil, false, nil
	}
	ids, _ = args.Flags["availability-ids"].([]int)
	// The cap matches the job's chunk size; the server answers 422 above it.
	// An id that does not resolve is also a 422 naming it rather than a
	// silently narrowed list, so there is nothing to filter here.
	if len(ids) == 0 {
		return nil, false, fmt.Errorf("--availability-ids: at least one id is required")
	}
	if len(ids) > availabilityIDsMaxItems {
		return nil, false, fmt.Errorf("--availability-ids: %d ids exceeds the %d cap; a longer list is a date range wearing the wrong shape",
			len(ids), availabilityIDsMaxItems)
	}
	for _, id := range ids {
		if id < 1 {
			return nil, false, fmt.Errorf("--availability-ids: %d is not a valid availability id (must be >= 1)", id)
		}
	}
	return ids, true, nil
}

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
// Mirrors the bulk-update filter shape — as of spec 1.32.0 that includes
// BOTH of its scopes, `availability_ids` as well as product_option_id +
// half-open [from, to) — but the response carries `total_deleted` directly:
// there's no async job, no stderr signal, no polling. 409
// AVAILABILITY_HAS_CONFIRMED_BOOKING aborts the entire request before any
// row is touched.
func bulkDeleteDef() CommandDef {
	return CommandDef{
		Use: "bulk-delete", Short: "Bulk soft-delete availabilities, by id or across a date range (synchronous)",
		Kind: KindMutation, Verb: "POST", Path: "/availabilities/bulk-delete",
		Ability: invpkg.Write, DryRunMode: DryRunBody,
		Long: "Soft-deletes departures, either the exact rows named by --availability-ids " +
			fmt.Sprintf("(up to %d, which may span product options) or every availability of one ", availabilityIDsMaxItems) +
			"product option in [from, to). Exactly one scope; sending both is a 422. " +
			"Synchronous — response carries `total_deleted`. 409 " +
			"AVAILABILITY_HAS_CONFIRMED_BOOKING is returned if any matched row has " +
			"a confirmed Booking attached (entire request rejected, no rows touched); " +
			"`error.details.total_blocked` + `sample_availability_ids` (up to 20) " +
			"identify the blockers. The confirmed-booking precheck runs even on " +
			"--dry-run.\n\n" +
			"Replacing a schedule? Delete BEFORE you create, never after. " +
			"`availabilities create-rule` is asynchronous (202, then a worker writes the " +
			"rows) while this is synchronous, so a range-scoped delete issued after the " +
			"rule takes the corrected departures too if the worker has already run — a " +
			"coin toss on queue latency with an empty season as one outcome. The id scope " +
			"is immune: ids name rows that exist now, so they cannot reach a departure the " +
			"worker has not written yet.",
		Flags: availabilityScopeFlags(),
		// D20: the scope decides exactly how many departures one call
		// destroys, which is the largest blast radius in this command tree.
		// The audit keeps only body_sha256, so omitting --availability-ids
		// would leave an incident responder unable to tell which departures
		// were deleted.
		ForensicFields: []string{"product-option-id", "from", "to", "availability-ids"},
		Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
			ids, useIDs, err := resolveAvailabilityScope(args)
			if err != nil {
				return nil, err
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
			if useIDs {
				body["availability_ids"] = ids
				// The scopes are mutually exclusive on the wire, so a --data
				// blob carrying the other one would turn an id-scoped call
				// into a 422. Typed flags win: drop it.
				delete(body, "product_option_id")
				delete(body, "from")
				delete(body, "to")
			} else {
				from, to, rerr := parseAvailabilityRange(args)
				if rerr != nil {
					return nil, rerr
				}
				body["product_option_id"] = args.FlagString("product-option-id")
				body["from"] = from.Format("2006-01-02")
				body["to"] = to.Format("2006-01-02")
				delete(body, "availability_ids")
			}
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
				// A delete that matched nothing answers 200, and on a
				// destructive command "success" and "did nothing" must not
				// look the same.
				//
				// This covers a WELL-FORMED scope that found nothing left to
				// delete — rows already removed, or a correct range that is
				// not the one the operator meant. The empty-range typo cannot
				// reach here: parseAvailabilityRange refuses `from >= to`
				// before the request, because the server 422s it too (see that
				// function for the spec citation). stdout stays the data
				// contract, so the line goes to stderr.
				// dry-run comes off the assembled body, not args.DryRun —
				// see bulkDeleteNoOpWarnings for why.
				res.Warnings = append(res.Warnings, bulkDeleteNoOpWarnings(resp.Body, body["dry_run"] == true)...)
			}
			return res, perr
		},
	}
}

// bulkDeleteNoOpWarnings returns one stderr line when a bulk-delete reports
// that it removed nothing, naming the likeliest cause the payload can show.
//
// Fires when: a non-dry-run response reports `status: no_op`, or
// `total_deleted: 0`.
//
// Stays quiet when: the request body set `dry_run`, the response labels itself
// `preview`, the body does not parse (an error envelope included), or neither
// counter is present. A delete that removed rows says nothing extra.
func bulkDeleteNoOpWarnings(body []byte, dryRun bool) []string {
	// dryRun comes from the REQUEST body rather than the response because the
	// response cannot be trusted to distinguish them: `status` is not required
	// on the 200, and the spec's no-op paragraph carries no dry-run exception,
	// so a preview that matched nothing may well answer `status: "no_op"`. A
	// warning that fires on a preview — which deletes nothing by design — is a
	// warning operators learn to ignore. The body rather than the --dry-run
	// flag because `--data` can set `dry_run` on its own.
	if dryRun {
		return nil
	}
	var env struct {
		Data struct {
			Status       string `json:"status"`
			TotalDeleted *int   `json:"total_deleted"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil
	}
	// Redundant with the dry-run check above, for a server that DOES label its
	// previews — not the primary test, which is why the request body decides.
	if env.Data.Status == "preview" {
		return nil
	}
	noOp := env.Data.Status == "no_op" ||
		(env.Data.TotalDeleted != nil && *env.Data.TotalDeleted == 0)
	if !noOp {
		return nil
	}
	// The hint names a cause that can actually produce this 200. It used to
	// blame the half-open range, but the server refuses `from == to` with a
	// 422 (cli-v1.yaml, 1.22.0) and parseAvailabilityRange now refuses it
	// locally, so that typo never reaches here. What does reach here is a
	// well-formed range, or a set of ids, that genuinely had nothing left to
	// delete — which is worth saying out loud on a destructive command.
	return []string{"BULK_DELETE_NO_OP deleted=0 hint=the scope matched no live departure; " +
		"it may already have been deleted, or the range may not be the one you meant — " +
		"confirm with `availabilities list --include-trashed` before re-running"}
}

// parseFaresFlag decodes and validates the `--fares` JSON array shared by
// `availabilities update` and `availabilities bulk-update pricing`. Both
// write `availability_pricing_tier` rows through the spec's
// AvailabilityFare shape, so they share one parser: a fix to the amount
// rules can't drift between the single-slot and date-range write paths.
//
// It returns the entries as RawMessage so the operator's bytes reach the
// wire VERBATIM. Decoding into `any` would route every amount through
// float64 and silently round anything past 2^53 — 9007199254740993 ships as
// ...992. Out of range for minor units, but the exact bytes are already in
// hand, so there is no reason to lose them.
//
// The client-side gates all mirror a server 422 that is expensive to
// discover the hard way:
//
//   - Empty array. The server used to accept it and would write an audit
//     row, queue a job per chunk of matched ids and fire PriceScheduleUpdated
//     at the channel managers, all for zero data change.
//   - Missing `amount` key. The spec requires `amount` to be PRESENT but
//     allows it to be null, precisely so a client that forgets to serialise
//     an amount gets an error instead of silently wiping the operator's
//     price. A Go marshaller that omitempty's a zero amount is exactly that
//     bug, so we check for the KEY, not for a non-null value.
//   - Wrong `amount` type. `Money` is an integer or null; a quoted "4500"
//     or a nested object would 422 server-side.
//   - Missing, non-string, or blank `pricing_tier_id`.
//
// `amount: null` is explicitly legal and means "delete the override so the
// slot follows the catalogue price again" — it is passed straight through.
//
// NOTE: this gates the --fares FLAG only. Every mutation also accepts
// --data, and `--data '{"fares":[...]}'` bypasses every check here. For a
// malformed shape that is fine — the server rejects it. For a large amount
// it is NOT: JSONBodyFromArgs decodes --data through map[string]any, so
// 9007199254740993 is rounded to ...992 before it is sent and the server
// never sees the value the operator typed. The verbatim guarantee above
// covers the flag path only. Tracked in TODOS.md.
func parseFaresFlag(raw string) ([]map[string]json.RawMessage, error) {
	var fares []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fares); err != nil {
		return nil, fmt.Errorf("--fares: invalid JSON: %w", err)
	}
	// `--fares null` decodes into a nil slice without error, which is not
	// the same input as `--fares '[]'` and deserves its own message.
	if fares == nil {
		return nil, fmt.Errorf("--fares: expected a JSON array, got null")
	}
	if len(fares) == 0 {
		return nil, fmt.Errorf("--fares: empty array is rejected by the server (422). " +
			"Omit --fares entirely to leave pricing untouched, or send " +
			`[{"pricing_tier_id":"...","amount":null}] to delete an override`)
	}
	for i, entry := range fares {
		// Only a JSON `null` element reaches here as a nil map — every other
		// non-object element (a number, string, array, bool) fails the decode
		// above and never enters this loop. Without this guard a `null`
		// element would be reported as a missing "amount" key, which reads
		// like a field problem rather than a shape problem.
		if entry == nil {
			return nil, fmt.Errorf("--fares[%d]: expected an object, got null", i)
		}
		amount, ok := entry["amount"]
		if !ok {
			return nil, fmt.Errorf(`--fares[%d]: missing "amount" key. `+
				`It is required to be present but may be null; null DELETES the `+
				`override so the slot follows the catalogue price again`, i)
		}
		if err := validateFareAmount(amount); err != nil {
			return nil, fmt.Errorf("--fares[%d]: %w", i, err)
		}
		// Distinguish absent from present-but-wrong-type. The spec types
		// pricing_tier_id as a string, but ids appear unquoted in enough
		// examples that an operator hand-writing the JSON will reach for
		// {"pricing_tier_id": 7}. Reporting that as "missing" sends them
		// hunting for a key that is right there in front of them.
		rawTier, present := entry["pricing_tier_id"]
		if !present {
			return nil, fmt.Errorf(`--fares[%d]: missing "pricing_tier_id"`, i)
		}
		var tier string
		if err := json.Unmarshal(rawTier, &tier); err != nil {
			return nil, fmt.Errorf(`--fares[%d]: "pricing_tier_id" must be a JSON string, got %s`, i, rawTier)
		}
		if strings.TrimSpace(tier) == "" {
			return nil, fmt.Errorf(`--fares[%d]: "pricing_tier_id" is empty`, i)
		}
	}
	return fares, nil
}

// validateFareAmount enforces the spec's AvailabilityFare.amount shape: an
// integer in minor units, or null to delete the override. A quoted "4500"
// or a nested object 422s server-side; catching it here names the field.
func validateFareAmount(raw json.RawMessage) error {
	if string(raw) == "null" {
		return nil // the documented "delete this override" spelling
	}
	// Decode through UseNumber and type-assert rather than unmarshalling
	// straight into json.Number: json.Number is a string type, so a QUOTED
	// "4500" unmarshals into it without error and would sail through.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return fmt.Errorf(`"amount" must be an integer in minor units, or null, got %s`, raw)
	}
	n, ok := v.(json.Number)
	if !ok {
		return fmt.Errorf(`"amount" must be an integer in minor units, or null, got %s`, raw)
	}
	if _, err := n.Int64(); err != nil {
		return fmt.Errorf(`"amount" must be a whole number of minor units, got %s`, raw)
	}
	return nil
}

// overlayJSONField sets one key on an already-marshalled JSON object body.
// Used for fields whose value is structured (an array, an object) and so
// cannot travel through JSONBodyFromArgs's flag→key map, which copies the
// flag's Go value verbatim and would put a quoted string on the wire.
func overlayJSONField(body []byte, key string, value any) ([]byte, error) {
	m := map[string]any{}
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	m[key] = value
	return json.Marshal(m)
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
				{Name: "limit", Type: "int", Min: 1},
				{Name: "cursor", Type: "string"},
				// An integer here, unlike the string-typed flag of the same
				// name on the two bulk endpoints. Both carry the spec's
				// PositiveIntegerId on the wire; the difference is only which
				// layer refuses a non-numeric id, so the description says the
				// part an operator can act on.
				{Name: "product-option-id", Type: "int", Min: 1, Description: "Filter by product option (numeric id)"},
				{Name: "product-id", Type: "string", Description: "Filter to availabilities of this product (accepts the id in either spelling the API takes)"},
				{Name: "include-trashed", Type: "bool", Description: "Include soft-deleted rows"},
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
				if v := args.FlagString("product-id"); v != "" {
					p.ProductId = &v
				}
				if args.FlagBool("include-trashed") {
					t := true
					p.IncludeTrashed = &t
				}
				if args.FlagSet("product-option-id") {
					v := args.FlagInt("product-option-id")
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
			Long: "Updates capacity, status and/or per-slot pricing overrides on ONE " +
				"availability.\n\n" +
				"--fares is the only way to reprice exactly one slot. `bulk-update pricing` " +
				"is date-range scoped, so on a date carrying several sessions it writes " +
				"every one of them.\n\n" +
				"Each --fares entry writes an availability_pricing_tier row for this slot " +
				"only; tiers not listed are left alone. An entry with \"amount\": null " +
				"DELETES the override so the slot follows the catalogue price again. " +
				"Writing the catalogue value is NOT a substitute — the pivot row survives, " +
				"is_override stays true, and the slot silently keeps the old number the " +
				"next time the tier is repriced.\n\n" +
				"422 when a pricing_tier_id belongs to another product's ladder, when the " +
				"availability is not attached to a ProductOption (Resource availabilities " +
				"have no pricing tiers), or when --fares is an empty array. The CLI catches " +
				"the empty-array case before the request goes out; --data '{\"fares\":[]}' " +
				"bypasses that gate and is rejected server-side instead.\n\n" +
				"Field and fare changes commit in ONE transaction, so a failure cannot " +
				"leave the capacity change applied and the pricing write missing. A " +
				"committed fare change then dispatches PriceScheduleUpdated to the channel " +
				"managers best-effort AFTER the commit: a 200 means the fare is stored, " +
				"not that the channels have been told.",
			Example: "  # Close one slot\n" +
				"  ceebee inventory availabilities update av_42 --status blocked\n\n" +
				"  # Reprice one slot's adult tier to 4500 minor units\n" +
				"  ceebee inventory availabilities update av_42 --fares '[{\"pricing_tier_id\":\"pt_7\",\"amount\":4500}]'\n\n" +
				"  # Drop the override so the slot follows the catalogue price again\n" +
				"  ceebee inventory availabilities update av_42 --fares '[{\"pricing_tier_id\":\"pt_7\",\"amount\":null}]'",
			Flags: []FlagDef{
				{Name: "capacity", Type: "int", Description: "Slot capacity"},
				{Name: "status", Type: "string", Description: "available|blocked"},
				{Name: "is-bookable", Type: "bool", Description: "Bulk-close state, tracked independently of --status. Reads false after a bulk-update booking-status close"},
				{Name: "fares", Type: "string", Description: "JSON array of per-slot pricing overrides: [{pricing_tier_id, amount}, ...]. amount null deletes the override"},
				{Name: "day-count", Type: "int", Description: "Multi-day events: number of days this departure spans"},
				{Name: "start-time", Type: "string", Description: "HH:MM, product-local wall clock (datetime products only)"},
				{Name: "end-time", Type: "string", Description: "HH:MM, product-local wall clock (datetime products only)"},
			},
			ForensicFields: []string{"capacity", "status", "is-bookable", "fares", "day-count", "start-time", "end-time"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"capacity":    "capacity",
					"status":      "status",
					"is-bookable": "is_bookable",
					"day-count":   "day_count",
					"start-time":  "start_time",
					"end-time":    "end_time",
				})
				if err != nil {
					return nil, err
				}
				// --fares is a JSON array, so it can't go through the
				// flag→key map (which copies the flag's Go value verbatim
				// and would send the array as a quoted string). Parse and
				// overlay it onto the body the helper built.
				if args.FlagSet("fares") {
					fares, ferr := parseFaresFlag(args.FlagString("fares"))
					if ferr != nil {
						return nil, ferr
					}
					body, err = overlayJSONField(body, "fares", fares)
					if err != nil {
						return nil, err
					}
				}
				resp, err := r.Client.UpdateAvailabilityWithBodyWithResponse(ctx, id, &gen.UpdateAvailabilityParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Availability", id)
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
		{
			Use: "create", Short: "Create one availability (departure)", Kind: KindMutation,
			Verb: "POST", Path: "/availabilities", Ability: invpkg.Write, DryRunMode: DryRunBody,
			Long: "Creates a single departure. Use `create-rule` instead to generate a " +
				"recurring series; this is the one-off.",
			Flags: []FlagDef{
				{Name: "product-option-id", Type: "string", Required: true, Description: "Target product option ID"},
				{Name: "date", Type: "string", Required: true, Description: "Departure date (YYYY-MM-DD)"},
				{Name: "start-time", Type: "string", Required: true, Description: "HH:MM, product-local wall clock"},
				{Name: "end-time", Type: "string", Required: true, Description: "HH:MM, product-local wall clock"},
				{Name: "capacity", Type: "int", Required: true, Description: "Slot capacity"},
				{Name: "day-count", Type: "int", Description: "Multi-day events: number of days this departure spans"},
				{Name: "end-date", Type: "string", Description: "Multi-day events: explicit end date (YYYY-MM-DD)"},
				{Name: "is-bookable", Type: "bool", Description: "Whether the departure is open for booking"},
			},
			ForensicFields: []string{"product-option-id", "date", "start-time", "end-time", "capacity", "day-count", "end-date", "is-bookable"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"product-option-id": "product_option_id",
					"date":              "date",
					"start-time":        "start_time",
					"end-time":          "end_time",
					"capacity":          "capacity",
					"day-count":         "day_count",
					"end-date":          "end_date",
					"is-bookable":       "is_bookable",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.CreateAvailabilityWithBodyWithResponse(ctx, &gen.CreateAvailabilityParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, perr := ParseGenResponse(resp.Body, resp.HTTPResponse, "Availability", "")
				if res != nil {
					res.WireBody = body
				}
				return res, perr
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
				"products both are ignored. If the date " +
				"range × weekdays matches zero days, returns 200 status=no_op (no dispatch).\n\n" +
				"Reading a dry run: would_create is the number that will be materialized — " +
				"NOT total_matched, which counts occurrences the rule generated before skips. " +
				"skip_reasons.already_exists matches the exact (from, to) pair, so this " +
				"endpoint only ever ADDS: a rule aimed at a season that already runs at " +
				"another time skips nothing and every day ends up selling twice. Spec 1.31.0 " +
				"reports that collision on the dry run as notes.other_departures_exist (a " +
				"count) and rule.other_departures_sample (up to three of the days, with the " +
				"times already on them). Read both before approving. Nothing is refused — " +
				"which schedule you want is not the server's call. With more than one " +
				"departure time, also read rule.times[]: would_create and total_matched are " +
				"SUMS across times, so a time that would create nothing is invisible in them. " +
				"A dry run carries no `status` field at all (would_apply marks a preview; the " +
				"only 200 status is no_op), so do not gate on status==preview. Replacing a " +
				"season is two calls and bulk-delete must come FIRST; see that command.",
			Flags: []FlagDef{
				{Name: "product-option-id", Type: "string", Required: true, Description: "Target product option"},
				{Name: "start-date", Type: "string", Required: true, Description: "Recurrence window start (YYYY-MM-DD)"},
				{Name: "end-date", Type: "string", Required: true, Description: "Recurrence window end (YYYY-MM-DD)"},
				{Name: "weekdays", Type: "intSlice", Required: true, Description: "Days of week (0=Sun .. 6=Sat); comma-separated"},
				{Name: "start-time", Type: "string", Description: "HH:MM (datetime products only)"},
				{Name: "end-time", Type: "string", Description: "HH:MM (datetime products only)"},
				{Name: "add-days-count", Type: "int", Description: "Multi-day events: extra days added to the end timestamp"},
				{Name: "capacity", Type: "int", Description: "Capacity for every departure the rule creates"},
				{Name: "times", Type: "json", Description: "Departure times as JSON, or @file.json: [{start_time, end_time, add_days_count?}, ...]"},
			},
			// D20: `times` and `capacity` decide how many departures the rule creates
			// and how large each one is. The audit keeps only body_sha256, so a
			// value absent from this list cannot be reconstructed afterwards.
			// add-days-count belongs here by this list's own stated criterion:
			// it decides how LARGE each generated departure is (a multi-day
			// event spanning N extra days). It is wired into the body and was
			// missing from the audit, so an incident responder could see which
			// option and which dates were touched but not that each departure
			// spanned four days — the one value that explains the resulting
			// overlapping inventory.
			ForensicFields: []string{"product-option-id", "start-date", "end-date", "weekdays", "start-time", "end-time", "capacity", "times", "add-days-count"},
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
					"capacity":          "capacity",
					"times":             "times",
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
		// `deleted_at`) and `bulk-delete`, but there is no
		// /availabilities/{id}/restore operation, so a soft-delete cannot be
		// undone from the CLI.
		//
		// It CAN be enumerated, though, and an earlier version of this note
		// denied it: `Availability.deleted_at` is a real response field (see
		// the generated Availability struct) and GET /availabilities takes
		// `include_trashed`, which `availabilities list` exposes. That
		// distinction is the whole of incident response after a bad
		// bulk-delete — list the deleted rows to learn exactly what went,
		// then re-create them — so the inaccurate version pointed operators
		// at ops for a question they could answer themselves.
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
			"Bulk update slot capacity", "",
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
			"Bulk update bookable flag (open / close slots)", "",
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
			"Replaces the fares for the listed tiers across [from, to). Tiers omitted "+
				"are left alone. An amount of null DELETES the override for that tier "+
				"across the matched range, so those slots follow the catalogue price "+
				"again. An empty --fares array is a 422.\n\n"+
				"Beware the range scope: every session on a matched DATE is written, "+
				"including ones that had no override before. Use `availabilities update "+
				"<id> --fares` when a date carries more than one session and you only "+
				"mean to touch one of them.\n\n"+
				"--dry-run on this setting alone carries a real diff. total_matched is "+
				"just the date-range row count — identical whether the proposed amount "+
				"matches the current fare or is wildly different — so it cannot answer "+
				"\"will this change anything?\". The preview adds total_changed (matched "+
				"slots whose effective price or pinned/unpinned state would move, counted "+
				"over the WHOLE range) plus diff.before / diff.after listing the first "+
				"preview_limit matched slots with their current and proposed fares. "+
				"preview_truncated is true when the range is longer than that listing; "+
				"the counts still cover everything. A slot with no override today that is "+
				"being written to the catalogue value still counts as changed — the write "+
				"pins it.",
			[]FlagDef{
				{Name: "fares", Type: "string", Required: true, Description: "JSON array: [{pricing_tier_id, amount}, ...]. amount null deletes the override"},
			},
			func(args RunArgs) (any, error) {
				fares, err := parseFaresFlag(args.FlagString("fares"))
				if err != nil {
					return nil, err
				}
				return map[string]any{"fares": fares}, nil
			},
		),
		// start-time and end-time share spec's TimeValue shape (start_time +
		// end_time + optional day_count) — only the `setting` discriminator
		// differs. Build both from the same flag list and closure so a fix
		// to one path can't drift from the other.
		bulkUpdateDef("start-time", "Bulk update slot start time (and optional day-count for multi-day)", "", timeValueFlags, timeValueNewValue),
		bulkUpdateDef("end-time", "Bulk update slot end time", "", timeValueFlags, timeValueNewValue),
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
func bulkUpdateDef(settingName, short, long string, perSettingFlags []FlagDef, newValueFn func(RunArgs) (any, error)) CommandDef {
	// Common to every bulk-update setting: the scoping fields. Shared with
	// bulk-delete, which takes the same two scopes as of spec 1.32.0.
	flags := availabilityScopeFlags()
	flags = append(flags, perSettingFlags...)

	// Forensic fields per D37 + plan §"async bulk-update": capture
	// setting, from, to, product_option_id, plus the per-setting
	// new_value fields.
	//
	// D20: `availability-ids` decides exactly how many departures one call
	// changes, which is the largest blast radius in this command tree. The audit
	// keeps only body_sha256, so omitting it would leave an incident responder
	// unable to tell which departures were touched.
	forensic := []string{"from", "to", "product-option-id", "availability-ids"}
	for _, f := range perSettingFlags {
		forensic = append(forensic, f.Name)
	}

	specSetting := strings.ReplaceAll(settingName, "-", "_")

	return CommandDef{
		Use: settingName, Short: short, Long: long,
		Kind: KindMutation, Verb: "POST", Path: "/availabilities/bulk-update",
		Ability: invpkg.Write, DryRunMode: DryRunBody,
		Flags:          flags,
		ForensicFields: forensic,
		// All 5 bulk-update subcommands hit the same HTTP path; the
		// `setting` discriminator is what distinguishes them. Static
		// forensic ensures audit_summary records which one ran.
		StaticForensic: map[string]any{"setting": specSetting},
		Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
			ids, idsSet, err := resolveAvailabilityScope(args)
			if err != nil {
				return nil, err
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
			if idsSet {
				body["availability_ids"] = ids
				// Same eviction bulk-delete does, and for the same reason: the
				// scopes are mutually exclusive ON THE WIRE, so a --data blob
				// carrying the other one turns a valid call into a 422. Typed
				// flags win by DELETING the other scope, not merely by
				// overwriting their own keys — overwriting leaves the orphan
				// keys in place and the server reads "both scopes present".
				delete(body, "product_option_id")
				delete(body, "from")
				delete(body, "to")
			} else {
				from, to, rerr := parseAvailabilityRange(args)
				if rerr != nil {
					return nil, rerr
				}
				body["from"] = from.Format("2006-01-02")
				body["to"] = to.Format("2006-01-02")
				body["product_option_id"] = args.FlagString("product-option-id")
				delete(body, "availability_ids")
			}
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
