# Categories

A `Category` groups Products in the catalog ("Tours", "Classes", "Equipment"). Many-to-many with Products via `category_ids[]` on the Product.

**Read-only at the tenant level.** Categories are managed centrally by the CaptainBook platform — tenants and the CLI cannot create, update, or delete them. The catalog is curated to keep the public taxonomy consistent across all tenants.

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory categories list` | GET /categories | `cli:read` | n/a |
| `inventory categories get <id>` | GET /categories/{id} | `cli:read` | n/a |

## Worked examples

### 1. List the full catalog

```bash
ceebee inventory categories list --limit 200
```

Returns `{id, name, slug, description, position, product_count, created_at}`. `slug`, `description`, and `position` may read as `null` / `0` against today's schema (the `product_categories` table only persists `id` + `name`; the model has accessors for the others but they're stubs).

### 2. Find category IDs for a target audience

```bash
ceebee inventory categories list --format json \
  | jq -r '.data[] | select(.name | test("walking|culinary|sailing"; "i")) | "\(.id)\t\(.name)"'
```

Use the integer IDs in `products create --category-ids "18,87,7"` to attach a product to multiple buckets.

### 3. Inspect one category

```bash
ceebee inventory categories get 18 --format json
```

`product_count` is set only when the `products` relation is eager-loaded by the controller; otherwise null.

### 4. List products attached to a category

```bash
ceebee inventory products list --category 18 --format json \
  | jq '.data[] | {id, title, status}'
```

Useful for "what does this category currently hold?" reviews.

## Pitfalls

- ⚠️ **No write operations.** `categories create / update / delete` do not exist. The gen client carries the methods (the spec previously had them) but they're intentionally not bound at the CLI layer. Looking for a way to "add a category" is a wrong-tool sign — escalate to platform.
- ⚠️ **`category_ids[]` is integer, not string.** When passing to `products create` / `update`, use `--category-ids "18,87,7"` (kebab-case flag, intSlice). The spec previously typed these as strings; the new contract is integers.
- ⚠️ **`slug` / `description` / `position` may be null.** Their underlying columns don't exist on `product_categories` today — model accessors return null. Don't rely on these fields for filtering or display.
- ⚠️ **`category_ids` on `products create` may 404.** If passing categories at create time fails with "not found", create the product without categories then PATCH `--category-ids` afterwards (a known server-side resolver bug — see PR #5 server-team list).

## See also

- [products.md](products.md) — `--category-ids[]` attaches a product to one or more categories.
