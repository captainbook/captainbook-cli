# Discounts

A `Discount` is a promo code or auto-applied rule that reduces booking total — fixed amount or percentage, optionally scoped to one product option, optionally with a max-redemption count (`nb_offers`) and/or validity window. The `apply` endpoint attaches a discount to an existing booking (computing a refund if needed). Soft-delete IS the cancellation operation.

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory discounts list` | GET /discounts | `cli:read` | n/a |
| `inventory discounts show <id>` | GET /discounts/{id} | `cli:read` | n/a |
| `inventory discounts create` | POST /discounts | `cli:write` | body |
| `inventory discounts update <id>` | PATCH /discounts/{id} | `cli:write` | body |
| `inventory discounts delete <id>` | DELETE /discounts/{id} | `cli:write` | query |
| `inventory discounts restore <id>` | POST /discounts/{id}/restore | `cli:write` | body |
| `inventory discounts apply <id>` | POST /discounts/{id}/apply | `cli:write` | body |

## Worked examples

### 1. Find currently-valid discounts

Intent: list everything redeemable right now.

```bash
ceebee inventory discounts list --valid-at "2026-04-27T12:00:00Z"
```

Returns discounts where `validity_start <= valid_at < validity_end` (or `validity_end IS NULL`).

### 2. Create a 15% promo code, auto-apply off, scoped to one option

```bash
ceebee inventory discounts create \
  --code SPRING15 \
  --product-option-id po_88 \
  --discount-pct 15 \
  --validity-start 2026-05-01T00:00:00Z \
  --validity-end 2026-06-01T00:00:00Z \
  --nb-offers 100 \
  --dry-run
```

Validation: provide exactly one of `--discount-pct` or `--discounted-price`. 422 if both or neither.

### 3. Create a fixed-amount global discount

Intent: €10 off any booking, no expiry.

```bash
ceebee inventory discounts create \
  --code WELCOME10 \
  --discounted-price 1000 \
  --validity-start 2026-04-01T00:00:00Z \
  --auto-apply false
```

`1000` = €10.00. `--product-option-id` omitted → global discount.

### 4. Apply a discount to an existing booking (with refund preview)

Intent: a customer asks for the SPRING15 retroactively on `bk_42`.

```bash
ceebee inventory discounts apply disc_spring15 --booking-id bk_42 --dry-run
```

Dry-run returns the recomputed `discount_total` and the would-be `refund_amount`. Drop `--dry-run` to commit. Stripe is NOT called by `apply` — if a refund is owed, follow up with `bookings refund`.

### 5. "Cancel" a discount (= soft-delete)

```bash
ceebee inventory discounts delete disc_spring15 --dry-run    # query-string dry-run
ceebee inventory discounts delete disc_spring15
```

Existing `booking_discount` pivot rows stay valid; the discount no longer applies to new bookings or future redemptions. Restore via `discounts restore`.

## Pitfalls

- ⚠️ **`apply` returns 409 `DISCOUNT_NOT_APPLICABLE`** if any of the following: booking is cancelled/expired, the validity window doesn't include the booking date, the discount is scoped to a different `product_option_id`, or `nb_offers` is exhausted. The error envelope's `code` field is stable — branch on `DISCOUNT_NOT_APPLICABLE` rather than the human message.
- ⚠️ **`apply` does NOT issue refunds.** It updates `booking_discount` and recomputes `discount_total`. If money is now owed back, run `ceebee inventory bookings refund <booking-id> --amount <delta> --reason "discount applied retroactively"` separately.
- ⚠️ **Soft-delete is the cancellation operation.** There is no `cancel` subcommand for discounts — `delete` is it. The `dry_run` flag for delete is a **query parameter** (`?dry_run=true`), not a body field, because the HTTP method has no body.
- ⚠️ **Exactly-one-of constraint:** `discounted_price` and `discount_pct` are mutually exclusive — server returns 422 if both or neither are set.

## See also

- [bookings.md](bookings.md) — `apply` requires `--booking-id`; refunds happen there.
- [product-options.md](product-options.md) — `--product-option-id` scopes a discount.
- [transactions.md](transactions.md) — read the resulting refund transactions after `apply` + `bookings refund`.
