# Locations

A `Location` is a physical place attached to a tenant, product, or business unit — start points, end points, stops along a tour, primary venue. Used by widgets and customer confirmation emails.

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory locations list` | GET /locations | `cli:read` | n/a |
| `inventory locations get <id>` | GET /locations/{id} | `cli:read` | n/a |
| `inventory locations create` | POST /locations | `cli:write` | body |
| `inventory locations update <id>` | PATCH /locations/{id} | `cli:write` | body |
| `inventory locations delete <id>` | DELETE /locations/{id} | `cli:write` | none |

## Type vocabulary

`PRIMARY | START | END | VISITED | SECONDARY` — same enum on list-filter, create, update, and read response. (`MEETING` was removed; if you see it in older docs, treat as `PRIMARY` or `START`.)

## Worked examples

### 1. Attach a START location to a sailing product

```bash
ceebee inventory locations create \
  --type START --name "Pont des Arts pontoon" \
  --address "Pont des Arts, 75001 Paris, France" \
  --attach-to product --attach-to-id 44 \
  --city Paris --country-code FR --postal-code 75001 \
  --latitude 48.8580 --longitude 2.3373
```

`--attach-to` is the polymorphic owner kind — one of `product | organisation | partner`. `--attach-to-id` is the owner row id. The CLI never exposes Eloquent FQCNs; the controller resolves the enum to the right relation.

### 2. Add an END + a SECONDARY waypoint

```bash
# End point
ceebee inventory locations create \
  --type END --name "Place du Trocadéro" \
  --address "Place du Trocadéro, 75016 Paris" \
  --attach-to product --attach-to-id 44 \
  --city Paris --country-code FR --postal-code 75016

# Side stop along the way
ceebee inventory locations create \
  --type SECONDARY --name "Café de Flore" \
  --address "172 Boulevard Saint-Germain, 75006 Paris" \
  --attach-to product --attach-to-id 44 \
  --city Paris --country-code FR --postal-code 75006
```

### 3. Update a location

```bash
ceebee inventory locations update 12 \
  --type PRIMARY --name "Pont des Arts (renamed)" \
  --address "Pont des Arts, 75001 Paris, France"
```

Update takes a deliberately narrower set than create: `--type`, `--name`, `--address`, `--latitude`, `--longitude`, `--google-place-id`. That is the whole list.

**The address components are write-once.** `--street-address`, `--city`, `--country-code`, `--postal-code` and `--region` exist on `create` only — `update` has neither the flags nor the underlying body fields, so `--data` is no escape hatch either. Get them right the first time; changing a location's city means deleting the row and recreating it (and `delete` is hard, see example 5).

They are also invisible on the way back: the read response carries no `city` / `country_code` / `postal_code` / `region` field at all. The only trace is the derived `address` accessor, which composes `street_address` + city + country into one string. So a wrong `--city` is neither fixable nor directly observable — you infer it from `address`.

`timezone` and `notes` are not flags on either verb. They exist as request-body fields (reachable via `--data`) and are accepted, but the columns don't exist, so they are dropped on persist and always read back null. Sending them is a no-op, not an error.

### 4. List by type

```bash
ceebee inventory locations list --type START --format json
```

### 5. Hard-delete (with the obvious trap)

```bash
ceebee inventory locations delete 12
```

Returns 409 if any published product still references the location — detach those products first (or unpublish them) before deleting.

## Pitfalls

- ⚠️ **Locations are NOT soft-deletable.** Delete is hard. There's no `restore` command. Use list-filter + `--type` to find the right row before deletion.
- ⚠️ **No server-side dry-run on delete.** CLI rejects `--dry-run` on this endpoint with a typed error.
- ⚠️ **`--attach-to-id` is enforced** — you'll get 422 if the id doesn't resolve to a record of the requested kind.
- ⚠️ **Update enum is the same as create** — older drafts had a narrower update set; that's gone. Both accept `PRIMARY|START|END|VISITED|SECONDARY`.
- ⚠️ **`address` vs `street_address`** — when both are sent, `street_address` wins; `address` falls back if street is omitted. The read response surfaces a derived `address` accessor (`street_address` + city + country) — don't expect to round-trip the literal string you wrote.
- ⚠️ **Address components are create-only and never read back.** `--street-address`, `--city`, `--country-code`, `--postal-code`, `--region` are on `create` and nowhere else: `update` has no such flags and `UpdateLocationRequest` has no such properties, so `--data` cannot reach them either. Nor does the read response return them. A typo'd city is unfixable in place and only visible through the composed `address` string. Delete and recreate.
- ⚠️ **`timezone` and `notes` are body-only, and go nowhere.** There is no flag for either on `create` or `update`; they exist solely as request-body fields you can reach through `--data`. The server accepts them and drops them — no columns — so the write returns 200 with no error and the field reads back null forever. Don't store anything you need in them.

## See also

- [products.md](products.md) — products carry their attached locations on the read response.
- [resources.md](resources.md) — for physical inventory (boats, guides) bound to a product option, use Resources, not Locations.
