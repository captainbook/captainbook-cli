# Bookings

A `Booking` is a customer reservation against a `ProductOption` on a specific date. The CLI exposes reads plus three sensitive (`cli:cs`) actions: `cancel`, `refund`, `comp`. There is no `create` in V1 — bookings are created via the public booking flow.

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory bookings list` | GET /bookings | `cli:read` | n/a |
| `inventory bookings show <id>` | GET /bookings/{id} | `cli:read` | n/a |
| `inventory bookings transactions <id>` | GET /bookings/{id}/transactions | `cli:read` | n/a |
| `inventory bookings cancel <id>` | POST /bookings/{id}/cancel | `cli:cs` (or `cli:write` for `refund_policy=auto`) | body |
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
ceebee inventory bookings show bk_42 --format json
```

Response inlines `data.guests[]` and the most-recent `data.transactions[]`. Use `bookings transactions bk_42` for the full ledger.

### 3. Cancel with `auto` policy (operator-level)

Intent: a customer cancels a booking; apply the product's standard cancellation policy.

```bash
ceebee inventory bookings cancel bk_42 \
  --reason "customer cancellation request" \
  --refund-policy auto \
  --notify-customer true \
  --dry-run
```

Dry-run returns `data.refund_amount` (computed from the policy) and `data.policy_applied`. `refund_policy=auto` works with `cli:write`. Drop `--dry-run` to commit.

### 4. Cancel with policy override (CS only)

Intent: comp a full refund despite the no-refund policy.

```bash
ceebee inventory bookings cancel bk_42 \
  --reason "weather event — owner approved full refund" \
  --refund-policy full \
  --notify-customer true
```

`refund_policy` of `none`, `full`, or `partial` requires `cli:cs` — operator tokens 403 here. `partial` additionally requires `--refund-amount <minor-units>`.

### 5. Refund a partial amount (CS only)

Intent: refund €50 of a €150 booking.

```bash
ceebee inventory bookings refund bk_42 \
  --amount 5000 \
  --reason "discount applied retroactively" \
  --notify-customer false \
  --dry-run
```

`5000` = €50.00. `--notify-customer` defaults `false` for refund — operators debugging refunds should not silently email customers. Set `true` to dispatch the refund-receipt notification. Drop `--dry-run` to commit; Stripe is called for real.

### 6. Comp a booking (zero-out, no Stripe)

Intent: write off a booking with no money movement.

```bash
ceebee inventory bookings comp bk_42 \
  --reason "owner-comped tour" \
  --notify-customer false
```

A `Transaction` of type `comp` is recorded; no Stripe call. `--notify-customer` defaults `false`.

## Pitfalls

- ⚠️ **`cancel`, `refund`, and `comp` all touch live external systems** (Stripe, mailer, SMS). Always `--dry-run` first. The `forensic_summary` in `~/.ceebee/audit.jsonl` captures the request + response for these — useful for post-incident review.
- ⚠️ **`refund` and `comp` require `cli:cs`** — operator tokens (`cli:write` only) get `403 ABILITY_MISSING`. `cancel` requires `cli:cs` only when `--refund-policy` is overridden (`none`, `full`, `partial`); `auto` works with `cli:write`.
- ⚠️ **`refund` defaults `notify_customer` to `false`**, opposite of `cancel` which defaults to `true`. Different ergonomics for different ops: cancellation customers expect an email; refund-debugging engineers don't want to spam them.
- ⚠️ **Date-time vs date filters.** `bookings list --from 2026-05-01` matches bookings whose **start date** is May 1 or later (date, tenant TZ). `transactions list --from "2026-05-01T00:00:00Z"` is a UTC date-time on `Transaction.created_at`. Don't mix.

## See also

- [transactions.md](transactions.md) — full transaction ledger per booking.
- [guests.md](guests.md) — per-booking guests, edited separately.
- [discounts.md](discounts.md) — `discounts apply` attaches to a booking; refund is a separate step here.
- [notifications.md](notifications.md) — resend the booking confirmation email/SMS.
