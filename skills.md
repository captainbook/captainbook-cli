# `ceebee` Agent Skill Guide

This single-file guide has been replaced by a per-resource cookbook layout.

**Start here:** [`skills/index.md`](skills/index.md)

The new layout splits the agent-facing documentation into:

- `skills/index.md` — global tour: setup, idempotency, dry-run, audit log, exit codes, error codes, capability table.
- `skills/<resource>.md` — per-resource cookbooks with worked examples and resource-specific pitfalls. One per inventory resource family (auth, products, product-options, availabilities, pricing-categories, pricing-tiers, resources, resource-calendar, locations, discounts, gift-certificates, bookings, transactions, customers, guests, extras, questions, answers, categories, media, notifications, partners, segments), plus `skills/statistics.md` for the whole `ceebee stats` namespace. Two families have **no** cookbook yet — `workflows` and `workflow-executions`. Their verbs are listed in README's resource table, but they have no worked examples or pitfalls here, and five spec changes are documented only in `--help`: the three 1.55.0 trigger action types, the 1.56.0 `whatsapp` channel, 1.57.0's depth-first step deletion, and 1.60.0's `step_type`/`action_type` pairing plus the stored-type-no-longer-exists read-back. Their refusals do reach `skills/index.md`'s error-code table (`INVALID_STEP_CONFIG`). That is a known gap, not an omission from this list.

This pointer file remains so existing tooling that references `skills.md` keeps working. New work should consult `skills/index.md` first.
