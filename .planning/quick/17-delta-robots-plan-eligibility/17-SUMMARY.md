# Quick 17 Complete

Completed 2026-10-10. Implementation commit:
`4f44ab2abe2ed0bb803d4f903d9b446d099761ad`, pushed to
`origin/codex/cleanup-deployed-worktree` on GitHub.

Robots-disallowed gathered candidates are excluded before sitemap/global
execution budgeting and immutable launch plan creation. The actual crawler's
effective runtime robots settings are used. Raw observations/stable holds,
manual queue retention and unchanged 100 percent Quality coverage remain.
Preview and DeltaPlan expose bounded exclusion provenance. Three independent
final reviews PASS; automated gates and limitations recorded in VERIFICATION.

## Production Release

- Pre-build and post-build no-force CHECK_ONLY: no active crawl, PASS.
- Only app rebuilt/recreated; no ClickHouse restart.
- App image: `sha256:30a859f23150f0f6bb59a81d05dcb18e7dc95da7761c5f901b0a4dc24618f453`.
- App container: `f5bd809e5b7da5cbbc53c738f7c21abdcd526bb3eb1ea7de6004859301373a5e`.
- Health OK immediately after startup retries and again after rollout.
- ClickHouse unchanged: `685ab71b0245ebfbcb339fa3ab6dd5fe404aec72e4d7602056ecbf4eb0cf5078`,
  started `2026-10-04T01:30:46.812520785Z`, healthy; SELECT 1 returned 1.
- Recent 100 app-log unexpected-error scan: zero.
- Deployed four runtime source hashes exactly match committed local source.
- Rollback source archive:
  `/opt/crawlobserver/app/.deploy-backups/quick17-4f44ab2abe2ed0bb803d4f903d9b446d099761ad/runtime-before.tar.gz`.
  SHA256 `6599f5daaf0c3a787787c1559f383d630abb969b770c4f16e8e348b4b74ef72a`.
- Own server /tmp release/script/preview/health/log files removed; rollback retained.

No live crawl/rescan, historical session rewrite, snapshot publication or
trust relaxation performed. Authenticated production preview not available
without changing credentials; no key created. Next scheduled Delta uses the
new plan filter. Existing independent publication holds and large legitimate
candidate sets are not overridden, so no promised page-count or promotion.
