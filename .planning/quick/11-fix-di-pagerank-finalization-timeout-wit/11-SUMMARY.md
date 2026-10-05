# Quick 11: PageRank Finalization Timeout

Completed and deployed on 2026-10-05. Runtime commit:
`659f49382df32f0300da309c712e2677b638123b`, pushed to
`origin/codex/cleanup-deployed-worktree` on `Vicarta/crawlobserver`.

## Diagnosis And Fix

DI session `1ab684b0-bee2-4aef-ad93-0e53d546afe2` collected 61 pages with no
lost rows. Its synchronous depth mutation at 12:01:19 UTC had processed the
target session's two parts, but still waited for 16 parts from 10 other
session partitions. At 12:06:19 the shared five-minute budget expired;
PageRank's initial graph read inherited that expired context. The external
404 on `https://de.diskinternals.com/guides/features/` is separate.

Both depth and PageRank mutations now explicitly target their session
partition, retaining synchronous completion and the session WHERE predicate.
PageRank receives its own bounded context. FINAL/revision/evidence checks,
terminal status ordering, failure metadata and trust thresholds are unchanged.
No historical session or PageRank data was repaired or recomputed.

ClickHouse's exact reason for leaving the other parts pending was not proved;
the confirmed avoidable dependency on those parts is removed. No worker-pool
configuration, mutation cancellation, schema migration or trust relaxation
was introduced.

## Validation And Review

See `11-VERIFICATION.md`: full Go race suite, focused regressions, affected
package vet, app build, real isolated ClickHouse 25.5 depth/PageRank/evidence
fixtures, 144 frontend tests, lint and build passed. Fresh independent Code,
Tests and UX critics passed. Existing whole-repository vet/format failures
and Svelte/build warnings were recorded, not changed. The production build
also reported existing npm dependency audit warnings; dependency updates are
outside this timeout fix.

## Production Acceptance

- Safe CHECK_ONLY gates before build and restart both found no active crawl.
  Only `crawlobserver-app` was recreated; FORCE was not used.
- App started at `2026-10-05T15:26:02.838492223Z` (18:26 Kyiv), image
  `sha256:5495355c1d3fafad050f6646892f10b4715df1b30962809c4a4a0a16af2e80c6`.
- Health returned `{"status":"ok"}`; 54 startup log lines contained zero
  unexpected errors. Deployed runtime source hashes match the release commit.
- ClickHouse retained container
  `685ab71b0245ebfbcb339fa3ab6dd5fe404aec72e4d7602056ecbf4eb0cf5078`,
  unchanged start `2026-10-04T01:30:46.812520785Z`, healthy; SELECT 1 returned 1.
- Historical session readback is unchanged: 61 pages,
  `completed_with_errors`, original PageRank timeout, one observed HTTP 404.
- No live crawl/rescan/recompute or external-site modification was invoked.
  Actual next scheduled Delta completion remains a natural live observation,
  not something claimed by the isolated fixture or health check.
- Rollback image and source archive retained at
  `/opt/crawlobserver/app/.deploy-backups/pr-timeout-659f49382df32f0300da309c712e2677b638123b`.
  This task's temporary test container/volumes were removed; uploaded scratch
  scripts, test binary and deployment tar are removed after acceptance.
