package inventory

import (
	"context"

	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/captainbook/captainbook-cli/internal/inventory/gen"
)

// guestsDefs declares guest commands: list, get, update.
//
// "Guests" are people on a booking who aren't necessarily the booker (i.e.
// the customer). The only mutation is update — guests are created/deleted
// implicitly by booking lifecycle changes.
func guestsDefs() []CommandDef {
	return []CommandDef{
		{
			Use: "guests list", Short: "List guests", Kind: KindRead,
			Verb: "GET", Path: "/guests", Ability: invpkg.Read,
			Flags: []FlagDef{
				{Name: "limit", Type: "int"}, {Name: "cursor", Type: "string"},
				{Name: "booking-id", Type: "string", Description: "Filter by booking"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				p := &gen.ListGuestsParams{}
				if v := args.FlagInt("limit"); v != 0 {
					p.Limit = &v
				}
				if v := args.FlagString("cursor"); v != "" {
					p.Cursor = &v
				}
				if v := args.FlagString("booking-id"); v != "" {
					p.BookingId = &v
				}
				resp, err := r.Client.ListGuestsWithResponse(ctx, p)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Guest", "")
			},
		},
		{
			Use: "guests get <id>", Short: "Show one guest", Kind: KindRead,
			Verb: "GET", Path: "/guests/{id}", Ability: invpkg.Read,
			PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.ShowGuestWithResponse(ctx, id)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Guest", id)
			},
		},
		{
			Use: "guests update <id>", Short: "Update a guest", Kind: KindMutation,
			Verb: "PATCH", Path: "/guests/{id}", Ability: invpkg.Write,
			DryRunMode: DryRunBody, PositionalArgs: []string{"id"},
			Long: "Update a guest. Use this to add passport / DOB to satisfy compliance " +
				"requirements (e.g. the Greek passenger-list rule). The custom_attributes " +
				"map is too complex for typed flags; pass it via --data when needed.",
			Flags: []FlagDef{
				{Name: "name", Type: "string", Description: "Guest full name"},
				{Name: "email", Type: "string", Description: "Guest email"},
				{Name: "phone", Type: "string", Description: "Guest phone"},
				{Name: "passport", Type: "string", Description: "Passport number (compliance)"},
				{Name: "dob", Type: "string", Description: "Date of birth (YYYY-MM-DD)"},
				{Name: "dietary", Type: "string", Description: "Dietary requirements"},
			},
			// Passport, DOB, email, and phone are PII — capture them in the
			// audit forensic_summary so compliance edits leave a trace.
			ForensicFields: []string{"passport", "dob", "email", "phone"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"name":     "name",
					"email":    "email",
					"phone":    "phone",
					"passport": "passport",
					"dob":      "dob",
					"dietary":  "dietary",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.UpdateGuestWithBodyWithResponse(ctx, id, &gen.UpdateGuestParams{}, "application/json", asReader(body))
				if err != nil {
					return nil, err
				}
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Guest", id)
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
	}
}
