# Product Options

A `ProductOption` is a variant of a `Product` — "Half-day tour" vs "Full-day tour", "Group A" vs "Group B". Pricing tiers, availabilities, and questions hang off ProductOptions; extras attach to `Product`. Soft-deletable.

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory product-options list` | GET /product-options | `cli:read` | n/a |
| `inventory product-options show <id>` | GET /product-options/{id} | `cli:read` | n/a |
| `inventory product-options create` | POST /product-options | `cli:write` | body |
| `inventory product-options update <id>` | PATCH /product-options/{id} | `cli:write` | body |
| `inventory product-options delete <id>` | DELETE /product-options/{id} | `cli:write` | none |
| `inventory product-options restore <id>` | POST /product-options/{id}/restore | `cli:write` | body |

## Worked examples

### 1. List all options under a product

Intent: enumerate the variants of `prod_42` to choose one for an availability bulk-update.

```bash
ceebee inventory product-options list --product-id prod_42
```

Returns table of `{id, product_id, title, status, capacity, updated_at}`.

### 2. Show one option

Intent: confirm capacity + status before bulk-updating availabilities under it.

```bash
ceebee inventory product-options show po_88 --format json
```

### 3. Create a new variant with a dry-run preview

Intent: add a "Sunset" variant under product 42.

```bash
ceebee inventory product-options create \
  --product-id 42 \
  --title "Sunset" \
  --capacity 8 \
  --dry-run
```

Drop `--dry-run` to commit. Idempotency-key auto-minted, printed to stderr. `--option-code` is auto-generated from the title slug if omitted (e.g. `SUNSET-AB12CD`); pass explicitly to control the SKU.

### 4. Update capacity / age limits

```bash
ceebee inventory product-options update 88 \
  --capacity 12 --min-age 14 --max-age 65
```

Note: ProductOption has no `description` or `status` of its own — those live on the parent Product. The `--title` flag is mapped to the underlying `name` column server-side.

### 5. Soft-delete + restore round-trip

```bash
ceebee inventory product-options delete 88                       # 204
ceebee inventory product-options list --product-id 42 --include-trashed
ceebee inventory product-options restore 88                      # 200
```

## Pitfalls

- ⚠️ **Cascade on delete:** `ProductOption::$cascadeDeletes` propagates the soft-delete to `virtualProductOption` and `discount`. **`PricingTier`s and `Availability` rows owned by the option are NOT cascaded** — they remain soft-readable but referenced rows may surprise you on restore.
- ⚠️ **No server-side dry-run on delete.** Same shape as products — the CLI rejects `--dry-run` on `delete` at parse time. Inspect references first via `ceebee inventory pricing-tiers list --product-option-id po_88` and `ceebee inventory availabilities list --product-option-id po_88`.
- ⚠️ **Capacity on the option vs. on availabilities:** `ProductOption.capacity` is the default. Per-date `Availability.capacity` overrides it. Don't `update --capacity` and assume it backfills existing availability rows — it doesn't. Use `availabilities bulk-update capacity` instead.
- ⚠️ **`status: archived` is a soft-archive distinct from soft-delete.** Archived rows still appear in `list` (without `--include-trashed`); soft-deleted rows do not. Two filters, two states, both reachable.

## See also

- [products.md](products.md) — parent resource.
- [pricing-tiers.md](pricing-tiers.md) — tiers reference an option.
- [availabilities.md](availabilities.md) — per-date capacity tied to an option.
- [extras.md](extras.md) — child catalog filtered by `--product-id` (Product-scoped).
- [questions.md](questions.md) — child catalog filtered by `--product-option-id`.
