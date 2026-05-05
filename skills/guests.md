# Guests

A `Guest` is a per-booking traveler — name, passport, DOB, dietary requirements, and arbitrary `custom_attributes`. Distinct from `Customer` (which is the booking-account holder). The CLI exposes reads + a single PATCH; the principal use case is the **Greek passenger-list legal compliance workflow** where CS edits passport numbers post-booking.

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory guests list` | GET /guests | `cli:read` | n/a |
| `inventory guests show <id>` | GET /guests/{id} | `cli:read` | n/a |
| `inventory guests update <id>` | PATCH /guests/{id} | `cli:write` | body |

## Worked examples

### 1. List guests on a booking

```bash
ceebee inventory guests list --booking-id bk_42
```

Returns table of `{id, booking_id, name, email, dob, passport, updated_at}`.

### 2. Show one guest

```bash
ceebee inventory guests show g_88 --format json
```

Includes `dietary` and `custom_attributes` (free-form JSON).

### 3. Fix a passport number (Greek compliance)

Intent: passenger boarded with a different passport than was on the booking; update it.

```bash
ceebee inventory guests update g_88 \
  --passport AB1234567 \
  --dry-run
```

Drop `--dry-run` to commit. The audit log records the field change with `forensic_summary` (PII-redacted summary, full diff).

### 4. Update dietary + custom attributes together

```bash
ceebee inventory guests update g_88 \
  --dietary "vegetarian, no nuts" \
  --custom-attributes '{"emergency_contact":"+30 210 1234567","loyalty_tier":"gold"}'
```

`--custom-attributes` takes a JSON object; the server replaces the whole `custom_attributes` blob (no per-key merge).

### 5. List guests updated since last sync

```bash
ceebee inventory guests list --since "2026-04-01T00:00:00Z" --limit 200
```

Useful for nightly reconciliation against external CRMs.

## Pitfalls

- ⚠️ **`custom_attributes` is REPLACE, not merge.** Updating a single key requires re-sending the whole object. Read first → mutate locally → write back.
- ⚠️ **Passport edits are PII-sensitive.** They appear in `~/.ceebee/audit.jsonl` with a `forensic_summary` (the full passport string IS captured for compliance reasons). Treat the audit file as PII and rotate per local policy.
- ⚠️ **Guest is not Customer.** `Customer.id` and `Guest.id` are distinct ID spaces — don't pass a customer id where a guest id is expected. Use `--booking-id` to find guests; the booking carries `customer_id` separately.
- ⚠️ **No create / delete in V1.** Guests are created when a booking is placed (via the public flow). To add a guest to an existing booking, that's not in the V1 surface.

## See also

- [bookings.md](bookings.md) — `bookings show` inlines guests inline.
- [customers.md](customers.md) — the account record (different concept).
