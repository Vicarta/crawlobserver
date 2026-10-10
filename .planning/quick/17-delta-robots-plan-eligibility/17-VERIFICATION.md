# Quick 17 Verification

Date: 2026-10-10. Scope: robots eligibility before Delta launch only.

## Fixture Acceptance

The end-to-end planner fixture retains three raw sitemap rows, selects two
allowed siblings under a two-page cap, and excludes the blocked sitemap,
manual and problem URLs. Preview and immutable request plan agree: two
launched URLs, three exclusions, zero deferred work. Manual remains pending;
reservation reread agrees without a second robots request. All-blocked plans
are rejected without launch. Existing Quality coverage remains 100 percent.

Specific-agent rules, Allow overrides, independent origins and path/query
behavior use the existing crawler parser. A divergent saved-baseline/runtime
fixture proves runtime timeout/private-IP policy plus request UA/TLS/IP
overrides match actual crawler execution. Raw stable acknowledgements and
their publication holds are unchanged. Respect-disabled behavior is retained.

## Automated Gates

- Final `go test ./...`: PASS.
- Final `go test ./internal/server -run 'Quality|Delta' -count=1`: PASS.
- Final focused `go test -race ./internal/server -run 'Delta|Robots' -count=1`:
  PASS (builder and independent Tests critic).
- Broader race pass on server/storage/config/fetcher/crawler: PASS on the
  initial iteration; final config-overlay change covered by focused race.
- Final `go vet ./internal/server ./internal/storage ./internal/config
  ./internal/fetcher ./internal/crawler`: PASS.
- Final `go build ./...`: PASS.
- Frontend `npm test`: 146 tests / 14 files PASS; lint and build PASS.
- `git diff --check`: PASS.
- Full `go vet ./...`: pre-existing unrelated RWMutex-copy finding at
  internal/updater/updater_test.go:191; not modified.

Local HTTP fixtures required approved sandbox escalation for loopback binding.
No fixture invoked a live site crawl, rescan or production mutation.

## Independent Reviews

Builder: gpt-6-luna/max. Fresh Code, Tests and API/operator critics:
gpt-6.1-sol/high. First review caught effective-config mismatch; builder fixed
it and added the divergence regression. Three fresh final critics report PASS.
Code critic could run pure tests only; HTTP/race evidence supplied by builder,
Tests critic and operator critic. No visual UI change or screenshot required.

Nonblocking residuals: unchanged non-sitemap source prefetch windows can be
occupied by blocked URLs; this task does not change query retrieval policy.
No dedicated greater-than-20 sample/serialized persistence fixture; bounded
sample and copy mappings inspected by independent reviewers.

## Production Boundary

Baseline production source hashes match HEAD for handlers_delta.go,
delta_sitemap_refresh.go and config.go. Initial CHECK_ONLY gate reports no
active crawl. Release requires commit/push, a fresh pre-build gate, post-build
gate, app-only restart, health, unchanged healthy ClickHouse/SELECT 1 and log
scan. Production acceptance/deployment evidence follows in SUMMARY.

Authenticated local GET preview was unavailable: server.api_key is empty.
No credential was created or permissions changed; no secret was printed.
Runtime behavior proven by fixture evidence; no live Delta acceptance run.
