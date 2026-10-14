package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/captainbook/captainbook-cli/internal/inventory/gen"
)

// companionsMaxItems mirrors the spec's `maxItems` on the `companions` array
// of both the create and update payloads.
const companionsMaxItems = 20

// companionsDesc is the --companions flag description, shared by create and
// update so the replace-not-merge semantics can only be described once.
//
// The distinction the description has to carry is REPLACE vs MERGE: the
// server swaps the whole set, so a caller adding one link has to resend the
// ones they are keeping. Sending a single new link against a category that
// already has two is a silent removal of the other two, and nothing in the
// response says so — the payload just comes back with one link, which reads
// like it worked.
var companionsDesc = `"Must travel with" links as JSON, or @file.json: ` +
	fmt.Sprintf(`[{"category_id": 7, "min_quantity": 2}, ...] (max %d). REPLACES every `, companionsMaxItems) +
	`existing link — resend the ones you are keeping. '[]' clears them all; ` +
	`omit the flag to leave them unchanged. The category is bookable when ANY ` +
	`one link is met; links are never added together`

// companionsRawFromArgs returns the companions value as the operator WROTE it,
// from --companions when the flag is set and otherwise from --data, plus the
// label to name in any refusal.
//
// Reading it out of --data rather than off the assembled body is deliberate and
// does two things at once. It closes the bypass — the gates below used to run
// only `if args.FlagSet("companions")`, so `--data '{"companions":[...]}'`
// reached the wire with no self-link, duplicate, unknown-key, cap or bounds
// check, on a field whose semantics are REPLACE, i.e. a silent link wipe. And
// it preserves the operator's bytes: JSONBodyFromArgs decodes --data through
// map[string]any, which routes every number via float64 and alters anything
// past 2^53, so validating the ASSEMBLED body would inspect an already-rounded
// id. Taking the raw bytes here and overlaying them back replaces the rounded
// value with the verbatim one for this field.
//
// The wider --data rounding problem is untouched and still tracked in TODOS.md;
// this fixes only the field it validates.
func companionsRawFromArgs(args RunArgs) (raw json.RawMessage, label string, present bool, err error) {
	if args.FlagSet("companions") {
		v, _ := args.Flags["companions"].(json.RawMessage)
		return v, "--companions", true, nil
	}
	if len(args.RawData) == 0 {
		return nil, "", false, nil
	}
	var probe map[string]json.RawMessage
	if jerr := json.Unmarshal(args.RawData, &probe); jerr != nil {
		return nil, "", false, fmt.Errorf("--data: invalid JSON: %w", jerr)
	}
	v, ok := probe["companions"]
	if !ok {
		return nil, "", false, nil
	}
	return v, "--data.companions", true, nil
}

// omitTarget names what the operator should actually leave out to keep the
// existing links, which differs by input: dropping `--companions` leaves the
// rest of the request intact, but dropping `--data` would discard every other
// field in the blob, so that path has to point at the key instead.
func omitTarget(label string) string {
	if label == "--companions" {
		return "the flag"
	}
	return `the "companions" key`
}

// canonicalSelfID normalises the path id the self-link gate compares against.
//
// The gate compares a CANONICALISED companion id against this, so while the raw
// positional arg was used, `update 0007 --companions '[{"category_id":7,…}]'`
// aliased category 7: every local gate passed and a self-link reached the wire,
// on the field whose semantics are REPLACE.
//
// Deliberately LOOSER than checkPositiveIntegerDigits, which is the opposite of
// what a payload validator wants and the right thing here. That function
// enforces the spec's narrow `^[1-9][0-9]*$` because the CLI must not generate
// a spelling the contract does not advertise. This is a defensive comparison,
// not a payload, so it has to be at least as broad as the server's own
// coercion: the spec records that `filter_var(FILTER_VALIDATE_INT)` also
// accepts `"+5"` and a leading-space `" 5"`, and a path segment of `0007`
// resolves to category 7 just the same. Normalising narrowly would reject
// those and silently DISABLE the check, which is how the first version of this
// left the alias open.
//
// "" means the id cannot name a category at all, which disables the self-check
// rather than failing the call — the server owns that refusal.
func canonicalSelfID(id string) string {
	t := strings.TrimSpace(id)
	t = strings.TrimPrefix(t, "+")
	t = strings.TrimLeft(t, "0")
	if t == "" || strings.TrimLeft(t, "0123456789") != "" {
		return ""
	}
	return t
}

// overlayValidatedCompanions validates whichever input supplied `companions`
// and writes the validated rows back over the assembled body. A no-op when
// neither input carries the key.
func overlayValidatedCompanions(body []byte, args RunArgs, selfID string) ([]byte, error) {
	// Canonicalise selfID before it is compared against canonicalised row ids.
	// The gate used to compare a canonical companion id against the RAW path
	// arg, so `update 0007 --companions '[{"category_id":7,…}]'` aliased
	// category 7 and shipped a self-link the server refuses — on the field
	// whose semantics are REPLACE. A path id that is not a positive integer id
	// cannot name a category at all, so it simply disables the self-check
	// rather than failing the call here; the server owns that refusal.
	selfID = canonicalSelfID(selfID)
	raw, label, present, err := companionsRawFromArgs(args)
	if err != nil {
		return nil, err
	}
	if !present {
		return body, nil
	}
	rows, err := parseCompanionsValue(raw, selfID, label)
	if err != nil {
		return nil, err
	}
	return overlayJSONField(body, "companions", rows)
}

// parseCompanionsValue validates a companions array against the spec's
// PricingCategoryCompanionInput shape, whichever input supplied it.
//
// It returns the rows as RawMessage so the operator's bytes reach the wire
// VERBATIM, for the reason parseFaresFlag does: decoding into `any` routes
// every number through float64 and silently rounds anything past 2^53.
//
// Every gate here mirrors a documented server 422, which on this endpoint is
// worth catching locally for a reason beyond the round trip: `companions`
// REPLACES the set, so a refused call leaves the existing links alone while a
// half-believed one does not. selfID is the category being updated, or "" on
// create — where a self-link is not expressible, the row having no id yet.
// label names the input in refusals, so a --data payload is not blamed on a
// flag the operator never typed.
//
// `[]` is legal and clears every link, unlike --fares where an empty array is
// a server 422. The caller distinguishes "clear" from "leave alone" by whether
// the key was supplied at all.
func parseCompanionsValue(raw json.RawMessage, selfID, label string) ([]json.RawMessage, error) {
	// `null` needs its own branch, and only the --data path can produce it:
	// the "json" flag type refuses bare scalars before the closure runs, so
	// `--companions null` never arrives, but `--data '{"companions":null}'`
	// decodes into a nil slice with NO error and would otherwise skip every
	// gate below. On a field whose semantics are REPLACE, and which the spec
	// types as a non-nullable array, that is out of contract in the one
	// direction that silently destroys data.
	//
	// nil is distinguishable from `[]` here: encoding/json leaves the slice nil
	// for `null` and allocates an empty one for `[]`, and `[]` is legal — it
	// clears the links deliberately.
	var rows []json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("%s: expected a JSON array of "+
			"{category_id, min_quantity} objects — pass '[]' to clear every link, or omit "+
			"%s to leave them unchanged: %w", label, omitTarget(label), err)
	}
	if rows == nil {
		return nil, fmt.Errorf("%s: expected a JSON array, got null. Pass '[]' to clear every "+
			"link, or omit %s to leave them unchanged — null is not in the contract and must "+
			"not be guessed at on a field that REPLACES the whole set", label, omitTarget(label))
	}
	if len(rows) > companionsMaxItems {
		return nil, fmt.Errorf("%s: %d links exceeds the %d-link cap", label, len(rows), companionsMaxItems)
	}
	seen := map[string]int{}
	for i, row := range rows {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(row, &obj); err != nil {
			return nil, fmt.Errorf("%s[%d]: expected an object with category_id and min_quantity, got %s", label, i, row)
		}
		// Only a JSON `null` element reaches here as a nil map — every other
		// non-object element fails the decode above.
		if obj == nil {
			return nil, fmt.Errorf("%s[%d]: expected an object, got null", label, i)
		}
		// The input schema is `additionalProperties: false`, so an unknown key
		// is a 422 rather than an ignored field. Named locally because a
		// typo'd key would otherwise be reported as the required key missing,
		// which sends the operator hunting for something already there.
		for k := range obj {
			if k != "category_id" && k != "min_quantity" {
				return nil, fmt.Errorf("%s[%d]: unknown key %q (the payload accepts only category_id and min_quantity)", label, i, k)
			}
		}
		rawID, ok := obj["category_id"]
		if !ok {
			return nil, fmt.Errorf(`%s[%d]: missing "category_id"`, label, i)
		}
		id, err := normalizePositiveIntegerID(rawID)
		if err != nil {
			return nil, fmt.Errorf("%s[%d].category_id: %w", label, i, err)
		}
		if selfID != "" && id == selfID {
			return nil, fmt.Errorf("%s[%d].category_id: %s is the category being updated — "+
				"a category cannot be required to travel with itself", label, i, id)
		}
		if prev, dup := seen[id]; dup {
			return nil, fmt.Errorf("%s[%d].category_id: %s is already linked at index %d; "+
				"each category may appear once (a category is bookable when ANY link is met, so a second "+
				"row for the same category cannot mean a stricter rule)", label, i, id, prev)
		}
		seen[id] = i

		rawQty, ok := obj["min_quantity"]
		if !ok {
			return nil, fmt.Errorf(`%s[%d]: missing "min_quantity"`, label, i)
		}
		var qty int
		if err := json.Unmarshal(rawQty, &qty); err != nil {
			return nil, fmt.Errorf("%s[%d].min_quantity: must be an integer between 1 and %d, got %s", label, i, companionMaxQuantity, rawQty)
		}
		if qty < 1 || qty > companionMaxQuantity {
			return nil, fmt.Errorf("%s[%d].min_quantity: must be between 1 and %d (got %d)", label, i, companionMaxQuantity, qty)
		}
	}
	return rows, nil
}

// companionMaxQuantity mirrors the spec's `maximum` on
// PricingCategoryCompanionInput.min_quantity.
const companionMaxQuantity = 999

// normalizePositiveIntegerID validates one spec `PositiveIntegerId` and
// returns it in its canonical decimal spelling, so two spellings of the same
// id compare equal.
//
// The schema is `{type: integer, minimum: 1}` OR
// `{type: string, pattern: "^[1-9][0-9]*$"}`: `5` and `"5"` are both accepted,
// while `"05"`, `"1e0"`, `"5.0"`, `true`, `0` and `-1` are 422s. The spec is
// deliberately narrower than the live validator on `"+5"` and `" 5"` — both
// accepted by accident of filter_var — and notes that narrowing is the safe
// direction, so this follows the schema rather than the accident.
//
// Decoded through json.Number rather than a numeric type, which preserves the
// literal: `5.0` stays "5.0" and `1e0` stays "1e0", so both are caught here
// instead of silently becoming 5.
func normalizePositiveIntegerID(raw json.RawMessage) (string, error) {
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return checkPositiveIntegerDigits(asString, raw)
	}
	var num json.Number
	if err := json.Unmarshal(raw, &num); err != nil {
		return "", fmt.Errorf("must be a positive integer id, as a number or a digit string (got %s)", raw)
	}
	return checkPositiveIntegerDigits(num.String(), raw)
}

func checkPositiveIntegerDigits(s string, raw json.RawMessage) (string, error) {
	if s == "" {
		return "", fmt.Errorf("must be a positive integer id, got an empty string")
	}
	if strings.TrimLeft(s, "0123456789") != "" || s[0] == '0' {
		return "", fmt.Errorf("must be a positive integer id matching ^[1-9][0-9]*$ (got %s)", raw)
	}
	return s, nil
}

// pricingCategoriesDefs declares the pricing-categories resource: list, get,
// create, update, delete, restore.
//
// PricingCategory is the parent bucket (e.g. "Adults", "Children") that
// PricingTier rows attach to. Tiers describe a headcount band and a fare;
// the named label and the product_id link live one level up here.
//
// Per spec: deletePricingCategory is `DryRunNotSupported` (Params is
// IdempotencyKey-only, no body). Soft-delete cascades to child PricingTiers.
func pricingCategoriesDefs() []CommandDef {
	return []CommandDef{
		{
			Use: "pricing-categories list", Short: "List pricing categories",
			Kind: KindRead, Verb: "GET", Path: "/pricing-categories", Ability: invpkg.Read,
			// NOTE: no --since. The `pricing_categories` table carries no
			// created_at / updated_at columns at all, so there is nothing for
			// the filter to bound — `/pricing-categories` REMOVED the parameter and now
			// answers 422. That 422 is deliberate on the server's part: a
			// silently-unfiltered page is what a polling client would
			// misread as "nothing changed since last run". There is no
			// incremental-sync signal here; list in full and diff
			// client-side. Re-adding the flag would not compile —
			// gen.ListPricingCategoriesParams has no Since field.
			Flags: []FlagDef{
				{Name: "limit", Type: "int", Min: 1, Description: "Page size"},
				{Name: "cursor", Type: "string", Description: "Pagination cursor"},
				{Name: "product-id", Type: "int", Min: 1, Description: "Filter by parent product"},
				{Name: "include-trashed", Type: "bool", Description: "Include soft-deleted"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				p := &gen.ListPricingCategoriesParams{}
				if v := args.FlagInt("limit"); v != 0 {
					p.Limit = &v
				}
				if v := args.FlagString("cursor"); v != "" {
					p.Cursor = &v
				}
				if args.FlagSet("product-id") {
					v := args.FlagInt("product-id")
					p.ProductId = &v
				}
				if args.FlagBool("include-trashed") {
					t := true
					p.IncludeTrashed = &t
				}
				resp, err := r.Client.ListPricingCategoriesWithResponse(ctx, p)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "PricingCategory", "")
			},
		},
		{
			Use: "pricing-categories get <id>", Short: "Show one pricing category",
			Kind: KindRead, Verb: "GET", Path: "/pricing-categories/{id}", Ability: invpkg.Read,
			PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.ShowPricingCategoryWithResponse(ctx, id)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "PricingCategory", id)
			},
		},
		{
			Use: "pricing-categories create", Short: "Create a pricing category",
			Kind: KindMutation, Verb: "POST", Path: "/pricing-categories",
			Ability: invpkg.Write, DryRunMode: DryRunBody,
			Long: "Create the parent bucket (e.g. Adults / Children) that PricingTier rows " +
				"will live under. --type is one of ADULT|CHILD|INFANT|YOUTH|STUDENT|SENIOR|" +
				"TRAVELLER|EU_CITIZEN|MILITARY|EU_CITIZEN_STUDENT (default TRAVELLER). --name " +
				"is translatable (stored as English).",
			Flags: []FlagDef{
				{Name: "product-id", Type: "string", Required: true, Description: "Parent product ID"},
				{Name: "name", Type: "string", Required: true, Description: "Bucket label (Adults, Children, …)"},
				{Name: "type", Type: "string", Description: "ADULT|CHILD|INFANT|YOUTH|STUDENT|SENIOR|TRAVELLER|EU_CITIZEN|MILITARY|EU_CITIZEN_STUDENT"},
				{Name: "min-age", Type: "int", Description: "Minimum age (optional)"},
				{Name: "max-age", Type: "int", Description: "Maximum age (optional)"},
				{Name: "is-internal", Type: "bool", Description: "Internal categories are excluded from public-facing checkout flows."},
				{Name: "companions", Type: "json", Description: companionsDesc},
			},
			// D20: companions decide whether the category can be booked at
			// all, and the write REPLACES the set. The audit records the
			// POST-image only — the value this call sent — because the CLI
			// never reads the category first and the response body is not
			// audited. So it answers "what did this call make the rule" and
			// never "which links did it delete"; reconstructing the removal
			// needs the prior audit row for the same category.
			ForensicFields: []string{"product-id", "name", "type", "is-internal", "companions"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"product-id":  "product_id",
					"name":        "name",
					"type":        "type",
					"min-age":     "min_age",
					"max-age":     "max_age",
					"is-internal": "is_internal",
				})
				if err != nil {
					return nil, err
				}
				// Validated and overlaid after the field map rather than
				// through it: JSONBodyFromArgs copies a json-typed flag
				// verbatim, so a malformed array would reach the wire
				// unchecked. Runs for --data too, not just the flag. selfID
				// is "" — a category cannot link to itself on create, having
				// no id yet.
				body, err = overlayValidatedCompanions(body, args, "")
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.CreatePricingCategoryWithBodyWithResponse(ctx, &gen.CreatePricingCategoryParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "PricingCategory", "")
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
		{
			Use: "pricing-categories update <id>", Short: "Update a pricing category",
			Kind: KindMutation, Verb: "PATCH", Path: "/pricing-categories/{id}",
			Ability: invpkg.Write, DryRunMode: DryRunBody,
			PositionalArgs: []string{"id"},
			Long: "Reparenting (changing product_id) is not supported via PATCH per spec — " +
				"create a new category if you need to move tiers across products.",
			Flags: []FlagDef{
				{Name: "name", Type: "string", Description: "Bucket label"},
				{Name: "type", Type: "string", Description: "ADULT|CHILD|INFANT|YOUTH|STUDENT|SENIOR|TRAVELLER|EU_CITIZEN|MILITARY|EU_CITIZEN_STUDENT"},
				{Name: "min-age", Type: "int", Description: "Minimum age"},
				{Name: "max-age", Type: "int", Description: "Maximum age"},
				{Name: "is-internal", Type: "bool", Description: "Internal categories are excluded from public-facing checkout flows. Setting it true is refused with 422 while another category's companions names this one."},
				{Name: "companions", Type: "json", Description: companionsDesc},
			},
			ForensicFields: []string{"name", "type", "is-internal", "companions"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"name":        "name",
					"type":        "type",
					"min-age":     "min_age",
					"max-age":     "max_age",
					"is-internal": "is_internal",
				})
				if err != nil {
					return nil, err
				}
				// See the create closure: validated outside the field map
				// because JSONBodyFromArgs copies a json flag verbatim, and
				// for --data as well as the flag. Here the path id IS the
				// self-link the server refuses, so it is passed in and caught
				// before the request.
				body, err = overlayValidatedCompanions(body, args, id)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.UpdatePricingCategoryWithBodyWithResponse(ctx, id, &gen.UpdatePricingCategoryParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "PricingCategory", id)
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
		{
			Use: "pricing-categories delete <id>", Short: "Soft-delete a pricing category",
			Kind: KindMutation, Verb: "DELETE", Path: "/pricing-categories/{id}",
			Ability: invpkg.Write, DryRunMode: DryRunNotSupported,
			PositionalArgs: []string{"id"},
			Long: "Soft-deletes the category and CASCADES to child PricingTiers. Also removes " +
				"every \"Must travel with\" link FROM or TO the category, so a category that " +
				"required this one can now be booked without it — and `restore` does NOT bring " +
				"those links back. Spec defines no dry-run input on this endpoint.",
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.DeletePricingCategoryWithResponse(ctx, id, &gen.DeletePricingCategoryParams{IdempotencyKey: args.IdempotencyKeyUUID})
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "PricingCategory", id)
			},
		},
		{
			Use: "pricing-categories restore <id>", Short: "Restore a soft-deleted pricing category",
			Kind: KindMutation, Verb: "POST", Path: "/pricing-categories/{id}/restore",
			Ability: invpkg.Write, DryRunMode: DryRunNotSupported,
			PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.RestorePricingCategoryWithResponse(ctx, id, &gen.RestorePricingCategoryParams{IdempotencyKey: args.IdempotencyKeyUUID})
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "PricingCategory", id)
			},
		},
	}
}
