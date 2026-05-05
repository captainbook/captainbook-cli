# TODOS

## Windows support
Add `windows/amd64` and `windows/arm64` to the CI/CD build matrix and Makefile `PLATFORMS`.
Test config file path handling on Windows (`%USERPROFILE%\.ceebee\config.yaml`).

**Why:** Broadens agent compatibility for Windows-based dev environments.
**Priority:** P3 — no known Windows users yet.

## ~~Spec/code drift tests (inventory CLI)~~ — DONE

Implemented as `cmd/inventory/spec_drift_test.go`. Walks the AST of
`cmd/inventory/*.go` and parses `api/inventory/cli-v1.yaml` (single-hop
`$ref` + `allOf` composition), running three assertions:

- `TestSpecDrift_FieldMapKeysExistInSpec`: every JSON key in every
  `JSONBodyFromArgs` map literal must be a property of the spec's
  request body OR a query parameter. Also catches verb/path typos (e.g.
  POST /availabilities/{id} when the spec has only PATCH there).
  Caught: `--send-email → send_email`, availability-restore POST/PATCH,
  `/auth/whoami` vs `/whoami`.
- `TestSpecDrift_FlagDescriptionEnumsMatchSpec`: every `FlagDef`
  whose description starts with a `tok|tok|tok` run is set-equal-checked
  against the spec enum at the corresponding field. Caught: booking-
  status / gift-cert-status / transactions-type / transactions-status.
- `TestSpecDrift_IdempotencyKeyThreaded`: every `gen.<Mutation>Params`
  literal MUST set `IdempotencyKey` (the set of "mutation Params" is
  derived statically from `internal/inventory/gen` so the test stays
  accurate as the spec evolves). Caught: 33 mutation closures that were
  passing empty Params, causing audit/wire key divergence.

Validated by reverting each historical drift bug in turn and observing
precise file:line failure output.
