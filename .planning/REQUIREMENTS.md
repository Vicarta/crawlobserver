# Requirements: CrawlObserver Server Deployment

**Defined:** 2026-05-29
**Core Value:** A server operator can deploy, start, secure, verify, and recover CrawlObserver without guessing critical runtime steps.

## v1 Requirements

### Deployment

- [ ] **DEPLOY-01**: Server has a documented directory layout for binary, config, data, logs, and backups.
- [ ] **DEPLOY-02**: Server has repeatable Docker Compose steps to build and run the `crawlobserver` app image.
- [ ] **DEPLOY-03**: ClickHouse runs in Docker Compose with persistent storage and no public host-published ports.
- [ ] **DEPLOY-04**: Application can run `migrate` against the configured ClickHouse instance.
- [ ] **DEPLOY-05**: Application can start `serve` and expose the UI/API through the selected access layer.

### Security

- [ ] **SEC-01**: Production Basic Auth credentials are unique and stored outside git.
- [ ] **SEC-02**: GitHub access uses a dedicated passphrase-protected SSH key.
- [ ] **SEC-03**: ClickHouse is not publicly exposed with empty/default credentials.
- [ ] **SEC-04**: Service process runs as a dedicated non-root user.
- [ ] **SEC-05**: Server access path is Tailscale-only; no public UI/API or public reverse proxy is configured.

### Operations

- [ ] **OPS-01**: Service restart procedure is documented and verified.
- [ ] **OPS-02**: Logs can be inspected with documented commands.
- [ ] **OPS-03**: Backup and restore path for ClickHouse data and config is documented.
- [ ] **OPS-04**: Upgrade and rollback procedure is documented.
- [ ] **OPS-05**: Resource limits and crawl safety defaults are documented for first production use.

### Verification

- [ ] **VERIFY-01**: Build/test commands are recorded and pass locally before server rollout.
- [ ] **VERIFY-02**: Server smoke check confirms UI, API health, migration, and persistence after restart.
- [ ] **VERIFY-03**: Git remote can use the dedicated SSH identity after public key is added to GitHub.

## v2 Requirements

### Packaging

- **PKG-01**: Add production application Dockerfile.
- **PKG-02**: Add full app plus ClickHouse Compose stack.
- **PKG-03**: Add automated release artifact deployment.

### Observability

- **OBS-01**: Add structured health endpoint if current API surface is insufficient.
- **OBS-02**: Add lightweight metrics/runbook integration.

### Project Access Control

- **SEC-06**: Project-scoped API keys must not disclose unrelated projects through `GET /api/projects`.
- **SEC-07**: The application supports local non-admin users with password authentication and persistent sessions.
- **SEC-08**: Non-admin users can access only projects explicitly assigned to them and cannot perform admin-only operations.
- **VERIFY-04**: Access control tests cover project list filtering, session access denial, and admin-only user management.

### Selected-row CSV Export

- **CSV-23-01**: Every existing row-selectable table offers selected-row CSV export while selection is non-empty.
- **CSV-23-02**: CSV export contains only explicitly selected rows, including selections retained across pagination.
- **CSV-23-03**: Pages, Sessions, and Interlinking Opportunities use explicit business-field CSV projections.
- **CSV-23-04**: Existing permissions and selection-dependent workflows remain unchanged.
- **CSV-23-05**: The Pages export action appears immediately to the right of Clear; other selectable tables use the same command treatment.
- **CSV-23-06**: Selection uses stable row identity and cannot remap to another row after pagination.
- **CSV-23-07**: Automated and production browser verification covers selected-row output and affected workflows.
- **CSV-23-08**: Production rollout backs up first and restarts only the app service after active-crawl preflight.

### Trustworthy Sitemap Availability And Lastmod Validation

- **SITEMAP-26-01**: Sitemap reports expose complete, partial, unavailable, or unknown observation trust and never present partial evidence as complete coverage.
- **SITEMAP-26-02**: Sitemap URL comparison preserves raw evidence and records exact/decoded/normalized/invalid/legacy identity provenance.
- **AVAIL-26-01**: Every observed sitemap URL belongs to exactly one of present, missing 404/410, redirect, error, or not-crawled, and bucket counts reconcile to the distinct observed total.
- **AVAIL-26-02**: Successful indexable canonical HTML pages absent from sitemap are separated from intentionally excluded crawl-only pages.
- **DATE-26-01**: Raw sitemap lastmod is retained and classified as comparable, missing, invalid, or future using precision-aware W3C parsing.
- **DATE-26-02**: Page modification date source is retained and comparable pages report same-date, sitemap-newer, or page-newer states without fabricating unavailable values.
- **API-26-01**: Authenticated sitemap summary and drilldown APIs support server-side filters, sorting, pagination totals, status/final outcome, errors, source sitemap, and date comparison while preserving legacy consumers.
- **UI-26-01**: Reports > Sitemaps exposes availability and lastmod summaries, trust warnings, dense drilldowns, URL actions, and filtered CSV export.
- **COMPAT-26-01**: Raw sessions, Current Snapshot trust/promotion, authorization, historical evidence, and legacy routes/fields remain compatible; no website or crawl data is automatically changed.
- **TEST-26-01**: Automated coverage includes storage, crawler, API, UI, export, compatibility, partial/legacy, and browser scenarios.
- **DEPLOY-26-01**: ProductFeatures, API documentation, changelog, and runbook are updated; rollout is app-only after active-crawl and health gates.

### PageRank Quality Evidence Consistency

- **PRQ-251-01**: Terminal completed state is published only after durable finalized PageRank evidence; PageRank failure is explicit and fails quality closed.
- **PRQ-251-02**: PageRank reports and PageRank-based quality metrics use the same eligible-page predicate and evidence revision.
- **PRQ-251-03**: PageRank attempts persist typed revision, state, source, algorithm/options identity, eligible/positive/zero counts, timestamps, and failure evidence.
- **QUAL-251-01**: Quality results and findings are isolated by a deterministic evaluation revision that references one exact PageRank evidence revision.
- **QUAL-251-02**: Scheduler replay and restart recovery replace stale quality facts whenever evidence or rules change, including deterioration and improvement.
- **SNAP-251-01**: Current Snapshot promotion requires trusted quality evaluated from the current finalized PageRank evidence revision.
- **API-251-01**: Admins can idempotently re-evaluate an authorized terminal session without a new crawl or PageRank recompute, and GET returns durable provenance.
- **UI-251-01**: Quality details show evaluation/evidence provenance, stale state, and an admin-only re-evaluate command.
- **TEST-251-01**: Regression coverage spans ClickHouse visibility, lifecycle ordering, shared counts, JS/non-JS, concurrent evaluation, scheduler/restart, API, and UI.
- **DEPLOY-251-01**: ProductFeatures and rollout evidence are updated; deployment is app-only and blocked by active crawls.

### Deferred Authorization And External API Hardening

- **AUTH-27-01**: Project read-only credentials cannot trigger resource reparse, custom tests, extractions, or interlinking simulation.
- **AUTH-27-02**: Global rulesets, extractor sets, logs, and backups have explicit system/project scope and negative authorization coverage.
- **AUTH-27-03**: The Agency Growth Core `CRAWLOBSERVER_API_KEY` remains an intentional agency-wide full-access master key governed by finite router capabilities, approvals, audit ledger, and server-side binding.
- **TEST-27-01**: A negative authorization matrix covers every work-triggering and system-wide endpoint.
- **EXTAPI-28-01**: External agent contracts use versioned OpenAPI/JSON Schema for quality, Current Snapshot, and evidence endpoints.
- **EXTAPI-28-02**: External projections expose sanitized evidence without leaking internal credentials or unrelated project data.
- **TEST-28-01**: Schema compatibility and sanitized projection tests protect external consumers.

## Out of Scope

| Feature | Reason |
|---------|--------|
| Kubernetes | More operational surface than needed for first server deployment |
| Multi-node ClickHouse | Single-server rollout first |
| Public SaaS tenancy controls | Current request is deployment and startup of this project, not SaaS productization |

## Traceability

| Requirement | Phase | Status |
|-------------|-------|--------|
| DEPLOY-01 | Phase 1 | Pending |
| DEPLOY-02 | Phase 1 | Pending |
| DEPLOY-03 | Phase 1 | Pending |
| DEPLOY-04 | Phase 1 | Pending |
| DEPLOY-05 | Phase 1 | Pending |
| SEC-01 | Phase 2 | Pending |
| SEC-02 | Phase 1 | Pending |
| SEC-03 | Phase 2 | Pending |
| SEC-04 | Phase 2 | Pending |
| SEC-05 | Phase 1 | Pending |
| OPS-01 | Phase 3 | Pending |
| OPS-02 | Phase 3 | Pending |
| OPS-03 | Phase 3 | Pending |
| OPS-04 | Phase 3 | Pending |
| OPS-05 | Phase 2 | Pending |
| VERIFY-01 | Phase 1 | Pending |
| VERIFY-02 | Phase 1 | Pending |
| VERIFY-03 | Phase 1 | Pending |
| SEC-06 | Phase 5 | Done |
| SEC-07 | Phase 5 | Done |
| SEC-08 | Phase 5 | Done |
| VERIFY-04 | Phase 5 | Done |
| CSV-23-01 | Phase 23 | Done |
| CSV-23-02 | Phase 23 | Done |
| CSV-23-03 | Phase 23 | Done |
| CSV-23-04 | Phase 23 | Done |
| CSV-23-05 | Phase 23 | Done |
| CSV-23-06 | Phase 23 | Done |
| CSV-23-07 | Phase 23 | Done |
| CSV-23-08 | Phase 23 | Done |
| SITEMAP-26-01 | Phase 26 | Planned |
| SITEMAP-26-02 | Phase 26 | Planned |
| AVAIL-26-01 | Phase 26 | Planned |
| AVAIL-26-02 | Phase 26 | Planned |
| DATE-26-01 | Phase 26 | Planned |
| DATE-26-02 | Phase 26 | Planned |
| API-26-01 | Phase 26 | Planned |
| UI-26-01 | Phase 26 | Planned |
| COMPAT-26-01 | Phase 26 | Planned |
| TEST-26-01 | Phase 26 | Planned |
| DEPLOY-26-01 | Phase 26 | Planned |
| PRQ-251-01 | Phase 25.1 | Planned |
| PRQ-251-02 | Phase 25.1 | Planned |
| PRQ-251-03 | Phase 25.1 | Planned |
| QUAL-251-01 | Phase 25.1 | Planned |
| QUAL-251-02 | Phase 25.1 | Planned |
| SNAP-251-01 | Phase 25.1 | Planned |
| API-251-01 | Phase 25.1 | Planned |
| UI-251-01 | Phase 25.1 | Planned |
| TEST-251-01 | Phase 25.1 | Planned |
| DEPLOY-251-01 | Phase 25.1 | Planned |
| AUTH-27-01 | Phase 27 | Deferred |
| AUTH-27-02 | Phase 27 | Deferred |
| AUTH-27-03 | Phase 27 | Deferred |
| TEST-27-01 | Phase 27 | Deferred |
| EXTAPI-28-01 | Phase 28 | Deferred |
| EXTAPI-28-02 | Phase 28 | Deferred |
| TEST-28-01 | Phase 28 | Deferred |

**Coverage:**
- v1 requirements: 22 total
- Mapped to phases: 22
- Unmapped: 0

---
*Requirements defined: 2026-05-29*
*Last updated: 2026-08-08 for urgent PageRank quality evidence consistency planning*
