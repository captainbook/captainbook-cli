package inventory

import (
	"context"
	"fmt"
	"time"

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
			Flags: []FlagDef{
				{Name: "limit", Type: "int"}, {Name: "cursor", Type: "string"},
				{Name: "since", Type: "string", Description: "ISO 8601 lower-bound on updated_at"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				p := &gen.ListCategoriesParams{}
				if v := args.FlagInt("limit"); v != 0 {
					p.Limit = &v
				}
				if v := args.FlagString("cursor"); v != "" {
					p.Cursor = &v
				}
				if v := args.FlagString("since"); v != "" {
					t, err := time.Parse(time.RFC3339, v)
					if err != nil {
						return nil, fmt.Errorf("--since: invalid RFC3339 timestamp: %w", err)
					}
					p.Since = &t
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
