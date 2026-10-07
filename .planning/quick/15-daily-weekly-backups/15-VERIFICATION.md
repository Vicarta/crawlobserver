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

Deployment pending. Require no-force safety gate before app image build and
again after build, safe app-only restart, health response status=ok, unchanged
healthy ClickHouse identity and SELECT 1=1, and scheduler log with 00:20
Europe/Kyiv and three dates plus previous week. Compare archive/export names,
sizes and modification times before/after; no acceptance-time archive cleanup.

Only two SQL archive/export generations (local October 6 and 7) exist before
release. There is no previous-week backup to reconstruct; future successful
runs populate the retention buckets. No fake historical copies.
