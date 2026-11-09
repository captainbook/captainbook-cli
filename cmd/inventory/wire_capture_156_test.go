package inventory

// Wire-level capture for the surfaces added by the cli-v1 1.56.0 sync —
// spec 1.34.0 → 1.56.0.
//
// Same reason as its two predecessors, which state it plainly: both drift
// directions prove a flag EXISTS, never that its value reaches the wire, so a
// declared-but-unwired flag is invisible to the whole suite while --help
// advertises it. This sync is the largest one yet by that measure — four new
// operations, three of them on bookings, plus four new body fields:
//
//   - 1.47.0 POST /bookings, PATCH /bookings/{id}, POST /bookings/{id}/reschedule
//   - 1.47.0 POST /products/bookable
//   - 1.49.0 replaced_resource_id on bookings set-resources
//   - 1.50.0 same_resource inside a companions link
//   - 1.51.0 max_per_resource on pricing-categories create/update
//   - 1.53.0 allocation_mode on products update
//   - 1.55.0 three new action_type trigger values
//
// The three booking writes carry local gates that mirror documented 422s, and
// those get the same treatment as the wire assertions: a gate is only worth
// having if it refuses BEFORE the request, so each refusal case asserts that
// nothing reached the server. A gate that let the call through and relied on
// the server's 422 would still read as a gate by inspection while costing the
// operator a round trip and an audit row.

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
)

// -----------------------------------------------------------------------------
// POST /bookings — `bookings create`
// -----------------------------------------------------------------------------

// createArgs is a minimal VALID `bookings create` invocation, so each test can
// vary one thing. Keeping the valid baseline in one place is what makes the
// refusal cases below meaningful: each one differs from a call that works by
// exactly the thing it claims to refuse.
func createArgs(extra ...string) []string {
	return append([]string{"bookings", "create",
		"--availability-id", "4821",
		"--booking-type", "booking",
		"--guests", `{"3":2,"4":1}`,
		"--is-refundable=true",
		"--customer-id", "7b9e5a2c-0000-4000-8000-000000000001",
	}, extra...)
}

// createGuestsArgs is createArgs with a different --guests value. pflag takes
// the LAST occurrence of a repeated flag, so appending overrides the baseline
// without having to restate the other four required flags.
func createGuestsArgs(guests string) []string {
	return createArgs("--guests", guests)
}

// reservationArgs is the baseline switched to a reservation, which changes
// which fields are legal rather than just their values.
func reservationArgs(extra ...string) []string {
	return createArgs(append([]string{"--booking-type", "reservation"}, extra...)...)
}

// TestWire_BookingsCreateReachesTheServer is the baseline: the operation the
// CLI could not perform at all before this sync.
//
// It asserts the PATH as well as the body. `bookings create` and
// `products create-bookable` are both POSTs to a path that differs from a
// neighbouring one by a single segment, and every other guard in this package
// reads the DECLARED annotation rather than the request a closure issues — so
// a closure wired to the wrong generated operation passes both drift
// directions.
//
// Value: protects=bookings create issues POST /bookings carrying every typed flag; fails_when=a flag is dropped from the field map, or the closure calls a neighbouring generated operation; why_new=the operation is new in this sync; seam=captureReq
func TestWire_BookingsCreateReachesTheServer(t *testing.T) {
	_, body, req, hit, err := captureReq(t, createArgs(
		"--deposit", "2000",
		"--currency", "EUR",
		"--payment-method", "cash",
		"--payment-notes", "paid at the desk",
		"--comments", "window seat",
		"--private-notes", "regular",
		"--extras", `[{"extraId":7,"quantity":2}]`,
		"--answers", `{"11":"vegetarian"}`,
		"--resource-id", "9",
		"--partner-id", "4",
		"--branding-id", "2",
		"--locales", "en,fr",
		"--notify-customer=false",
	)...)
	if !hit {
		t.Fatalf("no request reached the server: %v", err)
	}
	if req.Method != "POST" || req.Path != "/bookings" {
		t.Errorf("request line = %s %s, want POST /bookings", req.Method, req.Path)
	}
	for key, want := range map[string]any{
		"availability_id": float64(4821),
		"booking_type":    "booking",
		"is_refundable":   true,
		"customer_id":     "7b9e5a2c-0000-4000-8000-000000000001",
		"deposit":         float64(2000),
		"currency":        "EUR",
		"payment_method":  "cash",
		"payment_notes":   "paid at the desk",
		"comments":        "window seat",
		"private_notes":   "regular",
		"resource_id":     float64(9),
		"partner_id":      float64(4),
		"branding_id":     float64(2),
		"notify_customer": false,
	} {
		if got := body[key]; got != want {
			t.Errorf("%s = %#v, want %#v (body: %v)", key, got, want, body)
		}
	}
	// guests is the party, and it has to arrive as an OBJECT keyed by pricing
	// category id — the one field whose wrong shape the server reads as "no
	// categories at all".
	guests, ok := body["guests"].(map[string]any)
	if !ok {
		t.Fatalf("guests = %#v, want a JSON object keyed by pricing category id", body["guests"])
	}
	if guests["3"] != float64(2) || guests["4"] != float64(1) {
		t.Errorf("guests = %v, want {3:2, 4:1}", guests)
	}
	extras, ok := body["extras"].([]any)
	if !ok || len(extras) != 1 {
		t.Fatalf("extras = %#v, want a one-element array", body["extras"])
	}
	// notify_admins was never typed, so it must be ABSENT rather than sent as
	// the cobra default. Both server defaults are TRUE, so sending the mirror
	// of an untyped flag is harmless here — but the same pattern on a flag
	// whose default the server later changes would silently pin the old one.
	if _, present := body["notify_admins"]; present {
		t.Errorf("notify_admins must be absent when the flag is unset, got %v", body)
	}
}

// TestWire_BookingsCreateRefusalsHappenBeforeTheWire covers every local gate on
// StoreBookingRequest. Each case mirrors a 422 the spec documents, and each one
// must refuse with nothing sent.
//
// Value: protects=the documented StoreBookingRequest refusals are named locally, with no request issued; fails_when=validateStoreBookingBody stops being called, or a gate is dropped or inverted; why_new=the operation and all of its gates are new in this sync; seam=capture
func TestWire_BookingsCreateRefusalsHappenBeforeTheWire(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string // a distinctive fragment of the refusal
	}{
		{
			// "Sending both, or neither, is a 422."
			name: "both customer spellings",
			args: createArgs("--customer", `{"name":"A","country":"GR"}`),
			want: "mutually exclusive",
		},
		{
			name: "neither customer spelling",
			args: []string{"bookings", "create",
				"--availability-id", "4821", "--booking-type", "booking",
				"--guests", `{"3":1}`, "--is-refundable=true"},
			want: "needs a customer",
		},
		{
			// `[{"3":2}]` and `{"3":2}` are both well-formed JSON and the json
			// flag type accepts either; only one is a guest mix.
			name: "guests sent as an array",
			args: createGuestsArgs(`[{"3":2}]`),
			want: "JSON OBJECT",
		},
		{
			name: "guests empty",
			args: createGuestsArgs(`{}`),
			want: "at least one pricing category",
		},
		{
			// A JSON true would otherwise reach the server as "1": the spec
			// says numeric validates BEFORE integer for exactly this reason.
			name: "a boolean guest count",
			args: createGuestsArgs(`{"3":true}`),
			want: "whole number of guests",
		},
		{
			// -1 is not a refund. The deposit gate's lower bound is its own
			// branch: a negative value has a currency's worth of meaning on
			// the wire and the server refuses it.
			name: "a negative deposit",
			args: createArgs("--deposit=-1", "--currency", "EUR"),
			want: "0 or more",
		},
		{
			// The count branch below the integer check: a whole number that
			// books negative people.
			name: "a negative guest count",
			args: createGuestsArgs(`{"3":-1}`),
			want: "must be 0 or more",
		},
		{
			// A null ITEM, not a null list: `[null]` decodes into a one-element
			// slice holding a nil map, so without its own branch every key
			// lookup below would read as absent and report a missing extraId.
			name: "a null extras item",
			args: createArgs("--extras", `[null]`),
			want: "got null",
		},
		{
			// extraId is the ONE camelCase key on this request.
			name: "extras with the snake_case spelling",
			args: createArgs("--extras", `[{"extra_id":7,"quantity":2}]`),
			want: "camelCase",
		},
		{
			name: "extras quantity of zero",
			args: createArgs("--extras", `[{"extraId":7,"quantity":0}]`),
			want: "omit the entry",
		},
		{
			// "A NON-ZERO deposit requires currency, which must be the
			// account's — it is validated and NOT converted."
			name: "a non-zero deposit with no currency",
			args: createArgs("--deposit", "2000"),
			want: "needs --currency",
		},
		{
			name: "a reservation with no expiry",
			args: reservationArgs(),
			want: "requires --expiry",
		},
		{
			name: "a reservation carrying payment",
			args: reservationArgs("--expiry", "1800", "--payment-method", "cash"),
			want: "refused on --booking-type reservation",
		},
		{
			name: "a booking carrying an expiry",
			args: createArgs("--expiry", "1800"),
			want: "only a reservation lapses",
		},
		{
			// The generic enum gate, not validateStoreBookingBody — included
			// because `card` is the value an operator reaches for first and
			// the spec refuses it on EVERY account.
			name: "payment-method card",
			args: createArgs("--payment-method", "card"),
			want: "card",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, body, hit, err := capture(t, tc.args...)
			if err == nil {
				t.Fatalf("expected a refusal; the command succeeded with body %v", body)
			}
			if hit {
				t.Errorf("the refusal still reached the server with body %v", body)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}

	// The guard against over-correcting: a reservation that states its window
	// and carries no payment must still go through, or the gates above are
	// just a ban on the field.
	_, body, hit, err := capture(t, "bookings", "create",
		"--availability-id", "4821", "--booking-type", "reservation",
		"--expiry", "1800", "--guests", `{"3":1}`, "--is-refundable=false",
		"--customer", `{"name":"Mrs Papadopoulou","country":"GR"}`)
	if !hit {
		t.Fatalf("a legitimate reservation was refused: %v", err)
	}
	if body["expiry"] != float64(1800) || body["booking_type"] != "reservation" {
		t.Errorf("reservation body = %v, want expiry 1800 and booking_type reservation", body)
	}
	cust, ok := body["customer"].(map[string]any)
	if !ok || cust["country"] != "GR" {
		t.Errorf("customer = %#v, want the object to reach the wire verbatim", body["customer"])
	}

	// The two explicit-null / zero paths the gates skip on purpose, asserted so
	// a tightened gate cannot start refusing them. An EXPLICIT null extras list
	// is how a --data caller clears add-ons, and it is not an array to iterate;
	// a ZERO deposit records "nothing was handed over" and needs no currency,
	// which is the whole reason the currency rule is keyed on non-zero. Both go
	// through --data because the json flag type refuses a bare `null` before
	// the gate is ever reached, so --data is the only road to this branch.
	t.Run("an explicit null extras list and a zero deposit are not refusals", func(t *testing.T) {
		_, body, hit, err := capture(t, createArgs("--data", `{"extras":null,"deposit":0}`)...)
		if !hit {
			t.Fatalf("a null extras list with a zero deposit was refused: %v", err)
		}
		if got, present := body["extras"]; !present || got != nil {
			t.Errorf("extras = %#v, want an explicit JSON null on the wire", got)
		}
		if body["deposit"] != float64(0) {
			t.Errorf("deposit = %#v, want 0 (body: %v)", body["deposit"], body)
		}
	})
}

// TestWire_BookingsCreateDataIsHeldToTheSameRules closes the bypass. Every gate
// reads the ASSEMBLED body rather than the flags for one reason: a gate written
// as `if args.FlagSet(...)` has a documented bypass, and --data is it.
//
// Value: protects=a --data payload is validated exactly as the typed flags are; fails_when=any gate is rewritten to read args.FlagSet instead of the assembled body; why_new=the gates are new, and the same bypass was a real bug on companions; seam=capture
func TestWire_BookingsCreateDataIsHeldToTheSameRules(t *testing.T) {
	// --data supplements rather than replaces here, because the four fields the
	// spec lists in `required:` are Required flags — so the bypass worth
	// closing is a field supplied ONLY through --data. `customer` is the one
	// that matters: paired with the --customer-id flag it is the exact 422 the
	// spec calls out ("Sending both, or neither, is a 422"), and a gate reading
	// args.FlagSet would see one spelling and pass.
	t.Run("a customer object supplied through --data still collides", func(t *testing.T) {
		_, body, hit, err := capture(t, append(createArgs(),
			"--data", `{"customer":{"name":"A","country":"GR"}}`)...)
		if err == nil {
			t.Fatalf("--data carrying a second customer spelling was accepted: %v", body)
		}
		if hit {
			t.Errorf("the refusal still reached the server with body %v", body)
		}
		if !strings.Contains(err.Error(), "mutually exclusive") {
			t.Errorf("error = %v, want the same collision refusal the flags get", err)
		}
	})

	// Same shape on the money rule: a deposit arriving only through --data must
	// still have to state its currency.
	t.Run("a deposit supplied through --data still needs a currency", func(t *testing.T) {
		_, body, hit, err := capture(t, append(createArgs(), "--data", `{"deposit":2000}`)...)
		if err == nil {
			t.Fatalf("--data carrying a bare deposit was accepted: %v", body)
		}
		if hit {
			t.Errorf("the refusal still reached the server with body %v", body)
		}
		if !strings.Contains(err.Error(), "needs --currency") {
			t.Errorf("error = %v, want the deposit/currency refusal", err)
		}
	})
}

// -----------------------------------------------------------------------------
// PATCH /bookings/{id} — `bookings update`
// -----------------------------------------------------------------------------

// TestWire_BookingsUpdateReachesTheServer, plus the "at least one field" rule.
//
// That rule is the interesting half. The server answers 422 for an empty
// amendment specifically so an agent cannot read it as applied — but the agent
// that provoked it sent no field in the first place, and its natural next move
// is to retry under --dry-run, which is also not a change. Naming the three
// fields locally ends that loop in one message.
//
// Value: protects=bookings update issues PATCH /bookings/{id} with its three fields, and refuses an empty amendment locally; fails_when=requireAnyBookingAmendment stops being called or starts counting dry_run as a change; why_new=the operation is new in this sync; seam=captureReq
func TestWire_BookingsUpdateReachesTheServer(t *testing.T) {
	_, body, req, hit, err := captureReq(t, "bookings", "update", "bk_42",
		"--comments", "moved to the back row",
		"--private-notes", "VIP",
		"--custom-attributes", `{"dietary":"vegetarian"}`)
	if !hit {
		t.Fatalf("no request reached the server: %v", err)
	}
	if req.Method != "PATCH" || req.Path != "/bookings/bk_42" {
		t.Errorf("request line = %s %s, want PATCH /bookings/bk_42", req.Method, req.Path)
	}
	if body["comments"] != "moved to the back row" || body["private_notes"] != "VIP" {
		t.Errorf("notes did not reach the wire: %v", body)
	}
	attrs, ok := body["custom_attributes"].(map[string]any)
	if !ok || attrs["dietary"] != "vegetarian" {
		t.Errorf("custom_attributes = %#v, want the object verbatim", body["custom_attributes"])
	}

	t.Run("an empty amendment is refused before the wire", func(t *testing.T) {
		for _, args := range [][]string{
			{"bookings", "update", "bk_42"},
			{"bookings", "update", "bk_42", "--dry-run"},
		} {
			_, b, h, e := capture(t, args...)
			if e == nil {
				t.Fatalf("%v was accepted with body %v", args, b)
			}
			if h {
				t.Errorf("%v still reached the server with body %v", args, b)
			}
			if !strings.Contains(e.Error(), "at least one of") {
				t.Errorf("error = %v, want it to name the three fields", e)
			}
		}
	})

	t.Run("one field is enough", func(t *testing.T) {
		// Presence, not value: the spec says resending a value the booking
		// already has is a 200 with matching diff halves, and an empty string
		// is a legitimate note. A gate keyed on non-emptiness would refuse
		// this.
		_, b, h, e := capture(t, "bookings", "update", "bk_42", "--comments", "")
		if !h {
			t.Fatalf("an empty-string comment was refused: %v", e)
		}
		if got, present := b["comments"]; !present || got != "" {
			t.Errorf("comments = %#v, want an empty string on the wire", got)
		}
	})
}

// -----------------------------------------------------------------------------
// POST /bookings/{id}/reschedule — `bookings reschedule`
// -----------------------------------------------------------------------------

// TestBookingsReschedule_IsCSGated mirrors TestBookingsCancel_IsCSGated for the
// fifth CS route, and reads the ability off the spec rather than trusting the
// binding.
//
// The spec's securitySchemes table is the authority here and names reschedule
// explicitly — "the six routes gated on abilities:cli:cs" — with the reason:
// it moves a live booking a customer is already travelling on, which is a
// judgement about a person's trip rather than a catalogue edit. CLAUDE.md
// warns that the table is silent on operations it does not annotate and was
// wrong about cancel; this one it annotates.
//
// Value: protects=bookings reschedule binds cli:cs, so an operator token is refused before the network; fails_when=the binding is relaxed to cli:write, or the spec moves the route between ability groups; why_new=the operation is new in this sync; seam=none
func TestBookingsReschedule_IsCSGated(t *testing.T) {
	def := bookingsDefFor(t, "bookings reschedule <id>")
	if def.Ability != invpkg.CS {
		t.Errorf("bookings reschedule binds Ability %q, want %q (cli:cs): the spec's ability "+
			"table names reschedule among the six cli:cs routes, beside cancel, comp, refund, "+
			"confirmation resend and gift-cert void", def.Ability, invpkg.CS)
	}
	// And the spec still says so. A re-sync that moved the route into the
	// cli:write complement would leave the binding over-strict, refusing an
	// operator token the server would now accept.
	if !strings.Contains(specAbilityTableRow(t, "cli:cs"), "reschedule") {
		t.Error("the spec's cli:cs ability row no longer names reschedule — the route may have " +
			"moved to cli:write, which would make this gate refuse a call the server accepts")
	}
}

// specAbilityTableRow returns the spec's securitySchemes prose row for one
// ability. TestSpecDrift_AbilitiesMatchSpec already parses that table for its
// route count; this reads the row as text because the claim here is about one
// word appearing in it, not about the count.
func specAbilityTableRow(t *testing.T, ability string) string {
	t.Helper()
	raw, err := os.ReadFile("../../api/inventory/cli-v1.yaml")
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, "| `"+ability+"`") {
			return line
		}
	}
	t.Fatalf("spec has no securitySchemes table row for %q", ability)
	return ""
}

// TestWire_BookingsRescheduleReachesTheServer.
//
// The note handling is the part worth pinning. --comments is the FALLBACK used
// when --reschedule-notes is omitted, and `--reschedule-notes=""` records a
// deliberately blank note instead of falling back — so an empty string has to
// survive to the wire as an empty string, distinguishable from the key being
// absent. A closure using the usual `if v != ""` unset-guard would collapse the
// two and silently restore the fallback the operator asked to suppress.
//
// Value: protects=reschedule issues POST /bookings/{id}/reschedule, and an explicitly blank reschedule_notes reaches the wire rather than being dropped; fails_when=the closure gains a non-empty guard on the notes, or the field map loses a key; why_new=the operation is new in this sync; seam=captureReq
func TestWire_BookingsRescheduleReachesTheServer(t *testing.T) {
	_, body, req, hit, err := captureReq(t, "bookings", "reschedule", "bk_42",
		"--availability-id", "5190",
		"--resource-id", "9",
		"--comments", "customer rang",
		"--reschedule-notes", "moved to Saturday at the customer's request",
		"--notify-customer=false")
	if !hit {
		t.Fatalf("no request reached the server: %v", err)
	}
	if req.Method != "POST" || req.Path != "/bookings/bk_42/reschedule" {
		t.Errorf("request line = %s %s, want POST /bookings/bk_42/reschedule", req.Method, req.Path)
	}
	for key, want := range map[string]any{
		"availability_id":  float64(5190),
		"resource_id":      float64(9),
		"comments":         "customer rang",
		"reschedule_notes": "moved to Saturday at the customer's request",
		"notify_customer":  false,
	} {
		if got := body[key]; got != want {
			t.Errorf("%s = %#v, want %#v (body: %v)", key, got, want, body)
		}
	}

	t.Run("a deliberately blank note is not the same as an omitted one", func(t *testing.T) {
		_, b, h, e := capture(t, "bookings", "reschedule", "bk_42",
			"--availability-id", "5190", "--comments", "fallback", "--reschedule-notes", "")
		if !h {
			t.Fatalf("no request reached the server: %v", e)
		}
		got, present := b["reschedule_notes"]
		if !present {
			t.Fatalf("reschedule_notes is absent, so the server falls back to --comments — "+
				"the one thing an explicit empty value exists to prevent (body: %v)", b)
		}
		if got != "" {
			t.Errorf("reschedule_notes = %#v, want an empty string", got)
		}
	})

	t.Run("an omitted note sends no key at all", func(t *testing.T) {
		_, b, h, e := capture(t, "bookings", "reschedule", "bk_42", "--availability-id", "5190")
		if !h {
			t.Fatalf("no request reached the server: %v", e)
		}
		if _, present := b["reschedule_notes"]; present {
			t.Errorf("reschedule_notes must be absent when unset, so the server's own fallback "+
				"applies: %v", b)
		}
	})
}

// -----------------------------------------------------------------------------
// POST /products/bookable — `products create-bookable`
// -----------------------------------------------------------------------------

// TestWire_ProductsCreateBookableReachesTheServer.
//
// Value: protects=create-bookable issues POST /products/bookable with both lists verbatim; fails_when=the path is wrong, the lists are dropped from the field map, or a list is decoded and re-encoded (which would round a large id); why_new=the operation is new in this sync; seam=captureReq
func TestWire_ProductsCreateBookableReachesTheServer(t *testing.T) {
	_, body, req, hit, err := captureReq(t, "products", "create-bookable",
		"--title", "Sunset Sailing Tour",
		"--currency", "EUR",
		"--options", `[{"title":"10:00 departure","capacity":12}]`,
		"--pricing-categories", `[{"name":"Adults","amount":5000,"type":"ADULT"}]`)
	if !hit {
		t.Fatalf("no request reached the server: %v", err)
	}
	if req.Method != "POST" || req.Path != "/products/bookable" {
		t.Errorf("request line = %s %s, want POST /products/bookable — the plain create is "+
			"POST /products and differs by one segment", req.Method, req.Path)
	}
	opts, ok := body["options"].([]any)
	if !ok || len(opts) != 1 {
		t.Fatalf("options = %#v, want a one-element array", body["options"])
	}
	opt, _ := opts[0].(map[string]any)
	if opt["title"] != "10:00 departure" || opt["capacity"] != float64(12) {
		t.Errorf("options[0] = %v, want the object verbatim", opt)
	}
	cats, ok := body["pricing_categories"].([]any)
	if !ok || len(cats) != 1 {
		t.Fatalf("pricing_categories = %#v, want a one-element array", body["pricing_categories"])
	}
	cat, _ := cats[0].(map[string]any)
	if cat["name"] != "Adults" || cat["amount"] != float64(5000) {
		t.Errorf("pricing_categories[0] = %v, want the object verbatim", cat)
	}
}

// TestWire_ProductsCreateBookableClosedShapesAreRefusedLocally.
//
// The spec is explicit about why the item shapes are closed rather than
// forgiving: "silently discarding either produces a product priced at nothing
// under a 201". `title` on an option (the column is `name`) and `amount` on a
// category are the two spellings it names as the ones callers get wrong.
//
// Value: protects=a misspelled or missing key in either list is named locally, with no request issued; fails_when=validateBookableProductBody stops being called or its allow-lists drift from the spec's additionalProperties:false shapes; why_new=the operation and its gates are new in this sync; seam=capture
func TestWire_ProductsCreateBookableClosedShapesAreRefusedLocally(t *testing.T) {
	base := []string{"products", "create-bookable", "--title", "T", "--currency", "EUR"}
	okOptions := `[{"title":"10:00"}]`
	okCategories := `[{"name":"Adults","amount":5000}]`

	for _, tc := range []struct{ name, options, categories, want string }{
		{"an option spelled name instead of title", `[{"name":"10:00"}]`, okCategories, "unknown key"},
		{"an option with no title", `[{"capacity":12}]`, okCategories, `missing "title"`},
		{"a category spelled price instead of amount", okOptions, `[{"name":"Adults","price":5000}]`, "unknown key"},
		{"a category with no amount", okOptions, `[{"name":"Adults"}]`, `missing "amount"`},
		{"a category asserting is_internal", okOptions, `[{"name":"A","amount":1,"is_internal":false}]`, "unknown key"},
		{"an empty option list", `[]`, okCategories, "at least 1 entry"},
		// `[null]` decodes into a one-element slice holding a nil map, so
		// without the null-item branch every required key reads as absent and
		// the refusal names `title` instead of the null the caller wrote.
		{"a null option item", `[null]`, okCategories, "got null"},
		{"a null category item", okOptions, `[null]`, "got null"},
		{"an object where a list belongs", `{"title":"10:00"}`, okCategories, "JSON ARRAY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append(append([]string{}, base...),
				"--options", tc.options, "--pricing-categories", tc.categories)
			_, body, hit, err := capture(t, args...)
			if err == nil {
				t.Fatalf("expected a refusal; the command succeeded with body %v", body)
			}
			if hit {
				t.Errorf("the refusal still reached the server with body %v", body)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}

	// The cap, derived rather than typed, so it tracks the constant.
	t.Run("more than the cap is refused", func(t *testing.T) {
		rows := make([]string, bookableListMax+1)
		for i := range rows {
			rows[i] = `{"title":"t"}`
		}
		args := append(append([]string{}, base...),
			"--options", "["+strings.Join(rows, ",")+"]", "--pricing-categories", okCategories)
		_, body, hit, err := capture(t, args...)
		if err == nil {
			t.Fatalf("a list of %d was accepted: %v", len(rows), body)
		}
		if hit {
			t.Errorf("the refusal still reached the server with body %v", body)
		}
		if !strings.Contains(err.Error(), "exceeds the 20-entry cap") {
			t.Errorf("error = %v, want it to name the cap", err)
		}
	})
}

// -----------------------------------------------------------------------------
// The four new body fields on existing operations.
// -----------------------------------------------------------------------------

// TestWire_NewBodyFieldsOf156ReachTheServer covers allocation_mode,
// max_per_resource and replaced_resource_id in one place, because each is a
// single key on an operation whose other fields are already pinned elsewhere.
//
// Value: protects=the three scalar fields this sync added reach the wire under their spec names; fails_when=a field map entry is dropped or misspelled; why_new=all three are new in this sync and no existing test sends them; seam=capture
func TestWire_NewBodyFieldsOf156ReachTheServer(t *testing.T) {
	t.Run("products update sends allocation_mode", func(t *testing.T) {
		_, body, hit, err := capture(t, "products", "update", "pr_1",
			"--allocation-mode", "homogeneous_multi")
		if !hit {
			t.Fatalf("no request reached the server: %v", err)
		}
		if body["allocation_mode"] != "homogeneous_multi" {
			t.Errorf("allocation_mode = %#v, want homogeneous_multi (body: %v)", body["allocation_mode"], body)
		}
	})

	// The create endpoints REFUSE allocation_mode with a 422 rather than
	// dropping it (1.53.0), so neither may offer the flag: a flag whose every
	// value is refused is worse than no flag, because --help advertises it.
	t.Run("neither create offers allocation-mode", func(t *testing.T) {
		for _, args := range [][]string{
			{"products", "create", "--title", "T", "--currency", "EUR", "--allocation-mode", "single_resource"},
			{"products", "create-bookable", "--title", "T", "--currency", "EUR",
				"--options", `[{"title":"t"}]`, "--pricing-categories", `[{"name":"A","amount":1}]`,
				"--allocation-mode", "single_resource"},
		} {
			_, _, hit, err := capture(t, args...)
			if err == nil {
				t.Errorf("%v accepted --allocation-mode; the server refuses it with a 422 on "+
					"both create endpoints, so the flag should not exist", args[:2])
				continue
			}
			if hit {
				t.Errorf("%v reached the server", args[:2])
			}
			if !strings.Contains(err.Error(), "unknown flag") {
				t.Errorf("%v failed with %v, want an unknown-flag parse error — anything else "+
					"means the flag EXISTS", args[:2], err)
			}
		}
	})

	t.Run("pricing-categories create and update send max_per_resource", func(t *testing.T) {
		_, body, hit, err := capture(t, "pricing-categories", "create",
			"--product-id", "pr_1", "--name", "Drivers", "--max-per-resource", "1")
		if !hit {
			t.Fatalf("no request reached the server: %v", err)
		}
		if body["max_per_resource"] != float64(1) {
			t.Errorf("max_per_resource = %#v, want 1 (body: %v)", body["max_per_resource"], body)
		}

		_, body, hit, err = capture(t, "pricing-categories", "update", "pc_7", "--max-per-resource", "99")
		if !hit {
			t.Fatalf("no request reached the server: %v", err)
		}
		if body["max_per_resource"] != float64(99) {
			t.Errorf("max_per_resource = %#v, want 99 (body: %v)", body["max_per_resource"], body)
		}
	})

	// Both ends of the spec's 1-99 range. The upper bound is what FlagDef.Max
	// was added for: the lower bound alone was enforceable before, so a flag
	// advertising "1-99" passed 100 to the server to have its own help text
	// read back as a validation failure.
	t.Run("max-per-resource is bounded at both ends", func(t *testing.T) {
		for _, tc := range []struct{ value, want string }{
			{"0", "must be >= 1"},
			{"-1", "must be >= 1"},
			{"100", "must be <= 99"},
		} {
			_, body, hit, err := capture(t, "pricing-categories", "update", "pc_7",
				"--max-per-resource="+tc.value)
			if err == nil {
				t.Errorf("--max-per-resource=%s was accepted: %v", tc.value, body)
				continue
			}
			if hit {
				t.Errorf("--max-per-resource=%s reached the server: %v", tc.value, body)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("--max-per-resource=%s: error = %v, want %q", tc.value, err, tc.want)
			}
		}
	})

	t.Run("set-resources sends replaced_resource_id beside the main id", func(t *testing.T) {
		_, body, hit, err := capture(t, "bookings", "set-resources", "bk_42",
			"--main-resource-id", "9", "--replaced-resource-id", "7",
			"--expected-resource-state-token", "tok")
		if !hit {
			t.Fatalf("no request reached the server: %v", err)
		}
		if body["replaced_resource_id"] != float64(7) || body["main_resource_id"] != float64(9) {
			t.Errorf("want main 9 replacing 7, got %v", body)
		}
	})

	// REPLACED_RESOURCE_WITHOUT_MAIN, named locally. The pair IS the meaning of
	// the field, so alone it describes no write at all — and the server's
	// refusal arrives inside error.details.rejections[], which the operator has
	// to read to learn they omitted the other half.
	t.Run("replaced-resource-id alone is refused before the wire", func(t *testing.T) {
		_, body, hit, err := capture(t, "bookings", "set-resources", "bk_42",
			"--replaced-resource-id", "7", "--expected-resource-state-token", "tok")
		if err == nil {
			t.Fatalf("--replaced-resource-id alone was accepted: %v", body)
		}
		if hit {
			t.Errorf("the refusal still reached the server with body %v", body)
		}
		if !strings.Contains(err.Error(), "REPLACED_RESOURCE_WITHOUT_MAIN") {
			t.Errorf("error = %v, want it to name the server's own code", err)
		}
	})
}

// TestWire_CompanionsSameResourceReachesTheServer covers the 1.50.0 field.
//
// The failure this prevents is specific and was LIVE the moment the spec
// landed: the companions validator enforces the schema's
// additionalProperties:false by name, so until same_resource was admitted the
// CLI refused, locally, a link the server accepts — telling the operator their
// own correct payload carried an unknown key. On a flag whose semantics are
// REPLACE that reading is the dangerous one: the obvious workaround is to drop
// the key and resend, which silently reverts the rule for every link in the
// list.
//
// Value: protects=same_resource is accepted and reaches the wire verbatim, and a non-boolean is refused locally; fails_when=the key is dropped from the allow-list (a false local refusal) or its type check is removed; why_new=the field is new in this sync and the validator refused it by construction; seam=capture
func TestWire_CompanionsSameResourceReachesTheServer(t *testing.T) {
	for _, same := range []string{"true", "false"} {
		_, body, hit, err := capture(t, "pricing-categories", "update", "pc_7",
			"--companions", `[{"category_id":30,"min_quantity":2,"same_resource":`+same+`}]`)
		if !hit {
			t.Fatalf("same_resource:%s was refused locally: %v", same, err)
		}
		rows, ok := body["companions"].([]any)
		if !ok || len(rows) != 1 {
			t.Fatalf("companions = %#v, want a one-element array", body["companions"])
		}
		row, _ := rows[0].(map[string]any)
		got, present := row["same_resource"]
		if !present {
			t.Fatalf("same_resource did not reach the wire: %v", row)
		}
		if got != (same == "true") {
			t.Errorf("same_resource = %#v, want %s", got, same)
		}
	}

	// Omitted is a third state, not false: "a link sent without it keeps the
	// stored value". Sending an explicit false for an absent key would switch
	// the rule off on every re-sent link.
	t.Run("an omitted same_resource sends no key", func(t *testing.T) {
		_, body, hit, err := capture(t, "pricing-categories", "update", "pc_7",
			"--companions", `[{"category_id":30,"min_quantity":2}]`)
		if !hit {
			t.Fatalf("no request reached the server: %v", err)
		}
		rows, _ := body["companions"].([]any)
		row, _ := rows[0].(map[string]any)
		if _, present := row["same_resource"]; present {
			t.Errorf("same_resource must stay absent when the caller omits it, so the link "+
				"keeps its stored value: %v", row)
		}
	})

	// "Any other value than a boolean is a 422 under
	// companions.{n}.same_resource" — and JSON's truthiness neighbours are
	// exactly what a caller reaches for.
	t.Run("a non-boolean is refused before the wire", func(t *testing.T) {
		for _, bad := range []string{`1`, `"true"`, `"yes"`, `null`} {
			_, body, hit, err := capture(t, "pricing-categories", "update", "pc_7",
				"--companions", `[{"category_id":30,"min_quantity":2,"same_resource":`+bad+`}]`)
			if err == nil {
				t.Errorf("same_resource:%s was accepted: %v", bad, body)
				continue
			}
			if hit {
				t.Errorf("same_resource:%s reached the server: %v", bad, body)
			}
			if !strings.Contains(err.Error(), "same_resource") {
				t.Errorf("same_resource:%s: error = %v, want it to name the field", bad, err)
			}
		}
	})
}

// TestWire_NewTriggerActionTypesAreAccepted covers the 1.55.0 enum additions.
//
// The enum gate is a local refusal, so a value the server accepts and the CLI
// does not is unreachable from the CLI entirely — the request is never sent.
// segment_entered and segment_left are the pointed case: the spec says both
// "have been accepted since they shipped but were never written down", so the
// CLI has been refusing two live trigger types.
//
// Value: protects=the three action_type values added in 1.55.0 pass the enum gate and reach the wire; fails_when=the description's enum run loses a token, which makes the value locally unreachable; why_new=all three are new to the CLI's gate in this sync; seam=capture
func TestWire_NewTriggerActionTypesAreAccepted(t *testing.T) {
	for _, actionType := range []string{"segment_entered", "segment_left", "whatsapp_message_received"} {
		_, body, hit, err := capture(t, "workflows", "trigger", "create", "wf_1",
			"--action-type", actionType)
		if !hit {
			t.Fatalf("--action-type %s was refused locally: %v", actionType, err)
		}
		if body["action_type"] != actionType {
			t.Errorf("action_type = %#v, want %s (body: %v)", body["action_type"], actionType, body)
		}
	}

	// The gate must still be a gate.
	_, body, hit, err := capture(t, "workflows", "trigger", "create", "wf_1",
		"--action-type", "whatsapp_message_sent")
	if err == nil {
		t.Fatalf("an invented action_type was accepted: %v", body)
	}
	if hit {
		t.Errorf("the refusal still reached the server with body %v", body)
	}
}

// -----------------------------------------------------------------------------
// The declared bounds and the derived field map, enumerated from the live tree.
// -----------------------------------------------------------------------------

// TestWire_FlagDefMaxIsEnforcedForEveryBoundedFlag is the Max half of
// TestWire_FlagDefMinIsEnforcedForEveryBoundedFlag, and it exists because the
// annotation this sync added had no reader at all.
//
// bindCommands writes `intMaxes` with the stated purpose that "a bound nothing
// can read is a bound no test can enumerate, which is the hole intMins was
// added to close" — but nothing read it, so the only Max in the tree was
// exercised by a hand-written case naming `pricing-categories update` and the
// value 99. The next flag to declare Max would inherit no coverage, which is
// precisely the asymmetry the field was introduced to end: the lower bound was
// enforced centrally and the upper one was not, and `stats products --limit`
// paid for it by passing 100 to a server whose spec caps it at 50.
//
// Unlike the Min walker this does NOT skip commands taking a positional id —
// the first flag to declare Max (`--max-per-resource`) lives on
// `pricing-categories update <id>`, so skipping them would find nothing.
//
// Value: protects=every FlagDef declaring Max refuses value+1 before any request, and accepts the bound itself; fails_when=the central Max check in makeRunE is dropped or made exclusive, or a new Max-bearing flag is added with no guard; why_new=the intMaxes annotation had no reader and the single bound in the tree was asserted by a hand-written case; seam=testNewRunner
func TestWire_FlagDefMaxIsEnforcedForEveryBoundedFlag(t *testing.T) {
	type bounded struct {
		path  []string
		flag  string
		max   int
		extra []string // positionals + required flags, without which cobra never runs
	}
	var all []bounded

	var walk func(c *cobra.Command, trail []string)
	walk = func(c *cobra.Command, trail []string) {
		// Read the bounds off the LIVE command via the annotation, not the AST:
		// a FlagDef built in a loop reads as empty to an AST walker and the
		// entity is skipped in silence.
		if raw := c.Annotations["intMaxes"]; raw != "" {
			for _, pair := range strings.Split(raw, ",") {
				name, rawMax, ok := strings.Cut(pair, "=")
				if !ok {
					t.Fatalf("malformed intMaxes annotation on %s: %q", c.CommandPath(), pair)
				}
				m, err := strconv.Atoi(rawMax)
				if err != nil {
					t.Fatalf("malformed intMaxes bound on %s: %q", c.CommandPath(), pair)
				}
				all = append(all, bounded{
					path:  append([]string{}, trail...),
					flag:  name,
					max:   m,
					extra: append(positionalStubs(c), requiredFlagArgs(c, name)...),
				})
			}
		}
		for _, child := range c.Commands() {
			walk(child, append(append([]string{}, trail...), child.Name()))
		}
	}
	root := Cmd()
	for _, child := range root.Commands() {
		walk(child, []string{child.Name()})
	}

	if len(all) == 0 {
		t.Fatal("found no int flags declaring Max — the walker is broken, not the CLI")
	}

	for _, b := range all {
		t.Run(strings.Join(b.path, " ")+" --"+b.flag, func(t *testing.T) {
			args := append(append([]string{}, b.path...), b.extra...)
			args = append(args, "--"+b.flag+"="+strconv.Itoa(b.max+1))
			q, body, hit, err := capture(t, args...)
			if err == nil {
				t.Errorf("--%s=%d was accepted (max is %d)", b.flag, b.max+1, b.max)
			} else if !strings.Contains(err.Error(), b.flag) {
				t.Errorf("--%s=%d failed with %v, want the error to name the flag", b.flag, b.max+1, err)
			}
			if hit {
				t.Errorf("--%s=%d reached the wire as %v / %v", b.flag, b.max+1, q, body)
			}

			// The bound itself must still be accepted, or a guard that refused
			// everything would satisfy the assertion above. Max is INCLUSIVE.
			//
			// A command whose OTHER inputs the stub cannot satisfy (a scope
			// trio, a UUID positional) is refused for a reason that has nothing
			// to do with this bound. That is not a Max failure, and failing on
			// it would make the test assert the stub's realism instead of the
			// guard. The counter-guard survives the exemption: a check that
			// refused every value would still name THIS flag, which is exactly
			// what the condition below tests for.
			args = append(append([]string{}, b.path...), b.extra...)
			args = append(args, "--"+b.flag+"="+strconv.Itoa(b.max))
			switch _, _, hit, err := capture(t, args...); {
			case hit:
				// Accepted and sent: the inclusive bound holds.
			case err != nil && strings.Contains(err.Error(), "--"+b.flag):
				t.Errorf("--%s=%d (the maximum, which is INCLUSIVE) was refused by its own "+
					"bound check: %v", b.flag, b.max, err)
			default:
				t.Logf("inclusive bound not exercised for --%s: the command was refused for an "+
					"unrelated reason (%v)", b.flag, err)
			}
		})
	}
	t.Logf("exercised %d upper-bounded flags across the live tree", len(all))
}

// positionalStubs returns one placeholder argument per `<token>` in the
// command's Use line, so a command taking an id can be invoked at all. The
// value is never dereferenced by these tests — the stub server answers every
// path — it only has to get past cobra's ExactArgs.
func positionalStubs(c *cobra.Command) []string {
	var out []string
	for _, tok := range strings.Fields(c.Use)[1:] {
		if strings.HasPrefix(tok, "<") {
			out = append(out, "stub_1")
		}
	}
	return out
}

// TestWire_ProductsCreateBookableSendsEveryDeclaredFlag pins the DERIVED field
// map, which is the one thing a derivation buys and a literal does not need.
//
// `products create-bookable` is the only command whose field map is computed —
// snakeFieldMap turns each flag name into its snake_case key rather than
// restating 29 pairs. The derivation cannot typo a pair, but it CAN omit one:
// the flag list and the name list passed to snakeFieldMap are two separate
// literals, so a flag declared above and left out below is advertised by
// --help, accepted by cobra, and silently dropped on the way to the wire. Both
// drift directions stay green — they read the flag list, not the field map —
// and the baseline wire test sends only four of the twenty-nine.
//
// So this enumerates the LIVE flag set and asserts each one arrives, which also
// means the next flag added to this command inherits the check.
//
// Value: protects=every flag products create-bookable declares reaches the wire under its snake_case key; fails_when=a flag is added to the Flags literal and not to the snakeFieldMap call, or snakeFieldMap stops replacing dashes; why_new=the field map is derived rather than literal and the baseline wire test sends 4 of its 29 keys; seam=testNewRunner
func TestWire_ProductsCreateBookableSendsEveryDeclaredFlag(t *testing.T) {
	cmd, _ := resolveCommand(Cmd(), []string{"products", "create-bookable"})
	if cmd == nil {
		t.Fatal("products create-bookable is not registered")
	}

	// Values that have to satisfy a local gate rather than merely parse: the
	// enum gate reads the pipe runs in these descriptions, and the two lists
	// are shape-checked by validateBookableProductBody.
	special := map[string]string{
		"status":             "draft",
		"schedule-type":      "date",
		"delivery-method":    "VOUCHER",
		"redemption-method":  "MANIFEST",
		"currency":           "EUR",
		"locale":             "en",
		"timezone":           "Europe/Athens",
		"options":            `[{"title":"10:00 departure"}]`,
		"pricing-categories": `[{"name":"Adults","amount":5000}]`,
	}
	// The four flags bindCommands adds to every mutation. They are transport
	// and preview concerns, not body fields.
	framework := map[string]bool{
		"format": true, "dry-run": true, "idempotency-key": true, "data": true,
		"help": true, "verbose": true, "profile": true,
	}

	var args []string
	var want []string
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if framework[f.Name] {
			return
		}
		v, ok := special[f.Name]
		if !ok {
			switch f.Value.Type() {
			case "bool":
				v = "true"
			case "int":
				v = "1"
			case "intSlice", "stringSlice":
				v = "1"
			default:
				v = "x"
			}
		}
		args = append(args, "--"+f.Name+"="+v)
		want = append(want, f.Name)
	})

	if len(want) < 29 {
		t.Fatalf("enumerated only %d flags on products create-bookable, want the full 29 — "+
			"the walker lost coverage rather than the command shrinking", len(want))
	}

	_, body, hit, err := capture(t, append([]string{"products", "create-bookable"}, args...)...)
	if !hit {
		t.Fatalf("no request reached the server: %v", err)
	}
	for _, name := range want {
		key := strings.ReplaceAll(name, "-", "_")
		if _, present := body[key]; !present {
			t.Errorf("--%s was declared and accepted but never reached the wire as %q "+
				"(snakeFieldMap's name list is missing it); body keys: %d", name, key, len(body))
		}
	}
}

// TestWire_BookingsCreateAcceptsNumericStrings is the regression test for a
// gate that was stricter than the contract it mirrors.
//
// StoreBookingRequest: "Numeric fields validate as `numeric` BEFORE `integer` …
// A numeric string (`"2"`) is still accepted." The first version of these gates
// decoded straight into an int, which got the boolean half right and the quoted
// half backwards — and the quoted half is the one a caller hits, because this
// API hands ids back as STRINGS (`Extra.id` and `PricingCategory.id` are both
// `{type: string}`). So `extras list` → `bookings create --extras
// '[{"extraId":"7",…}]'` was refused locally for a payload the server accepts.
//
// Value: protects=a quoted number is accepted and reaches the wire wherever the spec accepts one, while a boolean or fractional value is still refused; fails_when=the gates go back to json.Unmarshal into int, or decodeNumericInt stops refusing booleans; why_new=the read->write composition was a local dead end and no test covered the quoted form; seam=testNewRunner
func TestWire_BookingsCreateAcceptsNumericStrings(t *testing.T) {
	t.Run("a quoted guest count reaches the wire", func(t *testing.T) {
		_, body, hit, err := capture(t, createGuestsArgs(`{"30":"2"}`)...)
		if !hit {
			t.Fatalf("a quoted guest count was refused locally: %v", err)
		}
		guests, ok := body["guests"].(map[string]any)
		if !ok || guests["30"] != "2" {
			t.Errorf("guests = %#v, want the operator's bytes verbatim — the gate validates, "+
				"it must not rewrite the payload", body["guests"])
		}
	})

	t.Run("a quoted extraId reaches the wire", func(t *testing.T) {
		// The exact composition: `extras list` returns id as a string.
		_, body, hit, err := capture(t, createArgs("--extras", `[{"extraId":"7","quantity":"2"}]`)...)
		if !hit {
			t.Fatalf("a quoted extraId was refused locally: %v", err)
		}
		rows, ok := body["extras"].([]any)
		if !ok || len(rows) != 1 {
			t.Fatalf("extras = %#v, want a one-element array", body["extras"])
		}
		row, _ := rows[0].(map[string]any)
		if row["extraId"] != "7" || row["quantity"] != "2" {
			t.Errorf("extras[0] = %v, want the quoted values verbatim", row)
		}
	})

	t.Run("a quoted deposit reaches the wire", func(t *testing.T) {
		_, body, hit, err := capture(t, createArgs("--payment-method", "cash",
			"--data", `{"deposit":"2000","currency":"EUR"}`)...)
		if !hit {
			t.Fatalf("a quoted deposit was refused locally: %v", err)
		}
		if body["deposit"] != "2000" {
			t.Errorf("deposit = %#v, want \"2000\"", body["deposit"])
		}
	})

	// Integral values the server takes, whatever their spelling. A serializer
	// that produced a count from a float emits `2.0`; refusing it was the same
	// stricter-than-the-wire mistake as refusing `"2"`.
	t.Run("an integral float or exponent is accepted", func(t *testing.T) {
		for _, tc := range []struct{ name, guests string }{
			{"integral float", `{"30":2.0}`},
			{"exponent", `{"30":1e3}`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, _, hit, err := capture(t, createGuestsArgs(tc.guests)...)
				if !hit {
					t.Errorf("--guests %s was refused locally, but its VALUE is a whole number "+
						"and Laravel's integer rule (filter_var) takes it: %v", tc.guests, err)
				}
			})
		}
	})

	// The width guard belongs to float64, not to int32.
	//
	// `integralNumber`'s float fallback used to refuse anything over
	// math.MaxInt32, claiming that was where float64 stops representing
	// integers exactly. It is not — float64 is exact to 2^53 — so ids between
	// 2^31 and 2^53 were refused locally whenever they arrived spelled as a
	// float, which is the same stricter-than-the-wire mistake this whole test
	// exists to pin, one spelling deeper. The spec has no 32-bit ceiling to
	// appeal to either: `PositiveIntegerId` is `{type: integer, minimum: 1}`
	// with no `maximum` and no `format`.
	//
	// The plain-integer path is checked alongside it because it never had the
	// int32 limit, and a future "simplification" that routes both spellings
	// through one bound should fail here rather than quietly narrow it.
	t.Run("a wide id is accepted in either spelling", func(t *testing.T) {
		for _, tc := range []struct{ name, extras string }{
			// 3000000000 > math.MaxInt32 (2147483647), well inside float64's
			// exact range and inside a 64-bit int.
			{"plain integer above MaxInt32", `[{"extraId":3000000000,"quantity":1}]`},
			{"float-spelled above MaxInt32", `[{"extraId":3000000000.0,"quantity":1}]`},
			{"exponent above MaxInt32", `[{"extraId":3e9,"quantity":1}]`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, _, hit, err := capture(t, createArgs("--extras", tc.extras)...)
				if !hit {
					t.Errorf("--extras %s was refused locally, but the value is a whole number "+
						"the spec places no upper bound on: %v", tc.extras, err)
				}
			})
		}
	})

	// And the guard that remains must still bite: past 2^53 a float64 cannot
	// be trusted to be the integer the caller wrote, so converting it would
	// invent a different id. Refusing is correct there.
	t.Run("a float beyond float64's exact integer range is refused", func(t *testing.T) {
		// 1e19 also exceeds int64, so this is refused on both counts.
		_, body, hit, err := capture(t, createArgs("--extras", `[{"extraId":1e19,"quantity":1}]`)...)
		if err == nil {
			t.Fatalf("--extras with a 1e19 id was accepted: %v", body)
		}
		if hit {
			t.Errorf("--extras with a 1e19 id reached the server: %v", body)
		}
	})

	// The other half of the rule must still hold, or the fix is just a hole.
	t.Run("a boolean or fractional value is still refused", func(t *testing.T) {
		for _, tc := range []struct{ name, guests, want string }{
			{"boolean", `{"3":true}`, "whole number of guests"},
			{"fractional", `{"3":2.5}`, "whole number of guests"},
			{"non-numeric string", `{"3":"two"}`, "whole number of guests"},
			{"null", `{"3":null}`, "whole number of guests"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, body, hit, err := capture(t, createGuestsArgs(tc.guests)...)
				if err == nil {
					t.Fatalf("--guests %s was accepted: %v", tc.guests, body)
				}
				if hit {
					t.Errorf("--guests %s reached the server: %v", tc.guests, body)
				}
				if !strings.Contains(err.Error(), tc.want) {
					t.Errorf("--guests %s: error = %v, want %q", tc.guests, err, tc.want)
				}
			})
		}
	})
}

// TestWire_ReservationPaymentRefusalIsDeterministic is the regression test for a
// refusal whose text was chosen by Go's randomised map order.
//
// Value: protects=a reservation carrying several payment fields names every one of them, in a stable order; fails_when=the ordered slice goes back to a map literal, or the loop returns on the first match; why_new=the message was nondeterministic, so an operator fixed one flag and was refused for the next, and any assertion on it was flaky; seam=testNewRunner
func TestWire_ReservationPaymentRefusalIsDeterministic(t *testing.T) {
	args := reservationArgs("--expiry", "1800", "--deposit", "2000",
		"--currency", "EUR", "--payment-method", "cash", "--payment-notes", "at the desk")

	var first string
	for i := 0; i < 8; i++ {
		_, body, hit, err := capture(t, args...)
		if err == nil {
			t.Fatalf("a reservation carrying payment fields was accepted: %v", body)
		}
		if hit {
			t.Errorf("the refusal still reached the server with body %v", body)
		}
		// Every offending flag, not whichever the map yielded first.
		for _, want := range []string{"--deposit", "--payment-method", "--payment-notes"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("refusal does not name %s: %v", want, err)
			}
		}
		if i == 0 {
			first = err.Error()
			continue
		}
		if err.Error() != first {
			t.Fatalf("the refusal text changed between runs, so it is still map-ordered:\n 1: %s\n %d: %s",
				first, i+1, err.Error())
		}
	}
}

// TestWire_ReplacedResourceGateReadsTheAssembledBody closes the --data bypass on
// the one gate in this file that was keyed on the flags.
//
// Value: protects=the replaced/main pairing rule applies to a --data payload exactly as to the typed flags; fails_when=the gate goes back to args.FlagSet, which --data bypasses; why_new=every sibling gate reads the assembled body and this one did not, which is the bypass their shared header calls out; seam=testNewRunner
func TestWire_ReplacedResourceGateReadsTheAssembledBody(t *testing.T) {
	_, body, hit, err := capture(t, "bookings", "set-resources", "bk_42",
		"--expected-resource-state-token", "tok",
		"--data", `{"replaced_resource_id":7}`)
	if err == nil {
		t.Fatalf("--data carrying a lone replaced_resource_id was accepted: %v", body)
	}
	if hit {
		t.Errorf("the refusal still reached the server with body %v", body)
	}
	if !strings.Contains(err.Error(), "REPLACED_RESOURCE_WITHOUT_MAIN") {
		t.Errorf("error = %v, want the server's own code", err)
	}

	// And the legitimate pair still goes through, whichever input supplies it.
	_, body, hit, err = capture(t, "bookings", "set-resources", "bk_42",
		"--expected-resource-state-token", "tok",
		"--data", `{"replaced_resource_id":7,"main_resource_id":9}`)
	if !hit {
		t.Fatalf("a complete pair supplied through --data was refused: %v", err)
	}
	if body["replaced_resource_id"] != float64(7) || body["main_resource_id"] != float64(9) {
		t.Errorf("want main 9 replacing 7, got %v", body)
	}
}

// TestWire_BookingsCreatePayloadLevelRefusals covers the three refusals the spec
// lists as "payload-level ... that no single field rule can express" and that
// are decidable from the body alone.
//
// Each is worth catching at the edge for a reason the spec states: an all-zero
// mix is reported by the booking engine "with a complaint about `fare` — a
// parameter you never sent"; a deposit with no method records a CARD payment
// that was never taken, because the engine defaults an absent method to card;
// and a payment note with no deposit has no transaction to be written onto.
//
// Value: protects=the three body-decidable payload-level refusals are named locally before any request; fails_when=any of the three gates is dropped, or the all-zero check starts counting a category of 0 as a booking; why_new=the spec lists six such refusals and the CLI gated none of the three it can decide; seam=testNewRunner
func TestWire_BookingsCreatePayloadLevelRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"a mix of all zeroes books nobody", createGuestsArgs(`{"30":0,"31":0}`), "books nobody"},
		{"one category at zero is the same", createGuestsArgs(`{"30":0}`), "books nobody"},
		{"a deposit with no payment method", createArgs("--deposit", "2000", "--currency", "EUR"), "needs --payment-method"},
		{"a payment note with no deposit", createArgs("--payment-notes", "at the desk"), "needs a --deposit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, body, hit, err := capture(t, tc.args...)
			if err == nil {
				t.Fatalf("accepted: %v", body)
			}
			if hit {
				t.Errorf("the refusal still reached the server with body %v", body)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}

	// The legitimate shapes must still pass, or these are bans.
	t.Run("a non-zero category among zeroes is a real booking", func(t *testing.T) {
		if _, _, hit, err := capture(t, createGuestsArgs(`{"30":0,"31":2}`)...); !hit {
			t.Errorf("a mix with one real guest was refused: %v", err)
		}
	})
	t.Run("a deposit WITH a payment method and notes goes through", func(t *testing.T) {
		if _, _, hit, err := capture(t, createArgs("--deposit", "2000", "--currency", "EUR",
			"--payment-method", "cash", "--payment-notes", "at the desk")...); !hit {
			t.Errorf("a complete cash deposit was refused: %v", err)
		}
	})
	t.Run("a zero deposit needs no method", func(t *testing.T) {
		if _, _, hit, err := capture(t, createArgs("--deposit", "0")...); !hit {
			t.Errorf("--deposit 0 records no money and should need no method: %v", err)
		}
	})
}

// TestWire_MainResourceBoundReadsTheAssembledBody is the test the gate it covers
// never had.
//
// The bound moved onto the assembled body precisely because
// `--data '{"main_resource_id":0}'` bypassed the flag-keyed version — and no
// test exercised that input, so the gate could have reverted to args.FlagSet
// with the whole suite green. The existing coverage only passes
// `--main-resource-id=0`, which the flag-keyed version caught too.
//
// Value: protects=the main-resource lower bound applies to a --data payload, not just the typed flag; fails_when=the check goes back to args.FlagSet, which --data bypasses; why_new=the fix's own motivating input had no test and the pre-fix code passed the one test there was; seam=testNewRunner
func TestWire_MainResourceBoundReadsTheAssembledBody(t *testing.T) {
	// The motivating input: 0 arrives only through --data.
	_, body, hit, err := capture(t, "bookings", "set-resources", "bk_42",
		"--expected-resource-state-token", "tok", "--data", `{"main_resource_id":0}`)
	if err == nil {
		t.Fatalf("--data carrying main_resource_id 0 was accepted: %v", body)
	}
	if hit {
		t.Errorf("the refusal still reached the server with body %v", body)
	}
	if !strings.Contains(err.Error(), "cannot be cleared, only replaced") {
		t.Errorf("error = %v, want the sentence naming the real constraint rather than a bare bound", err)
	}

	// The typed flag keeps its own refusal.
	_, body, hit, err = capture(t, "bookings", "set-resources", "bk_42",
		"--expected-resource-state-token", "tok", "--main-resource-id=0")
	if err == nil {
		t.Fatalf("--main-resource-id=0 was accepted: %v", body)
	}
	if hit {
		t.Errorf("the flag refusal reached the server with body %v", body)
	}

	// And a legitimate value still goes through from either input.
	_, body, hit, err = capture(t, "bookings", "set-resources", "bk_42",
		"--expected-resource-state-token", "tok", "--data", `{"main_resource_id":9}`)
	if !hit {
		t.Fatalf("a valid main_resource_id supplied through --data was refused: %v", err)
	}
	if body["main_resource_id"] != float64(9) {
		t.Errorf("main_resource_id = %#v, want 9", body["main_resource_id"])
	}
}

// TestWire_DepositNullPaymentMethodIsRefused pins the null half of the deposit
// gate: `payment_method: null` leaves the engine on its `card` default exactly
// as a missing key does, which is the state the gate exists to prevent.
//
// Value: protects=an explicit null payment_method is treated as absent beside a non-zero deposit; fails_when=the gate goes back to testing key presence, which a typed-null payload satisfies; why_new=the gate tested presence only and the spec types the field [string, "null"]; seam=testNewRunner
func TestWire_DepositNullPaymentMethodIsRefused(t *testing.T) {
	_, body, hit, err := capture(t, createArgs("--currency", "EUR",
		"--data", `{"deposit":2000,"payment_method":null}`)...)
	if err == nil {
		t.Fatalf("a null payment_method beside a deposit was accepted: %v", body)
	}
	if hit {
		t.Errorf("the refusal still reached the server with body %v", body)
	}
	if !strings.Contains(err.Error(), "needs --payment-method") {
		t.Errorf("error = %v, want the deposit/payment-method refusal", err)
	}
}
