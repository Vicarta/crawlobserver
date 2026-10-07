# Quick 15 Verification

## Frozen Implementation

Opt-in `backup.retain_weekly: 1` selects three newest distinct local dates
and the newest previous Monday-Sunday week archive, with overlap deduplicated.
Production uses `retain: 3`, `time: "00:20"`, `timezone: Europe/Kyiv`.
Legacy count mode remains unchanged. All full-backup paths apply the policy;
calendar export pruning runs only after successful full archive creation and
keeps nearest earlier exports needed by retained full archives. Manual exports
do not prune. New marked pre-update snapshots use a separate bounded count pool
and do not displace or postpone full backups. Archive contents/API unchanged.

## Local Evidence

- Focused fixture/race tests: backup, config, CLI, storage and manual backup/
  export server paths passed. No production backup/crawl/restore invoked.
- New fixtures cover daily/week/year/timezone/DST boundaries, marked partials,
  legacy mode, successful and failed full backup, manual export after rollover,
  and a true local-midnight export dependency outside all calendar dates.
- Fresh final Code, Tests and operator-UX critics: PASS/PASS/PASS.
- `go build ./...` and `go vet` for all changed packages passed.
- `go vet ./...` reports an existing unchanged updater test lock-copy warning
  at internal/updater/updater_test.go:191. No unrelated fix performed.
- Initial `go test -race ./...` passed. Final parallel run hit the existing
  auth timing fixture TestPasswordlessCodeRequestDoesNotWaitForProvider; a
  sequential final rerun passed (all packages, server 50.876s). No auth
  source/test changes.
- Frontend: 146 tests, ESLint and Vite build passed; existing Svelte/chunk
  warnings remain unchanged. No frontend changes.
- `git diff --check` passed. golangci-lint is not installed locally.

## Production Acceptance Contract

Deployed 2026-10-07 from implementation commit
`1fd86a710d5f02816e34e81d26b2b423c7b95671`. Both no-force safety gates passed;
only app was rebuilt/recreated. API health returned status=ok. ClickHouse
remained healthy, with identical container ID and start time, SELECT 1=1.
Existing archive/export names, sizes and modification times are unchanged.
No live crawl/rescan/backup/restore or acceptance-time archive cleanup occurred.

Scheduler startup readback:
`daily at 00:20 (Europe/Kyiv), retaining three daily dates plus the previous calendar week`.
Production config parsed back as retain=3 / retain_weekly=1, enabled=true,
interval=24h; structured comparison proved no other config changes.
Unexpected startup-error scan: zero matching lines.

App image: `sha256:ec973c67d6d606989be28175a870024dde61eda3b650c1fdc1d58b0c8b928fe8`.
App container: `62d700a1a3c973173d1a8e0735621b362ac8a332830e22efa3c9226e672f9df0`,
started 2026-10-07T12:49:45.423493852Z.
Unchanged ClickHouse: `685ab71b0245ebfbcb339fa3ab6dd5fe404aec72e4d7602056ecbf4eb0cf5078`,
started 2026-10-04T01:30:46.812520785Z.
Restricted rollback source/config copies are retained under
`/opt/crawlobserver/app/.deploy-backups/backup-retention-1fd86a710d5f02816e34e81d26b2b423c7b95671`.
Production npm install reported existing dependency audit warnings; no dependency
updates were included in this bounded retention release.

Only two SQL archive/export generations (local October 6 and 7) exist before
release. There is no previous-week backup to reconstruct; future successful
runs populate the retention buckets. No fake historical copies.
