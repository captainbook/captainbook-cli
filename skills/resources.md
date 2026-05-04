# Resources

A `Resource` is a piece of physical or human inventory bound to a `ProductOption` to constrain its bookable capacity — a sailboat, a yoga studio, a senior guide, a snorkel kit. Without a Resource attachment, capacity is just a number on the option; with one, it's the boat / room / guide that's actually limited.

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory resources list` | GET /resources | `cli:read` | n/a |
| `inventory resources get <id>` | GET /resources/{id} | `cli:read` | n/a |
| `inventory resources create` | POST /resources | `cli:write` | body |
| `inventory resources update <id>` | PATCH /resources/{id} | `cli:write` | body |
| `inventory resources delete <id>` | DELETE /resources/{id} | `cli:write` | none |
| `inventory resources restore <id>` | POST /resources/{id}/restore | `cli:write` | none |
| `inventory resources attach <option-id>` | POST /product-options/{id}/resources | `cli:write` | body |
| `inventory resources detach <option-id> <resource-id>` | DELETE /product-options/{option_id}/resources/{resource_id} | `cli:write` | none |

## Vocabulary

- `--category` (enum): `guide | asset | equipment | auxiliary` — the kind of resource. Used by the dashboard to filter and group.
- `--type` (free-form string): the tenant-pickable label (`Sailboat`, `Senior Guide`, `Wetsuit Kit`, `Yoga Studio A`).
- `--capacity` (optional int): null = no per-resource cap (the resource doesn't bound seat count by itself; capacity is option-level). Set this when the resource has its own seat limit (a 6-pax boat).

## Worked examples

### 1. Create the Oceanis 449 and attach it to a sailing option

```bash
BOAT_ID=$(ceebee inventory resources create \
  --name "Oceanis 449" --type "Sailboat" \
  --category asset --capacity 8 \
  --format json | jq -r '.data.resource.id')

ceebee inventory resources attach 47 --resource-id $BOAT_ID
```

The attach writes a `resourceables` polymorphic pivot row. It's idempotent — re-attaching the same resource overwrites the pivot's optional fields (`capacity`, `seniority`).

### 2. Override the resource's capacity on a specific option

A 6-guide pool, but only 2 of them work the morning shift:

```bash
ceebee inventory resources attach 47 \
  --resource-id $GUIDE_POOL_ID \
  --capacity 2
```

`--capacity` here is the **pivot** capacity (option-specific), not the Resource-level default. Omit to inherit `Resource.capacity`.

### 3. Track guide seniority

```bash
ceebee inventory resources attach 47 \
  --resource-id $JEAN_GUIDE_ID \
  --seniority 3
```

`--seniority` is a pivot-level integer (0+) used by the dashboard to rank guides for assignment. No semantic enforcement server-side.

### 4. List by category

```bash
ceebee inventory resources list --category asset --limit 50
```

### 5. Detach when the boat is out for maintenance

```bash
ceebee inventory resources detach 47 $BOAT_ID
```

Returns 204 No Content. The pivot row is removed; the resource itself stays alive.

### 6. Soft-delete + restore the resource itself

```bash
ceebee inventory resources delete 2
# Resource soft-deleted. Existing pivot rows stay attached but reference a trashed row;
# the booking flow will reject availabilities backed by deleted resources.

ceebee inventory resources restore 2
```

## Pitfalls

- ⚠️ **Detach is hard, not soft.** No restore. Re-attach with the original `resource_id` if you need to undo.
- ⚠️ **Pivot capacity vs Resource capacity** — `attach --capacity N` overrides the Resource's default for that one product option. Don't confuse them: setting `Resource.capacity=8` then `attach --capacity 2` means "this resource normally seats 8, but on THIS option only 2 of those seats are available."
- ⚠️ **Re-attaching is a no-op rewrite.** Idempotent: posting attach twice with different `--capacity` values updates the pivot, doesn't error. Use this on purpose to bump capacity without explicitly detaching.
- ⚠️ **No server-side dry-run on delete, restore, or detach.** CLI rejects `--dry-run` on those routes.
- ⚠️ **`category` is not free-form.** Server validates against the enum (`guide|asset|equipment|auxiliary`). The CLI's enum gate catches bad values before sending. Free-form lives on `--type`.
- ⚠️ **Attached resources without `Availability` won't constrain anything.** The `resourceables` pivot is only consulted when an Availability is materialized via the recurrence rule (or the dashboard). Bare option-level resource attach is meaningless until at least one Availability exists.

## See also

- [product-options.md](product-options.md) — the parent of resource attachments.
- [availabilities.md](availabilities.md) — `create-rule` materializes Availability rows that honor attached Resources.
- [products.md](products.md) — the schedule_type and is_private settings interact with resource constraints during booking.
