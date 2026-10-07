# Quick 16: PageRank Foreground Writeback

Completed and app-only deployed on 2026-10-07.

## Result

Implementation commit: 6fc391b83768adb77cfa8826298d8d7ed0da7d2a.
Branch: codex/cleanup-deployed-worktree; pushed to origin on GitHub.
Only storage PageRank writeback, focused integration fixtures and canonical
ProductFeatures changed, plus this task's GSD records. Unrelated untracked
files remain untouched and unstaged.

PR now uses a foreground session-scoped INSERT SELECT FINAL to replace rank
and revision, preserving every other field and the original crawl timestamp.
Attempt-specific Join tables avoid historical deferred-mutation dependencies;
cleanup has a fresh bounded context. Existing graph, population, revision and
rank evidence checks remain mandatory before finalized evidence.

## Independent Review

Builder: gpt-6-luna / max. Read-only diagnosis: gpt-6.1-sol / high.
Final independent Code, Tests and operator/UX verdicts: PASS. Visual UX not
applicable; no UI changed. Test critics independently ran focused race tests
and integration-tag compilation, explicitly not local DB execution. The root
ran four actual ClickHouse fixtures using the production version in an
isolated fixture-only container; see VERIFICATION for exact results.

Reviews corrected an obsolete mutation comment and an overbroad status claim;
the final documentation explicitly scopes completed_with_errors to PageRank
finalization failure for non-stopped crawls. Platform thread limits prevented
additional fresh critic threads. Separate existing independent reviewers
reread the entire final diff; none edited files or received private findings
from another critic. This workflow limitation is recorded, not hidden.

Full Go race suite, focused race, changed/relevant-package vet, full build,
146 frontend tests, frontend lint/build and diffcheck passed. Broad vet still
has the pre-existing updater_test.go:191 lock-copy warning; not changed.

## Production Acceptance

- No-force CHECK_ONLY before build: no active crawls.
- Runtime source backup matched Git baseline b7ea809 for clickhouse_pages.go.
- Built only app with VERSION=6fc391b83768adb77cfa8826298d8d7ed0da7d2a.
- Second no-force CHECK_ONLY after build: no active crawls.
- Existing restart-app-safe.sh recreated only crawlobserver-app.
- App image: sha256:ca80f71da2cc46d63267c2d81670d49d304668ab90ecaa01a7876d7633386999.
- App container: 15a218f7d49034d1c83cee3de34104460ca8221604b9e849a19ae4df3350cdd3.
- App started: 2026-10-07T13:37:40.106206856Z.
- Health returned {"status":"ok"} after startup and again after DB readback.
- ClickHouse stayed healthy; container ID and start time unchanged:
  685ab71b0245ebfbcb339fa3ab6dd5fe404aec72e4d7602056ecbf4eb0cf5078,
  2026-10-04T01:30:46.812520785Z. SELECT 1 returned 1.
- Recent 100 app startup log error scan: 0.
- Deployed clickhouse_pages.go SHA256:
  6c6315bd999226d677491ce5eab95c28f761d4f6b50804b673926f03c9a85bc4,
  matching committed local source.

Rollback source archive, mode 0600 in a mode 0700 directory:
/opt/crawlobserver/app/.deploy-backups/quick16-6fc391b83768adb77cfa8826298d8d7ed0da7d2a/runtime-before.tar.gz
SHA256: cc253d7a73f3d0a7856f9514872cc981ba7301577ca28214d524746183e8cbdb.

Post-release read-only verification retained the two historical outcomes:
DI 675d9840 has 61 durable pages, zero fetch errors, one 404 and failed attempt
15f6f14f-cd7c-4d1a-864d-e69bc4ce8c4d. Astrogen 3ba3c262 has 29 durable pages,
zero fetch errors, five 404s and failed attempt 5f03e74d-35b7-4302-a5ed-f6c0c59cb024.
Both still truthfully show completed_with_errors. No live crawl/rescan,
production PR recompute, mutation cancellation, historical status/rank/evidence
rewrite, trust-policy change or ClickHouse restart was performed.

The isolated pending-mutation regression proves the corrected write path;
it is not a claim that a new production crawl has executed successfully.
Existing historical queued mutations were not cancelled as part of this fix.
The fixture container/volumes and all task-created server /tmp binary, runner,
release archive, deploy script, health and startup-log files were removed.
