# Quick 17: Robots Eligibility Before Daily Delta Launch

## Authorized Goal

Exclude robots-disallowed URLs from Daily Delta execution plans before launch,
systemically for all projects and candidate sources. Preserve the existing
100 percent Quality coverage gate. Tests, independent review, commit/push and
safe app-only production release are required. No live crawl/rescan, history
repair, threshold change, churn guard, sitemap/site edit or unrelated cleanup.

Baseline HEAD: 1c4b5bce0d9e153a1c67163e07d8cefa4eddcfc8.
Branch: codex/cleanup-deployed-worktree, upstream origin on GitHub.
Unrelated untracked files must remain untouched and unstaged.

## Proven Production Case

DI Oct 9 session 1dfe4838-ffca-413d-b236-fc864d9309fd planned 2053 and
fetched 2052. Oct 10 session 5ab183a0-5260-4fcf-bcfc-f79ad3477494 planned
2052 and fetched 2051. Both plans include /order/; retained robots.txt says
Disallow: /order/* and crawler logs confirm the URL was blocked. Coverage
99.95 percent correctly fails the unchanged 100 percent gate. Snapshot
publication does not occur, and pending-unpublished sitemap events repeat.
Discovery is disabled; this is selection/execution eligibility mismatch.

## Smallest Implementation

Use the existing RobotsCache and exact crawler policy/config (user agent,
timeout, TLS, source IP/private-address controls and per-origin rules).
Apply RespectRobotsTxt to every candidate source before execution budgets and
DeltaPlan launched URLs/counts/source mappings are finalized. Preserve raw
sitemap observation contents. Excluded sitemap events must not be counted as
unlaunched/deferred execution work or invalidate launch reservation rereads.
Keep blocked manual queue entries unconsumed. Respect-disabled projects retain
their existing behavior. Do not invent a new robots failure policy.

Expected files: handlers_delta.go, small delta_robots helper/tests, existing
sitemap refresh cache wiring if needed, ProductFeatures. Estimate 250-400
lines including focused fixtures; necessary test coverage may increase this
without expanding the objective. Builder gpt-6-luna/max; independent Code,
Tests/operator critics gpt-6.1-sol/high. Root owns GSD, git and production.

## Acceptance And Validation

Fixture-only tests must demonstrate /order/ omitted from the immutable plan,
allowed siblings retained, budget applied after exclusion, all-source and
per-origin/user-agent/Allow behavior, no-candidate no launch, respect-disabled
behavior, manual retention, selection accounting and reservation agreement.
Quality thresholds/code remain unchanged. No tests may crawl production.

Run focused/full Go race tests, relevant vet/build and existing frontend gates.
Use three independent frozen-iteration reviews; fix actionable findings only.
After commit/push, run no-force CHECK_ONLY before and after app build; restart
only app, verify health, unchanged healthy ClickHouse/SELECT 1 and recent logs.
Production acceptance may use a read-only plan preview (robots/sitemap reads),
not a live delta run. Record any authentication/access limitation explicitly.
