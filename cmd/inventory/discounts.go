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
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				body, err := JSONBodyFromArgs(args, args.DryRun, nil)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.CreateDiscountWithBodyWithResponse(ctx, &gen.CreateDiscountParams{}, "application/json", asReader(body))
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Discount", "")
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
				p := &gen.DeleteDiscountParams{}
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
				resp, err := r.Client.ApplyDiscountWithBodyWithResponse(ctx, id, &gen.ApplyDiscountParams{}, "application/json", asReader(body))
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Discount", id)
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
				resp, err := r.Client.RestoreDiscountWithBodyWithResponse(ctx, id, &gen.RestoreDiscountParams{}, "application/json", asReader(body))
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Discount", id)
			},
		},
	}
}
