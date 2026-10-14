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

## "Must travel with" (`--companions`)

A category can be gated on another one: *"a Child seat may only be booked when at least one Adult is in the same booking."* That rule lives on the dependent category as a list of **companion links**, each `{category_id, min_quantity}`.

```bash
# Children can only be booked alongside at least 1 Adult
ceebee inventory pricing-categories update $CHILD_ID \
  --companions "[{\"category_id\": $ADULT_ID, \"min_quantity\": 1}]"
```

Three rules decide what the list means:

- **ANY one link satisfies it.** Links are never added together. `[{A,1},{B,1}]` means "at least one A **or** at least one B", not both.
- **The write REPLACES the whole set.** `[]` clears every link; omitting `--companions` leaves them untouched. There is no add-one call.
- **No links means no rule** — the category is freely bookable.

Pass `@file.json` instead of an inline string for anything past a link or two:

```bash
ceebee inventory pricing-categories update 31 --companions @companions.json --dry-run
```

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

### 5. Gate a child category on adults, then remove the gate

```bash
# Require 2 adults per child booking
ceebee inventory pricing-categories update 31 \
  --companions '[{"category_id": 30, "min_quantity": 2}]'

# Later: drop the requirement entirely
ceebee inventory pricing-categories update 31 --companions '[]'
```

## Pitfalls

- ⚠️ **No `--since`, and sending `since=` is a 422.** The `pricing_categories` table carries no `created_at` / `updated_at` columns, so there is nothing for the filter to bound and `created_at` / `updated_at` are always null on the response. The 422 is deliberate — a silently-unfiltered page is what a polling client would misread as a delta. No incremental sync here: list in full and diff client-side.
- ⚠️ **Reparenting is forbidden via PATCH.** Updating `product_id` is rejected by `UpdatePricingCategoryRequest::rules()` per spec. Create a new category if you need to move tiers across products.
- ⚠️ **Delete cascades to PricingTiers, and silently drops every companion link.** Deleting a category soft-deletes every tier underneath it **and removes every "Must travel with" link from or to it** — so a category that required this one becomes freely bookable. Restore brings back neither: tiers need explicit `pricing-tiers restore <id>` calls, and the links are gone for good. Re-send `--companions` on every category that pointed at the deleted one.
- ⚠️ **`--data '{"companions": …}'` is validated exactly like the flag.** Both inputs assemble the same wire field, so the self-link, duplicate, unknown-key, 20-link and `min_quantity` 1–999 checks run whichever one you use, and the refusal names `--data` when that was the source. Your bytes are also carried through verbatim on that path, so a `category_id` past 2^53 is not rounded. (The wider `--data` number-rounding issue on other fields is still open — see TODOS.md.)
- ⚠️ **`--companions` REPLACES; it never merges.** Adding one link means re-sending the ones you are keeping. Send a single new link against a category that already has two and the other two are deleted — and nothing in the response says so, it just comes back with one link, which reads like it worked. Read the current set with `get <id>` first.
- ⚠️ **A companion with no pricing tiers is accepted, returned on read, and ignored.** Customers cannot book an unpriced category, so a link to one gates nothing until that category has tiers. A category whose every link is unpriced effectively has no rule — and the payload gives no hint of it.
- ⚠️ **`--is-internal` is refused while another category names this one.** Setting it true is a 422 for as long as some other category's `companions` points here; clear that link first. Internal categories cannot be companions either.
- ⚠️ **Loops are refused on update, including indirect ones.** Linking A→B when B already has to travel with A — directly or through a chain — is a 422, because neither would be bookable.
- ⚠️ **`--min-age` / `--max-age` are advisory.** They're passed through to the booking widget for age-gating UX but don't enforce anything server-side. If a customer lies about age, you find out at the door.
- ⚠️ **No tiers created automatically.** Creating a category does NOT create a default tier. You have to follow up with `pricing-tiers create` — without a tier, the bucket has no fare and the product can't be priced.
- ⚠️ **No server-side dry-run on delete or restore.** CLI rejects `--dry-run` on those routes.
- ⚠️ **`--name` is translatable.** Send a bare string; the server wraps it as `{"en": "..."}`.

## See also

- [pricing-tiers.md](pricing-tiers.md) — the children of categories. Required reading.
- [products.md](products.md) — owns the categories via `product_id`.
- [product-options.md](product-options.md) — pricing categories are scoped to the product, not the option.
