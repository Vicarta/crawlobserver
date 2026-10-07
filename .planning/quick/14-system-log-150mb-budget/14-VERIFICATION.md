# Quick 14 Verification

## Implementation Gates

- 27 fake-client unit tests pass; no fixture invokes Docker, SSH or a real DB.
- Python compile and installer shell syntax pass; git diff --check passes.
- Fresh final Code, Tests and CLI/UX critics: PASS (Sol, high).
- Builder: Luna, max. Runtime and tests are the builder's sole owned edits.
- Actual ClickHouse 25.5 authenticated Docker/stdin dry-run passed without DDL:
  retained_bytes=305524511, inactive_bytes=210939199, budget_bytes=150000000,
  planned_drop_parts=10, projected_retained_bytes=53136528.
- Live read-only validation exposed a TIMEOUT_EXCEEDED under the earlier 15s
  limit and continuous age-snapshot churn. Final limits are SQL 20s, command
  25s, job 110s and systemd 120s. Verified immutable-part ages are reused only
  for the same table/name/bytes, and every DROP requires fresh over-budget
  inventory plus complete current age evidence. Merge/TTL/size-change fixtures
  cover the discovered races; no production deletion occurred before review.

## Release Contract

No-force preflight; approved system logs only. Install source, script and units,
dry-run again, apply bounded cleanup, enable timer. Verify active log bytes at
or below 150000000 separately from pending native physical GC. Compare business
table row counts, SELECT 1, API health and unchanged app/ClickHouse container IDs.
No app or ClickHouse restart, live crawl/rescan, TTL change or filesystem part
deletion. Release acceptance is recorded in 14-SUMMARY.md after deployment.
