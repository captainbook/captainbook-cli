# TODOS

## Windows support
Add `windows/amd64` and `windows/arm64` to the CI/CD build matrix and Makefile `PLATFORMS`.
Test config file path handling on Windows (`%USERPROFILE%\.ceebee\config.yaml`).

**Why:** Broadens agent compatibility for Windows-based dev environments.
**Priority:** P3 — no known Windows users yet.

## Spec/code enum-drift test (inventory CLI)
Add a test that walks every `FlagDef.Description` in `cmd/inventory/*.go`,
parses any `|`-separated tokens it finds, locates the matching spec field
in `api/inventory/cli-v1.yaml` (via the flag-name → JSON-key map already
in each Run closure), and asserts the description tokens match the spec's
`enum:` list verbatim.

**Why:** During PR review on the inventory CLI v1 work, automated tooling
and codex caught three instances of enum/string drift between flag help
text and the spec within two days:
- `--booking-status` advertised `confirmed|pending|cancelled|expired`;
  spec is `ON_HOLD|CONFIRMED|EXPIRED|CANCELLED` (uppercase, no "pending").
- gift-cert `--status` advertised `active|redeemed|voided`; spec is
  `active|redeemed|partial|void|expired` ("voided" doesn't exist).
- `--send-email` mapped to JSON `send_email`; spec field is `send_now`
  (server silently dropped, gift-cert emails never sent on issue).

The drift is silent for the agent (the help text reads, the JSON sends,
the server returns 2xx because the unknown field is ignored) and only
surfaces via review or production failure. A targeted CI test would
catch the entire class.

**Implementation sketch:**
1. Walk `cmd/inventory/*.go` (AST or regex `FlagDef{`-blocks); collect
   `(file, command-Use, flag-Name, flag-Description)` tuples.
2. For each tuple, look up the corresponding `JSONBodyFromArgs` map call
   in the same Run closure to get the flag-name → JSON-key mapping.
3. Parse `api/inventory/cli-v1.yaml`; flatten to
   `map[verb+path]map[json-key]{type, enum?}`.
4. If the spec field has an `enum:` list, parse the description's
   `|`-separated tokens and assert the sets match (order-independent,
   case-sensitive — `pending` vs `ON_HOLD` is the bug).
5. If the description has no `|`, skip — only enums are validated.

A second, simpler check worth bundling: assert every JSON key produced
by `JSONBodyFromArgs` exists as a property on the spec's request body
schema. Catches the `--send-email → send_email` class even when no enum
is involved.

**Effort:** S–M (1–2 hours with a one-off YAML-to-flat-map parse).
**Priority:** P2 — this bug class has shipped to a PR three times in
two days. Worth automating before the surface grows.
