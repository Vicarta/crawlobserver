# GSD State

## Project Reference

See: `.planning/PROJECT.md` (updated 2026-05-29)

**Core value:** A server operator can deploy, start, secure, verify, and recover CrawlObserver without guessing critical runtime steps.
**Current focus:** Phase 26: Trustworthy sitemap availability and lastmod validation (planned)

## Current Status

- Repository cloned into the local CrawlObserver workspace.
- Brownfield codebase map initialized under `.planning/codebase/`.
- Requirements and roadmap initialized for server deployment.
- Dedicated GitHub SSH key generated locally.
- Server target recorded locally outside public project artifacts.
- Service deployment mode changed to Docker Compose.
- Access policy recorded: Tailscale-only, no public service exposure.
- Server deployment completed on 2026-05-29:
  - Docker image built on server.
  - ClickHouse migrations completed.
  - App running through Docker Compose.
  - Tailscale Serve configured for private tailnet access.
  - Public ports `8899`, `8123`, and `9000` are not reachable from outside.
- Phase 5 completed on 2026-06-03:
  - Fixed `GET /api/projects` filtering for project-scoped API keys.
  - Added local admin/viewer users with session cookies and project assignments.
  - Added admin UI user management and viewer UI gating.
  - Added regression tests for project-key and viewer scoping.
- Phase 6 completed on 2026-06-06:
  - Added admin-only selected-page rescan API and Pages UI bulk action.
  - Targeted rescans refetch exact selected URLs without automatic depth/PageRank recomputation.
  - Added hover-readable overflow text for Pages URL/title cells.
  - Added resizable persisted columns in the shared data table.
  - Built and deployed through Docker Compose on the server.

## Next Action

Phase 25.2 is complete and deployed. Phase 26 is planned; begin it only on
explicit product direction and keep its sitemap-reporting scope separate.

## Accumulated Context

### Roadmap Evolution

- Phase 25.2 inserted after Phase 25.1: bounded changed-only Daily Delta and
  verified effective origins (URGENT).
  - Fresh sitemap planning compares both the newest complete raw observation and
    the materialized Current Snapshot sitemap observation. The published
    materialized observation remains the authoritative safety term, so capped,
    deferred, failed, or untrusted work cannot consume a pending added or
    forward-lastmod event.
  - Every evidence-backed changed event is eligible by default, followed by up
    to 50 deterministic rotating canaries. Existing changed/new/global limits
    remain safety ceilings: quiet sites normally stay near the requested small
    daily set, while genuine broad changes may use the configured capacity.
  - Conditional-request mode sends real validators; `304` outcomes preserve
    current page and link evidence instead of replacing it with empty evidence.
  - Static and rendered discovery share one exact independent zero/N budget.
  - Session list/detail responses derive `effective_origin` only from durable
    launched/final-redirect evidence. Raw `SeedURLs` remain unchanged provenance
    and ProjectPage displays both.
  - Five implementation plans include focused tests, full local gates,
    `ProductFeatures.md` ownership, and guarded app-only deployment. Live crawls
    and production data mutation are explicitly excluded.
  - Phase 26 remains planned and now depends on Phase 25.2.
  - Completed and deployed on 2026-08-28 from commit
    `fc5a708afe0e3cc2a6cda63a42f80737a5e22ccc`. The rollout waited for the
    active DI crawl to finish, passed both safe gates, restarted only the app,
    and verified app/ClickHouse health plus read-only production UI evidence.
    DI currently has 2,063 forward-lastmod events and 25 canaries, so its large
    next candidate set is justified by change evidence rather than a minimum
    target or an automatic reduction of its 5,000 safety ceiling.

- Phase 25.1 inserted after Phase 25: PageRank quality evidence consistency (URGENT).
  - At phase start, production session `cecabb70-b621-48a1-9dc4-1feb3c3757cb` was marked
    completed at 10:37:13 and quality-evaluated at 10:37:18 with 20 zero-PR
    pages, while PageRank completed at 10:37:19; all 51 eligible pages already
    had positive PR but the persisted quality result still read `untrusted · 65`.
  - Phase 25.1 adds shared PageRank eligibility, durable evidence/evaluation
    revisions, fail-closed completion/promotion, deterministic replay, and a
    narrow admin quality re-evaluation/readback contract.
  - First production rollout repaired the target quality result, but acceptance
    failed because historical sessions selected future baselines and repeatedly
    moved Current Snapshot backwards. The app was safely rolled back with no
    crawl or ClickHouse restart.
  - Attempt 2 requires strict historical predecessors, an authoritative
    full-source/content watermark, typed `superseded` replay, scheduler fixed
    point, and stable production pointer/revision counts across two intervals.
  - Strict Sol plan check passed on 2026-08-08 after append-only history,
    deterministic legacy adoption, independent promotion recovery,
    fail-on-unavailable ClickHouse tests, API provenance, and rollback
    checkpoints were made explicit.
  - Phase 26 remains planned and unstarted; no sitemap work is mixed into this
    urgent defect.
  - Phase 27 tracks authorization-scope hardening without changing the Growth
    Core agency-wide master key; Phase 28 tracks external OpenAPI/evidence
    projections.
  - Completed and deployed on 2026-08-08. The exact Gerus session now has
    finalized `observed_existing` evidence with 51 eligible / 51 positive / 0
    zero pages, `trusted · 90` quality, and Current Snapshot revision 24.
  - Legacy `untrusted · 65` remains immutable history. More than two scheduler
    intervals produced no new evaluation, promotion, snapshot revision, or
    crawl; ClickHouse was not restarted.

- Phase 26 added and planned: trustworthy sitemap availability and lastmod
  validation.
  - Split the ambiguous `sitemap_only` population into observed present,
    missing 404/410, redirect, error, and not-crawled states.
  - Add explicit sitemap-observation completeness and URL-match provenance so
    partial or legacy evidence cannot be described as a complete audit.
  - Validate raw sitemap `lastmod` against sourced page-modification evidence
    and expose missing, invalid, future, stale, newer, and unavailable states.
  - Keep existing raw sessions, Current Snapshot trust rules, legacy API
    compatibility, and app-only rollout safety.

- Phase 25 completed and deployed on 2026-08-03:
  - Traced the Astrogen 404 to an internal article link targeting the obsolete
    slashless URL, followed by a `301` to the final trailing-slash `404`.
  - Added redirect-aware discovery evidence to page detail and aligned its
    source classification with the Pages table.
  - Added an always-visible `URL discovered from` block before GSC Ranking
    Keywords, including source page, anchor, DOM location, redirect alias,
    sitemap/seed/candidate evidence, and an explicit unavailable state.
  - Verified focused Go and frontend suites, desktop/mobile production UI,
    app health, and ClickHouse continuity after an app-only safe restart.

- Phase 25 added: URL discovery provenance in page detail.
  - Operators need the URL Detail view to explain where a URL came from before
    they interpret a 404 as a website defect.
  - The implementation must distinguish concrete inlinks, sitemap/raw-loc
    evidence, seeds, Daily Delta candidates, and unavailable historical evidence.

- Phase 24 completed and deployed on 2026-07-27:
  - Reproduced the Gerus defect only under concurrent same-origin Chrome
    navigations: rendered body/H1 changed while route metadata could remain the
    shared SPA shell indefinitely.
  - Added context-aware per-origin render serialization while preserving
    cross-origin concurrency. Each page timeout begins after its render slot is
    acquired.
  - Split server HTML and rendered DOM evidence in Pages > Issues and corrected
    Unicode SEO text lengths.
  - Verified renderer race tests, full crawler/storage suites, the focused
    Issues API handler, 98 frontend tests, lint, build, production HTTPS, UI,
    and logs.
  - Rebuilt and recreated only `crawlobserver-app` after two zero-active-crawl
    preflights. Rollback image:
    `crawlobserver:rollback-js-origin-20260727-171019`.

- Phase 23 completed and deployed on 2026-07-18:
  - Added selected-row CSV export to Pages All/HTML, Crawl Sessions, and Interlinking Opportunities.
  - Used explicit business-field projections and the existing UTF-8 CSV serializer.
  - Replaced Interlinking index-based selection with stable source/target/category identities so sorting and pagination cannot remap a selection.
  - Verified 98 frontend tests, lint, production build, three authenticated production downloads, app-only recreation, and healthy runtime.

- Phase 21 added: Fresh sitemap synchronization for Daily Delta.
  - Confirmed via production read-only evidence that Daily Delta read `sitemap_urls` from the materialized current snapshot and invoked the crawler with `FetchSitemaps=false`.
  - A corrected live sitemap URL could therefore coexist with an obsolete URL in the snapshot and be re-crawled repeatedly, including zero-inlink 404 candidates.
  - The phase refreshes sitemap candidates at planning time, records provenance/failure state, and prevents stale snapshot sitemap entries from silently driving new Delta runs.
  - Completed and deployed on 2026-07-16. Every sitemap-enabled Delta preview/run/schedule now uses a fresh sitemap observation; snapshot membership is updated only after trusted fresh promotion.
  - Production dry-runs confirmed fresh mode for Astrogen (171 fresh, 17 added, 16 removed) and DI (2,676 fresh, 38 added, 0 removed), with no crawl session created by verification.

- Phase 20 added: Strict 5xx trust gate and origin-safe crawl pressure control.
  - Confirmed origin cause: concurrent page requests amplified uncached CMS list fetches and produced real transient upstream 500 responses.
  - CrawlObserver remediation remains necessary: fail-closed 5xx promotion gates, row-level transient-failure quarantine, and generic per-origin page-fetch pressure controls.
  - No domain or `/blog/*` hardcoding; all numeric controls belong in admin-only project settings.

- Phase 12 added: Materialized Current Snapshot.
  - Variant 2 selected: a materialized project current snapshot represented as a synthetic session.
  - Raw sessions remain audit/debug artifacts; snapshot retention trims materialized delta tracking only.

- Phase 11 added: Crawl Quality Trust Gate.
- Phase 11 implemented and deployed on 2026-06-25:
  - Added admin project quality settings and canary URLs.
  - Added ClickHouse quality result/finding tables.
  - Added session quality API and session list badges.
  - Added evaluator scheduler that marks Daily Delta sessions as non-baseline warning sessions.
  - Deployed by rebuilding and recreating only the `app` container.

- Phase 8 added: Daily Delta Crawl with settings and internal-link discovery.

- Phase 22 completed and deployed on 2026-07-18:
  - Links > External now defaults to raw, materialized `source -> external target` graph edges, so Current Snapshots no longer show an empty view merely because external health checks were not copied.
  - Each row includes source page, external URL, anchor text, rel, tag, and DOM location; the existing Domains and URL Checks health views remain available.
  - Text filters now support a leading `!` exclusion operator without changing numeric filter syntax.
  - Added the direct single-session API path so reloading a Current/Baseline Snapshot deep link no longer leaves the app blank.
  - Production preflight confirmed zero active crawls; only `crawlobserver-app` was rebuilt/recreated and health plus browser verification passed.

## Quick Tasks Completed

| Date | Task | Result |
| --- | --- | --- |
| 2026-06-06 | Fix Google Search Console OAuth return flow and property switching | OAuth callback now returns to the project GSC tab, connected admins can change property without disconnecting, manual GSC fetches replace stale project data, and production health is OK after Docker Compose deploy. |
| 2026-06-06 | Add GSC page keyword drilldown | Added a project-scoped GSC page query API and Search Console Pages UI drilldown showing ranking queries per URL, sorted by impressions by default; production health and live DI endpoint verified. |
| 2026-06-08 | Make Resume parameter-aware | Resume now opens with saved crawl settings, requires explicit confirmation when changed settings would trigger a full recrawl, and backend supports confirmed full recrawls from original seed URLs. |
| 2026-06-12 | Check all images as page resources in UI and API | Static and JS-rendered image src/srcset/lazy references are extracted as resource checks, Resources UI/API exposes `resource_type=image`, and Daily Delta inherits resource checking so future runs can detect missing images. |
| 2026-06-15 | Detect soft 404 errors and SPA SEO warnings generically | Added a generic page issues API/UI/report path: soft 404 signals are errors, repeated rendered/static metadata signals are warnings, and no project-specific URL/domain rules are used. |
| 2026-06-16 | Format GSC dates and expand timeline chart | GSC timeline axis and inspection dates now render as `YYYY-MM-DD`, and the timeline chart uses the full available block width. |
| 2026-06-25 | Internal PageRank footer-exclusion settings and manual recalc | Added footer link classification, project PageRank footer-inclusion setting, settings UI checkbox, and manual recalc wiring that applies the project setting. |

## Open Decisions

- Whether to build the Compose image on the server or push/pull a registry image later.

---
*Last updated: 2026-08-28 after Phase 25.2 production acceptance*

- Phase 13 added: PageRank Lab access, automatic PageRank recalculation after settings changes, and confirmed erroneous-page pruning.
- Phase 14 added: structured crawl stop reasons plus deploy guard to prevent silent crawl interruption during app restarts.
- Phase 14 implemented and deployed on 2026-06-27:
  - Manual and shutdown stops now store structured stop metadata.
  - Session APIs expose stop reason/message/time for UI and agents.
  - Session UI shows stopped-session reasons such as `interrupted by restart`.
  - Added fail-closed `deploy/restart-app-safe.sh`; production app was rebuilt and recreated with `--no-deps app`.
- Phase 15 implemented and deployed on 2026-06-30:
  - PageRank Lab now supports add-link, remove-link, and combined simulations.
  - Removal requests are validated against existing `source -> target` pairs before async simulation starts.
  - Missing removal links return a clear API/UI error instead of being ignored.
  - Production was rebuilt and restarted with the app-only safe restart guard; health check passed.
- Phase 16 implemented on 2026-07-01:
  - Daily Delta sessions now store candidate-plan metadata in saved crawl config.
  - Quality gates can block tiny launched candidate plans, tiny sitemap candidate sets, and incomplete launched-candidate coverage.
  - Project Quality settings expose the new candidate-plan thresholds.
- Phase 17 implemented and deployed on 2026-07-01:
  - PageRank Lab will prioritize directly affected pages above the broader affected-page table.
  - Simulation results will use server-side filtering/sorting/pagination and the shared resizable DataTable UI.
- Phase 18 added: Daily Delta candidate source visibility and orphan 404 cleanup.
  - Current problem: Daily Delta can directly recrawl sitemap/problem/stale/manual/discovered candidates, so a 404 may have no internal inlinks even though it keeps appearing in current snapshot views.
  - Desired behavior: show per-URL candidate source, distinguish orphan/problem candidates from linked site errors, and allow admin-confirmed cleanup of stale orphan 404s without deleting raw sessions.
- Phase 18 implemented and deployed on 2026-07-12:
  - Daily Delta stores launched URL candidate sources in session config.
  - Pages API/UI exposes candidate-source context and labels 404 URLs with no sitemap membership and no internal inlinks as orphan/problem candidates.
  - Project Settings exposes orphan 404 retention days, cleanup preview, and confirmed cleanup for the current snapshot.
  - Astrogen project settings now exclude `/cdn-cgi/`.
  - Production was rebuilt on the server and restarted through the app-only safe restart guard; health checks passed.
- Phase 19 added and implemented locally on 2026-07-13:
  - Investigated Astrogen current snapshot false-looking 500 statuses with production read-only checks.
  - Found that Daily Delta runs recorded real upstream 500 responses during crawl windows, then promoted those transient failures because 5xx spikes were not part of the trust gate.
  - Added configurable 5xx growth and Daily Delta 5xx promotion gates.
  - Fixed report drilldowns to use explicit status-code ranges for 2xx/3xx/4xx/5xx navigation.
  - Verified frontend tests/lint/build and targeted Go quality tests locally.
  - Production has not been changed; data recovery requires explicit backup/dry-run approval before mutation.
