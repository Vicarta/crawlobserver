# Quick 16: PageRank Write Must Not Wait For Unrelated Mutations

## Request And Scope

Fix PageRank calculation/finalization timeouts reported for DI session
675d9840-ed01-4b7b-ab68-27fcd7f5023f and Astrogen session
3ba3c262-a533-4851-afb8-b966d1c0fac8. Determine whether fetching/persistence
completed. Commit/push and safe app-only production release. No new crawl,
rescan, historical status/rank rewrite, mutation cancellation, schema change,
trust relaxation, depth refactor or new recovery orchestration.

Expected owned implementation: storage PageRank write function, existing
partition integration test, focused new regression tests, ProductFeatures.
Estimate 200-350 lines including fixtures/docs; necessary test coverage may
increase this without changing the accepted objective. Builder Luna/max;
audit and fresh independent Code/Tests/operator-UX critics Sol/high. Root
owns GSD, isolated runtime verification, git and production release.

## Verified Read-Only Production Evidence

- DI: pages_crawled=61, durable FINAL pages=61, fetch_errors=0, observed404=1.
- Astrogen: pages_crawled=29, durable FINAL pages=29, fetch_errors=0,404=5.
- Both Finalization records contain only pagerank_failure, no buffer loss.
- Graph work is small (46 / 21 eligible pages). Five-minute write waits
  ended with updating pagerank via joinGet context deadline exceeded.
- Every page already has the corresponding failed attempt revision:
  DI15f6f14f-cd7c-4d1a-864d-e69bc4ce8c4d;
  Astrogen5f03e74d-35b7-4302-a5ed-f6c0c59cb024. Positive counts46/21.
- 18 outstanding pages mutations; oldest mutation_78248.txt from October1
  fails UNKNOWN_TABLE for missing temporary depth Join table. New session
  partition-scoped depth/PR mutations still wait for14unrelatedparts.
- Quick11 partition/fresh-budget changes are deployed but do not eliminate
  table-wide mutation completion waits; this supersedes that assumption.
- Production pages uses ReplacingMergeTree(crawled_at), partition=session,
  ORDER BY(session,url). No active crawl at initial safety check.

## Minimal Change And Acceptance

Replace only PageRank ALTER mutation with a foreground session-scoped
INSERT SELECT FINAL replacement of pagerank and pagerank_revision, preserving
crawled_at and all other fields. Use attempt-specific rank Join table so
previous queued mutations cannot depend on or reuse the new table. Bounded
fresh cleanup context; preserve existing graph/rank FINAL verification,
revision coverage, evidence lifecycle and terminal/publication ordering.

Real isolated ClickHouse25.5 fixtures must reproduce a poisoned unrelated
mutation and prove PR finalizes without waiting for it, leaves unrelated
session/page data unchanged, preserves duplicate-version selection and fields,
stamps all rows, handles a second attempt, and survives FINAL compaction.
No production graph recompute or mutation cleanup for testing. Local Docker
is unavailable; use a resource-bounded network-disabled throwaway server
container with fixture data and a cross-compiled test binary if safe.

Run focused race, full race, changed-package vet/build, relevant frontend
gates and diffcheck. Obtain fresh three PASS reviews before commit/push.
Then no-force safety gates before/after app build; restart only app, verify
health, unchanged healthy ClickHouse and SELECT1, deployed source/version,
and read-only unchanged historical session/evidence. Record actual fixture
acceptance separately from future natural scheduled-crawl observation.
