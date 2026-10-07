package inventory

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/captainbook/captainbook-cli/internal/inventory/gen"
)

// segmentsDefs declares customer segments: list, get, members, fields, create.
//
// Segment ids are UUIDs, not the opaque prefixed ids most resources use, so the
// positional argument is parsed before the call rather than passed through — a
// malformed id fails here instead of as a server 404.
//
// `fields` is the vocabulary endpoint: it answers "what can this tenant filter a
// segment on", which is the only way a caller learns the legal shape of
// --conditions. It is per-tenant and per-business-unit, so it cannot be
// hardcoded in docs.
func segmentsDefs() []CommandDef {
	return []CommandDef{
		{
			Use: "segments list", Short: "List customer segments", Kind: KindRead,
			Verb: "GET", Path: "/segments", Ability: invpkg.Read,
			Flags: []FlagDef{
				{Name: "limit", Type: "int", Min: 1, Description: "Page size"},
				{Name: "cursor", Type: "string", Description: "Pagination cursor"},
				{Name: "since", Type: "string", Description: "ISO 8601 lower-bound on updated_at"},
				{Name: "status", Type: "string", Description: "active|archived|backfilling|draft|errored"},
				{Name: "type", Type: "string", Description: "smart|static"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				p := &gen.ListSegmentsParams{}
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
				if v := args.FlagString("status"); v != "" {
					s := gen.ListSegmentsParamsStatus(v)
					p.Status = &s
				}
				if v := args.FlagString("type"); v != "" {
					t := gen.ListSegmentsParamsType(v)
					p.Type = &t
				}
				resp, err := r.Client.ListSegmentsWithResponse(ctx, p)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Segment", "")
			},
		},
		{
			Use: "segments get <id>", Short: "Show a segment", Kind: KindRead,
			Verb: "GET", Path: "/segments/{id}", Ability: invpkg.Read,
			PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				raw, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				id, err := uuid.Parse(raw)
				if err != nil {
					return nil, fmt.Errorf("segment id %q is not a UUID: %w", raw, err)
				}
				resp, err := r.Client.ShowSegmentWithResponse(ctx, id)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Segment", raw)
			},
		},
		{
			Use: "segments members <id>", Short: "List the customers currently in a segment",
			Kind: KindRead, Verb: "GET", Path: "/segments/{id}/members", Ability: invpkg.Read,
			PositionalArgs: []string{"id"},
			Flags: []FlagDef{
				{Name: "limit", Type: "int", Min: 1, Description: "Page size"},
				{Name: "cursor", Type: "string", Description: "Pagination cursor"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				raw, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				id, err := uuid.Parse(raw)
				if err != nil {
					return nil, fmt.Errorf("segment id %q is not a UUID: %w", raw, err)
				}
				p := &gen.ListSegmentMembersParams{}
				if v := args.FlagInt("limit"); v != 0 {
					p.Limit = &v
				}
				if v := args.FlagString("cursor"); v != "" {
					p.Cursor = &v
				}
				resp, err := r.Client.ListSegmentMembersWithResponse(ctx, id, p)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Customer", raw)
			},
		},
		{
			Use: "segments fields", Short: "The fields this tenant can filter a segment on",
			Kind: KindRead, Verb: "GET", Path: "/segment-fields", Ability: invpkg.Read,
			Long: "Lists the filterable fields available to this tenant and business unit. " +
				"Read this before writing --conditions for `segments create`: the vocabulary " +
				"is per-tenant, so there is no fixed list to copy from the docs.",
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				resp, err := r.Client.ListSegmentFieldsWithResponse(ctx)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "SegmentField", "")
			},
		},
		{
			Use: "segments create", Short: "Create a smart segment", Kind: KindMutation,
			Verb: "POST", Path: "/segments", Ability: invpkg.Write, DryRunMode: DryRunBody,
			Long: "Creates a smart segment from a condition tree.\n\n" +
				"--conditions takes the tree as JSON (or @file.json) and requires at least " +
				"one filter: an empty filter list matches NOBODY, and the server refuses it " +
				"at the edge rather than creating a permanently empty segment. Run " +
				"`segments fields` first to see what this tenant can filter on.",
			Flags: []FlagDef{
				{Name: "name", Type: "string", Required: true, Description: "Segment name (≤200 chars)"},
				{Name: "description", Type: "string", Description: "Optional description (≤1000 chars)"},
				{Name: "conditions", Type: "json", Required: true, Description: "Condition tree as JSON, or @file.json: {filters: [...]}. At least one filter is required"},
			},
			// The audit keeps only body_sha256, so `conditions` — which IS the
			// segment's definition — would otherwise be unreconstructable. It is
			// capturable at all only because it is a flag rather than --data.
			ForensicFields: []string{"name", "conditions"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"name":        "name",
					"description": "description",
					"conditions":  "conditions",
				})
				if err != nil {
					return nil, err
				}
				// POST /segments is the ONLY one of the spec's 70 mutations that
				// declares no IdempotencyKey parameter, so codegen emits no
				// *Params for it and there is no field to assign the resolved key
				// to. Left alone, the transport's idempotencyKeyRT would mint a
				// SECOND key and the audit's idempotency_key would name a key the
				// server never saw — the divergence inventory.go warns about,
				// which breaks forensic correlation precisely where it matters
				// most: a segment is live the moment it exists, so a retried
				// create that the caller believed was key-protected can enrol
				// customers twice.
				//
				// The generated method takes reqEditors, so the header can be set
				// through the client's own extension point rather than by editing
				// the vendored spec (which must stay byte-identical upstream).
				// idempotencyKeyRT preserves a pre-set value verbatim, so this
				// wins. The upstream spec gap is logged in TODOS.md.
				setKey := func(_ context.Context, req *http.Request) error {
					if args.IdempotencyKey != "" {
						req.Header.Set("Idempotency-Key", args.IdempotencyKey)
					}
					return nil
				}
				resp, err := r.Client.CreateSegmentWithBodyWithResponse(ctx, "application/json", asReader(body), setKey)
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, perr := ParseGenResponse(resp.Body, resp.HTTPResponse, "Segment", "")
				if res != nil {
					res.WireBody = body
				}
				return res, perr
			},
		},
	}
}
