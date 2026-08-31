# Pricing Tiers

A `PricingTier` is a **headcount band** under a parent `PricingCategory` ("1–3 guests pay €120 each, 4+ pay €100 each"). Tiers describe the band (`min`, `max`) and the fare (`amount`); the named label ("Adults", "Children") and the `product_id` link live on the parent — see [pricing-categories.md](pricing-categories.md).

Soft-deletable. Bulk pricing changes go through `availabilities bulk-update pricing` (which references existing tier IDs, not categories).

`list` and `get` also take `--availability-id`, which overlays per-slot fares from the `availability_pricing_tier` pivot onto `amount` — see "Per-slot overrides" below.

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory pricing-tiers list` | GET /pricing-tiers | `cli:read` | n/a |
| `inventory pricing-tiers get <id>` | GET /pricing-tiers/{id} | `cli:read` | n/a |
| `inventory pricing-tiers create` | POST /pricing-tiers | `cli:write` | body |
| `inventory pricing-tiers update <id>` | PATCH /pricing-tiers/{id} | `cli:write` | body |
| `inventory pricing-tiers delete <id>` | DELETE /pricing-tiers/{id} | `cli:write` | none |
| `inventory pricing-tiers restore <id>` | POST /pricing-tiers/{id}/restore | `cli:write` | body |

## Required prerequisites

A `PricingCategory` must exist on the target product **first** — tiers attach via `--pricing-category-id`. Without a category, you can't create a tier.

```bash
# 1. Make sure a category exists (one-time per product per audience)
ADULT=$(ceebee inventory pricing-categories create \
  --product-id 44 --name Adults --type ADULT \
  --format json | jq -r '.data.pricing_category.id')

# 2. Then create the tier(s) under it
ceebee inventory pricing-tiers create --pricing-category-id $ADULT --amount 12500 --min 1
```

## Per-slot overrides (`availability_pricing_tier`)

A tier's `amount` is the **catalogue** price. Individual slots can override it through the `availability_pricing_tier` pivot — that is how "same tour, +€20 on New Year's Eve" is stored. There is no command named after the pivot: it is **read** through availability-scoped tier queries and **written** only through `availabilities bulk-update pricing`.

### Read — scope a tier query to one slot

```bash
ceebee inventory pricing-tiers list --availability-id av_2026_12_31_po88
ceebee inventory pricing-tiers get 22 --availability-id av_2026_12_31_po88
```

Availability-scoped responses carry three fields that make an override detectable:

| Field | Meaning |
|-------|---------|
| `amount` | **Effective** price — the pivot `fare` when a row exists, catalogue price otherwise |
| `default_amount` | Catalogue price (`pricing_tiers.fare`), for diffing against `amount` |
| `is_override` | `true` iff an `availability_pricing_tier` row exists for this slot |

Without `--availability-id`, `default_amount` and `is_override` are omitted entirely and `amount` is always the catalogue price. `--availability-id` and `--product-id` are mutually exclusive — the CLI rejects the pair before the round-trip, and the server returns 422. An availability with no product context (e.g. a Resource-backed slot) returns an empty page, not an error.

To scan a date range rather than a single slot, come at it from the availability side with `--include-pricing`:

```bash
# Which September slots have overridden fares, and by how much?
ceebee inventory availabilities list \
  --product-option-id po_88 --from 2026-09-01 --to 2026-10-01 \
  --include-pricing --format json \
| jq '.data[] | select(.pricing_tiers[]?.is_override)
      | {id, date,
         fares: [.pricing_tiers[] | select(.is_override)
                 | {id, was: .default_amount, now: .amount}]}'
```

### Write — one path only

```bash
ceebee inventory availabilities bulk-update pricing \
  --product-option-id po_88 \
  --from 2026-12-31 --to 2027-01-01 \
  --fares '[{"pricing_tier_id":"22","amount":14500}]' \
  --dry-run
```

Drop `--dry-run` to commit. Three constraints to plan around:

- **No single-slot write.** `availabilities update <id>` accepts only `--capacity` and `--status`. To override exactly one slot, bracket it with a one-day half-open range as above.
- **Additive, never subtractive.** Tiers absent from `--fares` keep what they had, and no operation deletes a pivot row — so a slot cannot be reverted to "follows the catalogue". Re-setting it to the catalogue number by hand leaves `is_override: true` forever.
- **Asynchronous.** Returns 202 plus `BULK_UPDATE_ACCEPTED bulk_update_id=<uuid>` on stderr. Exit 0 means *queued*; confirm by re-reading with `--include-pricing`.

## Worked examples

### 1. List tiers under one product

```bash
ceebee inventory pricing-tiers list --product-id 44
```

Returns `{id, pricing_category_id, min, max, amount, currency, deleted_at, ...}` — `amount` in minor units of tenant currency. `currency` defaults to `EUR` (no per-row column; tenant-level).

### 2. Single flat fare (one tier, all headcounts)

```bash
ceebee inventory pricing-tiers create \
  --pricing-category-id $ADULT \
  --amount 12500 --min 1
```

`--max` omitted → open-ended ("1 or more pay €125 each").

### 3. Volume discount: 1–3 pay €125, 4+ pay €100

```bash
# Band 1: 1–3 guests
ceebee inventory pricing-tiers create \
  --pricing-category-id $ADULT \
  --amount 12500 --min 1 --max 3

# Band 2: 4 and up
ceebee inventory pricing-tiers create \
  --pricing-category-id $ADULT \
  --amount 10000 --min 4
```

### 4. Reparent a tier under a different category

```bash
ceebee inventory pricing-tiers update 22 \
  --pricing-category-id $NEW_CATEGORY_ID
```

Sending `--pricing-category-id` on PATCH moves the tier (404 if the target category doesn't exist). Reparenting categories themselves is forbidden — see [pricing-categories.md](pricing-categories.md).

### 5. PREVIEW impact of a delete (data-loss-adjacent)

`pricing-tiers delete` does NOT support dry-run server-side — the CLI rejects `--dry-run`. Use the read-side check instead:

```bash
ceebee inventory availabilities list --product-option-id po_88 \
  --include-pricing --format json \
  | jq '[.data[] | select(.pricing_tiers[]?.id == "22") | .id] | length'
```

`--include-pricing` is required — without it the response carries no `pricing_tiers[]` at all and the filter silently matches nothing, reporting a reassuring zero. Availability rows have no `pricing_tier_ids` field; the tiers are only reachable through the embedded array.

Then proceed with the real delete only after you've recorded the affected count.

### 6. Soft-delete + restore

```bash
ceebee inventory pricing-tiers delete 22
ceebee inventory pricing-tiers restore 22
```

## Pitfalls

- ⚠️ **DATA-LOSS-ADJACENT DELETE.** Soft-deleting a tier soft-deletes every `Availability` row rendered against that tier. Calendar data disappears from list endpoints. **Count affected availabilities first** (example 5) — and consider exporting them to JSON before delete.
- ⚠️ **No server-side dry-run on delete.** The CLI rejects `--dry-run` on `pricing-tiers delete` at parse time. Inspect `availabilities list` before pulling the trigger.
- ⚠️ **Restoring the tier does not restore cascaded availabilities.** Phase 1 has no "restore cascade" operation. If you delete and then realize the cascade was a mistake, the availabilities are stuck soft-deleted unless an engineer hand-clears `deleted_at` in DB.
- ⚠️ **Required: `--pricing-category-id` and `--amount`.** All other flags optional.
- ⚠️ **Legacy aliases ignored on write:** `create` / `update` accept `name`, `product_option_id`, `availability_id`, and `currency` in the request body (reachable via `--data`, since the CLI exposes no such flags) — all four are validated, recorded in the audit log, and then **silently dropped on persist**. The tier's name belongs on the parent `PricingCategory`; currency is tenant-level. Do not confuse the ignored body field `availability_id` with the read-side `--availability-id` filter on `list` / `get`, which is real and does the pivot overlay described above.
- ⚠️ **Editing a tier's `amount` does not move slots that already have a pivot override.** `pricing-tiers update <id> --amount` changes the catalogue price; any availability with an `availability_pricing_tier` row keeps its own fare and silently diverges. Audit with `availabilities list --include-pricing` before assuming a price change took effect calendar-wide.
- ⚠️ **`amount` is minor units in tenant currency.** `12500` is €125.00 in EUR or ¥12,500 in JPY. The tier's `currency` follows the tenant; not per-row.
- ⚠️ **`min` / `max` define inclusive headcount bounds.** `min=4 max=null` means "4 or more". Overlapping bands are not validated server-side — the booking flow picks the first match.
- ⚠️ **Known server bug:** `pricing-tiers restore` may return 404 even after a successful delete (the row IS soft-deleted but the restore handler can't find it). Filed with server team — workaround is delete only what you're prepared to keep deleted.

## See also

- [pricing-categories.md](pricing-categories.md) — required parent. Read this first.
- [product-options.md](product-options.md) — tiers belong to a category which belongs to a product, indirectly to options via product.
- [availabilities.md](availabilities.md) — the only write path for per-slot pivot fares (`bulk-update pricing`), and the `--include-pricing` overlay for scanning a date range.
