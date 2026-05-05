package inventory

import (
	"context"

	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/captainbook/captainbook-cli/internal/inventory/gen"
)

// productOptionsDefs declares the product options resource: list, get,
// create, update, delete, restore.
//
// All endpoints support body-level dry-run except delete (verify in spec;
// per the brief, treat as NotSupported when in doubt).
func productOptionsDefs() []CommandDef {
	return []CommandDef{
		{
			Use: "product-options list", Short: "List product options", Kind: KindRead,
			Verb: "GET", Path: "/product-options", Ability: invpkg.Read,
			Flags: []FlagDef{
				{Name: "limit", Type: "int", Description: "Page size"},
				{Name: "cursor", Type: "string", Description: "Pagination cursor"},
				{Name: "product-id", Type: "string", Description: "Filter by parent product"},
				{Name: "include-trashed", Type: "bool", Description: "Include soft-deleted"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				params := &gen.ListProductOptionsParams{}
				if v := args.FlagInt("limit"); v != 0 {
					params.Limit = &v
				}
				if v := args.FlagString("cursor"); v != "" {
					params.Cursor = &v
				}
				if v := args.FlagString("product-id"); v != "" {
					params.ProductId = &v
				}
				if args.FlagBool("include-trashed") {
					t := true
					params.IncludeTrashed = &t
				}
				resp, err := r.Client.ListProductOptionsWithResponse(ctx, params)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "ProductOption", "")
			},
		},
		{
			Use: "product-options get <id>", Short: "Show one product option", Kind: KindRead,
			Verb: "GET", Path: "/product-options/{id}", Ability: invpkg.Read,
			PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.ShowProductOptionWithResponse(ctx, id)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "ProductOption", id)
			},
		},
		{
			Use: "product-options create", Short: "Create a product option", Kind: KindMutation,
			Verb: "POST", Path: "/product-options", Ability: invpkg.Write,
			DryRunMode: DryRunBody,
			Long: "ProductOption has no description or status of its own — those live on the " +
				"parent Product. --title is mapped onto the underlying `name` column. " +
				"--option-code is auto-generated from the title slug when omitted.",
			Flags: []FlagDef{
				{Name: "title", Type: "string", Required: true, Description: "Option title (persisted as `name`)"},
				{Name: "product-id", Type: "string", Required: true, Description: "Parent product ID"},
				{Name: "option-code", Type: "string", Description: "Tenant-supplied SKU (auto-generated from title when omitted)"},
				{Name: "capacity", Type: "int", Description: "Default capacity"},
				{Name: "min-age", Type: "int", Description: "Minimum allowed guest age"},
				{Name: "max-age", Type: "int", Description: "Maximum allowed guest age"},
			},
			ForensicFields: []string{"capacity", "product-id", "option-code"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"title":       "title",
					"product-id":  "product_id",
					"option-code": "option_code",
					"capacity":    "capacity",
					"min-age":     "min_age",
					"max-age":     "max_age",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.CreateProductOptionWithBodyWithResponse(ctx, &gen.CreateProductOptionParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil { return &RunResult{WireBody: body}, err }
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "ProductOption", "")
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
		{
			Use: "product-options update <id>", Short: "Update a product option", Kind: KindMutation,
			Verb: "PATCH", Path: "/product-options/{id}", Ability: invpkg.Write,
			DryRunMode:     DryRunBody,
			PositionalArgs: []string{"id"},
			Flags: []FlagDef{
				{Name: "title", Type: "string", Description: "Option title (persisted as `name`)"},
				{Name: "option-code", Type: "string", Description: "Tenant-supplied SKU"},
				{Name: "capacity", Type: "int", Description: "Default capacity"},
				{Name: "min-age", Type: "int", Description: "Minimum allowed guest age"},
				{Name: "max-age", Type: "int", Description: "Maximum allowed guest age"},
			},
			ForensicFields: []string{"capacity", "option-code"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"title":       "title",
					"option-code": "option_code",
					"capacity":    "capacity",
					"min-age":     "min_age",
					"max-age":     "max_age",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.UpdateProductOptionWithBodyWithResponse(ctx, id, &gen.UpdateProductOptionParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil { return &RunResult{WireBody: body}, err }
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "ProductOption", id)
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
		{
			Use: "product-options delete <id>", Short: "Soft-delete a product option", Kind: KindMutation,
			Verb: "DELETE", Path: "/product-options/{id}", Ability: invpkg.Write,
			// Per brief: "DeleteProductOption (verify in spec)" — spec has no dry-run input
			// on the delete params shape, so treat as NotSupported.
			DryRunMode:     DryRunNotSupported,
			PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.DeleteProductOptionWithResponse(ctx, id, &gen.DeleteProductOptionParams{IdempotencyKey: args.IdempotencyKeyUUID})
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "ProductOption", id)
			},
		},
		{
			Use: "product-options restore <id>", Short: "Restore a soft-deleted product option",
			Kind: KindMutation, Verb: "POST", Path: "/product-options/{id}/restore",
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
				resp, err := r.Client.RestoreProductOptionWithBodyWithResponse(ctx, id, &gen.RestoreProductOptionParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil { return &RunResult{WireBody: body}, err }
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "ProductOption", id)
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
	}
}
