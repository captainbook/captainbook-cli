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
- `--capacity` (optional int): null = no per-resource cap (the resource doesn't bound seat count by itself; capacity is option-level). Set this when the resource has its own seat limit (a 6-pax boat). **Refused with 422 for `--category equipment`** — capacity is what makes a resource a booking's single main resource, and equipment is never that.

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
- ⚠️ **Pivot capacity is what makes a resource "main".** `Booking::attachResources()` reads the pivot capacity, so `attach --capacity N` doesn't just cap seats — it moves the resource into the single main slot the booking competes over. Setting it on an equipment resource is refused with 422 rather than silently nulled, because a resource that claims seats nothing downstream honours is worse than a rejected write.
- ⚠️ **Re-attaching is a no-op rewrite.** Idempotent: posting attach twice with different `--capacity` values updates the pivot, doesn't error. Use this on purpose to bump capacity without explicitly detaching.
- ⚠️ **No server-side dry-run on delete, restore, or detach.** CLI rejects `--dry-run` on those routes.
- ⚠️ **`category` is not free-form.** Server validates against the enum (`guide|asset|equipment|auxiliary`). The CLI's enum gate catches bad values before sending. Free-form lives on `--type`.
- ⚠️ **Attached resources without `Availability` won't constrain anything.** The `resourceables` pivot is only consulted when an Availability is materialized via the recurrence rule (or the dashboard). Bare option-level resource attach is meaningless until at least one Availability exists.

## Two different attachments: option-level vs booking-level

Don't conflate these — they use different endpoints, different id spaces, and different semantics.

| | `resources attach/detach` | `bookings set-resources` |
|---|---|---|
| Binds a resource to | a **ProductOption** (`resourceables` pivot) | a single **Booking** (`booking_resource` pivot) |
| Meaning | "this option can draw on this boat/guide" | "this specific trip is running on that boat, with these kits" |
| Shape | one resource per call, incremental | full desired state, replaces |
| Concurrency guard | none (idempotent rewrite) | `expected_resource_state_token`, 409 on stale |

Option-level attachment defines the *pool*; booking-level assignment picks *from* that pool for one trip. The `bookings available-resources` / `available-equipment-resources` / `available-auxiliary-resources` lists enumerate the legal picks — a resource that was never attached to the booking's ProductOption won't appear in any of them, and forcing it anyway returns `BOOKING_RESOURCE_CONFLICT` with a `*_RESOURCE_NOT_ON_PRODUCT_OPTION` code.

Which of the three lists a resource lands in is decided by the **pivot capacity**, not by `--category`: a non-auxiliary resource with a pivot capacity is the booking's single *main* resource, one without is *equipment*. That's why `attach-resource --capacity` is refused for an equipment resource — it would make the kit compete for the guide's slot.

To answer "which trips is this auxiliary resource on?", resolve the id from `resources list --category auxiliary`, then pass it to `bookings list --resource-id <id> --from … --to …`.

## See also

- [bookings.md](bookings.md) — assigning resources to an individual booking (`available-resources`, `available-equipment-resources`, `available-auxiliary-resources`, `set-resources`).
- [product-options.md](product-options.md) — the parent of resource attachments.
- [availabilities.md](availabilities.md) — `create-rule` materializes Availability rows that honor attached Resources.
- [products.md](products.md) — the schedule_type and is_private settings interact with resource constraints during booking.
