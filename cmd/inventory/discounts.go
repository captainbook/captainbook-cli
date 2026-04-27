package inventory

import (
	"context"

	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/captainbook/captainbook-cli/internal/inventory/gen"
)

// discountsDefs declares discounts: list, get, create, delete, apply,
// restore.
//
// IMPORTANT: deleteDiscount uses query-param dry_run (DryRunMode: Query) —
// the only such endpoint in the inventory API. The closure forwards
// args.DryRun via *DeleteDiscountParams.DryRun rather than via the body.
//
// Tuned diff renderer: "Discount".
func discountsDefs() []CommandDef {
	return []CommandDef{
		{
			Use: "discounts list", Short: "List discounts", Kind: KindRead,
			Verb: "GET", Path: "/discounts", Ability: invpkg.Read,
			Flags: []FlagDef{
				{Name: "limit", Type: "int", Description: "Page size"},
				{Name: "cursor", Type: "string", Description: "Pagination cursor"},
				{Name: "code", Type: "string", Description: "Filter by code"},
				{Name: "product-option-id", Type: "string", Description: "Filter by option"},
				{Name: "auto-apply", Type: "bool", Description: "Filter auto-apply"},
				{Name: "include-trashed", Type: "bool", Description: "Include soft-deleted"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				p := &gen.ListDiscountsParams{}
				if v := args.FlagInt("limit"); v != 0 {
					p.Limit = &v
				}
				if v := args.FlagString("cursor"); v != "" {
					p.Cursor = &v
				}
				if v := args.FlagString("code"); v != "" {
					p.Code = &v
				}
				if v := args.FlagString("product-option-id"); v != "" {
					p.ProductOptionId = &v
				}
				if args.FlagSet("auto-apply") {
					b := args.FlagBool("auto-apply")
					p.AutoApply = &b
				}
				if args.FlagBool("include-trashed") {
					t := true
					p.IncludeTrashed = &t
				}
				resp, err := r.Client.ListDiscountsWithResponse(ctx, p)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Discount", "")
			},
		},
		{
			Use: "discounts get <id>", Short: "Show one discount", Kind: KindRead,
			Verb: "GET", Path: "/discounts/{id}", Ability: invpkg.Read,
			PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.ShowDiscountWithResponse(ctx, id)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Discount", id)
			},
		},
		{
			Use: "discounts create", Short: "Create a discount", Kind: KindMutation,
			Verb: "POST", Path: "/discounts", Ability: invpkg.Write,
			DryRunMode: DryRunBody,
			Long: "Create a discount. Provide exactly one of --discounted-price (fixed amount in " +
				"minor units) or --discount-pct (percentage, 0-100, float). Server returns 422 " +
				"if both or neither are provided. Omit --product-option-id for a global discount; " +
				"otherwise the discount is scoped to one option. " +
				"Pass --discount-pct via --data when fractional precision is required.",
			Flags: []FlagDef{
				{Name: "code", Type: "string", Required: true, Description: "Discount code"},
				{Name: "validity-start", Type: "string", Required: true, Description: "RFC3339 timestamp when validity begins"},
				{Name: "validity-end", Type: "string", Description: "RFC3339 timestamp when validity ends"},
				{Name: "start-date", Type: "string", Description: "RFC3339 promo window start"},
				{Name: "end-date", Type: "string", Description: "RFC3339 promo window end"},
				{Name: "discounted-price", Type: "int", Description: "Fixed discount amount in minor units (xor --discount-pct)"},
				{Name: "nb-offers", Type: "int", Description: "Maximum number of redemptions"},
				{Name: "auto-apply", Type: "bool", Description: "Auto-apply discount to matching bookings"},
				{Name: "product-option-id", Type: "string", Description: "Scope to a single product option"},
				{Name: "discount-text", Type: "string", Description: "Customer-facing label"},
				{Name: "discount-image", Type: "string", Description: "URL of accompanying image"},
			},
			ForensicFields: []string{"code", "discounted-price", "nb-offers", "auto-apply", "product-option-id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"code":              "code",
					"validity-start":    "validity_start",
					"validity-end":      "validity_end",
					"start-date":        "start_date",
					"end-date":          "end_date",
					"discounted-price":  "discounted_price",
					"nb-offers":         "nb_offers",
					"auto-apply":        "auto_apply",
					"product-option-id": "product_option_id",
					"discount-text":    "discount_text",
					"discount-image":   "discount_image",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.CreateDiscountWithBodyWithResponse(ctx, &gen.CreateDiscountParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil { return &RunResult{WireBody: body}, err }
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Discount", "")
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
		{
			// D32: deleteDiscount uses QUERY-param dry_run. The closure
			// forwards args.DryRun via *DeleteDiscountParams.DryRun rather
			// than via the body.
			Use: "discounts delete <id>", Short: "Soft-delete a discount", Kind: KindMutation,
			Verb: "DELETE", Path: "/discounts/{id}", Ability: invpkg.Write,
			DryRunMode:     DryRunQuery,
			PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				p := &gen.DeleteDiscountParams{IdempotencyKey: args.IdempotencyKeyUUID}
				if args.DryRun {
					t := true
					p.DryRun = &t
				}
				resp, err := r.Client.DeleteDiscountWithResponse(ctx, id, p)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Discount", id)
			},
		},
		{
			Use: "discounts apply <id>", Short: "Apply a discount to a booking",
			Kind: KindMutation, Verb: "POST", Path: "/discounts/{id}/apply",
			Ability: invpkg.Write, DryRunMode: DryRunBody,
			PositionalArgs: []string{"id"},
			Flags: []FlagDef{
				{Name: "booking-id", Type: "string", Required: true, Description: "Target booking ID"},
			},
			ForensicFields: []string{"booking-id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"booking-id": "booking_id",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.ApplyDiscountWithBodyWithResponse(ctx, id, &gen.ApplyDiscountParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil { return &RunResult{WireBody: body}, err }
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Discount", id)
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
		{
			Use: "discounts restore <id>", Short: "Restore a soft-deleted discount",
			Kind: KindMutation, Verb: "POST", Path: "/discounts/{id}/restore",
			Ability: invpkg.Write, DryRunMode: DryRunBody,
			PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, nil)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.RestoreDiscountWithBodyWithResponse(ctx, id, &gen.RestoreDiscountParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil { return &RunResult{WireBody: body}, err }
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Discount", id)
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
	}
}
