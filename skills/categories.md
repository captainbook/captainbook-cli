# Categories

A `Category` groups Products in the catalog ("Tours", "Classes", "Equipment"). Many-to-many with Products via `category_ids[]` on the Product. CRUD with **hard-delete** (FK-protected).

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory categories list` | GET /categories | `cli:read` | n/a |
| `inventory categories show <id>` | GET /categories/{id} | `cli:read` | n/a |
| `inventory categories create` | POST /categories | `cli:write` | body |
| `inventory categories update <id>` | PATCH /categories/{id} | `cli:write` | body |
| `inventory categories delete <id>` | DELETE /categories/{id} | `cli:write` | none |

## Worked examples

### 1. List all categories

```bash
ceebee inventory categories list --limit 200
```

Returns `{id, name, slug, position, updated_at}`.

### 2. Create a new "Workshops" category

```bash
ceebee inventory categories create \
  --name "Workshops" \
  --slug workshops \
  --position 5 \
  --dry-run
```

### 3. Rename a category

```bash
ceebee inventory categories update cat_42 --name "Tours and Excursions"
```

The slug is NOT auto-updated when the name changes — pass `--slug` explicitly if you want the URL to follow.

### 4. PREVIEW deletion (FK check)

Intent: confirm no products still reference `cat_old`.

```bash
ceebee inventory categories delete cat_old 2>&1 | head -5
```

If any published product references the category, server returns `409 RESOURCE_IN_USE`. Detach references first via `products update <id> --category-ids …` (the new array, with `cat_old` omitted).

### 5. List products attached to a category, before deleting it

```bash
ceebee inventory products list --category workshops --format json | jq '.data[] | {id, title}'
```

Use this output to decide whether to detach or delete one-by-one.

## Pitfalls

- ⚠️ **HARD delete with FK guard.** No soft-delete for categories. Server returns `409 RESOURCE_IN_USE` (with stable `code` field) if any published Product references the category. Detach references via Product `update --category-ids` first, or accept the 409 as a "not safe to delete" signal.
- ⚠️ **No server-side dry-run on delete.** CLI rejects `--dry-run`. The 409 itself is the dry-run substitute — the call fails fast and idempotently when not safe.
- ⚠️ **No restore endpoint.** Once deleted, a category is gone (hard delete). Re-create with `categories create` if needed; existing Products that referenced the deleted category will need their `category_ids[]` re-set.
- ⚠️ **Renaming the name does not change the slug.** Customer-facing URLs rely on slug — be deliberate about updating both together.

## See also

- [products.md](products.md) — `--category-ids[]` attaches/detaches.
