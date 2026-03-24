# ceebee

CLI for the CaptainBook Statistics API. Query revenue, bookings, products, resources, customers, channels, occupancy, extras, discounts, gift certificates, and dashboard summary from the command line.

## Install

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
export CEEBEE_API_URL=https://your-instance.captainbook.io
export CEEBEE_API_TOKEN=your-bearer-token
```

### Config profiles

```bash
ceebee config add production --url https://your-instance.captainbook.io --token your-bearer-token
ceebee config add staging --url https://staging.captainbook.io --token staging-token
ceebee config use production
ceebee config list
```

Profiles are stored at `~/.ceebee/config.yaml` with 0600 permissions. Environment variables take precedence over profiles.

## Usage

```bash
# Dashboard overview
ceebee stats summary

# Revenue for the last 30 days (default)
ceebee stats revenue

# Bookings for a specific period
ceebee stats bookings --from 2026-03-01 --to 2026-03-24

# Top 5 products by revenue as a table
ceebee stats products --sort-by revenue --limit 5 --format table

# Revenue compared to previous period
ceebee stats revenue --from 2026-03-01 --to 2026-03-24 --compare previous

# Revenue compared to same period last year
ceebee stats revenue --from 2026-03-01 --to 2026-03-24 --compare year-ago

# CSV export
ceebee stats bookings --format csv > bookings.csv

# Filter by business unit
ceebee stats revenue --business-unit-id 42

# Use a specific profile
ceebee stats summary --profile staging

# Debug output
ceebee stats revenue --verbose
```

## Endpoints

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

Run `ceebee stats <endpoint> --help` for endpoint-specific flags.

## Output formats

- **`json`** (default) — Full API response envelope (meta, data, series, comparison)
- **`table`** — Human-readable ASCII table
- **`csv`** — Header row + data rows

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
| 1 | Authentication failed (401) |
| 2 | Access denied (403) |
| 3 | Validation error (422) |
| 4 | Network/timeout error |
| 5 | JSON parse error |
| 6 | Configuration error |
| 7 | Server error (5xx) |
| 8 | Rate limited (429) |
| 9 | Unexpected status |

## Development

```bash
make build      # Build binary
make test       # Run tests
make lint       # Run go vet
make build-all  # Cross-compile all platforms
make clean      # Remove binaries
```

## AI agents

See [skills.md](skills.md) for agent-facing documentation.

## License

Proprietary — CaptainBook.
