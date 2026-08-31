package inventory

import (
	"context"

	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/captainbook/captainbook-cli/internal/inventory/gen"
)

// categoriesDefs declares the categories resource: list, get.
//
// Categories are READ-ONLY at the CLI / tenant level. Tenants do not
// create / update / delete categories — those are platform-managed
// (operations happen through CB internal tooling, not the tenant CLI).
// The gen client carries create/update/delete methods (the spec exposes
// the routes) but they are intentionally NOT bound here.
func categoriesDefs() []CommandDef {
	return []CommandDef{
		{
			Use: "categories list", Short: "List categories", Kind: KindRead,
			Verb: "GET", Path: "/categories", Ability: invpkg.Read,
			// NOTE: no --since. The `product_categories` table carries no
			// created_at / updated_at columns at all, so there is nothing for
			// the filter to bound — `/categories` REMOVED the parameter and now
			// answers 422. That 422 is deliberate on the server's part: a
			// silently-unfiltered page is what a polling client would
			// misread as "nothing changed since last run". There is no
			// incremental-sync signal here; list in full and diff
			// client-side. Re-adding the flag would not compile —
			// gen.ListCategoriesParams has no Since field.
			Flags: []FlagDef{
				{Name: "limit", Type: "int"}, {Name: "cursor", Type: "string"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				p := &gen.ListCategoriesParams{}
				if v := args.FlagInt("limit"); v != 0 {
					p.Limit = &v
				}
				if v := args.FlagString("cursor"); v != "" {
					p.Cursor = &v
				}
				resp, err := r.Client.ListCategoriesWithResponse(ctx, p)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Category", "")
			},
		},
		{
			Use: "categories get <id>", Short: "Show one category", Kind: KindRead,
			Verb: "GET", Path: "/categories/{id}", Ability: invpkg.Read,
			PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.ShowCategoryWithResponse(ctx, id)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Category", id)
			},
		},
	}
}
