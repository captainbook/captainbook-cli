package inventory

import (
	"context"
	"fmt"
	"time"

	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/captainbook/captainbook-cli/internal/inventory/gen"
)

// locationsDefs declares the locations resource: list, get, create, update,
// delete.
//
// Locations are start / end / meeting points referenced by products. Type
// enum (PRIMARY|START|END|VISITED|SECONDARY) lives on both list filter and
// create/update body.
//
// Per spec: deleteLocation has NO dry-run (Params carries only IdempotencyKey,
// no body). Returns 409 if the location is still referenced by published
// products — caller must detach first.
func locationsDefs() []CommandDef {
	return []CommandDef{
		{
			Use: "locations list", Short: "List locations (start, end, meeting points)",
			Kind: KindRead, Verb: "GET", Path: "/locations", Ability: invpkg.Read,
			Flags: []FlagDef{
				{Name: "limit", Type: "int", Description: "Page size"},
				{Name: "cursor", Type: "string", Description: "Pagination cursor"},
				{Name: "type", Type: "string", Description: "PRIMARY|START|END|VISITED|SECONDARY"},
				{Name: "since", Type: "string", Description: "ISO 8601 lower-bound on updated_at"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				p := &gen.ListLocationsParams{}
				if v := args.FlagInt("limit"); v != 0 {
					p.Limit = &v
				}
				if v := args.FlagString("cursor"); v != "" {
					p.Cursor = &v
				}
				if v := args.FlagString("type"); v != "" {
					t := gen.ListLocationsParamsType(v)
					p.Type = &t
				}
				if v := args.FlagString("since"); v != "" {
					t, err := time.Parse(time.RFC3339, v)
					if err != nil {
						return nil, fmt.Errorf("--since: invalid RFC3339 timestamp: %w", err)
					}
					p.Since = &t
				}
				resp, err := r.Client.ListLocationsWithResponse(ctx, p)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Location", "")
			},
		},
		{
			Use: "locations get <id>", Short: "Show one location",
			Kind: KindRead, Verb: "GET", Path: "/locations/{id}", Ability: invpkg.Read,
			PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.ShowLocationWithResponse(ctx, id)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Location", id)
			},
		},
		{
			Use: "locations create", Short: "Create a location", Kind: KindMutation,
			Verb: "POST", Path: "/locations", Ability: invpkg.Write, DryRunMode: DryRunBody,
			Long: "Every location must be attached to an owning record. Specify --attach-to " +
				"(product|organisation|partner) and --attach-to-id (the owner row's id). The CLI " +
				"never exposes Eloquent FQCNs. --address is used as street_address when " +
				"--street-address isn't supplied.",
			Flags: []FlagDef{
				{Name: "type", Type: "string", Required: true, Description: "PRIMARY|START|END|VISITED|SECONDARY"},
				{Name: "name", Type: "string", Required: true, Description: "Location name"},
				{Name: "address", Type: "string", Required: true, Description: "Postal address (used as street_address fallback)"},
				{Name: "attach-to", Type: "string", Required: true, Description: "product|organisation|partner"},
				{Name: "attach-to-id", Type: "string", Required: true, Description: "Id of the owning record"},
				{Name: "street-address", Type: "string", Description: "Preferred — persisted directly"},
				{Name: "city", Type: "string", Description: "City"},
				{Name: "country-code", Type: "string", Description: "ISO 3166-1 alpha-2"},
				{Name: "postal-code", Type: "string", Description: "Postal code"},
				{Name: "region", Type: "string", Description: "Region / state / department"},
				{Name: "latitude", Type: "float", Description: "Latitude (decimal)"},
				{Name: "longitude", Type: "float", Description: "Longitude (decimal)"},
				{Name: "google-place-id", Type: "string", Description: "Google Maps place ID"},
			},
			ForensicFields: []string{"type", "name", "attach-to", "attach-to-id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"type":            "type",
					"name":            "name",
					"address":         "address",
					"attach-to":       "attach_to",
					"attach-to-id":    "attach_to_id",
					"street-address":  "street_address",
					"city":            "city",
					"country-code":    "country_code",
					"postal-code":     "postal_code",
					"region":          "region",
					"latitude":        "latitude",
					"longitude":       "longitude",
					"google-place-id": "google_place_id",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.CreateLocationWithBodyWithResponse(ctx, &gen.CreateLocationParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Location", "")
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
		{
			Use: "locations update <id>", Short: "Update a location", Kind: KindMutation,
			Verb: "PATCH", Path: "/locations/{id}", Ability: invpkg.Write, DryRunMode: DryRunBody,
			PositionalArgs: []string{"id"},
			Flags: []FlagDef{
				{Name: "type", Type: "string", Description: "PRIMARY|START|END|VISITED|SECONDARY"},
				{Name: "name", Type: "string", Description: "Location name"},
				{Name: "address", Type: "string", Description: "Postal address (persisted as street_address)"},
				{Name: "latitude", Type: "float", Description: "Latitude (decimal)"},
				{Name: "longitude", Type: "float", Description: "Longitude (decimal)"},
				{Name: "google-place-id", Type: "string", Description: "Google Maps place ID"},
			},
			ForensicFields: []string{"type", "name"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"type":            "type",
					"name":            "name",
					"address":         "address",
					"latitude":        "latitude",
					"longitude":       "longitude",
					"google-place-id": "google_place_id",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.UpdateLocationWithBodyWithResponse(ctx, id, &gen.UpdateLocationParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Location", id)
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
		{
			Use: "locations delete <id>", Short: "Hard-delete a location",
			Kind: KindMutation, Verb: "DELETE", Path: "/locations/{id}",
			Ability: invpkg.Write,
			// Spec: DELETE has no body and Params carries only IdempotencyKey
			// (no DryRun). 409 RESOURCE_IN_USE if the location is still
			// referenced by published products — detach first.
			DryRunMode:     DryRunNotSupported,
			PositionalArgs: []string{"id"},
			Long: "Hard-deletes the location. Returns 409 if the location is still " +
				"referenced by published products — detach those references first.",
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.DeleteLocationWithResponse(ctx, id, &gen.DeleteLocationParams{IdempotencyKey: args.IdempotencyKeyUUID})
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Location", id)
			},
		},
	}
}
