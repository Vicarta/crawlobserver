# Quick Task 9: Proven Delta Origins and Operational Email Alerts

## Goal

Show the response-proven primary origin plus related proven hosts for a
multi-subdomain Daily Delta, and notify verified active administrators about
crawl execution failures and newly observed page errors.

## Scope

- Keep the original seed as audit provenance; do not guess an origin from
  majority, planned URLs alone, or unrelated hosts.
- Persist per-session/event/admin delivery receipts in SQLite, use bounded
  idempotent retries, and expose status/history in the existing admin Logs UI.
- Compare errors against each URL's latest prior durable project observation;
  an unseen URL is new, while a URL omitted by a rotating Delta is not resolved.
- Update `ProductFeatures.md` with origin, recipient, comparator and delivery
  semantics.

## Acceptance

- Related proven Delta hosts are visible behind an accessible disclosure;
  unavailable, partial, conflicting, and unrelated evidence remains fail-closed.
- Only active verified admins receive mail; admin access is global across
  projects, while viewers and API keys are not recipients.
- Receipts survive restart, duplicate replays are suppressed, retries stop
  within one hour, and accepted means provider acceptance rather than inbox
  delivery.
- Focused backend/frontend tests and the existing frontend lint/build pass.

## State

Implementation is locally ready for independent review. Not committed,
deployed, or production-validated.
