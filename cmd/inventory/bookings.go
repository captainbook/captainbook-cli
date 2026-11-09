package inventory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/captainbook/captainbook-cli/internal/inventory/gen"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

// bookingsDefs declares booking commands: list, get, create, update,
// reschedule, transactions, available-resources,
// available-auxiliary-resources, available-equipment-resources,
// set-resources, cancel, refund, comp, resend-confirmation.
//
// Create, update and reschedule arrived with spec 1.47.0 and close the gap
// where an agent could read every booking on the account, reassign its guide,
// cancel it, comp it and refund it — but could not take one, so "book Mrs
// Papadopoulou onto Friday's 10:00" ended in a hand-off to a screen. They
// split three ways on purpose, matching the server: create takes the booking
// and charges nothing, update amends only notes and custom attributes, and
// reschedule moves a booking IN PLACE and keeps its id.
//
// The three resource verbs are the read → decide → guarded-write loop for
// booking resource assignment: list candidates, then POST the full desired
// state along with the resource_state_token you read, so a concurrent
// back-office edit fails the write instead of being silently overwritten.
//
// Cancel, refund, comp, reschedule and resend-confirmation are CS-only
// (cli:cs) operations. Reschedule is CS for the reason the spec's ability
// table gives: it moves a live booking a customer is already travelling on,
// which is a judgement about a person's trip rather than a catalogue edit. Cancel is CS because refund_policy is required and its whole
// V1 enum (none|full|partial) is policy overrides, which the spec puts behind
// cli:cs — see the binding for the full reasoning. Refund and comp capture
// rich forensic_summary fields per D37 (refund: amount, reason,
// transaction_id; comp: reason, notify_customer).
//
// Tuned diff renderer: "Booking".
func bookingsDefs() []CommandDef {
	return []CommandDef{
		{
			Use: "bookings list", Short: "List bookings", Kind: KindRead,
			Verb: "GET", Path: "/bookings", Ability: invpkg.Read,
			Flags: []FlagDef{
				{Name: "limit", Type: "int", Min: 1, Max: 200},
				{Name: "cursor", Type: "string"},
				{Name: "q", Type: "string", Description: "Free-text search"},
				{Name: "booking-status", Type: "string", Description: "ON_HOLD|CONFIRMED|EXPIRED|CANCELLED (uppercase per spec)"},
				{Name: "from", Type: "string", Description: "Booking start date >= (YYYY-MM-DD)"},
				{Name: "to", Type: "string", Description: "Booking start date <= (YYYY-MM-DD)"},
				{Name: "customer-email", Type: "string", Description: "Filter by customer email"},
				{Name: "reference", Type: "string", Description: "Filter by booking reference"},
				{Name: "product-option-id", Type: "int", Min: 1, Description: "Filter by product option"},
				{Name: "resource-id", Type: "int", Min: 1, Description: "Filter to bookings this resource is assigned to (active resources only)"},
				// `resources` is the only expansion the spec defines, so this is a
				// single value and not a list. The help used to say
				// "Comma-separated expansions", which promised a form the closure
				// cannot produce and nothing validated — `--include resources,bogus`
				// went to the wire as one opaque string.
				// Written as a pipe run so the generic enum gate actually fires.
				// extractEnumTokens requires a "|" and at least two tokens, so a
				// one-member list reads as prose and is NOT validated — which is
				// how `--include bogus` reached the wire while the help text
				// claimed otherwise. The empty second token is dropped by
				// extractEnumTokens, leaving exactly {resources}.
				{Name: "include", Type: "string", Description: "resources| — adds assigned resources + resource_state_token"},
				{Name: "include-cancelled", Type: "bool", Description: "Lift the CancellingScope filter so cancelled bookings appear alongside active ones"},
				{Name: "customer-id", Type: "string", Description: "Bookings whose booker is this customer (UUID)"},
				// Carries the same pipe-run as --booking-status so the generic enum
				// gate covers it too. With the prose-only description it had, the
				// gate did not fire: `--status cnofirmed` went to the wire
				// verbatim while `--booking-status cnofirmed` was refused by name.
				// The gate exists because some endpoints ignore an unknown enum
				// value and answer with unfiltered data, which is the worst
				// possible result for a typo.
				{Name: "status", Type: "string", Description: "ON_HOLD|CONFIRMED|EXPIRED|CANCELLED — alias of --booking-status"},
				{Name: "date-field", Type: "string", Description: "starts_at|confirmed_at|created_at"},
				{Name: "origin-type", Type: "string", Description: "ota|direct_online|direct_offline|indirect_offline|reseller|other"},
				{Name: "partner-id", Type: "int", Min: 1, Description: "Bookings involving this partner on either side"},
				{Name: "since", Type: "string", Description: "ISO 8601 lower-bound on updated_at"},
				{Name: "include-trashed", Type: "bool", Description: "Include soft-deleted rows"},
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
					p.CustomerEmail = &v
				}
				if v := args.FlagString("reference"); v != "" {
					p.Reference = &v
				}
				if args.FlagSet("product-option-id") {
					v := args.FlagInt("product-option-id")
					p.ProductOptionId = &v
				}
				if args.FlagSet("resource-id") {
					v := args.FlagInt("resource-id")
					// Spec pins resource_id to minimum 1. Letting a 0 through
					// the usual `!= 0` unset-guard would silently drop the
					// filter and return EVERY booking — the agent would read
					// that as "this resource is on all of them". Same
					// wrong-data-with-no-signal failure the enum gate exists
					// to prevent, so fail loudly here too.
					p.ResourceId = &v
				}
				if v := args.FlagString("include"); v != "" {
					// 1.29.0 spells include as "one value or a list of them", so the
					// generated type is a union. The CLI sends the scalar spelling;
					// the list spelling stays reachable through the same union.
					var inc gen.ListBookingsIncludeParam
					if err := inc.FromListBookingsIncludeParam0(gen.ListBookingsIncludeParam0(v)); err != nil {
						return nil, fmt.Errorf("--include: %w", err)
					}
					p.Include = &inc
				}
				if args.FlagBool("include-cancelled") {
					t := true
					p.IncludeCancelled = &t
				}
				if v := args.FlagString("customer-id"); v != "" {
					id, err := uuid.Parse(v)
					if err != nil {
						return nil, fmt.Errorf("--customer-id: invalid UUID: %w", err)
					}
					p.CustomerId = &id
				}
				if v := args.FlagString("status"); v != "" {
					// --status and --booking-status are the same server-side
					// filter under two names. Sent together they are two
					// contradictory values of one filter, and the spec states no
					// precedence, so which one wins is the server's business and
					// unknowable here. Refuse rather than guess.
					if args.FlagSet("booking-status") {
						return nil, fmt.Errorf("--status and --booking-status are the same filter; pass only one")
					}
					p.Status = &v
				}
				if v := args.FlagString("date-field"); v != "" {
					d := gen.ListBookingsParamsDateField(v)
					p.DateField = &d
				}
				if v := args.FlagString("origin-type"); v != "" {
					o := gen.ListBookingsParamsOriginType(v)
					p.OriginType = &o
				}
				if args.FlagSet("partner-id") {
					v := args.FlagInt("partner-id")
					// Spec pins partner_id to minimum 1; a 0 would otherwise be
					// dropped by the unset-guard and silently widen the page.
					p.PartnerId = &v
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
			Use: "bookings create", Short: "Take a booking or hold a reservation",
			Kind: KindMutation, Verb: "POST", Path: "/bookings",
			Ability: invpkg.Write, DryRunMode: DryRunBody,
			Long: "Takes a booking on a departure, or holds a reservation that expires.\n\n" +
				"IT CHARGES NOTHING. --payment-method records how the customer is PAYING; " +
				"--deposit is money ALREADY RECEIVED — the cash handed over at the desk — in " +
				"the account currency's minor units. `card` is refused on every account: " +
				"this endpoint has no card flow, so take card payments in the dashboard.\n\n" +
				"FARES COME FROM THE PRODUCT'S PRICING POLICY, not the payload. `prices` and " +
				"`price` are refused with a 422 rather than dropped, so a negotiated price " +
				"cannot be reported back as applied; enter one in the dashboard. `quantity` " +
				"is derived from --guests and is refused too.\n\n" +
				"Name the customer exactly once: --customer-id for an existing one, or " +
				"--customer to create one. Both, or neither, is a 422.\n\n" +
				"--booking-type reservation REQUIRES --expiry (in SECONDS) and refuses " +
				"--deposit, --payment-method and --payment-notes; --booking-type booking is " +
				"the mirror image and refuses --expiry.\n\n" +
				"Notifications default ON — pass --notify-customer=false / --notify-admins=false " +
				"to suppress them. They are dispatched after the write commits, so a create " +
				"that rolls back sends nothing.\n\n" +
				"READ guest_mix_warnings ON THE RESPONSE. A product may require one guest type " +
				"to travel with another (a child with an adult); staff may book past that rule, " +
				"as on the dashboard, which warns on screen. This surface warns in that field " +
				"instead, and `[]` means the mix satisfies every rule.\n\n" +
				"--dry-run refuses everything it can already decide — a closed departure, one " +
				"short of seats, outside the token's business unit, or carrying a pricing " +
				"category, extra, answer, --resource-id or --branding-id that does not belong " +
				"to it — so an approval is never parked for a payload the commit would reject. " +
				"Two causes stay commit-only because they live inside the booking engine: the " +
				"departure's cut-off window, and a party size no pricing tier covers. Both " +
				"answer 409 DEPARTURE_NOT_BOOKABLE and RELEASE their idempotency key, as do " +
				"CAPACITY_UNAVAILABLE and NO_RESOURCE_AVAILABLE — retry the identical call " +
				"under the SAME key rather than minting a new one.",
			Flags: []FlagDef{
				{Name: "availability-id", Type: "int", Required: true, Min: 1, Description: "The departure to book (an availability id, from availabilities list)"},
				{Name: "booking-type", Type: "string", Required: true, Description: "booking|reservation — a real booking, or a hold that expires"},
				{Name: "guests", Type: "json", Required: true, Description: `Guest mix keyed by PRICING CATEGORY id, as JSON or @file.json: {"3": 2, "4": 1}. The head count is the sum and is not sent separately`},
				{Name: "is-refundable", Type: "bool", Required: true, Description: "Whether the booking is refundable (required, as on the dashboard form)"},
				{Name: "customer-id", Type: "string", Description: "An existing customer by UUID (customers.id is a UUID, unlike every other id here). Mutually exclusive with --customer"},
				{Name: "customer", Type: "json", Description: `Create a new customer, as JSON or @file.json: {"name": "...", "country": "GR", "email": "...", "phone": "...", "zip_code": "..."}. name and country (ISO 3166-1 alpha-2) are required. Mutually exclusive with --customer-id`},
				{Name: "payment-method", Type: "string", Description: "cash|paypal|bank_transfer — how the customer is paying. Records intent and takes no payment; card is refused on every account. Refused entirely on a reservation"},
				{Name: "payment-notes", Type: "string", Description: "Free-text note on the payment (≤2000 chars). Needs a deposit to attach to; refused on a reservation"},
				{Name: "deposit", Type: "int", Description: "Money ALREADY RECEIVED, in the account currency's MINOR units (2000 = 20.00 EUR; 2000 = 2000 JPY). Not a charge. A non-zero value requires --currency. Refused above the booking's computed total (DEPOSIT_EXCEEDS_TOTAL), and refused entirely on a reservation"},
				{Name: "currency", Type: "string", Description: "Required alongside a NON-ZERO --deposit and must equal the account currency reported by auth whoami — it is validated, never converted, so the amount is recorded exactly as sent"},
				{Name: "private-notes", Type: "string", Description: "Internal notes, never shown to the customer (≤5000 chars)"},
				{Name: "comments", Type: "string", Description: "Public notes on the booking (≤5000 chars)"},
				{Name: "expiry", Type: "int", Description: "SECONDS until a reservation lapses — 1800 is half an hour, and the configured default is 600. REQUIRED with --booking-type reservation and refused with --booking-type booking"},
				{Name: "locales", Type: "stringSlice", Description: "Comma-separated content locales for the booking (e.g. en,fr)"},
				{Name: "answers", Type: "json", Description: `Answers to the product's custom booking questions, keyed by QUESTION id, as JSON or @file.json. The value's shape is the question's own, so read them first with questions list --product-id`},
				{Name: "extras", Type: "json", Description: `Add-ons to purchase, as JSON or @file.json: [{"extraId": 7, "quantity": 2}, ...]. The product's MANDATORY extras are attached by the engine whether or not they appear here`},
				{Name: "resource-id", Type: "int", Min: 1, Description: "Assign the guide or asset up front. Omit for automatic assignment; reassign later with bookings set-resources"},
				{Name: "branding-id", Type: "int", Min: 1, Description: "Branding to apply to the booking's documents"},
				{Name: "partner-id", Type: "int", Min: 1, Description: "Attribute the booking to a partner; their commission comes from their own configuration. Omit for a Direct booking, which records the authenticated user as the provider"},
				{Name: "notify-admins", Type: "bool", Default: true, Description: "Notify admins (server default true)"},
				{Name: "notify-customer", Type: "bool", Default: true, Description: "Send the customer their confirmation (server default true)"},
			},
			// invariant: this is the one booking operation that brings a
			// booking into existence, and the audit keeps only body_sha256 —
			// so everything that decides WHAT was sold, to WHOM, and what
			// money was recorded against it has to be reconstructable from
			// the row itself. --guests is the party, --deposit/--currency the
			// money already taken, --customer-id/--customer the person.
			ForensicFields: []string{
				"availability-id", "booking-type", "guests", "is-refundable",
				"customer-id", "customer", "payment-method", "deposit", "currency",
				"expiry", "extras", "resource-id", "partner-id", "branding-id",
				"notify-customer", "notify-admins",
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"availability-id": "availability_id",
					"booking-type":    "booking_type",
					"guests":          "guests",
					"is-refundable":   "is_refundable",
					"customer-id":     "customer_id",
					"customer":        "customer",
					"payment-method":  "payment_method",
					"payment-notes":   "payment_notes",
					"deposit":         "deposit",
					"currency":        "currency",
					"private-notes":   "private_notes",
					"comments":        "comments",
					"expiry":          "expiry",
					"locales":         "locales",
					"answers":         "answers",
					"extras":          "extras",
					"resource-id":     "resource_id",
					"branding-id":     "branding_id",
					"partner-id":      "partner_id",
					"notify-admins":   "notify_admins",
					"notify-customer": "notify_customer",
				})
				if err != nil {
					return nil, err
				}
				// Validated on the ASSEMBLED body rather than per flag, so a
				// --data payload is held to the same rules as the typed flags.
				// Every gate mirrors a documented 422 — see the function.
				if err := validateStoreBookingBody(body); err != nil {
					return nil, err
				}
				resp, err := r.Client.CreateBookingWithBodyWithResponse(ctx, &gen.CreateBookingParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, perr := ParseGenResponse(resp.Body, resp.HTTPResponse, "Booking", "")
				if res != nil {
					res.WireBody = body
				}
				return res, perr
			},
		},
		{
			Use: "bookings update <id>", Short: "Amend a booking's notes and custom attributes",
			Kind: KindMutation, Verb: "PATCH", Path: "/bookings/{id}",
			Ability: invpkg.Write, DryRunMode: DryRunBody,
			PositionalArgs: []string{"id"},
			Long: "Amends a booking's notes and its operator metadata. NOTHING here changes " +
				"what the customer gets or what they owe, so there is no capacity check and " +
				"no money recompute — and NO NOTIFICATION IS SENT, matching the dashboard's " +
				"edit form. To re-send the confirmation use `bookings resend-confirmation`.\n\n" +
				"AT LEAST ONE of --comments, --private-notes or --custom-attributes must be " +
				"present. A request carrying none of them is a 422 rather than a 200 with an " +
				"empty diff, because a 200 reads as \"applied\". The check is on PRESENCE: " +
				"resending a value the booking already has is a 200 with matching diff halves, " +
				"and --dry-run alone is not a change.\n\n" +
				"--guests and --extras are DEFERRED, not rejected on principle: both have to " +
				"recompute the booking's stored total and that calculation is not yet shared " +
				"out of the dashboard. Change them there. `prices`, `price`, `quantity` and " +
				"`expiry` are refused outright.\n\n" +
				"Clearing a note needs a JSON null, which a string flag cannot express: pass " +
				"--data '{\"comments\": null}'. An empty --comments=\"\" stores an empty " +
				"string, which is a different value.\n\n" +
				"Requires BOTH the update_booking and update_booking_after_date permissions. " +
				"The second name is misleading — the dashboard's edit form checks it on every " +
				"booking, not only a departed one, so this endpoint does too rather than " +
				"being wider than the screen.",
			Flags: []FlagDef{
				{Name: "comments", Type: "string", Description: "Public notes (≤5000 chars). Clearing needs --data '{\"comments\": null}'"},
				{Name: "private-notes", Type: "string", Description: "Internal notes, never shown to the customer (≤5000 chars). Clearing needs --data '{\"private_notes\": null}'"},
				{Name: "custom-attributes", Type: "json", Description: `Custom attributes keyed by the definition's SLUG, as JSON or @file.json: {"slug": value}. A PATCH — an omitted slug is left alone. Definitions are per BUSINESS UNIT, so a slug from another unit is unknown here; '{}' is a 422. A pool-backed definition is refused: those change only through the atomic claim/release action`},
			},
			ForensicFields: []string{"comments", "private-notes", "custom-attributes"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"comments":          "comments",
					"private-notes":     "private_notes",
					"custom-attributes": "custom_attributes",
				})
				if err != nil {
					return nil, err
				}
				// The server's "at least one of three" rule, checked on the
				// assembled body so --data counts. Caught locally because the
				// refusal is otherwise indistinguishable to a reader from a
				// successful no-op, and because the obvious empty call
				// (`bookings update <id> --dry-run`) is exactly the one an
				// agent makes first.
				if err := requireAnyBookingAmendment(body); err != nil {
					return nil, err
				}
				resp, err := r.Client.UpdateBookingWithBodyWithResponse(ctx, id, &gen.UpdateBookingParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, perr := ParseGenResponse(resp.Body, resp.HTTPResponse, "Booking", id)
				if res != nil {
					res.WireBody = body
				}
				return res, perr
			},
		},
		{
			Use: "bookings reschedule <id>", Short: "Move a booking to another departure (CS only)",
			Kind: KindMutation, Verb: "POST", Path: "/bookings/{id}/reschedule",
			Ability: invpkg.CS, DryRunMode: DryRunBody,
			PositionalArgs: []string{"id"},
			// cli:cs, like cancel, comp, refund, resend-confirmation and
			// gift-cert void — the spec's ability table names reschedule
			// explicitly and gives the reason: it moves a live booking a
			// customer is already travelling on, which is a judgement about a
			// person's trip rather than a catalogue edit.
			Long: "Moves a booking to another departure OF THE SAME PRODUCT OPTION, in place. " +
				"The booking KEEPS ITS ID: extras, answers, transactions, guests and any pool " +
				"access code stay on the same row, because nothing is replaced. Requires the " +
				"cli:cs ability.\n\n" +
				"THE PRICE DOES NOT CHANGE. The booking keeps the fare it was taken at even " +
				"when the destination is priced differently — moving a midweek booking onto a " +
				"peak Saturday does not reprice it. Say so when the destination is priced " +
				"differently; `prices` is refused.\n\n" +
				"AN OPTION CHANGE IS NOT AVAILABLE HERE. On the dashboard that operation " +
				"cancels the booking and creates a replacement with a new id, so a destination " +
				"belonging to another product option is refused with " +
				"PRODUCT_OPTION_CHANGE_NOT_SUPPORTED, as is naming one with `product_option_id`.\n\n" +
				"The note lands on the reschedule LEDGER, not on the booking's own comments: " +
				"--reschedule-notes is the note, --comments is the fallback used when " +
				"--reschedule-notes is OMITTED, and --reschedule-notes=\"\" records a " +
				"deliberately blank one instead of falling back. Whichever is recorded reads " +
				"back in diff.after.reschedules[], which is where to confirm it landed — the " +
				"booking's own comments field is written only by `bookings update`.",
			Flags: []FlagDef{
				{Name: "availability-id", Type: "int", Required: true, Min: 1, Description: "The DESTINATION departure. It also determines the product option, which is why none is accepted separately — a destination on a different option is a 422"},
				{Name: "resource-id", Type: "int", Min: 1, Description: "Reassign the guide or asset as part of the move. Omit to let the engine reassign automatically, which preserves the original assignment source when the resource is unchanged"},
				{Name: "comments", Type: "string", Description: "A note on the move (≤5000 chars), recorded on the reschedule LEDGER as the fallback when --reschedule-notes is omitted. It does NOT amend the booking's own comments"},
				{Name: "reschedule-notes", Type: "string", Description: "Why the booking moved (≤5000 chars), recorded on the reschedule ledger. Pass an empty value to record a deliberately blank note rather than falling back to --comments"},
				{Name: "notify-admins", Type: "bool", Default: true, Description: "Notify admins (server default true)"},
				{Name: "notify-customer", Type: "bool", Default: true, Description: "Notify the customer their trip moved (server default true)"},
			},
			// invariant: the destination departure IS the operation, and
			// --notify-customer decides whether the person on the trip is told
			// it moved. The ledger row is minted server-side, so the audit is
			// the only local record of which move was asked for.
			ForensicFields: []string{"availability-id", "resource-id", "reschedule-notes", "comments", "notify-customer", "notify-admins"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"availability-id":  "availability_id",
					"resource-id":      "resource_id",
					"comments":         "comments",
					"reschedule-notes": "reschedule_notes",
					"notify-admins":    "notify_admins",
					"notify-customer":  "notify_customer",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.RescheduleBookingWithBodyWithResponse(ctx, id, &gen.RescheduleBookingParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, perr := ParseGenResponse(resp.Body, resp.HTTPResponse, "Booking", id)
				if res != nil {
					res.WireBody = body
				}
				return res, perr
			},
		},
		{
			Use: "bookings transactions <id>", Short: "List transactions for a booking",
			Kind: KindRead, Verb: "GET", Path: "/bookings/{id}/transactions",
			Ability: invpkg.Read, PositionalArgs: []string{"id"},
			Flags: []FlagDef{
				{Name: "limit", Type: "int", Min: 1, Max: 200, Description: "Page size"},
				{Name: "cursor", Type: "string", Description: "Pagination cursor"},
				{Name: "since", Type: "string", Description: "ISO 8601 lower-bound on updated_at"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				p := &gen.ListBookingTransactionsParams{}
				if v := args.FlagString("since"); v != "" {
					t, err := time.Parse(time.RFC3339, v)
					if err != nil {
						return nil, fmt.Errorf("--since: invalid RFC3339 timestamp: %w", err)
					}
					p.Since = &t
				}
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
			Use: "bookings available-resources <id>", Short: "List main resources assignable to a booking",
			Kind: KindRead, Verb: "GET", Path: "/bookings/{id}/resources/available",
			Ability: invpkg.Read, PositionalArgs: []string{"id"},
			Long: "Candidate MAIN (non-auxiliary) resources for this booking, filtered by the " +
				"same availability and concurrency checks the back-office resource switcher " +
				"uses. Feed an id from here to `bookings set-resources --main-resource-id`; " +
				"anything not in this list is what BOOKING_RESOURCE_CONFLICT rejects.",
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.ListAvailableBookingResourcesWithResponse(ctx, id)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Resource", id)
			},
		},
		{
			Use: "bookings available-auxiliary-resources <id>", Short: "List auxiliary resources assignable to a booking",
			Kind: KindRead, Verb: "GET", Path: "/bookings/{id}/resources/auxiliary/available",
			Ability: invpkg.Read, PositionalArgs: []string{"id"},
			Long: "Candidate AUXILIARY resources for this booking. Feed ids from here to " +
				"`bookings set-resources --auxiliary-resource-ids`, which takes the full " +
				"desired set rather than a delta.",
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.ListAvailableBookingAuxiliaryResourcesWithResponse(ctx, id)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Resource", id)
			},
		},
		{
			Use: "bookings available-equipment-resources <id>", Short: "List equipment resources assignable to a booking",
			Kind: KindRead, Verb: "GET", Path: "/bookings/{id}/resources/equipment/available",
			Ability: invpkg.Read, PositionalArgs: []string{"id"},
			Long: "Candidate EQUIPMENT resources for this booking: every non-auxiliary " +
				"resource on its product option that carries no pivot capacity. Feed ids " +
				"from here to `bookings set-resources --equipment-resource-ids`.\n\n" +
				"Unlike the main list this runs no availability or concurrency filter, and " +
				"that is deliberate — a null pivot capacity means the resource is not " +
				"rationed per booking, so every candidate here is always assignable.",
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.ListAvailableBookingEquipmentResourcesWithResponse(ctx, id)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Resource", id)
			},
		},
		{
			Use: "bookings set-resources <id>", Short: "Set the resources assigned to a booking",
			Kind: KindMutation, Verb: "POST", Path: "/bookings/{id}/resources",
			Ability: invpkg.Write, DryRunMode: DryRunBody,
			PositionalArgs: []string{"id"},
			Long: "Desired-state write over a booking's resource assignment. A booking's " +
				"resources come in THREE kinds, split by the product option's pivot " +
				"capacity rather than by the resource's category: exactly one MAIN " +
				"resource (the guide or the asset — the capacity-bearing one), many " +
				"EQUIPMENT resources (non-auxiliary, no pivot capacity), and many " +
				"AUXILIARY ones.\n\n" +
				"This is NOT a delta. --main-resource-id ASSIGNS the single main " +
				"resource: it replaces the current one if there is one and makes the " +
				"first assignment if there is not. --equipment-resource-ids and " +
				"--auxiliary-resource-ids each REPLACE their whole set (pass one empty, " +
				"`--auxiliary-resource-ids=`, to clear it). Omit a flag entirely to " +
				"leave that kind untouched.\n\n" +
				"A booking on a product configured for several units can hold MORE THAN " +
				"ONE main resource — five riders over three jet skis — and each assigned " +
				"row then carries its own seats. --main-resource-id replaces ONE unit " +
				"there, so it needs --replaced-resource-id beside it to say which; " +
				"without it the write is refused as MAIN_RESOURCE_REPLACEMENT_AMBIGUOUS. " +
				"The replacement carries that unit's riders and nothing else on the " +
				"booking moves — the other units, the equipment and their sources stay " +
				"as they were, with no wholesale re-attach on that path.\n\n" +
				"One trap worth knowing when you omit --equipment-resource-ids: moving " +
				"the main resource detaches and re-attaches the option's WHOLE equipment " +
				"set, so a booking previously narrowed to a subset comes back with all of " +
				"it. Pass the set you want alongside --main-resource-id to keep a narrowed " +
				"one. The dry run reports this faithfully — read it.\n\n" +
				"The write is guarded against concurrent edits: read the booking first " +
				"(`bookings get <id>`, or `bookings list --include resources`), pass its " +
				"resource_state_token as --expected-resource-state-token, and the server " +
				"rejects with BOOKING_RESOURCE_STATE_STALE if anything moved in between. " +
				"On a stale rejection, re-read and re-decide — never blind-retry the same " +
				"body. An invalid selection comes back as BOOKING_RESOURCE_CONFLICT " +
				"instead, listing every refused id with a code naming the flag you put it " +
				"in and what it turned out to be; list the legal choices with " +
				"`bookings available-resources` / `bookings available-equipment-resources` " +
				"/ `bookings available-auxiliary-resources`.\n\n" +
				"Booking and resource state commit atomically; notifications, workflows, " +
				"and calendar jobs fire after commit.",
			Flags: []FlagDef{
				{Name: "main-resource-id", Type: "int", Description: "The booking's main resource. Replaces the current one, or makes the first assignment if it has none. On a booking spread over several units it replaces the one named by --replaced-resource-id."},
				// Min 1 like every other PositiveIntegerId flag here, so a 0 is
				// refused by name rather than dropped by the unset-guard and
				// turned into a MAIN_RESOURCE_REPLACEMENT_AMBIGUOUS a round
				// trip later — a refusal naming a DIFFERENT field than the one
				// the operator got wrong.
				{Name: "replaced-resource-id", Type: "int", Min: 1, Description: "The unit --main-resource-id takes over from. Required when the booking holds several main resources (five riders over three jet skis); optional and a no-op on a single-unit booking. Needs --main-resource-id beside it."},
				// stringSlice, not intSlice, on purpose: pflag's intSlice parser
				// runs strconv.Atoi on the raw value, so the documented
				// clear-all form `--auxiliary-resource-ids=` dies at flag-parse
				// time with "invalid syntax" before the command ever runs.
				// stringSlice accepts the empty value as [] and we convert
				// below, which is also where the spec's per-item minimum of 1
				// gets enforced.
				{Name: "equipment-resource-ids", Type: "stringSlice", Description: "Full desired equipment set (replaces, not appends). Pass empty to clear all."},
				{Name: "auxiliary-resource-ids", Type: "stringSlice", Description: "Full desired auxiliary set (replaces, not appends). Pass empty to clear all."},
				{Name: "expected-resource-state-token", Type: "string", Required: true, Description: "resource_state_token from your last booking read"},
			},
			ForensicFields: []string{"main-resource-id", "replaced-resource-id", "equipment-resource-ids", "auxiliary-resource-ids", "expected-resource-state-token"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}

				// Both set flags arrive as []string (see the FlagDef comment);
				// convert to []int so the body carries JSON integers, not
				// quoted strings the server would reject. An explicitly-empty
				// slice must survive as [] — that's the clear-all signal — so
				// this rewrites the value in place rather than skipping when
				// there's nothing to convert.
				//
				// The copy is made once and only when a set flag was given, so
				// we don't mutate the caller's flags — forensic_summary reads
				// the same map afterwards and should record what the user typed.
				var flags map[string]any
				for _, name := range []string{"equipment-resource-ids", "auxiliary-resource-ids"} {
					if !args.FlagSet(name) {
						continue
					}
					raw := args.FlagSlice(name)
					ids := make([]int, 0, len(raw))
					for _, s := range raw {
						n, convErr := strconv.Atoi(strings.TrimSpace(s))
						if convErr != nil {
							return nil, fmt.Errorf("--%s: %q is not an integer", name, s)
						}
						if n < 1 {
							return nil, fmt.Errorf("--%s: ids must be >= 1 (got %d)", name, n)
						}
						ids = append(ids, n)
					}
					if flags == nil {
						flags = make(map[string]any, len(args.Flags))
						for k, v := range args.Flags {
							flags[k] = v
						}
						args.Flags = flags
					}
					flags[name] = ids
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"main-resource-id":              "main_resource_id",
					"replaced-resource-id":          "replaced_resource_id",
					"equipment-resource-ids":        "equipment_resource_ids",
					"auxiliary-resource-ids":        "auxiliary_resource_ids",
					"expected-resource-state-token": "expected_resource_state_token",
				})
				if err != nil {
					return nil, err
				}
				// REPLACED_RESOURCE_WITHOUT_MAIN, caught here rather than on the
				// wire. The pair is the whole meaning of the field — it names
				// which unit main_resource_id takes over from — so alone it
				// cannot describe any write at all, and the server refuses it
				// with a 422 whose rejections[] the operator then has to read
				// to learn they omitted the other half.
				//
				// Read off the ASSEMBLED body, not args.FlagSet: every other
				// gate in this file does, for the reason their shared header
				// states — a gate keyed on the flags has --data as a documented
				// bypass.
				if err := validateBookingResourcesBody(body); err != nil {
					return nil, err
				}
				resp, err := r.Client.UpdateBookingResourcesWithBodyWithResponse(ctx, id, &gen.UpdateBookingResourcesParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Booking", id)
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
		{
			Use: "bookings cancel <id>", Short: "Cancel a booking (CS only)",
			Kind: KindMutation, Verb: "POST", Path: "/bookings/{id}/cancel",
			Ability: invpkg.CS, DryRunMode: DryRunBody,
			PositionalArgs: []string{"id"},
			// cli:cs, not cli:write, and NOT conditional on --refund-policy.
			// The server settles it: POST /bookings/{id}/cancel sits in the
			// `abilities:cli:cs` route group alongside comp, refund and
			// resend-confirmation, and Phase1CCancelTest asserts "returns 403
			// with cli:write only (cancel needs cli:cs)" using a body with
			// refund_policy=none — the value that moves no money. So cancel
			// is CS for EVERY refund_policy, and a cli:write gate here would
			// only let an operator token onto the wire to be refused there.
			//
			// The spec agrees where it is specific:
			// CancelBookingRequest.refund_policy says of `none`, `full`,
			// `partial` that they "override the product's cancellation policy
			// (CS only — operator tokens are 403 on overrides)". refund_policy
			// is required and those three are the whole V1 enum, so no
			// operator-legal value exists to gate conditionally on. Its
			// securitySchemes ability table disagrees — it omits cancel from
			// the cli:cs row, which is what misled this binding in the first
			// place. Do not trust that table over the routes: it is wrong
			// upstream, tracked as captainbook/captainbook#8113.
			//
			// A static gate is also the only kind this path can enforce:
			// Refuse() runs in runMutation before the body is built, so it
			// cannot see refund_policy.
			//
			// The relaxed branch the docs used to promise ("cli:write for
			// refund_policy=auto") was real against an older spec. `auto` is
			// out of the V1 enum pending the structured policy engine — it
			// 422s POLICY_AUTO_NOT_READY — and the operator-reachable cancel
			// went out with it. If `auto` is restored, revisit: that value is
			// the non-override case, and honouring it needs the ability check
			// moved after body parsing.
			Long: "Cancel a booking, applying refund_policy. Requires the cli:cs " +
				"ability for EVERY --refund-policy value: `none`, `full`, and " +
				"`partial` all override the product's cancellation policy, and the " +
				"server 403s operator tokens on overrides. Cancelling with " +
				"--refund-policy full or partial moves real money through Stripe; " +
				"--dry-run returns the policy preview and estimated refund without " +
				"contacting Stripe or the mailer.",
			Flags: []FlagDef{
				{Name: "reason", Type: "string", Required: true, Description: "Cancellation reason"},
				{Name: "refund-policy", Type: "string", Required: true, Description: "none|full|partial (all three are CS-only policy overrides)"},
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
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
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
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
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
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Booking", id)
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
		{
			Use: "bookings resend-confirmation <id>", Short: "Resend booking confirmation (CS only)",
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

// -----------------------------------------------------------------------------
// Request-body gates for the three booking write operations (spec 1.47.0).
//
// Each gate below mirrors a 422 the server documents, and all of them read the
// ASSEMBLED body rather than the flags, so a --data payload is held to exactly
// the same rules as the typed flags. That is the same reason
// overlayValidatedCompanions reads --data: a gate that only runs
// `if args.FlagSet(...)` is a gate with a documented bypass.
//
// The bar for catching a refusal locally is not "the server would also catch
// it". It is that the refusal is EXPENSIVE to read from the far side — because
// the message names a different field than the one the caller got wrong, or
// because the failure is indistinguishable from success in a transcript.
// -----------------------------------------------------------------------------

// decodeBodyObject re-reads an assembled request body as a flat key→raw map.
// Every request body in the spec is an object, and JSONBodyFromArgs has
// already refused a non-object --data, so a failure here is a bug in the
// assembly rather than bad input.
func decodeBodyObject(body []byte) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("inventory: assembled request body is not a JSON object: %w", err)
	}
	return m, nil
}

// jsonIsNull reports whether a raw value is the JSON literal `null`, which is
// NOT the same as the key being absent: `{"comments": null}` clears the field
// and is a present value, while an omitted `comments` leaves it alone.
func jsonIsNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

// decodeNumericInt reads a request-body integer the way this API's own
// validator does, and is the reason these gates are not a plain
// `json.Unmarshal(raw, &int)`.
//
// StoreBookingRequest: "Numeric fields validate as `numeric` BEFORE `integer`.
// Laravel's `integer` rule stringifies first, so a JSON `true` would otherwise
// pass as `"1"` ... A numeric string (`"2"`) is still accepted." Both halves
// matter and they pull opposite ways: a boolean must be refused, a quoted
// number must not be. Decoding straight into an int gets the first half right
// and the second half backwards — and the second half is the one a caller hits,
// because this API hands ids back as STRINGS (`Extra.id` and
// `PricingCategory.id` are both `{type: string}`), so `extras list` →
// `bookings create --extras '[{"extraId":"7",...}]'` is the ordinary
// composition, not an edge case.
//
// Accepts `2`, `"2"`, and any other spelling of a WHOLE number — `2.0` and
// `1e3` included, because their values are integers and the server takes them.
// Refuses `true`, `false`, `2.5`, `"abc"`, `null` and anything out of range.
// See integralNumber for why the value is judged rather than the literal.
func decodeNumericInt(raw json.RawMessage) (int, bool) {
	switch string(bytes.TrimSpace(raw)) {
	case "true", "false":
		// Checked by value, before either decode: `numeric` refuses a boolean,
		// and Laravel's stringify-first `integer` rule would have taken it.
		return 0, false
	}
	var quoted string
	if err := json.Unmarshal(raw, &quoted); err == nil {
		n, convErr := strconv.Atoi(strings.TrimSpace(quoted))
		return n, convErr == nil
	}
	var num json.Number
	if err := json.Unmarshal(raw, &num); err != nil {
		return 0, false
	}
	return integralNumber(num)
}

// integralNumber converts a JSON number to an int when its VALUE is a whole
// number, whatever its spelling.
//
// `2`, `2.0` and `2e0` are the same integer and the server takes all three:
// Laravel's `integer` rule is `filter_var(FILTER_VALIDATE_INT)`, which
// stringifies first, so an integral float passes and `2.5` does not. Reading
// the literal with Atoi refused `2.0` and `1e3` too — the same
// stricter-than-the-wire mistake decodeNumericInt exists to fix, one spelling
// deeper, and a serializer that emitted a count from a float hits it.
//
// Rejects a non-integral value, and anything outside int range rather than
// wrapping.
func integralNumber(num json.Number) (int, bool) {
	if n, err := num.Int64(); err == nil {
		if int64(int(n)) != n {
			return 0, false // out of int range on this platform
		}
		return int(n), true
	}
	f, err := num.Float64()
	if err != nil || f != math.Trunc(f) || math.IsInf(f, 0) || math.IsNaN(f) {
		return 0, false
	}
	// Only the FLOAT-spelled path needs a width guard, and the right boundary
	// is float64's, not int32's. float64 represents every integer exactly up to
	// 2^53; past that the nearest representable value may be a different
	// integer, so converting would silently invent a number the caller never
	// wrote. Refuse instead.
	//
	// The previous bound here was math.MaxInt32, with the same reasoning
	// attached — which was wrong about where float64 loses exactness, and
	// refused legal ids between 2^31 and 2^53 when they happened to be spelled
	// as a float. The integer path above never had that limit: it bounds by
	// what this platform's `int` holds, so a plain `9000000000` is accepted on
	// a 64-bit build. The spec agrees there is no 32-bit ceiling to enforce —
	// `PositiveIntegerId` is `{type: integer, minimum: 1}` with no `maximum`
	// and no `format`.
	const maxExactFloat = 1 << 53
	if f > maxExactFloat || f < -maxExactFloat {
		return 0, false
	}
	n := int64(f)
	if int64(int(n)) != n {
		return 0, false // out of int range on this platform
	}
	return int(n), true
}

// requireAnyBookingAmendment enforces UpdateBookingRequest's "at least one of
// comments, private_notes or custom_attributes must be present" rule.
//
// Worth catching here rather than on the wire because of how the failure READS.
// The server answers 422 for an empty amendment specifically so an agent cannot
// mistake it for an applied change — but the agent that provoked it is the one
// that sent no field in the first place, and the natural next move is to retry
// with --dry-run, which is also not a change. Naming the three fields locally
// ends that loop in one message.
//
// Presence, not value: `{"comments": null}` clears the note and counts, and so
// does resending a value the booking already has.
func requireAnyBookingAmendment(body []byte) error {
	m, err := decodeBodyObject(body)
	if err != nil {
		return err
	}
	for _, k := range []string{"comments", "private_notes", "custom_attributes"} {
		if _, ok := m[k]; ok {
			return nil
		}
	}
	return fmt.Errorf("at least one of --comments, --private-notes or --custom-attributes is " +
		"required: this endpoint amends those three fields and nothing else, so a request " +
		"carrying none of them is refused with a 422 rather than answered 200 with an empty " +
		"diff (--dry-run is not a change on its own)")
}

// validateBookingResourcesBody enforces the two UpdateBookingResourcesRequest
// rules worth naming locally. BOTH read the assembled body, so --data is held
// to the same rules as the typed flags — the pairing gate used to read
// args.FlagSet and the main-resource bound still did, which left the file with
// one gate bypassable by the input the other one closed.
func validateBookingResourcesBody(body []byte) error {
	m, err := decodeBodyObject(body)
	if err != nil {
		return err
	}

	// Spec pins main_resource_id to minimum 1, and 1.6.0 dropped its
	// `nullable: true` — there is no longer any way to leave a booking with no
	// main resource through this endpoint. The old spec allowed null, so a
	// caller reaching for "unassign" will try 0; without this guard that
	// becomes an opaque PositiveIntegerId 422 instead of a local message naming
	// the real constraint.
	if raw, ok := m["main_resource_id"]; ok {
		if n, numeric := decodeNumericInt(raw); numeric && n < 1 {
			return fmt.Errorf(
				"--main-resource-id must be >= 1 (got %d); a booking's main resource cannot be cleared, only replaced", n)
		}
	}

	// REPLACED_RESOURCE_WITHOUT_MAIN: `replaced_resource_id` names the unit
	// `main_resource_id` takes over from, so without the latter it describes no
	// write at all. The server refuses it inside
	// `error.details.rejections[]`, which the operator has to read to learn
	// they omitted the other half.
	if _, hasReplaced := m["replaced_resource_id"]; hasReplaced {
		if _, hasMain := m["main_resource_id"]; !hasMain {
			return fmt.Errorf("--replaced-resource-id names the unit --main-resource-id takes over from, " +
				"so it needs --main-resource-id beside it (the server refuses this as " +
				"REPLACED_RESOURCE_WITHOUT_MAIN)")
		}
	}
	return nil
}

// validateStoreBookingBody enforces the StoreBookingRequest rules whose server
// refusal is hardest to act on from the far side. Every one of them is a
// documented 422.
//
// What it deliberately does NOT re-implement: capacity, the departure's
// cut-off window, whether a pricing category belongs to the product, or any
// party size against a pricing tier. Those live inside the booking engine and a
// second implementation here would drift from it — the spec says as much about
// the two the server's own dry run cannot predict.
func validateStoreBookingBody(body []byte) error {
	m, err := decodeBodyObject(body)
	if err != nil {
		return err
	}

	// Exactly one customer. The spec: "Sending both, or neither, is a 422."
	// Named locally because the two spellings are easy to conflate —
	// customers.id is a UUID while every other id on this request is an
	// integer, so a caller holding an integer reaches for --customer and a
	// caller holding a UUID reaches for both.
	_, hasID := m["customer_id"]
	_, hasObj := m["customer"]
	switch {
	case hasID && hasObj:
		return fmt.Errorf("--customer-id and --customer are mutually exclusive: pass " +
			"--customer-id to book an EXISTING customer, or --customer to create one")
	case !hasID && !hasObj:
		return fmt.Errorf("a booking needs a customer: pass --customer-id (an existing " +
			"customer's UUID, from a booking read or `customers list`) or --customer " +
			"(a JSON object creating one, requiring at least name and country)")
	}

	// guests is the party, and the one field whose wrong SHAPE is silent:
	// `[{"3":2}]` and `{"3":2}` are both well-formed JSON, the json flag type
	// accepts either, and only one of them is a guest mix. Sent as an array
	// the server reads no categories at all.
	if raw, ok := m["guests"]; ok {
		var guests map[string]json.RawMessage
		if err := json.Unmarshal(raw, &guests); err != nil || guests == nil {
			return fmt.Errorf(`--guests must be a JSON OBJECT keyed by pricing category id, `+
				`like {"3": 2, "4": 1} — not an array, and not null (got %s)`, raw)
		}
		if len(guests) == 0 {
			return fmt.Errorf("--guests must name at least one pricing category: an empty " +
				"mix books nobody and is refused with a 422")
		}
		for categoryID, rawCount := range guests {
			count, ok := decodeNumericInt(rawCount)
			if !ok {
				return fmt.Errorf("--guests[%q]: must be a whole number of guests, got %s "+
					"(a quoted number like \"2\" is fine — the server accepts it; a JSON true "+
					"would otherwise reach it as 1)", categoryID, rawCount)
			}
			if count < 0 {
				return fmt.Errorf("--guests[%q]: must be 0 or more, got %d", categoryID, count)
			}
		}
	}

	// extras is the mirror image: an array of {extraId, quantity}, and the
	// spec types the item shape as additionalProperties:false, so a misspelled
	// key (extra_id, the snake_case every other field on this request uses) is
	// a 422 naming a key the caller believes they sent.
	if raw, ok := m["extras"]; ok && !jsonIsNull(raw) {
		var rows []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &rows); err != nil {
			return fmt.Errorf(`--extras must be a JSON ARRAY of {"extraId": n, "quantity": n} `+
				`objects (got %s)`, raw)
		}
		for i, row := range rows {
			if row == nil {
				return fmt.Errorf("--extras[%d]: expected an object, got null", i)
			}
			for k := range row {
				if k != "extraId" && k != "quantity" {
					return fmt.Errorf("--extras[%d]: unknown key %q — the payload accepts only "+
						"extraId and quantity, and extraId is the ONE camelCase key on this "+
						"request", i, k)
				}
			}
			for _, k := range []string{"extraId", "quantity"} {
				rawV, present := row[k]
				if !present {
					return fmt.Errorf("--extras[%d]: missing %q", i, k)
				}
				v, ok := decodeNumericInt(rawV)
				if !ok {
					return fmt.Errorf("--extras[%d].%s: must be an integer, got %s "+
						"(a quoted number like \"7\" is fine — `extras list` returns ids as "+
						"strings and the server accepts them)", i, k, rawV)
				}
				if v < 1 {
					return fmt.Errorf("--extras[%d].%s: must be >= 1 (got %d); omit the entry "+
						"rather than asking for none of an add-on", i, k, v)
				}
			}
		}
	}

	// A non-zero deposit requires a currency, which the server validates
	// against the account's rather than converting. Caught locally because
	// --deposit without --currency is the shape a caller lands on naturally —
	// there is one currency per account, so naming it feels redundant.
	if raw, ok := m["deposit"]; ok && !jsonIsNull(raw) {
		deposit, ok := decodeNumericInt(raw)
		if !ok {
			return fmt.Errorf("--deposit must be a whole number of MINOR units, got %s "+
				"(a quoted number is fine)", raw)
		}
		if deposit < 0 {
			return fmt.Errorf("--deposit must be 0 or more (got %d); it is money already "+
				"RECEIVED, not a charge and not a refund", deposit)
		}
		if _, hasCurrency := m["currency"]; deposit != 0 && !hasCurrency {
			return fmt.Errorf("--deposit %d needs --currency: a non-zero deposit must state "+
				"the account's currency, which the server validates rather than converts, so "+
				"the amount is recorded exactly as sent", deposit)
		}
	}

	// The spec lists payload-level refusals "that no single field rule can
	// express", and names why each is caught at the edge rather than in the
	// engine: the engine answers an all-zero mix "with a complaint about
	// `fare` — a parameter you never sent". Three of the six are decidable
	// from the body alone; the other three (a resource_id not on this
	// departure's option, a branding_id from another unit, an availability_id
	// that is not a product-option departure) need server state and are left
	// to it.
	if raw, ok := m["guests"]; ok {
		var guests map[string]json.RawMessage
		if json.Unmarshal(raw, &guests) == nil && len(guests) > 0 {
			total, allNumeric := 0, true
			for _, rawCount := range guests {
				n, numeric := decodeNumericInt(rawCount)
				if !numeric {
					allNumeric = false
					break
				}
				total += n
			}
			if allNumeric && total == 0 {
				return fmt.Errorf("--guests books nobody: every category is 0. A category may be " +
					"0 on its own, but a booking of no guests is refused — and the booking engine " +
					"reports it as a complaint about `fare`, a parameter you never sent")
			}
		}
	}

	// Presence is not enough: payment_method is typed `[string, "null"]`, and an
	// explicit null leaves the engine on its `card` default exactly as a missing
	// key does — which is the whole reason this gate exists. The sibling deposit
	// and payment_notes checks already use jsonIsNull; this matches them.
	rawMethod, hasMethodKey := m["payment_method"]
	hasPaymentMethod := hasMethodKey && !jsonIsNull(rawMethod)
	if raw, ok := m["deposit"]; ok && !jsonIsNull(raw) {
		if n, numeric := decodeNumericInt(raw); numeric && n != 0 && !hasPaymentMethod {
			return fmt.Errorf("--deposit %d needs --payment-method: the booking engine defaults an "+
				"absent method to `card`, so recording money with no method records a CARD payment "+
				"that was never taken", n)
		}
	}

	if _, hasNotes := m["payment_notes"]; hasNotes {
		if raw, ok := m["deposit"]; !ok || jsonIsNull(raw) {
			return fmt.Errorf("--payment-notes needs a --deposit to attach to: the note is written " +
				"onto the transaction the deposit creates, so without one it has nowhere to go. " +
				"Use --private-notes instead")
		}
	}

	// booking_type decides which of two field sets is legal, and the spec
	// REFUSES the wrong one rather than dropping it — in both directions. The
	// reservation arm matters most: `expiry` is required there, and a
	// reservation taken without one is the failure a caller is least likely to
	// predict, because every other optional-looking field on this request
	// really is optional.
	bookingType := ""
	if raw, ok := m["booking_type"]; ok {
		_ = json.Unmarshal(raw, &bookingType)
	}
	_, hasExpiry := m["expiry"]
	switch bookingType {
	case "reservation":
		if !hasExpiry {
			return fmt.Errorf("--booking-type reservation requires --expiry: a reservation is " +
				"a hold that LAPSES, so the server refuses one with no window (the value is in " +
				"SECONDS — 1800 is half an hour, and the configured default is 600)")
		}
		// An ordered slice, and every present field named at once. A map here
		// picks a random one of the three per run, so an operator fixes the
		// flag they were told about and is refused again for the next — and any
		// assertion on the message is flaky.
		var offending []string
		for _, pair := range [][2]string{
			{"--deposit", "deposit"},
			{"--payment-method", "payment_method"},
			{"--payment-notes", "payment_notes"},
		} {
			if _, present := m[pair[1]]; present {
				offending = append(offending, pair[0])
			}
		}
		if len(offending) > 0 {
			return fmt.Errorf("%s refused on --booking-type reservation: a reservation "+
				"records no payment. Take the booking with --booking-type booking, or drop "+
				"the payment fields from the hold", strings.Join(offending, ", "))
		}
	case "booking":
		if hasExpiry {
			return fmt.Errorf("--expiry is refused on --booking-type booking: only a " +
				"reservation lapses. Use --booking-type reservation for a hold")
		}
	}
	return nil
}
