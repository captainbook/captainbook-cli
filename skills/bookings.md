# Bookings

A `Booking` is a customer reservation against a `ProductOption` on a specific date. The CLI exposes reads, the booking lifecycle (`create`, `update`, `reschedule`), resource assignment, the money-moving actions `cancel`, `refund`, and `comp`, and a confirmation resend.

Five of these need `cli:cs` rather than `cli:write`: `cancel`, `refund`, `comp`, `reschedule` and `resend-confirmation`. `cancel` is CS for every `--refund-policy` value, because the whole V1 enum (`none|full|partial`) is policy overrides and the server 403s operator tokens on overrides; `reschedule` is CS because it moves a live booking a customer is already travelling on, which is a judgement about a person's trip rather than a catalogue edit.

**Taking a booking arrived in spec 1.47.0.** Before it, an agent could read every booking on the account, reassign its guide, cancel it, comp it and refund it — but could not create one, so "book Mrs Papadopoulou onto Friday's 10:00" ended in a hand-off to a screen. Three operations close that, and they split deliberately: `create` takes the booking and **charges nothing**, `update` amends only notes and operator metadata, and `reschedule` moves a booking **in place**, keeping its id.

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory bookings list` | GET /bookings | `cli:read` | n/a |
| `inventory bookings get <id>` | GET /bookings/{id} | `cli:read` | n/a |
| `inventory bookings create` | POST /bookings | `cli:write` | body |
| `inventory bookings update <id>` | PATCH /bookings/{id} | `cli:write` | body |
| `inventory bookings reschedule <id>` | POST /bookings/{id}/reschedule | `cli:cs` | body |
| `inventory bookings transactions <id>` | GET /bookings/{id}/transactions | `cli:read` | n/a |
| `inventory bookings available-resources <id>` | GET /bookings/{id}/resources/available | `cli:read` | n/a |
| `inventory bookings available-equipment-resources <id>` | GET /bookings/{id}/resources/equipment/available | `cli:read` | n/a |
| `inventory bookings available-auxiliary-resources <id>` | GET /bookings/{id}/resources/auxiliary/available | `cli:read` | n/a |
| `inventory bookings set-resources <id>` | POST /bookings/{id}/resources | `cli:write` | body |
| `inventory bookings cancel <id>` | POST /bookings/{id}/cancel | `cli:cs` | body |
| `inventory bookings refund <id>` | POST /bookings/{id}/refund | `cli:cs` | body |
| `inventory bookings comp <id>` | POST /bookings/{id}/comp | `cli:cs` | body |
| `inventory bookings resend-confirmation <id>` | POST /bookings/{id}/notifications/resend-confirmation | `cli:cs` | body |

`resend-confirmation` is the same operation as `inventory notifications resend` — one route, two spellings. See [notifications.md](notifications.md).

## Filters on `bookings list`

cli-v1 1.29.0 widened this list considerably. The ones worth knowing:

| Flag | Notes |
|---|---|
| `--date-field` | Which date `--from`/`--to` bound: `starts_at` (default, the trip), `confirmed_at`, or `created_at`. This is the single most common source of "why is this booking missing" — a trip in March confirmed in January is outside a January `starts_at` window. |
| `--status` | Case-insensitive alias of `--booking-status`. Both exist; pick one. |
| `--origin-type` | `ota`, `direct_online`, `direct_offline`, `indirect_offline`, `reseller`, `other`. The surface that sold the booking. |
| `--partner-id` | Matches **either** side of the partner relationship. Ids come from `inventory partners list`. |
| `--customer-id` | The booker, as a UUID. From `customers list`. A malformed value fails locally rather than as a 404. |
| `--include-trashed` | Soft-deleted bookings. Distinct from `--include-cancelled`, which lifts the cancelling scope. |
| `--since` | ISO 8601 lower bound on `updated_at`, for incremental sync. |
| `--include resources` | Enriches each row with assigned resources plus a `resource_state_token` suitable for stale-state guarded writes. Any other value is a 422 — a misspelling used to return 200 with no resources and nothing saying why. |

```bash
# Bookings CONFIRMED in January, whatever month they depart
ceebee inventory bookings list --from 2026-01-01 --to 2026-01-31 --date-field confirmed_at

# One partner's OTA bookings
ceebee inventory bookings list --partner-id 7 --origin-type ota --format table

# Everything that changed since the last sync, including soft-deleted rows
ceebee inventory bookings list --since 2026-10-01T00:00:00Z --include-trashed

# Rows with their assigned resources and a write token
ceebee inventory bookings list --include resources --format json
```

## Worked examples

### 0a. Take a booking (`create`)

Intent: "book Mrs Papadopoulou and her two children onto Friday's 10:00 sailing, she paid €200 cash at the desk."

```bash
# 1. Find the departure and the pricing categories whose ids the guest mix is keyed by.
ceebee inventory availabilities list --product-option-id 88 --from 2026-05-08 --to 2026-05-09
ceebee inventory pricing-categories list --product-id 7

# 2. Preview under a key you mint yourself, so step 3 can reuse it.
#    (Omit --idempotency-key and the CLI mints one per invocation, which means
#     the preview and the commit would NOT share a key.)
KEY=$(uuidgen)

ceebee inventory bookings create \
  --idempotency-key "$KEY" \
  --availability-id 4821 \
  --booking-type booking \
  --guests '{"30":1,"31":2}' \
  --is-refundable=true \
  --customer '{"name":"Maria Papadopoulou","country":"GR","email":"maria@example.com"}' \
  --payment-method cash \
  --deposit 20000 --currency EUR \
  --dry-run

# 3. Commit under the SAME key. A dry run writes no idempotency row, so the
#    key is still free for exactly one real call.
ceebee inventory bookings create --idempotency-key "$KEY" ...   # same flags, minus --dry-run
```

Six things that are easy to get wrong:

- **It charges nothing.** `--payment-method` records how the customer *is paying*; `--deposit` is money **already received**, in the account currency's minor units (`20000` = €200.00). `--payment-method card` is refused on every account — this endpoint has no card flow, so take card payments in the dashboard.
- **A non-zero `--deposit` requires `--currency`**, and the server *validates* it against the account currency rather than converting. The CLI refuses the pair locally if you omit it.
- **`--guests` is an OBJECT keyed by pricing category id**, not an array: `{"30":1,"31":2}`. Both shapes are well-formed JSON and only one is a guest mix, so the CLI refuses the array form before the wire. The head count is the sum and is not sent separately.
- **Name the customer exactly once**: `--customer-id` (a UUID — `customers.id` is the one id here that is not an integer) **or** `--customer`. Both, or neither, is a 422.
- **Fares come from the product's pricing policy.** `prices` and `price` are refused, not dropped, so a negotiated price cannot be reported back as applied. Enter one in the dashboard.
- **Read `guest_mix_warnings` on the response.** A product may require one guest type to travel with another (a child with an adult). Staff may book past that rule, as on the dashboard — which warns on screen. This surface warns in that field instead, and `[]` means the mix satisfies every rule. Report a non-empty one.

A hold rather than a booking swaps which fields are legal:

```bash
ceebee inventory bookings create \
  --availability-id 4821 --booking-type reservation --expiry 1800 \
  --guests '{"30":2}' --is-refundable=true --customer-id "$CUSTOMER_UUID"
```

`--expiry` is in **SECONDS** (`1800` is half an hour; the configured default is 600). It is **required** on a reservation and refused on a booking, and a reservation refuses `--deposit`, `--payment-method` and `--payment-notes` — a hold records no payment. The CLI refuses each of those combinations locally.

Three of this endpoint's refusals **release their idempotency key**, because each describes a passing state: `CAPACITY_UNAVAILABLE`, `DEPARTURE_NOT_BOOKABLE` and `NO_RESOURCE_AVAILABLE`. Retry the identical call under the **same** key rather than minting a new one — a fresh key on a retry is how an approval that was granted during a busy minute becomes un-approvable after a cancellation frees the seat.

### 0b. Amend a booking's notes (`update`)

Intent: add an internal note and set a custom attribute, without telling the customer anything.

```bash
ceebee inventory bookings update bk_42 \
  --private-notes "guide asked for the shallow route" \
  --custom-attributes '{"dietary":"vegetarian"}' \
  --dry-run
```

- **At least one of `--comments`, `--private-notes` or `--custom-attributes` must be present.** A request carrying none is a 422 rather than a 200 with an empty diff, because a 200 reads as "applied" — and `--dry-run` alone is not a change. The CLI names the three fields locally so the empty call does not cost a round trip.
- **No notification is sent**, matching the dashboard's edit form. To re-send the confirmation use `bookings resend-confirmation`.
- **`--guests` and `--extras` are deferred, not refused on principle.** Both have to recompute the booking's stored total and that calculation is not yet shared out of the dashboard. Change them there.
- **Custom attribute slugs are per BUSINESS UNIT.** A slug from another unit is unknown here rather than mistyped, and the 422 lists the ones configured for *this booking's* unit. A definition backed by a code pool is refused outright: those change only through the atomic claim/release action, because the uniqueness constraint protects the pool table rather than the booking's copy.
- **Clearing a note needs a JSON null**, which a string flag cannot express: `--data '{"comments": null}'`. `--comments=""` stores an empty string, which is a different value.

### 0c. Move a booking to another departure (`reschedule`, CS only)

Intent: the customer cannot make Friday; move them to Saturday's 10:00 on the same product option.

```bash
# The destination must be a departure of the SAME product option.
ceebee inventory availabilities list --product-option-id 88 --from 2026-05-09 --to 2026-05-10

ceebee inventory bookings reschedule bk_42 \
  --availability-id 5190 \
  --reschedule-notes "customer rang; moved to Saturday at their request" \
  --dry-run
```

- **The booking keeps its id.** Extras, answers, transactions, guests and any pool access code stay on the same row, because nothing is replaced.
- **The price does not change.** The booking keeps the fare it was taken at even when the destination is priced differently — moving a midweek booking onto a peak Saturday does not reprice it. Say so when the destination is priced differently; `prices` is refused.
- **An option change is not available here.** On the dashboard that operation cancels the booking and creates a replacement with a new id, so a destination on another product option is refused with `PRODUCT_OPTION_CHANGE_NOT_SUPPORTED`.
- **The note lands on the reschedule LEDGER, not on the booking's `comments`.** `--reschedule-notes` is the note; `--comments` is the fallback used when `--reschedule-notes` is *omitted*; and `--reschedule-notes=""` records a deliberately blank note instead of falling back. Whichever is recorded reads back in `diff.after.reschedules[]`, which is where to confirm it landed.
- **Read the ledger root-first.** `bookings get <id>` carries `reschedules[]` and `origin_booking_id` for the booking's whole *chain*, oldest first, keyed on the booking a move landed on — so asking a mid-chain booking for its own entries returns almost nothing. A booking the dashboard moved by cancel-and-replace used to read here as an ordinary cancellation, which answered "what happened to booking X" with "it was cancelled" while the customer was travelling on a different id.

### 1. List confirmed bookings starting in May

```bash
ceebee inventory bookings list \
  --booking-status CONFIRMED \
  --from 2026-05-01 --to 2026-05-31 \
  --product-option-id 88
```

Status enum: `ON_HOLD`, `CONFIRMED`, `EXPIRED`, `CANCELLED`. Date filters apply to `Booking.starts_at` in tenant timezone.

### 2. Show one booking with inlined guests + recent transactions

```bash
ceebee inventory bookings get bk_42 --format json
```

Response inlines `data.guests[]`, the most-recent `data.transactions[]`, and `data.answers[]` (the customer's responses to the operator's custom booking questions). Use `bookings transactions bk_42` for the full ledger, and `answers list` for cross-booking manifest reads. It also carries `data.resources[]` (the assigned boat / guide / kit) and `data.resource_state_token`, which you need for `set-resources`.

`answers` is on `get` only — deliberately **absent** from `bookings list`, because answers routinely carry passport numbers, dates of birth and dietary/medical notes, and a paginated list carrying them would be a bulk PII export. Ordering is stable across repeated reads (`question_id`, then `answerable_type` / `answerable_id` / `id`).

Distinguish the two empty cases, because they mean opposite things:

| `data.answers` | Meaning |
|----------------|---------|
| `[]` | You may see answers; this booking has none. |
| `null` | You may **not** see answers — the token's user lacks `view_answers_of_booking`. The server does not even read them from the database. |

### 2b. What your permissions withhold

Field-level redaction on `Booking` is silent: **the keys are always present, only the values are withheld.** A script that checks for key presence will conclude the data doesn't exist rather than that it can't see it.

| Missing permission | What goes null/empty |
|--------------------|----------------------|
| `view_booker_of_booking` | `customer.name` / `.email` / `.phone` and the inline `guests[]`. `customer.id` survives, so the payload stays joinable. |
| `see_money_of_booking` | `total_amount`, `paid_amount`, `refunded_amount` (null) and `transactions[]` (empty). Also `unit_amount` / `total_amount` on each `extras[]` row — but the array itself **survives**, carrying `extra_id`, `name` and `quantity`. What the customer bought and how many is an operational fact a guide preparing a departure needs; it is not an amount. |
| `view_answers_of_booking` | `answers` (null — see the table above). |

The dedicated child endpoints agree with those gates rather than letting you walk around them: `transactions list` also requires `see_money_of_booking`, and `guests list` / `answers list` also require `view_booker_of_booking` / `view_answers_of_booking`. A guest or answer outside your business unit or assigned trips reads as **404**, not 403, so the id space is not enumerable.

Ability and permission are two different gates. `cli:read` decides which *endpoints* you can reach; the permissions of the user the token was issued to decide which *data* comes back. A caller holding `view_own_booking` (declared `exclusive_of` `view_any_booking`) sees only the bookings their linked resource is assigned to — list endpoints return a smaller page rather than a 403, so a short page is not evidence of a small calendar.

### 2c. What the booking cost: `total_amount` and `extras[]`

`total_amount` is the booking's **whole** total: the pricing tiers plus the sum of the purchased add-ons. Until spec 1.34.0 it was the tier total alone, which published a figure **below** the booking's own `paid_amount` on any booking carrying add-ons — reading as an overpayment to anyone reconciling, and as a discount to an agent asked what the trip cost. If you have a cached figure from an older run, it is low by exactly the extras total.

`get <id>` inlines the breakdown as `extras[]`, one row per add-on actually bought: `{extra_id, name, quantity, unit_amount, total_amount, deleted_at}`.

```bash
ceebee inventory bookings get bk_42 --format json \
| jq '{total: .data.total_amount, paid: .data.paid_amount,
       extras: [.data.extras[] | {name, quantity, unit_amount, total_amount}]}'
```

Four things to know before you reconcile anything against it:

- **`extras[]` is detail-only.** It is on `get <id>` and deliberately absent from `bookings list` and from the `before` / `after` snapshots on a `cancel` / `comp` receipt. `total_amount` carries the money on all of those surfaces, so nothing is left unreconcilable — but a list-driven script cannot see the breakdown.
- **`unit_amount` is the price AS CHARGED, not the catalogue price.** It is the `booking_extra` fare, which diverges from `extras get <id>`'s `amount` the moment an operator reprices the add-on. A booking must not restate its history at today's prices.
- **It carries no currency of its own, and the writer decides which one it is in.** A channel booking stores the channel's figure (in `Booking.currency`); a direct booking stores the catalogue fare in the *account* currency, unconverted. Treat the number as unlabelled: comparable with the other amounts on the **same** booking, never converted, summed across bookings, or compared against a currency resolved elsewhere. Read `transactions` if you need a labelled amount.
- **A retired extra still appears**, with `deleted_at` set — retiring an add-on does not unsell it, and dropping the row would quietly take its money out of the total. This is **not** behind `--include-trashed`: a booking's total must not depend on a query parameter. To resolve the catalogue row, `extras get <id> --include-trashed`.

Two agreements it does **not** make, both deliberate. The base column here is `gross_price`, while most other per-booking readers base on `retail_price` (which adds the application fee for a tenant without `marketplace_fee_included`), so the figures still differ for a marketplace booking. And the aggregate statistics surface sums `gross_price` with **no extras at all** — a revenue figure read from `ceebee stats` will not reconcile against the sum of these totals. That is a basis difference, not a bug to chase.

### 3. Find every trip a given resource is on

Intent: "which trips has the spare wetsuit kit been assigned to this month?"

```bash
KIT_ID=$(ceebee inventory resources list --category auxiliary --format json \
  | jq -r '.data[] | select(.name == "Spare Wetsuit Kit") | .id')

ceebee inventory bookings list \
  --resource-id "$KIT_ID" \
  --from 2026-08-01 --to 2026-08-31
```

`--resource-id` filters through the `booking_resource` pivot and matches **active resources only** — a soft-deleted resource returns zero rows rather than an error, so an empty result here is ambiguous between "never assigned" and "resource was deleted". Check `resources get <id>` if the empty answer is surprising.

Add `--include resources` to any `bookings list` call to inline each row's assigned resources plus its `resource_state_token`. Omit it when you just want the light payload.

### 4. Reassign the boat on a booking (read → decide → guarded write)

Intent: the Oceanis is in for repairs; move tomorrow's charter onto the Sun Odyssey.

```bash
# 1. Read current state AND the guard token.
TOKEN=$(ceebee inventory bookings get bk_42 --format json | jq -r '.data.resource_state_token')

# 2. Ask the server which main resources are actually assignable.
ceebee inventory bookings available-resources bk_42

# 3. Preview, then commit.
ceebee inventory bookings set-resources bk_42 \
  --main-resource-id 91 \
  --expected-resource-state-token "$TOKEN" \
  --dry-run

ceebee inventory bookings set-resources bk_42 \
  --main-resource-id 91 \
  --expected-resource-state-token "$TOKEN"
```

A booking's resources come in **three** kinds. The split is the product option's pivot capacity, not the resource's `category`: a non-auxiliary resource *with* a capacity competes for the single main slot, one *without* is equipment.

| kind | what it is | how many | flag |
| --- | --- | --- | --- |
| main | the guide or the asset (boat, vehicle, room) | exactly one | `--main-resource-id` |
| equipment | the kit that goes out with every departure | many | `--equipment-resource-ids` |
| auxiliary | everything else attached as auxiliary | many | `--auxiliary-resource-ids` |

`set-resources` is a **desired-state** write, not a delta:

- `--main-resource-id` **assigns** the single main resource: it replaces the current one if there is one, and makes the first assignment if the booking has none.
- `--equipment-resource-ids` and `--auxiliary-resource-ids` each **replace their whole set**. Pass `--auxiliary-resource-ids=1,2` to end up with exactly those two; pass `--auxiliary-resource-ids=` (empty) to clear every auxiliary.
- Omitting a flag leaves that kind untouched.

Only `--main-resource-id` may name a capacity-bearing resource. Naming one under either plural flag is how you'd ask for a *second* guide or asset, and it's refused (`EQUIPMENT_RESOURCE_IS_MAIN` / `AUXILIARY_RESOURCE_IS_MAIN`).

#### Bookings spread over several units (spec 1.49.0)

A product configured for several units — five riders over three jet skis — holds **more than one** main resource, and each assigned row carries its own `seats`. `--main-resource-id` replaces **one** unit there, so it needs `--replaced-resource-id` beside it to say which:

```bash
ceebee inventory bookings set-resources bk_42 \
  --main-resource-id 91 --replaced-resource-id 88 \
  --expected-resource-state-token "$TOKEN"
```

- Without it the write is refused as `MAIN_RESOURCE_REPLACEMENT_AMBIGUOUS`. On a single-unit booking the flag is optional and a no-op.
- The replacement carries **that unit's** riders, not the whole party, and nothing else on the booking moves: the other units, the equipment and their sources stay exactly as they were. There is no wholesale re-attach on this path.
- `--replaced-resource-id` without `--main-resource-id` describes no write at all; the CLI refuses it locally rather than letting the server answer `REPLACED_RESOURCE_WITHOUT_MAIN` from inside `rejections[]`.
- **A `resource_state_token` read before 1.49.0 is refused once as stale** on any booking whose rows carry seats — the token now folds `seats` in, so a change of mix between the same units is no longer invisible to it. Re-read the booking and retry.

Candidates come from `available-resources` (main), `available-equipment-resources` (equipment) and `available-auxiliary-resources` (auxiliary). The main and auxiliary lists apply the same availability and concurrency checks as the back-office switcher; the equipment list applies none, because a null pivot capacity means the resource isn't rationed per booking — every candidate is always assignable.

### 5. Handling the two 409s from `set-resources`

`BOOKING_RESOURCE_STATE_STALE` — someone changed the booking's resources between your read and your write. The token you sent is no longer current. **Re-read, re-decide, re-send.** Never blind-retry the same body: the state you based the decision on is gone, and the reason the server rejected you is precisely that it can't tell whether your intent still holds.

`BOOKING_RESOURCE_CONFLICT` (409) — your view was current, but the selection itself is illegal. Re-reading won't help. Re-run the `available-*` lists and pick from what comes back.

`BOOKING_RESOURCE_REQUEST_INVALID` (422, spec 1.49.0) — raised when *every* rejection is one of the two request-shape ones, `MAIN_RESOURCE_REPLACEMENT_AMBIGUOUS` and `REPLACED_RESOURCE_WITHOUT_MAIN`. It carries the same `error.details.rejections[]`. **Nothing was applied**, so the idempotency key is released: correct the body and retry under the same key.

The server doesn't short-circuit: it plans the whole write and reports **every** refused id at once in `error.details.rejections[]`, so one round trip tells you everything to fix. Each entry's `code` is `<FIELD>_RESOURCE_<PROBLEM>`, where `<FIELD>` is the flag you put the id in (`MAIN` / `EQUIPMENT` / `AUXILIARY`) — not what the resource turned out to be. So a wrong-field mistake names both halves:

| code | meaning |
| --- | --- |
| `<FIELD>_RESOURCE_NOT_ON_PRODUCT_OPTION` | the booking's product option doesn't carry that resource at all — attach it to the option first |
| `<FIELD>_RESOURCE_IS_MAIN` | it's capacity-bearing. A booking holds one; use `--main-resource-id` |
| `<FIELD>_RESOURCE_IS_EQUIPMENT` | it has no pivot capacity. Use `--equipment-resource-ids` |
| `<FIELD>_RESOURCE_IS_AUXILIARY` | it's auxiliary. Use `--auxiliary-resource-ids` |
| `MAIN_RESOURCE_NOT_AVAILABLE` | it *is* a main resource on the option, but can't take this booking — overlapping departure, party too large, or inside a cool-off window |
| `AUXILIARY_RESOURCE_NOT_AVAILABLE` | it *is* auxiliary on the option, but is on an overlapping departure or can't host this party size |
| `MAIN_RESOURCE_REPLACEMENT_AMBIGUOUS` | the booking is spread over several units; name the one `--main-resource-id` replaces with `--replaced-resource-id` |
| `REPLACED_RESOURCE_NOT_HELD` | `--replaced-resource-id` is not one of this booking's units |
| `REPLACED_RESOURCE_WITHOUT_MAIN` | `--replaced-resource-id` was sent without a `--main-resource-id` (the CLI refuses this locally) |
| `MAIN_RESOURCE_ALREADY_HELD` | `--main-resource-id` names another unit this booking already holds; name one it does not |

There is no `EQUIPMENT_RESOURCE_NOT_AVAILABLE`: equipment is never rationed per booking, so it's either on the option or it isn't.

`MAIN_RESOURCE_NOT_AVAILABLE` can also land on a request whose dry run was clean — a concurrent booking can take the resource in between, and `--expected-resource-state-token` can't see that (it covers *this* booking's assignments, and none of them moved). Re-read the candidates and retry.

### 6. Cancel applying the product's policy (CS only)

Intent: a customer cancels a booking; refund what the product's cancellation policy says they are owed.

There is no `auto` in V1. `Product.cancellation_policy` is free-text human prose and nothing derives a refund amount from it, so `--refund-policy auto` is rejected with `422 POLICY_AUTO_NOT_READY`. Read the policy yourself, decide the amount, and state it explicitly:

```bash
# 1. Read the policy text you are applying.
ceebee inventory products get prd_7 --format json | jq -r '.data.cancellation_policy'

# 2. State the decision the policy implies.
ceebee inventory bookings cancel bk_42 \
  --reason "customer cancellation request; 14 days out, policy allows full refund" \
  --refund-policy full \
  --notify-customer=true \
  --dry-run
```

Dry-run returns `data.refund_amount` and `data.policy_applied` without contacting Stripe or the mailer. Drop `--dry-run` to commit.

Because you are the policy engine here, put the reasoning in `--reason` — that string is what the audit log has to explain the money movement with.

### 7. Cancel with policy override

Intent: comp a full refund despite the no-refund policy.

```bash
ceebee inventory bookings cancel bk_42 \
  --reason "weather event — owner approved full refund" \
  --refund-policy full \
  --notify-customer=true
```

`partial` additionally requires `--refund-amount <minor-units>`. Nothing changes ability-wise between this recipe and recipe 6: `none`, `full` and `partial` are all overrides, so all three need `cli:cs`.

### 8. Refund a partial amount (CS only)

Intent: refund €50 of a €150 booking.

```bash
ceebee inventory bookings refund bk_42 \
  --amount 5000 \
  --reason "discount applied retroactively" \
  --notify-customer=false \
  --dry-run
```

`5000` = €50.00. `--notify-customer` defaults `false` for refund — operators debugging refunds should not silently email customers. Set `true` to dispatch the refund-receipt notification. Drop `--dry-run` to commit; Stripe is called for real.

### 9. Comp a booking (zero-out, no Stripe)

Intent: write off a booking with no money movement.

```bash
ceebee inventory bookings comp bk_42 \
  --reason "owner-comped tour" \
  --notify-customer=false
```

A `Transaction` of type `comp` is recorded; no Stripe call. `--notify-customer` defaults `false`.

## Pitfalls

- ⚠️ **`cancel`, `refund`, and `comp` all touch live external systems** (Stripe, mailer, SMS). Always `--dry-run` first. The `forensic_summary` in `~/.ceebee/audit.jsonl` captures the request + response for these — useful for post-incident review.
- ⚠️ **`cancel`, `refund` and `comp` all require `cli:cs`.** Operator tokens (`cli:write` only) get `ABILITY_MISSING` on all three, refused locally before any network call. For `cancel` this holds for every `--refund-policy` value including `none`: the spec classes all of `none|full|partial` as CS-attributed overrides of the product's policy, and `refund_policy` is required, so V1 has no operator-reachable cancel. The relaxed path older docs promised (`cli:write` when the policy is applied automatically) belonged to a `refund_policy=auto` that is not in the V1 enum. The spec's ability table now names cancel in its `cli:cs` row explicitly (fixed upstream in captainbook/captainbook#8113); older copies of the spec omitted it, and for those the routes are authoritative.
- ⚠️ **`refund` defaults `notify_customer` to `false`**, opposite of `cancel` which defaults to `true`. Different ergonomics for different ops: cancellation customers expect an email; refund-debugging engineers don't want to spam them.
- ⚠️ **Date-time vs date filters.** `bookings list --from 2026-05-01` matches bookings whose **start date** is May 1 or later (date, tenant TZ). `transactions list --from "2026-05-01T00:00:00Z"` is a UTC date-time on `Transaction.created_at`. Don't mix.
- ⚠️ **`set-resources` replaces, it doesn't append.** `--auxiliary-resource-ids=7` on a booking that already has kits 3 and 5 leaves it with *only* kit 7. Read the current set first and send the full intended list.
- ⚠️ **Moving the main resource silently restores the full equipment set.** Switching the main detaches and re-attaches the option's *whole* equipment set, so a booking you'd previously narrowed to a subset comes back with all of it. Send `--equipment-resource-ids` alongside `--main-resource-id` to keep a narrowed set. The dry run reports this faithfully — read it.
- ⚠️ **An unrecognised field is now a 422, not a silent drop.** Every field on this request is optional, so a body carrying only a misspelt one used to validate, plan nothing, and return `200` with `before` equal to `after` — indistinguishable from a write that worked.
- ⚠️ **The state token isn't optional and isn't reusable.** `--expected-resource-state-token` is required, and it's invalidated by any resource change on that booking — including your own successful write. Re-read before each subsequent `set-resources` on the same booking; the response's `data.resource_state_token` is the new one.
- ⚠️ **Don't retry through a `BOOKING_RESOURCE_STATE_STALE`.** It means the world moved, not that the request was malformed. Retrying the identical body is how you clobber someone else's change the moment the guard happens to line up. Re-read and re-decide.
- ⚠️ **`--resource-id` on list matches active resources only.** A soft-deleted resource yields an empty page, not a 404 — don't read that as "this resource was never used".
- ⚠️ **A GetYourGuide or Xola booking with an add-on over-reports its total.** Those import paths write the add-on total INTO `gross_price` *and* attach the same add-ons to the pivot, so the extras are counted twice. This is a known product-wide defect (`retail_price` carries the same doubled figure, and ~40 call sites across the product add the extras on top of it), tracked as captainbook#8616 — the CLI agrees with every other surface rather than special-casing it. Do not "fix" such a figure locally; escalate. Bokun imports attach no extras and are unaffected.
- ⚠️ **Redaction is silent, and `null` does not mean "empty".** `customer.name`, `total_amount`, `answers` and friends come back as `null` (keys present) when the token's *user* lacks the matching permission — see example 2b. `answers: []` means "none exist"; `answers: null` means "you may not see them". Treating the two the same ships a manifest that is quietly missing every dietary requirement.
- ⚠️ **`starts_at` / `ends_at` on a booking are in the PRODUCT's timezone, not UTC.** They are read from the linked availability, whose `from` / `to` columns store product-local wall clock: `2026-08-04T10:00:00+01:00` is a 10:00 local departure for a `Europe/London` product. The booking's own timestamps (`confirmed_at`, `cancelled_at`, `created_at`, …) are real UTC. Don't normalise the whole object to UTC with one rule.
- ⚠️ **`create` charges nothing, whatever `--payment-method` says.** It records how the customer *is paying*; `--deposit` is money already received. Nothing is taken, no card is charged, and `--payment-method card` is refused on every account. An agent that reports "payment taken" off a 201 is reporting something that did not happen.
- ⚠️ **`--deposit` and every money field on `create` are MINOR units.** `2000` is €20.00 for a EUR account and 2000 for a JPY one. A non-zero deposit must also state `--currency`, which the server validates rather than converts.
- ⚠️ **`update` sends no notification, `reschedule` does.** Amending notes is silent by design, matching the dashboard's edit form; a reschedule dispatches `BookingRescheduled` after the transaction commits unless you pass `--notify-customer=false`. Don't assume the quieter one.
- ⚠️ **A reschedule does not reprice.** The booking keeps the fare it was taken at, even onto a more expensive departure. Say so when the destination is priced differently rather than letting an operator assume the difference was collected.
- ⚠️ **Three of `create`'s 409s release their idempotency key** — `CAPACITY_UNAVAILABLE`, `DEPARTURE_NOT_BOOKABLE`, `NO_RESOURCE_AVAILABLE` — because each describes a passing state. Retry the identical call under the **same** key. Minting a new one on retry is how a card approved during a busy minute stays un-approvable after a cancellation frees the seat.
- ⚠️ **`reschedule` needs `cli:cs`, unlike `create` and `update`.** An operator token gets `ABILITY_MISSING` locally. It is grouped with cancel and refund on purpose: it moves a trip a customer is already on.
- ⚠️ **`--include resources` is opt-in on list but implicit on get.** `bookings get` always carries `resources[]` + `resource_state_token`; `bookings list` only does with `--include resources`. Scripts that grab the token from a list call will silently read `null` without it.

## See also

- [resources.md](resources.md) — creating resources and attaching them to product options (the pool `set-resources` picks from).
- [transactions.md](transactions.md) — full transaction ledger per booking.
- [guests.md](guests.md) — per-booking guests, edited separately.
- [answers.md](answers.md) — cross-booking reads of the same `answers[]` this endpoint inlines (manifests, dietary rollups).
- [discounts.md](discounts.md) — `discounts apply` attaches to a booking; refund is a separate step here.
- [notifications.md](notifications.md) — resend the booking confirmation email/SMS.
