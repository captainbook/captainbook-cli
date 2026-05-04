# Pricing Tiers

A `PricingTier` is a **headcount band** under a parent `PricingCategory` ("1–3 guests pay €120 each, 4+ pay €100 each"). Tiers describe the band (`min`, `max`) and the fare (`amount`); the named label ("Adults", "Children") and the `product_id` link live on the parent — see [pricing-categories.md](pricing-categories.md).

Soft-deletable. Bulk pricing changes go through `availabilities bulk-update pricing` (which references existing tier IDs, not categories).

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory pricing-tiers list` | GET /pricing-tiers | `cli:read` | n/a |
| `inventory pricing-tiers get <id>` | GET /pricing-tiers/{id} | `cli:read` | n/a |
| `inventory pricing-tiers create` | POST /pricing-tiers | `cli:write` | body |
| `inventory pricing-tiers update <id>` | PATCH /pricing-tiers/{id} | `cli:write` | body |
| `inventory pricing-tiers delete <id>` | DELETE /pricing-tiers/{id} | `cli:write` | none |
| `inventory pricing-tiers restore <id>` | POST /pricing-tiers/{id}/restore | `cli:write` | none |

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
ceebee inventory availabilities list --product-option-id po_88 --format json \
  | jq '.data[] | select(.pricing_tier_ids[]? == "22") | .id' \
  | wc -l
```

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
- ⚠️ **Legacy aliases ignored:** the spec accepts `--name`, `--product-option-id`, `--availability-id` for backward compat — they're persisted to the audit log but **silently dropped on persist**. Don't rely on them. The tier's name belongs on the parent `PricingCategory`. Availability scoping happens via the `availability_pricing_tier` pivot, not a tier column.
- ⚠️ **`amount` is minor units in tenant currency.** `12500` is €125.00 in EUR or ¥12,500 in JPY. The tier's `currency` follows the tenant; not per-row.
- ⚠️ **`min` / `max` define inclusive headcount bounds.** `min=4 max=null` means "4 or more". Overlapping bands are not validated server-side — the booking flow picks the first match.
- ⚠️ **Known server bug:** `pricing-tiers restore` may return 404 even after a successful delete (the row IS soft-deleted but the restore handler can't find it). Filed with server team — workaround is delete only what you're prepared to keep deleted.

## See also

- [pricing-categories.md](pricing-categories.md) — required parent. Read this first.
- [product-options.md](product-options.md) — tiers belong to a category which belongs to a product, indirectly to options via product.
- [availabilities.md](availabilities.md) — bulk pricing changes go through `availabilities bulk-update pricing` referencing existing tier IDs.
