# Quick 14: Automatic System Log Budget

## Authorized Scope

Owner approved periodic automatic cleanup toward 150 MB for ClickHouse system
logs only, accepting temporary overshoot. No crawl/GSC/project data or
application_logs cleanup, filesystem quota, storage migration or logging disable.
Keep query_log max-age 24h and other TTLs unchanged. Budget means 150,000,000
bytes of retained active log parts; inactive files are observed separately and
removed by native GC, not an excuse to evict additional live history.

Expected files: stdlib budget script and fixture tests, two systemd units,
existing log-retention installer, deploy README, ProductFeatures and GSD records.
Estimate 350-450 lines. No app source/framework/dependency changes.

Production read-only validation required additional necessary race fixtures and
reuse of verified ages for unchanged immutable parts: a broad repeated age
scan could never converge while live logs kept producing new parts. This does
not widen the authorized feature; fresh active metadata still gates each DDL.

## Sequence And Acceptance

1. Inventory actual system.parts and timestamp availability read-only. Delegate
   sole builder; fixtures only. The confirmed system.parts min_time is epoch
   zero, so event ages must come from min(event_time) by _part, not file mtime.
2. Every five minutes, explicit allowlisted system log names/rotated suffixes
   only; no shell SQL interpolation/secrets. Oldest-first part eviction via
   ClickHouse DROP PART, bounded work, query timeout, repeated fresh inventory.
   Under/equal budget and inactive-only excess do not mutate. Dry-run cannot
   mutate. Fail closed on invalid inventory and genuine provider errors;
   concurrent part merges recover through bounded re-inventory.
3. Fixture tests cover safety boundary, ordering, dry-run, failure/race cases;
   compile/shell syntax/diff validation, then fresh Code/Tests/UX review.
4. Commit/push owned files. No-force production preflight; install only script,
   installer, units and documentation. First production dry-run, then approved
   cleanup; enable timer. No app/ClickHouse restart or live crawl/rescan.
5. Verify retained active system logs <= budget, separately report pending
   inactive physical bytes. Record unchanged business table size/rows, health,
   SELECT 1, container identities, enabled timer and job result. Push closeout.

## Limitations

This is a periodic budget, not strict physical quota. Part-level eviction may
remove an entire compacted log block, so history can be shorter than 24h and
post-cleanup size can undershoot 150 MB. Native grace/read references and growth
between runs can delay physical convergence. Do not change old_parts_lifetime,
force OPTIMIZE, delete files manually or widen to arbitrary system tables.
Old log history deleted under this policy cannot be restored by code rollback.
