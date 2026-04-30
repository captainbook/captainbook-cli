package inventory

import (
	"context"
	"fmt"
	"time"

	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/captainbook/captainbook-cli/internal/inventory/gen"
)

// resourcesDefs declares the resources resource: list, get, create, update,
// delete, restore, plus attach/detach to a ProductOption.
//
// A Resource is physical or human inventory (a boat, a guide, a yoga studio)
// that constrains a ProductOption's bookable capacity. `category` is the
// kind (guide|asset|equipment|auxiliary); `type` is a free-form tenant label
// like "Sailboat" or "Senior Guide". `capacity` is optional — null means
// the resource doesn't constrain seat count by itself.
//
// Per spec: deleteResource is `DryRunNotSupported`. Attach/detach use the
// nested route /product-options/{id}/resources.
func resourcesDefs() []CommandDef {
	return []CommandDef{
		{
			Use: "resources list", Short: "List resources",
			Kind: KindRead, Verb: "GET", Path: "/resources", Ability: invpkg.Read,
			Flags: []FlagDef{
				{Name: "limit", Type: "int", Description: "Page size"},
				{Name: "cursor", Type: "string", Description: "Pagination cursor"},
				{Name: "category", Type: "string", Description: "guide|asset|equipment|auxiliary"},
				{Name: "include-trashed", Type: "bool", Description: "Include soft-deleted"},
				{Name: "since", Type: "string", Description: "ISO 8601 lower-bound on updated_at"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				p := &gen.ListResourcesParams{}
				if v := args.FlagInt("limit"); v != 0 {
					p.Limit = &v
				}
				if v := args.FlagString("cursor"); v != "" {
					p.Cursor = &v
				}
				if v := args.FlagString("category"); v != "" {
					c := gen.ListResourcesParamsCategory(v)
					p.Category = &c
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
				resp, err := r.Client.ListResourcesWithResponse(ctx, p)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Resource", "")
			},
		},
		{
			Use: "resources get <id>", Short: "Show one resource",
			Kind: KindRead, Verb: "GET", Path: "/resources/{id}", Ability: invpkg.Read,
			PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.ShowResourceWithResponse(ctx, id)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Resource", id)
			},
		},
		{
			Use: "resources create", Short: "Create a resource",
			Kind: KindMutation, Verb: "POST", Path: "/resources",
			Ability: invpkg.Write, DryRunMode: DryRunBody,
			Long: "Create a Resource (physical/human inventory). Use --category to classify " +
				"(guide|asset|equipment|auxiliary) and --type for the free-form tenant label " +
				"(e.g. 'Sailboat', 'Senior Guide'). --capacity is optional — omit if the " +
				"resource doesn't bound seat count on its own.",
			Flags: []FlagDef{
				{Name: "name", Type: "string", Required: true, Description: "Resource name"},
				{Name: "type", Type: "string", Required: true, Description: "Free-form label (Sailboat, Senior Guide, …)"},
				{Name: "category", Type: "string", Required: true, Description: "guide|asset|equipment|auxiliary"},
				{Name: "capacity", Type: "int", Description: "Default capacity (null = no per-resource cap)"},
			},
			ForensicFields: []string{"name", "type", "category", "capacity"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"name":     "name",
					"type":     "type",
					"category": "category",
					"capacity": "capacity",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.CreateResourceWithBodyWithResponse(ctx, &gen.CreateResourceParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Resource", "")
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
		{
			Use: "resources update <id>", Short: "Update a resource",
			Kind: KindMutation, Verb: "PATCH", Path: "/resources/{id}",
			Ability: invpkg.Write, DryRunMode: DryRunBody,
			PositionalArgs: []string{"id"},
			Flags: []FlagDef{
				{Name: "name", Type: "string", Description: "Resource name"},
				{Name: "type", Type: "string", Description: "Free-form label"},
				{Name: "category", Type: "string", Description: "guide|asset|equipment|auxiliary"},
				{Name: "capacity", Type: "int", Description: "Default capacity"},
			},
			ForensicFields: []string{"name", "category", "capacity"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"name":     "name",
					"type":     "type",
					"category": "category",
					"capacity": "capacity",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.UpdateResourceWithBodyWithResponse(ctx, id, &gen.UpdateResourceParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Resource", id)
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
		{
			Use: "resources delete <id>", Short: "Soft-delete a resource",
			Kind: KindMutation, Verb: "DELETE", Path: "/resources/{id}",
			Ability: invpkg.Write, DryRunMode: DryRunNotSupported,
			PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.DeleteResourceWithResponse(ctx, id, &gen.DeleteResourceParams{IdempotencyKey: args.IdempotencyKeyUUID})
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Resource", id)
			},
		},
		{
			Use: "resources restore <id>", Short: "Restore a soft-deleted resource",
			Kind: KindMutation, Verb: "POST", Path: "/resources/{id}/restore",
			Ability: invpkg.Write, DryRunMode: DryRunNotSupported,
			PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.RestoreResourceWithResponse(ctx, id, &gen.RestoreResourceParams{IdempotencyKey: args.IdempotencyKeyUUID})
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Resource", id)
			},
		},
		{
			Use: "resources attach <option-id>", Short: "Attach a resource to a product option",
			Kind: KindMutation, Verb: "POST", Path: "/product-options/{id}/resources",
			Ability: invpkg.Write, DryRunMode: DryRunBody,
			PositionalArgs: []string{"option-id"},
			Long: "Attaches a Resource to a ProductOption (writes the resourceables polymorphic " +
				"pivot). Idempotent — re-attaching an already-linked resource rewrites the " +
				"pivot's optional fields (capacity / seniority).",
			Flags: []FlagDef{
				{Name: "resource-id", Type: "string", Required: true, Description: "Resource to attach"},
				{Name: "capacity", Type: "int", Description: "Override the Resource's capacity for this attachment"},
				{Name: "seniority", Type: "int", Description: "Pivot-level seniority override"},
			},
			ForensicFields: []string{"resource-id", "capacity", "seniority"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				optionID, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"resource-id": "resource_id",
					"capacity":    "capacity",
					"seniority":   "seniority",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.AttachResourceToProductOptionWithBodyWithResponse(ctx, optionID, &gen.AttachResourceToProductOptionParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Resource", optionID)
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
		{
			Use: "resources detach <option-id> <resource-id>", Short: "Detach a resource from a product option",
			Kind: KindMutation, Verb: "DELETE", Path: "/product-options/{option_id}/resources/{resource_id}",
			Ability: invpkg.Write, DryRunMode: DryRunNotSupported,
			PositionalArgs: []string{"option-id", "resource-id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				if len(args.PathArgs) < 2 {
					return nil, fmt.Errorf("resources detach requires <option-id> <resource-id>")
				}
				optionID := args.PathArgs[0]
				resourceID := args.PathArgs[1]
				resp, err := r.Client.DetachResourceFromProductOptionWithResponse(ctx, optionID, resourceID, &gen.DetachResourceFromProductOptionParams{IdempotencyKey: args.IdempotencyKeyUUID})
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Resource", resourceID)
			},
		},
	}
}
