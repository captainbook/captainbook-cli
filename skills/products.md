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

Returns `{id, title, status, schedule_type, from_price, currency, is_private, ...}`. Cursor-paginate with `--cursor "<pagination.cursor_next>"`. Note: `--status` query filter is no longer in spec — published-vs-draft is exposed via `is_active` on the read response.

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

## Pitfalls

- ⚠️ **Cascade on delete:** `Product::$cascadeDeletes = ['options']` — soft-deleting a Product cascades to its `ProductOption`s, and each option in turn cascades to its `virtualProductOption` and `discount`. **`PricingTier`s and `Availability` rows are NOT cascaded** — clean those up separately or restore later may leave orphans visible.
- ⚠️ **`delete` has no server dry-run.** The CLI rejects `--dry-run` on `products delete` at parse time. To preview cascade impact, fetch the option count first: `ceebee inventory product-options list --product-id 42 --format json | jq '.data | length'`.
- ⚠️ **Implicit-override traps:**
  - `--must-validate-cancellation-policy=false` (default) silently nulls **both** `--cancellation-policy` AND `--cancellation-policy-link`. Set the flag to `true` to retain a policy.
  - `--is-private=false` forces `is_priced_per_person=true` and `use_alternate_tier_pricing=false`, regardless of what you sent.
- ⚠️ **`--inclusions` / `--exclusions`:** spec says "rich text" but the dashboard treats these as plain bullet lists. HTML you send round-trips on read but renders literally to customers. Send plain text until the spec gets clarified.
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
