# Questions

A `Question` is a checkout-time question presented to the customer for a `ProductOption` — "What's your shoe size?", "Any dietary restrictions?". Answers attach to bookings via the `Answer` model. Soft-deletable; delete cascades to answers.

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory questions list` | GET /questions | `cli:read` | n/a |
| `inventory questions show <id>` | GET /questions/{id} | `cli:read` | n/a |
| `inventory questions create` | POST /questions | `cli:write` | body |
| `inventory questions update <id>` | PATCH /questions/{id} | `cli:write` | body |
| `inventory questions delete <id>` | DELETE /questions/{id} | `cli:write` | none |
| `inventory questions restore <id>` | POST /questions/{id}/restore | `cli:write` | body |

## Worked examples

### 1. List required questions for one option

```bash
ceebee inventory questions list --product-option-id po_88 --required true
```

Returns `{id, product_option_id, label, type, required, position}`.

### 2. Add a "Shoe size" question

```bash
ceebee inventory questions create \
  --product-option-id po_88 \
  --label "Shoe size (EU)" \
  --type number \
  --required true \
  --position 2 \
  --dry-run
```

Drop `--dry-run` to commit.

### 3. Make an existing question optional

```bash
ceebee inventory questions update q_42 --required false
```

Default `--format json` returns the diff envelope.

### 4. Soft-delete a deprecated question

```bash
ceebee inventory questions delete q_42         # 204; cascade-deletes answers
ceebee inventory questions list --include-trashed --product-option-id po_88
ceebee inventory questions restore q_42        # 200; cascade-restores answers
```

### 5. List questions updated since last sync

```bash
ceebee inventory questions list --since "2026-04-01T00:00:00Z"
```

## Pitfalls

- ⚠️ **Cascade on delete:** `Question::$cascadeDeletes = ['answers']`. Soft-deleting a Question soft-deletes every `Answer` (per-booking response) tied to it. Historical answers vanish from default reads — `--include-trashed` brings them back, restoring the question restores them.
- ⚠️ **No server-side dry-run on delete.** CLI rejects `--dry-run` at parse time. To gauge impact, count answers server-side via your admin UI; CLI does not expose "find answers for question X".
- ⚠️ **`label` is Spatie-translatable**, but the API returns English only (matches the global translation contract). Multi-language editing is not in V1.
- ⚠️ **`required: true` only enforces at checkout.** Existing past bookings without an answer are not retroactively flagged — required-ness is a forward-looking constraint.

## See also

- [product-options.md](product-options.md) — Questions hang off `ProductOption`.
- [extras.md](extras.md) — sibling catalog (also presented at checkout).
- [bookings.md](bookings.md) — answers populate at booking creation.
