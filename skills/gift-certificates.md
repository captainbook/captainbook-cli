# Gift Certificates

Two distinct resources live under `gift-certificates`:

- **AvailableGiftCertificate** ("available") — a sellable SKU. The thing a tenant lists for sale. CRUD with **hard-delete** (FK-protected against issued certs).
- **GiftCertificate** ("issued") — a real instance handed to a recipient, with a redemption code, balance, expiry, status. Issued via `issue`, voided via `void`, redemption email re-sent via `resend`.

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory gift-certificates available list` | GET /gift-certs/available | `cli:read` | n/a |
| `inventory gift-certificates available show <id>` | GET /gift-certs/available/{id} | `cli:read` | n/a |
| `inventory gift-certificates available create` | POST /gift-certs/available | `cli:write` | body |
| `inventory gift-certificates available update <id>` | PATCH /gift-certs/available/{id} | `cli:write` | body |
| `inventory gift-certificates available delete <id>` | DELETE /gift-certs/available/{id} | `cli:write` | none |
| `inventory gift-certificates issued list` | GET /gift-certs/issued | `cli:read` | n/a |
| `inventory gift-certificates issued show <id>` | GET /gift-certs/issued/{id} | `cli:read` | n/a |
| `inventory gift-certificates issue` | POST /gift-certs/issue | `cli:write` | body |
| `inventory gift-certificates void <id>` | POST /gift-certs/{id}/void | `cli:write` | body |
| `inventory gift-certificates resend <id>` | POST /gift-certs/{id}/resend | `cli:write` | body |

## Worked examples

### 1. List active issued certs for one recipient

```bash
ceebee inventory gift-certificates issued list \
  --status active \
  --recipient-email customer@example.com
```

Status enum: `active`, `redeemed`, `partial`, `void`, `expired`.

### 2. Issue a €100 gift cert and DO NOT email yet

Intent: stage a gift cert for manual verification, send later.

```bash
ceebee inventory gift-certificates issue \
  --available-gift-certificate-id agc_basic \
  --recipient-email gift-recipient@example.com \
  --recipient-name "Alex Doe" \
  --amount 10000 \
  --send-now false \
  --sender-message "Happy birthday!"
```

`10000` = €100.00. `--send-now false` (the default) keeps the redemption code from going out. To deliver later, run `gift-certificates resend`.

### 3. Issue + send in one call (preview first)

```bash
ceebee inventory gift-certificates issue \
  --available-gift-certificate-id agc_premium \
  --recipient-email recipient@example.com \
  --recipient-name "Alex Doe" \
  --amount 25000 \
  --send-now true \
  --dry-run
```

Dry-run shows the would-be `GiftCertificate` row and confirms the mailer would fire — without sending. Drop `--dry-run` to commit + email.

### 4. Void an issued cert with reason

Intent: customer disputes the purchase; void the certificate, notify them.

```bash
ceebee inventory gift-certificates void gc_42 \
  --reason "purchase disputed by buyer" \
  --notify-recipient true
```

`--reason` is required (max 500 chars). `--notify-recipient` defaults `false`.

### 5. Resend the redemption email to a new address

```bash
ceebee inventory gift-certificates resend gc_42 \
  --recipient-email new-recipient@example.com
```

Without `--recipient-email`, resends to the original recipient. External side effect: mailer.

## Pitfalls

- ⚠️ **`available delete` is HARD delete** (not soft). Returns `409 RESOURCE_IN_USE` if any issued `GiftCertificate` still references the SKU. Either void all issued certs first, or accept the orphaning. There is no `available restore` — once deleted, the SKU is gone.
- ⚠️ **No server-side dry-run on `available delete`.** CLI rejects `--dry-run`. Check references first: `ceebee inventory gift-certificates issued list --available-gift-certificate-id agc_basic --format json | jq '.data | length'`.
- ⚠️ **`issue --send-now true` is a one-way email trigger.** There is no "unsend"; voiding the cert with `--notify-recipient true` is the closest you get. Default is `false` deliberately so an LLM doesn't accidentally dispatch a redemption email mid-experimentation.
- ⚠️ **Money is in tenant currency minor units.** `--amount 5000` is €50.00 EUR or ¥5000 JPY — confirm `meta.currency` first via `whoami`.

## See also

- [bookings.md](bookings.md) — gift certs can be redeemed against bookings (server-side flow, not in this CLI).
- [transactions.md](transactions.md) — gift-cert redemptions appear as transactions on the redeeming booking.
