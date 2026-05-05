# Customers

A `Customer` is the bookings-engine first-class customer model — the account record across multiple bookings. Read-only in V1; create/update happen through the public booking flow or admin UI. Soft-deletable.

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory customers list` | GET /customers | `cli:read` | n/a |
| `inventory customers show <id>` | GET /customers/{id} | `cli:read` | n/a |

## Worked examples

### 1. Find a customer by email

```bash
ceebee inventory customers list --email customer@example.com
```

Returns at most one row (email is effectively unique per tenant, but the schema is forgiving — assume 0..1).

### 2. Free-text search

Intent: find "Kowalski" across name and email.

```bash
ceebee inventory customers list --q "Kowalski"
```

`--q` searches the primary string fields (name, email, phone).

### 3. List Italian customers updated since last sync

```bash
ceebee inventory customers list \
  --country IT \
  --since "2026-04-01T00:00:00Z" \
  --limit 200
```

`--country` takes ISO 3166-1 alpha-2. `--since` filters on `updated_at` (UTC unless offset-suffixed).

### 4. Show one customer

```bash
ceebee inventory customers show cust_42 --format json
```

Response includes contact info + summary stats (total bookings, total spend in minor units, last-booking date).

### 5. Include soft-deleted customers

```bash
ceebee inventory customers list --q "test@" --include-trashed
```

Useful when reconciling old test data or GDPR-deleted records.

## Pitfalls

- ⚠️ **No write operations in V1.** No `create`, no `update`, no `delete`. Customer edits happen via the admin UI or public booking flow. Future Phase 2 will add CRUD.
- ⚠️ **Country filter is ISO alpha-2, not country name.** `--country UK` returns nothing — use `GB`. Same goes for Greece (`GR`) vs the colloquial.
- ⚠️ **Email is not strictly unique server-side.** Production data has duplicate-email rows (legacy migrations). When matching by email, prefer `--email` filter then iterate; don't assume a single result.

## See also

- [bookings.md](bookings.md) — bookings link to a `customer_id`.
- [guests.md](guests.md) — distinct concept: per-booking guests are not the same as Customers.
