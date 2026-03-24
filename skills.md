# CaptainBook Statistics CLI (`ceebee`) — Agent Skill Guide

You have access to `ceebee`, a CLI tool for querying the CaptainBook Statistics API. This document tells you how to use it.

## Setup

Configuration is required before use. Either:

1. **Environment variables** (recommended for agents):
   ```bash
   export CEEBEE_API_URL=https://demo.captainbook.io
   export CEEBEE_API_TOKEN=sk-xxxxxxxxxxxx
   ```

2. **Config profiles**:
   ```bash
   ceebee config add demo --url https://demo.captainbook.io --token sk-xxxxxxxxxxxx
   ```

The authenticated user must have the `view_reports` permission or `super admin` role.

## Quick Start

```bash
# Dashboard overview (one call, key KPIs)
ceebee stats summary

# Revenue for the last 30 days
ceebee stats revenue

# Bookings this month, table format
ceebee stats bookings --from 2026-03-01 --to 2026-03-24 --format table

# Revenue compared to previous period
ceebee stats revenue --from 2026-03-01 --to 2026-03-24 --compare previous

# Top 5 products by revenue, CSV
ceebee stats products --sort-by revenue --limit 5 --format csv
```

## Common Flags (all `stats` subcommands)

| Flag | Default | Description |
|---|---|---|
| `--from` | 30 days ago | Period start (YYYY-MM-DD) |
| `--to` | today | Period end (YYYY-MM-DD) |
| `--granularity` | `day` | Time series bucket: `day`, `week`, `month`, `quarter`, `year` |
| `--business-unit-id` | — | Filter by business unit |
| `--product-id` | — | Filter by product (excluded on `gift-certs`) |
| `--compare-from` | — | Comparison period start (requires `--compare-to`) |
| `--compare-to` | — | Comparison period end (requires `--compare-from`) |
| `--compare` | — | Shorthand: `previous` or `year-ago` |
| `--format` / `-f` | `json` | Output: `json`, `table`, `csv` |
| `--profile` | default | Config profile to use |
| `--verbose` / `-v` | false | Debug output to stderr |

**Constraints:** Date ranges cannot exceed 365 days. `--compare` and `--compare-from`/`--compare-to` are mutually exclusive.

## Endpoints

### `ceebee stats summary` — Dashboard Overview
**Use when:** You need a quick overview of key KPIs in one call.
Returns: revenue (gross/net/refunds), bookings (total/confirmed/cancelled), customers (total/new/returning), occupancy rate, top product, top channel.

### `ceebee stats revenue` — Revenue & Transactions
**Use when:** You need gross/net revenue, commissions, tips, refunds, or transaction counts.
Extra flags: `--payment-method`, `--origin`, `--day-of-week`

### `ceebee stats bookings` — Booking Volume
**Use when:** You need booking counts, status breakdown, guest counts, or lead time.
Extra flags: `--status`, `--product-option-id`, `--day-of-week`, `--time-from`, `--time-to`

### `ceebee stats products` — Product Rankings
**Use when:** You need top-performing products by bookings, revenue, or guests.
Extra flags: `--sort-by`, `--sort-direction`, `--limit`

### `ceebee stats resources` — Resource Rankings
**Use when:** You need to see which guides, assets, or equipment are most utilised.
Extra flags: `--resource-category`, `--sort-by`, `--sort-direction`, `--limit`

### `ceebee stats customers` — Customer Metrics
**Use when:** You need new vs returning customers, retention rate, or top spenders.
Extra flags: `--sort-by`, `--returning-only`, `--sort-direction`, `--limit`

### `ceebee stats channels` — Channel Distribution
**Use when:** You need to compare booking sources (direct, OTAs, marketplace).

### `ceebee stats occupancy` — Capacity Utilisation
**Use when:** You need slot availability, occupancy rates, or peak times.
Extra flags: `--product-option-id`

### `ceebee stats extras` — Extra/Add-on Sales
**Use when:** You need to see which extras sell most, their revenue, or attachment rate.
Extra flags: `--sort-by`, `--sort-direction`, `--limit`

### `ceebee stats discounts` — Discount Code Usage
**Use when:** You need to track which discount codes are used and their financial impact.

### `ceebee stats gift-certs` — Gift Certificate Metrics
**Use when:** You need issuance, redemption, expiry, or outstanding value of gift certificates.
Note: `--product-id` is not available for this endpoint.

## Output Formats

- **`json`** (default): Full API response envelope (meta + data + series + comparison). Best for programmatic parsing.
- **`table`**: Human-readable ASCII table. Series data omitted.
- **`csv`**: Header row + data rows. Good for spreadsheet import.

## Comparison Mode

Use `--compare previous` to compare against the immediately preceding period of equal length. Use `--compare year-ago` to compare against the same dates one year prior.

The JSON output includes a `comparison` object with `period`, `data`, `series`, and `deltas` (absolute and percentage change for each numeric field). `percentage` is `null` when the previous value was zero.

## Exit Codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Authentication failed (401) |
| 2 | Access denied (403) |
| 3 | Validation error (422) |
| 4 | Network/timeout error |
| 5 | JSON parse error |
| 6 | Configuration error |
| 7 | Server error (5xx) |
| 8 | Rate limited (429) |
| 9 | Unexpected status |

## Tips for Agents

1. **Start with `summary`** for a quick overview — one call instead of many.
2. **Use `revenue` for money questions**, `bookings` for volume questions.
3. **Use `--compare previous`** to answer "how does X compare to last month?"
4. **Use `--granularity month`** for long date ranges, `day` for short ones.
5. **All monetary values respect the tenant's currency** (from `meta.currency`).
6. **Filter by `--business-unit-id`** when the tenant has multiple locations.
7. For ranked endpoints, use `--sort-by` and `--limit` to get exactly what you need.
8. **Check exit codes** to distinguish error types programmatically.
9. **Use `--format json`** (default) for reliable parsing; `--format table` for display.
10. **Errors go to stderr**, data goes to stdout — pipe stdout safely.
