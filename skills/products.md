# Products

Products are the top of the inventory hierarchy: a Product is "a thing the tenant sells" — a tour, a class, a rental. Each Product has one or more `ProductOption`s (variants), and is referenced by `PricingTier`s, `Availability` rows, `Extras`, `Questions`, and `Booking` rows. Products are soft-deletable (Laravel `SoftDeletes`).

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory products list` | GET /products | `cli:read` | n/a |
| `inventory products show <id>` | GET /products/{id} | `cli:read` | n/a |
| `inventory products create` | POST /products | `cli:write` | body |
| `inventory products update <id>` | PATCH /products/{id} | `cli:write` | body |
| `inventory products delete <id>` | DELETE /products/{id} | `cli:write` | none |
| `inventory products restore <id>` | POST /products/{id}/restore | `cli:write` | body |

## Worked examples

### 1. List published products in a category

Intent: find every active product tagged `kayaking`.

```bash
ceebee inventory products list --status published --category kayaking --limit 100
```

Returns a table of `{id, title, status, schedule_type, from_price, currency, updated_at}`. Cursor-paginate with `--cursor "<pagination.cursor_next>"`.

### 2. Show one product, machine-readable

Intent: feed the product detail into a downstream script.

```bash
ceebee inventory products show prod_42 --format json
```

The envelope has `meta`, `data` (the full Product). Includes timezone, cancellation policy, currency.

### 3. Create a draft product (preview first)

Intent: stage a new "Sunset Snorkeling" tour as a draft, but verify the payload before committing.

```bash
ceebee inventory products create \
  --title "Sunset Snorkeling" \
  --currency EUR \
  --from-price 7500 \
  --capacity 12 \
  --schedule-type FIXED \
  --status draft \
  --dry-run
```

`--from-price 7500` = €75.00. Dry-run prints a colored diff (no row created); idempotency row is NOT consumed. Drop `--dry-run` to commit.

### 4. Update title + price together

Intent: rebrand and reprice an existing product in one atomic call.

```bash
ceebee inventory products update prod_42 \
  --title "Sunset Snorkeling — Premium" \
  --from-price 9500
```

Default `--format json` returns `{ "data": { "diff": {...}, "would_apply": false } }`-shaped result with the applied changes. Add `--dry-run` to preview without committing.

### 5. Soft-delete then restore

Intent: pull a product offline temporarily.

```bash
ceebee inventory products delete prod_42       # 204; deleted_at set
ceebee inventory products list --include-trashed   # to find it again
ceebee inventory products restore prod_42       # 200; deleted_at cleared
```

Delete does NOT support `--dry-run` server-side. Use `--include-trashed` on `list` to surface soft-deleted rows.

## Pitfalls

- ⚠️ **Cascade on delete:** `Product::$cascadeDeletes = ['options']` — soft-deleting a Product cascades to its `ProductOption`s, and each option in turn cascades to its `virtualProductOption` and `discount`. **`PricingTier`s and `Availability` rows are NOT cascaded** — clean those up separately or restore later may leave orphans visible.
- ⚠️ **`delete` has no server dry-run.** The CLI rejects `--dry-run` on `products delete` at parse time. To preview cascade impact, fetch the option count first: `ceebee inventory product-options list --product-id prod_42 --format json | jq '.data | length'`.
- ⚠️ **Translatable fields are English-only on read.** `title` and `description` are Spatie-translatable on the server, but the CLI returns the English translation only. Multi-language editing is not in V1.
- ⚠️ **`from_price` is a denormalized hint**, not the price applied at booking. Real prices live on `PricingTier`s attached to `Availability`s. Updating `from_price` does not re-price existing availabilities.

## See also

- [product-options.md](product-options.md) — variants under a Product.
- [pricing-tiers.md](pricing-tiers.md) — fares attached to options/availabilities.
- [availabilities.md](availabilities.md) — per-date capacity rendered from a product option.
- [media.md](media.md) — product images and PDFs.
- [categories.md](categories.md) — `category_ids[]` on create/update.
