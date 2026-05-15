package inventory

import (
	"context"
	"fmt"

	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/captainbook/captainbook-cli/internal/inventory/gen"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

// bookingsDefs declares booking commands: list, get, cancel, refund, comp,
// resend-confirmation.
//
// Refund + comp are CS-only (cli:cs) operations and capture rich
// forensic_summary fields per D37 (refund: amount, reason, transaction_id;
// comp: reason, notify_customer).
//
// Tuned diff renderer: "Booking".
func bookingsDefs() []CommandDef {
	return []CommandDef{
		{
			Use: "bookings list", Short: "List bookings", Kind: KindRead,
			Verb: "GET", Path: "/bookings", Ability: invpkg.Read,
			Flags: []FlagDef{
				{Name: "limit", Type: "int"},
				{Name: "cursor", Type: "string"},
				{Name: "q", Type: "string", Description: "Free-text search"},
				{Name: "booking-status", Type: "string", Description: "ON_HOLD|CONFIRMED|EXPIRED|CANCELLED (uppercase per spec)"},
				{Name: "from", Type: "string", Description: "Booking start date >= (YYYY-MM-DD)"},
				{Name: "to", Type: "string", Description: "Booking start date <= (YYYY-MM-DD)"},
				{Name: "customer-email", Type: "string", Description: "Filter by customer email"},
				{Name: "reference", Type: "string", Description: "Filter by booking reference"},
				{Name: "product-option-id", Type: "string", Description: "Filter by product option"},
				{Name: "include-cancelled", Type: "bool", Description: "Lift the CancellingScope filter so cancelled bookings appear alongside active ones"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				p := &gen.ListBookingsParams{}
				if v := args.FlagInt("limit"); v != 0 {
					p.Limit = &v
				}
				if v := args.FlagString("cursor"); v != "" {
					p.Cursor = &v
				}
				if v := args.FlagString("q"); v != "" {
					p.Q = &v
				}
				if v := args.FlagString("booking-status"); v != "" {
					s := gen.ListBookingsParamsBookingStatus(v)
					p.BookingStatus = &s
				}
				if v := args.FlagString("from"); v != "" {
					d, err := parseDate(v)
					if err != nil {
						return nil, fmt.Errorf("--from: %w", err)
					}
					p.From = &d
				}
				if v := args.FlagString("to"); v != "" {
					d, err := parseDate(v)
					if err != nil {
						return nil, fmt.Errorf("--to: %w", err)
					}
					p.To = &d
				}
				if v := args.FlagString("customer-email"); v != "" {
					e := openapi_types.Email(v)
					p.CustomerEmail = &e
				}
				if v := args.FlagString("reference"); v != "" {
					p.Reference = &v
				}
				if v := args.FlagString("product-option-id"); v != "" {
					p.ProductOptionId = &v
				}
				if args.FlagBool("include-cancelled") {
					t := true
					p.IncludeCancelled = &t
				}
				resp, err := r.Client.ListBookingsWithResponse(ctx, p)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Booking", "")
			},
		},
		{
			Use: "bookings get <id>", Short: "Show one booking", Kind: KindRead,
			Verb: "GET", Path: "/bookings/{id}", Ability: invpkg.Read,
			PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.ShowBookingWithResponse(ctx, id)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Booking", id)
			},
		},
		{
			Use: "bookings transactions <id>", Short: "List transactions for a booking",
			Kind: KindRead, Verb: "GET", Path: "/bookings/{id}/transactions",
			Ability: invpkg.Read, PositionalArgs: []string{"id"},
			Flags: []FlagDef{
				{Name: "limit", Type: "int", Description: "Page size"},
				{Name: "cursor", Type: "string", Description: "Pagination cursor"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				p := &gen.ListBookingTransactionsParams{}
				if v := args.FlagInt("limit"); v != 0 {
					p.Limit = &v
				}
				if v := args.FlagString("cursor"); v != "" {
					p.Cursor = &v
				}
				resp, err := r.Client.ListBookingTransactionsWithResponse(ctx, id, p)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Transaction", id)
			},
		},
		{
			Use: "bookings cancel <id>", Short: "Cancel a booking",
			Kind: KindMutation, Verb: "POST", Path: "/bookings/{id}/cancel",
			Ability: invpkg.Write, DryRunMode: DryRunBody,
			PositionalArgs: []string{"id"},
			Flags: []FlagDef{
				{Name: "reason", Type: "string", Required: true, Description: "Cancellation reason"},
				{Name: "refund-policy", Type: "string", Required: true, Description: "none|full|partial (spec: required; CS-only for overrides)"},
				{Name: "refund-amount", Type: "int", Description: "Refund amount in minor units (only with partial)"},
				// Server's CancelBookingRequest defaults notify_customer to TRUE.
				// Mirror that default at the cobra layer so --help reads "(default
				// true)"; we only emit notify_customer in the body when the user
				// explicitly sets the flag, so omitting still yields the server
				// default. Pass --notify-customer=false to suppress the email.
				{Name: "notify-customer", Type: "bool", Default: true, Description: "Notify customer of cancellation (server default true)"},
			},
			ForensicFields: []string{"reason", "refund-policy", "refund-amount", "notify-customer"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"reason":          "reason",
					"refund-policy":   "refund_policy",
					"refund-amount":   "refund_amount",
					"notify-customer": "notify_customer",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.CancelBookingWithBodyWithResponse(ctx, id, &gen.CancelBookingParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil { return &RunResult{WireBody: body}, err }
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Booking", id)
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
		{
			Use: "bookings refund <id>", Short: "Refund a booking (CS only)",
			Kind: KindMutation, Verb: "POST", Path: "/bookings/{id}/refund",
			Ability: invpkg.CS, DryRunMode: DryRunBody,
			PositionalArgs: []string{"id"},
			Long: "Issue a refund against a booking. Requires the cli:cs ability " +
				"(operator tokens are 403). Forensic fields amount, reason, " +
				"transaction_id are captured in the audit log per D37.",
			Flags: []FlagDef{
				{Name: "amount", Type: "int", Required: true, Description: "Refund amount in minor units"},
				{Name: "reason", Type: "string", Required: true, Description: "Refund reason"},
				{Name: "transaction-id", Type: "string", Description: "Original transaction to refund against"},
				{Name: "notify-customer", Type: "bool", Description: "Notify customer of refund"},
			},
			ForensicFields: []string{"amount", "reason", "transaction-id", "notify-customer"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"amount":          "amount",
					"reason":          "reason",
					"transaction-id":  "transaction_id",
					"notify-customer": "notify_customer",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.RefundBookingWithBodyWithResponse(ctx, id, &gen.RefundBookingParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil { return &RunResult{WireBody: body}, err }
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Booking", id)
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
		{
			Use: "bookings comp <id>", Short: "Comp a booking (CS only)",
			Kind: KindMutation, Verb: "POST", Path: "/bookings/{id}/comp",
			Ability: invpkg.CS, DryRunMode: DryRunBody,
			PositionalArgs: []string{"id"},
			Long: "Mark a booking as comped (complimentary; no charge to the customer). " +
				"Requires the cli:cs ability. Forensic fields reason, notify_customer " +
				"are captured in the audit log per D37.",
			Flags: []FlagDef{
				{Name: "reason", Type: "string", Required: true, Description: "Comp reason"},
				{Name: "notify-customer", Type: "bool", Description: "Notify customer"},
			},
			ForensicFields: []string{"reason", "notify-customer"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"reason":          "reason",
					"notify-customer": "notify_customer",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.CompBookingWithBodyWithResponse(ctx, id, &gen.CompBookingParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil { return &RunResult{WireBody: body}, err }
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Booking", id)
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
		{
			Use: "bookings resend-confirmation <id>", Short: "Resend booking confirmation",
			Kind: KindMutation, Verb: "POST", Path: "/bookings/{id}/notifications/resend-confirmation",
			Ability: invpkg.CS, DryRunMode: DryRunBody,
			PositionalArgs: []string{"id"},
			Flags:          resendBookingConfirmationFlags,
			ForensicFields: []string{"channel", "recipient"},
			Run:            resendBookingConfirmationRun,
		},
	}
}

// parseDate accepts YYYY-MM-DD and returns an openapi_types.Date.
func parseDate(s string) (openapi_types.Date, error) {
	t, err := timeParseDate(s)
	if err != nil {
		return openapi_types.Date{}, err
	}
	return openapi_types.Date{Time: t}, nil
}

// resendBookingConfirmationFlags / resendBookingConfirmationRun back both
// `bookings resend-confirmation <id>` and `notifications resend <booking-id>`.
// The latter is a top-level alias of the former (spec only defines one
// endpoint, POST /bookings/{id}/notifications/resend-confirmation), so the
// closure and flag list are shared here to prevent the two from drifting.
var resendBookingConfirmationFlags = []FlagDef{
	{Name: "channel", Type: "string", Description: "email|sms"},
	{Name: "recipient", Type: "string", Description: "Override email/phone"},
}

func resendBookingConfirmationRun(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
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
	resp, err := r.Client.ResendBookingConfirmationWithBodyWithResponse(ctx, id, &gen.ResendBookingConfirmationParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
	if err != nil {
		return &RunResult{WireBody: body}, err
	}
	res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Booking", id)
	if res != nil {
		res.WireBody = body
	}
	return res, err
}
