# Extras

An `Extra` is an add-on or upsell tied to a `Product` — equipment rental, photo package, transport. Customers pick extras at checkout. CRUD with soft-delete + restore. No cascade on delete.

These endpoints are the **catalogue**: what is on offer today, and repriceable. What a customer actually *bought* lives on the booking as `extras[]` (see [bookings.md](bookings.md)), carries the fare as charged rather than the catalogue `amount`, and is counted in the booking's `total_amount`.

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory extras list` | GET /extras | `cli:read` | n/a |
| `inventory extras get <id>` | GET /extras/{id} | `cli:read` | n/a |
| `inventory extras create` | POST /extras | `cli:write` | body |
| `inventory extras update <id>` | PATCH /extras/{id} | `cli:write` | body |
| `inventory extras delete <id>` | DELETE /extras/{id} | `cli:write` | none |
| `inventory extras restore <id>` | POST /extras/{id}/restore | `cli:write` | body |

## Worked examples

### 1. List extras for one product

```bash
ceebee inventory extras list --product-id pr_88
```

Returns table of `{id, product_id, name, amount, max_quantity, updated_at}`.

### 2. Create a "Wetsuit rental" extra at €15

```bash
ceebee inventory extras create \
  --product-id pr_88 \
  --name "Wetsuit rental" \
  --amount 1500 \
  --currency EUR \
  --max-quantity 20 \
  --dry-run
```

`1500` = €15.00. `--max-quantity` caps how many of this extra can be bought per booking. Drop `--dry-run` to commit.

### 3. Bump price across all "Photo package" extras

Intent: small change in one tenant.

```bash
for id in $(ceebee inventory extras list --format json | jq -r '.data[] | select(.name=="Photo package") | .id'); do
  ceebee inventory extras update "$id" --amount 2500 --dry-run
done
```

Inspect the diffs, then re-run without `--dry-run`.

### 4. Soft-delete and restore

```bash
ceebee inventory extras delete ex_42        # 204
ceebee inventory extras list --include-trashed --product-id pr_88
ceebee inventory extras get ex_42 --include-trashed   # resolve one retired row
ceebee inventory extras restore ex_42       # 200
```

`get <id>` needs `--include-trashed` to resolve a retired extra — which is exactly the case when you are chasing an `extras[]` row on a booking whose `deleted_at` is set. Without the flag the lookup 404s on a row the booking is still happily reporting.

### 5. List extras updated in last week

```bash
ceebee inventory extras list --since "2026-04-20T00:00:00Z"
```

## Pitfalls

- ⚠️ **No cascade on delete.** Soft-deleting an Extra does NOT touch related rows — children are not affected. Safer than `pricing-tiers delete`, but historical bookings still reference the (now soft-deleted) extra; their booking lines stay intact, the row still shows up in the booking's `extras[]` with `deleted_at` set, and its money is still in that booking's `total_amount`. Retiring an add-on does not unsell it.
- ⚠️ **The catalogue `amount` is not what anyone was charged.** Repricing an extra does not restate the bookings that already bought it — those carry their own `unit_amount`. Never reconcile a booking against this endpoint's price; read `bookings get <id>`'s `extras[]` instead.
- ⚠️ **No server-side dry-run on delete.** CLI rejects `--dry-run` at parse time. To gauge impact, search bookings server-side via the admin UI; the CLI does not expose "find bookings using extra X".
- ⚠️ **Amount is minor units in tenant currency.** `--amount 500` is €5.00 in EUR, ¥500 in JPY. `--currency` is required on create, but it is not a choice: `extras` has no currency column, so the value is dropped on persist and must equal the account currency or the create is refused with `422 VALIDATION_FAILED`. Read it off `meta.currency` (or `whoami`) and echo it back.
- ⚠️ **`--max-quantity` is per-booking, not per-availability.** If you sell 20 wetsuits per booking and an availability has 30 seats, each booking still caps at 20 wetsuits.

## See also

- [products.md](products.md) — Extras hang off `Product`.
- [questions.md](questions.md) — sibling catalog (asked at checkout).
