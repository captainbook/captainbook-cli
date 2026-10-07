# captainbook-cli

`ceebee` — the CaptainBook CLI. Two namespaces over two HTTP APIs owned by the
`captainbook/captainbook` server repo: `stats` (read-only analytics) and
`inventory` (read + write).

## Testing

```bash
go test ./... -race -count=1   # the gate CI runs
go vet ./...                   # second CI lane
```

`make test` exists but omits `-race`, so it is weaker than CI. Verify against the
commands above.

Expectations:

- Every changed behaviour gets a test that would catch a real regression. A test
  with no regression it would catch is not worth keeping.
- Both branches of a new conditional, and the error path, not just the happy one.
- A bug fix lands with the regression test that proves it.
- Never commit code that makes an existing test fail.

### The two spec contracts

Both APIs are described by OpenAPI specs owned upstream and vendored here
byte-identical. **Never edit a vendored spec** — a local fix forks the contract.
Upstream problems go through a ticket on `captainbook/captainbook`.

| Spec | Vendored at | Pinned by |
|---|---|---|
| Inventory CLI v1 | `api/inventory/cli-v1.yaml` | codegen + `cmd/inventory/spec_{drift,coverage}_test.go` |
| Statistics | `api/statistics/statistics-openapi.yml` | `internal/api/statistics_drift_test.go` (test-only; no generated client) |

Each spec is pinned in **both** directions, and that pairing is the point: one
direction proves every flag the CLI has exists in the spec, the other proves
every operation and field the spec has is reachable from the CLI. Neither alone
can see absence.

Three things those tests deliberately do NOT prove, each with its own guard:

- **That a value reaches the wire.** A flag can be declared and never wired, and
  both drift directions stay green. `cmd/inventory/wire_capture_test.go` and
  `cmd/stats_wire_test.go` assert on the request the server actually receives.
- **That a flag's TYPE matches the spec.** See
  `TestSpecDrift_FlagTypesMatchSpecTypes`; it reads the LIVE cobra tree, not the
  AST, because an AST walker reads a non-literal field as empty and skips the
  entity in silence.
- **That abilities are right for operations the spec does not annotate.**
  `TestSpecDrift_AbilitiesMatchSpec` scrapes prose and is silent on operations
  lacking it. Verify those against `routes/api_cli_v1.php` in the server repo.

### Regenerating the inventory client

```bash
make codegen         # speccompat → oapi-codegen
make codegen-check   # regenerate, BUILD, then assert no drift
```

`make codegen` can exit 0 while emitting Go that does not compile, which is why
`codegen-check` builds. When a spec bump breaks codegen, the fix belongs in
`tools/speccompat` (a rewrite over a temp copy), never in the vendored spec.

## Versioning

No `VERSION` file and no `CHANGELOG.md`, deliberately. The version comes from git
tags: `release.yml` fires on a pushed `v*` tag and `Makefile` derives it from
`git describe --tags`. Adding a version file would create a second source of
truth that drifts from `git describe`.
