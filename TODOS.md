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

## ~~Spec-drift test can't compare a flag's Go TYPE against the spec~~ — DONE

Closed by `TestSpecDrift_FlagTypesMatchSpecTypes` in
`cmd/inventory/spec_drift_test.go`, added during the cli-v1 1.29.0 sync.

**What it was:** no drift test compared a `FlagDef.Type` against the spec
parameter's schema type, so the CLI declared `string` flags against
`type: integer` params for several releases and nothing noticed. The failure was
silent rather than loud — `args.FlagInt()` on a value stored as a string falls
through its type assertion to the zero value instead of failing to compile.

**How it was closed, and why not the obvious way:** the entry originally proposed
adding `Type` to `flagLit` and extracting it in `extractCmdLit` — i.e. reading the
AST. That would have reintroduced a known hole: an AST walker reads a non-literal
field value as EMPTY and skips the entity in SILENCE, so a loop-built `Flags` list
would simply not be checked. The test instead walks the LIVE cobra tree from
`Cmd()`, reading each flag's real type via `pflag.Flag.Value.Type()` and the
operation from the `verb`/`path` annotations `bindCommands` sets. It carries a
fail-closed `if checked == 0 { t.Fatal }` guard, because a walker that resolves
nothing would otherwise pass vacuously — the same silent-skip failure in a new
costume.

**Verified by seeding:** declaring `partners list --limit` as `string` against the
spec's `integer` still COMPILES, and the test names it. That is the point — the
compiler caught the 12 call sites in this sync only because codegen happened to
emit a mismatched Go type; a `string` flag against an integer param that codegen
still models as `*string` would have sailed through.

It compared 301 flag types on the sync that introduced it, with
`flagTypeExceptions` empty.

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

**Still open, and now narrower.** The `json`-typed flags added in the cli-v1
1.29.0 sync (`--steps`, `--trigger`, `--times`, `--conditions`) do NOT have this
bug: they carry `json.RawMessage` and are validated rather than decoded, so the
operator's bytes reach the wire verbatim — the same approach `parseFaresFlag`
already used. See `parseJSONFlag` in `cmd/inventory/inventory.go`.

**Narrower again after the cli-v1 1.34.0 sync.** `companions` is the first field
read back OUT of `--data` as raw bytes rather than off the re-marshalled body
(`companionsRawFromArgs`), so a `category_id` past 2^53 now survives that path
verbatim. It is one field, not a fix: every other `--data` key still round-trips.
The pattern generalises, though — read the key from `args.RawData` and overlay
the RawMessage — so it is the shape a full fix would take.

That leaves `--data` itself as the one remaining path that still round-trips
numbers through `float64`, which is what the rest of this entry describes. Fixing
it means changing `JSONBodyFromArgs` to merge into `json.RawMessage` (or decode
with `UseNumber`) instead of `map[string]any`, and that touches every mutation,
so it was deliberately left out of the sync.

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

## Upstream: `GET /workflow-nodes` returns `config: []` for every trigger, where the spec declares an object

`WorkflowNode.config` is `type: object` with string values in
`docs/api/cli-v1.yaml`, and the server honours that for `actions` and `logic`
nodes — `send_notification` returns
`{"channel": "required|string|in:mail,sms,push,wallet", ...}`. But all 21
`triggers` come back as `config: []`: a PHP empty associative array serialises
to a JSON **array**, not `{}`.

oapi-codegen generates `map[string]string` from the spec (correctly), so the
decode fails:

```
json: cannot unmarshal array into Go struct field WorkflowNode.data.triggers.config of type map[string]string
```

Measured against `oooo.captainbook.test` on 2026-10-06:

| invocation | result |
|---|---|
| `workflows nodes --kind action` | OK |
| `workflows nodes --kind logic` | OK |
| `workflows nodes --kind trigger` | FAILS |
| `workflows nodes` (default, all kinds) | FAILS |

So the **default invocation is unusable**, which matters more than it looks:
`workflow-nodes` is the only way a caller learns which `action_type` values
exist and what each config must contain, and it is what `workflows create
--trigger/--steps` depends on.

**Why this is not fixed here:** the server contradicts its own published spec,
and `api/inventory/cli-v1.yaml` is vendored byte-identical — editing it to say
`oneOf: [object, array]` would fork the contract and bake the bug into every
generated client. The fix belongs in the server: cast the empty config to an
object (`(object) $config`, or `JSON_FORCE_OBJECT` on that field) so an empty
config serialises as `{}`.

**How to apply:** file this upstream on captainbook/captainbook, then re-run
`ceebee inventory workflows nodes` to confirm. No CLI change should be needed —
the generated type is already right. Until then, `--kind action` and
`--kind logic` are the usable paths and the command's `--help` says so.

## Upstream: abilities are not declared inline for the 11 operations added between cli-v1 1.6.0 and 1.29.0

`TestSpecDrift_AbilitiesMatchSpec` check 1 maps each operation's required
ability to its `CommandDef.Ability` by scraping the spec prose
``Requires the `cli:x` ability``. None of the 11 operations added in this sync
carries that sentence, so the check is **silent on all of them** — a command
bound to `cli:read` on a `cli:write` route would ship and surface as a runtime
403 that no test catches. Only the `cli:cs` route count (5) is pinned.

Abilities for these 11 were verified by hand against
`routes/api_cli_v1.php` in the server repo on 2026-10-06:

- `cli:read` (8): `GET /partners`, `GET /partners/{id}`, `GET /segments`,
  `GET /segments/{id}`, `GET /segments/{id}/members`, `GET /segment-fields`,
  `GET /resource-calendar`, `GET /workflow-nodes`
- `cli:write` (3): `POST /segments`, `POST /products/{id}/duplicate`,
  `POST /availabilities`
- no new `cli:cs`, so the spec's five-route count still holds

**Why this is not fixed here:** the prose lives in the vendored spec, which is
byte-identical to upstream and never edited locally.

**How to apply:** ask upstream to add the standard ``Requires the `cli:x`
ability`` sentence to those 11 operation descriptions. The CLI side then needs
no change — the check starts covering them on the next sync. The regex is
`declaredAbilityRe` in `cmd/inventory/spec_drift_test.go`.

## A `ForensicFields` entry is required to be CORRECT, but not required to EXIST

`TestCommandDefIntegrity_ForensicFieldsNameRealFlags` proves every
`ForensicFields` entry names a real flag, so a misspelling fails. Nothing proves
an entry is PRESENT. The audit stores only `body_sha256` plus the values of
listed flags, so a destructive flag left off the list is unreconstructable in
incident response.

This has already bitten once: `products update --confirm-ticket-reissue`
irreversibly invalidates customer QR codes un-notified and was omitted, while
far tamer flags like `is-private` were captured.

**Pros:** closes the only open direction of a guard whose other direction is
already enforced.
**Cons:** "destructive" needs a definition the test can apply. Every write flag
would be noisy; a curated list is another allow-list to maintain.

**Context / where to start:** the hard constraint is that such a test must read
the LIVE command object or an annotation, NOT the AST. `ForensicFields` built in
a loop parses as EMPTY in an AST walker and the entity is skipped in silence —
that failure mode is documented at `cmd/inventory/inventory.go` where
`bindCommands` annotates `forensicFields` precisely so tests can assert on it.
Start from that annotation.

**Effort:** human M / CC S. **Priority:** P2. **Depends on:** nothing.

## No request-side size cap when a flag reads `@file.json`

**Update:** the dangerous half of this is fixed. An oversized `@file.json` used to
land verbatim in `forensic_summary` and make the ENTIRE audit log unreadable
(`bufio.Scanner: token too long` for every key, not just the big one);
`readJSONL` now uses a growing `bufio.Reader`, covered by
`TestAuditReader_OneHugeEntryDoesNotHideTheRest`. What remains below is only the
memory question, which is the user's own process and their own file.


Responses are capped at 10MB (`internal/api/client.go`, `io.LimitReader`). The
request side has no equivalent: `readDataFlag` reads the whole named file into
memory, and the `json` flag type added in this sync is a second caller on that
path.

Impact is a user exhausting their own memory with their own file — no server,
tenant or multi-user exposure. Recorded because the asymmetry is now reachable
from two places rather than one.

**Cons:** picking the cap is a guess. Nothing in the spec documents a
request-body size limit, and the one 10 MiB figure it does mention is a
tenant-raisable default, so a CLI-side cap would be inventing a server limit
rather than mirroring a stated one.

**Effort:** human S / CC S. **Priority:** P3. **Depends on:** nothing.

## `--data` cannot satisfy a flag the command marks required

`bindCommands` calls `MarkFlagRequired` for every required field, and cobra
enforces that during parsing — before `JSONBodyFromArgs` ever reads `--data`. So
`workflows create --data @workflow.json` fails with `required flag(s) "name" not
set` even when the file supplies `name`, and the only way to send a complete
body from a file is to repeat each required field as a flag.

Found while adding the `--data` + flag precedence case to
`TestWire_JSONFlagSourcesAndPrecedence` (`cmd/inventory/wire_capture_test.go`);
the test passes `--name` for exactly this reason.

**Fix sketch:** relax the required-flag check to a post-parse validation that
also considers `--data`'s keys — i.e. drop `MarkFlagRequired` and assert the
assembled body instead. That moves the error from cobra's parser to the command,
so the message and exit code have to be built rather than inherited.

**Cons:** the current failure is loud, early and correct for the common case, and
`--help` marking a flag required is honest. Changing it trades a clear parse
error for a later, hand-written one, and weakens `--help`.

**Effort:** human S / CC S. **Priority:** P3. **Depends on:** nothing.


## ~~`--limit 0` means two different things in the two namespaces~~ — DONE

Closed together with the entry below by `FlagDef.Min`, validated once in
`makeRunE`. `inventory products list --limit 0` and `stats products --limit 0` now
produce the same refusal; `--limit 1` and `--limit 50` still work, and an unset
`--limit` still means unfiltered.

**What it was:** the inventory side used `if v := args.FlagInt("limit"); v != 0` at
27 call sites, so an explicit 0 was dropped and the server's default page came back
presented as the caller's requested size — while the statistics side refused it.
Same flag, same spec rule (`minimum: 1`), opposite behaviour.

## ~~The `v < 1` integer guard is open-coded 14 times~~ — DONE

Replaced by `FlagDef.Min`, an inclusive lower bound declared on the flag and
enforced once in `makeRunE`'s collection loop. 13 hand-written blocks removed;
41 flag declarations now carry `Min: 1`.

**Why it mattered, concretely:** this duplication is what hid the worst bug in this
sync. The `--product-id`/`--availability-id` exclusion gate in `pricing_tiers.go`
read two *int* flags with `args.FlagString`, which type-asserts to string and
returns `""` for an int flag — so the gate could never fire, the two params went to
the wire together, and the server answered the exact 422 the gate existed to
pre-empt. The compiler, both drift directions and the whole suite stayed green.

**Three sites were deliberately NOT converted:**
- `bookings.go` `--main-resource-id` keeps its bespoke check, because its message
  carries domain information a generic bound cannot ("a booking's main resource
  cannot be cleared, only replaced").
- `availabilities.go` `--availability-ids` and `bookings.go`'s resource-id lists
  parse their own strings with their own 1000-id cap, so a scalar `Min` does not
  apply. See the remaining `must be >= 1` occurrences.

**Guarded by** `TestWire_FlagDefMinIsEnforcedForEveryBoundedFlag`, which derives
its cases from the live command tree via a new `intMins` annotation rather than a
hand-written list, so a newly bounded flag cannot land unexercised — it covered 35
bounded flags on the sync that introduced it, at both the rejected and accepted
ends. Removing the central check fails it 70 times.

**And** `TestWire_UnsetIntFlagStillMeansUnfiltered`, which pins the
`if !cmd.Flags().Changed(fd.Name) { continue }` that keeps an unset int flag
(reading 0) from failing its own bound. Remove that line and `extras list` sends
`product_id=0` unasked — the original silent-widening bug in a new place.

## A committed `bulk-update` that matched nothing reports success

`POST /availabilities/bulk-update` answers 200 with `status: "no_op"` and
`bulk_update_id: null` when the filter matched zero rows. The CLI emits no signal,
exits 0, and in table mode renders it through the same diff renderer as a dry-run
preview — so "nothing was changed" is indistinguishable from "4,000 departures
were queued". The only async signal, `BULK_UPDATE_ACCEPTED`, is gated on 202.

The authors already singled out ONE instance of this: `availabilities.go:844`
notes that `from == to` "matched nothing and used to answer a cheerful 200 with
total_matched: 0, which reads as a successful change", and the server made it a
422. The general case (range × weekdays matching zero days) still answers
`no_op`, and `availabilities.go:502` documents it without signalling it.

**Fix:** read `data.status` on the 2xx path and write a stderr counterpart —
`BULK_UPDATE_NO_OP total_matched=0` — plus record it in the audit entry.
**Cons:** new stderr output for scripts to learn.
**Effort:** human S / CC S. **Priority:** P2. **Depends on:** nothing.

## A failed audit append is swallowed, and there is no intent row

`cmd/inventory/inventory.go` does `_ = r.AuditLogger.Append(auditEntry)`. A
mutation whose forensic record fails to persist — unwritable path, full disk, lock
contention — still reports success and exits 0, while `skills/index.md` presents
"Every successful mutation logs to `~/.ceebee/audit.jsonl`" as a guarantee.

Separately, the entry is written only AFTER the response returns, so a mutation
interrupted between send and response leaves no record at all even though the
server may have applied it. For a `cli:cs` money-moving operation that is the
exact case the log exists for.

**Fix:** surface the append failure (loud stderr at minimum; arguably non-zero
exit for `cli:cs`), and write an intent row — command, endpoint, idempotency key,
body hash — before `def.Run`, reconciled with the outcome row afterwards.
**Cons:** two rows per mutation changes the log's shape and every reader of it.
**Effort:** human M / CC S. **Priority:** P2. **Depends on:** nothing.

## `audit.dry_run` reports the flag, not the wire

`auditEntry.DryRun` is read from `args.DryRun`, but the body's `dry_run` can be
set independently through `--data`. So `--data '{"dry_run":true}'` records a
PREVIEWED mutation as a committed one — the audit log asserting the opposite of
what happened. The same asymmetry is already handled for the body hash, which
reflects the real wire body via `res.WireBody`.

**Precedent set by the cli-v1 1.34.0 sync, not yet applied here.**
`bulkDeleteNoOpWarnings` had the identical problem for its stderr signal — a
`--data`-set `dry_run` made it warn on a preview — and it was fixed by reading
`body["dry_run"]` from the assembled body rather than `args.DryRun`. The audit
field is the same one-line change against the same map; it was left out of that
sync because `auditEntry` is shared by every command and the change deserves its
own test per mutation shape rather than riding along with one endpoint's warning.

**Fix:** derive `DryRun` from the assembled wire body rather than the flag, the
way `bulkDeleteNoOpWarnings` now does, or refuse a `dry_run` key inside `--data`
and require the flag.
**Effort:** human S / CC S. **Priority:** P2. **Depends on:** nothing.

## `workflows create` can write a third-party secret to the audit log

`trigger` and `steps` are captured verbatim into `~/.ceebee/audit.jsonl`, and their
`config` is an operator-authored object the spec does not constrain (`config: {type:
object}`, "Validated against the resolved trigger's rules()"). The action/trigger
vocabulary includes `webhook`, so an endpoint URL, signing secret or auth header
placed in that config is persisted in plaintext across four rotations (~200 MB).

`internal/inventory/audit.go` describes the opposite intent: "This is the seam that
keeps PII off disk — the audit package never reaches into request/response bodies."
These two fields reach around that seam by being flags.

**Fix:** capture a shape-only projection (strip values under keys matching
`secret|token|key|password|authorization|signature`, or record action types plus a
per-step hash) rather than the tree verbatim.
**Cons:** a redacted tree is less reconstructable, which is the reason it was
captured in the first place — see the `segments create --conditions` rationale.
**Effort:** human S / CC S. **Priority:** P2. **Depends on:** nothing.

## `meta` is dropped in table and csv output

`internal/output/formatter.go` destructures `meta` in `formatTable` and never
renders it; `formatCSV` does not read it at all. So in the default output format
for read commands, the statistics response's `meta.filters` and `meta.dated_by`
are invisible — and the spec makes reading them a client obligation:
`meta.filters` is the server's own echo of which filters genuinely narrowed the
query ("an absent one did nothing"), and `meta.dated_by` exists "because a figure
dated by the wrong clock looks exactly like one dated by the right one".

`skills/statistics.md` tells the reader to check both. In `--format table` they
cannot. Inventory pagination state is dropped the same way on the four new
paginated list commands, which default to table.

**Fix:** render a short meta preamble in table mode — at minimum `meta.filters`,
`meta.dated_by.event` and any pagination cursor.
**Effort:** human S / CC S. **Priority:** P2. **Depends on:** nothing.

## Three `sort_by` enums are invented, and pinned in neither direction

The statistics spec's shared `sort_by` parameter has `type: string` and NO enum
("valid values depend on endpoint"). `/customers` and `/locations` override it
inline WITH an enum, so those two are pinned by the drift test. `products`,
`resources` and `extras` are not — their enums in `internal/api/endpoints.go` were
written by hand, and `TestStatisticsDrift_*` only asserts agreement when the spec
declares an enum.

If any invented set is narrower than the server's real one, a legal sort field is
refused locally and never reaches the wire. Concrete suspect: the CLI gives
`resources` `{bookings, revenue}`, while `ResourceItem` publishes `total_guests`
and `confirmed_guests` and `/locations`' spec-declared enum includes `guests`.

**Fix:** get upstream to enumerate `sort_by` per operation; failing that, record
each invented enum in an allow-list naming the server-side source it was read
from, so the gap is a decision with a citation instead of a silent hole.
**Effort:** human S / CC S. **Priority:** P2. **Depends on:** upstream.

## Body-side `product_option_id` is an unvalidated string

`availabilities create`, `bulk-update` and `bulk-delete` declare
`--product-option-id` as a plain string and send it unchecked, while the spec types
it `PositiveIntegerId` — `oneOf: [integer minimum 1, string matching
^[1-9][0-9]*$]`, with the spec noting "measured against the real validator: `5`
and `"5"` are accepted; `"abc"`, `"05"`, `"1e0"`, `"5.0"`, `true`, `0` and `-1` are
all 422". The same id on `availabilities list` is int-typed and refused locally.

`TestSpecDrift_FlagTypesMatchSpecTypes` cannot help: `expectedFlagType` returns ""
for a union, by design.

Related: `bulk-update`'s `product_option_id` is `oneOf: [PositiveIntegerId, array
of integer]` — "one product option, or a LIST of them: close these nine options
for all of October". The CLI only sends the scalar, so the list spelling is
unreachable.

**Fix:** validate the string against `^[1-9][0-9]*$` (or type it int, since the
union accepts both spellings), and add an `intSlice` path for bulk-update's list
form. Note the spec refuses a list for `setting=pricing`.
**Effort:** human S / CC S. **Priority:** P2. **Depends on:** nothing.

## ~~`stats` dates are resolved in the HOST's timezone, not the account's~~ — DONE

Raised by Optibot on #27 as a blocker. Omitted `--from`/`--to` are now ABSENT from
the query so the server applies its account-timezone defaults; `--compare` asks for
an explicit period rather than guessing a bound host-side. `defaultFrom`/`defaultTo`
are gone. Guarded by `TestStatsWire_OmittedPeriodIsLeftToTheServer`, which asserts
which keys are absent — the assertion nothing had, and the reason a wrong date
looked exactly like a right one.

### Original entry

`defaultFrom`/`defaultTo` in `cmd/stats.go` compute the default window from
`time.Now()` on the machine running the CLI, and the values are then always sent.
The spec defines `from`/`to` as days in the ACCOUNT's timezone and documents its
own defaults ("30 days before the account's today"), which the CLI therefore never
lets the server apply. A host in a different timezone silently queries a window
shifted by a day.

**Fix:** omit `from`/`to` when the caller did not pass them and let the server
default them, or resolve the account timezone first and compute from that. At
minimum say in `skills/statistics.md` that the default window is host-local.
**Cons:** omitting them changes what the CLI sends on every unparameterised call.
**Effort:** human S / CC S. **Priority:** P2. **Depends on:** nothing.

## A rate-limited `stats` call has no overall deadline

`waitBackoff`'s only production caller passes `context.Background()`, and
`maxRetryAfter` is 60 seconds with 2 retries, so a rate-limited call can sit in
sleeps for ~120s on top of 3×30s of request timeout, and the `<-ctx.Done()` arm
that exists to interrupt it can never fire. The `TimeoutError` it would eventually
return also hardcodes `defaultTimeout.String()` rather than the real wait.

**Fix:** give the stats run path a bounded context, and carry the actual duration
into the error.
**Cons:** pre-existing in substance — the 60s cap and the Background context both
predate this branch — but `waitBackoff` is new and is now the single place that
owns the wait, so it is the natural place to bound it.
**Effort:** human S / CC S. **Priority:** P3. **Depends on:** nothing.

## Upstream: `POST /segments` is the only mutation without an `Idempotency-Key`

69 of the 70 non-GET operations in `cli-v1.yaml` carry
`- $ref: "#/components/parameters/IdempotencyKey"`. `createSegment` carries no
`parameters:` block at all, so codegen emits no `*Params` type for it and there is
no field to assign the resolved key to.

The CLI works around it by setting the header through the generated method's
`reqEditors` (see `cmd/inventory/segments.go`), pinned by
`TestWire_IdempotencyKeyReachesTheWire`. That is a workaround, not a fix: the spec
still does not document the header, so any other generated client gets no key
parameter either, and the workaround is invisible to
`TestSpecDrift_IdempotencyKeyThreaded`, which only fires on a `gen.*Params`
composite literal.

It matters more here than most: a segment is live the moment it exists, and
anything bound to it acts on customers as the backfill enrols them, so a retried
create can enrol twice.

**Fix:** ticket on `captainbook/captainbook` adding the IdempotencyKey parameter to
`createSegment`; then drop the reqEditors workaround and thread
`&gen.CreateSegmentParams{IdempotencyKey: args.IdempotencyKeyUUID}` like the other
69. **Never** patch the vendored spec locally.
**Effort:** human S / CC S. **Priority:** P2. **Depends on:** upstream.

## `TestSpecDrift_IdempotencyKeyThreaded` is blind to a missing `Params`

It walks the AST for a `gen.*Params` COMPOSITE LITERAL and asserts the literal
sets `IdempotencyKey`. A mutation closure that constructs no Params at all is
never visited — so the guard cannot see the one absence it exists to catch, which
is exactly how `segments create` shipped unthreaded.

`TestWire_IdempotencyKeyReachesTheWire` now covers the two known cases from the
wire side, which is the stronger check, but it enumerates commands by hand.

**Fix:** invert the AST test — walk every `KindMutation` CommandDef and fail when
its closure passes no Params — or extend the wire test to every mutation
generically.
**Why:** same silent-skip class as the AST-vs-live-object problem already
documented for `ForensicFields` and flag types.
**Effort:** human S / CC S. **Priority:** P2. **Depends on:** nothing.

## ~~`resource-calendar` hides `unavailable_resources` in table and csv output~~ — DONE

Raised by Optibot on #27 as a blocker. Now emitted on stderr as
`RESOURCE_CALENDAR_INCOMPLETE resource_id=<id> reasons=<...>`, in the same
key=value shape as `BULK_UPDATE_ACCEPTED`, so stdout stays the data contract and
the signal survives table, json and csv alike. Guarded by
`TestWire_ResourceCalendarSignalsIncompleteAnswers`, which asserts the warning in
all three formats AND that a complete answer stays silent — a signal that fires
always is a signal that gets ignored.

The general `meta`-is-dropped problem below is still open; this was the instance
where the dropped field changed the MEANING of the result.

### Original entry

The spec gives `GET /resource-calendar` a top-level `unavailable_resources` field
naming "every reason an answer is empty or partial" — a disconnected Google
calendar, a window beyond sync coverage, a resource the caller may not see.
`internal/output/formatter.go` decodes only `meta` and `data` and renders only
`data`, so in table (the default for reads) and csv it vanishes.

A response of `{"data":[],"unavailable_resources":{"7":["bookings_restricted_to_own"]}}`
therefore prints an empty list and exits 0. "This resource is free" and "we could
not see this resource's calendar" become the same output — on a command whose whole
purpose is answering when a resource is busy, and whose own doc warns an empty list
is a real answer.

`--format json` is unaffected: it passes the body through verbatim.

**Fix:** surface the field alongside the rows in table and csv, including when
`data` is empty. Part of the broader `meta`-is-dropped entry above; listed
separately because here the dropped field changes the meaning of the result rather
than annotating it.
**Effort:** human S / CC S. **Priority:** P2. **Depends on:** nothing.

## `segments create --dry-run --format table` prints "(no field changes)"

`SegmentMutationResult` puts the preview in `data.after`, not `data.diff.after`.
`ParseGenResponse` still recognises `would_apply: true` as a diff envelope, so
`RenderDiff` runs and reports no field changes while discarding the actual segment
preview — the condition tree the operator asked to inspect before committing.

**Fix:** normalise that response shape before the generic diff renderer, or give
segments its own renderer. `--format json` shows the preview correctly today, so
the workaround is real but undiscoverable.
**Effort:** human S / CC S. **Priority:** P2. **Depends on:** nothing.

## `audit show` rounds large numbers and drops entries containing huge ones

`AuditEntry.ForensicSummary` is `map[string]any`, so every nested number decodes as
`float64`. The `json` flag type preserves `9007199254740993` on the wire and in the
log file, but `audit show` re-emits it as `...992` — the precision the
`json.RawMessage` design exists to protect is lost on the way back out.

Worse: `1e400` is valid JSON and `parseJSONFlag` accepts it, but it overflows
`float64` on read, so `readJSONL` skips the **whole entry** — including the
mutation's identity and outcome. A rejected request logs the same way, so the
server never has to accept it.

**Fix:** decode with `json.Decoder.UseNumber()`, or keep `forensic_summary` as
`json.RawMessage` and only decode it when rendering.
**Why:** an audit trail that silently omits the entry for an unusual mutation is
worse than one that renders it awkwardly.
**Effort:** human S / CC S. **Priority:** P2. **Depends on:** nothing.

## `readJSONL` has no per-line cap

Rewriting it from `bufio.Scanner` to `bufio.Reader` fixed the real failure (one
over-long line made the ENTIRE log unreadable) but replaced a hard 4 MB ceiling
with none. `ReadBytes` grows and copies, so peak RSS is roughly 2.1× the longest
line — measured at 648 MB for one 300 MB entry. The old failure was an error
message; the new one is an OOM kill with no message.

Reachable through the same path: `--conditions @big.json` stores the file verbatim
in `forensic_summary`.

**Fix:** accumulate up to a cap (~8 MB) and, on overflow, discard to the next
newline without buffering — skipping that one entry while keeping the rest
readable, which is the property the rewrite was for.
**Effort:** human S / CC S. **Priority:** P2. **Depends on:** nothing.

## Locally-refused mutations still write an audit entry

`runMutation` appends after `def.Run` regardless of outcome, and `errorCode`
returns `""` for a plain `fmt.Errorf`, so a mutation refused entirely inside the
closure — before any request — writes an entry with `body_sha256: ""`, `status: 0`
and an empty `error_code`. That is indistinguishable from a write that WAS sent and
whose response failed to decode.

This sync enlarged the set: `availabilities bulk-update` moved `--from`/`--to`/
`--product-option-id` out of `MarkFlagRequired` into the closure, so what used to
fail at cobra flag-validation (no entry) now fails inside `Run` (entry). Same for
the `--to` must-be-after-`--from` check, the availability-ids caps, `segments get`'s
UUID parse, and the new `--status`/`--booking-status` exclusivity.

**Fix:** have pre-flight gates return a distinguishable error (an `*api.ExitError`
or a sentinel) and skip `Append` when `res == nil && WireBody == nil`.
**Effort:** human S / CC S. **Priority:** P2. **Depends on:** nothing.

## `--data` bypasses the bulk-update scope gate and is absent from the audit

**UPDATE (cli-v1 1.34.0 sync, 2026-10-14): the scope half of this is CLOSED.**
Both bulk endpoints now evict the opposing scope's keys from `--data` before
sending, and `resolveAvailabilityScope` refuses a `--data`-only scope with "one
scope is required". Pinned at both call sites by `TestWire_BulkDeleteScopes` and
`TestWire_BulkUpdateScopeEvictsTheOtherScopeFromData`. Only the forensic half
below is still open; see also the companions-via---data entry, which is the same
remaining cause for one field.

`availabilities bulk-update` decides scope from FLAGS only (`args.FlagSet(...)`),
while `JSONBodyFromArgs` seeds the body from `--data` first. So
`--data '{"availability_ids":[9999],"from":"2020-01-01"}'` passes a gate that
believes only one scope was given, and the assembled body can carry both.

More consequential: `forensicSummary` also reads only `args.Flags`, so a value
supplied through `--data` is absent from the audit entry. Every "a value absent
from this list cannot be reconstructed afterwards" claim in this file set is
defeated for any mutation whose caller used `--data`.

**Fix:** gate on the ASSEMBLED body rather than the flag set, and either record
`--data`-supplied keys in `forensic_summary` or state plainly that forensic
completeness holds only for flag-supplied values.
**Effort:** human M / CC S. **Priority:** P2. **Depends on:** nothing.

## Inventory `FlagDef` has `Min` but no `Max`

The statistics lane gained `ExtraFlag.Max` in this sync, for a real reason:
`--limit` advertised "(1-50)" and refused 0 while passing 100 through to a server
whose spec caps it at 50. The inventory lane did not get the symmetric field, so
`cli-v1.yaml`'s `maximum: 200` on the shared `Limit` and `maximum: 25` on
resource-calendar's are unenforced — `resource-calendar list --limit 500` goes out.

**Fix:** add `Max` beside `Min` and validate it in the same place. Also give
resource-calendar's `--limit` a description naming its 1-25 range, which the other
limits document.
**Effort:** human S / CC S. **Priority:** P3. **Depends on:** nothing.

## `stats` advertises flag defaults it no longer sends

`cmd/stats.go` declares `--limit` default 10, `--sort-by` default "bookings",
`--sort-direction` default "desc", but the collection loop now gates on
`cmd.Flags().Changed(...)` — so an unspecified flag is omitted from the query while
`--help` still prints `(default 10)`. Harmless only because the spec's own defaults
currently match; nothing pins the two together, so the help text starts lying the
first time upstream changes one.

**Fix:** either drop the cobra defaults (and let the server's apply) or send them,
and add a drift assertion tying the CLI's advertised default to the spec's.
**Effort:** human S / CC S. **Priority:** P3. **Depends on:** nothing.

## `stats` day-of-week and time-of-day values are unvalidated

`day_of_week`, `time_of_day_from` and `time_of_day_to` are declared with no `Enum`
and no format check, so `--day-of-week monday` (the spec wants `mon`) and
`--time-of-day-from 99:99` go straight out, and from>to is unchecked. They are the
only filter group with no local gate, which matters because the surrounding design
refuses an unsupported filter rather than letting the server quietly widen.

**Fix:** give `day_of_week` its spec enum and validate the two times as `HH:MM`
with from <= to, like `validateDateRange` does for the period.
**Effort:** human S / CC S. **Priority:** P2. **Depends on:** nothing.

## Wire tests run without most of the production transport chain

`fakeServer` builds its gen client with only the flat-error normalizer installed;
the host allow-list, bearer injection, idempotency minting and retry layers are
absent, because the allow-list rejects httptest's loopback addresses.

That is a real coverage boundary, and it hid a whole bug class until this sync:
generated decoding rejects a flat `{"error":"text"}` body and returns `(nil, err)`
before any of this package's error handling runs, so every wire test passed while
the user got `json: cannot unmarshal string into Go struct field
ErrorEnvelope.error`. The normalizer is installed in the harness now;
`idempotencyKeyRT` is still exercised only against its own stubs in
`internal/inventory/transport_test.go`, never end-to-end.

**Fix:** make the host allow-list accept a test-configured host so `fakeServer` can
install the whole chain, and assert chain MEMBERSHIP separately (see
`TestNew_InstallsFlatErrorNormalizer` for the shape — behaviour tests on a layer
cannot see the layer being unwired).
**Effort:** human S / CC S. **Priority:** P2. **Depends on:** nothing.

## The two doc-drift scanners still duplicate their scanning logic

`internal/docscan` now holds the quote-aware shell cutter and `SameSet`, which fixed
the real defect: `cmd/docs_drift_test.go` had reimplemented the cut with a naive
`strings.Index` and reintroduced a truncation bug the inventory scanner was hardened
against.

The SCANNERS themselves are still two implementations — `scanDoc`/`parseInvocation`
in `cmd/inventory/skills_drift_test.go` and `scanDocsForCeebee`/`parseDocInvocation`
in `cmd/docs_drift_test.go`. They differ in more than the cutter: how the `ceebee`
prefix is located, and which unprefixed tokens count as subcommands versus flag
values.

**Why not merged now:** reconciling those semantics means editing a working,
load-bearing test helper on both sides at once, and a mistake there weakens doc
validation silently rather than loudly. Sharing the cutter removed the correctness
divergence, which was the part that mattered.

**Fix:** move the fenced-block scanner into `internal/docscan` with one documented
semantics, and have both suites pass in the root command they resolve against.
**Raised by:** Optibot on #27 (non-blocking).
**Effort:** human S / CC S. **Priority:** P3. **Depends on:** nothing.

## A channel booking with an add-on over-reports its total, upstream

As of the cli-v1 1.34.0 sync, `Booking.total_amount` includes the booking's
purchased extras, which is what every other per-booking reader in the product
already did. On two import paths the extras are then counted TWICE.

`ChannelManager\GetYourGuide\ManageBookingsEndpoint` writes the add-on total
INTO `gross_price`, `retail_price`, `direct_price`, `net_price` and `voucher`,
and attaches the same add-ons to the `booking_extra` pivot with the identical
`fare` × `quantity`. `XolaBookingImport` does the same from CSV.
`BokunBookingImport` attaches no extras and is unaffected.

**Deliberately NOT worked around here.** This is a pre-existing, product-wide
defect, not something the CLI introduced: `retail_price` carries the same
doubled figure and roughly 40 call sites across 26 files add the extras on top
of it — the mobile payload, the automation webhooks, the OCTO channel resource,
the accounting and booking exports, the money-visibility badge, the payroll
payout jobs, the document/invoice variable resolver. The CLI is the latest
surface to agree with them. The root cause is one place (those columns should
not include an amount the pivot already carries) and fixing it there corrects
every surface at once; special-casing it per endpoint would leave the cause in
place and make this surface the only one computing on a different basis.

Tracked upstream as captainbook/captainbook#8616. Documented for operators in
`skills/bookings.md`. If a figure from a GYG or Xola booking looks high by
exactly its add-on total, that is this — escalate rather than patching the CLI.

**Fix:** nothing in this repo — re-vendor the spec once captainbook#8616 lands.
**Why:** a plausible-looking local "fix" here would fork the money basis from
the rest of the product, which is worse than the known over-report.
**Effort:** human S / CC S. **Priority:** P3. **Depends on:** upstream.


## ~~skills/availabilities.md uses the `po_` id spelling where the flag is an integer~~ — DONE

**Completed:** during the cli-v1 1.34.0 sync (2026-10-14).

All nine `--product-option-id po_88` examples now pass the bare number, and the
explanatory note was rewritten.

This entry was closed ONCE on false reasoning and had to be reopened, which is
the part worth remembering. The first version argued that `po_88` was correct on
bulk-update / bulk-delete because their flag is Go-typed `string` while
`availabilities list`'s is `int` — so only the two `list` examples were defects.
That confuses where the refusal comes from with what is valid. The wire contract
is `PositiveIntegerId` on all five endpoints (`{type: integer, minimum: 1}` or
`{type: string, pattern: "^[1-9][0-9]*$"}`, cli-v1.yaml ~6832), and the 1.22.0
changelog entry adds that a non-numeric `product_option_id` became a 422
VALIDATION_FAILED rather than a 404 on exactly those endpoints. `po_88` matches
neither branch, so every one of the nine examples was a documented 422; the
string-typed flag only means cobra does not catch it first.

Still open as the general lesson: `TestSkillsDocDrift` validates that a
documented flag EXISTS on the resolved command, never that the documented VALUE
would parse or be accepted. A value-level check — resolve each documented flag
to its spec field and reject a value the schema cannot accept — would have
caught this twice over.

## A path-id-only mutation writes an audit row that names no target

**One of three symptoms of a single cause.** `forensicSummary` can only see
`args.Flags` and `StaticForensic`, so nothing a closure DERIVES and nothing a
positional arg carries can reach `forensic_summary`. The other two are "A
companions value sent through --data is not recorded" and "The BULK_DELETE_NO_OP
outcome reaches stderr but not the audit row". A `RunResult.ForensicExtra` sink
closes all three; the `CommandDef.ForensicPathArgs` sketch below is an
ALTERNATIVE to that sink, not an addition. Do not build both.

`pricing-categories delete <id>` produces an audit entry from which the deleted
category cannot be identified: the CommandDef declares no `ForensicFields`, a
DELETE sends no body so `body_sha256` is empty, and the spec's 204 carries no
body so `tryParseDataID` leaves `response_id` empty. `forensicSummary()`
(`cmd/inventory/inventory.go`) reads only `args.Flags`, and `RunResult.ResourceID`
— which DOES hold the path id — is used for diff rendering and never copied into
`AuditEntry`.

Raised by the security specialist during the cli-v1 1.34.0 sync review and
deliberately deferred there: it is pre-existing, it affects EVERY mutation whose
only input is a path id (`delete`, `restore`, and the several `{id}/action`
routes) rather than just this one, and the fix is a schema or CommandDef change
that wants its own tests.

What made it worth recording now: spec 1.30.0 widened the blast radius of this
particular delete. It already cascaded to child PricingTiers; it now also
removes every "Must travel with" link FROM or TO the category, and a restore
brings none of them back. So the one operation whose audit row cannot say what
it hit is also the one that got harder to undo.

**Fix:** either add a `CommandDef.ForensicPathArgs` (or a StaticForensic-style
hook) so `forensicSummary` can record positional ids, or copy
`RunResult.ResourceID` into `AuditEntry` and bump the audit schema version.
Prefer whichever keeps `forensic_summary` the single place an incident responder
reads. Then cover it for one path-id-only mutation per shape.

**Why:** the audit log is the only record of a destructive call — it keeps
`body_sha256` and not the body — so a row that identifies no target cannot
answer the one question asked after an incident.
**Effort:** human S / CC S. **Priority:** P2. **Depends on:** nothing.

## The enum-description drift test cannot see a nested spec field

`TestSpecDrift_FlagDescriptionEnumsMatchSpec` resolves a flag to its spec field
through `specDoc.bodyField`, which is a FLAT top-level lookup over
`op.BodyInline` / `op.BodyRef`. Any flag whose spec field lives inside a nested
object is therefore never compared, silently.

The live instance is `availabilities bulk-update capacity --operator`. Its
description is the pipe-token run `set_to|increase_by|decrease_by`, and the spec
pins exactly that enum — but at `BulkUpdateAvailabilityRequest.new_value.operator`
(`api/inventory/cli-v1.yaml` around line 8533), one level down inside the
discriminated `new_value` object. Verified by mutation: adding a bogus
`multiply_by` token to that description leaves the whole suite green.

Found during the cli-v1 1.34.0 sync review. A reviewer initially attributed it
to the test iterating AST flags rather than live ones; that was a real and
separate hole and it is now fixed (the loop is live-driven, and comparisons went
38 -> 39), but it is NOT the cause here. The cause is the flat resolver, and the
mutation stays green after that fix.

**Fix:** teach `bodyField` to descend one level into inline object properties
(and to follow a `$ref` on a nested property), or give the few discriminated
payloads an explicit flag-to-path map. Prefer the former — a map is a third
statement of the mapping that can drift from the other two.

**Why:** the enum run in a flag description is a promise cobra prints to the
operator, and for the one bulk-update field that takes a closed set of
operators nothing checks it against the server's.
**Effort:** human S / CC S. **Priority:** P3. **Depends on:** nothing.

## A companions value sent through --data is not recorded in forensic_summary

`forensicSummary` reads only `args.Flags`, so `pricing-categories update <id>
--data '{"companions":[...]}'` writes an audit row with no `companions` entry,
while the same call written with `--companions` records the full value. The row
keeps only `body_sha256`, which is not reversible.

That matters more than the usual forensic gap because the field REPLACES the
whole "Must travel with" set: a call that wiped every link on a category is
indistinguishable, in the audit, from one that touched nothing about companions.
The cli-v1 1.34.0 sync promoted `--data` to a first-class, fully validated input
for this field (it is now gated exactly like the flag), which is what makes the
asymmetry worth closing.

**Fix:** after a successful `overlayValidatedCompanions`, write the validated
rows back into `args.Flags["companions"]` so both paths audit identically — or
add a per-run forensic sink on `RunResult` for values a closure derives rather
than reads from a flag. The second is the more general fix; several closures
assemble body fields that no `ForensicFields` entry can name.

**Why:** the audit log is the only durable record of a destructive call, and
this one is a silent-wipe shape.
**Effort:** human S / CC S. **Priority:** P2. **Depends on:** nothing.

## overlayJSONField round-trips sibling fields through float64

`overlayJSONField` unmarshals the whole assembled body into `map[string]any`,
sets one key and re-marshals. Every sibling field therefore survives a
decode/encode cycle, and Go decodes JSON numbers into float64 — so an integer
past 2^53 in a sibling is altered.

Reachable but not currently harmful: the fields that share a body with an
overlaid one are `min_age` / `max_age` (spec `integer, minimum: 0`, no maximum)
on pricing-categories, and the `availabilities update` fares overlay's
neighbours. No realistic age or capacity is near 2^53, so this is recorded as a
property rather than a live defect. It is the same root cause as the open
`--data` rounding entry, in a different code path.

**Fix:** splice the key into the already-marshalled object, or assemble the body
once as `map[string]json.RawMessage` so only the overlaid key is rewritten.
**Why:** the repo already fixed this class once for `--fares` and records it as
open for `--data`; this is the third site and the same one-line discipline.
**Effort:** human S / CC S. **Priority:** P3. **Depends on:** nothing.

## A 1000-id bulk-delete runs close to the client's fixed 60s timeout

A reviewer measured a 1000-id `availabilities bulk-delete` against a test tenant
at ~31-32s, with the inventory client's budget a fixed 60s and `retryRT` sitting
INSIDE that budget — so a network-error retry after 32s has under 28s left. Two
of three identical 1000-id attempts also returned `500 INTERNAL_ERROR` at
~31-32s while smaller batches returned the expected 422, which is consistent
with a server or gateway limit near 30s rather than with the id count.

Treat the numbers as indicative, not established: they came from a single
unplanned run against one tenant, not a controlled measurement.

**Fix:** decide a recommended batch size well under the 1000 cap (200-300 ids
still collapses 250 calls into one) and say so in `skills/availabilities.md`;
or make the 60s budget configurable for the two bulk endpoints. Either way add
the pitfall that a timed-out bulk-delete is NOT evidence nothing was deleted —
re-read with `availabilities list --include-trashed` before retrying, because a
blind retry on a partially-committed transaction is the harmful move.
**Why:** the one endpoint where a client timeout is ambiguous is the destructive
one, and the docs currently steer operators straight at the cap.
**Effort:** human S / CC S. **Priority:** P2. **Depends on:** upstream
confirmation of the ~30s server limit.


## The BULK_DELETE_NO_OP outcome reaches stderr but not the audit row

`availabilities bulk-delete` now warns on stderr when a committed delete removed
nothing. That outcome is computed from the response, after `runMutation` has
already assembled the `AuditEntry` from the CommandDef's static fields and
`args.Flags` — so it reaches the operator's terminal and never
`~/.ceebee/audit.jsonl`. A committed delete that removed 0 rows and one that
removed 4,000 produce byte-identical audit rows: same status, same empty
error_code, same forensic scope fields.

Raised by the red team during the cli-v1 1.34.0 sync, after the stderr half had
been implemented. The audit half needs a channel that does not exist yet: a
closure cannot contribute to `forensic_summary`, because the only inputs are
`StaticForensic` and flag values.

**Fix:** add a per-run forensic sink on `RunResult` (e.g. `ForensicExtra
map[string]any`) that `runMutation` merges into the entry, then have bulk-delete
record `total_deleted` and `status`. The same sink closes the companions-via---data
gap recorded above, so do them together.

**Why:** the audit log is the record of what a destructive call DID, and
"deleted nothing" is the single most useful thing it could say about a
bulk-delete that an operator is now querying after the fact.
**Effort:** human S / CC S. **Priority:** P2. **Depends on:** nothing.

## The forensic size cap does not cover the hand-built media audit entry

`capForensicValue` bounds one forensic value at 16 KB, but it runs inside
`forensicSummary`, and `products media upload` is the one mutation that
assembles its `AuditEntry` and forensic map by hand instead. So `--alt-text`
(an unbounded operator string, limited only by ARG_MAX — around 1 MB on macOS)
still lands verbatim in `audit.jsonl`.

Smaller than the 60 MB `@file.json` the cap was written for, but it sits on the
same rotation budget (50 MB, 3 files) the cap exists to protect. The real reason
it persists: the cap is applied at the CALLER rather than at the sink, so every
hand-built `AuditEntry` — present and future — opts out by default.

**Fix:** move the cap down to where every entry passes, rather than up where
each caller must remember it — capping inside `invpkg.FileLogger.Append` would
cover every present and future hand-built entry by construction. Short of that,
route media.go's map through `capForensicValue` on insert.
**Why:** a cap with one documented exception is a cap nobody can rely on.
**Effort:** human S / CC S. **Priority:** P3. **Depends on:** nothing.


## The range scope of a destructive bulk-delete has no blast-radius bound

`parseAvailabilityRange` bounds ORDERING (`to` strictly after `from`) but not
SPAN, so `--from 0001-01-01 --to 9999-12-31` passes the only local gate and one
synchronous call soft-deletes every departure of a product option across all
time. The sibling id scope on the same command is capped at 1000
(`availabilityIDsMaxItems`) and refuses anything larger, so the two scopes of one
destructive endpoint disagree about whether a blast radius needs a limit.

Raised by the security specialist in the third review pass of the cli-v1 1.34.0
sync, and deliberately NOT fixed there: adding a span limit is a new
client-side refusal, and the spec documents no maximum range, so it risks
exactly the CLI-only refusal the shared scope helper exists to avoid. That is a
judgement call for the repo owner rather than a review fix.

**Fix:** decide a maximum span (400 days covers the longest season anyone
schedules in one call) and refuse past it unless an explicit opt-out flag is
given, naming the computed day count in the refusal so a year typo is visible.
A year outside roughly 2000-2100 is worth refusing on its own. Confirm upstream
first that no legitimate caller needs an unbounded range.
**Why:** a one-character year typo is the cheapest way to delete a tenant's
whole calendar, and the confirmed-booking precheck is the only thing standing in
front of it.
**Effort:** human S / CC S. **Priority:** P2. **Depends on:** upstream
confirmation that a span cap is acceptable.

## `--format table` drops a dry run's non-diff fields

`tryParseDiffEnvelope` keeps an envelope whenever `would_apply` is true, and
`renderResult` RETURNS after the diff renderer in table mode — so for a dry run
whose payload carries sibling keys outside `diff`, those keys are never printed.
On `availabilities create-rule --dry-run --format table` that silently discards
`notes.other_departures_exist`, `rule.other_departures_sample`, `would_create`,
`total_matched` and `skip_reasons`: the collision warning that spec 1.31.0 added
and that `skills/availabilities.md` now tells operators to read before
approving. `bulk-delete --dry-run --format table` loses `status` and
`total_matched` the same way.

Mitigated, not harmless: mutations default to `--format=json`, so the fields do
print unless an operator explicitly asks for table. Pre-existing — TODOS already
records it for `segments create` — but this sync is what made it matter, by
documenting fields the table path drops.

**Fix:** render the diff and then fall through to `output.Format(res.Body)` so
sibling keys still print, rather than treating `would_apply: true` alone as
proof the payload is a pure diff envelope. Add a table-format assertion for
`notes` / `other_departures_sample` beside the existing every-format
BULK_DELETE_NO_OP test.
**Why:** the one surface that warns an operator they are about to create 1,137
duplicate departures is also the one a format flag can silence.
**Effort:** human S / CC S. **Priority:** P2. **Depends on:** nothing.

## companions `min_quantity` is stricter than the contract

`parseCompanionsValue` decodes `min_quantity` with `json.Unmarshal` into a Go
`int`, which refuses three spellings the server accepts: the digit string `"2"`,
and the integer-valued numbers `2.0` and `2e0`. Under OpenAPI 3.1 / JSON Schema
2020-12 the latter two satisfy `type: integer`, and Laravel's `integer` rule
stringifies before validating — which is the whole basis of the sibling
`PositiveIntegerId` note in the spec ("the operator assistant sends one
(`(string) $option->id`)"). So an assistant-generated payload that stringifies
both fields by the same mechanism is refused locally for a body the server would
take, which is the CLI-only-refusal class this repo treats as a real defect.

`category_id` already handles the loose spellings; only `min_quantity` does not.

**Fix:** decode `min_quantity` the way `category_id` is decoded — try a digit
string, then `json.Number` — accepting an integer-valued float and keeping the
1..999 bound. Pin `"2"` as accepted so the asymmetry cannot return.
**Why:** every other companions gate matches the spec exactly; this is the one
place the CLI is narrower than the server.
**Effort:** human S / CC S. **Priority:** P3. **Depends on:** nothing.

## The pre-gate counts a tag-triggered release lane as a pre-merge obligation

`gstack-pregate` derives its lanes from `.github/workflows/*.yml` and treats
every job that runs tests as a `requires-remote` obligation on a non-Linux
machine. That produces two obligations here, and only one of them can ever be
satisfied before merge:

- `ci.yml/test` — triggers on `pull_request`, so opening the PR runs it. Cleared
  with `gstack-pregate clear --remote ci.yml/test --run-url <url>`.
- `release.yml/test` — triggers ONLY on `push: tags: v[0-9]*`. This repo cuts
  releases by pushing a tag AFTER merge (there is no VERSION file; the Makefile
  derives the version from `git describe`), so the lane cannot run on a branch
  or a PR by construction.

Net effect: `gstack-ship-receipt write` always fails with
`PREGATE_REMOTE_UNCLEARED` on the release lane, so no ship receipt can be
written for this repo pre-merge. Neither `.gstack/pregate.json` nor
`gstack-pregate clear` has a way to declare a lane release-time-only — `warn`
explicitly cannot downgrade the lanes check's requires-remote verdict.

This is a tooling-fit gap rather than a defect in either tool: the pre-gate's
model is "every tests-bearing workflow job must have run", which is right for a
repo whose workflows all trigger on PR, and wrong for one with a separate
tag-triggered release pipeline.

**Fix:** upstream, a way to mark a workflow lane as release-time (e.g. a
`release_lanes` key in pregate-v1, or deriving it from the job's own triggers —
a job reachable only from `push: tags:` cannot be a pre-merge obligation).
Locally, nothing: do not clear it with a false run URL, and do not delete the
`test` job from `release.yml`, which exists so a tagged release cannot ship
untested.
**Why:** the receipt is the artifact a later agent reads to know the gate ran;
one that can never be written is worse than one that is absent, because its
absence looks like an oversight.
**Effort:** human S / CC S. **Priority:** P3. **Depends on:** upstream gstack.

## Upstream: the statistics spec added a whole endpoint without moving its version

`api/statistics/statistics-openapi.yml` reads `version: 1.4.0` both before and
after the Attendance endpoint landed. Upstream commit `836e7ea965`
("feat(statistics): report no-shows, and say when nobody was counting") added
`GET /api/v1/statistics/attendance`, the `Attendance` tag, `AttendanceData` and
`AttendanceSeriesItem` — 115 lines — and left `info.version` alone.

The inventory spec is the contrast and shows this is a discipline gap rather
than a convention: `cli-v1.yaml` moved 1.34.0 → 1.56.0 over the same period and
carries a per-release changelog in `info.description` precise enough to drive a
CLI sync on its own. The statistics spec has neither a changelog nor a moving
version, so **the version string cannot be used to detect that this spec
changed**, and a sync that diffs on versions alone would have skipped it
entirely.

Nothing broke here: `TestStatisticsDrift_EveryEndpointIsBound` reads the paths
rather than the version and reported the unbound metric immediately, which is
exactly the absence that direction of the pin exists to see. That is the
mitigation already in place, and it is why this is P3 rather than a defect —
but it means the statistics lane's only change signal is the drift tests, and a
response-shape change (which nothing pins on this side) would still be
invisible.

**Fix:** upstream — bump `info.version` on a statistics spec change, and
ideally give it the `info.description` changelog the inventory spec has. File on
`captainbook/captainbook`; do not patch the vendored copy, which must stay
byte-identical.
**Why:** a version that does not move is worse than no version, because a reader
reasonably concludes nothing changed.
**Effort:** human S / CC S. **Priority:** P3. **Depends on:** upstream.

## Two new nullable fields can be set but not cleared from a typed flag

The 1.56.0 sync added two fields the spec types as nullable, where `null` is a
meaningful value the flag cannot express:

- `bookings update --comments` / `--private-notes` — the spec says "Send null to
  clear", and a string flag cannot. `--comments=""` stores an empty string,
  which is a different value the server keeps.
- `pricing-categories update --max-per-resource` — "null removes the limit", and
  an int flag has no null. `0` is out of the spec's 1-99 range and is refused.

Both are reachable with `--data '{"comments": null}'` and both `--help` strings
say so, so this is a documented ergonomic gap rather than a capability one. It
is the same shape as the `intSlice` entry above: the CLI can set these fields
but never clear them without dropping to the raw body.

A general fix would be a nullable flag type (a `--clear-<field>` companion, or a
sentinel the parser maps to JSON null) rather than three per-field workarounds —
which is why this is recorded once instead of being patched twice.

**Why:** "set but never unset" is a silent half-surface; an agent reading
`--help` sees the field and not the asymmetry unless it reads to the end of the
description.
**Effort:** human M / CC M. **Priority:** P3. **Depends on:** nothing.

## Six error codes added in 1.47.0-1.49.0 fall back to RawAPIError

The sync typed two of the new codes — the three key-releasing booking 409s
(`KeyReleasingConflictError`) and `PLAN_LIMIT_REACHED`
(`PlanLimitReachedError`) — because each says something the server's own message
does not: what to do about the idempotency key, and that no change to the
request will help.

The rest reach the user through `RawAPIError`, which preserves the code and the
message verbatim: `DEPOSIT_EXCEEDS_TOTAL`, `PRODUCT_OPTION_CHANGE_NOT_SUPPORTED`,
`DESTINATION_ALREADY_STARTED`, `POOL_BACKED_ATTRIBUTE_NOT_EDITABLE`,
`BOOKING_HAS_NO_DEPARTURE` and `BOOKING_RESOURCE_REQUEST_INVALID`. That is a
deliberate choice, not an oversight — the registry has always been selective,
and a dozen older codes sit in the same fallback — but it is worth naming the
criterion so the next sync applies the same one: **a code earns a type when the
CLI can add a REMEDY the message cannot carry.** By that test,
`BOOKING_RESOURCE_REQUEST_INVALID` is the strongest remaining candidate, because
it releases its idempotency key for the same counter-intuitive reason the three
409s do and nothing in its message says so.

Note there is still no test pinning the registry against the spec's `Error.code`
catalogue in either direction, so a code the server starts emitting is invisible
here until someone hits it.

**Why:** the fallback is honest, so nothing is lost today; the gap is that
"which codes deserve a type" is decided per sync with no written rule.
**Effort:** human S / CC S. **Priority:** P3. **Depends on:** nothing.

## Review notes from the 1.56.0 sync that were not fixed in that PR

Six findings from the pre-landing review of the 1.56.0 spec sync landed below the
blocking floor and ship as notes rather than fixes. Recorded together because four
of them are one defect class — a bound or a gate that is only half-pinned.

**`--day-count -1` reaches the wire against a spec `minimum: 0`.** Pre-existing: `main`
declared no lower bound on the three `--day-count` flags either. The 1.56.0 sync briefly
added `Min: 1` there as a side effect of the `Max` rollout, which was worse — the spec
documents `0` as "an ordinary same-day session" — so it was removed rather than
corrected downward. The real obstacle is structural: `FlagDef.Min` treats `0` as
"unset", so a spec minimum of 0 cannot be expressed as a bound and no drift direction
can cover it. Fixing it means either a `MinSet bool` beside `Min`, or a pointer.

**`TestSpecDrift_FlagMinMatchesSpecMinimum` is one-directional.** It fails where the spec
STATES a minimum and merely logs where a flag declares a `Min` the spec does not state.
Its sibling `TestSpecDrift_FlagMaxMatchesSpecMaximum` gained that second direction in the
same commit, which is what catches an invented bound; the `Min` side still has the hole.
Restoring `Min: 1` on `timeValueFlags` is reported as "unverified" and passes.

**That same test guards coverage with `checked == 0` only.** The `Max` test gained
per-construction-pattern probes (a `$ref`'d parameter, an inline parameter, a body
property, a composed-schema body property) precisely because a single count is satisfied
by the query-parameter branch alone. The `Min` test still relies on the count, so if
`specBounds`' request-body walk stopped resolving, every body-field `Min` would become a
log line while the query-parameter minima kept `checked > 0`.

**`extras: null` and `customer_id: null` slip past `validateStoreBookingBody`.** An
explicit `null` on `extras` skips the closed-shape check, and a `null` `customer_id`
satisfies the "exactly one customer" rule and then 422s on the wire. The sibling
companions gate refuses `null` explicitly and documents why ("null is not in the
contract and must not be guessed at"); these two take the opposite position silently.
Reachable only through `--data`.

**`KeyReleasingConflictError.Error()` hardcodes "status 409"** while its constructor
discards the real status, and `ParseError` dispatches on the code with no status
assertion. A proxy or a future server change returning one of those codes under another
status would print a false status and assert a key-release guarantee the spec grants
only to the 409.

**Two refusal-ordering findings the owner skipped at review, recorded here so they are not lost.**
Both are live in `validateStoreBookingBody` and both were explicitly skipped, not overlooked.
(1) The deposit/currency, deposit/payment-method and payment-notes gates all run BEFORE the
`booking_type` switch that refuses those three fields outright on a reservation, so
`bookings create --booking-type reservation --deposit 2000` is told to add `--currency`, then
`--payment-method`, then `--expiry`, and only then that the payment fields were never legal on a
hold. FOUR independent readers raised this (a p1 specialist, the p1 and p2 adversarial passes,
and the p2 targeted reader); the owner skipped it because reordering a 300-line validator for
message ordering, at zero blocking findings, is the shape of change that has already produced
three fix-induced regressions on this branch. The fix is one move: hoist the switch above the
three payment gates. (2) The payment-notes gate tests only key PRESENCE of `deposit` while its
two siblings test a non-zero value, so `--deposit 0 --payment-notes "cheque"` passes every local
gate and reaches the wire; a zero deposit creates no transaction, so the note has nowhere to
land and is silently dropped rather than refused. Raised by the p2 targeted reader at
confidence 7. Fix: decode the deposit and require non-zero in that gate, matching its siblings.

**`workflows` and `workflow-executions` have no agent-facing cookbook.** Both are real command
groups, so their verbs, the three 1.55.0 trigger action types (`segment_entered`,
`segment_left`, `whatsapp_message_received`) and the 1.56.0 `whatsapp` channel for
`send_notification` are reachable only through `--help`. Reference coverage exists in README's
resource table; there is no how-to or explanation coverage, and `workflows delete` cascades to
steps, which makes this the most destructive family without a cookbook. Raised by both docs
audits on 2026-11-09. **This entry exists because `skills.md` and commit 57a31e9's message both
claimed the gap was already recorded here and it was not** — the second docs audit caught the
false cross-reference, removed it from `skills.md`, and could not fix the commit message, so the
honest repair is to make the claim true going forward rather than rewrite history.

**`PRODUCT_BEING_DUPLICATED` is untyped and nearly undocumented.** It has reference coverage in
README and `skills/index.md` only: `skills/products.md` documents `products duplicate` without
the couple-of-minutes window the copy job holds the source for, and `internal/inventory/errors.go`
does not type it, so the CLI surfaces it as a raw API error with no retry guidance — even though
the spec marks it "Transient and retriable", sets `error.retriable` true, and says the refusal
RELEASES the `Idempotency-Key`, which is exactly the guidance a typed error would carry. It fires on seven product and
product-option writes and NOT on `duplicate` itself: the spec's 1.7.0 list is `PATCH
/products/{id}`, `DELETE /products/{id}`, `POST /products/{id}/restore`, `POST
/product-options`, `PATCH /product-options/{id}`, `DELETE /product-options/{id}` and `POST
/product-options/{id}/restore`, while `POST /products/{id}/duplicate`'s own 409 is the plain
`IdempotencyConflict`. An earlier draft of this entry implied `duplicate` was one of the seven,
which would have sent a fixer to the one command that cannot raise the code.

**Upstream: the spec contradicts itself on whether every other `409` is final.**
`api/inventory/cli-v1.yaml:7980` asserts "Every other `409` describes something that will answer
the same way forever", while the same document's `PRODUCT_BEING_DUPLICATED` entry (≈8069) makes
that code transient, retriable and key-releasing, and `IDEMPOTENCY_IN_PROGRESS` is auto-retried
by this CLI's own transport. The docs here now follow the specific passages rather than the
blanket one. Per CLAUDE.md the vendored spec is never edited locally, so the fix is a ticket on
`captainbook/captainbook`. Found by the codex outside voice and confirmed by the second docs
audit, independently.

**`bookings create` writes customer name, email and phone verbatim to the local audit log.**
`ForensicFields` includes `customer`, whose value is the raw
`{"name","email","phone","zip_code",...}` object, and `forensicSummary`/`capForensicValue` apply
only a 16 KiB size cap with no redaction — so every booking taken through the CLI persists
plaintext PII to `~/.ceebee/audit.jsonl` (50 MB × 3 rotation). `bookings update` and
`reschedule` add free-text `comments`, `private-notes` and `reschedule-notes` to the same sink.
This follows existing precedent (`guests.go` already captures `passport`, `dob`, `email`,
`phone`), so it is a convention this PR WIDENS rather than a new policy break — but it widens it
to every create, and nothing in `FlagDef` can mark a field redact-on-audit. Raised by the
adversarial reader on 2026-11-09 as INVESTIGATE/Low; the owner's call at review was to record it
and decide later, with no behaviour change in this PR. Deciding it needs a redaction design
(hash, omit, or truncate) and probably spans more than the CLI.

**`specBounds` is blind to every `PositiveIntegerId` body property, and an earlier version of
this entry named the wrong cause.** `resolve()` in `cmd/inventory/spec_drift_test.go` DOES follow
`$ref`, up to 8 hops — the hole is that `PositiveIntegerId` is a `oneOf`, so its `minimum: 1`
sits in `oneOf[0]` where `prop["minimum"]` never reads it. Observable: `--replaced-resource-id`
is logged as "spec states no minimum here" and `--main-resource-id` never reaches the `missing`
arm, though both `$ref` it. That matters for this branch specifically, because it DELETED
`Min: 1` from `--main-resource-id`'s `FlagDef` and moved enforcement into the hand-written
`validateBookingResourcesBody`: if that gate were dropped, no drift direction would see it. Fix
is to flatten `oneOf`/`anyOf` in `specBounds` the way `allOf` already is. Found by the
adversarial reader, which also caught that the previous wording here ("a `$ref` this reader does
not follow") would have sent the next fixer to code that already works.

**Two self-inflicted notes from this branch's own review, left unfixed at the frozen cap.**
Fixing either would have destaled the only review record that bound CURRENT, and the cap left no
round to re-bind, so they ship disclosed instead. (1) Inserting two regression tests between
`TestWire_MainResourceBoundReadsTheAssembledBody`'s doc comment and its `func` orphaned that
comment: its `Value:` card now heads `TestWire_ExpiryNullIsNotAWindow` and the original function
has none. Documentation only. (2) The new null refusal reuses the phrase "cannot be cleared, only
replaced", which was the only text assertion the `0`-path test made, so that assertion no longer
tells which guard fired and a widened null check that swallowed `0` would stay green. Both are a
comment move and an assertion split respectively — small, and worth doing in one focused commit.

**`docFlagValues` cannot see a documented negative int value.** The extractor behind
`TestSkillsDocIntFlagValues` treats any token starting with `-` as the next flag, so
`--day-count -1` yields no entry: the value is neither parsed nor bound-checked and `checked`
does not increment, which the test's own floor guard cannot notice because the omission is
per-value. Negative is the one value class that gate cannot see, and it is exactly the class the
`FlagDef.Min` entry above records as reaching the wire today. Fix: accept `^-?[0-9]` as a value.
Latent rather than active — no cookbook currently documents a negative literal.

**`--format table` and `csv` drop fields the operator needs and erase null-vs-zero.** Two
findings with one shape, both reachable only by asking for a non-default format (mutations
default to `json`, reads to `table`). (1) `bookings create` and `reschedule` route through the
shared diff renderer, whose `DiffEnvelope` carries neither `payment_url` nor
`departure_time_unverified`, and `runMutation` early-returns on `res.DiffEnv != nil && Format ==
"table"` — so a reschedule's "the destination may already have departed" warning and a booking's
hosted-checkout link are not rendered. Raised by the codex outside voice, which called it
blocking; the owner's call at review was to ship it as a disclosed note, on the grounds that the
default format shows both fields, nothing is lost server-side and no test fails. (2)
`internal/output/formatter.go` renders a JSON `null` as an empty cell, so attendance's
`no_show_guests: null` is indistinguishable from `0` — the exact conflation the statistics spec
forbids ("zero and null mean different things here and only one of them is good news").
`formatTable` also reads only `data`, so a metric's `series` is dropped entirely, which is
pre-existing for every series metric.

**The new body gates exit 1 while the bounds added beside them exit 12.**
`validateStoreBookingBody`, `requireAnyBookingAmendment`, `validateBookingResourcesBody` and
`validateBookableProductBody` all return a bare `fmt.Errorf`, so `cmd/root.go` exits 1, while the
`FlagDef.Max` check added in the same change returns `api.ExitError{Code: api.ExitValidation}`
and exits 12. Same class of refusal, two exit codes, so an agent branching on exit status to
separate "my payload was wrong" from "something else broke" gets inconsistent answers. Confirms
the existing learning `ceebee-inventory-typed-errors-all-exit-1-not-the-taxonomy`: there is no
`invpkg`→exit-code mapper anywhere. Pre-existing for the one gate that existed before, now five.

**~~`KeyReleasingConflictError` names an action the operator cannot take.~~ FIXED** on
jbajou/cli-specs-update, 2026-11-09. Raised by the red-team reader, skipped once as "message
actionability", then raised AGAIN by the adversarial reader at High/Blocks:yes with the argument
that settled it: the message said to retry under the **same** idempotency key, the type carried
none, and the CLI mints a UUIDv7 per invocation and prints it nowhere, so the obvious retry of
re-running the command mints a fresh key and takes the second booking the message itself warns
about. The type now carries `Key`, `ParseGenResponse` fills it from the outgoing request's
`Idempotency-Key` header (the request is what the server saw, and where an explicit
`--idempotency-key` and the transport's minted one converge), and the message names both the key
and the flag. Pinned by `TestKeyReleasingConflictError_NamesTheKeyItTellsYouToReuse` and
`TestWire_KeyReleasing409ReportsTheKeyTheServerSaw`, both proven red against the unfixed code.
Remaining limit, disclosed: the wire test covers only an explicitly passed `--idempotency-key`,
because `fakeServer` omits the minting round-tripper; `transport_test.go` owns the minted path.

**No rule owns `{type: ["null"]}` spelled as a bare one-member sequence.** `rewriteNullOnlyType`
declines it via the `len == 2` guard, and rule 1's `isNullTypeSchema` cannot match it because it
requires `val.Kind == yaml.ScalarNode` — it owns only the scalar `{type: null}`. So that
construct is owned by neither rule and would fail `make codegen` loudly. Failing beats silently
erasing a union branch, so this is the safe direction and not urgent, but the guard's comment,
the README paragraph and an earlier draft of this entry all claimed rule 1 owned it, which is
wrong; all three now say so. The fix, if upstream ever emits that spelling, is to widen
`isNullTypeSchema` to accept a one-member sequence, or narrow the guard to "the only key is
`type` AND its value is a scalar". Found by the red-team reader on 2026-11-09, which proved it
by running `normalize()` over three fixtures: `{type: ["null"], description: …}` rewrites,
`{type: ["null"]}` alone does not, and `oneOf: [$ref, {type: ["null"]}]` survives verbatim.

**`rewriteNullOnlyType` can widen a union branch rule 1 declined.** Its `len == 2` guard
skips a bare `{type: ["null"]}` union member but not a two-key one, so where
`rewriteNullableUnion` declines a pathological parent (both `oneOf` and `anyOf`), a
branch spelled `{type: ["null"], description: …}` is still stripped to an untyped
`nullable: true` — widening the union instead of failing loudly, which is the one
outcome that package calls worse than not helping. No such construct exists in today's
spec, and `TestRewriteNullOnlyType_VendoredSpecCarriesNoneAfterNormalize` says so. The
tightening would be "skip any mapping that is a member of a `oneOf`/`anyOf` sequence".

**`FlagDef.Min` cannot express a lower bound of 0, so 41 int flags accept a negative.**
`Min` is a plain `int` whose zero value means "unbounded", so `Min: 0` and "no Min" are
one declaration, and `makeRunE` gates the check on `fd.Min != 0`. Every flag whose spec
`minimum` is 0 — `--capacity`, `--amount`, `--min-age`, `--from-price`,
`--max-credits-per-run` and 36 more — therefore passes `-5` straight to a server that
refuses it. `TestSpecDrift_FlagMinMatchesSpecMinimum` now names all 41 in a single log
line rather than leaving the limit invisible, which is how this was found. The fix is to
make the bound's presence separable from its value (`MinSet bool`, or `*int`) and widen
the `makeRunE` condition; it touches every `CommandDef` that declares an int flag, which
is why it is not folded into a review-response commit.

**Three benign `pii.phone.e164` pregate warns need an allowlist entry on `main`.**
`gstack-pregate` reports MEDIUM `pii.phone.e164` on three ten-digit integer bounds: one
in the `integralNumber` comment in `cmd/inventory/bookings.go`, and two in the wide-id
subtest in `cmd/inventory/wire_capture_156_test.go` — one of which is `math.MaxInt32`
spelled out, the value that subtest exists to probe. None is a phone number. The verdict
is `warn`, not `fail`, so nothing is blocked, and the bounds are deliberately not
respelled: obscuring `math.MaxInt32` to satisfy a phone-number regex would cost more
legibility than the warn costs noise. Allowlists only count from `origin/main` and
`--policy-from head` may only ADD test lanes, so the entry (by id + path) has to land in
the policy on `main`. (This entry names no digits on purpose: an earlier draft quoted
them and raised the count from three to five.)

**`PlanLimitReachedError.UserMessage` prints a double period on its no-details arm.**
The fallback arm is `"PLAN_LIMIT_REACHED: %s. This is an account ENTITLEMENT…"`, and the
server's `message` normally ends in a period of its own, so a 403 carrying no `details`
renders `Plan allowance exhausted.. This is an account ENTITLEMENT…`. Observed by /qa on
jbajou/cli-specs-update against a loopback stub (capture 006); the partial- and
full-details arms interpolate before a parenthetical instead and read correctly
(captures 005, 017). Pre-existing, not introduced by the Optibot-response commits.
Cosmetic, severity Low, deferred by the Standard fix tier. The fix is to trim a trailing
`.` from `msg` before interpolating, in that arm and any sibling with the same shape.

**Why:** each is a gate or bound that is pinned in one direction only, which is the
shape that let a real regression through during that very sync.
**Effort:** human S / CC S each, except the `FlagDef.Min` change (human M / CC M — wide
but mechanical). **Priority:** P3. **Depends on:** nothing.
