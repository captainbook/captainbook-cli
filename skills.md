# `ceebee` Agent Skill Guide

This single-file guide has been replaced by a per-resource cookbook layout.

**Start here:** [`skills/index.md`](skills/index.md)

The new layout splits the agent-facing documentation into:

- `skills/index.md` — global tour: setup, idempotency, dry-run, audit log, exit codes, error codes, capability table.
- `skills/<resource>.md` — per-resource cookbooks with worked examples and resource-specific pitfalls. One per inventory resource family (auth, products, product-options, availabilities, pricing-tiers, discounts, gift-certificates, bookings, transactions, customers, guests, extras, questions, categories, media, notifications).

This pointer file remains so existing tooling that references `skills.md` keeps working. New work should consult `skills/index.md` first.
