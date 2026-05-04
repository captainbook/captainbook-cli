# Availabilities

An `Availability` is the per-date instance of a `ProductOption`: capacity for a given date, current bookable status, start/end times, and the active pricing tier set. Read endpoints answer "what's bookable on May 5?". The PATCH endpoint edits one row. The **bulk-update** endpoint async-edits every row matching `(product_option_id, from, to)` and is split into five subcommands by setting. The **create-rule** endpoint generates Availability rows from a recurrence pattern (the same job the dashboard's recurrence picker dispatches).

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory availabilities list` | GET /availabilities | `cli:read` | n/a |
| `inventory availabilities get <id>` | GET /availabilities/{id} | `cli:read` | n/a |
| `inventory availabilities update <id>` | PATCH /availabilities/{id} | `cli:write` | body |
| `inventory availabilities create-rule` | POST /availability-rules | `cli:write` | body |
| `inventory availabilities bulk-update capacity` | POST /availabilities/bulk-update | `cli:write` | body |
| `inventory availabilities bulk-update booking-status` | POST /availabilities/bulk-update | `cli:write` | body |
| `inventory availabilities bulk-update pricing` | POST /availabilities/bulk-update | `cli:write` | body |
| `inventory availabilities bulk-update start-time` | POST /availabilities/bulk-update | `cli:write` | body |
| `inventory availabilities bulk-update end-time` | POST /availabilities/bulk-update | `cli:write` | body |

`bulk-update` is split into five subcommands because the underlying `BulkAvailabilityUpdateJob` only handles one setting per call. To change capacity AND bookable status across a date range, run two commands.

## Worked examples

### 1. List availabilities for one option, May 2026

```bash
ceebee inventory availabilities list \
  --product-option-id po_88 \
  --from 2026-05-01 --to 2026-06-01 \
  --has-capacity true
```

Half-open range — `2026-06-01` is excluded. `--has-capacity true` filters to rows that still have seats.

### 2. Edit a single date

Intent: bump capacity from 12 to 15 on May 5 and mark the row available.

```bash
ceebee inventory availabilities update av_2026_05_05_po88 \
  --capacity 15 \
  --status available \
  --dry-run
```

Single-row PATCH; idempotent on retry. Drop `--dry-run` to commit.

### 3. Bulk-update capacity across May (async)

Intent: weather-driven seasonal capacity bump for `po_88`.

```bash
ceebee inventory availabilities bulk-update capacity \
  --product-option-id po_88 \
  --from 2026-05-01 --to 2026-06-01 \
  --value 18 --operator set_to
```

Returns `202 Accepted`. Stdout has the JSON envelope (with `bulk_update_id`, `total_matched`, `status: queued`); stderr has the grep-able signal:

```text
BULK_UPDATE_ACCEPTED bulk_update_id=018f5e2d-9a14-7c12-bb03-77a8c7c2e5ab
```

`--operator` accepts `set_to`, `increase_by`, `decrease_by` (lowercase per server enum). Exit code 0 means *queued*, not *applied*.

### 4. Bulk-update booking status (close the calendar)

Intent: close all of August due to a known venue closure.

```bash
ceebee inventory availabilities bulk-update booking-status \
  --product-option-id po_88 \
  --from 2026-08-01 --to 2026-09-01 \
  --is-bookable false \
  --dry-run
```

`--dry-run` returns 200 with `total_matched` (no jobs queued). Drop the flag for real.

### 5. Bulk-update pricing for two tiers

Intent: raise summer prices on the Adult and Child tiers for July.

```bash
ceebee inventory availabilities bulk-update pricing \
  --product-option-id po_88 \
  --from 2026-07-01 --to 2026-08-01 \
  --fare pricing_tier_id=pt_adult,amount=9500 \
  --fare pricing_tier_id=pt_child,amount=5500
```

`9500` = €95.00, `5500` = €55.00. Tiers omitted from `--fare` are left alone (server uses `replaceAll: false`).

### 6. Bulk-update times

Intent: shift the start time of every August availability to 9:30, keep end at 11:30.

```bash
ceebee inventory availabilities bulk-update start-time \
  --product-option-id po_88 \
  --from 2026-08-01 --to 2026-09-01 \
  --start-time 09:30 --end-time 11:30
```

`start-time` and `end-time` subcommands take both fields plus optional `--day-count` for multi-day tours.

### 7. Generate Availabilities from a recurrence (NEW)

Intent: every Saturday 2pm–6pm and every Wednesday 8am–6pm, May–August, on product option 47.

```bash
# Saturdays
ceebee inventory availabilities create-rule \
  --product-option-id 47 \
  --start-date 2026-05-01 --end-date 2026-08-31 \
  --weekdays 6 --start-time 14:00 --end-time 18:00 \
  --dry-run                                # preview first

# Wednesdays
ceebee inventory availabilities create-rule \
  --product-option-id 47 \
  --start-date 2026-05-01 --end-date 2026-08-31 \
  --weekdays 3 --start-time 08:00 --end-time 18:00
```

`--weekdays` uses PHP's `format('w')` convention: **Sunday=0 … Saturday=6**. Pass multiple weekdays as a comma-separated list (`--weekdays 3,6`). The rule itself is NOT stored — it's a one-shot generator that fans out via `CreateBatchAvailabilityJob`. Once dispatched, materialized rows are queryable via `availabilities list`.

**Dry-run** returns 200 + `total_matched` + `status: preview`. **Real** returns 202 + `status: queued` + `bulk_update_id`. **No-op** (zero weekday matches in the date range): 200 + `status: no_op` (nothing dispatched).

For `date`-type products, `--start-time`/`--end-time` are ignored (slots span full days). For `datetime` products both are required. `--add-days-count` extends the `to` timestamp for multi-day events.

## Pitfalls

- ⚠️ **Bulk-update is async and has no in-band completion signal in V1.** Exit 0 + `BULK_UPDATE_ACCEPTED` on stderr means the audit row was created and jobs queued on `inventory`. Confirm by polling `availabilities list` or by reading the `BulkAvailabilityUpdate` audit row server-side. Phase 2 will add `GET /availabilities/bulk-updates/{id}`.
- ⚠️ **One setting per bulk call.** `capacity AND booking-status` requires two calls; the underlying job dispatcher can only carry one setting at a time. The CLI enforces this by exposing five separate subcommands.
- ⚠️ **Date range is half-open `[from, to)`.** `--from 2026-05-01 --to 2026-06-01` matches every May date, NOT June 1.
- ⚠️ **Timezone:** dates are interpreted in the tenant's `Organisation.timezone`. A rule for "all of August in tenant TZ" is not the same as "all of August UTC" — server uses tenant TZ.
- ⚠️ **Pricing bulk-update is additive, not replacive.** Tiers omitted from `--fare` keep their existing fares. To zero out a tier across a range, include it explicitly with the new amount.

## See also

- [product-options.md](product-options.md) — `--product-option-id` is required for bulk-update.
- [pricing-tiers.md](pricing-tiers.md) — fares used by `bulk-update pricing` reference tier IDs.
- [bookings.md](bookings.md) — bookings consume availability capacity.
