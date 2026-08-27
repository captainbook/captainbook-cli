# TODOS

## Windows support
Add `windows/amd64` and `windows/arm64` to the CI/CD build matrix and Makefile `PLATFORMS`.
Test config file path handling on Windows (`%USERPROFILE%\.ceebee\config.yaml`).

**Why:** Broadens agent compatibility for Windows-based dev environments.
**Priority:** P3 — no known Windows users yet.

## ~~Docs/code drift tests (skills + README)~~ — DONE

Implemented as `cmd/inventory/skills_drift_test.go`. Complements
`spec_drift_test.go`: that one checks code against the spec, this one
checks the **docs** against the command tree the CLI actually builds
(walked live from `Cmd()`, so it needs no config or network).

- `TestSkillsDocDrift`: every `ceebee inventory …` invocation in a fenced
  code block across `skills/*.md` + `README.md` must resolve to a real
  command, use only declared flags, and not pass `--dry-run` to a
  `DryRunNotSupported` endpoint. Caught: `bulk-update pricing --fare`
  (real flag is `--fares`, a JSON array), `pricing-tiers delete --dry-run`,
  `products list --status` (documented in README, flag did not exist),
  `guests update --custom-attributes`, `notifications resend-confirmation`,
  and `<resource> show` in 8 docs (the verb is `get` everywhere).
- `TestSkillsDocEndpointTables`: every row of the `| command | METHOD /path |
  ability | dry-run |` grids is checked against the command's bound verb,
  path, ability, and DryRunMode. Caught: `discounts update` and
  `categories create/update/delete` (no such spec operations), three wrong
  gift-cert paths, `gift-certificates available create` (verb is
  `create-available`), and the `bookings cancel` ability discrepancy now
  tracked in #19.
- `TestSpecQueryParamsAreExposedAsFlags`: every GET's spec query params must
  have a flag or an entry in `intentionallyUnexposedQueryParams` **with a
  reason**, so a spec re-sync can't quietly add an unreachable filter.
- `TestFlagDescriptionsHaveNoBackquotes`: cobra's `UnquoteUsage()` turns the
  first backquoted word of a description into the flag's value placeholder,
  so ``persisted as `fare` `` rendered as `--amount fare` instead of
  `--amount int`. Five flags were affected.

Validated by regressing each historical bug in turn and observing precise
file:line failure output.

Open questions filed: #18 (product `status` filter/response enum mismatch),
#19 (`bookings cancel` gated `cli:write` while sibling refund ops are
`cli:cs`).

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
