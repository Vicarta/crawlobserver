# Quick Task 11: Fix DI PageRank Finalization Timeout

## Goal

Prevent small Daily Delta sessions from failing PageRank finalization because
unrelated historical partitions or an earlier optional finalization stage
consume its bounded execution budget. Preserve durable finalized PageRank
evidence before successful terminal completion and all existing trust gates.

## Scope And Estimate

- Expected runtime files: `internal/crawler/engine.go`,
  `internal/storage/clickhouse_pages.go`; focused crawler and storage tests,
  `ProductFeatures.md`, and quick-task planning/verification documents.
- Estimate 150-250 changed runtime/test/documentation lines, excluding the
  release evidence summary. One builder owns implementation and test files.
- Read-only production diagnostics confirmed table-wide synchronous depth
  mutation waiting on unrelated session partitions, followed by expiration of
  the shared five-minute count/depth/PageRank context.
- No frontend/email refactor, schema migration, new infrastructure, predicate
  change, quality-threshold relaxation, unrelated hardening, or historical data
  repair. Do not start a production crawl, rescan, or PageRank recomputation.

## Initial Evidence

- Read-only production evidence identifies a 61-page DI session starting at
  12:00:14. Its depth mutation began at 12:01:19; the five-minute deadline
  expired at 12:06:19. The persisted initial-snapshot PageRank timeout reports
  no lost buffered rows.
- Production confirms pages are partitioned by session. The table-wide depth
  mutation still had 16 pending parts from 10 other sessions, totaling about
  18 MB, while the target session's two parts (about 457 KB) and 61 rows were
  already completed. Waiting for unrelated partitions exhausted the shared
  context before PageRank's first graph read.
- Code uses one five-minute context for count, depth, PageRank, and near
  duplicates. Depth and PageRank both submit synchronous pages mutations with
  a session WHERE predicate but no explicit partition restriction.

## Acceptance

- Production diagnostics identify the exact failed finalization stage and its
  preceding delay rather than treating a graph-read error as proof of a slow
  graph query. Record query duration, mutation/partition evidence, and crawler
  timestamps when available; absent evidence remains explicitly unavailable.
- Depth and PageRank writes target only the session partition, retaining the
  session predicate and synchronous mutation wait.
- Mandatory PageRank receives its own fresh bounded context after count/depth
  work; an expired optional stage cannot prevent its
  first graph read. Do not replace bounded deadlines with unlimited work or
  merely increase the shared timeout.
- The existing FINAL graph/rank fingerprint, eligible population, positive/zero
  reconciliation, and complete revision-stamp checks remain authoritative.
  `completed`, terminal publication, and `Done` retain the durable evidence
  barrier; PageRank failure still records its cause and produces
  `completed_with_errors` unless explicit stopped status takes precedence.
- An isolated ClickHouse regression fixture proves a small target session can
  finish synchronous derived updates and finalize verified positive PageRank
  without mutating an unrelated session. A focused budget regression proves
  expired optional work does not pass an expired context to mandatory PageRank.
- Independent review, canonical documentation, commit/push, safe app-only
  rollout, and read-only health/ClickHouse continuity verification complete the
  task. Automated fixtures never operate on production data.

## Tasks

### 1. Confirm Cause And Apply The Smallest Finalization Fix

**Files:** `internal/crawler/engine.go`,
`internal/storage/clickhouse_pages.go`; read-only existing evidence/storage
code and production diagnostics.

**Action:** Retain the correlated failed DI session, depth-mutation timing,
target versus unrelated partition evidence, and persisted PageRank failure
as the diagnostic baseline. The raw error
`reading pagerank graph pages: context deadline exceeded` originates at the
initial snapshot before started evidence; a final readback failure has the
additional `verifying pagerank evidence` prefix. Implement only the confirmed
causal correction: partition-local depth/PageRank mutations and a fresh
bounded PageRank-stage context.
Preserve synchronous waits, final readback, failure metadata, terminal status
ordering, and stop behavior. Do not repair the already failed production
session or broaden this fix to unrelated mutation paths.

**Verify:** Production reads establish the cause and the edited path directly
addresses it; no mutation or crawl is invoked during diagnosis. Inspect the
queries and context lifetimes against the existing completion barrier.

**Done:** The identified source of the small-session timeout is corrected
without changing eligibility, quality acceptance, or product scope.

### 2. Prove Regression Behavior And Preserve Product Contracts

**Files:** `internal/crawler/engine_test.go`, focused existing/new storage test
file for isolated ClickHouse fixtures, `ProductFeatures.md`.

**Action:** Add the smallest focused context-budget regression using the
existing test patterns. Add an isolated ClickHouse target/unrelated-session
fixture for synchronous depth and PageRank updates: verify target depths,
positive eligible ranks, durable finalized evidence, full revision stamps,
target-only mutation partition scope, and unchanged unrelated rows. Unchanged
unrelated rows alone do not distinguish the old table-wide mutation behavior.
Retain fail-closed report/evidence tests for newer
started/failed events, incomplete revision stamps, and predicate mismatch.
Document the bounded stage and partition-local finalization behavior in the
canonical feature catalog. Use existing test/deployment utilities rather than
introducing a reusable harness or production diagnostic infrastructure.

**Verify:** One targeted validation pass runs relevant crawler/storage tests,
including `TestEngineDoneClosesAfterFinalization`, `TestFinalizationStatus`,
`TestFinalizationMetadataIsPersistedInTerminalRowAndClearedOnResume`,
`TestTerminalCompletionWaitsForFinalizedPageRankEvidenceAcrossRenderModes`, and
isolated ClickHouse `TestPageRankEvidenceComputeUsesEligiblePopulation` plus
the new regressions. Record exact commands/results; unavailable ClickHouse
execution is not a pass. The fixture's terminal/evidence readback is the
end-to-end acceptance result, not only mutation counters or query text.

**Done:** Tests prove the causal correction and existing successful/failing
completion and trust contracts; canonical documentation matches behavior.

### 3. Review, Commit, Push, And Safely Deploy The App

**Files:** Quick-task summary/verification and planning state; implementation
files only for blocking review findings required by this acceptance.

**Action:** After the sole builder finishes, run fresh independent Code,
Tests, and UX critics following the prescribed gauntlet loop. A backend-only
UX review may report `PASS` with a not-applicable reason. Resolve blocking
findings and obtain all three latest-iteration passes. Commit and push to the
connected GitHub repository. Preserve the existing rollback image and use the
safe deployment guard in CHECK_ONLY mode before build and again before
app-only restart. If a crawl is active, wait and recheck; never use FORCE or
stop it. Recreate only the app, leaving ClickHouse running unchanged.

**Verify:** Read-only application health, deployed release identity,
ClickHouse container continuity/health and `SELECT 1` pass. Inspect recent
runtime errors without starting any live crawl or recomputing historical
PageRank. Record the deployed commit, checks, and any remaining observational
limitation in the summary.

**Done:** The minimal reviewed correction is committed, pushed, deployed, and
documented; production application and the existing ClickHouse remain healthy.
