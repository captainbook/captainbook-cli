package inventory

import (
	"context"
	"fmt"
	"time"

	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/captainbook/captainbook-cli/internal/inventory/gen"
)

// partnersDefs declares the partners resource: list and get.
//
// Read-only by design. The server mounts both routes outside any
// `abilities:cli:write` group ("Partners (selling partners and channel
// partners) — read-only" in routes/api_cli_v1.php), and the spec publishes no
// create/update/delete, so there is deliberately no write surface here.
func partnersDefs() []CommandDef {
	return []CommandDef{
		{
			Use: "partners list", Short: "List partners (selling partners and channel partners)",
			Kind: KindRead, Verb: "GET", Path: "/partners", Ability: invpkg.Read,
			Flags: []FlagDef{
				{Name: "limit", Type: "int", Min: 1, Description: "Page size"},
				{Name: "cursor", Type: "string", Description: "Pagination cursor"},
				{Name: "since", Type: "string", Description: "ISO 8601 lower-bound on updated_at"},
				{Name: "include-trashed", Type: "bool", Description: "Include soft-deleted rows"},
				{Name: "q", Type: "string", Description: "Free-text search over partner name"},
				{Name: "partner-type", Type: "string", Description: "channel|selling_partner"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				p := &gen.ListPartnersParams{}
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
				if args.FlagBool("include-trashed") {
					t := true
					p.IncludeTrashed = &t
				}
				if v := args.FlagString("q"); v != "" {
					p.Q = &v
				}
				if v := args.FlagString("partner-type"); v != "" {
					pt := gen.ListPartnersParamsPartnerType(v)
					p.PartnerType = &pt
				}
				resp, err := r.Client.ListPartnersWithResponse(ctx, p)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Partner", "")
			},
		},
		{
			Use: "partners get <id>", Short: "Show one partner", Kind: KindRead,
			Verb: "GET", Path: "/partners/{id}", Ability: invpkg.Read,
			PositionalArgs: []string{"id"},
			Flags: []FlagDef{
				{Name: "include-trashed", Type: "bool", Description: "Include soft-deleted rows"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				p := &gen.ShowPartnerParams{}
				if args.FlagBool("include-trashed") {
					t := true
					p.IncludeTrashed = &t
				}
				resp, err := r.Client.ShowPartnerWithResponse(ctx, id, p)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Partner", id)
			},
		},
	}
}
