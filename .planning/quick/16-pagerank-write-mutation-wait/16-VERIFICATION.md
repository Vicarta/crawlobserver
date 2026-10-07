# Quick 16 Verification

## Read-Only Diagnosis

Production DI session 675d9840-ed01-4b7b-ab68-27fcd7f5023f has 61 durable
FINAL pages and 61 pages_crawled; Astrogen 3ba3c262-a533-4851-afb8-b966d1c0fac8
has 29/29. Both have zero fetch error strings, no recorded buffer loss and only
pagerank_failure in Finalization. Observed site 404 counts are 1/5 respectively.
Fetching/persistence completed; the complete crawl pipeline did not finalize
successfully and cannot be presented as trusted successful analytics.

All stored rows already carry each failed attempt's rank revision. Writes
waited five minutes on synchronous ALTER completion. Production pages has 18
pending mutations; the earliest October 1 depth mutation references a missing
Join table. New partition-scoped PR mutations wait on 14 unrelated parts.
This proves the earlier partition-only fix does not eliminate the dependency.

## Real ClickHouse Acceptance

Version 25.5.11.15, immutable production ClickHouse image reused in a separate
network-disabled, 1 CPU/1 GiB container with fixture-only database. No published
port, production volume/config/credentials or production mutation changes.
Container and anonymous fixture volumes removed by trap.

Linux/amd64 fixture binary built with:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -tags integration -o /private/tmp/co-pr-write-integration.test ./internal/storage
```

Final actual execution passed:

- TestFinalizationMutationsStayInSessionPartition: 0.49s.
- TestPageRankInsertWritebackIgnoresUnrelatedPendingMutation: 0.61s;
  first compute 145.5ms with poisoned unrelated mutation still pending.
- TestPageRankEvidenceSequenceOrdersEqualTimestampsAcrossRetryAndRestart: 0.22s.
- TestPageRankEvidenceComputeUsesEligiblePopulation: 0.44s.

Fixture reproduces create Join -> stop merges -> queue mutation -> drop Join
-> start merges -> durable UNKNOWN_TABLE. It verifies PR finalized evidence,
duplicate-version selection, preserved crawl timestamps/title/HTML/depth,
revision stamping/reset of ineligible rows, unchanged unrelated rows, unique
attempt-table cleanup, preserved old session table, retry and OPTIMIZE FINAL.
Fixture mutation cancellation is confined to the throwaway database.

Two initial fixture runs failed and were corrected before acceptance: missing
Join table was rejected at ALTER analysis, and actual UNKNOWN_TABLE spelling
did not match a spaced-only error assertion. No runtime change was needed for
these fixture corrections. Final real execution, not compile-only, is green.

## Repository Checks

- go test -race ./...:PASS.
- go test -race ./internal/storage ./internal/crawler ./internal/server -run 'PageRank|Finalization' -count=1:PASS.
- go vet ./internal/storage ./internal/crawler ./internal/server:PASS.
- go build ./...:PASS.
- Integration-tag package compilation:PASS.
- frontend npm test: 146 tests/14 files PASS; npm run lint and npm run build PASS.
- git diff --check:PASS.
- Broad go vet ./... retains pre-existing updater_test.go:191 lock-copy warning;
  untouched. Existing frontend Svelte/chunk-size warnings remain unchanged.

## Release Gate

Independent Code, Tests and operator/UX reviews pass for the final wording-only
iteration; visual UX is not applicable because no UI changed. Review caught two obsolete or
overbroad comments; the builder corrected only wording, without runtime/test
changes. Platform agent-thread capacity prevented creating additional fresh
critic threads, so existing independent critics received separate review
instructions and reread the complete final diff. No critic edited files or
received another critic's findings/private builder reasoning. Production
release results are recorded in SUMMARY.
No live crawl/rescan or historical session/evidence rewrite is permitted.
Two no-force active-crawl checks are required around an app-only image build.
Health, unchanged healthy ClickHouse, SELECT 1, startup logs and read-only
historical session preservation are required after app restart. Future
scheduled-crawl success is separate from fixture/source acceptance.
