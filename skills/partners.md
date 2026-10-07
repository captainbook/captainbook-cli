# `ceebee inventory partners` — selling partners and channel partners

Read-only. Partners are the counterparties a booking can involve on either side: a **selling partner** (an agency that sold the trip) or a **channel partner** (an OTA or distribution channel). There is no write surface — the server mounts both routes outside any `cli:write` group and the spec publishes no create/update/delete.

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory partners list` | GET /partners | `cli:read` | n/a |
| `inventory partners get <id>` | GET /partners/{id} | `cli:read` | n/a |

## Why you need this

`bookings list --partner-id` takes a partner id, and nothing else in the CLI hands you one. Partner ids are not guessable and are not the same thing as a channel id (which is what `stats channels` reports). Start here, then filter bookings.

## Worked examples

```bash
# Who sells for this tenant?
ceebee inventory partners list --partner-type selling_partner --format table

# Find one by name, then look at their bookings
ceebee inventory partners list --q "atlas" --format json
ceebee inventory bookings list --partner-id 7 --format table

# Include soft-deleted rows when reconciling historical bookings whose partner
# has since been removed
ceebee inventory partners list --include-trashed --format table

# Incremental sync
ceebee inventory partners list --since 2026-10-01T00:00:00Z
```

## Flags

| Flag | Notes |
|---|---|
| `--partner-type` | `channel` or `selling_partner`. Omit for both. |
| `--q` | Free-text over partner name. |
| `--include-trashed` | Soft-deleted partners. Useful when an old booking references one. |
| `--since` | ISO 8601 lower bound on `updated_at`. |
| `--limit`, `--cursor` | Cursor pagination, as everywhere else. |

## Pitfalls

- **A partner is not a channel.** `--partner-id` on `bookings list` matches either side of the partner relationship; `stats channels` reports a `channel_id`, which is a different identifier and a string rather than an integer. They are not interchangeable.
- **`get <id>` accepts `--include-trashed` too.** Without it a soft-deleted partner is a 404, which reads as "no such partner" rather than "deleted".
