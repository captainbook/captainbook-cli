# Answers

An `Answer` is one customer response to one custom booking `Question` — a shoe size, a passport number, "no shellfish". `bookings get <id>` already inlines a single booking's answers; this resource exists for the **manifest-shaped** question — *"what are tomorrow's dietary requirements across every booking on the 09:00 departure?"* — which would otherwise cost one call per booking.

Read-only. Answers are written by the customer at checkout; there is no create / update / delete on this surface. Edit the *question* via [questions.md](questions.md), and per-guest compliance fields (passport, DOB) via [guests.md](guests.md).

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory answers list` | GET /answers | `cli:read` | n/a |
| `inventory answers get <id>` | GET /answers/{id} | `cli:read` | n/a |

## Permissions

`cli:read` gets you to the endpoint. Seeing anything also requires **`view_answers_of_booking`** on the user the token was issued to — gated separately from the rest of the read tier because this is guest PII (passport numbers, dates of birth, nationalities, dietary and medical notes), not inventory data. Without it, `answers list` is a `403 FORBIDDEN` and `bookings get` returns `answers: null` rather than `[]`.

A caller restricted to `view_own_booking` sees only answers on the trips their linked resource is assigned to, matching `bookings list`. That comes back as a **smaller page, not a 403** — so a short result is not evidence that few answers exist. An answer outside your business unit or assigned trips reads as **404**, not 403, so the id space is not enumerable.

## Worked examples

### 1. Tomorrow's dietary manifest for one departure

Intent: the galley needs every dietary note across every booking on the 09:00 September 1 sailing.

```bash
ceebee inventory answers list \
  --product-option-id 88 \
  --from 2026-09-01T00:00:00Z --to 2026-09-02T00:00:00Z \
  --format json \
| jq -r '.data[] | select(.label | test("dietary"; "i"))
         | [.answerable_type, .answerable_id, .answer] | @tsv'
```

`--from` / `--to` bound the **trip's departure**, not when the answer was written. They are read in the tenant's timezone, or in the product's when `--product-option-id` narrows the query to one — the same rule `bookings list --date-field starts_at` follows, because `availabilities.from` stores product-local wall clock.

### 2. One booking's answers

```bash
ceebee inventory answers list --booking-id bk_42
```

Equivalent to reading `data.answers[]` off `bookings get bk_42`. Prefer the booking read when you already need the booking; prefer this when you're iterating answers across many.

### 3. One question, across every booking

Intent: "how many people answered `yes` to the wetsuit-hire question this season?"

```bash
ceebee inventory answers list \
  --question-id 17 \
  --from 2026-05-01T00:00:00Z --to 2026-10-01T00:00:00Z \
  --format json \
| jq '[.data[] | select(.answer == "1")] | length'
```

Note `"1"`, not `true`: boolean answers serialize as the **string** `"1"` / `"0"`. Read `type` to know how to interpret `answer` — `country` answers come back as an ISO-3166 alpha-3 code, not a display name.

### 4. Per-guest answers only

```bash
ceebee inventory answers list --booking-id bk_42 --granularity guest
```

`--granularity` takes `booking`, `guest`, or `extra` and filters on the linked question's granularity — whether the operator asked once per booking, once per guest, or once per purchased extra.

### 5. Sync what changed

```bash
ceebee inventory answers list --since "2026-08-01T00:00:00Z"
```

`--since` is a genuine UTC bound against `updated_at`. It answers a different question from `--from` / `--to`: use those for a manifest ("who departs when"), this to sync ("what changed").

### 6. Recover answers to a retired question

```bash
ceebee inventory answers list --booking-id bk_42 --include-trashed
```

`Question` soft-deletes and **cascade-soft-deletes its answers**, so retiring a custom question hides every historical answer to it. Without this flag a manifest query for a past departure comes back short with nothing to say so. The flag lifts the scope on the linked question too, so restored rows keep a readable `label`.

## Reading the payload

| Field | Notes |
|-------|-------|
| `label` | The question text, denormalised so the answer is readable without a second call. `null` if the question row is unavailable. |
| `type` | The linked question's `answer_type`. Needed to interpret `answer` — see example 3. |
| `granularity` | What the operator asked *about*: `booking`, `guest`, or `extra`. Copied from the question. |
| `answerable_type` | Which row the answer physically hangs off, for joining: `customer`, `guest`, or `extra`. |
| `answerable_id` | Id of that row. Join against the booking's `guests[].id` or `customer.id`. |
| `answer` | The answer flattened to a single display string. Always a string; empty when the stored value is null. |
| `answer_raw` | The stored answer decoded verbatim from its JSON column, no flattening. |

**`granularity` and `answerable_type` can legitimately disagree, and that is not a bug.** They answer different questions. Booking-granularity answers are persisted against the *booker*, so `granularity: booking` pairs with `answerable_type: customer` — never `booking`.

**Extra-granularity answers store one entry per purchased unit.** `answer` comma-joins them into one string; use `answer_raw` (an array of `{quantity, answer}` objects) to recover the per-unit structure.

## Pitfalls

- ⚠️ **This is PII, and it is gated separately for that reason.** Passport numbers, dates of birth, nationalities, medical notes. `view_answers_of_booking` is not implied by `cli:read`. Don't dump this into a shared channel or a log.
- ⚠️ **`--from` / `--to` bound the DEPARTURE; `--since` bounds `updated_at`.** Reaching for `--from` to mean "answered since" silently returns the wrong set — it filters on when the trip leaves, not when the customer typed.
- ⚠️ **`answerable_id` is not guaranteed to resolve.** `answers` is constrained on `booking_id` only — the polymorphic `answerable` pair has no foreign key — and rescheduling a booking replicates answers **without remapping guest ids**. So a guest-granularity answer on a rescheduled booking can point at the original booking's now-gone guest. The answer text is still correct and still attributable to the booking; only the per-guest attribution is lost. Treat an unresolved `answerable_id` as "unknown guest", not as an error.
- ⚠️ **A manifest for a past departure can come back short with no warning** if the operator has since retired the question. Pass `--include-trashed` on any historical read.
- ⚠️ **Booleans are `"1"` / `"0"` strings, countries are alpha-3 codes.** Branch on `type` before comparing `answer`.
- ⚠️ **`answer` here is close to, but deliberately not identical to, the string workflows and manifests render.** Two values differ, both in this surface's favour: boolean `false` is `"0"` here and `""` there, and a nested array without an `answer` key is flattened recursively here but stringifies to the literal `"Array"` there. Don't assume the two surfaces agree byte-for-byte.
- ⚠️ **`bookings list` never carries answers.** Only `bookings get <id>` inlines them — a paginated list carrying passport numbers would be a bulk PII export. Use this resource for the cross-booking read instead of paging bookings and calling `get` on each.

## See also

- [questions.md](questions.md) — the questions these answer, and the cascade that hides them on delete.
- [bookings.md](bookings.md) — `bookings get` inlines `answers[]`; also covers the `null`-vs-`[]` redaction rule.
- [guests.md](guests.md) — structured per-guest compliance fields (passport, DOB) that are guest columns rather than answers.
