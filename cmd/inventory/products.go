package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/captainbook/captainbook-cli/internal/inventory/gen"
)

// productsDefs declares the products resource: list, get, create,
// create-bookable, update, duplicate, delete, restore.
//
// create vs create-bookable: a bare `POST /products` makes a product nothing
// can sell, because departures, prices and questions all hang off a product
// OPTION and a priced category needs a tier. 1.44.0 added
// `POST /products/bookable`, which creates the product, its options and its
// priced categories in one transaction, and the spec says to prefer it for any
// new product. Both are bound: the plain create is still the right call for a
// shell an operator will fill in from the dashboard.
//
// Multipart media upload + media list/delete live in media.go since they're a
// separate sub-tree (`ceebee inventory media …`).
//
// Per spec: deleteProduct has NO dry-run support (D32: NotSupported).
// Update + create + restore use body-level dry_run (D24).
//
// Tuned diff renderer: "Product" — RenderProductDiff is invoked when
// a successful dry-run returns a MutationResult diff envelope.
func productsDefs() []CommandDef {
	return []CommandDef{
		{
			Use: "products list", Short: "List products", Kind: KindRead,
			Verb: "GET", Path: "/products", Ability: invpkg.Read,
			Flags: []FlagDef{
				{Name: "limit", Type: "int", Min: 1, Max: 200, Description: "Page size (1-200, default 50)"},
				{Name: "cursor", Type: "string", Description: "Pagination cursor"},
				{Name: "q", Type: "string", Description: "Free-text search"},
				{Name: "category", Type: "int", Min: 1, Description: "Filter by category ID (a ProductCategory id; 0 is refused rather than ignored)"},
				// Two-state on purpose: `status` filters the `is_active`
				// boolean (published → true, draft → false) and the server
				// validates it with `in:published,draft`. There is no
				// archived state; the spec used to advertise one, which made
				// `--status archived` sail past the client-side enum gate to
				// earn a server 422 a round trip later
				// (captainbook/captainbook#8111, captainbook-cli#18). Now
				// that the spec is fixed, the gate rejects it locally.
				{Name: "status", Type: "string", Description: "draft|published"},
				{Name: "include-trashed", Type: "bool", Description: "Include soft-deleted"},
				{Name: "since", Type: "string", Description: "ISO 8601 lower-bound on updated_at"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				params := &gen.ListProductsParams{}
				if v := args.FlagInt("limit"); v != 0 {
					params.Limit = &v
				}
				if v := args.FlagString("cursor"); v != "" {
					params.Cursor = &v
				}
				if v := args.FlagString("q"); v != "" {
					params.Q = &v
				}
				if args.FlagSet("category") {
					v := args.FlagInt("category")
					params.Category = &v
				}
				if v := args.FlagString("status"); v != "" {
					st := gen.ListProductsParamsStatus(v)
					params.Status = &st
				}
				if args.FlagBool("include-trashed") {
					t := true
					params.IncludeTrashed = &t
				}
				if v := args.FlagString("since"); v != "" {
					t, err := time.Parse(time.RFC3339, v)
					if err != nil {
						return nil, fmt.Errorf("--since: invalid RFC3339 timestamp: %w", err)
					}
					params.Since = &t
				}
				resp, err := r.Client.ListProductsWithResponse(ctx, params)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Product", "")
			},
		},
		{
			Use: "products get <id>", Short: "Show one product", Kind: KindRead,
			Verb: "GET", Path: "/products/{id}", Ability: invpkg.Read,
			PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.ShowProductWithResponse(ctx, id)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Product", id)
			},
		},
		{
			Use: "products create", Short: "Create a product", Kind: KindMutation,
			Verb: "POST", Path: "/products", Ability: invpkg.Write,
			DryRunMode: DryRunBody,
			Long: "Create a product. Private vs shared is controlled by --is-private: " +
				"private (true) means a single party books the whole experience; shared " +
				"(false, default) means multiple parties share the slot. When --is-private=false " +
				"the server forces is_priced_per_person=true and use_alternate_tier_pricing=false. " +
				"--must-validate-cancellation-policy=false (default) silently nulls both " +
				"--cancellation-policy and --cancellation-policy-link — set it to true to retain the policy. " +
				"--product-code is auto-generated from the title slug when omitted.",
			Flags: []FlagDef{
				{Name: "title", Type: "string", Required: true, Description: "Product title"},
				{Name: "currency", Type: "string", Required: true, Description: "ISO currency code — must equal the account currency from whoami; not stored, but a mismatch is refused with 422"},
				{Name: "description", Type: "string", Description: "Product description (translatable rich text)"},
				{Name: "instructions", Type: "string", Description: "Rich text shown to confirmed customers"},
				{Name: "requirements", Type: "string", Description: "Rich text — what the customer needs to bring/know"},
				{Name: "inclusions", Type: "string", Description: "Rich text — what's included"},
				{Name: "exclusions", Type: "string", Description: "Rich text — what's not included"},
				{Name: "product-code", Type: "string", Description: "Tenant SKU (auto-gen from title when omitted)"},
				{Name: "status", Type: "string", Description: "draft|published"},
				{Name: "schedule-type", Type: "string", Description: "date|datetime"},
				{Name: "delivery-method", Type: "string", Description: "TICKET|VOUCHER — \"Ticket type\". VOUCHER (the default) = one ticket per booking; TICKET = one per guest. Asking for TICKET needs the update_delivery_method permission."},
				{Name: "redemption-method", Type: "string", Description: "MANIFEST|DIGITAL|PRINT — how the customer is admitted. MANIFEST (the default) = no identification needed. Anything else needs the update_delivery_method permission."},
				{Name: "capacity", Type: "int", Description: "Default capacity"},
				{Name: "from-price", Type: "int", Description: "Starting price (minor units)"},
				{Name: "from-price-label", Type: "string", Description: "Caption next to from-price (e.g. \"From €50/person\")"},
				{Name: "category-ids", Type: "intSlice", Description: "Comma-separated category IDs (integer)"},
				{Name: "timezone", Type: "string", Description: "IANA timezone"},
				{Name: "locale", Type: "string", Description: "Default content locale (e.g. en, fr)"},
				{Name: "is-private", Type: "bool", Description: "Private (one party books the whole slot) vs shared (multiple parties)"},
				{Name: "is-priced-per-person", Type: "bool", Default: true, Description: "Price per traveler (true) vs per group/booking (false)"},
				{Name: "is-tier-priced", Type: "bool", Description: "Quantity-based pricing tiers apply"},
				{Name: "use-alternate-tier-pricing", Type: "bool", Description: "Alternate tier-pricing rendering"},
				{Name: "show-last-tier", Type: "bool", Description: "Show the last tier in customer-facing pricing"},
				{Name: "displayable", Type: "bool", Default: true, Description: "Show in widgets / catalog"},
				{Name: "must-validate-cancellation-policy", Type: "bool", Description: "Customer must accept the cancellation policy at checkout"},
				{Name: "cancellation-policy", Type: "string", Description: "Inline policy text (mutually exclusive with --cancellation-policy-link)"},
				{Name: "cancellation-policy-link", Type: "string", Description: "External policy URL (mutually exclusive with --cancellation-policy)"},
			},
			ForensicFields: []string{"from-price", "capacity", "status", "schedule-type", "is-private", "is-priced-per-person", "delivery-method", "redemption-method"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"title":                             "title",
					"currency":                          "currency",
					"description":                       "description",
					"instructions":                      "instructions",
					"requirements":                      "requirements",
					"inclusions":                        "inclusions",
					"exclusions":                        "exclusions",
					"product-code":                      "product_code",
					"status":                            "status",
					"schedule-type":                     "schedule_type",
					"delivery-method":                   "delivery_method",
					"redemption-method":                 "redemption_method",
					"capacity":                          "capacity",
					"from-price":                        "from_price",
					"from-price-label":                  "from_price_label",
					"category-ids":                      "category_ids",
					"timezone":                          "timezone",
					"locale":                            "locale",
					"is-private":                        "is_private",
					"is-priced-per-person":              "is_priced_per_person",
					"is-tier-priced":                    "is_tier_priced",
					"use-alternate-tier-pricing":        "use_alternate_tier_pricing",
					"show-last-tier":                    "show_last_tier",
					"displayable":                       "displayable",
					"must-validate-cancellation-policy": "must_validate_cancellation_policy",
					"cancellation-policy":               "cancellation_policy",
					"cancellation-policy-link":          "cancellation_policy_link",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.CreateProductWithBodyWithResponse(ctx, &gen.CreateProductParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Product", "")
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
		{
			Use: "products create-bookable", Short: "Create a product that can actually be sold",
			Kind: KindMutation, Verb: "POST", Path: "/products/bookable",
			Ability: invpkg.Write, DryRunMode: DryRunBody,
			Long: "Creates a product, at least one product OPTION and at least one PRICED " +
				"pricing category in ONE call and one transaction.\n\n" +
				"PREFER THIS OVER products create FOR ANY NEW PRODUCT. A product on its own " +
				"cannot be sold: departures, prices and questions all hang off a product " +
				"option, and a non-internal pricing category with no tier is filtered out of " +
				"the booking widget — so a plain create followed by nothing leaves a product " +
				"no customer can reach.\n\n" +
				"Takes every flag products create takes, under the same rules (the implicit " +
				"overrides, the update_delivery_method permission for ticketing, the " +
				"65,535-BYTE text cap), plus --options and --pricing-categories. --status " +
				"defaults to draft exactly as products create does, because publishing a " +
				"product with no photo and no departures would put an empty page on the " +
				"widget.\n\n" +
				"BOTH ITEM SHAPES ARE CLOSED: an unlisted key is a 422 naming it rather than " +
				"being accepted and dropped. The option key is title (the column is name) and " +
				"the category key is amount; those are the two spellings callers get wrong, " +
				"and silently discarding either produces a product priced at nothing under a " +
				"201. Every category is created with is_internal false; the key is not " +
				"accepted.\n\n" +
				"WHAT IT DOES NOT DO: departures and the main photo. Departures need a " +
				"schedule only the operator can state and belong to availabilities create or " +
				"the rule/bulk commands; a photo is an upload. Both therefore stay in " +
				"readiness.blocking on the response, which is the field to read and report " +
				"rather than announcing a finished product.\n\n" +
				"There is deliberately no --allocation-mode here, and that is the server's " +
				"rule rather than an omission: a spanning mode needs a fleet to span, this " +
				"endpoint creates the schedules but never the resources, so it REFUSES the " +
				"field with a 422 instead of dropping it. Create the product, create its " +
				"schedules, attach two or more assets with a capacity each, then run products " +
				"update --allocation-mode.\n\n" +
				"Plan limits and permissions are checked exactly as on products create — " +
				"entitlement before permission, and both before the dry run — so a preview " +
				"cannot answer would_apply true for a create the commit would refuse.",
			Flags: []FlagDef{
				{Name: "title", Type: "string", Required: true, Description: "Product title"},
				{Name: "currency", Type: "string", Required: true, Description: "ISO currency code — must equal the account currency from whoami; not stored, but a mismatch is refused with 422"},
				{Name: "description", Type: "string", Description: "Product description (translatable rich text)"},
				{Name: "instructions", Type: "string", Description: "Rich text shown to confirmed customers"},
				{Name: "requirements", Type: "string", Description: "Rich text — what the customer needs to bring/know"},
				{Name: "inclusions", Type: "string", Description: "Rich text — what's included"},
				{Name: "exclusions", Type: "string", Description: "Rich text — what's not included"},
				{Name: "product-code", Type: "string", Description: "Tenant SKU (auto-gen from title when omitted)"},
				{Name: "status", Type: "string", Description: "draft|published"},
				{Name: "schedule-type", Type: "string", Description: "date|datetime"},
				{Name: "delivery-method", Type: "string", Description: "TICKET|VOUCHER — \"Ticket type\". VOUCHER (the default) = one ticket per booking; TICKET = one per guest. Asking for TICKET needs the update_delivery_method permission."},
				{Name: "redemption-method", Type: "string", Description: "MANIFEST|DIGITAL|PRINT — how the customer is admitted. MANIFEST (the default) = no identification needed. Anything else needs the update_delivery_method permission."},
				{Name: "capacity", Type: "int", Description: "Default capacity"},
				{Name: "from-price", Type: "int", Description: "Starting price (minor units)"},
				{Name: "from-price-label", Type: "string", Description: "Caption next to from-price (e.g. \"From €50/person\")"},
				{Name: "category-ids", Type: "intSlice", Description: "Comma-separated category IDs (integer)"},
				{Name: "timezone", Type: "string", Description: "IANA timezone"},
				{Name: "locale", Type: "string", Description: "Default content locale (e.g. en, fr)"},
				{Name: "is-private", Type: "bool", Description: "Private (one party books the whole slot) vs shared (multiple parties)"},
				{Name: "is-priced-per-person", Type: "bool", Default: true, Description: "Price per traveler (true) vs per group/booking (false)"},
				{Name: "is-tier-priced", Type: "bool", Description: "Quantity-based pricing tiers apply"},
				{Name: "use-alternate-tier-pricing", Type: "bool", Description: "Alternate tier-pricing rendering"},
				{Name: "show-last-tier", Type: "bool", Description: "Show the last tier in customer-facing pricing"},
				{Name: "displayable", Type: "bool", Default: true, Description: "Show in widgets / catalog"},
				{Name: "must-validate-cancellation-policy", Type: "bool", Description: "Customer must accept the cancellation policy at checkout"},
				{Name: "cancellation-policy", Type: "string", Description: "Inline policy text (mutually exclusive with --cancellation-policy-link)"},
				{Name: "cancellation-policy-link", Type: "string", Description: "External policy URL (mutually exclusive with --cancellation-policy)"},
				// The two lists that make the product SELLABLE, and the whole
				// reason this endpoint exists beside `products create`. Both
				// are required with at least one entry: a product with neither
				// is the shell the plain create produces, and accepting empty
				// lists would rebuild it behind a different name.
				{Name: "options", Type: "json", Required: true, Description: `Bookable variants (1-20) as JSON or @file.json: [{"title": "10:00 departure", "capacity": 12, "min_age": null, "max_age": null}, ...]. title is required and lands in product_options.name. Departures, prices and questions hang off these, not off the product`},
				{Name: "pricing-categories", Type: "json", Required: true, Description: `Price buckets (1-20) as JSON or @file.json: [{"name": "Adults", "amount": 5000, "type": "ADULT"}, ...]. name and amount (MINOR units) are required; type is one of ADULT|CHILD|INFANT|YOUTH|STUDENT|SENIOR|TRAVELLER|EU_CITIZEN|MILITARY|EU_CITIZEN_STUDENT. Each also creates one open-ended tier (min 1, max null) at amount, because a category with no tier is invisible to the booking widget`},
			},
			// invariant: the two lists ARE the sellable product — the options
			// departures hang off and the prices customers pay. The audit keeps
			// only body_sha256, so without them a row records that a product was
			// created and nothing about what it sells or costs.
			ForensicFields: []string{"from-price", "capacity", "status", "schedule-type", "is-private", "is-priced-per-person", "delivery-method", "redemption-method", "options", "pricing-categories"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				// DERIVED, not duplicated. Every key this request takes is its
				// flag name in snake_case, including the two that make the
				// endpoint what it is (`options`, `pricing_categories`), so a
				// hand-written literal here would restate 29 pairs that a rule
				// already determines — and a restated pair is the only kind that
				// can be typo'd. `products create` keeps its own literal, so the
				// shared keys stay pinned against the spec from there, and the
				// spec guarantees the two cannot drift
				// (CreateBookableProductRequest spreads CreateProductRequest's
				// rules() in PHP).
				//
				// The flag list above stays a literal: the AST walker reads
				// `Flags:` only as a composite literal, and the coverage test
				// unions the LIVE tree's flags, so reachability is pinned either
				// way.
				body, err := JSONBodyFromArgs(args, args.DryRun, snakeFieldMap(
					"title", "currency", "description", "instructions", "requirements",
					"inclusions", "exclusions", "product-code", "status", "schedule-type",
					"delivery-method", "redemption-method", "capacity", "from-price",
					"from-price-label", "category-ids", "timezone", "locale", "is-private",
					"is-priced-per-person", "is-tier-priced", "use-alternate-tier-pricing",
					"show-last-tier", "displayable", "must-validate-cancellation-policy",
					"cancellation-policy", "cancellation-policy-link",
					"options", "pricing-categories",
				))
				if err != nil {
					return nil, err
				}
				// Validated after assembly so --data is held to the same rules as
				// the flags: JSONBodyFromArgs copies a json-typed flag verbatim
				// and would otherwise send either list unchecked.
				if err := validateBookableProductBody(body); err != nil {
					return nil, err
				}
				resp, err := r.Client.CreateBookableProductWithBodyWithResponse(ctx, &gen.CreateBookableProductParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, perr := ParseGenResponse(resp.Body, resp.HTTPResponse, "Product", "")
				if res != nil {
					res.WireBody = body
				}
				return res, perr
			},
		},
		{
			Use: "products update <id>", Short: "Update a product", Kind: KindMutation,
			Verb: "PATCH", Path: "/products/{id}", Ability: invpkg.Write,
			DryRunMode:     DryRunBody,
			PositionalArgs: []string{"id"},
			Long: "Update a product. Switching --is-private cascades a heavy inventory recompute " +
				"(7 jobs per 1000 availabilities); under --dry-run those side effects appear in " +
				"MutationResult.side_effects. Switching --schedule-type=date collapses Availability " +
				"windows to full-day spans and deletes resourceables. Same implicit overrides as " +
				"create: --must-validate-cancellation-policy=false nulls both policy fields; " +
				"--is-private=false forces is_priced_per_person=true and use_alternate_tier_pricing=false.\n\n" +
				"CHANGING --delivery-method reissues the tickets of every booking departing in " +
				"the next 10 years: the QR codes those customers are already holding stop " +
				"scanning, and NOTHING notifies them — the operator has to resend. So the " +
				"change is refused with TICKET_REISSUE_NOT_CONFIRMED unless you also pass " +
				"--confirm-ticket-reissue. Run it under --dry-run first: that is never " +
				"refused and it reports ticket_reissue.affected_bookings, which is how you " +
				"find out the blast radius before deciding. The refusal is raised before the " +
				"write, so it releases the idempotency key — the retry carrying the " +
				"confirmation may reuse it.\n\n" +
				"Changing --delivery-method or --redemption-method also requires the " +
				"update_delivery_method permission. Sending back the value the product " +
				"already has is not a change: no permission needed, nothing reissued, so an " +
				"ordinary read-modify-write round trip stays safe without it.",
			Flags: []FlagDef{
				{Name: "title", Type: "string", Description: "Product title"},
				{Name: "description", Type: "string", Description: "Product description (translatable rich text)"},
				{Name: "instructions", Type: "string", Description: "Rich text shown to confirmed customers"},
				{Name: "requirements", Type: "string", Description: "Rich text — what the customer needs to bring/know"},
				{Name: "inclusions", Type: "string", Description: "Rich text — what's included"},
				{Name: "exclusions", Type: "string", Description: "Rich text — what's not included"},
				{Name: "product-code", Type: "string", Description: "Tenant SKU"},
				{Name: "status", Type: "string", Description: "draft|published"},
				{Name: "schedule-type", Type: "string", Description: "date|datetime"},
				{Name: "delivery-method", Type: "string", Description: "TICKET|VOUCHER — \"Ticket type\". CHANGING it reissues existing tickets and needs --confirm-ticket-reissue plus the update_delivery_method permission. Sending the stored value is not a change."},
				{Name: "redemption-method", Type: "string", Description: "MANIFEST|DIGITAL|PRINT — how the customer is admitted. Changing it needs the update_delivery_method permission but reissues nothing."},
				{Name: "confirm-ticket-reissue", Type: "bool", Description: "Acknowledge that changing --delivery-method invalidates the tickets customers already hold, un-notified. Nothing is stored. Ignored when --delivery-method is absent or unchanged, and never needed under --dry-run."},
				// PATCH only. `POST /products` and `POST /products/bookable`
				// REFUSE the field with a 422 (1.53.0), so there is deliberately
				// no --allocation-mode on either create command: offering one
				// would advertise a value every create answers 422 for.
				{Name: "allocation-mode", Type: "string", Description: "single_resource|homogeneous_multi — how many resources one booking may occupy. Needs a schedule carrying two or more capacity-bearing assets; homogeneous_multi is refused with a 422 naming the gate that refused it. Echoing the stored value runs no gate."},
				{Name: "capacity", Type: "int", Description: "Default capacity"},
				{Name: "from-price", Type: "int", Description: "Starting price (minor units)"},
				{Name: "from-price-label", Type: "string", Description: "Caption next to from-price"},
				{Name: "category-ids", Type: "intSlice", Description: "Comma-separated category IDs (integer)"},
				{Name: "timezone", Type: "string", Description: "IANA timezone"},
				{Name: "locale", Type: "string", Description: "Default content locale"},
				{Name: "is-private", Type: "bool", Description: "Private (one party / whole slot) vs shared — toggling cascades inventory jobs"},
				{Name: "is-priced-per-person", Type: "bool", Description: "Price per traveler vs per group"},
				{Name: "is-tier-priced", Type: "bool", Description: "Quantity-based pricing tiers apply"},
				{Name: "use-alternate-tier-pricing", Type: "bool", Description: "Alternate tier-pricing rendering"},
				{Name: "show-last-tier", Type: "bool", Description: "Show the last tier in customer-facing pricing"},
				{Name: "displayable", Type: "bool", Description: "Show in widgets / catalog"},
				{Name: "must-validate-cancellation-policy", Type: "bool", Description: "Customer must accept the cancellation policy at checkout"},
				{Name: "cancellation-policy", Type: "string", Description: "Inline policy text"},
				{Name: "cancellation-policy-link", Type: "string", Description: "External policy URL"},
			},
			// allocation-mode is forensic: changing the EFFECTIVE mode
			// republishes the product's capacity to every connected channel,
			// and the figure a channel then advertises is chosen by the mode.
			// A capacity edit does the same, which is why it was already here.
			ForensicFields: []string{"from-price", "capacity", "status", "schedule-type", "is-private", "is-priced-per-person", "delivery-method", "redemption-method", "confirm-ticket-reissue", "allocation-mode"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"title":                             "title",
					"description":                       "description",
					"instructions":                      "instructions",
					"requirements":                      "requirements",
					"inclusions":                        "inclusions",
					"exclusions":                        "exclusions",
					"product-code":                      "product_code",
					"status":                            "status",
					"schedule-type":                     "schedule_type",
					"delivery-method":                   "delivery_method",
					"redemption-method":                 "redemption_method",
					"confirm-ticket-reissue":            "confirm_ticket_reissue",
					"allocation-mode":                   "allocation_mode",
					"capacity":                          "capacity",
					"from-price":                        "from_price",
					"from-price-label":                  "from_price_label",
					"category-ids":                      "category_ids",
					"timezone":                          "timezone",
					"locale":                            "locale",
					"is-private":                        "is_private",
					"is-priced-per-person":              "is_priced_per_person",
					"is-tier-priced":                    "is_tier_priced",
					"use-alternate-tier-pricing":        "use_alternate_tier_pricing",
					"show-last-tier":                    "show_last_tier",
					"displayable":                       "displayable",
					"must-validate-cancellation-policy": "must_validate_cancellation_policy",
					"cancellation-policy":               "cancellation_policy",
					"cancellation-policy-link":          "cancellation_policy_link",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.UpdateProductWithBodyWithResponse(ctx, id, &gen.UpdateProductParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Product", id)
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
		{
			Use: "products duplicate <id>", Short: "Duplicate a product and everything under it",
			Kind: KindMutation, Verb: "POST", Path: "/products/{id}/duplicate",
			Ability: invpkg.Write, DryRunMode: DryRunBody, PositionalArgs: []string{"id"},
			Long: "Copies one product. --include selects which related collections come " +
				"with it; omit it for the server's default set. Use --dry-run first: the " +
				"blast radius of this command is whatever --include names, and the audit " +
				"records only that list, not the resulting rows.",
			Flags: []FlagDef{
				{Name: "title", Type: "string", Description: "Title for the copy (defaults to the source title with a suffix)"},
				{Name: "product-code", Type: "string", Description: "Product code for the copy"},
				// All twelve spec sections, in the spec's own order. The earlier
				// list named only eight, and because `include` REPLACES the
				// defaults rather than adding to them, a caller following the
				// help text produced a copy with no schedules, no pricing and
				// no resources — three sections the server copies by DEFAULT.
				// An incomplete list here is not a documentation nit; it
				// silently narrows what the operator can ask for.
				{Name: "include", Type: "stringSlice", Description: "Sections to copy, comma-separated. REPLACES the defaults (details, schedules, photos, pricing, documents, resources, deposit). One or more of: details, schedules, photos, pricing, departures, locations, documents, resources, extras, questions, deposit, faq"},
			},
			// invariant: --include decides what this irreversible-ish copy
			// actually duplicates, and the audit keeps only body_sha256, so
			// omitting it here would leave an incident responder unable to tell
			// what was copied.
			ForensicFields: []string{"title", "product-code", "include"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"title":        "title",
					"product-code": "product_code",
					"include":      "include",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.DuplicateProductWithBodyWithResponse(ctx, id, &gen.DuplicateProductParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, perr := ParseGenResponse(resp.Body, resp.HTTPResponse, "Product", id)
				if res != nil {
					res.WireBody = body
				}
				return res, perr
			},
		},
		{
			Use: "products delete <id>", Short: "Soft-delete a product", Kind: KindMutation,
			Verb: "DELETE", Path: "/products/{id}", Ability: invpkg.Write,
			DryRunMode:     DryRunNotSupported, // D32: spec has no dry-run input.
			PositionalArgs: []string{"id"},
			Long: "Soft-deletes a product. NOTE: this endpoint does not support --dry-run; " +
				"the spec defines no dry-run input. Use `products get <id>` first to verify " +
				"the resource state before deletion.",
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.DeleteProductWithResponse(ctx, id, &gen.DeleteProductParams{IdempotencyKey: args.IdempotencyKeyUUID})
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Product", id)
			},
		},
		{
			Use: "products restore <id>", Short: "Restore a soft-deleted product",
			Kind: KindMutation, Verb: "POST", Path: "/products/{id}/restore",
			Ability: invpkg.Write, DryRunMode: DryRunBody,
			PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, nil)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.RestoreProductWithBodyWithResponse(ctx, id, &gen.RestoreProductParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Product", id)
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
	}
}

// -----------------------------------------------------------------------------
// POST /products/bookable request-body gates.
// -----------------------------------------------------------------------------

// bookableListCaps mirror the spec's minItems/maxItems on the two lists.
const (
	bookableListMin = 1
	bookableListMax = 20
)

// validateBookableProductBody checks the two lists that distinguish
// `products create-bookable` from `products create`.
//
// Both item shapes are `additionalProperties: false` in the spec, so an
// unlisted key is a 422 naming it. The spec also names the two keys callers
// actually get wrong — `title` on an option (the column is `name`) and `amount`
// on a category — and says why it matters that they are refused rather than
// dropped: "silently discarding either produces a product priced at nothing
// under a 201". A local gate is worth it for the same reason the server's is.
// Reads the ASSEMBLED body, so --data is held to the same rules as the flags.
func validateBookableProductBody(body []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return fmt.Errorf("inventory: assembled request body is not a JSON object: %w", err)
	}
	if err := validateBookableList(m, "options", "--options",
		[]string{"title"},
		[]string{"title", "option_code", "capacity", "min_age", "max_age"}); err != nil {
		return err
	}
	return validateBookableList(m, "pricing_categories", "--pricing-categories",
		[]string{"name", "amount"},
		[]string{"name", "type", "min_age", "max_age", "amount"})
}

// validateBookableList checks one of the two arrays: present, an array, within
// the 1-20 bounds, every item an object carrying its required keys and no key
// outside the allowed set.
//
// It does NOT check value types beyond that. The server owns the per-field
// rules (the 200-char titles, the age bounds, the pricing-category enum), and
// restating them here would be a second implementation to drift. What is
// checked is the SHAPE, which is the part a 422 describes least usefully: an
// unknown key is reported against an index the caller has to count to.
func validateBookableList(body map[string]json.RawMessage, key, flag string, required, allowed []string) error {
	raw, ok := body[key]
	if !ok {
		return fmt.Errorf("%s is required: this endpoint exists to create a product that can "+
			"be SOLD, and a product with no %s is the shell `products create` already makes", flag, key)
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		return fmt.Errorf("%s must be a JSON ARRAY of objects (got %s): %w", flag, raw, err)
	}
	if len(rows) < bookableListMin {
		return fmt.Errorf("%s needs at least %d entry: an empty list would rebuild the "+
			"unsellable shell `products create` produces, under a different name", flag, bookableListMin)
	}
	if len(rows) > bookableListMax {
		return fmt.Errorf("%s: %d entries exceeds the %d-entry cap", flag, len(rows), bookableListMax)
	}
	for i, row := range rows {
		if row == nil {
			return fmt.Errorf("%s[%d]: expected an object, got null", flag, i)
		}
		for k := range row {
			if !slices.Contains(allowed, k) {
				return fmt.Errorf("%s[%d]: unknown key %q — the item shape is CLOSED and the "+
					"server refuses it with a 422 rather than dropping it. Accepts: %s",
					flag, i, k, strings.Join(allowed, ", "))
			}
		}
		for _, k := range required {
			if _, present := row[k]; !present {
				return fmt.Errorf("%s[%d]: missing %q (required: %s)", flag, i, k, strings.Join(required, ", "))
			}
		}
	}
	return nil
}

// snakeFieldMap builds a JSONBodyFromArgs field map for flags whose JSON key is
// simply the flag name in snake_case — `--from-price` → `from_price`.
//
// It exists so a command whose every key follows that rule does not restate the
// rule once per field. A hand-written literal is worth it where a key diverges
// from its flag (`--send-now` → `send_at`, `--include` → `include`), because
// the pairing is real information; where it does not, the literal is 29 chances
// to typo a pairing the compiler cannot check, and the drift tests only catch
// the half that lands on a key the spec does not have.
//
// Callers pass flag names, so an unknown name yields a key the command never
// sets rather than a wrong one: JSONBodyFromArgs skips any flag absent from
// args.Flags, and args.Flags is built solely from the command's own Flags.
func snakeFieldMap(names ...string) map[string]string {
	out := make(map[string]string, len(names))
	for _, n := range names {
		out[n] = strings.ReplaceAll(n, "-", "_")
	}
	return out
}
