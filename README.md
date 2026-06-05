<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="art/logo-light.svg">
    <source media="(prefers-color-scheme: light)" srcset="art/logo.svg">
    <img alt="ceebee" src="art/logo.svg" width="280">
  </picture>
</p>

<p align="center">
  CLI for the CaptainBook API.<br>
  Read statistics, manage inventory, run customer-success operations.<br>
  Built for agents (Claude Code &amp; co.) — idempotent, dry-runnable, audited.
</p>

## Install

### One-liner

```bash
curl -fsSL https://captainbook.github.io/captainbook-cli/install.sh | sh
```

Auto-detects your OS and architecture, downloads the latest release, verifies the checksum, and installs to `$HOME/.local/bin` when available (no sudo) or falls back to `/usr/local/bin` (sudo).

Override the install location with `PREFIX`:

```bash
curl -fsSL https://captainbook.github.io/captainbook-cli/install.sh | PREFIX="$HOME/.local/bin" sh
```

### From source

```bash
go install github.com/captainbook/captainbook-cli@latest
```

### From release

Download the binary for your platform from [GitHub Releases](https://github.com/captainbook/captainbook-cli/releases) and place it on your PATH.

### Build locally

```bash
git clone https://github.com/captainbook/captainbook-cli.git
cd captainbook-cli
make build
```

## Setup

### Environment variables

```bash
export CEEBEE_API_URL=https://your-tenant.captainbook.io/api/v1/cli
export CEEBEE_API_TOKEN=your-bearer-token
```

The URL must include the `/api/v1/cli` base — the same path stats and inventory both live under. (Earlier releases accepted a bare tenant root for `stats` only; both command groups now share one base.)

### Config profiles

```bash
ceebee config add production --url https://your-tenant.captainbook.io/api/v1/cli --token your-bearer-token
ceebee config add staging    --url https://staging.captainbook.io/api/v1/cli     --token staging-token
ceebee config use production
ceebee config list
```

Profiles are stored at `~/.ceebee/config.yaml` with `0600` permissions.

**Resolution order:**
- Explicit `--profile <name>` always wins — env vars are ignored.
- Without `--profile`, `CEEBEE_API_URL` / `CEEBEE_API_TOKEN` override the default profile (partial overrides allowed).

Run with `--verbose` to see which source was used (`profile:sandbox`, `env`, or `env+profile:sandbox`).

### Token abilities

The CLI talks to `https://{tenant_slug}.captainbook.io/api/v1/cli/*` with a Sanctum bearer token carrying one or more abilities:

- `cli:read` — list / show / get
- `cli:write` — create / update / delete / restore / attach / detach
- `cli:cs` — customer-success ops (`bookings refund`, `bookings comp`, `bookings resend-confirmation`)

Inspect what your token has:

```bash
ceebee inventory whoami
```

## Two namespaces

```text
ceebee stats …       # read-only analytics over revenue, bookings, customers, channels
ceebee inventory …   # read+write: products, options, pricing, availabilities, bookings, …
ceebee audit …       # local-only mutation audit log (~/.ceebee/audit.jsonl)
```

### `ceebee stats` — analytics

```bash
ceebee stats summary                                       # dashboard overview
ceebee stats revenue --from 2026-03-01 --to 2026-03-24
ceebee stats bookings --format csv > bookings.csv
ceebee stats products --sort-by revenue --limit 5 --format table
ceebee stats revenue --compare year-ago
ceebee stats summary --business-unit-id 42
```

| Command | Description |
|---|---|
| `stats summary` | Dashboard KPIs (revenue, bookings, customers, occupancy) |
| `stats revenue` | Gross/net revenue, commissions, tips, refunds |
| `stats bookings` | Booking volume, status breakdown, lead time |
| `stats products` | Product rankings by bookings, revenue, or guests |
| `stats resources` | Resource utilisation rankings |
| `stats customers` | New vs returning, retention rate, top spenders |
| `stats channels` | Booking channel distribution |
| `stats occupancy` | Slot availability and capacity utilisation |
| `stats extras` | Extra/add-on sales performance |
| `stats discounts` | Discount code usage statistics |
| `stats gift-certs` | Gift certificate issuance and redemption |

### `ceebee inventory` — read + write

The inventory namespace covers 90+ endpoints across 18 resources. Every mutation supports per-call idempotency (UUIDv7 minted automatically), per-endpoint dry-run where the server allows it, and is audited to `~/.ceebee/audit.jsonl`.

```bash
# Read
ceebee inventory products list --status published
ceebee inventory products get 42 --format json

# Write — private experience
ceebee inventory products create \
  --title "Sunset Sail" --currency EUR --schedule-type datetime \
  --status published --is-private --capacity 8 --from-price 35000

# Write — recurrence rule
ceebee inventory availabilities create-rule \
  --product-option-id 47 \
  --start-date 2026-05-01 --end-date 2026-08-31 \
  --weekdays 6 --start-time 14:00 --end-time 18:00

# Write — attach a boat to a product option
ceebee inventory resources create --name "Oceanis 449" --type Sailboat --category asset --capacity 8
ceebee inventory resources attach 47 --resource-id 2

# Customer-success
ceebee inventory bookings cancel <booking-id> --reason "weather" --refund-policy full
ceebee inventory gift-certificates issue --available-gift-certificate-id <sku-id> \
  --recipient-email alex@example.com --recipient-name "Alex Doe" --amount 10000
```

| Resource | Verbs available |
|---|---|
| `whoami` | get |
| `products` | list, get, create, update, delete, restore |
| `product-options` | list, get, create, update, delete, restore |
| `availabilities` | list, get, update, delete, **bulk-update** (capacity / booking-status / pricing / start-time / end-time), **bulk-delete**, **create-rule** |
| `pricing-categories` | list, get, create, update, delete, restore |
| `pricing-tiers` | list, get, create, update, delete, restore |
| `resources` | list, get, create, update, delete, restore, **attach**, **detach** |
| `locations` | list, get, create, update, delete |
| `bookings` | list, get, transactions, cancel, refund, comp, resend-confirmation |
| `transactions` | list, get |
| `customers` | list, get |
| `guests` | list, get, update |
| `extras` | list, get, create, update, delete, restore |
| `questions` | list, get, create, update, delete, restore |
| `discounts` | list, get, create, delete, apply, restore |
| `gift-certificates` | list-available, get-available, create-available, update-available, delete-available, list-issued, get-issued, issue, void, resend |
| `media` | list, upload, delete |
| `categories` | list, get *(read-only)* |
| `notifications` | resend |
| `workflows` | list, get, create, update, delete, restore, **activate**, **deactivate**, **trigger** (create / update), **steps** (create / update / delete) |
| `workflow-executions` | list, get, logs *(read-only)* |

Run `ceebee inventory <resource> --help` for full flag listings. See [skills/index.md](skills/index.md) for agent-facing cookbooks per resource.

### `ceebee audit` — local mutation log

Every successful mutation appends to `~/.ceebee/audit.jsonl` with the idempotency key, body sha256, ability used, dry-run flag, status code, duration, and a forensic summary of the relevant fields.

```bash
ceebee audit list --limit 20         # newest-first
ceebee audit show <idempotency-key>  # full row by idempotency key
```

Used to answer "who ran what against which tenant when?" without any server round-trip.

## Output formats

- **`json`** (default for mutations) — full API response envelope (`meta`, `data`)
- **`table`** (default for reads) — human-readable ASCII
- **`csv`** — header + rows

Override with `--format <json|table|csv>` on any command.

## Dry-run

Most mutations support `--dry-run` and return a colored diff envelope (`{ would_apply: true, diff: { before, after } }`) without persisting. The CLI rejects `--dry-run` at parse time on endpoints where the server doesn't support it (e.g. `products delete`, `media upload`), with a typed error and exit code 1.

```bash
ceebee inventory products update 42 --title "New title" --dry-run
```

## Idempotency

UUIDv7 is auto-minted per call and sent as the `Idempotency-Key` header. Override with `--idempotency-key <uuid>` to replay a specific call (server returns the cached original response). Retries within an invocation reuse the same key automatically.

## Shell completions

```bash
# Bash
source <(ceebee completion bash)

# Zsh
ceebee completion zsh > "${fpath[1]}/_ceebee"

# Fish
ceebee completion fish | source
```

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | CLI usage error (unknown flag, missing subcommand, dry-run not supported) |
| 10 | Authentication failed (401) |
| 11 | Access denied (403) |
| 12 | Validation error (422) |
| 13 | Network/timeout error |
| 14 | JSON parse error |
| 15 | Configuration error |
| 16 | Server error (5xx) |
| 17 | Rate limited (429) |
| 18 | Unexpected status |

## Development

```bash
make build           # Build binary
make test            # Run tests
make codegen         # Regenerate OpenAPI client from api/inventory/cli-v1.yaml
make codegen-check   # Fail if generated code is out of sync
make lint            # Run go vet
make build-all       # Cross-compile all platforms
make clean           # Remove binaries
```

The inventory CLI's wire shapes are codegen'd from `api/inventory/cli-v1.yaml`. The hand-written cobra layer in `cmd/inventory/` references the generated client. Three CI-enforced spec-drift tests catch flag/JSON-key drift, enum-token drift, and idempotency-key threading regressions.

## AI agents

The agent-facing entry point is **[skills/index.md](skills/index.md)** — start there for a global tour and per-resource cookbooks. Each cookbook covers worked examples, side-effect maps, and pitfalls specific to that resource.

## License

Proprietary — CaptainBook.
