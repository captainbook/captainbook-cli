# Auth — `ceebee inventory whoami`

The Auth namespace is a single read-only identity probe. Use it as the first call in any session: it validates the bearer token, returns the actor + tenant context, and surfaces the abilities your token carries. Cheap, and saves you from a 403 ten commands later.

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory whoami` | GET /whoami | `cli:read` (any token) | n/a (read) |

## Worked examples

### 1. Confirm auth before scripting

Intent: gate a long-running script on a working token.

```bash
ceebee inventory whoami --format json
```

Returns the envelope's `data.actor` (id, name, email), `data.tenant` (slug, currency, timezone), and `data.abilities` array. Exit code 10 means token is invalid; bail.

### 2. Check whether the token has `cli:cs`

Intent: detect whether the operator can run booking refunds before queueing them.

```bash
ceebee inventory whoami --format json | jq -r '.data.abilities | index("cli:cs") // empty'
```

Empty output → no `cli:cs`; the script should skip refund operations and emit a warning. Anything else (the index) → ability is present.

### 3. Print human-readable status

Intent: a quick visual check at the top of an interactive session.

```bash
ceebee inventory whoami
```

Default `--format table` prints actor, tenant, abilities, token expiry as four neat rows.

### 4. Check tenant + currency before a money-shaped operation

Intent: confirm you're talking to the right tenant before issuing a €500 gift cert.

```bash
ceebee inventory whoami --format json | jq '{slug: .data.tenant.slug, currency: .data.tenant.currency}'
```

Returns e.g. `{"slug":"demo","currency":"EUR"}`. If this prints `JPY` and you were planning EUR amounts, abort — money fields are minor units in the *tenant's* currency, so `15000` would be ¥15000 not €150.00.

## Pitfalls

- ⚠️ **A 401 from `whoami` doesn't always mean a bad token** — it can also mean the token's `expires_at` has passed (Sanctum 4 enforces this framework-side). Check the issuance UI before re-issuing.
- ⚠️ **Token leakage:** the bearer token is the credential of record (no TLS pinning). Treat `CEEBEE_API_TOKEN` and `~/.ceebee/config.toml` as secrets — both end up in the audit log if you `cat` them inside a piped command.
- ⚠️ **Cross-tenant tokens silently 401:** Sanctum scopes tokens to the issuing tenant. Pointing `CEEBEE_API_URL` at a different tenant's subdomain returns 401, not a clear "wrong tenant" error.

## See also

- [index.md](index.md#token-abilities) — full ability table.
- All other cookbooks — every command starts with the same auth context.
