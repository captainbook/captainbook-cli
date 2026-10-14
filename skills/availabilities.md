# Availabilities

An `Availability` is the per-date instance of a `ProductOption`: capacity for a given date, current bookable status, start/end times, and the active pricing tier set. Read endpoints answer "what's bookable on May 5?" — and with `--include-pricing`, "at what price?". The PATCH endpoint edits one row — capacity, status, and (new) per-slot fares. The **bulk-update** endpoint async-edits either the rows named by `--availability-ids` or every row matching `(product_option_id, from, to)`, and is split into five subcommands by setting. The **delete** / **bulk-delete** endpoints soft-delete rows — `bulk-delete` by exact ids or across a date range — and both reject the request with 409 `AVAILABILITY_HAS_CONFIRMED_BOOKING` if any matched row carries a confirmed booking. The **create-rule** endpoint generates Availability rows from a recurrence pattern (the same job the dashboard's recurrence picker dispatches).

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory availabilities list` | GET /availabilities | `cli:read` | n/a |
| `inventory availabilities get <id>` | GET /availabilities/{id} | `cli:read` | n/a |
| `inventory availabilities create` | POST /availabilities | `cli:write` | body |
| `inventory availabilities update <id>` | PATCH /availabilities/{id} | `cli:write` | body |
| `inventory availabilities delete <id>` | DELETE /availabilities/{id} | `cli:write` | body |
| `inventory availabilities create-rule` | POST /availability-rules | `cli:write` | body |
| `inventory availabilities bulk-update capacity` | POST /availabilities/bulk-update | `cli:write` | body |
| `inventory availabilities bulk-update booking-status` | POST /availabilities/bulk-update | `cli:write` | body |
| `inventory availabilities bulk-update pricing` | POST /availabilities/bulk-update | `cli:write` | body |
| `inventory availabilities bulk-update start-time` | POST /availabilities/bulk-update | `cli:write` | body |
| `inventory availabilities bulk-update end-time` | POST /availabilities/bulk-update | `cli:write` | body |
| `inventory availabilities bulk-delete` | POST /availabilities/bulk-delete | `cli:write` | body |

`bulk-update` is split into five subcommands because the underlying `BulkAvailabilityUpdateJob` only handles one setting per call. To change capacity AND bookable status across a date range, run two commands.

`bulk-delete` is **synchronous** (unlike `bulk-update`) — the response carries `total_deleted` directly, no `BULK_UPDATE_ACCEPTED` signal, no polling.

Both bulk endpoints take the **same two scopes, exactly one of them**: `--availability-ids` names the exact rows (up to 1000), or `--product-option-id` + `--from` + `--to` takes every departure of that option in the half-open range. Sending both is a 422; so is sending neither.

One asymmetry between them: an id list **may span product options on `bulk-delete`**, but `bulk-update pricing` refuses a list spanning more than one option — each product has its own pricing tiers, so there is no shared answer to which tier a fare belongs to. The other four `bulk-update` settings accept a cross-option list.

## Worked examples

### 1. List availabilities for one option, May 2026

```bash
ceebee inventory availabilities list \
  --product-option-id 88 \
  --from 2026-05-01 --to 2026-06-01 \
  --has-capacity=true
```

Half-open range — `2026-06-01` is excluded. `--has-capacity=true` filters to rows that still have seats.

### 2. Edit a single date

Intent: bump capacity from 12 to 15 on May 5 and mark the row available.

```bash
ceebee inventory availabilities update av_2026_05_05_po88 \
  --capacity 15 \
  --status available \
  --dry-run
```

Single-row PATCH; idempotent on retry. Drop `--dry-run` to commit.

`--status` accepts `available` and `blocked` only. `cancelled` was documented for a while but never implemented — the server validates `in:available,blocked` and maps the value onto the boolean `is_open_for_booking`, so sending `cancelled` is a 422. `--status` and `--is-bookable` are **not** two spellings of one field. `--status` maps onto `is_open_for_booking` — the operator's open/closed state for the slot. `--is-bookable` independently tracks the *bulk-close* state and reads `false` after a `bulk-update booking-status` close, so operators can tell a slot closed by a bulk action from one closed by hand. Setting both in one call is legal and writes both.

### 2b. Reprice ONE slot (`update --fares`)

Intent: the 09:00 departure on May 5 is a private charter at a premium; the 14:00 departure on the same date is not.

```bash
ceebee inventory availabilities update av_2026_05_05_0900_po88 \
  --fares '[{"pricing_tier_id":"pt_adult","amount":12000}]' \
  --dry-run
```

**This is the only way to reprice exactly one slot.** `bulk-update pricing` is date-range scoped, so on a date carrying several sessions it writes every one of them — reaching for it here would also reprice the 14:00.

`--fares` takes the same JSON array as the bulk form, and writes `availability_pricing_tier` for this availability only. Tiers not listed are left alone. Sending `"amount": null` deletes the override (see example 5b). Rejections are 422: a `pricing_tier_id` from another product's ladder, or an availability with no ProductOption behind it (Resource-backed slots have no pricing tiers) — as is an empty `--fares` array, which the CLI catches before the request goes out.

Capacity/status and fares sent in the same call commit in **one transaction**, so a failure can't leave the capacity change applied and the pricing write missing:

```bash
ceebee inventory availabilities update av_2026_05_05_0900_po88 \
  --capacity 8 \
  --fares '[{"pricing_tier_id":"pt_adult","amount":12000}]'
```

With `--fares` present, `diff.before` / `diff.after` carry the availability with its `pricing_tiers[]` overlay, so the effective amount and `is_override` are comparable either side of the write.

### 3. Bulk-update capacity across May (async)

Intent: weather-driven seasonal capacity bump for `88`.

```bash
ceebee inventory availabilities bulk-update capacity \
  --product-option-id 88 \
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
  --product-option-id 88 \
  --from 2026-08-01 --to 2026-09-01 \
  --is-bookable=false \
  --dry-run
```

`--dry-run` returns 200 with `total_matched` (no jobs queued). Drop the flag for real.

### 5. Bulk-update pricing for two tiers

Intent: raise summer prices on the Adult and Child tiers for July.

```bash
ceebee inventory availabilities bulk-update pricing \
  --product-option-id 88 \
  --from 2026-07-01 --to 2026-08-01 \
  --fares '[{"pricing_tier_id":"pt_adult","amount":9500},
            {"pricing_tier_id":"pt_child","amount":5500}]'
```

`--fares` is a **single JSON array**, not a repeatable key=value flag — every tier goes in one string. `9500` = €95.00, `5500` = €55.00. Tiers omitted from `--fares` are left alone (server uses `replaceAll: false`). An empty array is a 422 — the CLI rejects it locally before the request goes out.

This writes the `availability_pricing_tier` pivot: each matched slot gets a per-slot fare that overrides the tier's catalogue `amount`. Verify with `--include-pricing` (example 10).

**Range scope bites on multi-session dates.** Every session on a matched *date* is written, including ones that had no override before. If a date carries a 09:00 and a 14:00 and you only mean one of them, use `availabilities update <id> --fares` (example 2b).

#### Preview it properly with `--dry-run`

```bash
ceebee inventory availabilities bulk-update pricing \
  --product-option-id 88 \
  --from 2026-07-01 --to 2026-08-01 \
  --fares '[{"pricing_tier_id":"pt_adult","amount":9500}]' \
  --dry-run --format json \
| jq '{total_matched, total_changed, preview_truncated,
       sample: [.diff.after.availabilities[] | select(.changed) | {id, starts_at, fares}]}'
```

`setting=pricing` is the one setting whose dry-run carries a real diff. `total_matched` alone is just the date-range row count — identical whether your proposed amount matches the current fare or is wildly different — so it cannot answer *"will this change anything?"*. The pricing preview adds:

- `total_changed` — matched slots whose effective price or pinned/unpinned state would move, counted over the **whole** range.
- `diff.before` / `diff.after` — the first `preview_limit` matched slots with their current and proposed fares. `changed` appears on the `after` side only.
- `preview_truncated` — true when the range is longer than that listing. The **counts still cover everything**; only the listing is cut.

A slot with no override today that is being written to the catalogue value still counts as changed: the write pins it.

### 5b. Hand a range back to the catalogue price (`amount: null`)

Intent: the July premium is over; the Adult tier should follow the catalogue again.

```bash
ceebee inventory availabilities bulk-update pricing \
  --product-option-id 88 \
  --from 2026-07-01 --to 2026-08-01 \
  --fares '[{"pricing_tier_id":"pt_adult","amount":null}]'
```

`"amount": null` **deletes** the `availability_pricing_tier` row so the slot follows the catalogue price again. It works identically on the single-slot `availabilities update --fares`.

**Writing the catalogue value by hand is not a substitute.** The pivot row survives, `is_override` stays `true`, and the slot silently keeps the old number the next time the tier is repriced in the catalogue. If you want a slot to *track* the catalogue, you must send `null`.

The `amount` key must be **present** in every entry — that is what makes the forget-to-serialise case an error instead of a silent price wipe. The CLI enforces this locally: an entry with no `amount` key is rejected before the request is built.

### 6. Bulk-update times

Intent: shift the start time of every August availability to 9:30, keep end at 11:30.

```bash
ceebee inventory availabilities bulk-update start-time \
  --product-option-id 88 \
  --from 2026-08-01 --to 2026-09-01 \
  --start-time 09:30 --end-time 11:30
```

`start-time` and `end-time` subcommands take both fields plus optional `--day-count` for multi-day tours.

### 7. Soft-delete a single date

Intent: pull May 5 off the calendar entirely (e.g. private buyout cancelled).

```bash
ceebee inventory availabilities delete av_2026_05_05_po88 --dry-run
# 200 + would_apply + diff.before for the row; nothing deleted yet
ceebee inventory availabilities delete av_2026_05_05_po88
# 204 No Content
```

If the row has a confirmed Booking attached, both calls return 409 `AVAILABILITY_HAS_CONFIRMED_BOOKING` (the precheck runs even on `--dry-run`). Cancel or move the booking first, then retry.

**Channel retraction.** If the row's ProductOption is mapped to GetYourGuide, the delete also queues a `vacancies: 0` push so GYG stops selling the slot rather than waiting for its next scheduled pull — GYG has no delete endpoint, so zeroing vacancies is the only retraction there is. It's best-effort and asynchronous: the `204` means the row is gone locally, **not** that GYG has been told. The push is skipped when the slot is in the past or past the 90-day horizon, when `bookable_type` isn't a ProductOption, when another live availability still occupies the same datetime, or when the option's product is already gone. Dry-run queues nothing. Channels other than GYG aren't wired up yet.

### 8. Bulk soft-delete, by id or across a date range (synchronous)

Intent: rip every August slot off `88` because the venue closed for the month.

```bash
ceebee inventory availabilities bulk-delete \
  --product-option-id 88 \
  --from 2026-08-01 --to 2026-09-01 \
  --dry-run
# 200 + status: "preview" + total_matched: <N>
```

**Reach for `--availability-ids` whenever the request contains an exception.** A range deletes everything it matches, so *"remove the duplicates that rule just created, keep the ones that were already there"* has no range that expresses it — old and new sit on the same option inside the same dates. Name the rows instead:

```bash
# Browse the duplicates, then delete exactly those.
# The list endpoint pages at 50 by default (200 max) and the CLI does NOT
# follow the cursor for you, so accumulate across pages or you will silently
# collect only the first page.
set -u
CURSOR=""; IDS=""
while : ; do
  # Build the cursor argument positionally. Do NOT write ${CURSOR:+--cursor "$CURSOR"}:
  # bash splits that into two words but zsh keeps it as one, so the same line
  # works in one shell and fails with an unknown flag in the other.
  set --
  if [ -n "$CURSOR" ]; then set -- --cursor "$CURSOR"; fi

  PAGE=$(ceebee inventory availabilities list \
    --product-option-id 88 --from 2027-06-01 --to 2027-09-01 \
    --limit 200 "$@" --format json) || { echo "list failed; not deleting" >&2; return 1 2>/dev/null || exit 1; }

  MATCHED=$(printf '%s' "$PAGE" | jq -r '[.data[] | select(.starts_at | contains("T07:00")) | .id] | join(",")')
  # Skip empty pages. Appending one would put an empty element in the list
  # ("1,2,,5"), and --availability-ids runs strconv.Atoi on every element, so
  # the delete dies with `parsing "": invalid syntax` naming neither the loop
  # nor the doc.
  if [ -n "$MATCHED" ]; then IDS="${IDS:+$IDS,}$MATCHED"; fi

  CURSOR=$(printf '%s' "$PAGE" | jq -r '.pagination.cursor_next // empty')
  [ -n "$CURSOR" ] || break
done

[ -n "$IDS" ] || { echo "nothing matched; nothing to delete" >&2; return 0 2>/dev/null || exit 0; }

# Chunk at the 1000-id cap. A longer list is refused locally — AFTER you have
# paid for every list page above.
printf '%s' "$IDS" | tr ',' '\n' | xargs -n 1000 | tr ' ' ',' | while read -r CHUNK; do
  ceebee inventory availabilities bulk-delete --availability-ids "$CHUNK" --dry-run
done
```

Each chunk is its own transaction and its own confirmed-booking precheck, so a
failure part way through leaves earlier chunks deleted: the all-or-nothing
guarantee is per call, not per cleanup.

Details that example gets right on purpose, because each one silently produces
"no duplicates found" when you get it wrong:

- **`starts_at`, not `start_time`.** The Availability resource has no
  `start_time` field — it emits `starts_at`, a full ISO datetime in the
  product's timezone. A `select(.start_time == "07:00")` matches zero rows and
  reads as a clean calendar.
- **No `xargs -I`.** BSD/macOS `xargs` refuses a post-replacement argument past
  255 bytes, which a 40-id list already exceeds, so the one-call batch this
  endpoint exists for dies with "command line cannot be assembled". A shell
  variable is bounded by `ARG_MAX` instead, and a full 1000-id list is ~7 KB.
- **Quote `"$IDS"`, and do not pass it empty.** `--availability-ids ""` is
  refused by the flag parser itself (`strconv.Atoi: parsing ""`), not by the
  scope gate, so the error names the wrong layer; the guard above exits before
  that happens.
- **Page to the end.** Without the cursor loop the list stops at 50 rows, the
  delete succeeds on those 50, and `total_deleted` matching your list length
  reads as a finished cleanup. Count the pages against every row in the RANGE,
  not just the matches: the time filter is client-side jq, so the originals the
  duplicates collide with are paged too. The worked case here is 1,137
  duplicates sitting beside at least as many originals — roughly a dozen pages
  at `--limit 200`, then two `bulk-delete` calls at the 1000-id cap.
  `total_deleted` equal to your id count is evidence the CALL worked, never
  that the cleanup is complete.
- **A page that matches nothing must not be appended.** The loop above guards
  it; a naive `IDS="$IDS,$MATCHED"` produces `1,2,,5` from a middle empty page
  and a trailing comma from a final one, both of which the flag refuses.

One gotcha when adapting it: `--product-option-id` is declared an **integer**
flag on `availabilities list` and a **string** flag on the two bulk endpoints.
That is a difference in where the refusal comes from, not in what is valid —
the wire contract is `PositiveIntegerId` on every one of them, meaning an
integer or a digit string matching `^[1-9][0-9]*$`. So `list` refuses a
non-numeric id locally, while the bulk endpoints accept it locally and earn a
server 422. Always pass the bare number.

Up to 1000 ids, and they may span several product options. An id that resolves to nothing — deleted, mistyped, or not backed by a ProductOption — is a **422 naming it**, never a silent drop, so `total_matched` under the id scope is exactly the length of your list rather than a prediction. (That exactness is the point: a partial delete reported as `total_deleted: 167` over a list of 170 would leave you unable to tell which three survived.)

Drop `--dry-run` to commit. The response carries `status: "deleted"` and `total_deleted: <N>` — synchronous, no `BULK_UPDATE_ACCEPTED` signal, no polling.

If a committed delete removed **nothing**, the CLI writes one line to stderr:

```
BULK_DELETE_NO_OP deleted=0 hint=the scope matched no live departure; it may already have been deleted, or the range may not be the one you meant — confirm with `availabilities list --include-trashed` before re-running
```

It means the scope matched no live departure: either those rows were already deleted, or the scope is not the one you meant. On a destructive command, "removed nothing" and "removed 412" must not read the same, so the CLI says so rather than leaving you to notice `total_deleted: 0`. The line goes to stderr, so stdout stays parseable JSON.

It is **not** how you find out about an empty range. `--from 2026-08-01 --to 2026-08-01` reads as "that one day" and actually means an empty half-open window, but the CLI refuses that before sending — `--to` must be strictly after `--from` on BOTH bulk endpoints, and the server answers 422 either way. A single day is `--from D --to D+1`. So if you see `BULK_DELETE_NO_OP` the range was well-formed and simply had nothing left in it; confirm with `availabilities list --include-trashed` before re-running. If any matched row has a confirmed booking, the entire request is rejected with 409 `AVAILABILITY_HAS_CONFIRMED_BOOKING` (no rows touched); `error.details.total_blocked` plus `sample_availability_ids` (up to 20) identify the blockers — narrow the range or cancel/move the bookings before retrying.

The chunked write runs in **one transaction**, so a failure part-way through a 1,500-row delete leaves the range untouched rather than half gone — the 1000-row chunking is an implementation detail and never a partial result.

The cascade `Availability → pricingTiers` does NOT run on `bulk-delete` (the server uses a single bulk UPDATE that bypasses model events). This is intentional: pricing tiers are M:N with availabilities, so cascading from one row would soft-delete tier rows still referenced by other availabilities.

**Channel retraction is partial on bulk-delete.** Because the bulk UPDATE fires no model events, the server announces the removal explicitly: GYG-mapped options get a `vacancies: 0` push for the deleted slots — one announcement per option, derived from the rows actually deleted, so an id-scoped call spanning three options retracts from all three. But **only future slots inside the 90-day horizon are pushed** — a year-long range deletes every matched row and retracts only the near-term ones, leaving the rest for GYG's next scheduled pull. A datetime that still has another live availability behind it is never retracted. Like the single delete, this is best-effort and asynchronous: `200` + `total_deleted` doesn't mean GYG has been told.

### 9. Generate Availabilities from a recurrence (NEW)

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

Response shapes, which differ in a way that matters for scripting:

| Call | Status code | `status` field |
|---|---|---|
| Dry run | 200 | **absent** — `would_apply: true` is what marks a preview |
| No-op (zero weekday matches, or every match already exists) | 200 | `no_op` |
| Committed | 202 | `queued` |

Read the preview as **JSON**, the default for mutations. With an explicit
`--format table` the renderer takes the diff-envelope path and prints only the
field diff, dropping `notes`, `rule.other_departures_sample`, `would_create`
and `skip_reasons` — the very numbers this section tells you to check.

Gating on `status == "preview"` never matches: the spec says a dry-run preview carries no `status` at all, and the only value the 200 declares is `no_op`. Read `would_apply` to tell a preview from a no-op.

**With more than one departure time, read `rule.times[]`.** `would_create` and `total_matched` are sums across every time in the rule, so a time that would create nothing is invisible in the top-level numbers — a rule promising 186 departures can be 62 from one time and 0 from another, and only the per-time breakdown shows it.

#### This endpoint ADDS; it never replaces

`skip_reasons.already_exists` matches the exact `(from, to)` pair. So a rule aimed at a season that already runs **thirty minutes away** skips nothing: every occurrence is new, `would_create` reads exactly as it would for an empty calendar, and every day ends up selling twice. That put 1,137 duplicate departures on four fishing trips across seven approvals, each preview promising 172 departures and each one correct.

The dry run names the collision rather than refusing it — which schedule you want is not the server's call:

- **`notes.other_departures_exist`** — how many occurrences would land on a day the option already sells at another time.
- **`rule.other_departures_sample`** — up to three of those days with the times already on them (`2027-06-07 already has 07:00`), timezone-relabelled like `sample_dates`.

Read both on **every** preview before approving:

```bash
ceebee inventory availabilities create-rule \
  --product-option-id 47 \
  --start-date 2027-06-01 --end-date 2027-09-01 \
  --weekdays 1,2,3,4,5 --start-time 07:30 --end-time 12:00 \
  --dry-run --format json \
| jq '{would_create: .data.would_create,
       collides_with_existing: .data.notes.other_departures_exist,
       sample: .data.rule.other_departures_sample}'
```

`notes.dst_shifted_twin_exists` is the **disjoint** subset of the same fact caused by our own pre-1.17.0 DST arithmetic; a row counted there is never counted in `other_departures_exist`. One is a repair of our arithmetic, the other a decision about which of two schedules you want.

#### Replacing a season: delete FIRST, then create

Replacing is two calls, and **the order is not interchangeable**:

```bash
# 1. Delete the OLD departures — before the rule, never after
ceebee inventory availabilities bulk-delete --availability-ids 8812,8813,8814

# 2. Then create the corrected schedule
ceebee inventory availabilities create-rule \
  --product-option-id 47 --start-date 2027-06-01 --end-date 2027-09-01 \
  --weekdays 1,2,3,4,5 --start-time 07:30 --end-time 12:00
```

`create-rule` is asynchronous (202, then `CreateBatchAvailabilityJob` writes the rows) while `bulk-delete` is synchronous. A **range-scoped** delete issued *after* the rule therefore takes the corrected departures too if the worker has already run, and only the old ones if it has not — a coin toss on queue latency, with an **empty season** as one of the outcomes. The id scope is immune to it: ids name rows that exist now, so they cannot reach a departure the worker has not written yet.

For `date`-type products, `--start-time`/`--end-time` are ignored (slots span full days). For `datetime` products both are required. `--add-days-count` extends the `to` timestamp for multi-day events.

### 10. Read per-slot pricing (`--include-pricing`)

Intent: check which September slots carry a fare override rather than the catalogue price.

```bash
ceebee inventory availabilities list \
  --product-option-id 88 --from 2026-09-01 --to 2026-10-01 \
  --include-pricing --format json \
| jq '.data[] | select(.pricing_tiers[]?.is_override)
      | {id, date,
         fares: [.pricing_tiers[] | select(.is_override)
                 | {id, was: .default_amount, now: .amount}]}'
```

`--include-pricing` (also available on `get <id>`) embeds `pricing_tiers[]` on each row, where `amount` is the **effective** price — the `availability_pricing_tier` pivot fare when one exists, the catalogue price otherwise — alongside `default_amount` (catalogue) and `is_override` (whether a pivot row exists). It defaults to **false**; without it the rows carry no `pricing_tiers[]` at all, so a jq filter against them silently matches nothing.

Cost is bounded: 3 batched queries per page regardless of page size, no per-row N+1. Soft-deleted tiers are excluded, and slots with no product context (e.g. Resource-backed) get an empty array. For a single slot, `pricing-tiers list --availability-id <id>` returns the same overlay in full tier shape.

## Pitfalls

- ⚠️ **Bulk-update is async and has no in-band completion signal in V1.** Exit 0 + `BULK_UPDATE_ACCEPTED` on stderr means the audit row was created and jobs queued on `inventory`. Confirm by polling `availabilities list` or by reading the `BulkAvailabilityUpdate` audit row server-side. Phase 2 will add `GET /availabilities/bulk-updates/{id}`.
- ⚠️ **One setting per bulk call.** `capacity AND booking-status` requires two calls; the underlying job dispatcher can only carry one setting at a time. The CLI enforces this by exposing five separate subcommands.
- ⚠️ **Date range is half-open `[from, to)`.** `--from 2026-05-01 --to 2026-06-01` matches every May date, NOT June 1.
- ⚠️ **Timezone:** dates are interpreted in the tenant's `Organisation.timezone`. A rule for "all of August in tenant TZ" is not the same as "all of August UTC" — server uses tenant TZ.
- ⚠️ **`starts_at` / `ends_at` come back in the PRODUCT's timezone, not UTC.** `2026-08-04T10:00:00+01:00` is a 10:00 *local* departure for a `Europe/London` product — the `from` / `to` columns store product-local wall clock. Every other timestamp on the row (`created_at`, `updated_at`, `deleted_at`, …) is a genuine UTC instant emitted as `+00:00`. Slots with no product option behind them (e.g. resource-backed rows) fall back to `+00:00` because there is no product to attribute. Normalising these to UTC without reading the offset shifts every departure by the product's offset.
- ⚠️ **`--status` takes `available` or `blocked` only.** `cancelled` was documented but never implemented; sending it is a 422.
- ⚠️ **Pricing writes are additive, not replacive.** Tiers omitted from `--fares` keep their existing fares. To change a tier across a range, include it explicitly with the new amount.
- ⚠️ **Re-setting a fare to the catalogue number does NOT un-pin the slot.** The pivot row survives and `is_override` stays `true`, so the slot silently keeps the old number the next time the tier is repriced in the catalogue. Send `"amount": null` to delete the override — that is the only thing that hands a slot back to the catalogue price.
- ⚠️ **`bulk-update pricing` writes every session on a matched date.** It is date-range scoped, not slot scoped. On a date carrying a 09:00 and a 14:00, it reprices both — including the one that had no override before. Reach for `availabilities update <id> --fares` when you mean one slot.
- ⚠️ **`total_matched` on a pricing dry-run is not a change count.** It is the date-range row count and looks identical whether or not your amount differs from the current fare. Read `total_changed` for "will this change anything?", and remember `preview_truncated` cuts the `diff` listing but not the counts.
- ⚠️ **An empty `--fares` array is a 422.** It used to be accepted, and would write an audit row, queue a job per chunk of matched ids and fire `PriceScheduleUpdated` at the channel managers for zero data change. The CLI now rejects it locally. Omit `--fares` entirely to leave pricing untouched.
- ⚠️ **A 200 on a fare write means stored, not announced.** The committed change dispatches `PriceScheduleUpdated` to the channel managers best-effort **after** the commit; a failing listener is reported server-side, not surfaced to you. Same caveat as the delete-side channel retraction below.
- ⚠️ **`create-rule` only ever ADDS — a preview of 172 new departures is not evidence the calendar is right.** `skip_reasons.already_exists` needs the exact `(from, to)`, so reloading a season at corrected times collides with nothing and doubles every day. Check `notes.other_departures_exist` and `rule.other_departures_sample` on every dry run; nothing is refused.
- ⚠️ **Replacing a schedule: bulk-delete BEFORE create-rule, never after.** `create-rule` is async and `bulk-delete`'s range scope takes every departure of the option in `[from, to)`, so a delete issued after the rule can take the corrected season with it — decided by queue latency, with an empty season as one outcome. Prefer `--availability-ids` for the delete, which cannot reach rows the worker has not written yet.
- ⚠️ **Delete / bulk-delete are blocked by confirmed bookings.** Both endpoints precheck for `AVAILABILITY_HAS_CONFIRMED_BOOKING` (409) **including in dry-run**. `bulk-delete` is all-or-nothing — one blocker rejects the entire range, and `error.details.sample_availability_ids` returns up to 20 ids to investigate. Cancel/move the bookings or narrow the range, then retry.
- ⚠️ **Soft-delete is one-way from the CLI, but it is NOT invisible.** There is no `/availabilities/{id}/restore` operation, so you cannot undo a soft-delete here. You CAN still see exactly what went: `deleted_at` is a real field on the Availability resource and `list` accepts `--include-trashed`, so `availabilities list --include-trashed --product-option-id <id> --from <a> --to <b>` enumerates the deleted rows. That is the first step after a bad `bulk-delete` — read the list, then re-create those departures (or escalate to ops with the ids in hand). An earlier version of this pitfall claimed the rows could be "neither listed nor undeleted"; the listing half was wrong, which sent operators to ops for a question they could answer themselves.
- ⚠️ **A successful delete does not mean the OTA knows.** Channel retraction (GYG `vacancies: 0`) is queued asynchronously and best-effort, and `bulk-delete` only retracts slots inside the next 90 days. If you deleted a far-future range and need GYG to stop selling it *now*, don't assume the 200 handled it — escalate rather than re-running the delete, which is already a no-op.

## See also

- [product-options.md](product-options.md) — `--product-option-id` is required for bulk-update.
- [pricing-tiers.md](pricing-tiers.md) — fares used by `bulk-update pricing` reference tier IDs; also covers reading the `availability_pricing_tier` pivot per slot.
- [bookings.md](bookings.md) — bookings consume availability capacity.
