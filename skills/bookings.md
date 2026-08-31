# Bookings

A `Booking` is a customer reservation against a `ProductOption` on a specific date. The CLI exposes reads, resource assignment (`cli:write`), and the money-moving actions `cancel`, `refund`, and `comp`. `refund` and `comp` require `cli:cs`; `cancel` binds only `cli:write` even when its refund policy triggers a Stripe refund (see issue #19). There is no `create` in V1 — bookings are created via the public booking flow.

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory bookings list` | GET /bookings | `cli:read` | n/a |
| `inventory bookings get <id>` | GET /bookings/{id} | `cli:read` | n/a |
| `inventory bookings transactions <id>` | GET /bookings/{id}/transactions | `cli:read` | n/a |
| `inventory bookings available-resources <id>` | GET /bookings/{id}/resources/available | `cli:read` | n/a |
| `inventory bookings available-auxiliary-resources <id>` | GET /bookings/{id}/resources/auxiliary/available | `cli:read` | n/a |
| `inventory bookings set-resources <id>` | POST /bookings/{id}/resources | `cli:write` | body |
| `inventory bookings cancel <id>` | POST /bookings/{id}/cancel | `cli:write` | body |
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

Response inlines `data.guests[]` and the most-recent `data.transactions[]`. Use `bookings transactions bk_42` for the full ledger. It also carries `data.resources[]` (the assigned boat / guide / kit) and `data.resource_state_token`, which you need for `set-resources`.

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

### 6. Cancel with `auto` policy (operator-level)

Intent: a customer cancels a booking; apply the product's standard cancellation policy.

```bash
ceebee inventory bookings cancel bk_42 \
  --reason "customer cancellation request" \
  --refund-policy auto \
  --notify-customer true \
  --dry-run
```

Dry-run returns `data.refund_amount` (computed from the policy) and `data.policy_applied`. Drop `--dry-run` to commit.

### 7. Cancel with policy override

Intent: comp a full refund despite the no-refund policy.

```bash
ceebee inventory bookings cancel bk_42 \
  --reason "weather event — owner approved full refund" \
  --refund-policy full \
  --notify-customer true
```

`partial` additionally requires `--refund-amount <minor-units>`.

Note the ability gap: overriding the policy moves real money, but `cancel` binds `cli:write`, not `cli:cs`. An operator token can issue a full refund this way while being 403'd from `bookings refund`. Tracked in issue #19 — until it closes, treat `--refund-policy none|full|partial` as CS-grade by convention, not by enforcement.

### 8. Refund a partial amount (CS only)

Intent: refund €50 of a €150 booking.

```bash
ceebee inventory bookings refund bk_42 \
  --amount 5000 \
  --reason "discount applied retroactively" \
  --notify-customer false \
  --dry-run
```

`5000` = €50.00. `--notify-customer` defaults `false` for refund — operators debugging refunds should not silently email customers. Set `true` to dispatch the refund-receipt notification. Drop `--dry-run` to commit; Stripe is called for real.

### 9. Comp a booking (zero-out, no Stripe)

Intent: write off a booking with no money movement.

```bash
ceebee inventory bookings comp bk_42 \
  --reason "owner-comped tour" \
  --notify-customer false
```

A `Transaction` of type `comp` is recorded; no Stripe call. `--notify-customer` defaults `false`.

## Pitfalls

- ⚠️ **`cancel`, `refund`, and `comp` all touch live external systems** (Stripe, mailer, SMS). Always `--dry-run` first. The `forensic_summary` in `~/.ceebee/audit.jsonl` captures the request + response for these — useful for post-incident review.
- ⚠️ **`refund` and `comp` require `cli:cs`; `cancel` does not.** Operator tokens (`cli:write` only) get `403 ABILITY_MISSING` on refund and comp, but `cancel` is bound to `cli:write` for every `--refund-policy` value — including the overrides that move money. That asymmetry is a known gap, not a design choice (issue #19). Don't infer from a successful `cancel --refund-policy full` that your token is CS-grade.
- ⚠️ **`refund` defaults `notify_customer` to `false`**, opposite of `cancel` which defaults to `true`. Different ergonomics for different ops: cancellation customers expect an email; refund-debugging engineers don't want to spam them.
- ⚠️ **Date-time vs date filters.** `bookings list --from 2026-05-01` matches bookings whose **start date** is May 1 or later (date, tenant TZ). `transactions list --from "2026-05-01T00:00:00Z"` is a UTC date-time on `Transaction.created_at`. Don't mix.
- ⚠️ **`set-resources` replaces, it doesn't append.** `--auxiliary-resource-ids=7` on a booking that already has kits 3 and 5 leaves it with *only* kit 7. Read the current set first and send the full intended list.
- ⚠️ **The state token isn't optional and isn't reusable.** `--expected-resource-state-token` is required, and it's invalidated by any resource change on that booking — including your own successful write. Re-read before each subsequent `set-resources` on the same booking; the response's `data.resource_state_token` is the new one.
- ⚠️ **Don't retry through a `BOOKING_RESOURCE_STATE_STALE`.** It means the world moved, not that the request was malformed. Retrying the identical body is how you clobber someone else's change the moment the guard happens to line up. Re-read and re-decide.
- ⚠️ **`--resource-id` on list matches active resources only.** A soft-deleted resource yields an empty page, not a 404 — don't read that as "this resource was never used".
- ⚠️ **`--include resources` is opt-in on list but implicit on get.** `bookings get` always carries `resources[]` + `resource_state_token`; `bookings list` only does with `--include resources`. Scripts that grab the token from a list call will silently read `null` without it.

## See also

- [resources.md](resources.md) — creating resources and attaching them to product options (the pool `set-resources` picks from).
- [transactions.md](transactions.md) — full transaction ledger per booking.
- [guests.md](guests.md) — per-booking guests, edited separately.
- [discounts.md](discounts.md) — `discounts apply` attaches to a booking; refund is a separate step here.
- [notifications.md](notifications.md) — resend the booking confirmation email/SMS.
