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

`--from`/`--to` here are date-times on `Transaction.created_at` (UTC unless offset-suffixed). Type enum: `charge`, `refund`, `comp`. There is no `--status` filter: per spec, `Transaction.status` is always `succeeded` (failed payments don't produce a row at all), so a status filter is a no-op trap. The CLI omits the flag rather than silently returning zero rows for `failed`/`pending`/`partial`.

### 2. Show one transaction (full detail + Stripe IDs)

```bash
ceebee inventory transactions show tx_abc123 --format json
```

Response carries the Stripe charge/refund id, idempotency key used, gross/net amounts, and the linked `booking_id`.

### 3. Page all charges for reconciliation

```bash
ceebee inventory transactions list --type charge --limit 200
```

Cursor-paginate via `--cursor`. Failed payments don't appear here at all (no row is written), so reconcile by joining the charge ledger against Stripe's failed-payment events upstream.

### 4. List transactions for one booking

```bash
ceebee inventory bookings transactions bk_42 --format json
```

Returns the full ordered ledger for `bk_42` (charge → refund → comp ...).

## Pitfalls

- ⚠️ **No write operations.** `transactions create`/`refund`/`update` do not exist. Refunds happen via `bookings refund <booking-id>`; comps via `bookings comp <booking-id>`. Looking for "refund a transaction directly" is a wrong-tool sign — go to `bookings.md`.
- ⚠️ **Date filters are UTC date-times**, not tenant-TZ dates. `--from "2026-05-01T00:00:00Z"` is May 1 00:00 UTC; tenants in UTC+3 may see May 1 03:00 local. Compare with `bookings list --from 2026-05-01` (tenant-TZ date).
- ⚠️ **Partial refunds are a separate refund row, not a status.** A €50 partial refund on a €150 charge writes one `refund` row with `amount=-5000` (in minor units) alongside the original `charge` row — sum signed amounts to compute net. `Transaction.status` is always `succeeded` in v1; failed payments don't produce a row.

## See also

- [bookings.md](bookings.md) — origin of transactions; `refund` and `comp` mutations live there.
- [discounts.md](discounts.md) — applying a discount can require a follow-up `bookings refund`.
