# Products

Products are the top of the inventory hierarchy: a Product is "a thing the tenant sells" — a tour, a class, a rental. Each Product has one or more `ProductOption`s (variants), and is referenced by `PricingTier`s, `Availability` rows, `Extras`, `Questions`, and `Booking` rows. Products are soft-deletable (Laravel `SoftDeletes`).

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory products list` | GET /products | `cli:read` | n/a |
| `inventory products get <id>` | GET /products/{id} | `cli:read` | n/a |
| `inventory products create` | POST /products | `cli:write` | body |
| `inventory products update <id>` | PATCH /products/{id} | `cli:write` | body |
| `inventory products delete <id>` | DELETE /products/{id} | `cli:write` | none |
| `inventory products restore <id>` | POST /products/{id}/restore | `cli:write` | body |

## Worked examples

### 1. List published products in a category

```bash
ceebee inventory products list --category 18 --include-trashed=false --limit 100
```

Returns `{id, title, status, schedule_type, from_price, currency, is_private, ...}`. Cursor-paginate with `--cursor "<pagination.cursor_next>"`.

`--status` filters server-side on `draft|published` only:

```bash
ceebee inventory products list --status published
```

On the response, `status` is *derived* from the canonical `is_active` column (`true` → published, `false` → draft) and `is_active` is surfaced directly for clients that prefer the boolean.

⚠️ **There is no `archived` status.** `status` filters the two-state `is_active` column, and the server validates it with `in:published,draft`. `--status archived` is now rejected client-side by the CLI's enum gate (`invalid value for --status: "archived" (allowed: draft, published)`), same as a typo like `--status publshed` — no round trip. Use `--status draft` / `--status published`, or omit the flag. (The spec used to advertise `archived`, which put it on the allow-list and cost a server 422 to discover; fixed upstream in [captainbook#8111](https://github.com/captainbook/captainbook/issues/8111), tracked here as [captainbook-cli#18](https://github.com/captainbook/captainbook-cli/issues/18).)

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

## Pitfalls

- ⚠️ **Changing either ticketing field needs the `update_delivery_method` permission** — the single permission the Ticketing form's Update button carries. The gate is on the *change*, not the key: both fields are on the read shape, so echoing back the stored value is free. That keeps the ordinary read-modify-write round trip, and "GET a product, POST it back to clone it", working for a caller who never touches ticketing. On `products create` there's no stored row to compare against, so the comparison is against the column default — sending `VOUCHER` / `MANIFEST`, or omitting the fields, needs nothing.
- ⚠️ **`--redemption-method` reissues nothing.** Only `--delivery-method` invalidates tickets. Changing the redemption method alone still needs the permission, but no confirmation.
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
