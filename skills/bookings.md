# Bookings

A `Booking` is a customer reservation against a `ProductOption` on a specific date. The CLI exposes reads, resource assignment (`cli:write`), and the money-moving actions `cancel`, `refund`, and `comp`. All three money-moving actions require `cli:cs` — `cancel` included, for every `--refund-policy` value, because the whole V1 enum (`none|full|partial`) is policy overrides and the server 403s operator tokens on overrides. There is no `create` in V1 — bookings are created via the public booking flow.

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory bookings list` | GET /bookings | `cli:read` | n/a |
| `inventory bookings get <id>` | GET /bookings/{id} | `cli:read` | n/a |
| `inventory bookings transactions <id>` | GET /bookings/{id}/transactions | `cli:read` | n/a |
| `inventory bookings available-resources <id>` | GET /bookings/{id}/resources/available | `cli:read` | n/a |
| `inventory bookings available-auxiliary-resources <id>` | GET /bookings/{id}/resources/auxiliary/available | `cli:read` | n/a |
| `inventory bookings set-resources <id>` | POST /bookings/{id}/resources | `cli:write` | body |
| `inventory bookings cancel <id>` | POST /bookings/{id}/cancel | `cli:cs` | body |
| `inventory bookings refund <id>` | POST /bookings/{id}/refund | `cli:cs` | body |
| `inventory bookings comp <id>` | POST /bookings/{id}/comp | `cli:cs` | body |

## Worked examples

### 1. List confirmed bookings starting in May

```bash
ceebee inventory bookings list \
  --booking-status CONFIRMED \
  --from 2026-05-01 --to 2026-05-31 \
  --product-option-id po_88
```

Status enum: `ON_HOLD`, `CONFIRMED`, `EXPIRED`, `CANCELLED`. Date filters apply to `Booking.starts_at` in tenant timezone.

### 2. Show one booking with inlined guests + recent transactions

```bash
ceebee inventory bookings get bk_42 --format json
```

Response inlines `data.guests[]`, the most-recent `data.transactions[]`, and `data.answers[]` (the customer's responses to the operator's custom booking questions). Use `bookings transactions bk_42` for the full ledger, and `answers list` for cross-booking manifest reads. It also carries `data.resources[]` (the assigned boat / guide / kit) and `data.resource_state_token`, which you need for `set-resources`.

`answers` is on `get` only — deliberately **absent** from `bookings list`, because answers routinely carry passport numbers, dates of birth and dietary/medical notes, and a paginated list carrying them would be a bulk PII export. Ordering is stable across repeated reads (`question_id`, then `answerable_type` / `answerable_id` / `id`).

Distinguish the two empty cases, because they mean opposite things:

| `data.answers` | Meaning |
|----------------|---------|
| `[]` | You may see answers; this booking has none. |
| `null` | You may **not** see answers — the token's user lacks `view_answers_of_booking`. The server does not even read them from the database. |

### 2b. What your permissions withhold

Field-level redaction on `Booking` is silent: **the keys are always present, only the values are withheld.** A script that checks for key presence will conclude the data doesn't exist rather than that it can't see it.

| Missing permission | What goes null/empty |
|--------------------|----------------------|
| `view_booker_of_booking` | `customer.name` / `.email` / `.phone` and the inline `guests[]`. `customer.id` survives, so the payload stays joinable. |
| `see_money_of_booking` | `total_amount`, `paid_amount`, `refunded_amount` (null) and `transactions[]` (empty). |
| `view_answers_of_booking` | `answers` (null — see the table above). |

The dedicated child endpoints agree with those gates rather than letting you walk around them: `transactions list` also requires `see_money_of_booking`, and `guests list` / `answers list` also require `view_booker_of_booking` / `view_answers_of_booking`. A guest or answer outside your business unit or assigned trips reads as **404**, not 403, so the id space is not enumerable.

Ability and permission are two different gates. `cli:read` decides which *endpoints* you can reach; the permissions of the user the token was issued to decide which *data* comes back. A caller holding `view_own_booking` (declared `exclusive_of` `view_any_booking`) sees only the bookings their linked resource is assigned to — list endpoints return a smaller page rather than a 403, so a short page is not evidence of a small calendar.

### 3. Find every trip a given resource is on

Intent: "which trips has the spare wetsuit kit been assigned to this month?"

```bash
KIT_ID=$(ceebee inventory resources list --category auxiliary --format json \
  | jq -r '.data[] | select(.name == "Spare Wetsuit Kit") | .id')

ceebee inventory bookings list \
  --resource-id "$KIT_ID" \
  --from 2026-08-01 --to 2026-08-31
```

`--resource-id` filters through the `booking_resource` pivot and matches **active resources only** — a soft-deleted resource returns zero rows rather than an error, so an empty result here is ambiguous between "never assigned" and "resource was deleted". Check `resources get <id>` if the empty answer is surprising.

Add `--include resources` to any `bookings list` call to inline each row's assigned resources plus its `resource_state_token`. Omit it when you just want the light payload.

### 4. Reassign the boat on a booking (read → decide → guarded write)

Intent: the Oceanis is in for repairs; move tomorrow's charter onto the Sun Odyssey.

```bash
# 1. Read current state AND the guard token.
TOKEN=$(ceebee inventory bookings get bk_42 --format json | jq -r '.data.resource_state_token')

# 2. Ask the server which main resources are actually assignable.
ceebee inventory bookings available-resources bk_42

# 3. Preview, then commit.
ceebee inventory bookings set-resources bk_42 \
  --main-resource-id 91 \
  --expected-resource-state-token "$TOKEN" \
  --dry-run

ceebee inventory bookings set-resources bk_42 \
  --main-resource-id 91 \
  --expected-resource-state-token "$TOKEN"
```

`set-resources` is a **desired-state** write, not a delta:

- `--main-resource-id` switches the single primary (non-auxiliary) resource.
- `--auxiliary-resource-ids` **replaces the entire auxiliary set**. Pass `--auxiliary-resource-ids=1,2` to end up with exactly those two; pass `--auxiliary-resource-ids=` (empty) to clear every auxiliary.
- Omitting a flag leaves that half of the assignment untouched.

Candidates come from `available-resources` (main) and `available-auxiliary-resources` (auxiliary), which apply the same availability and concurrency checks as the back-office switcher.

### 5. Handling the two 409s from `set-resources`

`BOOKING_RESOURCE_STATE_STALE` — someone changed the booking's resources between your read and your write. The token you sent is no longer current. **Re-read, re-decide, re-send.** Never blind-retry the same body: the state you based the decision on is gone, and the reason the server rejected you is precisely that it can't tell whether your intent still holds.

`BOOKING_RESOURCE_CONFLICT` — your view was current, but the selection itself is illegal (resource double-booked at that slot, wrong category for the slot you put it in, not attached to the booking's ProductOption, or soft-deleted). Re-reading won't help. Re-run `available-resources` / `available-auxiliary-resources` and pick from what comes back.

### 6. Cancel applying the product's policy (CS only)

Intent: a customer cancels a booking; refund what the product's cancellation policy says they are owed.

There is no `auto` in V1. `Product.cancellation_policy` is free-text human prose and nothing derives a refund amount from it, so `--refund-policy auto` is rejected with `422 POLICY_AUTO_NOT_READY`. Read the policy yourself, decide the amount, and state it explicitly:

```bash
# 1. Read the policy text you are applying.
ceebee inventory products get prd_7 --format json | jq -r '.data.cancellation_policy'

# 2. State the decision the policy implies.
ceebee inventory bookings cancel bk_42 \
  --reason "customer cancellation request; 14 days out, policy allows full refund" \
  --refund-policy full \
  --notify-customer=true \
  --dry-run
```

Dry-run returns `data.refund_amount` and `data.policy_applied` without contacting Stripe or the mailer. Drop `--dry-run` to commit.

Because you are the policy engine here, put the reasoning in `--reason` — that string is what the audit log has to explain the money movement with.

### 7. Cancel with policy override

Intent: comp a full refund despite the no-refund policy.

```bash
ceebee inventory bookings cancel bk_42 \
  --reason "weather event — owner approved full refund" \
  --refund-policy full \
  --notify-customer=true
```

`partial` additionally requires `--refund-amount <minor-units>`. Nothing changes ability-wise between this recipe and recipe 6: `none`, `full` and `partial` are all overrides, so all three need `cli:cs`.

### 8. Refund a partial amount (CS only)

Intent: refund €50 of a €150 booking.

```bash
ceebee inventory bookings refund bk_42 \
  --amount 5000 \
  --reason "discount applied retroactively" \
  --notify-customer=false \
  --dry-run
```

`5000` = €50.00. `--notify-customer` defaults `false` for refund — operators debugging refunds should not silently email customers. Set `true` to dispatch the refund-receipt notification. Drop `--dry-run` to commit; Stripe is called for real.

### 9. Comp a booking (zero-out, no Stripe)

Intent: write off a booking with no money movement.

```bash
ceebee inventory bookings comp bk_42 \
  --reason "owner-comped tour" \
  --notify-customer=false
```

A `Transaction` of type `comp` is recorded; no Stripe call. `--notify-customer` defaults `false`.

## Pitfalls

- ⚠️ **`cancel`, `refund`, and `comp` all touch live external systems** (Stripe, mailer, SMS). Always `--dry-run` first. The `forensic_summary` in `~/.ceebee/audit.jsonl` captures the request + response for these — useful for post-incident review.
- ⚠️ **`cancel`, `refund` and `comp` all require `cli:cs`.** Operator tokens (`cli:write` only) get `ABILITY_MISSING` on all three, refused locally before any network call. For `cancel` this holds for every `--refund-policy` value including `none`: the spec classes all of `none|full|partial` as CS-attributed overrides of the product's policy, and `refund_policy` is required, so V1 has no operator-reachable cancel. The relaxed path older docs promised (`cli:write` when the policy is applied automatically) belonged to a `refund_policy=auto` that is not in the V1 enum. The spec's ability table now names cancel in its `cli:cs` row explicitly (fixed upstream in captainbook/captainbook#8113); older copies of the spec omitted it, and for those the routes are authoritative.
- ⚠️ **`refund` defaults `notify_customer` to `false`**, opposite of `cancel` which defaults to `true`. Different ergonomics for different ops: cancellation customers expect an email; refund-debugging engineers don't want to spam them.
- ⚠️ **Date-time vs date filters.** `bookings list --from 2026-05-01` matches bookings whose **start date** is May 1 or later (date, tenant TZ). `transactions list --from "2026-05-01T00:00:00Z"` is a UTC date-time on `Transaction.created_at`. Don't mix.
- ⚠️ **`set-resources` replaces, it doesn't append.** `--auxiliary-resource-ids=7` on a booking that already has kits 3 and 5 leaves it with *only* kit 7. Read the current set first and send the full intended list.
- ⚠️ **The state token isn't optional and isn't reusable.** `--expected-resource-state-token` is required, and it's invalidated by any resource change on that booking — including your own successful write. Re-read before each subsequent `set-resources` on the same booking; the response's `data.resource_state_token` is the new one.
- ⚠️ **Don't retry through a `BOOKING_RESOURCE_STATE_STALE`.** It means the world moved, not that the request was malformed. Retrying the identical body is how you clobber someone else's change the moment the guard happens to line up. Re-read and re-decide.
- ⚠️ **`--resource-id` on list matches active resources only.** A soft-deleted resource yields an empty page, not a 404 — don't read that as "this resource was never used".
- ⚠️ **Redaction is silent, and `null` does not mean "empty".** `customer.name`, `total_amount`, `answers` and friends come back as `null` (keys present) when the token's *user* lacks the matching permission — see example 2b. `answers: []` means "none exist"; `answers: null` means "you may not see them". Treating the two the same ships a manifest that is quietly missing every dietary requirement.
- ⚠️ **`starts_at` / `ends_at` on a booking are in the PRODUCT's timezone, not UTC.** They are read from the linked availability, whose `from` / `to` columns store product-local wall clock: `2026-08-04T10:00:00+01:00` is a 10:00 local departure for a `Europe/London` product. The booking's own timestamps (`confirmed_at`, `cancelled_at`, `created_at`, …) are real UTC. Don't normalise the whole object to UTC with one rule.
- ⚠️ **`--include resources` is opt-in on list but implicit on get.** `bookings get` always carries `resources[]` + `resource_state_token`; `bookings list` only does with `--include resources`. Scripts that grab the token from a list call will silently read `null` without it.

## See also

- [resources.md](resources.md) — creating resources and attaching them to product options (the pool `set-resources` picks from).
- [transactions.md](transactions.md) — full transaction ledger per booking.
- [guests.md](guests.md) — per-booking guests, edited separately.
- [answers.md](answers.md) — cross-booking reads of the same `answers[]` this endpoint inlines (manifests, dietary rollups).
- [discounts.md](discounts.md) — `discounts apply` attaches to a booking; refund is a separate step here.
- [notifications.md](notifications.md) — resend the booking confirmation email/SMS.
