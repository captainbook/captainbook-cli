package inventory

import (
	"context"
	"fmt"
	"time"

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
				{Name: "since", Type: "string", Description: "ISO 8601 lower-bound on updated_at"},
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
				if v := args.FlagString("since"); v != "" {
					t, err := time.Parse(time.RFC3339, v)
					if err != nil {
						return nil, fmt.Errorf("--since: invalid RFC3339 timestamp: %w", err)
					}
					params.Since = &t
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
			Long: "Create a product. Private vs shared is controlled by --is-private: " +
				"private (true) means a single party books the whole experience; shared " +
				"(false, default) means multiple parties share the slot. When --is-private=false " +
				"the server forces is_priced_per_person=true and use_alternate_tier_pricing=false. " +
				"--must-validate-cancellation-policy=false (default) silently nulls both " +
				"--cancellation-policy and --cancellation-policy-link — set it to true to retain the policy. " +
				"--product-code is auto-generated from the title slug when omitted.",
			Flags: []FlagDef{
				{Name: "title", Type: "string", Required: true, Description: "Product title"},
				{Name: "currency", Type: "string", Required: true, Description: "ISO currency code (e.g. EUR, USD)"},
				{Name: "description", Type: "string", Description: "Product description (translatable rich text)"},
				{Name: "instructions", Type: "string", Description: "Rich text shown to confirmed customers"},
				{Name: "requirements", Type: "string", Description: "Rich text — what the customer needs to bring/know"},
				{Name: "inclusions", Type: "string", Description: "Rich text — what's included"},
				{Name: "exclusions", Type: "string", Description: "Rich text — what's not included"},
				{Name: "product-code", Type: "string", Description: "Tenant SKU (auto-gen from title when omitted)"},
				{Name: "status", Type: "string", Description: "draft|published"},
				{Name: "schedule-type", Type: "string", Description: "date|datetime"},
				{Name: "capacity", Type: "int", Description: "Default capacity"},
				{Name: "from-price", Type: "int", Description: "Starting price (minor units)"},
				{Name: "from-price-label", Type: "string", Description: "Caption next to from-price (e.g. \"From €50/person\")"},
				{Name: "category-ids", Type: "intSlice", Description: "Comma-separated category IDs (integer)"},
				{Name: "timezone", Type: "string", Description: "IANA timezone"},
				{Name: "locale", Type: "string", Description: "Default content locale (e.g. en, fr)"},
				{Name: "is-private", Type: "bool", Description: "Private (one party books the whole slot) vs shared (multiple parties)"},
				{Name: "is-priced-per-person", Type: "bool", Default: true, Description: "Price per traveler (true) vs per group/booking (false)"},
				{Name: "is-tier-priced", Type: "bool", Description: "Quantity-based pricing tiers apply"},
				{Name: "use-alternate-tier-pricing", Type: "bool", Description: "Alternate tier-pricing rendering"},
				{Name: "show-last-tier", Type: "bool", Description: "Show the last tier in customer-facing pricing"},
				{Name: "displayable", Type: "bool", Default: true, Description: "Show in widgets / catalog"},
				{Name: "must-validate-cancellation-policy", Type: "bool", Description: "Customer must accept the cancellation policy at checkout"},
				{Name: "cancellation-policy", Type: "string", Description: "Inline policy text (mutually exclusive with --cancellation-policy-link)"},
				{Name: "cancellation-policy-link", Type: "string", Description: "External policy URL (mutually exclusive with --cancellation-policy)"},
			},
			ForensicFields: []string{"from-price", "capacity", "status", "schedule-type", "is-private", "is-priced-per-person"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"title":                             "title",
					"currency":                          "currency",
					"description":                       "description",
					"instructions":                      "instructions",
					"requirements":                      "requirements",
					"inclusions":                        "inclusions",
					"exclusions":                        "exclusions",
					"product-code":                      "product_code",
					"status":                            "status",
					"schedule-type":                     "schedule_type",
					"capacity":                          "capacity",
					"from-price":                        "from_price",
					"from-price-label":                  "from_price_label",
					"category-ids":                      "category_ids",
					"timezone":                          "timezone",
					"locale":                            "locale",
					"is-private":                        "is_private",
					"is-priced-per-person":              "is_priced_per_person",
					"is-tier-priced":                    "is_tier_priced",
					"use-alternate-tier-pricing":        "use_alternate_tier_pricing",
					"show-last-tier":                    "show_last_tier",
					"displayable":                       "displayable",
					"must-validate-cancellation-policy": "must_validate_cancellation_policy",
					"cancellation-policy":               "cancellation_policy",
					"cancellation-policy-link":          "cancellation_policy_link",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.CreateProductWithBodyWithResponse(ctx, &gen.CreateProductParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil { return &RunResult{WireBody: body}, err }
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
			Long: "Update a product. Switching --is-private cascades a heavy inventory recompute " +
				"(7 jobs per 1000 availabilities); under --dry-run those side effects appear in " +
				"MutationResult.side_effects. Switching --schedule-type=date collapses Availability " +
				"windows to full-day spans and deletes resourceables. Same implicit overrides as " +
				"create: --must-validate-cancellation-policy=false nulls both policy fields; " +
				"--is-private=false forces is_priced_per_person=true and use_alternate_tier_pricing=false.",
			Flags: []FlagDef{
				{Name: "title", Type: "string", Description: "Product title"},
				{Name: "description", Type: "string", Description: "Product description (translatable rich text)"},
				{Name: "instructions", Type: "string", Description: "Rich text shown to confirmed customers"},
				{Name: "requirements", Type: "string", Description: "Rich text — what the customer needs to bring/know"},
				{Name: "inclusions", Type: "string", Description: "Rich text — what's included"},
				{Name: "exclusions", Type: "string", Description: "Rich text — what's not included"},
				{Name: "product-code", Type: "string", Description: "Tenant SKU"},
				{Name: "status", Type: "string", Description: "draft|published"},
				{Name: "schedule-type", Type: "string", Description: "date|datetime"},
				{Name: "capacity", Type: "int", Description: "Default capacity"},
				{Name: "from-price", Type: "int", Description: "Starting price (minor units)"},
				{Name: "from-price-label", Type: "string", Description: "Caption next to from-price"},
				{Name: "category-ids", Type: "intSlice", Description: "Comma-separated category IDs (integer)"},
				{Name: "timezone", Type: "string", Description: "IANA timezone"},
				{Name: "locale", Type: "string", Description: "Default content locale"},
				{Name: "is-private", Type: "bool", Description: "Private (one party / whole slot) vs shared — toggling cascades inventory jobs"},
				{Name: "is-priced-per-person", Type: "bool", Description: "Price per traveler vs per group"},
				{Name: "is-tier-priced", Type: "bool", Description: "Quantity-based pricing tiers apply"},
				{Name: "use-alternate-tier-pricing", Type: "bool", Description: "Alternate tier-pricing rendering"},
				{Name: "show-last-tier", Type: "bool", Description: "Show the last tier in customer-facing pricing"},
				{Name: "displayable", Type: "bool", Description: "Show in widgets / catalog"},
				{Name: "must-validate-cancellation-policy", Type: "bool", Description: "Customer must accept the cancellation policy at checkout"},
				{Name: "cancellation-policy", Type: "string", Description: "Inline policy text"},
				{Name: "cancellation-policy-link", Type: "string", Description: "External policy URL"},
			},
			ForensicFields: []string{"from-price", "capacity", "status", "schedule-type", "is-private", "is-priced-per-person"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"title":                             "title",
					"description":                       "description",
					"instructions":                      "instructions",
					"requirements":                      "requirements",
					"inclusions":                        "inclusions",
					"exclusions":                        "exclusions",
					"product-code":                      "product_code",
					"status":                            "status",
					"schedule-type":                     "schedule_type",
					"capacity":                          "capacity",
					"from-price":                        "from_price",
					"from-price-label":                  "from_price_label",
					"category-ids":                      "category_ids",
					"timezone":                          "timezone",
					"locale":                            "locale",
					"is-private":                        "is_private",
					"is-priced-per-person":              "is_priced_per_person",
					"is-tier-priced":                    "is_tier_priced",
					"use-alternate-tier-pricing":        "use_alternate_tier_pricing",
					"show-last-tier":                    "show_last_tier",
					"displayable":                       "displayable",
					"must-validate-cancellation-policy": "must_validate_cancellation_policy",
					"cancellation-policy":               "cancellation_policy",
					"cancellation-policy-link":          "cancellation_policy_link",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.UpdateProductWithBodyWithResponse(ctx, id, &gen.UpdateProductParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil { return &RunResult{WireBody: body}, err }
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
				resp, err := r.Client.DeleteProductWithResponse(ctx, id, &gen.DeleteProductParams{IdempotencyKey: args.IdempotencyKeyUUID})
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
				resp, err := r.Client.RestoreProductWithBodyWithResponse(ctx, id, &gen.RestoreProductParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil { return &RunResult{WireBody: body}, err }
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Product", id)
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
	}
}

