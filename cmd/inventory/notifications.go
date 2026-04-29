package inventory

import (
	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
)

// notificationsDefs declares the notifications surface.
//
// In the spec, the only notification endpoint is the booking-confirmation
// resend at POST /bookings/{id}/notifications/resend-confirmation. To keep
// the agent-facing UX consistent the brief calls for a top-level
// `notifications resend` shortcut; we provide it as an alias of
// `bookings resend-confirmation <booking-id>` so the audit entry's
// command field correctly identifies the underlying endpoint. Flags +
// closure are shared with bookings.resend-confirmation via
// resendBookingConfirmationFlags / resendBookingConfirmationRun (defined
// in bookings.go) so the two paths can't drift.
func notificationsDefs() []CommandDef {
	return []CommandDef{
		{
			Use: "notifications resend <booking-id>", Short: "Resend booking confirmation notification",
			Kind: KindMutation, Verb: "POST", Path: "/bookings/{id}/notifications/resend-confirmation",
			Ability: invpkg.CS, DryRunMode: DryRunBody,
			PositionalArgs: []string{"booking-id"},
			Flags:          resendBookingConfirmationFlags,
			ForensicFields: []string{"channel", "recipient"},
			Run:            resendBookingConfirmationRun,
		},
	}
}
