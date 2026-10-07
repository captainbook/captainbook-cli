# `ceebee inventory segments` — customer segments

A segment is a saved selection of customers. A **smart** segment is a condition tree evaluated server-side; a **static** one is a fixed list. Creating a smart segment is the only write here.

## Endpoints

| Command | Method + path | Ability | Dry-run |
|---------|---------------|---------|---------|
| `inventory segments list` | GET /segments | `cli:read` | n/a |
| `inventory segments get <id>` | GET /segments/{id} | `cli:read` | n/a |
| `inventory segments members <id>` | GET /segments/{id}/members | `cli:read` | n/a |
| `inventory segments fields` | GET /segment-fields | `cli:read` | n/a |
| `inventory segments create` | POST /segments | `cli:write` | body |

## Read `fields` first

`segments fields` is the vocabulary endpoint: it answers *what can this tenant filter a segment on*. The answer is **per-tenant and per-business-unit**, so there is no fixed list to copy out of these docs, and guessing a field name produces a 422 rather than an empty segment.

```bash
ceebee inventory segments fields --format table
```

## Worked examples

```bash
# What segments exist, and which are still backfilling?
ceebee inventory segments list --format table
ceebee inventory segments list --status backfilling

# Smart segments only, recently touched
ceebee inventory segments list --type smart --since 2026-10-01T00:00:00Z

# Who is actually in it right now
ceebee inventory segments members 6ba7b810-9dad-11d1-80b4-00c04fd430c8 --limit 50 --format table

# Create one. --conditions is JSON, or @file.json for anything non-trivial.
ceebee inventory segments create \
  --name "High spenders" \
  --description "Lifetime spend over 1000" \
  --conditions '{"filters":[{"field":"total_spend","operator":"gte","value":1000}]}' \
  --dry-run

# Same thing from a file, which is what you want once the tree has any depth
ceebee inventory segments create --name "High spenders" --conditions @conditions.json
```

## `--conditions` is required, and an empty filter list is refused

At least one filter is mandatory. An empty filter list matches **nobody**, and the server refuses it at the edge rather than creating a segment that is permanently empty — the evaluator and the preview both read an empty tree as no-match, deliberately, so that opening the builder does not show an operator their entire customer base.

`--conditions` accepts a literal JSON document or `@file.json`. The value is validated for well-formedness before any request is sent and then forwarded **byte for byte**, so large ids keep their precision.

## Pitfalls

- **Segment ids are UUIDs**, not the opaque prefixed ids most resources use. `segments get bk_1` fails locally with a UUID parse error rather than as a server 404.
- **`status` is not something you set here.** `draft`, `active`, `backfilling`, `archived` and `errored` are lifecycle states the server owns; `--status` on `list` filters by them.
- **`members` is a snapshot**, not a definition. A smart segment's membership moves as customers change; two calls minutes apart can legitimately differ.
- **The audit trail records the condition tree.** `conditions` is listed in this command's forensic fields, so a created segment can be reconstructed later. That only works because it is a flag — a tree passed through `--data` would survive only as a body hash.
