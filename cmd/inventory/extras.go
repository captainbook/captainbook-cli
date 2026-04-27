package inventory

import (
	"context"

	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/captainbook/captainbook-cli/internal/inventory/gen"
)

// extrasDefs declares the extras resource: list, get, create, update,
// delete, restore.
func extrasDefs() []CommandDef {
	return []CommandDef{
		{
			Use: "extras list", Short: "List extras", Kind: KindRead,
			Verb: "GET", Path: "/extras", Ability: invpkg.Read,
			Flags: []FlagDef{
				{Name: "limit", Type: "int"}, {Name: "cursor", Type: "string"},
				{Name: "product-option-id", Type: "string", Description: "Filter by option"},
				{Name: "include-trashed", Type: "bool"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				p := &gen.ListExtrasParams{}
				if v := args.FlagInt("limit"); v != 0 {
					p.Limit = &v
				}
				if v := args.FlagString("cursor"); v != "" {
					p.Cursor = &v
				}
				if v := args.FlagString("product-option-id"); v != "" {
					p.ProductOptionId = &v
				}
				if args.FlagBool("include-trashed") {
					t := true
					p.IncludeTrashed = &t
				}
				resp, err := r.Client.ListExtrasWithResponse(ctx, p)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Extra", "")
			},
		},
		{
			Use: "extras get <id>", Short: "Show one extra", Kind: KindRead,
			Verb: "GET", Path: "/extras/{id}", Ability: invpkg.Read,
			PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.ShowExtraWithResponse(ctx, id)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Extra", id)
			},
		},
		{
			Use: "extras create", Short: "Create an extra", Kind: KindMutation,
			Verb: "POST", Path: "/extras", Ability: invpkg.Write, DryRunMode: DryRunBody,
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				body, err := JSONBodyFromArgs(args, args.DryRun, nil)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.CreateExtraWithBodyWithResponse(ctx, &gen.CreateExtraParams{}, "application/json", asReader(body))
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Extra", "")
			},
		},
		{
			Use: "extras update <id>", Short: "Update an extra", Kind: KindMutation,
			Verb: "PATCH", Path: "/extras/{id}", Ability: invpkg.Write,
			DryRunMode: DryRunBody, PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, nil)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.UpdateExtraWithBodyWithResponse(ctx, id, &gen.UpdateExtraParams{}, "application/json", asReader(body))
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Extra", id)
			},
		},
		{
			Use: "extras delete <id>", Short: "Soft-delete an extra", Kind: KindMutation,
			Verb: "DELETE", Path: "/extras/{id}", Ability: invpkg.Write,
			DryRunMode: DryRunNotSupported, PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.DeleteExtraWithResponse(ctx, id, &gen.DeleteExtraParams{})
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Extra", id)
			},
		},
		{
			Use: "extras restore <id>", Short: "Restore a soft-deleted extra",
			Kind: KindMutation, Verb: "POST", Path: "/extras/{id}/restore",
			Ability: invpkg.Write, DryRunMode: DryRunBody, PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, nil)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.RestoreExtraWithBodyWithResponse(ctx, id, &gen.RestoreExtraParams{}, "application/json", asReader(body))
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Extra", id)
			},
		},
	}
}
