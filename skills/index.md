# `ceebee` Agent Skill Guide — Index

You have access to `ceebee`, the CaptainBook CLI. It is the agent-shaped surface for driving a tenant's CaptainBook account: read statistics, edit inventory, run customer-success operations.

This index is the global tour. Each per-resource cookbook (linked at the bottom) carries the worked examples and the resource-specific pitfalls. Read this file once; cherry-pick the rest.

## Overview

`ceebee` ships two namespaces:

- **`ceebee stats …`** — read-only analytics over revenue, bookings, customers, channels, etc. The v1 statistics surface is unchanged from the legacy `skills.md` — see your existing instructions for those commands.
- **`ceebee inventory …`** — read+write inventory, bookings, transactions, gift certificates, customer notifications. This is the new v1 namespace and the focus of these cookbooks.

Both namespaces share authentication, configuration, idempotency, dry-run, audit, exit-code, and error-code conventions, all documented below.

## Setup

### Install

```bash
curl -fsSL https://captainbook.github.io/captainbook-cli/install.sh | sh
```

### Authenticate

The CLI talks to `https://{tenant_slug}.captainbook.io/api/v1/cli/*` over a Sanctum bearer token. Configure either via env vars (preferred for agents) or a profile.

```bash
# Env-var path
export CEEBEE_API_URL=https://demo.captainbook.io
export CEEBEE_API_TOKEN=sk-xxxxxxxxxxxx

# Profile path (persists in ~/.ceebee/config.toml)
ceebee config add demo --url https://demo.captainbook.io --token sk-xxxxxxxxxxxx
ceebee --profile demo inventory products list
```

When both are set, `--profile` wins over env vars.

### Token abilities

Tokens carry one or more abilities. The server enforces them at the route layer; missing abilities return `403 ABILITY_MISSING`.

| Ability     | Required for |
|-------------|--------------|
| `cli:read`  | All read endpoints. Implicit alongside `cli:write` / `cli:cs`. |
| `cli:write` | Inventory mutations: products, options, availabilities (incl. bulk), pricing tiers, discounts (incl. apply), gift certs (issue/void/resend), guests, extras, questions, categories, media. |
| `cli:cs`    | CS-only sensitive operations: `bookings cancel` with `refund_policy=none|full|partial`, `bookings refund`, `bookings comp`, `notifications resend-confirmation`. |

Recommended issuance: a `cli:read` token for reporting bots, a `cli:read + cli:write` token for inventory editors, and a `cli:read + cli:write + cli:cs` token for Customer Success engineers.

### TLS

`ceebee` uses the operating-system trust store and Sanctum bearer-token auth. **There is no certificate pinning** — this is a deliberate choice, not an oversight. Pinning would make tenant onboarding painful (subdomains rotate; corporate proxies inject MITM certs). The bearer token is the credential of record.

## Common conventions

### Money is integer minor units

Every money field — `amount`, `from_price`, `discounted_price`, `refund_amount`, etc. — is an integer in the tenant's currency's smallest unit. `15000` = €150.00 in a EUR tenant, ¥15000 in a JPY tenant. Currency is surfaced via `meta.currency` on every response. Resource cookbooks repeat this on each money example without further explanation; if a number looks weirdly large, you are in minor units.

### Dates and times

- **Dates** are interpreted in the tenant timezone (`Organisation.timezone`) — applies to fields like `Booking.starts_at`, `Availability.date`.
- **Date ranges** on list endpoints are half-open `[from, to)`. To match April 2026 in full, pass `?from=2026-04-01&to=2026-05-01`.
- **Date-times** (`*_at`) are UTC unless the supplied value carries an explicit `±HH:MM` offset.

### Idempotency keys

Every mutation accepts `Idempotency-Key: <UUIDv7>` (header). The CLI auto-mints one per invocation, prints it to stderr, and reuses it on retry within the same invocation:

```text
$ ceebee inventory bookings refund bk_88 --amount 5000 --reason "duplicate charge"
[ceebee] idempotency-key=018f5e2c-6c4a-7c5a-9d2c-83a1b1f6e4cd
{"data":{"transaction":{...}}}
```

To replay deliberately (e.g. resume a script that crashed mid-flight):

```bash
ceebee inventory bookings refund bk_88 \
  --amount 5000 --reason "duplicate charge" \
  --idempotency-key 018f5e2c-6c4a-7c5a-9d2c-83a1b1f6e4cd
```

The server matches on (`Idempotency-Key`, canonical-JSON SHA-256 of the body excluding `dry_run`). On replay with the same body, you get the original response. On replay with a different body, you get `409 IDEMPOTENCY_CONFLICT`. On a key currently in flight, `409 IDEMPOTENCY_IN_PROGRESS`. On a swept key (server crashed), `409 IDEMPOTENCY_UNKNOWN` — retry with a fresh key.

Dry-runs do NOT consume an idempotency row, so you can preview with the same key you intend to use for the real call.

### Dry-run

Most mutations support `--dry-run`. The server runs validation, authorization, and computes the diff, then rolls back the DB transaction and skips Stripe/mailers/jobs. The CLI renders a colored unified diff against the current resource. Reads forbid `--dry-run` (the flag is rejected at parse time). A few delete operations don't accept dry-run server-side — see the capability table below.

```bash
ceebee inventory pricing-tiers delete pt_42 --dry-run
# diff preview, then "would delete; cascade: 124 availabilities will be soft-deleted"
```

### Output format defaults

| Command kind | Default `--format` |
|--------------|--------------------|
| Reads (`list`, `show`, `whoami`) | `table` (human) |
| Mutations (`create`, `update`, `delete`, `cancel`, `refund`, `comp`, `apply`, `issue`, `void`, `resend`, `restore`, upload) | `json` (machine) |

Override either way with `--format json|table|csv`. JSON is always the full envelope (`meta`, `data`, plus `pagination` on list endpoints).

### Async bulk update

`ceebee inventory availabilities bulk-update …` returns `202 Accepted`. The CLI prints the response to stdout (when `--format json`) and emits one stable signal to stderr that scripts can grep:

```text
BULK_UPDATE_ACCEPTED bulk_update_id=018f5e2d-9a14-7c12-bb03-77a8c7c2e5ab
```

Exit code is `0`. The body carries `bulk_update_id`, `total_matched`, and `status: queued`. There is no in-band completion signal in V1 — confirm by polling `ceebee inventory availabilities list` with the relevant filter, or by reading the `BulkAvailabilityUpdate` audit row server-side.

```bash
# Capture the bulk-update id from a script:
bulk_id=$(
  ceebee inventory availabilities bulk-update capacity \
    --product-option-id po_42 --from 2026-05-01 --to 2026-06-01 \
    --value 12 --operator SET 2>&1 1>/dev/null \
  | grep '^BULK_UPDATE_ACCEPTED' \
  | sed 's/.*bulk_update_id=//'
)
```

### Audit log

Every successful mutation logs to `~/.ceebee/audit.jsonl` — one JSON object per call: timestamp, profile, tenant, command, endpoint, idempotency key, request body hash (SHA-256, never the body itself — PII-safe), ability used, dry-run flag, response status, response ID, duration, error code (if any), and a `forensic_summary` field for sensitive (`cli:cs`) operations like refund / cancel / comp.

```bash
ceebee audit list                                   # most recent first; --limit N
ceebee audit show 018f5e2c-6c4a-7c5a-9d2c-83a1b1f6e4cd
```

Use this to reconstruct what an agent did, or to find an idempotency key for a deliberate replay. The file is rotated at 50 MB; the last 3 rotations are kept (`audit.jsonl.1` … `audit.jsonl.3`). Cross-process safety is guaranteed by an advisory lockfile (`~/.ceebee/.audit.lock`) that serializes appends and rotation across concurrent ceebee invocations.

### Exit codes

| Code | Meaning |
|------|---------|
| 0    | Success (or async-accepted; see stderr signal) |
| 10   | Authentication failed (401) |
| 11   | Forbidden / ability missing (403) |
| 12   | Validation error (422) |
| 13   | Network / timeout |
| 14   | JSON parse error |
| 15   | Configuration error |
| 16   | Server error (5xx) |
| 17   | Rate limited (429) |
| 18   | Unexpected status |

### Error codes

The server emits a stable `code` string in every error envelope. The CLI maps each to a typed error with a crisp `UserMessage`. Common codes you will see:

| Code | When |
|------|------|
| `UNAUTHENTICATED` | Token missing, invalid, or expired. |
| `ABILITY_MISSING` | Token valid, but lacks the required ability (`cli:write` / `cli:cs`). |
| `VALIDATION_FAILED` | Request body violates schema; `errors[]` lists field paths. |
| `IDEMPOTENCY_CONFLICT` | Same key, different body. |
| `IDEMPOTENCY_IN_PROGRESS` | Same key still executing on the server. |
| `IDEMPOTENCY_UNKNOWN` | Key was swept (server crashed mid-flight); retry with a fresh key. |
| `DISCOUNT_NOT_APPLICABLE` | Booking state, validity window, scope, or `nb_offers` blocks the apply. |
| `RESOURCE_IN_USE` | Hard-delete blocked by FK references (categories, gift-cert SKUs). |
| `RATE_LIMITED` | 429; respect `Retry-After`. |

## Capability table

Which mutations support `--dry-run`, where it lives in the request, and any caveats. **body** = `{ "dry_run": true }` in the JSON body. **query** = `?dry_run=true` query parameter. **none** = server does not support dry-run for this op (the CLI errors locally at parse time).

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory products create` | POST /products | `cli:write` | body |
| `inventory products update <id>` | PATCH /products/{id} | `cli:write` | body |
| `inventory products delete <id>` | DELETE /products/{id} | `cli:write` | none |
| `inventory products restore <id>` | POST /products/{id}/restore | `cli:write` | body |
| `inventory product-options create` | POST /product-options | `cli:write` | body |
| `inventory product-options update <id>` | PATCH /product-options/{id} | `cli:write` | body |
| `inventory product-options delete <id>` | DELETE /product-options/{id} | `cli:write` | none |
| `inventory product-options restore <id>` | POST /product-options/{id}/restore | `cli:write` | body |
| `inventory availabilities update <id>` | PATCH /availabilities/{id} | `cli:write` | body |
| `inventory availabilities bulk-update capacity` | POST /availabilities/bulk-update | `cli:write` | body |
| `inventory availabilities bulk-update booking-status` | POST /availabilities/bulk-update | `cli:write` | body |
| `inventory availabilities bulk-update pricing` | POST /availabilities/bulk-update | `cli:write` | body |
| `inventory availabilities bulk-update start-time` | POST /availabilities/bulk-update | `cli:write` | body |
| `inventory availabilities bulk-update end-time` | POST /availabilities/bulk-update | `cli:write` | body |
| `inventory pricing-tiers create` | POST /pricing-tiers | `cli:write` | body |
| `inventory pricing-tiers update <id>` | PATCH /pricing-tiers/{id} | `cli:write` | body |
| `inventory pricing-tiers delete <id>` | DELETE /pricing-tiers/{id} | `cli:write` | none |
| `inventory pricing-tiers restore <id>` | POST /pricing-tiers/{id}/restore | `cli:write` | body |
| `inventory discounts create` | POST /discounts | `cli:write` | body |
| `inventory discounts update <id>` | PATCH /discounts/{id} | `cli:write` | body |
| `inventory discounts delete <id>` | DELETE /discounts/{id} | `cli:write` | query |
| `inventory discounts restore <id>` | POST /discounts/{id}/restore | `cli:write` | body |
| `inventory discounts apply <id>` | POST /discounts/{id}/apply | `cli:write` | body |
| `inventory gift-certificates available create` | POST /gift-certs/available | `cli:write` | body |
| `inventory gift-certificates available update <id>` | PATCH /gift-certs/available/{id} | `cli:write` | body |
| `inventory gift-certificates available delete <id>` | DELETE /gift-certs/available/{id} | `cli:write` | none |
| `inventory gift-certificates issue` | POST /gift-certs/issue | `cli:write` | body |
| `inventory gift-certificates void <id>` | POST /gift-certs/{id}/void | `cli:write` | body |
| `inventory gift-certificates resend <id>` | POST /gift-certs/{id}/resend | `cli:write` | body |
| `inventory bookings cancel <id>` | POST /bookings/{id}/cancel | `cli:cs` (or `cli:write` for `refund_policy=auto`) | body |
| `inventory bookings refund <id>` | POST /bookings/{id}/refund | `cli:cs` | body |
| `inventory bookings comp <id>` | POST /bookings/{id}/comp | `cli:cs` | body |
| `inventory guests update <id>` | PATCH /guests/{id} | `cli:write` | body |
| `inventory extras create` | POST /extras | `cli:write` | body |
| `inventory extras update <id>` | PATCH /extras/{id} | `cli:write` | body |
| `inventory extras delete <id>` | DELETE /extras/{id} | `cli:write` | none |
| `inventory extras restore <id>` | POST /extras/{id}/restore | `cli:write` | body |
| `inventory questions create` | POST /questions | `cli:write` | body |
| `inventory questions update <id>` | PATCH /questions/{id} | `cli:write` | body |
| `inventory questions delete <id>` | DELETE /questions/{id} | `cli:write` | none |
| `inventory questions restore <id>` | POST /questions/{id}/restore | `cli:write` | body |
| `inventory categories create` | POST /categories | `cli:write` | body |
| `inventory categories update <id>` | PATCH /categories/{id} | `cli:write` | body |
| `inventory categories delete <id>` | DELETE /categories/{id} | `cli:write` | none |
| `inventory media upload <product-id>` | POST /products/{id}/media | `cli:write` | none (multipart) |
| `inventory media delete <id>` | DELETE /media/{id} | `cli:write` | none |
| `inventory notifications resend-confirmation <booking-id>` | POST /bookings/{id}/notifications/resend-confirmation | `cli:cs` | body |

When the dry-run column says **none**, sending `--dry-run` from the CLI errors locally with `"dry-run not supported for this command"` and exit code 1 — no HTTP call is made.

## Resource directory

- [auth.md](auth.md) — `whoami`, token / ability probing.
- [products.md](products.md) — Products CRUD + restore.
- [product-options.md](product-options.md) — Product Options CRUD + restore.
- [availabilities.md](availabilities.md) — Per-date capacity + the 5 bulk-update subcommands.
- [pricing-tiers.md](pricing-tiers.md) — Pricing tiers (data-loss-adjacent delete).
- [discounts.md](discounts.md) — Discount catalog, apply, soft-delete-as-cancel.
- [gift-certificates.md](gift-certificates.md) — Sellable SKUs + issued instances.
- [bookings.md](bookings.md) — Read, cancel, refund, comp.
- [transactions.md](transactions.md) — Read-only ledger.
- [customers.md](customers.md) — Customer catalog.
- [guests.md](guests.md) — Per-booking guest reads + edits (Greek passport workflow).
- [extras.md](extras.md) — Add-ons CRUD + restore.
- [questions.md](questions.md) — Booking questions CRUD + restore.
- [categories.md](categories.md) — Product categories CRUD.
- [media.md](media.md) — Product images + documents.
- [notifications.md](notifications.md) — Booking confirmation resend.

## Tips for agents

1. **Run `ceebee inventory whoami` first.** It costs nothing, validates auth, and reports your tenant + abilities. Bail early if you're missing `cli:cs` for an op that needs it.
2. **Always `--dry-run` before destructive writes** — especially anything in the capability table flagged `cli:cs` or marked as cascade-deleting.
3. **Reads default to `table`, mutations default to `json`.** If you're piping to `jq`, force `--format json` on reads.
4. **Errors go to stderr, data to stdout.** Pipe stdout into your parser; tee stderr if you want the idempotency-key trail.
5. **For async bulk-update**, grep stderr for `BULK_UPDATE_ACCEPTED bulk_update_id=` to capture the audit-row id.
6. **Exit code 0 + stderr signal = async accepted, not synchronously done.** Treat it as "queued".
