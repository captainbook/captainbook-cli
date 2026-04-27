package inventory

import (
	"context"

	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/captainbook/captainbook-cli/internal/inventory/gen"
)

// categoriesDefs declares the categories resource: list, get, create.
//
// Update + delete exist server-side too (the gen client has them) but per
// the brief the v1 surface is list/get/create. Update + delete are
// available through `--data` if the user really needs them by extending
// the table here later.
func categoriesDefs() []CommandDef {
	return []CommandDef{
		{
			Use: "categories list", Short: "List categories", Kind: KindRead,
			Verb: "GET", Path: "/categories", Ability: invpkg.Read,
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
		{
			Use: "categories create", Short: "Create a category", Kind: KindMutation,
			Verb: "POST", Path: "/categories", Ability: invpkg.Write, DryRunMode: DryRunBody,
			Flags: []FlagDef{
				{Name: "name", Type: "string", Required: true, Description: "Category name"},
				{Name: "slug", Type: "string", Description: "URL slug"},
				{Name: "description", Type: "string", Description: "Category description"},
				{Name: "position", Type: "int", Description: "Sort position"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"name":        "name",
					"slug":        "slug",
					"description": "description",
					"position":    "position",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.CreateCategoryWithBodyWithResponse(ctx, &gen.CreateCategoryParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil { return &RunResult{WireBody: body}, err }
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Category", "")
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
	}
}
