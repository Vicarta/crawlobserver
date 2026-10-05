---
status: passed
date: 2026-10-05
---

# Quick 11 Verification

## Confirmed Cause

Production session `1ab684b0-bee2-4aef-ad93-0e53d546afe2` collected 61 pages
with zero lost rows. Its depth mutation started at 12:01:19 UTC; terminal
failure followed at 12:06:19. Target parts had already finished, while the
same synchronous mutation still awaited 16 parts in 10 unrelated partitions.
PageRank inherited the exhausted five-minute context and failed before its
initial graph snapshot. Earlier reads took milliseconds. No specific
ClickHouse worker-stall reason was established; there is no evidence of an
actual SYSTEM STOP MERGES command. The separate HTTP 404 is not this cause.

## Runtime Contract

- Depth and PageRank updates use `IN PARTITION tuple(toUUID(?))`, retain the
  session WHERE predicate, and keep `mutations_sync=1`.
- PageRank gets a fresh bounded five-minute context after count/depth.
- FINAL fingerprints, eligible population, complete revision stamps and
  durable finalized evidence still precede successful terminal publication.
- Failure metadata, `completed_with_errors`, explicit stopping and Done
  ordering remain unchanged. No historical data repair or live recompute.

## Validation

- `go test -race ./...`: PASS, all packages.
- Independent focused crawler race regressions: PASS, including expired
  optional budget, finalization status, metadata, Done ordering and stop paths.
- `go vet ./internal/crawler ./internal/storage`: PASS.
- `go build -o /private/tmp/co-pr-timeout-local ./cmd/crawlobserver`: PASS.
- Storage integration compile: PASS. Real ClickHouse 25.5 in a temporary
  network-disabled, resource-bounded container: PASS for
  `TestFinalizationMutationsStayInSessionPartition`,
  `TestPageRankEvidenceSequenceOrdersEqualTimestampsAcrossRetryAndRestart`,
  and `TestPageRankEvidenceComputeUsesEligiblePopulation`.
- Target depth/found_on, positive PageRank, finalized evidence and full revision
  stamps were read back; unrelated rows stayed unchanged; mutation commands
  proved explicit partition scope. Container and volumes removed afterwards.
- Frontend: 144 tests PASS, lint PASS, build PASS. Existing Svelte/build
  warnings remain. No frontend source changed.
- `git diff --check`: PASS.
- Fresh independent Code, Tests and UX critics: all PASS; UX not applicable
  because no UI surface changed. Models: `gpt-6.1-sol / high` for reviews and
  planning; `gpt-6-luna / max` for implementation and production diagnosis.

## Existing Unrelated Gate Failures

Full `go vet ./...` reports the existing copied-RWMutex warning in
`internal/updater/updater_test.go:191`. Frontend `npm run format:check`
reports 21 existing unchanged source files. Neither was modified or staged.
No claim that these whole-repository gates passed is made.

## Production Acceptance

Recorded in `11-SUMMARY.md` after the no-force app-only rollout. No live crawl,
rescan, PageRank recompute, historical session rewrite or external-site change
is part of this task. The next scheduled run is a natural live observation,
not a test invoked by this release.
