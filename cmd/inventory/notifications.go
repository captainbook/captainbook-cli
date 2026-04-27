package inventory

import (
	"context"

	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/captainbook/captainbook-cli/internal/inventory/gen"
)

// notificationsDefs declares the notifications surface.
//
// In the spec, the only notification endpoint is the booking-confirmation
// resend at POST /bookings/{id}/notifications/resend-confirmation. To keep
// the agent-facing UX consistent the brief calls for a top-level
// `notifications resend` shortcut; we provide it as an alias of
// `bookings resend-confirmation <booking-id>` so the audit entry's
// command field correctly identifies the underlying endpoint.
func notificationsDefs() []CommandDef {
	return []CommandDef{
		{
			Use: "notifications resend <booking-id>", Short: "Resend booking confirmation notification",
			Kind: KindMutation, Verb: "POST", Path: "/bookings/{id}/notifications/resend-confirmation",
			Ability: invpkg.Write, DryRunMode: DryRunBody,
			PositionalArgs: []string{"booking-id"},
			Flags: []FlagDef{
				{Name: "channel", Type: "string", Description: "email|sms"},
				{Name: "recipient", Type: "string", Description: "Override email/phone"},
			},
			ForensicFields: []string{"channel", "recipient"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"channel":   "channel",
					"recipient": "recipient",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.ResendBookingConfirmationWithBodyWithResponse(ctx, id, &gen.ResendBookingConfirmationParams{}, "application/json", asReader(body))
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Booking", id)
			},
		},
	}
}
