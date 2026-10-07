# Quick 14: Released System Log Budget

## Delivered

Owner-approved periodic 150,000,000-byte retained active system-log budget.
Explicit supported system-log tables and numeric rotated copies only; no crawl,
GSC, project or application_logs deletion. Oldest verified min(event_time) part
is dropped through ClickHouse SQL with fresh metadata proof. Immutable ages
are reused only for unchanged table/part/bytes. Ten bounded discovery attempts,
20s query, 25s command, 110s job and 120s systemd limits remain fail closed.
The five-minute timer retries transient failures. TTL policy is unchanged.

Implementation: 75dc870a874b8f3d99163172a4a8ac7031e1b62b.
Necessary live-churn retry correction: c0f799ada633a31590ff277ec19c1ce21c753286.
Both pushed to origin/codex/cleanup-deployed-worktree.

## Validation

28 fake-client tests, Python compile, installer shell syntax and diff checks
pass. Final fresh Code, Tests and CLI/UX reviewers all PASS (Sol/high); sole
builder Luna/max. Real ClickHouse 25.5 Docker/stdin dry-run and approved DROP
PART execution verified. The initial three-attempt job safely made partial
progress then stopped under live churn; the correction and immutable age reuse
allowed the final bounded apply to remove six parts and finish at 56,318,195
active bytes. No candidate was deleted without complete fresh age/size proof.

## Production Acceptance: 2026-10-07

- Multiple FORCE=0 CHECK_ONLY gates passed; no active crawl stopped.
- Timer installed, enabled and active. Scheduled 14:29:23 -> 14:34:23 EEST.
- Installed systemd job finished successfully at 14:29:24 EEST:
  retained_bytes=57348257, inactive_bytes=227857627, budget_bytes=150000000,
  dropped_parts=0 (idempotent under-budget no-op); Result=success, exit 0.
- All business-table active row counts and byte sizes, including application_logs,
  exactly match immediately before/after the final rollout. pages=12011,
  links=2409020, gsc_analytics=20406976; no business mutation was invoked.
- Loopback API health status=ok; ClickHouse healthy and SELECT 1=1.
- No app/ClickHouse restart: app ID remains
  14fe91c24c1ae7c7ff25c8f60c6e9f2d9a6609a6abfc9d3b8f964f76a7845317;
  ClickHouse ID remains
  685ab71b0245ebfbcb339fa3ab6dd5fe404aec72e4d7602056ecbf4eb0cf5078.
- The shell's final combined inspect template expected an app Docker healthcheck
  that does not exist; separate identity checks and API health passed. This did
  not affect installer, budget cleanup or systemd service success.
- Small initial source rollback archive retained at
  /opt/crawlobserver/app/.deploy-backups/system-log-budget-150mb/before.tar.gz.
  Code rollback cannot restore evicted diagnostic history.

Physical bytes can temporarily exceed 150 MB while inactive parts wait for
native GC. No filesystem log-part deletion, forced OPTIMIZE, old_parts_lifetime
change, new quota/storage migration or unrelated service repair was performed.
Temporary task deployment/diagnostic files are removed after acceptance.
