# Notifications

V1 ships a single notification command: **resend the booking confirmation** (email or SMS) for a given booking. Used when customers report missing emails or want a fresh copy. CS-sensitive (`cli:cs`) because it contacts customers directly.

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory notifications resend-confirmation <booking-id>` | POST /bookings/{id}/notifications/resend-confirmation | `cli:cs` | body |

## Worked examples

### 1. Resend the confirmation email to the booking's primary contact

```bash
ceebee inventory notifications resend-confirmation bk_42
```

Default channel is `email`; default recipient is the booking's primary contact. Default `--format json` returns `{notification_id, channel, sent_at}`.

### 2. Send via SMS instead

Intent: customer can't access email; send the confirmation as an SMS.

```bash
ceebee inventory notifications resend-confirmation bk_42 \
  --channel sms
```

Server uses the booking's primary phone. SMS provider fires.

### 3. Override the recipient (email)

Intent: customer's spouse needs a copy of the confirmation.

```bash
ceebee inventory notifications resend-confirmation bk_42 \
  --recipient spouse@example.com
```

`--recipient` overrides email when `--channel email`, or phone when `--channel sms`.

### 4. Dry-run before triggering

Intent: confirm what the server WOULD send before clicking the trigger.

```bash
ceebee inventory notifications resend-confirmation bk_42 \
  --channel email \
  --recipient customer@example.com \
  --dry-run
```

Dry-run returns the would-be `notification_id` and recipient — no email sent, no Stripe call (there is none for this op anyway). Drop `--dry-run` to commit.

## Pitfalls

- ⚠️ **`cli:cs` ability required.** Operator tokens (`cli:write` only) get `403 ABILITY_MISSING`. Customer-contact operations are deliberately CS-gated.
- ⚠️ **External side effect.** Mailer (or SMS provider) fires for real on success. There is no "unsend". Dry-run before any production resend.
- ⚠️ **No localized render preview in V1.** The confirmation email is rendered in the booking's locale on the server; the CLI does not return the rendered HTML/text. Phase 2 will add a `preview` mode.
- ⚠️ **`forensic_summary` captures the recipient.** Audit log entry includes the resolved recipient (PII). Treat `~/.ceebee/audit.jsonl` as PII-bearing.

## See also

- [bookings.md](bookings.md) — same `bk_*` id; CS-sensitive cancel/refund/comp flows.
- [index.md](index.md#audit-log) — audit log location and querying.
