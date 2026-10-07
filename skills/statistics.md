# `ceebee stats` — the statistics namespace

Read-only analytics over one tenant. Thirteen metrics, one shared period/comparison model, and a per-metric filter vocabulary the server **enforces** rather than suggests.

This is the cookbook the `inventory` resources have had all along and `stats` did not. If you only read one section, read [Filters are per-metric](#filters-are-per-metric-and-refused-when-unsupported) and [Dates are not what you assume](#dates-are-not-what-you-assume).

## Metrics

| Command | What it answers |
|---|---|
| `ceebee stats summary` | Dashboard KPIs in one call (revenue, bookings, customers, occupancy) |
| `ceebee stats revenue` | Gross/net revenue, commissions, tips, refunds |
| `ceebee stats bookings` | Booking volume, status breakdown, lead time |
| `ceebee stats products` | Product rankings |
| `ceebee stats resources` | Resource utilisation rankings |
| `ceebee stats customers` | Acquisition, retention, top spenders |
| `ceebee stats locations` | Location rankings |
| `ceebee stats channels` | Booking channel distribution |
| `ceebee stats occupancy` | Slot occupancy and capacity utilisation |
| `ceebee stats extras` | Extra/add-on sales performance |
| `ceebee stats discounts` | Discount code usage |
| `ceebee stats gift-certs` | Gift certificate issuance and redemption |
| `ceebee stats browsing` | Browsing funnel and visitor behaviour |

## Common flags

Every metric takes these:

```bash
--from YYYY-MM-DD      # default: 30 days ago
--to YYYY-MM-DD        # default: today
--granularity          # day (default) | week | month | quarter | year
--compare-from / --compare-to
--compare previous|year-ago   # shorthand; cannot combine with the explicit pair
--format json|table|csv
```

The CLI refuses a range longer than 365 days locally, before any request.

```bash
ceebee stats revenue --from 2026-01-01 --to 2026-03-31 --granularity month
ceebee stats revenue --from 2026-03-01 --to 2026-03-31 --compare year-ago
ceebee stats bookings --format csv > bookings.csv
```

## Filters are per-metric, and refused when unsupported

There is one filter vocabulary — `--business-unit-id`, `--product-id`, `--channel-id`, `--location-id`, `--resource-id`, `--day-of-week`, `--time-of-day-from`, `--time-of-day-to`, `--date-basis` — but **no metric implements all of it**, and the server answers `400` for one it does not implement rather than ignoring it.

That refusal is deliberate. Dropping an unsupported filter silently meant `browsing` with `product_id=12` returned every product's funnel labelled as that one product's. An honest number for the wrong question is worse than an error.

So the CLI only declares the flags a metric actually applies: if `--channel-id` does not appear in `ceebee stats occupancy --help`, that is the answer, not an omission.

| Metric | Filters it applies |
|---|---|
| `revenue` | business-unit, product, channel, location, resource, day-of-week |
| `bookings` | the above **plus** date-basis, time-of-day-from/to |
| `products`, `resources`, `customers`, `locations` | business-unit, product, channel, location, resource, date-basis |
| `channels` | business-unit, product, channel, location, resource, day-of-week |
| `extras`, `discounts` | business-unit, product, channel, location, resource |
| `occupancy` | business-unit, product, location |
| `summary` | business-unit, product, location |
| `browsing` | business-unit, day-of-week |
| `gift-certs` | business-unit |

Why the narrow ones are narrow:

- **`occupancy`** counts departure *slots*, so it joins no bookings: there is no channel to filter on, and no resource either — `booking_resource` records who *worked* a booking and an empty slot has nobody. It refuses `--date-basis` because it is dated by the departure already, and `--day-of-week` because it *groups* by weekday.
- **`gift-certs`** — a certificate is an amount, not a trip.
- **`browsing`** reads browsing events, which join no bookings at all.
- **`summary`** reports only what *all* its sections can honour. Five would accept `--channel-id` and `occupancy` cannot, so a channel-scoped summary would report the whole business's occupancy rate beside channel-scoped revenue, in one object, under one period.

## Dates are not what you assume

Every date parameter is **a day in the account's own timezone**, and so is every bucket in a time series. `--from 2026-01-01 --to 2026-01-01` for an account in Europe/Athens covers `2025-12-31T22:00Z` to `2026-01-01T22:00Z`.

Anything read off a *departure* is **product-local wall clock** instead, because that is what an availability stores. That covers `--day-of-week` and `--time-of-day-from/to` wherever a metric reads the departure, and `occupancy` throughout.

Check `meta.dated_by.event` in the response: when it is `trip_departure`, the period was interpreted in the **product's** calendar, not the account's.

### `--day-of-week` means different things per metric

It is always the weekday of *whatever the metric counts*, and the metrics do not agree:

- `bookings`, `channels` — the trip **departs**
- `revenue` — the payment **arrived**
- `browsing` — the visit **happened**

So "Saturday revenue" and "Saturday bookings" are different questions. If you need Saturday *trips* by money, use `products` or `resources` with `--date-basis trip` and read the revenue column.

### `--date-basis`

`booking` (default) dates every booking by when it was **sold**. `trip` dates it by **departure**. Available on `bookings`, `products`, `resources`, `customers` and `locations`.

`revenue` has no `--date-basis`: it is always dated by when the payment arrived. Its trip-dated equivalent is the revenue column of `products` or `resources` under `--date-basis trip`, not a re-dating of `revenue` itself.

```bash
# Trips departing in July, regardless of when they sold
ceebee stats bookings --from 2026-07-01 --to 2026-07-31 --date-basis trip

# Morning departures only
ceebee stats bookings --from 2026-07-01 --to 2026-07-31 \
  --time-of-day-from 09:00 --time-of-day-to 12:00
```

## Reading the response: three ways a number lies

The spec makes these client obligations, not niceties. Each one is a case where a
figure that is **wrong for the question** looks exactly like a figure that is right.

### `browsing` can answer 200 with no data

A `200` carrying `unavailable: true` is not data. It comes back when behavioural
analytics are off for the account, or when the range covers more events than the
report will scan. In that variant `funnel` and `by_type` are empty, `series` is
`[]`, and the counts are absent.

Chart it blindly and you report a funnel of zeroes, which reads as *nobody visits
us* rather than *tracking is off*. Branch on it, and read `reason` — a scan refusal
names a shorter range that would work.

```bash
ceebee stats browsing --from 2026-09-01 --to 2026-09-30 \
  | jq -e 'if .data.unavailable then "UNAVAILABLE: \(.data.reason)" | halt_error(1) else .data.funnel end'
```

### `locations` totals are short by the unplaceable

A product with neither a `PRIMARY` nor a `START` location cannot be placed, so its
bookings are absent from `locations[]`. This metric's totals then legitimately fall
short of `revenue`'s for the same window, which reads as a discrepancy if you do
not know why.

`data.unlocated_bookings` is how many were left out. Report it whenever it is not
zero rather than presenting a partial breakdown as the whole business.

```bash
ceebee stats locations --from 2026-09-01 --to 2026-09-30 | jq '{unlocated: .data.unlocated_bookings, rows: .data.locations}'
```

### Money has four states, and two of them are not numbers

Check `meta.money` before presenting any amount as the period's revenue:

| State | What you get | What it means |
|---|---|---|
| visible, complete | real amounts | safe to report |
| visible, narrowed | amounts minus channel bookings, `reason: see_money_of_ota_booking`, `excluded_bookings: N` | **partial** — a total with a non-zero `excluded_bookings` is not the period's revenue |
| withheld (permission) | every amount `null` | the caller may not see amounts at all |
| withheld (all-channel) | every amount `null` | every booking was a channel's, so a narrowed figure would be `0.00` and that reads as "earned nothing" |

`amounts_visible: false` means `null`, not `0`. And `meta.money` describes **its own
period only** — a comparison carries its own copy, and monetary entries in
`comparison.deltas` are `null` when either period is withheld.

## Pitfalls

- **`--channel-id` is a string**, not an integer, unlike every other id filter. Values come from `stats channels`.
- **An explicitly empty filter is refused.** `--channel-id ""` exits 12 rather than quietly returning an unfiltered result — the usual cause is an unset shell variable. Omitting the flag entirely is how you say "unfiltered".
- **An explicit `0` or negative id is refused** for the same reason. Real ids start at 1, so `--product-id 0` can only be a mistake, and silently dropping it returned the whole account's numbers presented as one product's.
- **`--time-from` / `--time-to` no longer exist.** They were renamed to `--time-of-day-from` / `--time-of-day-to`. The old names sent query keys the API never accepted, so they returned the whole period **unfiltered while presenting it as filtered**. If a script still uses them it now fails loudly, which is the point.
- **Money is in the tenant's display units** — `150.00` for EUR, `15000` for JPY. Check `meta.currency`.
- **Read `meta.filters`.** The server echoes back which filters it applied. If something is missing from that echo, it was not applied.
- **Statistics needs a subscription.** A plan without it answers `403`, as does a token whose user lacks `view_reports`.

## Exit codes

Shared with the rest of the CLI: `10` auth, `11` forbidden, `12` validation (including every local refusal above), `13` network, `14` JSON parse, `15` config, `16` server, `17` rate limited, `18` unexpected.

## See also

- [index.md](index.md) — setup, auth, profiles, output formats, global conventions.
- The statistics surface is documented upstream in `docs/api/statistics-openapi.yml`, vendored here at `api/statistics/statistics-openapi.yml` and asserted against this CLI by the statistics drift tests.
