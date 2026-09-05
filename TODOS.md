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

## ~~Every drift test runs CLI→spec, so a spec that grows is invisible~~ — DONE

Implemented as `cmd/inventory/spec_coverage_test.go`, the reverse of
`spec_drift_test.go`: that one proves every flag the CLI HAS exists in the
spec, this one proves every operation and request field the SPEC has is
reachable from the CLI. Together they are a biconditional.

- `TestSpecCoverage_EveryOperationIsBound`: every `VERB /path` in
  `api/inventory/cli-v1.yaml` must resolve to a command in the live `Cmd()`
  tree. 115 operations, 115 bound. Verified by deletion: removing
  `bookings available-equipment-resources` fails the test naming the exact
  endpoint.
- `TestSpecCoverage_EveryRequestFieldIsReachable`: every request-body property
  must be settable by some flag, checked against BOTH the field map and the
  live tree's flags (several commands build their body by hand and never
  appear in a field map — see the entry below). 292 fields across 115
  operations. Verified by deletion: removing `--delivery-method` fails the
  test naming `delivery_method` on both product endpoints.

Both are allow-list driven, and every entry carries the reason it is
deliberately unexposed rather than a bare suppression. The first run surfaced
30 candidates; triage found 4 genuine gaps, now fixed
(`resources create/update --rating`,
`pricing-categories create/update --is-internal`), and the rest were fields
the server accepts and ignores ("Accepted but not stored — no column" on
locations, legacy aliases on pricing-tiers), a free-form object reachable only
via `--data`, or values encoded structurally (bulk-update's `setting` IS the
subcommand name).

**Why it mattered:** syncing the spec 1.4.0 → 1.6.0 produced zero test
failures while the CLI was missing an entire endpoint and the whole product
ticketing surface. Green tests were not evidence of sync.

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

## ~~A stale whoami cache can lock a token out of every ability-gated command~~ — DONE

`abilities.Preflight` returns a warm cache entry without revalidating, and
`DiskCache.Get` honours `ExpiresAt == zero` as "cached forever" — only
`Invalidate` evicts it. The gate ran before any network call, so a token whose
cached set predated an ability grant was refused LOCALLY forever: no request
went out, so nothing could 401, so nothing invalidated the entry, and
`AbilityMissingError.UserMessage` told the user to ask an admin who had
already said yes. The advertised escape hatch (`--no-cache`, named in
abilities.go's Preflight doc) was never built, so the only remedy was deleting
`~/.ceebee`'s cache file by hand.

The cache is keyed on `(host, token)`, so issuing a NEW token always sidestepped
it. The trap was the in-place upgrade, which is the supported path: the Provider
panel's `updateApiToken` rewrites the abilities array on the same token string.
Gating `bookings cancel` on `cli:cs` would have regressed exactly that flow —
under the old `cli:write` gate a stale-but-really-CS token still reached the
server and succeeded.

Found by both Codex passes on the #19 branch, independently.

**Fix:** `Runner.refuseAbility` — Refuse plus one cache-bypassing re-read on a
miss, then refuse on the fresh set. A failed re-read returns the original
ability error rather than a network error. Every CommandDef path and the
hand-built `uploadCmd` go through it. The round trip is on the refusal path
only.

`abilityRefresher` performs that re-read by calling `whoamiFn` DIRECTLY and
writing the result back with `cache.Set`. Do not "simplify" it to
`Invalidate` + `Preflight`: that was the first attempt and it is not a bypass
at all. `Invalidate` does a read-modify-write of the cache file, so on a
read-only filesystem or a full disk it fails while the stale entry stays
perfectly readable — and `Preflight` then serves the exact entry the refresh
exists to escape, with no network call and no way for the caller to notice.
`TestAbilityRefresher_BypassesCacheEvenWhenEvictionFails` pins this: its fake
cache reports successful eviction and keeps serving the stale entry anyway.
**Fixed in:** the #19 branch, alongside a concrete-nil guard on `NewDiskCache`
(its `(nil, err)` return was being assigned into a `Cache` interface, making
Preflight's `cache != nil` true and dereferencing a nil receiver).

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

## ~~Upstream: `GET /products` advertises `status=archived`, server rejects it~~ — DONE

The spec's filter enum for `GET /products` was `[draft, published, archived]`,
but the server validates the parameter with `in:published,draft` and 422s on
anything else. There is no archived state: `Product.status` is derived from the
two-state `is_active` boolean and no archived column, scope, or migration
exists.

The asymmetry was worse than a plain typo. The client-side enum gate in
`cmd/inventory/inventory.go` validates string flags against the leading
pipe-token run in their description, so `--status publshed` failed locally with
a clear allowed-values message, while `--status archived` was on the allow-list
and was waved through to earn a server 422 a round trip later.

Fixed upstream in captainbook/captainbook#8111, which dropped `archived` from
the parameter enum and documented that `status` filters `is_active`. Resolved
here by re-syncing the vendored spec, regenerating the client (the
`ListProductsParamsStatus.Archived` const is gone), narrowing the `--status`
description in `cmd/inventory/products.go`, and correcting the
`skills/products.md` warning. `TestProductsList_StatusGateRejectsArchived`
now covers the runtime gate the description drives, which
`TestSpecDrift_FlagDescriptionEnumsMatchSpec` alone never exercised.

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

## ~~speccompat rewrites `type:` sequences without checking schema position~~ — DONE

`rewriteNullableTypeArray` matched any mapping key literally named `type` whose
value was a sequence, anywhere in the document — including inside an `example:`,
`default:`, or `x-` extension object where `type` is DATA, not a schema keyword.
An upstream `example: {type: [guest, 'null']}` would have been collapsed to
`type: guest` with a fabricated `nullable: true` injected into the operator's
example. Not contrived for this spec: `granularity` is enum
`[booking, guest, extra]`.

Fixed by requiring every non-null member of the sequence to be one of the seven
JSON Schema type names (`isJSONSchemaTypeName` in `tools/speccompat/main.go`)
before rewriting. It costs nothing on real schemas — a nullable type-array has
no other legal spelling — and makes the package's "fail loudly rather than
mangle" promise true for arbitrary upstream input. Covered by the
`type sequence under example: is data, not a schema keyword` passthrough case in
`tools/speccompat/main_test.go`.

Generalises: any YAML/JSON transform keyed on a bare key NAME needs a
position/context check, because spec key names recur as data.

## `--data` numbers are rounded through float64 before they reach the wire

`JSONBodyFromArgs` (cmd/inventory/inventory.go) decodes `--data` into
`map[string]any` and re-marshals it, so every JSON number round-trips through
`float64`. Anything past 2^53 is silently changed before the request is built:

```
availabilities update av_1 --data '{"fares":[{"pricing_tier_id":"pt_7","amount":9007199254740993}]}'
  -> wire: {"fares":[{"amount":9007199254740992,...}]}
```

The typed `--fares` flag is immune (parseFaresFlag returns `json.RawMessage`,
which marshals verbatim), so the same field on the same command behaves
differently depending on which flag carried it. The server cannot reject a
value that was corrupted before it was sent.

Amounts in minor units past 2^53 are ~90 trillion of any currency, so no
realistic operator hits this — it is a correctness and honesty problem, not an
outage. It applies to every `--data`-carrying mutation, not just fares.

**Fix:** decode `--data` with `json.Decoder` + `UseNumber()`, or keep the body
as `map[string]json.RawMessage` through to the marshal.
**Why:** one flag preserves the operator's number and the other quietly does not.
**Priority:** P3 — unreachable at realistic amounts.

## Upstream: `UpdateLocationRequest` description names a property it does not declare

`api/inventory/cli-v1.yaml` `UpdateLocationRequest` says "The controller
persists only `type`, `name`, `latitude`, `longitude`, `google_place_id`,
`region`, and (when `address` is provided) `street_address`" — but `region` is
not among its `properties`, and neither is `street_address`. `CreateLocationRequest`
declares both.

So one of two things is wrong upstream: either the controller really does
persist `region` on update and the schema is missing the property (in which
case no client can send it, including this CLI), or the description is stale
copy-paste from the create schema. The read response returns no `region`
either, so the difference is not observable from the outside.

Practical effect today: a location's `region` / `city` / `country_code` /
`postal_code` can be set at create and never changed, and `skills/locations.md`
now documents them as create-only for that reason.

**Fix:** ask the server team which it is; add the property or drop it from the
description. Then re-sync and expose `locations update --region` if it is real.
**Why:** the spec is the source of truth for the drift tests, and it currently
disagrees with itself.
**Priority:** P3 — documented; no operator is blocked, they just cannot edit.

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
  `create-available`), and the `bookings cancel` ability discrepancy that
  became #19 (resolved: cancel is `cli:cs`).
- `TestSpecQueryParamsAreExposedAsFlags`: every GET's spec query params must
  have a flag or an entry in `intentionallyUnexposedQueryParams` **with a
  reason**, so a spec re-sync can't quietly add an unreachable filter.
- `TestDocsNeverUseSpaceFormBoolFlags`: no doc may print the space form of a
  bool flag (`--flag false`), which pflag parses as `--flag=true` plus a stray
  positional. Doc-driven rather than grep-driven: it resolves each invocation
  against the real cobra tree and asks the flag whether it is actually a bool,
  so a string flag legitimately taking the literal word `true` is not
  false-positived. 18 doc invocations were rewritten to the `=` form.
- `TestFlagDescriptionsHaveNoBackquotes`: cobra's `UnquoteUsage()` turns the
  first backquoted word of a description into the flag's value placeholder,
  so ``persisted as `fare` `` rendered as `--amount fare` instead of
  `--amount int`. Five flags were affected.

Validated by regressing each historical bug in turn and observing precise
file:line failure output.

Open questions filed: #18 (product `status` filter/response enum mismatch),
#19 (`bookings cancel` gated `cli:write` while sibling refund ops are
`cli:cs`) — #19 resolved in favour of `cli:cs`, confirmed against the server:
cancel is in the `abilities:cli:cs` route group and `Phase1CCancelTest` 403s a
`cli:write` token even with `refund_policy=none`. The spec's ability table
omitted cancel from its `cli:cs` row, which is what misled the binding; filed
upstream as captainbook/captainbook#8113 and **resolved** — the table now names
cancel explicitly and restates `cli:write` as the complement of `cli:cs`, so it
no longer drifts each time a resource is added. Vendored in the 1.4.0 sync.

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
- `TestSpecDrift_AbilitiesMatchSpec`: every `CommandDef.Ability` is checked
  against the spec two ways, because the spec states abilities in prose and
  neither reading covers the set alone. (1) Per-operation: an operation whose
  description says "Requires the `cli:X` ability" pins every CommandDef on
  that verb+path. (2) Cardinality: the securitySchemes table commits to a
  route count ("the five routes gated on `abilities:cli:cs`") and the CLI must
  bind exactly that many distinct cli:cs routes. Caught: `gift-certificates
  void` still bound `cli:write` after spec 1.1.0 moved it to `cli:cs` — the
  second time a hand-mirrored ability drifted, after `bookings cancel` (#19).
  The parsing helpers are unit-tested directly
  (`TestSpecDriftHelpers_ParseAbilityAnnotations`,
  `TestSpecDriftHelpers_AbilityConstValue`) so the guards that fire on a spec
  rewording are themselves exercised rather than trusted.

Validated by reverting each historical drift bug in turn and observing
precise file:line failure output.
