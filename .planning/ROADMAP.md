# Roadmap: CrawlObserver Server Deployment

## Phase 1: Baseline Server Startup

**Goal:** Produce and verify the minimum repeatable Docker Compose path to run CrawlObserver on the server.

**Covers:** DEPLOY-01, DEPLOY-02, DEPLOY-03, DEPLOY-04, DEPLOY-05, SEC-02, SEC-05, VERIFY-01, VERIFY-02, VERIFY-03

**Deliverables:**
- Server directory layout.
- Dockerfile and Compose stack for app plus ClickHouse.
- ClickHouse startup procedure without public host-published ports.
- App migration and serve procedure through Compose.
- GitHub SSH public key instructions and remote alias.
- Server SSH alias and key path.
- Smoke test checklist.

## Phase 2: Secure Runtime

**Goal:** Convert the baseline startup path into a secure Tailscale-only long-running service.

**Covers:** SEC-01, SEC-03, SEC-04, OPS-05

**Deliverables:**
- Non-root service user plan.
- Production config policy.
- Tailscale Serve setup notes.
- ClickHouse exposure and credentials policy.
- Resource and crawler safety defaults.

## Phase 3: Operations Runbook

**Goal:** Make the deployment maintainable after initial launch.

**Covers:** OPS-01, OPS-02, OPS-03, OPS-04

**Deliverables:**
- Restart and status commands.
- Log inspection commands.
- Backup and restore procedure.
- Upgrade and rollback procedure.

## Phase 4: Packaging Improvements

**Goal:** Reduce manual server steps after the first deployment is proven.

**Covers:** PKG-01, PKG-02, PKG-03, OBS-01, OBS-02

**Deliverables:**
- Production Dockerfile or artifact install path.
- Optional full Compose stack.
- Health/metrics improvements if needed.
- Automated deployment candidate.

## Phase 5: Project-Scoped User Access

**Goal:** Add upstream-ready access control so non-admin users can log in and view only assigned projects, while existing Basic Auth and API key workflows remain compatible.

**Covers:** SEC-06, SEC-07, SEC-08, VERIFY-04

**Deliverables:**
- Project API keys only list their own project.
- User, session, and user-project persistence in the existing SQLite auth store.
- Cookie-based UI login/logout for local users.
- Admin-only user management endpoints and UI controls.
- Project-scoped read access enforcement across project/session views.
- Tests for project filtering and authz boundaries.

## Phase 6: Page Rescan and Table Ergonomics

**Goal:** Let operators rescan selected pages in an existing session and make dense page tables easier to inspect.

**Covers:** UX-01, UX-02, CRAWL-01, VERIFY-05

**Deliverables:**
- Admin-only selected-page rescan endpoint for existing sessions.
- Pages table row selection and compact bulk rescan action.
- Targeted refetch that preserves existing depth and Internal PageRank by default.
- Hover-readable clipped URL/title cells.
- Drag-resizable table columns with persisted widths.

### Phase 8: Daily Delta Crawl with settings and internal-link discovery

**Goal:** Add a project-level Daily Delta Crawl mode that checks candidate URLs, fully crawls only new/changed/problem pages, and follows internal links from changed/new pages with explicit limits.

**Requirements:**
- Persist all delta crawl settings per project before enabling scheduling.
- Keep Resume and Full Recrawl behavior separate from Daily Delta Crawl.
- Never delete or overwrite the previous crawl snapshot before a delta run succeeds.
- Use sitemap, GSC, problem pages, stale pages, and manual/API queued URLs as candidate sources.
- Discover new in-scope internal URLs from newly crawled or changed pages, bounded by configurable limits.
- Preserve existing project/user authorization boundaries; only admins may update settings or launch delta runs.
- Report candidate counts and launched session IDs to the UI/API.
**Depends on:** Phase 6
**Plans:** 1 plan

Plans:
- [ ] Backend and UI implementation for delta crawl settings, candidate discovery, and safe launch

## Phase 11: Crawl Quality Trust Gate

**Goal:** Add project-level data-quality trust gates for completed crawl sessions, with admin-configurable anomaly thresholds and canary pages.

**Requirements**:
- Evaluate full crawl sessions against the latest trusted full baseline for the same project.
- Keep Daily Delta sessions from becoming coverage/PageRank baselines.
- Store session quality status, score, findings, and baseline references for API/UI/agents.
- Add all numeric thresholds and canary URL checks to admin-only project settings.
- Expose session quality through API so external agents can refuse untrusted crawl data.
**Depends on:** Phase 8
**Plans:** 1 plan

Plans:
- [x] Backend/API/UI implementation for crawl quality settings, canaries, evaluator, and trust status

## Phase 12: Materialized Current Snapshot

**Goal:** Maintain a project-level current site snapshot that folds trusted Daily Delta sessions into a materialized full-site view while preserving raw sessions for audit/debug.

**Requirements**:
- Promote only trusted full crawls and trusted Daily Delta sessions into the materialized current snapshot.
- Keep failed or untrusted delta sessions out of the current snapshot.
- Store one baseline snapshot plus the latest configurable number of promoted deltas per project.
- Fold deltas into a new baseline on a separate configurable day interval and trim old delta tracking without deleting raw crawl sessions.
- Recompute Internal PageRank on the materialized current graph after every trusted delta promotion.
- Expose current snapshot metadata and a session-compatible current snapshot ID through API/UI.
- Preserve existing raw session APIs and avoid PageRank footer exclusion settings owned by Worker B.
**Depends on:** Phase 11
**Plans:** 1 plan

Plans:
- [ ] Backend/storage/API/UI vertical slice for current snapshot promotion and settings

## Phase 18: Daily Delta candidate source visibility and orphan 404 cleanup

**Goal:** Make 404/problem URLs explainable by showing where Daily Delta candidates came from, separating orphan/problem candidates from real linked site errors, and allowing safe cleanup of stale orphan 404s from current snapshots.

**Requirements**:
- Store per-URL candidate source metadata for Daily Delta launched URLs: sitemap, problem_pages, stale_pages, manual_queue, discovered.
- Expose candidate source through API for pages/current snapshot views so external agents can distinguish linked site errors from direct candidate recrawls.
- In the UI, label 404 URLs with no internal inlinks and no sitemap membership as orphan/problem candidates instead of ordinary linked site 404s.
- Add an admin cleanup path that removes 404 URLs from the current snapshot only when they have no internal inlinks, are not in the current sitemap, and are older than a configurable number of days.
- Cleanup must require confirmation, update graph-derived metrics including Internal PageRank, and keep raw crawl sessions available for audit.
- Add project exclude configuration support for technical paths such as `/cdn-cgi/`, applying it to future crawls and Daily Delta candidates without hardcoding any project-specific URLs.
- Preserve existing project/user authorization boundaries and keep delete/cleanup actions admin-only.
**Depends on:** Phase 17
**Plans:** 1 plan

Plans:
- [ ] Backend/API/UI implementation for candidate source tracking, orphan 404 labeling, safe cleanup, and project excludes

## Phase 19: Astrogen false 500 status investigation and status trust hardening

**Goal:** Systematically investigate why Astrogen pages are reported as 500 in CrawlObserver while opening normally in a browser, separate symptom from root cause and data damage, then harden status attribution, recovery, and monitoring without changing production until explicitly approved.

**Requirements**:
- Build a timeline and blast radius from UI/API/DB/jobs/logs/Sentry and the live Astrogen site.
- Identify the source of truth for status code, final URL, fetch error, retry state, candidate source, current snapshot membership, and UI badges.
- Form and attempt to falsify at least three root-cause hypotheses before patching.
- Distinguish crawler/runtime defects, storage/current-snapshot defects, UI/API interpretation defects, and damaged historical data.
- Plan code fixes separately from data recovery; all mass data changes require dry-run counts, backup/rollback, and stop criteria.
- Add regression, integration, state-transition, failure-path, and browser tests for the confirmed defect class.
- Add an invariant or monitor that detects recurrence without relying on a customer report.
- Verify in dev/local only and record a changelog; production deploy/recovery requires a separate explicit approval.
**Depends on:** Phase 18
**Plans:** 1 plan

Plans:
- [ ] Investigation, hardening patch plan, and recovery dry-run for false 500 status data

## Phase 20: Strict 5xx trust gate and origin-safe crawl pressure control

**Goal:** Prevent transient origin-side 5xx bursts from corrupting Current Snapshot and prevent CrawlObserver page-fetch concurrency from creating avoidable burst load on a shared origin, without hardcoding domains or URL paths.

**Requirements**:
- **TRUST-20-01:** Add a fail-closed session-level 5xx quality gate for full crawls and Daily Delta runs, using admin-configurable project thresholds and preserving the previous Current Snapshot when blocked.
- **TRUST-20-02:** Add row-level protection for a previously healthy current URL so one transient Daily Delta `2xx -> 5xx` observation remains visible for audit but cannot immediately replace the known-good current row.
- **TRUST-20-03:** Require configurable independent confirmation before a quarantined 5xx transition may enter Current Snapshot; successful rechecks clear the pending failure, and promotion publication is copy-on-write/idempotent.
- **PRESSURE-20-01:** Enforce a configurable maximum number of concurrent HTML/page requests per origin, shared by all workers in the crawl session and independent of URL path.
- **PRESSURE-20-02:** Add adaptive origin backoff/cooldown for 5xx and timeout bursts, while preserving bounded retries and crawl cancellation.
- **RAW-20-01:** Keep raw session results and confirmed real 5xx visible; never rewrite an observed upstream 5xx as 200.
- **CONFIG-20-01:** Expose all numeric gate, confirmation, concurrency, delay, and cooldown controls in admin-only project settings with validation and safe defaults.
- **OBS-20-01:** Expose blocked promotion, quarantined transitions, and origin throttling evidence through API/UI/logs so agents and operators can explain the result.
- **TEST-20-01:** Add regression, integration, state-transition, concurrency/race, failure-path, and browser tests, including current-snapshot immutability after a rejected run.
- **GENERIC-20-01:** Keep implementation generic: no Astrogen domain rule and no `/blog/*` special case.
**Depends on:** Phase 19
**Plans:** 4 plans

Plans:
- [ ] 20-00: Wave 0 deterministic backend and browser test infrastructure
- [ ] 20-01: Project settings and origin-safe primary-page pressure controller
- [ ] 20-02: Strict 5xx gate, durable row quarantine, and copy-on-write promotion
- [ ] 20-03: Status API/UI, observability, agent contract, and rollout verification

### Phase 21: Fresh sitemap synchronization for Daily Delta

**Goal:** Build every sitemap-derived Daily Delta candidate plan from the site's currently served sitemap rather than a historical snapshot copy, while preserving an auditable fallback path and preventing stale sitemap URLs from being reintroduced.

**Requirements**:
- **SITEMAP-21-01:** Fetch the current sitemap set before every sitemap-enabled Preview, Run-now, and scheduled Daily Delta plan.
- **SITEMAP-21-02:** Preserve raw `<loc>` evidence and apply project URL policy without deleting URL path content.
- **SITEMAP-21-03:** Use only a successful fresh sitemap as the sitemap candidate source; historical membership cannot reintroduce a removed URL.
- **SITEMAP-21-04:** Default refresh failure to an explicitly visible skip; allow only an admin-configured, visibly labelled snapshot fallback.
- **SITEMAP-21-05:** Persist session provenance, candidate sources, sitemap counts, warnings, and raw evidence for API/UI diagnosis.
- **SITEMAP-21-06:** Feed fresh sitemap counts and non-fresh states into the Delta quality gate without masquerading historical counts as current data.
- **SITEMAP-21-07:** Update materialized current-snapshot sitemap membership only after trusted fresh promotion; do not mass-delete historical data or pages.
- **SITEMAP-21-08:** Add regression, integration, state-transition, failure-path, API, and browser coverage.
- **SITEMAP-21-09:** Deploy only the app container after backup, active-crawl preflight, verification, and two non-mutating production previews.
**Depends on:** Phase 20
**Plans:** 4 plans

Plans:
- [x] 21-01: Typed sitemap refresh, configured roots, and raw evidence preservation
- [x] 21-02: Fresh Delta candidates, explicit failure policy, and quality gates
- [x] 21-03: Durable observation persistence and trusted snapshot promotion
- [x] 21-04: API/UI evidence, browser tests, and app-only deployment dry-runs

### Phase 22: Current Snapshot external links and source visibility

**Goal:** Make the External links view show the materialized graph's actual outbound link records, including each internal source page, while retaining the separate external-health views.

**Requirements**:
- **EXT-22-01:** Open Links > External on a Current Snapshot directly into the raw outbound-link view backed by `links`, not by `external_link_checks`.
- **EXT-22-02:** Show one row per source-to-external-target link with Source, External URL, Anchor, Rel, Tag, and Location; Source must navigate to the internal page detail.
- **EXT-22-03:** Preserve the existing domain and URL health-check views, label them explicitly as checks, and retain the existing Source column there.
- **EXT-22-04:** Preserve server-side filtering, sorting, pagination, CSV export, horizontal scrolling, and persisted resizable column widths for the raw external-link table.
- **EXT-22-05:** Support text exclusion with a documented `!value` syntax, for example `!/cdn-cgi/`, without changing numeric filter semantics.
- **EXT-22-06:** Add regression coverage proving a Current Snapshot exposes copied raw external links even where check rows are absent, and prove negated filters do not regress ordinary matching.
- **EXT-22-07:** Deploy only the `app` Compose service after backup, active-crawl preflight, health verification, and a browser-level UI check.
**Depends on:** Phase 21
**Plans:** 3 plans

Plans:
- [x] 22-01: Trace the data contract and add source-aware raw external links UI/API coverage
- [x] 22-02: Add negated text filters with regression coverage and UI affordance
- [x] 22-03: Validate, deploy the app only, and verify the Current Snapshot in production

### Phase 23: Export selected rows to CSV across selectable tables

**Goal:** Let users export exactly the rows they have selected in every table that exposes row-selection checkboxes, using a consistent CSV action and safe field mapping.

**Requirements**:
- **CSV-23-01:** Every table with row-selection checkboxes exposes an `Export selected CSV` action whenever at least one row is selected.
- **CSV-23-02:** Export only explicitly selected rows, including selections retained while paging, and never substitute all filtered or all loaded rows.
- **CSV-23-03:** Pages All/HTML exports the visible page business fields; Crawl Sessions exports session summary fields; Interlinking Opportunities exports the visible opportunity fields.
- **CSV-23-04:** Preserve existing selection-dependent actions, permissions, sorting, filtering, pagination, rescan/delete/assign/simulate workflows, and CSV UTF-8/escaping behavior.
- **CSV-23-05:** Place the new Pages action immediately to the right of `Clear`, and use a consistent download icon plus text label in the other selectable-table action areas.
- **CSV-23-06:** Use stable row identities so selected rows cannot silently change meaning after pagination or category changes.
- **CSV-23-07:** Add regression tests for selected-row projection, CSV escaping/order, and selection identity; verify the three affected tables in a production browser.
- **CSV-23-08:** Deploy only the `app` Compose service after backup, active-crawl preflight, health verification, and browser checks.
**Covers:** CSV-23-01, CSV-23-02, CSV-23-03, CSV-23-04, CSV-23-05, CSV-23-06, CSV-23-07, CSV-23-08
**Depends on:** Phase 22
**Plans:** 2 plans

Plans:
- [x] 23-01: Shared selected-row CSV contract and selectable-table implementations
- [x] 23-02: Regression verification, app-only deployment, and production browser checks

### Phase 24: Origin-safe JS rendering and issue provenance

**Goal:** Prevent same-origin Chrome concurrency from producing partially
rendered SEO metadata, and make the Issues table distinguish server HTML from
the rendered DOM.

**Requirements**:
- **RENDER-24-01:** Serialize top-level JS renders per origin while retaining
  cross-origin parallelism.
- **RENDER-24-02:** Honor crawl cancellation while waiting for an origin render
  slot and release every slot on all return paths.
- **ISSUE-24-01:** Derive static duplicate warnings only from persisted
  `static_*` fields.
- **ISSUE-24-02:** Show explicit server-HTML and rendered evidence in Issues.
- **TEXT-24-01:** Count UTF-8 title and description lengths as Unicode
  characters, not bytes.
- **TEST-24-01:** Cover concurrency, cancellation, provenance, Unicode, frontend,
  and browser paths.
- **DEPLOY-24-01:** Deploy only the app after active-crawl preflight and retain a
  rollback image.
**Depends on:** Phase 23
**Plans:** 1 plan

Plans:
- [x] 24-01: Origin gate, issue provenance, Unicode lengths, regression checks,
  and app-only deployment

### Phase 25: URL discovery provenance in page detail

**Goal:** Make every URL Detail view explain why the URL exists in the crawl,
including concrete internal referrers, sitemap evidence, seed/candidate origin,
and an explicit unavailable state when historical provenance was not retained.

**Requirements**:
- **PROV-25-01:** Add a stable page-detail discovery contract that reports the
  primary source plus all retained evidence: `found_on`, internal inlinks,
  sitemap source/raw loc, seed status, and Daily Delta candidate sources.
- **PROV-25-02:** Reuse the same source precedence and labels as Pages so Page
  Detail cannot contradict the Pages table.
- **PROV-25-03:** Show a compact `URL discovered from` block immediately before
  GSC Ranking Keywords, with clickable source URLs and anchor text.
- **PROV-25-04:** Always render the block, including explicit `seed` and
  `provenance unavailable` states; never infer a source that is not retained.
- **PROV-25-05:** Preserve authorization, pagination, Current Snapshot behavior,
  raw crawl history, and existing inbound/outbound link views.
- **TEST-25-01:** Cover source precedence, sitemap/seed/candidate/unknown cases,
  page-detail API serialization, frontend rendering, and production smoke checks.
- **DEPLOY-25-01:** Deploy only the app after automated verification and an
  active-crawl safety preflight; do not mutate crawl sessions or ClickHouse data.
**Depends on:** Phase 24
**Plans:** 1 plan

Plans:
- [x] 25-01: Discovery evidence contract, URL Detail block, regression tests,
  and app-only rollout

### Phase 25.1: PageRank quality evidence consistency (INSERTED)

**Goal:** Make terminal crawl state, Internal PageRank reports, Quality Gate,
and Current Snapshot promotion consume the same durable, versioned PageRank
evidence so stale or pre-finalization quality facts cannot be published.

**Requirements**:
- **PRQ-251-01:** A raw crawl cannot publish `completed` before synchronous
  PageRank mutation, verification, and finalized evidence succeed; failure is
  explicit and quality fails closed.
- **PRQ-251-02:** PageRank reports and `pagerank_zero_top_pages` use one shared
  eligible-page predicate and evidence revision.
- **PRQ-251-03:** Every computation or adopted legacy observation has a durable
  typed evidence revision with status, source, algorithm/options identity,
  eligible/positive/zero counts, timestamps, and failure detail.
- **QUAL-251-01:** Quality results are versioned by evaluation revision and
  reference exact PageRank evidence; append-only evaluations/findings and a
  separately published current pointer prevent concurrent or repeated
  evaluations from mixing.
- **QUAL-251-02:** Scheduler replay and restart recovery replace stale quality
  deterministically whenever evidence or rules revision changes, in either
  direction.
- **SNAP-251-01:** Current Snapshot initialization/promotion accepts only a
  trusted quality result evaluated from the currently finalized PageRank
  evidence revision, records the bound evaluation/evidence/rules/baseline
  revisions, and retries promotion independently without duplicating quality.
- **SNAP-251-02:** Historical quality uses the latest trusted full crawl
  strictly older by `(started_at, session_id)`. Current Snapshot persists a
  full-crawl source plus latest-content watermark; older full/delta replay is
  recorded as `superseded` and cannot mutate or move the authoritative pointer.
- **API-251-01:** Provide an admin-only, session-authorized, synchronous and
  idempotent quality re-evaluation endpoint with durable GET readback; it must
  repair already-completed sessions without a new crawl or PageRank recompute.
- **UI-251-01:** Quality details expose evaluated time, evaluation revision,
  evidence revision/source/status/counts, stale state, and the admin re-evaluate
  action without hiding the current finding history.
- **TEST-251-01:** Cover initial finalization ordering, mutation visibility,
  shared predicates, JS/non-JS sessions, stale positive/negative replacement,
  scheduler replay/restart fixed-point convergence, monotonic full/delta
  promotion across fold/restart, API authorization/idempotency, and UI provenance.
- **DEPLOY-251-01:** Update ProductFeatures, enforce a shared-state single-writer
  lock, and rollout only the app through the active-crawl safe gate without
  overlapping app and migration writers; then verify the exact Gerus session
  and ClickHouse continuity without rewriting historical page rows.

**Covers:** PRQ-251-01, PRQ-251-02, PRQ-251-03, QUAL-251-01, QUAL-251-02,
SNAP-251-01, SNAP-251-02, API-251-01, UI-251-01, TEST-251-01, DEPLOY-251-01
**Depends on:** Phase 25
**Plans:** 3 plans

Plans:
- [ ] 25.1-01: Shared PageRank eligibility and durable evidence foundation
- [ ] 25.1-02: Fail-closed lifecycle, versioned quality, promotion, and API
- [ ] 25.1-03: Provenance UI, regression closure, and safe production rollout

### Phase 26: Trustworthy sitemap availability and lastmod validation

**Goal:** Make sitemap-versus-crawl reporting distinguish observed missing URLs
from redirects, fetch errors, and URLs that were never crawled, while validating
raw sitemap `<lastmod>` against auditable page-modification evidence without
overstating partial or legacy data.

**Requirements**:
- **SITEMAP-26-01:** Persist or derive explicit sitemap-observation trust state
  (`complete`, `partial`, `unavailable`, `unknown`) and show why a report is not
  complete; incomplete evidence must never be presented as full-site coverage.
- **SITEMAP-26-02:** Preserve raw `<loc>` and sitemap source while using one
  crawler-consistent normalized identity for comparison. Report exact,
  decoded, normalized, invalid, and legacy match provenance without silently
  rewriting the source value.
- **AVAIL-26-01:** Decompose the sitemap URL universe into mutually exclusive
  `present`, `missing_404_410`, `redirect`, `error`, and `not_crawled` buckets;
  their distinct URL counts must reconcile to the observed sitemap total.
- **AVAIL-26-02:** Report successful indexable canonical HTML pages absent from
  the sitemap separately from intentionally excluded/non-indexable crawl-only
  pages, so the actionable missing-from-sitemap count is not inflated.
- **DATE-26-01:** Preserve raw `<lastmod>`, parse supported W3C date/date-time
  forms, and classify missing, invalid, and future values with precision-aware
  semantics.
- **DATE-26-02:** Retain the source of `page_modified_at` and compare comparable
  present pages as same date, sitemap newer, or page newer; expose unavailable
  and not-comparable states rather than inventing a date.
- **API-26-01:** Provide backward-compatible summary and server-side drilldown
  APIs with filters, sorting, pagination totals, final redirect outcome, fetch
  error, sitemap source, date comparison, and trust metadata.
- **UI-26-01:** Upgrade Reports > Sitemaps with availability cards, observation
  warning state, lastmod comparison summary, dense drilldown table, URL actions,
  filtered CSV export, and clear loading/empty/error/partial states.
- **COMPAT-26-01:** Preserve raw sessions, Current Snapshot promotion rules,
  authorization, legacy coverage fields/routes, and historical evidence; do
  not auto-edit a website, sitemap, canonical, or crawl data.
- **TEST-26-01:** Cover classifier exhaustiveness, redirect terminal outcomes,
  normalized identity collisions, partial/legacy observations, date precision,
  API compatibility, UI filters/export, and desktop/mobile browser flows.
- **DEPLOY-26-01:** Update ProductFeatures/API/runbook documentation and deploy
  only the app after active-crawl preflight, backup, health checks, ClickHouse
  continuity verification, and a read-only production sitemap report smoke.

**Covers:** SITEMAP-26-01, SITEMAP-26-02, AVAIL-26-01, AVAIL-26-02,
DATE-26-01, DATE-26-02, API-26-01, UI-26-01, COMPAT-26-01, TEST-26-01,
DEPLOY-26-01
**Depends on:** Phase 21, Phase 24, Phase 25
**Plans:** 10 plans

Plans:
- [ ] 26-01: Playwright and browser-test bootstrap
- [ ] 26-02: Durable observation, identity, and migration foundation
- [ ] 26-03: Page dates, immutable eligibility, and outcome lineage
- [ ] 26-04: Normal and Daily Delta observation/disposition producers
- [ ] 26-05: Evidence lifecycle and Current Snapshot cohorts
- [ ] 26-06: Authoritative classifier and precision/trust matrix
- [ ] 26-07: Backward-compatible summary and dual-universe API
- [ ] 26-08: Trust-first Reports > Sitemaps summary UI
- [ ] 26-09: Dual drilldowns, filtered export, and browser coverage
- [ ] 26-10: Shadow verification, backup/restore, documentation, and app-only rollout

### Phase 27: Authorization scope and mutation hardening

**Goal:** Separate read-only project access from work-triggering operations and
system-wide administrative resources with a negative authorization matrix.

**Requirements:** AUTH-27-01, AUTH-27-02, AUTH-27-03, TEST-27-01
**Depends on:** Phase 25.1
**Plans:** 0 plans

Plans:
- [ ] TBD: Audit and harden reparse, tests, extractions, interlinking simulation,
  global rulesets/extractor sets/logs/backups without changing the Growth Core
  agency-wide master key contract.

### Phase 28: Versioned external API evidence contracts

**Goal:** Publish a versioned OpenAPI/JSON Schema and sanitized external
snapshot, quality, and evidence projections for agent consumers.

**Requirements:** EXTAPI-28-01, EXTAPI-28-02, TEST-28-01
**Depends on:** Phase 25.1, Phase 27
**Plans:** 0 plans

Plans:
- [ ] TBD: External guide coverage, schema versioning, sanitized projections,
  compatibility and contract tests.

---
*Last updated: 2026-08-07 for trustworthy sitemap availability and lastmod validation planning*

## Phase 13: PageRank Lab access, recalc, and page pruning

**Goal:** Make PageRank Lab project-accessible, keep PageRank fresh after settings saves, and allow confirmed removal of known-bad pages with metric recalculation.

**Requirements**:
- PageRank Lab is available to authenticated project users, not only admins.
- Project access checks still prevent cross-project visibility.
- Saving changed PageRank settings triggers current graph PageRank recalculation.
- Operators can delete selected erroneous pages after confirmation.
- Deletion updates graph-derived data, including Internal PageRank.
- Update status for SEObserver/crawlobserver releases is verified before applying any upstream update.
**Depends on:** Phase 12
**Plans:** 1 plan

Plans:
- [x] Backend/API/UI implementation for PageRank Lab access, settings-triggered recalculation, and page pruning

## Phase 14: Deployment-safe crawl interruption handling

**Goal:** Prevent deploy/restart operations from silently stopping active crawls, and make any unavoidable shutdown interruption visible in API/UI.

**Requirements**:
- Persist a machine-readable stop reason for stopped sessions.
- Distinguish manual stops from process shutdown/deploy restart interruptions.
- Expose stopped-session reason and message through session list/detail API responses.
- Show the interruption reason in session UI without requiring log inspection.
- Add a deploy/restart guard that checks running crawls before recreating the app container and blocks unless explicitly forced.
- Preserve the existing production rule: restart only the CrawlObserver app container, not the whole Compose stack.
**Depends on:** Phase 13
**Plans:** 1 plan

Plans:
- [x] Backend/UI/deploy guard implementation for structured crawl interruption handling

## Phase 15: PageRank Link Removal Simulation

**Goal:** Extend PageRank Lab so users can simulate removing existing links from a source page and see the projected Internal PageRank impact before editing the site.

**Requirements**:
- Accept proposed link additions and proposed existing-link removals in the PageRank simulation API.
- Validate removal requests before launching async simulation: every `source -> target` removal must exist in the selected session/current snapshot link table.
- Return a clear user-facing error listing missing removal links when requested links are not present.
- Apply removal to the PageRank graph by removing the internal dofollow edge when applicable and reducing the source outlink count used as PageRank dilution.
- Preserve existing add-link simulation behavior.
- Expose removal simulation controls in PageRank Lab UI against the project current snapshot.
**Depends on:** Phase 14
**Plans:** 1 plan

Plans:
- [x] Backend/API/UI implementation for PageRank link removal simulation

## Phase 16: Daily Delta Candidate-Plan Quality Gates

**Goal:** Make Daily Delta promotion depend on the quality of the planned candidate set, not only on how many pages happened to be crawled.

**Requirements**:
- Store candidate-plan metadata for each Daily Delta session, including total candidates, launched candidates, per-source counts, deferred count, and baseline session ID.
- Require planned launch candidates to be represented in the crawl output before a delta can update the current snapshot.
- Detect suspiciously tiny sitemap-derived candidate plans compared with the current/baseline sitemap universe.
- Keep all numeric thresholds configurable per project in the existing Quality settings UI.
- Preserve current snapshot safety: untrusted deltas must never be promoted.
- Add regression tests for candidate-plan metadata parsing and delta promotion gates.
**Depends on:** Phase 12
**Plans:** 1 plan

Plans:
- [x] Backend/API/UI implementation for candidate-plan quality metadata and gates

## Phase 17: PageRank Lab Focused Results Table

**Goal:** Make PageRank Lab simulation results prioritize the pages the operator is evaluating and use the same table interaction model as the rest of CrawlObserver.

**Requirements**:
- Show the direct affected page(s) above the full results table: add-link targets and remove-link source pages.
- Keep full simulation results server-paginated instead of loading all affected rows into the browser.
- Add URL search/filtering for affected pages.
- Support sortable columns by clicking headers.
- Support persisted resizable column widths using the shared table component.
- Add bottom pagination for result browsing.
- Preserve current-snapshot simulation behavior and removal-link validation.
**Depends on:** Phase 15
**Plans:** 1 plan

Plans:
- [x] Frontend/API implementation for focused PageRank Lab impact and interactive results table
