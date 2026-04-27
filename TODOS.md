# TODOS

## Windows support
Add `windows/amd64` and `windows/arm64` to the CI/CD build matrix and Makefile `PLATFORMS`.
Test config file path handling on Windows (`%USERPROFILE%\.ceebee\config.yaml`).

**Why:** Broadens agent compatibility for Windows-based dev environments.
**Priority:** P3 — no known Windows users yet.

## ~~Spec/code enum-drift test (inventory CLI)~~ — DONE in commit TBD

Implemented as `cmd/inventory/spec_drift_test.go`. Walks the AST of
`cmd/inventory/*.go` to extract every `CommandDef` literal and the field
map from each `JSONBodyFromArgs` call inside its Run closure, parses
`api/inventory/cli-v1.yaml` into a flat ops + schemas map (single-hop
$ref + allOf composition), and runs two assertions:

- `TestSpecDrift_FieldMapKeysExistInSpec`: every JSON key the CLI sends
  must be a property of the spec request body OR a query parameter on
  the operation. Catches the `--send-email → send_email` class.
- `TestSpecDrift_FlagDescriptionEnumsMatchSpec`: every `FlagDef`
  whose description starts with a `tok|tok|tok` run is set-equal-checked
  against the spec enum at the corresponding field. Catches the
  booking-status / gift-cert-status / transactions-type drift class.

Verified to catch all four historical drift bugs by reverting each fix
in turn and observing test failure with the expected file/line/flag
detail.
