# Pricing Tiers

A `PricingTier` is a named fare class — "Adult", "Child", "Senior", "Member" — under a ProductOption. Tiers carry the actual booking price. Bulk pricing changes go through `availabilities bulk-update pricing`. Soft-deletable.

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory pricing-tiers list` | GET /pricing-tiers | `cli:read` | n/a |
| `inventory pricing-tiers show <id>` | GET /pricing-tiers/{id} | `cli:read` | n/a |
| `inventory pricing-tiers create` | POST /pricing-tiers | `cli:write` | body |
| `inventory pricing-tiers update <id>` | PATCH /pricing-tiers/{id} | `cli:write` | body |
| `inventory pricing-tiers delete <id>` | DELETE /pricing-tiers/{id} | `cli:write` | none |
| `inventory pricing-tiers restore <id>` | POST /pricing-tiers/{id}/restore | `cli:write` | body |

## Worked examples

### 1. List tiers under one option

```bash
ceebee inventory pricing-tiers list --product-option-id po_88
```

Returns `{id, product_option_id, name, amount, position, updated_at}` — `amount` in minor units of tenant currency.

### 2. List tiers active on one availability

Intent: see the fare set rendered for May 5.

```bash
ceebee inventory pricing-tiers list --availability-id av_2026_05_05_po88
```

Useful for reading "what does a customer see today on this date?".

### 3. Create a Senior tier

```bash
ceebee inventory pricing-tiers create \
  --product-option-id po_88 \
  --name "Senior" \
  --amount 5500 \
  --position 3 \
  --dry-run
```

`5500` = €55.00. Drop `--dry-run` to commit.

### 4. PREVIEW impact of a delete (data-loss-adjacent)

Intent: see what would be lost before deleting `pt_legacy_adult`.

`pricing-tiers delete` does NOT support dry-run server-side — the CLI rejects `--dry-run`. Use the read-side check instead:

```bash
ceebee inventory availabilities list --product-option-id po_88 --format json \
  | jq '.data[] | select(.pricing_tier_ids[]? == "pt_legacy_adult") | .id' \
  | wc -l
```

Then proceed with the real delete only after you've recorded the affected count.

### 5. Restore a soft-deleted tier (and its availabilities)

```bash
ceebee inventory pricing-tiers restore pt_legacy_adult --dry-run
ceebee inventory pricing-tiers restore pt_legacy_adult
```

Restoring the tier does NOT auto-restore the cascaded-deleted availabilities — those need their own `restoreAvailabilities` operation (not in V1). Plan accordingly.

## Pitfalls

- ⚠️ **DATA-LOSS-ADJACENT DELETE.** `PricingTier::$cascadeDeletes = ['availabilities']`. Soft-deleting a tier soft-deletes EVERY `Availability` row rendered against that tier. Calendar data for that tier disappears from list endpoints. **Always count affected availabilities first** (see example 4) — and consider exporting them to JSON before delete.
- ⚠️ **No server-side dry-run on delete.** The CLI rejects `--dry-run` on `pricing-tiers delete` at parse time. Inspect `availabilities list` before pulling the trigger.
- ⚠️ **Restoring the tier does not restore cascaded availabilities.** Phase 1 has no "restore cascade" operation. If you delete and then realize the cascade was a mistake, the availabilities are stuck soft-deleted unless an engineer hand-clears `deleted_at` in DB.
- ⚠️ **`amount` is minor units in tenant currency.** `2500` is €25.00 in EUR or ¥2500 in JPY. The tier's `currency` follows the parent product.

## See also

- [product-options.md](product-options.md) — tiers reference `product_option_id`.
- [availabilities.md](availabilities.md) — bulk pricing changes go through `availabilities bulk-update pricing`.
- [bookings.md](bookings.md) — bookings carry per-tier seat counts.
