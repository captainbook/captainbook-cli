# `ceebee inventory resource-calendar` — when a resource is busy

One read endpoint answering a single question: *what is occupying this resource, and when*. It spans bookings the resource is assigned to and explicit unavailability, in one list.

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory resource-calendar list` | GET /resource-calendar | `cli:read` | n/a |

## `--resource-id` is required, and takes up to ten

The spec marks it required at the query level, so the CLI refuses without it rather than spending a round trip on a 422. Ids come from `resources list`.

It accepts **several ids at once**, comma-separated, up to ten per call — which is why the endpoint exists in this shape. One call answers a roster-wide question; looping one id at a time spends ten requests on what the server does in one.

```bash
ceebee inventory resources list --format table          # find the resource
ceebee inventory resource-calendar list --resource-id res_12 --format table
```

## Worked examples

```bash
# What is this guide doing next month?
ceebee inventory resource-calendar list \
  --resource-id res_12 --from 2026-11-01 --to 2026-11-30 --format table

# Only the explicit unavailability, not bookings
ceebee inventory resource-calendar list --resource-id res_12 --event-types unavailable

# Everything except confirmed bookings, for the whole morning-shift roster
ceebee inventory resource-calendar list --resource-id res_12,res_13,res_14 \
  --event-types busy,unavailable --format table

# Narrow to one product option's events
ceebee inventory resource-calendar list --resource-id res_12 --product-option-id 48

# Page through a busy season
ceebee inventory resource-calendar list --resource-id res_12 --limit 100 --cursor "<cursor>"
```

## Flags

| Flag | Notes |
|---|---|
| `--resource-id` | **Required.** One or more ids, comma-separated, max 10 per call. From `resources list`. |
| `--from`, `--to` | `YYYY-MM-DD` window. |
| `--event-types` | Comma-separated subset of `booking`, `busy`, `unavailable`. Omit for all three. |
| `--product-option-id` | Positive integer. `0` is refused rather than silently ignored. |
| `--limit`, `--cursor` | Cursor pagination. |

## Pitfalls

- **This is not a capacity view.** It tells you when a resource is occupied, not whether a departure can still be booked — use `stats occupancy` or `availabilities list` for that. A resource with no calendar events is not necessarily free to assign.
- **`--product-option-id` narrows events, not resources.** It filters which of this resource's events you see; it does not ask "which resources could serve this option".
- **An empty list is a real answer.** It means nothing occupies the resource in that window, not that the resource does not exist — a bad id is a 404.
