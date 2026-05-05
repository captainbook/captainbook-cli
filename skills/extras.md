# Extras

An `Extra` is an add-on or upsell tied to a `ProductOption` — equipment rental, photo package, transport. Customers pick extras at checkout. CRUD with soft-delete + restore. No cascade on delete.

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory extras list` | GET /extras | `cli:read` | n/a |
| `inventory extras show <id>` | GET /extras/{id} | `cli:read` | n/a |
| `inventory extras create` | POST /extras | `cli:write` | body |
| `inventory extras update <id>` | PATCH /extras/{id} | `cli:write` | body |
| `inventory extras delete <id>` | DELETE /extras/{id} | `cli:write` | none |
| `inventory extras restore <id>` | POST /extras/{id}/restore | `cli:write` | body |

## Worked examples

### 1. List extras for one option

```bash
ceebee inventory extras list --product-option-id po_88
```

Returns table of `{id, product_option_id, title, price, capacity, updated_at}`.

### 2. Create a "Wetsuit rental" extra at €15

```bash
ceebee inventory extras create \
  --product-option-id po_88 \
  --title "Wetsuit rental" \
  --price 1500 \
  --capacity 20 \
  --dry-run
```

`1500` = €15.00. `--capacity` is per-availability stock for this extra. Drop `--dry-run` to commit.

### 3. Bump price across all "Photo package" extras

Intent: small change in one tenant.

```bash
for id in $(ceebee inventory extras list --format json | jq -r '.data[] | select(.title=="Photo package") | .id'); do
  ceebee inventory extras update "$id" --price 2500 --dry-run
done
```

Inspect the diffs, then re-run without `--dry-run`.

### 4. Soft-delete and restore

```bash
ceebee inventory extras delete ex_42        # 204
ceebee inventory extras list --include-trashed --product-option-id po_88
ceebee inventory extras restore ex_42       # 200
```

### 5. List extras updated in last week

```bash
ceebee inventory extras list --since "2026-04-20T00:00:00Z"
```

## Pitfalls

- ⚠️ **No cascade on delete.** Soft-deleting an Extra does NOT touch related rows — children are not affected. Safer than `pricing-tiers delete`, but historical bookings still reference the (now soft-deleted) extra; their booking lines stay intact.
- ⚠️ **No server-side dry-run on delete.** CLI rejects `--dry-run` at parse time. To gauge impact, search bookings server-side via the admin UI; the CLI does not expose "find bookings using extra X".
- ⚠️ **Price is minor units in tenant currency.** `--price 500` is €5.00 in EUR, ¥500 in JPY. The Extra inherits currency from the parent product's option.
- ⚠️ **`capacity` here is per-Extra stock**, not per-availability. If you sell 20 wetsuits and an availability has 30 seats, the wetsuit Extra caps at 20.

## See also

- [product-options.md](product-options.md) — Extras hang off `ProductOption`.
- [questions.md](questions.md) — sibling catalog (asked at checkout).
