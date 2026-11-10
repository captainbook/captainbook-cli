# Products

Products are the top of the inventory hierarchy: a Product is "a thing the tenant sells" — a tour, a class, a rental. Each Product has one or more `ProductOption`s (variants), and is referenced by `PricingTier`s, `Availability` rows, `Extras`, `Questions`, and `Booking` rows. Products are soft-deletable (Laravel `SoftDeletes`).

**Start from `products create-bookable`, not `products create`.** A product on its own cannot be sold: departures, prices and questions all hang off a product *option*, and a non-internal pricing category with no tier is filtered out of the booking widget — so a plain `create` followed by nothing leaves a product no customer can reach. `create-bookable` (spec 1.44.0) makes the product, its options and its priced categories in one call and one transaction. The plain `create` is still the right call for a shell an operator will fill in from the dashboard.

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory products list` | GET /products | `cli:read` | n/a |
| `inventory products get <id>` | GET /products/{id} | `cli:read` | n/a |
| `inventory products create` | POST /products | `cli:write` | body |
| `inventory products create-bookable` | POST /products/bookable | `cli:write` | body |
| `inventory products update <id>` | PATCH /products/{id} | `cli:write` | body |
| `inventory products delete <id>` | DELETE /products/{id} | `cli:write` | none |
| `inventory products restore <id>` | POST /products/{id}/restore | `cli:write` | body |
| `inventory products duplicate <id>` | POST /products/{id}/duplicate | `cli:write` | body |

## Worked examples

### 1. List published products in a category

```bash
ceebee inventory products list --category 18 --include-trashed=false --limit 100
```

Returns `{id, title, status, schedule_type, from_price, currency, is_private, ...}`. Cursor-paginate with `--cursor "<pagination.cursor_next>"`.

`--q` is **case- and accent-insensitive** (spec 1.36.0), and was neither: `products.title` is a JSON column, MySQL hands every extracted value back as a binary collation, so `--q foo` never matched a product titled `Foo`. It now folds accents too, so `--q croisiere` returns "Croisière". The other four free-text searches on this API have always behaved this way; `/products` was the exception and nothing said so. **Do not read a single hit as an identity match.**

`--status` filters server-side on `draft|published` only:

```bash
ceebee inventory products list --status published
```

On the response, `status` is *derived* from the canonical `is_active` column (`true` → published, `false` → draft) and `is_active` is surfaced directly for clients that prefer the boolean.

⚠️ **There is no `archived` status.** `status` filters the two-state `is_active` column, and the server validates it with `in:published,draft`. `--status archived` is now rejected client-side by the CLI's enum gate (`invalid value for --status: "archived" (allowed: draft, published)`), same as a typo like `--status publshed` — no round trip. Use `--status draft` / `--status published`, or omit the flag. (The spec used to advertise `archived`, which put it on the allow-list and cost a server 422 to discover; fixed upstream in [captainbook#8111](https://github.com/captainbook/captainbook/issues/8111), tracked here as [captainbook-cli#18](https://github.com/captainbook/captainbook-cli/issues/18).)

### 1a. `can_be_sold` and `readiness` — is this product actually sellable? (spec 1.44.0)

Every `Product` read carries two fields that answer the question directly, and before 1.44.0 neither was published even though `Product::updateCanBeSold()` had always computed the answer on every write:

```bash
ceebee inventory products get 42 --format json | jq '{can_be_sold: .data.can_be_sold, readiness: .data.readiness}'
```

- **`can_be_sold`** is the stored column every selling surface filters on. A freshly created product answers `false`.
- **`readiness`** is `{missing[], blocking[]}`. **`blocking == []` is the definition of `can_be_sold`** — that is the field to report to a human, not the bare boolean.
- **`missing` is advisory and is not a reason a product cannot sell.** It additionally carries `no_future_departure`, which is true of every product a seasonal operator owns out of season. Do not relay it as a fault.
- Since 1.44.0 `can_be_sold` also requires at least one non-internal pricing category to have a tier (`no_priced_category`). Products this moved to `false` were already unsellable — both the embedded product page and the calendar widget load categories filtered on `whereHas('pricingTiers')` — the column simply had not noticed.

### 2. Show one product, machine-readable

```bash
ceebee inventory products get 42 --format json
```

The envelope has `meta`, `data` (the full Product). Includes timezone, cancellation policy, currency, all the new bool toggles, and rich-text fields.

### 3. Create a SHARED experience (default — multiple parties per slot)

```bash
ceebee inventory products create \
  --title "Sunset Snorkeling" \
  --currency EUR --timezone "Europe/Athens" \
  --schedule-type datetime --status published \
  --is-private=false --is-priced-per-person \
  --from-price 7500 --from-price-label "From €75 per person" \
  --capacity 12 \
  --description "<p>Group snorkel tour with sunset views.</p>" \
  --inclusions "<p>Mask, fins, guide.</p>" \
  --must-validate-cancellation-policy \
  --cancellation-policy "Free cancellation up to 24h before."
```

When `--is-private=false`, the server forces `is_priced_per_person=true` and `use_alternate_tier_pricing=false` regardless of what you sent — that's the rule for shared experiences.

### 4. Create a PRIVATE experience (one party books the whole slot)

```bash
ceebee inventory products create \
  --title "Private Sunset Sail" \
  --currency EUR --timezone "Europe/Athens" \
  --schedule-type datetime --status published \
  --is-private --capacity 8 \
  --from-price 35000 --from-price-label "From €350 per group" \
  --description "<p>Charter the boat for your party.</p>" \
  --must-validate-cancellation-policy \
  --cancellation-policy-link "https://your-policy.example.com"
```

Use `--cancellation-policy-link` for an external policy URL (mutually exclusive with `--cancellation-policy`).

### 5. Update title + price together

```bash
ceebee inventory products update 42 \
  --title "Sunset Snorkeling — Premium" \
  --from-price 9500 \
  --is-private \
  --dry-run
```

Switching `--is-private` cascades 7 inventory recompute jobs per 1000 availabilities — under `--dry-run` those appear in `MutationResult.side_effects` so an agent can preview the blast radius before committing.

### 6. Soft-delete then restore

```bash
ceebee inventory products delete 42       # 204; deleted_at set
ceebee inventory products list --include-trashed   # find it again
ceebee inventory products restore 42       # 200; deleted_at cleared
```

Delete does NOT support `--dry-run` server-side.

### 7. Schedule type semantics

`--schedule-type date` means customer picks a date only (whole-day slots). `--schedule-type datetime` means customer picks a date and a starting time. Switching from `datetime` to `date` cascades: existing Availability `from`/`to` windows collapse to full-day spans, and `resourceables` rows for the option are deleted (date products don't bind resources).

### 8. Ticketing: change the ticket type safely

`--delivery-method` is the dashboard's "Ticket type": `VOUCHER` (the default) issues one ticket for the whole booking, `TICKET` issues one per guest. It decides the shape of the QR tokens a booking carries, and therefore how attendance is counted. `--redemption-method` is how the customer proves they're booked: `MANIFEST` (default, no identification needed), `DIGITAL` (digital or printed accepted), or `PRINT` (printed only).

Changing `--delivery-method` on a product that has bookings **deletes every ticket on every booking departing in the next 10 years and issues replacements**. The QR codes those customers already hold stop scanning, and *nothing notifies them* — the operator has to resend. So always read the blast radius first:

```bash
# 1. How many bookings would this invalidate? --dry-run is NEVER refused.
ceebee inventory products update 42 \
  --delivery-method TICKET \
  --dry-run --format json | jq '.data.ticket_reissue'
# → {"delivery_method_from":"VOUCHER","delivery_method_to":"TICKET",
#    "affected_bookings":12,"customers_notified":false}

# 2. Only then, and only if the operator accepts resending 12 sets of tickets:
ceebee inventory products update 42 \
  --delivery-method TICKET \
  --confirm-ticket-reissue
```

Without `--confirm-ticket-reissue` step 2 is refused with `422 TICKET_REISSUE_NOT_CONFIRMED`, whose `error.details` carries the same four keys the dry run returns — so one renderer covers both. The refusal is raised *before* the write, so it releases its idempotency key: the retry carrying the confirmation may reuse the same one.

Not refused when the product has no bookings in that window (`affected_bookings: 0` — nothing to invalidate), when the submitted value equals the stored one (not a change), or under `--dry-run`.

`affected_bookings` is an **estimate, not an outcome**: it's counted just before the write, and the job re-reads its own window afterwards, so a booking taken in between is reissued without being counted. It is also *not* scoped to your business unit, because the job isn't either — a scoped number would promise a smaller blast radius than the one that actually lands.

### 9. Create a product that can actually be sold (`create-bookable`)

Intent: stand up a sellable product in one call, rather than a shell plus three follow-ups.

```bash
ceebee inventory products create-bookable \
  --title "Sunset Sailing Tour" \
  --currency EUR --timezone "Europe/Athens" \
  --schedule-type datetime \
  --options '[{"title":"10:00 departure","capacity":12},
              {"title":"18:00 departure","capacity":12}]' \
  --pricing-categories '[{"name":"Adults","amount":7500,"type":"ADULT"},
                         {"name":"Children","amount":4000,"type":"CHILD","max_age":12}]' \
  --dry-run
```

It takes every flag `products create` takes, under the same rules, plus the two lists. What to know:

- **Both item shapes are CLOSED**: an unlisted key is a 422 naming it, not an ignored field. The two spellings callers get wrong are `title` on an option (the column is `name`) and `amount` on a category — and silently discarding either produces a product priced at nothing under a `201`. The CLI refuses both locally, along with `is_internal`, which is not accepted: every category is created with `is_internal: false`.
- **Each pricing category also creates one open-ended tier** (`min: 1`, `max: null`) at its `amount`, because a category with no tier is invisible to the booking widget. `amount` is in **minor units**.
- **`--status` defaults to `draft`**, exactly as the plain create does. Publishing a product with no photo and no departures would put an empty page on the widget.
- **It does not create departures or the main photo.** Departures need a schedule only the operator can state (`availabilities create` or the rule/bulk commands); a photo is an upload (`media upload`). Both therefore stay in `readiness.blocking` on the response — **read and report that field** rather than announcing a finished product.
- **The dry run is built entirely from unsaved models**, so it is reproducible: `uuid`, `product_code`, `option_code` and `color` are minted only on commit and read as `null` on the preview.

### 10. Let one booking span several resources (`--allocation-mode`)

A product's `allocation_mode` decides how many resources one booking may occupy: `single_resource`, or `homogeneous_multi` for a party spread over a fleet (five riders over three jet skis). It reached the API in spec 1.53.0; before that it was writable from the product edit page alone, and a value sent here was dropped under a `200`.

```bash
# 1. The fleet has to exist first: two or more capacity-bearing ASSETS on one schedule.
ceebee inventory products update prd_7 --allocation-mode homogeneous_multi --dry-run
```

- **`PATCH` only.** `products create` and `products create-bookable` **refuse** the field with a 422 rather than dropping it, because a spanning mode needs a fleet to span and neither endpoint creates resources. Neither CLI create command offers the flag. The sequence is: create the product, create its schedules, attach two or more assets with a capacity each, then PATCH the mode.
- **`homogeneous_multi` is refused with a 422 naming which of five gates refused it.** One of them is not operator-fixable: the `MultiResourceAllocation` account feature being off means contacting CaptainBook support, and for most accounts that is the answer — **do not retry**. The others are a shared `capacity` above 0, a capacity on a guide or equipment, the Payroll app with several guides on a schedule, and no schedule carrying two or more capacity-bearing assets.
- **`single_resource` is never refused**, so a product can always be moved back off a spanning mode.
- **Echoing the stored value is not a change** and runs no gate, so a read-modify-write round trip is never refused over this field — even on a row whose mode a gate now blocks.
- **Read two keys, not one.** `allocation_mode` is what the operator configured; `spans_several_resources` is what the platform is actually honouring. They differ behind those gates, all of which degrade silently.
- **Never price a party off `allocation_mode`.** On a per-group-priced product each unit a party takes is one group, so five riders on three jet skis pay three times — and a party that spans nothing pays once.

## Pitfalls

- ⚠️ **Changing either ticketing field needs the `update_delivery_method` permission** — the single permission the Ticketing form's Update button carries. The gate is on the *change*, not the key: both fields are on the read shape, so echoing back the stored value is free. That keeps the ordinary read-modify-write round trip, and "GET a product, POST it back to clone it", working for a caller who never touches ticketing. On `products create` there's no stored row to compare against, so the comparison is against the column default — sending `VOUCHER` / `MANIFEST`, or omitting the fields, needs nothing.
- ⚠️ **`--redemption-method` reissues nothing.** Only `--delivery-method` invalidates tickets. Changing the redemption method alone still needs the permission, but no confirmation.
- ⚠️ **`PLAN_LIMIT_REACHED` (403) is not a permissions problem.** `create`, `duplicate` and `restore` are gated on the account's plan allowance as well as the `create_product` permission (spec 1.39.0–1.40.0), and the entitlement is checked FIRST, so one account state gives one code whoever is asking — an account owner included, which is the hole 1.40.0 closed. `FORBIDDEN` means fix the token or the role; `PLAN_LIMIT_REACHED` means the operator must upgrade the plan or delete a product, and its `details` carry `plan_item`, `limit` and `current`. Enforcement is rolled out **per account**, so an account over its limit may still be answered `201` — handle the refusal, never predict it from the allowance.
- ⚠️ **Switching `--is-private` recomputes inventory AFTER the response (spec 1.45.0).** It runs one schedule at a time from the start of yesterday onwards, so a read taken straight after the `200` can still show the old inventory for a short while. Past departures keep the vacancies and fully-booked state they were last computed with, and `booked_quantity` is not recomputed at all — it counts booked guests and does not depend on `is_private`. Do not treat an immediate re-read as a verification failure.
- ⚠️ **Cascade on delete:** `Product::$cascadeDeletes = ['options']` — soft-deleting a Product cascades to its `ProductOption`s, and each option in turn cascades to its `virtualProductOption` and `discount`. **`PricingTier`s and `Availability` rows are NOT cascaded** — clean those up separately or restore later may leave orphans visible.
- ⚠️ **`delete` has no server dry-run.** The CLI rejects `--dry-run` on `products delete` at parse time. To preview cascade impact, fetch the option count first: `ceebee inventory product-options list --product-id 42 --format json | jq '.data | length'`.
- ⚠️ **Implicit-override traps:**
  - `--must-validate-cancellation-policy=false` (default) silently nulls **both** `--cancellation-policy` AND `--cancellation-policy-link`. Set the flag to `true` to retain a policy.
  - `--is-private=false` forces `is_priced_per_person=true` and `use_alternate_tier_pricing=false`, regardless of what you sent.
- ⚠️ **`--inclusions` / `--exclusions`:** spec says "rich text" but the dashboard treats these as plain bullet lists. HTML you send round-trips on read but renders literally to customers. Send plain text until the spec gets clarified.
- ⚠️ **`--currency` is required on create but is not a choice.** `products` has no currency column, so the value is dropped on persist and must equal the account currency — since spec 1.4.0 a mismatch is refused with `422 VALIDATION_FAILED` (it used to return `201` and report a product priced in a currency the account does not trade in). If an operator asks for a product in GBP on a EUR account, the answer is that the API cannot express it — not that you should send GBP. Read the account currency off `meta.currency` or `whoami` and echo it back.
- ⚠️ **`--product-code` auto-generated** from the title slug + random suffix when omitted (e.g. `SUNSET-SNORKELING-AB12CD`). Pass explicitly to control the SKU.
- ⚠️ **`--category-ids` is integer**, not string. The flag is `intSlice`. Comma-separate: `--category-ids "18,87,7"`.
- ⚠️ **Known server bug:** `--category-ids` on POST/PATCH may return 404 even when categories exist. Workaround: create the product without categories then PATCH `--category-ids` afterwards (still 404 today; tracked).
- ⚠️ **Translatable fields are English-only on read.** `title`, `description`, `instructions`, `requirements`, `inclusions`, `exclusions` are translatable on the server, but the CLI returns the English translation only. Multi-language editing is not in V1.
- ⚠️ **A plain `products create` leaves a product nothing can sell.** No option means no departures, no prices and no questions; a priced category needs a tier. Prefer `products create-bookable`, and read `readiness.blocking` on whichever you use before telling an operator the product is ready.
- ⚠️ **Changing the effective allocation mode republishes capacity to every connected channel** — and so does changing `--capacity`, which this endpoint never announced while the identical dashboard edit always has. The figure a channel publishes is chosen by the mode: the sum of free seats across the fleet for a spanning product, the largest single free unit otherwise. A CLI capacity edit used to recompute the stored seat counts and leave GetYourGuide, Viator and Octo advertising the old number.
- ⚠️ **`from_price` is a denormalized hint**, not the price applied at booking. Real prices live on `PricingTier`s under `PricingCategory`s. Updating `from_price` does not re-price existing availabilities.

## See also

- [product-options.md](product-options.md) — variants under a Product.
- [pricing-categories.md](pricing-categories.md) — Adult / Child / Senior buckets (parent of tiers).
- [pricing-tiers.md](pricing-tiers.md) — fares per headcount band.
- [availabilities.md](availabilities.md) — per-date capacity + `create-rule` recurrence generator.
- [resources.md](resources.md) — physical inventory (boats, guides) bound to a product option.
- [locations.md](locations.md) — start / end / waypoints attached to a product.
- [media.md](media.md) — product images and PDFs.
- [categories.md](categories.md) — read-only catalog tags.
