# TODOS

## Windows support
Add `windows/amd64` and `windows/arm64` to the CI/CD build matrix and Makefile `PLATFORMS`.
Test config file path handling on Windows (`%USERPROFILE%\.ceebee\config.yaml`).

**Why:** Broadens agent compatibility for Windows-based dev environments.
**Priority:** P3 — no known Windows users yet.

## intSlice flags can't express an empty list

Five flags are declared `Type: "intSlice"`: `availabilities create-rule --weekdays`,
`gift-certificates {create,update}-available --amounts`, and
`products {create,update} --category-ids`. pflag's intSlice parser runs
`strconv.Atoi` on the raw value, so the empty form `--flag=` fails at parse time
with `invalid syntax` — there is no way to send `[]` through any of them.

`bookings set-resources --auxiliary-resource-ids` hit this and was switched to
`stringSlice` with manual int parsing (which also enforces the spec's per-item
`minimum: 1`). The other five are only a latent limitation today because none of
them documents an empty value as meaningful — but `--category-ids=` (detach all
categories) and `--amounts=` (clear all denominations) are plausible operations
a user would expect to work.

Note also that any test constructing `RunArgs{Flags: ...}` by hand cannot catch
this class of bug: it bypasses cobra entirely. Flag-parsing behaviour needs a
test that goes through `root.Execute()`.

**Why:** Silent capability gap — the CLI can add to these lists but never clear them.
**Priority:** P3 — no reported user need yet; convert per-flag when one appears.

## Spec-drift test can't compare a flag's Go TYPE against the spec

`flagLit` in `cmd/inventory/spec_drift_test.go` captures only `Name` and
`Description`, so no drift test compares a `FlagDef.Type` against the spec
parameter's schema type. This is not hypothetical: `GET /answers` types
`question_id` and `product_option_id` as `integer` while every other endpoint
carrying `product_option_id` types it `string`, and nothing mechanical would
have caught picking the wrong one. The failure is silent rather than loud —
`args.FlagInt()` on a value stored as a string falls through its type
assertion to the zero value instead of failing to compile.

**Fix:** add `Type` to `flagLit`, extract it in `extractCmdLit`, and assert
integer→int / boolean→bool / string→string for every GET command's flags.
**Why:** the one drift class the spec-drift suite currently cannot see.
**Priority:** P2 — a wrong choice ships a silently-empty filter.

## Body keys set outside JSONBodyFromArgs escape the field-map guard

`TestSpecDrift_FieldMapKeysExistInSpec` extracts JSON keys only from the map
literal passed to `JSONBodyFromArgs`. `availabilities update --fares` does not
travel that path — it is overlaid separately via `overlayJSONField` — so the
`fares` key is invisible to the guard that covers `capacity` / `status` /
`is_bookable`. The key is correct today; if the server renames it, the
mechanical check stays green while the CLI sends a key the server ignores and
a PATCH that silently drops the pricing overrides still returns 200.

**Fix:** teach `extractCmdLit` to collect literal key arguments to
`overlayJSONField` into `FieldMap`, or assert the overlay key against the spec
in a dedicated test.
**Why:** the guard's coverage silently ends where the code stops using the helper.
**Priority:** P2 — same blast radius as any other renamed body key.

## speccompat rewrites `type:` sequences without checking schema position

`rewriteNullableTypeArray` matches any mapping key literally named `type` whose
value is a sequence, anywhere in the document — including inside an `example:`,
`default:`, or `x-` extension object where `type` is DATA, not a schema keyword.
An upstream `example: {type: [image, 'null']}` would be silently rewritten to
`type: image` plus a spurious `nullable: true` injected into the example. Not
reachable today (the vendored spec has exactly two `type: [` occurrences, both
in schema position), but silent mangling is precisely what the tool's
narrow-scope design promises not to do.

**Fix:** gate the rewrite on the enclosing mapping looking like a schema, or on
not being nested under `example:` / `default:`; add a passthrough test case.
**Why:** violates the shim's own "fail loudly rather than mangle" contract.
**Priority:** P3 — not reachable with the current upstream spec.

## Upstream: `/answers` types two ids as integer, inconsistent with every other endpoint

`GET /answers` declares `question_id` and `product_option_id` as
`type: integer`, but `Question.id`, `Answer.question_id`, `ProductOption.id`
and `GET /questions?product_option_id` are all `type: string`, and the docs use
prefixed ids (`q_42`, `po_88`) throughout. The CLI matches the spec, so
`answers list --product-option-id po_88` fails to parse while
`availabilities list --product-option-id po_88` works. Flag descriptions note
the divergence as a stopgap.

**Fix:** raise with the server repo so `/answers` matches the rest of the
surface; drop the flag-description caveat and switch to string flags once it does.
**Why:** an operator copying a working id between commands hits a parse error.
**Priority:** P3 — cosmetic until someone copies an id between the two.

## Bare true/false can still be swallowed as a MISSING positional

`bindCommands` now binds `cobra.NoArgs` on argument-less leaves, so
`gift-certificates issue --send-now false` errors instead of silently sending
the email. But `ExactArgs(N)` only catches the stray token when the user
supplied all N positionals. Verified residual: `bookings cancel --dry-run false`
with the id omitted passes arg validation with `PathArgs=["false"]` and builds a
request against booking id `"false"` carrying `dry_run: true` — the opposite of
what was typed. It degrades to a 404 rather than a wrong write, so it is not
dangerous, but it is the same silence and nothing guards it.

**Fix:** a bool-flag-aware Args validator that rejects a bare `true` / `false`
positional on ANY command, replacing the plain `ExactArgs` / `NoArgs` pair.
**Why:** the space-form bool trap has one corner left.
**Priority:** P3 — degrades to a 404, not a wrong write.

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
