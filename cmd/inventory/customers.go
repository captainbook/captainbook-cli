package inventory

import (
	"context"
	"fmt"
	"time"

	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/captainbook/captainbook-cli/internal/inventory/gen"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

// customersDefs declares customer commands: list, get.
//
// Customers are read-only at the CLI level — they're created server-side
// as a side effect of bookings, and customer self-service updates happen
// via the customer portal, not the CLI.
func customersDefs() []CommandDef {
	return []CommandDef{
		{
			Use: "customers list", Short: "List customers", Kind: KindRead,
			Verb: "GET", Path: "/customers", Ability: invpkg.Read,
			Flags: []FlagDef{
				{Name: "limit", Type: "int"},
				{Name: "cursor", Type: "string"},
				{Name: "q", Type: "string", Description: "Free-text search"},
				{Name: "email", Type: "string", Description: "Filter by exact email"},
				{Name: "country", Type: "string", Description: "ISO-3166-1 alpha-2"},
				{Name: "include-trashed", Type: "bool", Description: "Include soft-deleted"},
				{Name: "since", Type: "string", Description: "ISO 8601 lower-bound on updated_at"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				p := &gen.ListCustomersParams{}
				if v := args.FlagInt("limit"); v != 0 {
					p.Limit = &v
				}
				if v := args.FlagString("cursor"); v != "" {
					p.Cursor = &v
				}
				if v := args.FlagString("q"); v != "" {
					p.Q = &v
				}
				if v := args.FlagString("email"); v != "" {
					e := openapi_types.Email(v)
					p.Email = &e
				}
				if v := args.FlagString("country"); v != "" {
					p.Country = &v
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
				resp, err := r.Client.ListCustomersWithResponse(ctx, p)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Customer", "")
			},
		},
		{
			Use: "customers get <id>", Short: "Show one customer", Kind: KindRead,
			Verb: "GET", Path: "/customers/{id}", Ability: invpkg.Read,
			PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.ShowCustomerWithResponse(ctx, id)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Customer", id)
			},
		},
	}
}
