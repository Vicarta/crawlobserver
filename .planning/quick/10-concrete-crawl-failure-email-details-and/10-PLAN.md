# Quick Task 10: Concrete Crawl Failure Email Details and Page URLs

## Goal

Administrators can understand a crawl alert directly from its text or HTML
body: concrete execution causes, observed page failures, and identifiable full
page URLs. The session link remains supplementary.

## Scope And Estimate

- Expect approximately 300-500 changed lines in operational-email server,
  storage query/model, focused tests/mocks, and `ProductFeatures.md`.
- Read existing durable execution evidence first. Crawler/config changes are
  allowed only for the minimum missing execution-cause metadata (for example,
  lost buffered pages/links); no migration or new infrastructure.
- Preserve recipients, administrator access, existing per-URL new-error
  classes/comparison, event deduplication, receipts, and bounded retries.
- No unrelated fixes, live crawls, real email sends, data rewrites, FORCE
  restart, or stopping an active crawl.

## Must Haves

- A `crawl_failure` body includes a deterministic, bounded list of observed
  page errors with HTTP status or the stored fetch cause; execution causes are
  separate and never inferred solely from `completed_with_errors`.
- Persist terminal PageRank failures and buffer loss in the existing session
  config, because a graph-read timeout can occur before PageRank evidence is
  written. Read structured unexpected-stop reasons. Historical exact terminal
  crawler logs are the fallback source. Generic PageRank attempts, including
  manual recomputations, do not prove crawl causality and are not attributed.
  Missing causes are explicitly unavailable.
- Public HTTP(S) page URLs retain path and ordinary query information. Redact
  userinfo and credential-bearing query values, including URLs embedded in
  error text; unsafe/malformed schemes are omitted. Escape HTML and bound
  readable details with an explicit omitted-count indicator.
- Both text and HTML include the same details for both alert types. Only
  verified active administrators remain eligible; new-page-error detection and
  delivery behavior remain unchanged.
- A fixture-driven worker-to-fake-sender end-to-end test proves the rendered
  email contains concrete causes and exact public page paths without sending
  mail or creating a production crawl.
- Canonical product documentation, independent review, commit/push, safe
  app-only deployment, and read-only production health verification complete
  the task.

## Tasks

### 1. Read Concrete Failure Evidence

**Files:** `internal/storage/models.go`,
`internal/storage/clickhouse_operational_emails.go`, focused storage tests;
`internal/config/session_metadata.go`, crawler finalization/session code and
their tests only if an execution cause lacks durable evidence.

**Action:** Retain raw stored fetch-error text alongside the existing boolean
classification. Add the smallest session-page-error retrieval needed for
failure alerts; reuse it for new-error reads without changing the predecessor
comparison or HTTP/fetch classes. Read exact terminal crawler logs and existing
structured stop metadata. Persist only minimal PageRank-failure and buffer-loss
cause/count metadata in the existing session config, preserving other fields.
Keep ordering and detail-limit/count semantics deterministic.

**Verify:** Focused storage/config/crawler fixtures distinguish HTTP 404/500,
fetch errors, PageRank failure, buffer loss, unexpected stop, and missing
historical evidence; predecessor new-error comparison is unchanged.

**Done:** The notification worker can obtain concrete page and execution
evidence without a schema migration or guessing from terminal status.

### 2. Render Bounded Actionable Administrator Emails

**Files:** `internal/server/operational_email.go`, `internal/server/deps.go`,
existing server mock/test files, `ProductFeatures.md`.

**Action:** Populate failure-event details from task 1; retain separate
execution versus page labels and explicit unavailable fallback. Apply one
small sanitization/bounding path to both alert types, preserving public page
paths while redacting credentials. Render details consistently in text and
HTML, with truncation/omission stated. Keep the session link and current
recipient, event, receipt, retry, and classification contracts. Document
concrete details, bounds, and redaction in the canonical feature catalog.

**Verify:** A single targeted validation pass runs focused tests plus a
fixture-driven terminal-session -> event -> receipt -> fake sender test.
Inspect the rendered text/HTML for full page paths, HTTP/fetch cause,
execution-only failure, buffer loss, explicit unavailable history, bounded
overflow, escaped hostile markup, and absence of credentials. Existing
recipient/dedup/retry/new-error regression tests must pass.

**Done:** A failing crawl email itself explains its concrete known causes and
affected public pages; no real Resend message or live crawl is invoked.

### 3. Review, Release, And Verify App-Only Rollout

**Files:** Quick-task summary/verification and planning state; runtime source
only for review findings required by these acceptance criteria.

**Action:** Complete the prescribed independent gauntlet review and resolve
blocking findings without adding unrelated hardening. Commit and push the
release to the connected GitHub repository. Use the existing safe deployment
guard with `CHECK_ONLY` before build and again before app-only restart; never
set FORCE or stop crawls. If a crawl is active, wait for it to finish and
recheck. Preserve the ClickHouse container and existing deployment rollback
pattern. Record the deployed commit and evidence.

**Verify:** Read-only application health and ClickHouse continuity/health
checks pass after app-only deployment. Confirm the running release corresponds
to the pushed change without creating a crawl or sending email.

**Done:** The reviewed change is committed, pushed, deployed, and documented;
production application and existing ClickHouse remain healthy.
