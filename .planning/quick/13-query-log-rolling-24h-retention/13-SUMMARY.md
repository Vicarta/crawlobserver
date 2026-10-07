# Quick 13 Summary

Completed and deployed on 2026-10-07. Release
`d0c9d40e587546ad68f1d682319de0d5656bd237` pushed to
`origin/codex/cleanup-deployed-worktree` (Vicarta/crawlobserver).

## Scope And Checks

Only system.query_log changed from date-based three-day expiry to rolling
event_time + INTERVAL 24 HOUR DELETE. Updated mounted startup policy, existing
table TTL, deploy README and ProductFeatures. Other retention policies and
crawler/PageRank behavior are unchanged. Focused XML comparison, fake preflight
success/failure tests, timestamp boundary SELECT, diff check and all final
independent reviews passed; see 13-VERIFICATION.md. No app source changed.

## Production Acceptance

- Two FORCE=0 CHECK_ONLY=1 gates found no active/queued crawls.
- Mounted policy SHA256 matches release:
  `d4c80a04648ca9095d0a1d3ffeda10355b0df43c4cc446ac38284af44da28ea0`.
- Live DDL readback: query_log TTL `event_time + toIntervalHour(24)`.
  Other inspected system logs still have three days; application_logs five.
- Normal TTL materialization `mutation_513585.txt`, started
  `2026-10-07 07:56:46` UTC, completed with is_done=1, parts_to_do=0.
  No forced OPTIMIZE or mutation cancellation.
- At 07:59:30 UTC, query_log had 1,131,230 rows versus 2,573,384 before;
  active bytes 172,460,680 versus 392,116,540 (about 164.47 versus 373.95 MiB).
- Physical deletion is asynchronous: inactive bytes 414,679,107 immediately
  after materialization. 1,960 rows had newly crossed 24h during the roughly
  three-minute operation (oldest 2026-10-06 07:56:48). They await subsequent
  ordinary TTL merges; no strict wall-clock or immediate disk cap is claimed.
- Query logging continues (latest event 07:59:28 UTC). `SELECT 1` returned 1,
  app health returned `{"status":"ok"}`, ClickHouse healthy.
- Neither service restarted: app identity
  `14fe91c24c1ae7c7ff25c8f60c6e9f2d9a6609a6abfc9d3b8f964f76a7845317`,
  start `2026-10-06T19:01:09.524078319Z`; ClickHouse identity
  `685ab71b0245ebfbcb339fa3ab6dd5fe404aec72e4d7602056ecbf4eb0cf5078`,
  start `2026-10-04T01:30:46.812520785Z`.
- Prior policy/docs retained in
  `/opt/crawlobserver/app/.deploy-backups/query-log-24h-d0c9d40e587546ad68f1d682319de0d5656bd237/policy-docs-before.tar.gz`.
  Restoring that policy does not recover deleted query history.
- No live crawl/rescan, crawl data deletion, other table mutation, credentials
  changes, restart or FORCE bypass. Unrelated pending pages mutations were
  observed read-only and remain outside this retention change.

## Result

Rolling 24-hour expiry is applied in production and persisted for subsequent
ClickHouse startup. As with all MergeTree TTL, old physical parts and newly
expired records are removed asynchronously by normal background cleanup.
