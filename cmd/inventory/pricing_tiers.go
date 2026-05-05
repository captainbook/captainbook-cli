package inventory

import (
	"context"
	"fmt"
	"time"

	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/captainbook/captainbook-cli/internal/inventory/gen"
)

// pricingTiersDefs declares pricing tiers: list, get, create, update,
// delete, restore.
//
// Per brief: deletePricingTier docs say "always --dry-run first" but the
// spec defines no dry_run input on the delete endpoint — treat as
// NotSupported. The user-facing recommendation lives in the long help.
//
// Tuned diff renderer: "PricingTier".
func pricingTiersDefs() []CommandDef {
	return []CommandDef{
		{
			Use: "pricing-tiers list", Short: "List pricing tiers", Kind: KindRead,
			Verb: "GET", Path: "/pricing-tiers", Ability: invpkg.Read,
			Flags: []FlagDef{
				{Name: "limit", Type: "int", Description: "Page size"},
				{Name: "cursor", Type: "string", Description: "Pagination cursor"},
				{Name: "product-id", Type: "string", Description: "Filter by parent product (via the pricing_category relation)"},
				{Name: "include-trashed", Type: "bool", Description: "Include soft-deleted"},
				{Name: "since", Type: "string", Description: "ISO 8601 lower-bound on updated_at"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				p := &gen.ListPricingTiersParams{}
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
				if v := args.FlagString("since"); v != "" {
					t, err := time.Parse(time.RFC3339, v)
					if err != nil {
						return nil, fmt.Errorf("--since: invalid RFC3339 timestamp: %w", err)
					}
					p.Since = &t
				}
				resp, err := r.Client.ListPricingTiersWithResponse(ctx, p)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "PricingTier", "")
			},
		},
		{
			Use: "pricing-tiers get <id>", Short: "Show one pricing tier", Kind: KindRead,
			Verb: "GET", Path: "/pricing-tiers/{id}", Ability: invpkg.Read,
			PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.ShowPricingTierWithResponse(ctx, id)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "PricingTier", id)
			},
		},
		{
			Use: "pricing-tiers create", Short: "Create a pricing tier", Kind: KindMutation,
			Verb: "POST", Path: "/pricing-tiers", Ability: invpkg.Write,
			DryRunMode: DryRunBody,
			Long: "Pricing tier = headcount band on a parent PricingCategory (the named bucket like " +
				"Adults/Children). --pricing-category-id and --amount are required; --min/--max " +
				"describe the inclusive headcount band (--max omitted = open-ended).",
			Flags: []FlagDef{
				{Name: "pricing-category-id", Type: "string", Required: true, Description: "Owning PricingCategory row"},
				{Name: "amount", Type: "int", Required: true, Description: "Price (minor units, persisted as `fare`)"},
				{Name: "min", Type: "int", Description: "Inclusive lower bound of the headcount band"},
				{Name: "max", Type: "int", Description: "Inclusive upper bound; omit for open-ended"},
			},
			ForensicFields: []string{"pricing-category-id", "amount", "min", "max"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"pricing-category-id": "pricing_category_id",
					"amount":              "amount",
					"min":                 "min",
					"max":                 "max",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.CreatePricingTierWithBodyWithResponse(ctx, &gen.CreatePricingTierParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil { return &RunResult{WireBody: body}, err }
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "PricingTier", "")
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
		{
			Use: "pricing-tiers update <id>", Short: "Update a pricing tier", Kind: KindMutation,
			Verb: "PATCH", Path: "/pricing-tiers/{id}", Ability: invpkg.Write,
			DryRunMode:     DryRunBody,
			PositionalArgs: []string{"id"},
			Long: "Update a tier. Sending --pricing-category-id reparents the tier under a " +
				"different PricingCategory (404 if the target category doesn't exist).",
			Flags: []FlagDef{
				{Name: "pricing-category-id", Type: "string", Description: "Reparent under a different PricingCategory"},
				{Name: "amount", Type: "int", Description: "Price (minor units, persisted as `fare`)"},
				{Name: "min", Type: "int", Description: "Inclusive lower bound of the headcount band"},
				{Name: "max", Type: "int", Description: "Inclusive upper bound; omit for open-ended"},
			},
			ForensicFields: []string{"pricing-category-id", "amount", "min", "max"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"pricing-category-id": "pricing_category_id",
					"amount":              "amount",
					"min":                 "min",
					"max":                 "max",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.UpdatePricingTierWithBodyWithResponse(ctx, id, &gen.UpdatePricingTierParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil { return &RunResult{WireBody: body}, err }
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "PricingTier", id)
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
		{
			Use: "pricing-tiers delete <id>", Short: "Soft-delete a pricing tier", Kind: KindMutation,
			Verb: "DELETE", Path: "/pricing-tiers/{id}", Ability: invpkg.Write,
			// D32: spec defines no dry-run input. Treat as NotSupported.
			DryRunMode: DryRunNotSupported,
			Long: "Soft-deletes a pricing tier. This endpoint does NOT support --dry-run; " +
				"docs recommend running `pricing-tiers get <id>` first to inspect downstream " +
				"impact (availabilities + bookings referencing this tier).",
			PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.DeletePricingTierWithResponse(ctx, id, &gen.DeletePricingTierParams{IdempotencyKey: args.IdempotencyKeyUUID})
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "PricingTier", id)
			},
		},
		{
			Use: "pricing-tiers restore <id>", Short: "Restore a soft-deleted pricing tier",
			Kind: KindMutation, Verb: "POST", Path: "/pricing-tiers/{id}/restore",
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
				resp, err := r.Client.RestorePricingTierWithBodyWithResponse(ctx, id, &gen.RestorePricingTierParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil { return &RunResult{WireBody: body}, err }
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "PricingTier", id)
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
	}
}
