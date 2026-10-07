# Quick 15 Summary

Completed and deployed on 2026-10-07.

## Changes

Production now uses three distinct recent local daily recovery archives plus
the latest archive from the previous Monday-Sunday week, deduplicated when
one file serves both buckets. Schedule remains 00:20 Europe/Kyiv. The new
retain_weekly option is opt-in; legacy count retention is unchanged.

Scheduled/manual full archive paths use the same policy. Calendar-mode
critical exports never prune until full archive creation succeeds and preserve
required nearest earlier exports. Manual export defers pruning. Marked
lightweight pre-update copies use a separate count pool and cannot displace
or postpone full backups; archive contents, listing and restore are unchanged.
Historical unmarked partial copies are not reclassified. Docker self-update
is disabled and production's two existing archives are SQL backups.

## Evidence

Implementation commit: `1fd86a710d5f02816e34e81d26b2b423c7b95671`, pushed to
`origin/codex/cleanup-deployed-worktree` at https://github.com/Vicarta/crawlobserver.
Fresh final Code/Tests/operator-UX reviews all PASS. Full race suite passed on
the sequential final rerun; targeted race/vet/build and frontend 146 tests,
lint/build passed. Unrelated broad-vet updater warning is recorded, not fixed.

Both no-force deploy gates passed. Only app rebuilt/restarted; health status=ok,
ClickHouse identity/start unchanged and healthy, SELECT 1=1. Parsed config and
scheduler startup confirm 3+1 at 00:20 Europe/Kyiv. Existing backup/export
inventory unchanged and startup unexpected-error scan zero. See VERIFICATION
for exact image/container and restricted rollback paths.

Only local October 6/7 archive/export generations currently exist. Deleted
previous-week history cannot be recreated by a retention change. Future
successful runs populate the third daily and weekly slots. No fabricated old
copies or live production backup/crawl/rescan/restore/cleanup for acceptance.

## Agents

Builder: backup_retention_builder, gpt-6-luna/max.
Audit and independent final Code/Tests/UX: gpt-6.1-sol/high.
Root owned integration, git and safe production release.
