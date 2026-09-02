package inventory

import (
	"context"
	"fmt"
	"time"

	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/captainbook/captainbook-cli/internal/inventory/gen"
)

// answersDefs declares the answers resource: list, get.
//
// Answers are customers' responses to the operator's custom booking
// questions. `bookings get <id>` already returns one booking's answers
// inline; this resource exists for the manifest-shaped question — "what are
// tomorrow's dietary requirements across every booking on the 09:00
// departure?" — which would otherwise cost one call per booking.
//
// Read-only: answers are written by the customer at checkout, not by
// operators. There is no create/update/delete on this surface.
//
// PERMISSIONS — two distinct gates. The `cli:read` ABILITY on the token
// decides whether the endpoint is reachable at all; the PERMISSIONS of the
// user the token was issued to decide what comes back. This endpoint needs
// `view_answers_of_booking` in addition to `view_any_booking`, gated
// separately from the rest of the read tier because this is guest PII
// (passport numbers, dates of birth, nationalities, dietary and medical
// notes), not inventory data. A caller
// restricted to `view_own_booking` sees only answers on the trips they are
// assigned to, matching `bookings list`.
func answersDefs() []CommandDef {
	return []CommandDef{
		{
			Use: "answers list", Short: "List answers to custom booking questions",
			Kind: KindRead, Verb: "GET", Path: "/answers", Ability: invpkg.Read,
			Long: "Cross-booking read of what customers answered.\n\n" +
				"Requires the view_answers_of_booking permission on top of view_any_booking, " +
				"and a cli:read token to reach the endpoint at all — two separate gates. " +
				"This is guest PII " +
				"(passport numbers, dates of birth, dietary and medical notes), so it is " +
				"gated separately from the rest of the read tier. Without the permission " +
				"the request is a 403, and `bookings get` returns answers as null rather " +
				"than [].\n\n" +
				"--from / --to bound the trip's DEPARTURE, not when the answer was " +
				"written, and are read in the tenant's timezone (or the product's when " +
				"--product-option-id narrows the query to one). --since is the genuine " +
				"UTC bound against updated_at. They answer different questions: use " +
				"--from/--to for a manifest, --since to sync what changed.",
			Example: "  # Tomorrow's answers for one departure's product option\n" +
				"  ceebee inventory answers list --product-option-id 42 \\\n" +
				"      --from 2026-09-01T00:00:00Z --to 2026-09-02T00:00:00Z\n\n" +
				"  # Per-guest answers on one booking\n" +
				"  ceebee inventory answers list --booking-id bk_9 --granularity guest\n\n" +
				"  # Include answers to questions the operator has since retired\n" +
				"  ceebee inventory answers list --booking-id bk_9 --include-trashed",
			Flags: []FlagDef{
				{Name: "limit", Type: "int"}, {Name: "cursor", Type: "string"},
				{Name: "booking-id", Type: "string", Description: "Answers on one booking"},
				{Name: "question-id", Type: "int", Description: "Answers to one question, across bookings. Numeric here (the spec types it integer on this endpoint), unlike the prefixed ids other commands take"},
				{Name: "granularity", Type: "string", Description: "booking|guest|extra"},
				{Name: "product-option-id", Type: "int", Description: "Answers on bookings for one product option. Numeric here, unlike the prefixed form other commands accept"},
				{Name: "from", Type: "string", Description: "Trip departure >= ISO 8601 (inclusive). Bounds the DEPARTURE, not when the answer was written"},
				{Name: "to", Type: "string", Description: "Trip departure < ISO 8601 (exclusive). See --from"},
				{Name: "since", Type: "string", Description: "ISO 8601 lower-bound on updated_at (a genuine UTC bound, unlike --from/--to)"},
				// Question soft-deletes and cascade-soft-deletes its
				// answers, so retiring a custom question hides every
				// historical answer to it. Without this a manifest query
				// for a past departure comes back short with nothing to
				// say so.
				{Name: "include-trashed", Type: "bool", Description: "Include answers to retired questions"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				p := &gen.ListAnswersParams{}
				if v := args.FlagInt("limit"); v != 0 {
					p.Limit = &v
				}
				if v := args.FlagString("cursor"); v != "" {
					p.Cursor = &v
				}
				if v := args.FlagString("booking-id"); v != "" {
					p.BookingId = &v
				}
				if args.FlagSet("question-id") {
					v := args.FlagInt("question-id")
					p.QuestionId = &v
				}
				if v := args.FlagString("granularity"); v != "" {
					g := gen.ListAnswersParamsGranularity(v)
					p.Granularity = &g
				}
				if args.FlagSet("product-option-id") {
					v := args.FlagInt("product-option-id")
					p.ProductOptionId = &v
				}
				if v := args.FlagString("from"); v != "" {
					t, err := time.Parse(time.RFC3339, v)
					if err != nil {
						return nil, fmt.Errorf("--from: invalid RFC3339 timestamp: %w", err)
					}
					p.From = &t
				}
				if v := args.FlagString("to"); v != "" {
					t, err := time.Parse(time.RFC3339, v)
					if err != nil {
						return nil, fmt.Errorf("--to: invalid RFC3339 timestamp: %w", err)
					}
					p.To = &t
				}
				if v := args.FlagString("since"); v != "" {
					t, err := time.Parse(time.RFC3339, v)
					if err != nil {
						return nil, fmt.Errorf("--since: invalid RFC3339 timestamp: %w", err)
					}
					p.Since = &t
				}
				if args.FlagSet("include-trashed") {
					b := args.FlagBool("include-trashed")
					p.IncludeTrashed = &b
				}
				resp, err := r.Client.ListAnswersWithResponse(ctx, p)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Answer", "")
			},
		},
		{
			Use: "answers get <id>", Short: "Show one answer", Kind: KindRead,
			Verb: "GET", Path: "/answers/{id}", Ability: invpkg.Read,
			PositionalArgs: []string{"id"},
			Long: "Same permissions and scoping as `answers list`. An answer outside the " +
				"caller's business unit or assigned trips reads as 404, not 403, so the " +
				"id space is not enumerable.",
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.GetAnswerWithResponse(ctx, id)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Answer", id)
			},
		},
	}
}
