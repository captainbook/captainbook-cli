package inventory

import (
	"context"

	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/captainbook/captainbook-cli/internal/inventory/gen"
)

// productsDefs declares the products resource: list, get, create, update,
// delete, restore. Multipart media upload + media list/delete live in
// media.go since they're a separate sub-tree (`ceebee inventory media …`).
//
// Per spec: deleteProduct has NO dry-run support (D32: NotSupported).
// Update + create + restore use body-level dry_run (D24).
//
// Tuned diff renderer: "Product" — RenderProductDiff is invoked when
// a successful dry-run returns a MutationResult diff envelope.
func productsDefs() []CommandDef {
	return []CommandDef{
		{
			Use: "products list", Short: "List products", Kind: KindRead,
			Verb: "GET", Path: "/products", Ability: invpkg.Read,
			Flags: []FlagDef{
				{Name: "limit", Type: "int", Description: "Page size (1-200, default 50)"},
				{Name: "cursor", Type: "string", Description: "Pagination cursor"},
				{Name: "q", Type: "string", Description: "Free-text search"},
				{Name: "category", Type: "string", Description: "Filter by category slug or ID"},
				{Name: "include-trashed", Type: "bool", Description: "Include soft-deleted"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				params := &gen.ListProductsParams{}
				if v := args.FlagInt("limit"); v != 0 {
					params.Limit = &v
				}
				if v := args.FlagString("cursor"); v != "" {
					params.Cursor = &v
				}
				if v := args.FlagString("q"); v != "" {
					params.Q = &v
				}
				if v := args.FlagString("category"); v != "" {
					params.Category = &v
				}
				if args.FlagBool("include-trashed") {
					t := true
					params.IncludeTrashed = &t
				}
				resp, err := r.Client.ListProductsWithResponse(ctx, params)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Product", "")
			},
		},
		{
			Use: "products get <id>", Short: "Show one product", Kind: KindRead,
			Verb: "GET", Path: "/products/{id}", Ability: invpkg.Read,
			PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.ShowProductWithResponse(ctx, id)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Product", id)
			},
		},
		{
			Use: "products create", Short: "Create a product", Kind: KindMutation,
			Verb: "POST", Path: "/products", Ability: invpkg.Write,
			DryRunMode: DryRunBody,
			Flags: []FlagDef{
				{Name: "title", Type: "string", Required: true, Description: "Product title"},
				{Name: "currency", Type: "string", Required: true, Description: "ISO currency code (e.g. EUR, USD)"},
				{Name: "description", Type: "string", Description: "Product description"},
				{Name: "status", Type: "string", Description: "draft|published"},
				{Name: "schedule-type", Type: "string", Description: "FIXED|FLEXIBLE"},
				{Name: "capacity", Type: "int", Description: "Default capacity"},
				{Name: "cancellation-policy", Type: "string", Description: "Cancellation policy text"},
				{Name: "from-price", Type: "int", Description: "Starting price (minor units)"},
				{Name: "category-ids", Type: "stringSlice", Description: "Comma-separated category IDs"},
				{Name: "slug", Type: "string", Description: "URL slug (auto-generated if omitted)"},
				{Name: "timezone", Type: "string", Description: "IANA timezone"},
			},
			ForensicFields: []string{"from-price", "capacity", "status", "schedule-type", "cancellation-policy"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"title":               "title",
					"currency":            "currency",
					"description":         "description",
					"status":              "status",
					"schedule-type":       "schedule_type",
					"capacity":            "capacity",
					"cancellation-policy": "cancellation_policy",
					"from-price":          "from_price",
					"category-ids":        "category_ids",
					"slug":                "slug",
					"timezone":            "timezone",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.CreateProductWithBodyWithResponse(ctx, &gen.CreateProductParams{}, "application/json", asReader(body))
				if err != nil {
					return nil, err
				}
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Product", "")
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
		{
			Use: "products update <id>", Short: "Update a product", Kind: KindMutation,
			Verb: "PATCH", Path: "/products/{id}", Ability: invpkg.Write,
			DryRunMode:     DryRunBody,
			PositionalArgs: []string{"id"},
			Flags: []FlagDef{
				{Name: "title", Type: "string", Description: "Product title"},
				{Name: "description", Type: "string", Description: "Product description"},
				{Name: "status", Type: "string", Description: "draft|published|archived"},
				{Name: "schedule-type", Type: "string", Description: "FIXED|FLEXIBLE"},
				{Name: "capacity", Type: "int", Description: "Default capacity"},
				{Name: "cancellation-policy", Type: "string", Description: "Cancellation policy text"},
				{Name: "from-price", Type: "int", Description: "Starting price (minor units)"},
				{Name: "category-ids", Type: "stringSlice", Description: "Comma-separated category IDs"},
				{Name: "slug", Type: "string", Description: "URL slug"},
				{Name: "timezone", Type: "string", Description: "IANA timezone"},
			},
			ForensicFields: []string{"from-price", "capacity", "status", "schedule-type", "cancellation-policy"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"title":               "title",
					"description":         "description",
					"status":              "status",
					"schedule-type":       "schedule_type",
					"capacity":            "capacity",
					"cancellation-policy": "cancellation_policy",
					"from-price":          "from_price",
					"category-ids":        "category_ids",
					"slug":                "slug",
					"timezone":            "timezone",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.UpdateProductWithBodyWithResponse(ctx, id, &gen.UpdateProductParams{}, "application/json", asReader(body))
				if err != nil {
					return nil, err
				}
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Product", id)
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
		{
			Use: "products delete <id>", Short: "Soft-delete a product", Kind: KindMutation,
			Verb: "DELETE", Path: "/products/{id}", Ability: invpkg.Write,
			DryRunMode:     DryRunNotSupported, // D32: spec has no dry-run input.
			PositionalArgs: []string{"id"},
			Long: "Soft-deletes a product. NOTE: this endpoint does not support --dry-run; " +
				"the spec defines no dry-run input. Use `products get <id>` first to verify " +
				"the resource state before deletion.",
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.DeleteProductWithResponse(ctx, id, &gen.DeleteProductParams{})
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Product", id)
			},
		},
		{
			Use: "products restore <id>", Short: "Restore a soft-deleted product",
			Kind: KindMutation, Verb: "POST", Path: "/products/{id}/restore",
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
				resp, err := r.Client.RestoreProductWithBodyWithResponse(ctx, id, &gen.RestoreProductParams{}, "application/json", asReader(body))
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Product", id)
			},
		},
	}
}

