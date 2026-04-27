# Transactions

A `Transaction` is a single money-movement event tied to a booking — a charge, a refund, or a comp. Read-only in V1: write operations are driven through `bookings refund` and `bookings comp`. Useful for auditing money flow and reconciling Stripe.

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory transactions list` | GET /transactions | `cli:read` | n/a |
| `inventory transactions show <id>` | GET /transactions/{id} | `cli:read` | n/a |

(For per-booking transactions, see [bookings.md](bookings.md#endpoints) → `bookings transactions <id>`.)

## Worked examples

### 1. List all refunds in the last 7 days

```bash
ceebee inventory transactions list \
  --type refund \
  --from "2026-04-20T00:00:00Z" --to "2026-04-27T00:00:00Z"
```

`--from`/`--to` here are date-times on `Transaction.created_at` (UTC unless offset-suffixed). Type enum: `charge`, `refund`, `comp`. Status enum: `pending`, `succeeded`, `failed`, `partial`.

### 2. Show one transaction (full detail + Stripe IDs)

```bash
ceebee inventory transactions show tx_abc123 --format json
```

Response carries the Stripe charge/refund id, idempotency key used, gross/net amounts, and the linked `booking_id`.

### 3. Find every failed charge for reconciliation

```bash
ceebee inventory transactions list --type charge --status failed --limit 200
```

Cursor-paginate via `--cursor`.

### 4. List transactions for one booking

```bash
ceebee inventory bookings transactions bk_42 --format json
```

Returns the full ordered ledger for `bk_42` (charge → refund → comp ...).

## Pitfalls

- ⚠️ **No write operations.** `transactions create`/`refund`/`update` do not exist. Refunds happen via `bookings refund <booking-id>`; comps via `bookings comp <booking-id>`. Looking for "refund a transaction directly" is a wrong-tool sign — go to `bookings.md`.
- ⚠️ **Date filters are UTC date-times**, not tenant-TZ dates. `--from "2026-05-01T00:00:00Z"` is May 1 00:00 UTC; tenants in UTC+3 may see May 1 03:00 local. Compare with `bookings list --from 2026-05-01` (tenant-TZ date).
- ⚠️ **`status: partial` exists for partial refunds** of a single charge. A booking with a €50 partial refund on a €150 charge will show one `charge.succeeded` plus one `refund.partial`/`succeeded` transaction — read both to compute net.

## See also

- [bookings.md](bookings.md) — origin of transactions; `refund` and `comp` mutations live there.
- [discounts.md](discounts.md) — applying a discount can require a follow-up `bookings refund`.
