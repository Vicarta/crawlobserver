# Quick 13 Verification

## Before

Production ClickHouse 25.5: `event_time` is DateTime; existing query_log TTL
was `event_date + toIntervalDay(3)`. At 2026-10-07 07:45:19 UTC:
2,573,384 rows, 1,443,935 older than 24 hours; active 392,116,540 bytes,
inactive 14,112,236 bytes. No running/queued crawls. Unrelated pages mutations
were observed but not changed; no query contents or secrets were read.

## Focused Validation

- Builder gpt-6-luna max: structured XML comparison to HEAD proves only
  query_log/ttl changes; diff check passed.
- DateTime boundary SELECT on synthetic rows in production: age 23 hours
  expired=false, age 24 and 25 hours expired=true. No table fixture writes.
- Fresh Code critic gpt-6.1-sol high: PASS for final candidate and bounded
  deploy script (two no-force gates, in-place mounted policy copy, no restart).
- Fresh Tests critic gpt-6.1-sol high: PASS. Structured REXML comparison
  verified all other nodes unchanged. Executed exact README bash block with
  fake commands: preflight statuses 2/3 suppress docker; status 0 invokes exact
  ALTER with expected arguments. No real Docker or SSH in tests.
- Fresh UX critic gpt-6.1-sol high: PASS, no UI change; documentation states
  rolling expiry versus asynchronous physical cleanup accurately.
- First review found an unguarded README command sequence; final candidate
  fixes it with an explicit if guard. All final reviews refer to this version.
- `bash -n` temporary deployment script and `git diff --check`: passed.
- No Go/frontend source, dependencies or UI changed; broad app tests/build
  not relevant. Production table-DDL readback is the end-to-end release gate.

## Contract

Apply event_time + INTERVAL 24 HOUR DELETE to existing system.query_log and
persist identical startup policy. Default asynchronous TTL materialization
recalculates/applies existing rows without forced OPTIMIZE or service restart.
Other system logs retain three days; application_logs retains five days.
Expired history is deliberately deleted and cannot be recovered by restoring
the old TTL. Physical files, including inactive parts, disappear asynchronously.
Actual release result is recorded in 13-SUMMARY.md.
