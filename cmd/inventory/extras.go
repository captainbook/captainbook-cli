package inventory

import (
	"context"
	"fmt"
	"time"

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
				{Name: "product-id", Type: "string", Description: "Filter by product"},
				{Name: "include-trashed", Type: "bool"},
				{Name: "since", Type: "string", Description: "ISO 8601 lower-bound on updated_at"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				p := &gen.ListExtrasParams{}
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
			Flags: []FlagDef{
				{Name: "name", Type: "string", Required: true, Description: "Extra name"},
				{Name: "amount", Type: "int", Required: true, Description: "Price in minor units"},
				{Name: "currency", Type: "string", Required: true, Description: "ISO currency code"},
				{Name: "product-id", Type: "string", Required: true, Description: "Owning product"},
				{Name: "description", Type: "string", Description: "Extra description"},
				{Name: "max-quantity", Type: "int", Description: "Maximum purchasable per booking"},
			},
			ForensicFields: []string{"amount", "max-quantity", "product-id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"name":         "name",
					"amount":       "amount",
					"currency":     "currency",
					"product-id":   "product_id",
					"description":  "description",
					"max-quantity": "max_quantity",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.CreateExtraWithBodyWithResponse(ctx, &gen.CreateExtraParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil { return &RunResult{WireBody: body}, err }
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Extra", "")
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
		{
			Use: "extras update <id>", Short: "Update an extra", Kind: KindMutation,
			Verb: "PATCH", Path: "/extras/{id}", Ability: invpkg.Write,
			DryRunMode: DryRunBody, PositionalArgs: []string{"id"},
			Flags: []FlagDef{
				{Name: "name", Type: "string", Description: "Extra name"},
				{Name: "amount", Type: "int", Description: "Price in minor units"},
				{Name: "description", Type: "string", Description: "Extra description"},
				{Name: "max-quantity", Type: "int", Description: "Maximum purchasable per booking"},
			},
			ForensicFields: []string{"amount", "max-quantity"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"name":         "name",
					"amount":       "amount",
					"description":  "description",
					"max-quantity": "max_quantity",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.UpdateExtraWithBodyWithResponse(ctx, id, &gen.UpdateExtraParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil { return &RunResult{WireBody: body}, err }
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Extra", id)
				if res != nil {
					res.WireBody = body
				}
				return res, err
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
				resp, err := r.Client.DeleteExtraWithResponse(ctx, id, &gen.DeleteExtraParams{IdempotencyKey: args.IdempotencyKeyUUID})
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
				resp, err := r.Client.RestoreExtraWithBodyWithResponse(ctx, id, &gen.RestoreExtraParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil { return &RunResult{WireBody: body}, err }
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Extra", id)
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
	}
}
