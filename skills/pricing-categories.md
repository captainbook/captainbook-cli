# Pricing Categories

A `PricingCategory` is the **parent bucket** that one or more `PricingTier` rows live under — the named label customers see (`Adults`, `Children`, `Seniors`). Tiers describe the headcount band and the fare; the human-readable label and the `product_id` link live one level up here.

If you have a product with adult/child pricing, you have **two pricing categories** (one per audience), each with **one or more pricing tiers** underneath (one per headcount band).

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory pricing-categories list` | GET /pricing-categories | `cli:read` | n/a |
| `inventory pricing-categories get <id>` | GET /pricing-categories/{id} | `cli:read` | n/a |
| `inventory pricing-categories create` | POST /pricing-categories | `cli:write` | body |
| `inventory pricing-categories update <id>` | PATCH /pricing-categories/{id} | `cli:write` | body |
| `inventory pricing-categories delete <id>` | DELETE /pricing-categories/{id} | `cli:write` | none |
| `inventory pricing-categories restore <id>` | POST /pricing-categories/{id}/restore | `cli:write` | none |

## Type vocabulary

The `--type` enum classifies the audience semantically (used by the booking widget for age-gating, ID-doc workflows, currency rules):

`ADULT | CHILD | INFANT | YOUTH | STUDENT | SENIOR | TRAVELLER | EU_CITIZEN | MILITARY | EU_CITIZEN_STUDENT`

Default: `TRAVELLER` (use when no specific audience type applies).

## Worked examples

### 1. Adult / Child split for a private sailing product

```bash
ADULT_ID=$(ceebee inventory pricing-categories create \
  --product-id 44 \
  --name "Adults" --type ADULT --min-age 18 \
  --format json | jq -r '.data.pricing_category.id')

CHILD_ID=$(ceebee inventory pricing-categories create \
  --product-id 44 \
  --name "Children" --type CHILD --min-age 4 --max-age 17 \
  --format json | jq -r '.data.pricing_category.id')
```

Now create the per-band tiers underneath each (see [pricing-tiers.md](pricing-tiers.md)):

```bash
ceebee inventory pricing-tiers create --pricing-category-id $ADULT_ID --amount 12500 --min 1
ceebee inventory pricing-tiers create --pricing-category-id $CHILD_ID --amount 10000 --min 1
```

### 2. List categories under one product

```bash
ceebee inventory pricing-categories list --product-id 44
```

### 3. Add a senior pricing band later

```bash
ceebee inventory pricing-categories create \
  --product-id 44 \
  --name "Seniors" --type SENIOR --min-age 65
```

### 4. Soft-delete + restore

```bash
ceebee inventory pricing-categories delete 22
# Removes the bucket AND cascade-deletes its child PricingTiers
ceebee inventory pricing-categories restore 22
# Restores the bucket, but child tiers stay deleted — restore them individually if needed
```

## Pitfalls

- ⚠️ **Reparenting is forbidden via PATCH.** Updating `product_id` is rejected by `UpdatePricingCategoryRequest::rules()` per spec. Create a new category if you need to move tiers across products.
- ⚠️ **Delete cascades to PricingTiers.** Deleting a category soft-deletes every tier underneath it. Restore restores the category only — tiers stay soft-deleted and need explicit `pricing-tiers restore <id>` calls.
- ⚠️ **`--min-age` / `--max-age` are advisory.** They're passed through to the booking widget for age-gating UX but don't enforce anything server-side. If a customer lies about age, you find out at the door.
- ⚠️ **No tiers created automatically.** Creating a category does NOT create a default tier. You have to follow up with `pricing-tiers create` — without a tier, the bucket has no fare and the product can't be priced.
- ⚠️ **No server-side dry-run on delete or restore.** CLI rejects `--dry-run` on those routes.
- ⚠️ **`--name` is translatable.** Send a bare string; the server wraps it as `{"en": "..."}`.

## See also

- [pricing-tiers.md](pricing-tiers.md) — the children of categories. Required reading.
- [products.md](products.md) — owns the categories via `product_id`.
- [product-options.md](product-options.md) — pricing categories are scoped to the product, not the option.
