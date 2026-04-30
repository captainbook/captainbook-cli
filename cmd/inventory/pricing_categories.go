package inventory

import (
	"context"
	"fmt"
	"time"

	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/captainbook/captainbook-cli/internal/inventory/gen"
)

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
			Flags: []FlagDef{
				{Name: "limit", Type: "int", Description: "Page size"},
				{Name: "cursor", Type: "string", Description: "Pagination cursor"},
				{Name: "product-id", Type: "string", Description: "Filter by parent product"},
				{Name: "include-trashed", Type: "bool", Description: "Include soft-deleted"},
				{Name: "since", Type: "string", Description: "ISO 8601 lower-bound on updated_at"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				p := &gen.ListPricingCategoriesParams{}
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
			},
			ForensicFields: []string{"product-id", "name", "type"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"product-id": "product_id",
					"name":       "name",
					"type":       "type",
					"min-age":    "min_age",
					"max-age":    "max_age",
				})
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
			},
			ForensicFields: []string{"name", "type"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"name":    "name",
					"type":    "type",
					"min-age": "min_age",
					"max-age": "max_age",
				})
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
			Long: "Soft-deletes the category and CASCADES to child PricingTiers. Spec defines " +
				"no dry-run input on this endpoint.",
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
