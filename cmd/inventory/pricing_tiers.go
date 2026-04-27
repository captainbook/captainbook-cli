package inventory

import (
	"context"

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
				{Name: "product-option-id", Type: "string", Description: "Filter by option"},
				{Name: "availability-id", Type: "string", Description: "Filter by availability"},
				{Name: "include-trashed", Type: "bool", Description: "Include soft-deleted"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				p := &gen.ListPricingTiersParams{}
				if v := args.FlagInt("limit"); v != 0 {
					p.Limit = &v
				}
				if v := args.FlagString("cursor"); v != "" {
					p.Cursor = &v
				}
				if v := args.FlagString("product-option-id"); v != "" {
					p.ProductOptionId = &v
				}
				if v := args.FlagString("availability-id"); v != "" {
					p.AvailabilityId = &v
				}
				if args.FlagBool("include-trashed") {
					t := true
					p.IncludeTrashed = &t
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
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				body, err := JSONBodyFromArgs(args, args.DryRun, nil)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.CreatePricingTierWithBodyWithResponse(ctx, &gen.CreatePricingTierParams{}, "application/json", asReader(body))
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "PricingTier", "")
			},
		},
		{
			Use: "pricing-tiers update <id>", Short: "Update a pricing tier", Kind: KindMutation,
			Verb: "PATCH", Path: "/pricing-tiers/{id}", Ability: invpkg.Write,
			DryRunMode:     DryRunBody,
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
				resp, err := r.Client.UpdatePricingTierWithBodyWithResponse(ctx, id, &gen.UpdatePricingTierParams{}, "application/json", asReader(body))
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "PricingTier", id)
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
				resp, err := r.Client.DeletePricingTierWithResponse(ctx, id, &gen.DeletePricingTierParams{})
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
				resp, err := r.Client.RestorePricingTierWithBodyWithResponse(ctx, id, &gen.RestorePricingTierParams{}, "application/json", asReader(body))
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "PricingTier", id)
			},
		},
	}
}
