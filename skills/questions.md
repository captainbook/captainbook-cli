# Questions

A `Question` is a checkout-time question presented to the customer for a `ProductOption` — "What's your shoe size?", "Any dietary restrictions?". Answers attach to bookings via the `Answer` model and are readable via [answers.md](answers.md). Soft-deletable; delete cascades to answers.

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory questions list` | GET /questions | `cli:read` | n/a |
| `inventory questions get <id>` | GET /questions/{id} | `cli:read` | n/a |
| `inventory questions create` | POST /questions | `cli:write` | body |
| `inventory questions update <id>` | PATCH /questions/{id} | `cli:write` | body |
| `inventory questions delete <id>` | DELETE /questions/{id} | `cli:write` | none |
| `inventory questions restore <id>` | POST /questions/{id}/restore | `cli:write` | body |

## Worked examples

### 1. List required questions for one option

```bash
ceebee inventory questions list --product-option-id po_88 --required=true
```

Returns `{id, product_option_id, label, type, required, options, granularity, deleted_at}`.

`granularity` is `booking`, `guest`, or `extra` — whether the question is asked once per booking, once per guest, or once per purchased extra. It determines how many `Answer` rows a booking carries for this question, and which row those answers hang off (see [answers.md](answers.md)).

**Read-only, and creates are pinned to `booking`.** Neither request schema accepts the field, and the create controller writes `booking` unconditionally — so anything you create through the CLI is booking-granularity. Guest- and extra-granularity questions come from the operator UI and the importers; they read back correctly here but cannot be created or changed through this API.

### 2. Add a "Shoe size" question

```bash
ceebee inventory questions create \
  --product-option-id po_88 \
  --label "Shoe size (EU)" \
  --type number \
  --required=true \
  --dry-run
```

Drop `--dry-run` to commit.

### 3. Make an existing question optional

```bash
ceebee inventory questions update q_42 --required=false
```

Default `--format json` returns the diff envelope.

### 4. Soft-delete a deprecated question

```bash
ceebee inventory questions delete q_42         # 204; cascade-deletes answers
ceebee inventory questions list --include-trashed --product-option-id po_88
ceebee inventory questions restore q_42        # 200; cascade-restores answers
```

### 5. There is no `--since` on questions

The flag does not exist here, and the endpoint refuses the parameter with **422**. The `questions` table carries no `created_at` / `updated_at` columns at all, so there is nothing for the filter to bound — both fields are always null on the response.

The 422 is deliberate rather than a silently-unfiltered page, which is what a nightly poller would misread as "nothing changed since last run". There is no incremental-sync signal for questions: list in full per product option and diff client-side.

```bash
ceebee inventory questions list --product-option-id po_88
```

Four other resources are in the same position — `guests`, `pricing-categories`, `pricing-tiers`, `categories`. Everywhere else `--since` still means a real lower bound on `updated_at`.

## Pitfalls

- ⚠️ **Cascade on delete:** `Question::$cascadeDeletes = ['answers']`. Soft-deleting a Question soft-deletes every `Answer` (per-booking response) tied to it. Historical answers vanish from default reads — `answers list --include-trashed` brings them back, restoring the question restores them. This is how a manifest for a past departure comes back short with nothing to say so.
- ⚠️ **No server-side dry-run on delete.** CLI rejects `--dry-run` at parse time. Gauge the impact with `answers list --question-id <id> --limit 1` and read the envelope's pagination rather than materialising every row: those rows are guest PII (passport numbers, DOBs, medical notes) and the endpoint needs `view_answers_of_booking` on top of `cli:read` — see [answers.md](answers.md).
- ⚠️ **Every question you create here is booking-granularity, whatever you intended.** `QuestionController::store()` hardcodes `granularity: booking`; neither create nor update accepts the field. Worse, a `granularity` key in the body is **dropped by validation — not honoured and not rejected**, so you get a 201 and a row that silently disagrees with what you sent. Guest- and extra-granularity questions do exist (created in the operator UI and by the importers) and read back verbatim, but no API path produces or edits one. Deleting and recreating is not a workaround.
- ⚠️ **There is no `position` field.** Questions have no ordering column on this surface — neither the schema nor the write requests carry one. Order is whatever the server returns.
- ⚠️ **No `--since`, and sending `since=` is a 422.** No timestamp columns on the table; `created_at` / `updated_at` are always null. Don't build a sync loop on this resource — see example 5.
- ⚠️ **`label` is Spatie-translatable**, but the API returns English only (matches the global translation contract). Multi-language editing is not in V1.
- ⚠️ **`required: true` only enforces at checkout.** Existing past bookings without an answer are not retroactively flagged — required-ness is a forward-looking constraint.

## See also

- [product-options.md](product-options.md) — Questions hang off `ProductOption`.
- [extras.md](extras.md) — sibling catalog (also presented at checkout).
- [answers.md](answers.md) — reading the answers to these questions across bookings (manifests, rollups).
- [bookings.md](bookings.md) — answers populate at booking creation; `bookings get` inlines them.
