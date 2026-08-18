# CrawlObserver API Guide for External Agents

This document describes how an external agent should connect to the CrawlObserver API, authenticate safely, read crawl data, and perform common monitoring or SEO-analysis workflows.

Production base URL:

```text
https://crawlobserver.example.com/api
```

Do not use `http://0.0.0.0:8899/api` from an external agent. That is the internal bind address inside the service/container.

## Authentication

Use an API key whenever possible:

```http
X-API-Key: YOUR_API_KEY
```

Example:

```bash
curl -H "X-API-Key: $CRAWLOBSERVER_API_KEY" \
  https://crawlobserver.example.com/api/projects
```

Basic auth also exists for browser/admin access, but external agents should prefer `X-API-Key` so credentials can be scoped and revoked.

Recommended key type for external agents:

- `project` API key for read-only access to one project.
- `general` API key only when the agent must create crawls, manage projects, or perform admin operations.

Project-scoped keys are intentionally limited:

- They can read only assigned projects and sessions.
- They cannot create/delete API keys.
- They cannot start/resume/delete crawls.
- They cannot access sessions from other projects.

## Discover Server Info

Use this endpoint to confirm the API URL and auth mode:

```bash
curl -H "X-API-Key: $CRAWLOBSERVER_API_KEY" \
  https://crawlobserver.example.com/api/server-info
```

Expected shape:

```json
{
  "api_url": "https://crawlobserver.example.com/api",
  "has_auth": true,
  "host": "0.0.0.0",
  "port": 8899
}
```

The `host` and `port` fields describe the internal server listener. Agents should use `api_url`.

## Basic Agent Workflow

1. Check API health.
2. List visible projects.
3. Select a project.
4. List sessions for that project.
5. Select the latest completed session.
6. Read stats, pages, links, PageRank, resources, and GSC data.
7. Avoid write actions unless the key is explicitly authorized for them.

Health check:

```bash
curl https://crawlobserver.example.com/api/health
```

List visible projects:

```bash
curl -H "X-API-Key: $CRAWLOBSERVER_API_KEY" \
  https://crawlobserver.example.com/api/projects
```

List crawl sessions:

```bash
curl -H "X-API-Key: $CRAWLOBSERVER_API_KEY" \
  https://crawlobserver.example.com/api/sessions
```

## Read Session Data

Replace `{session_id}` with a real crawl session ID.

Session stats:

```bash
curl -H "X-API-Key: $CRAWLOBSERVER_API_KEY" \
  "https://crawlobserver.example.com/api/sessions/{session_id}/stats"
```

Pages, paginated:

```bash
curl -H "X-API-Key: $CRAWLOBSERVER_API_KEY" \
  "https://crawlobserver.example.com/api/sessions/{session_id}/pages?limit=100&offset=0"
```

Common page filters:

```text
status_code=404
url=fragment
title=fragment
content_type=text/html
```

For any text filter, prefix the value with `!` to exclude matching rows. The
predicate runs on the server before pagination and export:

```text
url=!/cdn-cgi/
title=!draft
```

Example: list 404 pages:

```bash
curl -H "X-API-Key: $CRAWLOBSERVER_API_KEY" \
  "https://crawlobserver.example.com/api/sessions/{session_id}/pages?status_code=404&limit=100"
```

Page detail:

```bash
curl -H "X-API-Key: $CRAWLOBSERVER_API_KEY" \
  --get "https://crawlobserver.example.com/api/sessions/{session_id}/page-detail" \
  --data-urlencode "url=https://example.com/page/"
```

Internal links:

```bash
curl -H "X-API-Key: $CRAWLOBSERVER_API_KEY" \
  "https://crawlobserver.example.com/api/sessions/{session_id}/internal-links?limit=100&offset=0"
```

All links:

```bash
curl -H "X-API-Key: $CRAWLOBSERVER_API_KEY" \
  "https://crawlobserver.example.com/api/sessions/{session_id}/links?limit=100&offset=0"
```

This endpoint is the source of truth for outbound-link auditing. Every row is
one `SourceURL -> TargetURL` edge and includes `AnchorText`, `Rel`, `Tag`, and
`LinkLocation`. To exclude a destination pattern, for example:

```text
target_url=!facebook.com
```

Read one session or snapshot directly:

```bash
curl -H "X-API-Key: $CRAWLOBSERVER_API_KEY" \
  "https://crawlobserver.example.com/api/sessions/{session_id}"
```

Use this endpoint when the ID is a `Current Snapshot` or `Current Baseline Snapshot`.
Those synthetic snapshots are intentionally omitted from `GET /sessions` but remain
available for direct reads when the caller has access to the owning project.

External link checks:

```bash
curl -H "X-API-Key: $CRAWLOBSERVER_API_KEY" \
  "https://crawlobserver.example.com/api/sessions/{session_id}/external-checks?limit=100&offset=0"
```

Resource checks:

```bash
curl -H "X-API-Key: $CRAWLOBSERVER_API_KEY" \
  "https://crawlobserver.example.com/api/sessions/{session_id}/resource-checks?limit=100&offset=0"
```

Resource checks include CSS, JavaScript, fonts, icons, and page images when resource checking was enabled for the crawl. To inspect broken or redirected images only, filter by resource type:

```bash
curl -H "X-API-Key: $CRAWLOBSERVER_API_KEY" \
  "https://crawlobserver.example.com/api/sessions/{session_id}/resource-checks?resource_type=image&limit=100&offset=0"
```

Useful filters:

```text
resource_type=image
status_code=404
url=fragment
is_internal=true
```

Page issues:

```bash
curl -H "X-API-Key: $CRAWLOBSERVER_API_KEY" \
  "https://crawlobserver.example.com/api/sessions/{session_id}/page-issues?limit=100&offset=0"
```

The page issues endpoint classifies generic SEO/technical findings from crawl signals. It does not rely on site-specific URLs.

Issue types:

```text
soft_404                 severity=error    HTTP 2xx page renders not-found signals
generic_rendered_title   severity=warning  rendered title is reused across multiple HTML 2xx pages
generic_static_metadata  severity=warning  static title/meta are reused across multiple HTML 2xx pages
```

Useful filters:

```text
severity=error
severity=warning
issue_type=soft_404
issue_type=generic_rendered_title
issue_type=generic_static_metadata
url=fragment
```

## PageRank and Crawl Graph

Top pages by internal PageRank:

```bash
curl -H "X-API-Key: $CRAWLOBSERVER_API_KEY" \
  "https://crawlobserver.example.com/api/sessions/{session_id}/pagerank-top?limit=100&offset=0"
```

PageRank distribution:

```bash
curl -H "X-API-Key: $CRAWLOBSERVER_API_KEY" \
  "https://crawlobserver.example.com/api/sessions/{session_id}/pagerank-distribution"
```

Treemap data:

```bash
curl -H "X-API-Key: $CRAWLOBSERVER_API_KEY" \
  "https://crawlobserver.example.com/api/sessions/{session_id}/pagerank-treemap"
```

Agents with project-scoped keys should treat PageRank endpoints as read-only. Do not call recompute endpoints unless using an admin/general key and explicitly instructed.

## Google Search Console Data

Replace `{project_id}` with a real project ID.

GSC connection status:

```bash
curl -H "X-API-Key: $CRAWLOBSERVER_API_KEY" \
  "https://crawlobserver.example.com/api/projects/{project_id}/gsc/status"
```

Overview:

```bash
curl -H "X-API-Key: $CRAWLOBSERVER_API_KEY" \
  "https://crawlobserver.example.com/api/projects/{project_id}/gsc/overview"
```

Queries:

```bash
curl -H "X-API-Key: $CRAWLOBSERVER_API_KEY" \
  "https://crawlobserver.example.com/api/projects/{project_id}/gsc/queries?limit=100&offset=0"
```

Pages:

```bash
curl -H "X-API-Key: $CRAWLOBSERVER_API_KEY" \
  "https://crawlobserver.example.com/api/projects/{project_id}/gsc/pages?limit=100&offset=0"
```

Timeline:

```bash
curl -H "X-API-Key: $CRAWLOBSERVER_API_KEY" \
  "https://crawlobserver.example.com/api/projects/{project_id}/gsc/timeline"
```

Use GSC endpoints to enrich crawl observations with search demand, impressions, clicks, CTR, and average position.

## Starting a Crawl

Only use this with a general/admin API key.

```bash
curl -X POST \
  -H "X-API-Key: $CRAWLOBSERVER_API_KEY" \
  -H "Content-Type: application/json" \
  https://crawlobserver.example.com/api/crawl \
  -d '{
    "seeds": ["https://example.com/"],
    "project_id": "PROJECT_ID",
    "max_pages": 1000,
    "max_depth": 0,
    "workers": 10,
    "delay": "1000ms",
    "crawl_scope": "host",
    "fetch_sitemaps": true,
    "check_external_links": true,
    "check_page_resources": true,
    "js_render_mode": "off"
  }'
```

Important crawl options:

| Field | Meaning |
| --- | --- |
| `seeds` | Start URLs. Required. |
| `project_id` | Project to attach the session to. |
| `max_pages` | Page limit. Use a real positive limit for production crawls. |
| `max_depth` | `0` means unlimited depth. |
| `workers` | Parallel crawl workers. |
| `delay` | Per-host delay, for example `1000ms` or `1s`. |
| `crawl_scope` | `host`, `domain`, or `subdirectory`. |
| `fetch_sitemaps` | Discover URLs from sitemaps. |
| `crawl_sitemap_only` | Crawl only sitemap URLs. |
| `js_render_mode` | `off`, `auto`, or `always`. |
| `store_html` | Store raw HTML. Use carefully because it increases storage. |

## Resume and Full Recrawl

Only use these endpoints with a general/admin API key.

Normal resume:

```bash
curl -X POST \
  -H "X-API-Key: $CRAWLOBSERVER_API_KEY" \
  -H "Content-Type: application/json" \
  "https://crawlobserver.example.com/api/sessions/{session_id}/resume" \
  -d '{}'
```

Full recrawl with changed parameters:

```bash
curl -X POST \
  -H "X-API-Key: $CRAWLOBSERVER_API_KEY" \
  -H "Content-Type: application/json" \
  "https://crawlobserver.example.com/api/sessions/{session_id}/resume" \
  -d '{
    "full_recrawl": true,
    "max_pages": 1000,
    "workers": 10,
    "crawl_scope": "host"
  }'
```

Current behavior: full recrawl creates a new session and keeps the old session intact. Use the returned `session_id` as the active session for follow-up reads.

Stop crawl:

```bash
curl -X POST \
  -H "X-API-Key: $CRAWLOBSERVER_API_KEY" \
  "https://crawlobserver.example.com/api/sessions/{session_id}/stop"
```

## Rescanning Individual Pages

For server-side integrations, use the project-bound endpoint with a dedicated
project API key whose `capability` is `targeted_rescan`. General/admin, browser
session, basic-auth, and evidence-only project keys are rejected on this route.
The authenticated key's `project_id` must exactly match `{project_id}` before
any storage or crawler mutation. The handler then verifies the project/session
relationship and exact seed origin. `Idempotency-Key` is required, must be at
most 200 characters, and should be a stable opaque publish-event reference.

```bash
curl -X POST \
  -H "X-API-Key: $CRAWLOBSERVER_RESCAN_API_KEY" \
  -H "Idempotency-Key: seo-publish-{event_id}" \
  -H "Content-Type: application/json" \
  "https://crawlobserver.example.com/api/projects/{project_id}/sessions/{session_id}/rescan-pages" \
  -d '{
    "urls": [
      "https://example.com/fixed-404-page/",
      "https://example.com/updated-page/"
    ]
  }'
```

Successful response:

```json
{
  "project_id": "...",
  "session_id": "...",
  "request_id": "...",
  "idempotency_key": "seo-publish-...",
  "status": "completed",
  "accepted_url_count": 2,
  "accepted_urls": ["https://example.com/fixed-404-page/", "https://example.com/updated-page/"],
  "request_digest": "sha256:...",
  "started_at": "2026-08-18T09:00:00Z",
  "completed_at": "2026-08-18T09:00:05Z"
}
```

An identical retry returns the same `request_id` and terminal response without
running a second rescan. Reusing the key with a different project/session/URL
set returns `409 idempotency_conflict`.

Stable error classifications include `project_rescan_capability_required` (403),
`project_session_mismatch` (409), `invalid_url` or `cross_origin_url` (422),
`session_not_rescannable` (409), `idempotency_conflict` (409),
`url_not_in_session` (422), `rescan_failed` (502), and `internal_error` (500).
Errors retain the normal string `error` field and add `error_code` plus bounded
project/session/request provenance where available. Authentication failures
before routing remain HTTP 401 with the standard API authentication response.

### Post-rescan verification contract

A `status: completed` targeted-rescan receipt confirms only that the mutation
request reached its terminal audit state. It is not evidence that the approved
page is present in the trusted Current Snapshot.

Verification uses a separate evidence-only project key and this exact sequence:

1. `GET /api/projects/{project_id}/current-snapshot` with
   `X-API-Key: $CRAWLOBSERVER_EVIDENCE_API_KEY`.
2. Require HTTP 200, exact `project_id`, non-empty `current_session_id`, and
   `quality_promotion_status: applied`. A 409 binding response is untrusted.
3. Ignore the rescan receipt's `session_id` for the evidence read. Use only the
   `current_session_id` returned by step 1 in
   `GET /api/sessions/{current_session_id}/page-detail?url={exact_url}`.
4. Require HTTP 200 and a well-formed `page` whose `CrawlSessionID` equals that
   Current Snapshot session, whose `URL` exactly equals the requested URL, and
   whose `CrawledAt` is not earlier than the receipt's `started_at`.

Any missing/malformed response, project/session/URL mismatch, non-200 Current
Snapshot or page-detail response, untrusted binding, or stale `CrawledAt` is
`not verified`. Do not fall back to the receipt session, a latest-session
guess, or a different URL. The executable scenario fixture, including positive
and denial outcomes, is
[`project-rescan-verification-v1.json`](fixtures/project-rescan-verification-v1.json).

The session-only endpoint remains available for existing UI and API callers and
continues to require general/admin credentials:

```bash
curl -X POST \
  -H "X-API-Key: $CRAWLOBSERVER_API_KEY" \
  -H "Content-Type: application/json" \
  "https://crawlobserver.example.com/api/sessions/{session_id}/rescan-pages" \
  -d '{
    "urls": [
      "https://example.com/fixed-404-page/",
      "https://example.com/updated-page/"
    ]
  }'
```

Use this after site fixes when only selected URLs need fresh status, title, metadata, resources, or rendered content.

## Daily Delta Crawl

Daily Delta Crawl is project-level. It checks configured candidate sources, starts a new bounded delta crawl session, and preserves previous sessions.
Candidate limits are split between known changed/problem/stale URLs and new URLs. The crawler also follows internal links discovered from launched candidates within the configured scope, discovery depth, and discovered-page budget. Manual queue URLs are marked consumed only when they are actually launched.

When the Sitemap source is enabled, preview, manual runs, and scheduled runs fetch the sitemap at plan time. They do not use an old Current Snapshot sitemap as the normal candidate source. The preview and the launched session contain `sitemap_refresh` metadata:

```json
{
  "mode": "fresh",
  "fetched_at": "2026-07-16T12:00:00Z",
  "fresh_url_count": 4031,
  "snapshot_url_count": 4016,
  "added_count": 23,
  "removed_count": 8,
  "invalid_entry_count": 0,
  "declared_sitemap_urls": ["https://example.com/sitemap.xml"],
  "warnings": []
}
```

`mode` is one of:

- `fresh`: complete sitemap observation; only these sitemap URLs are candidates.
- `skipped`: sitemap refresh was unavailable or incomplete, so the sitemap source contributed no URLs.
- `snapshot_fallback`: an admin explicitly enabled fallback; historical sitemap URLs may be used, but they are not fresh and never replace Current Snapshot sitemap membership.

Agents must not describe `skipped` or `snapshot_fallback` data as a current sitemap view. A URL that has disappeared from a fresh sitemap is not reintroduced merely because an older snapshot contains it. Raw sitemap `<loc>` evidence is returned with pages as `SitemapSourceURL` and `SitemapRawLoc`; agents must show it as received and must not silently repair literal spaces or malformed URL content.

Read settings:

```bash
curl -H "X-API-Key: $ADMIN_API_KEY" \
  https://crawlobserver.example.com/api/projects/{project_id}/delta/settings
```

Preview candidates:

```bash
curl -H "X-API-Key: $ADMIN_API_KEY" \
  https://crawlobserver.example.com/api/projects/{project_id}/delta/preview
```

Queue manual URLs:

```bash
curl -X POST \
  -H "X-API-Key: $ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"urls":["https://example.com/new-page/"]}' \
  https://crawlobserver.example.com/api/projects/{project_id}/delta/manual-queue
```

Start a delta run:

```bash
curl -X POST \
  -H "X-API-Key: $ADMIN_API_KEY" \
  https://crawlobserver.example.com/api/projects/{project_id}/delta/run
```

Only admin/general credentials should update settings or start runs. A delta run returns a new `session_id`; monitor it like any other crawl session.

## API Key Management

Only admins/general keys can manage API keys.

Create a project-scoped key:

```bash
curl -X POST \
  -H "X-API-Key: $ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  https://crawlobserver.example.com/api/api-keys \
  -d '{
    "name": "external-agent-readonly",
    "type": "project",
    "project_id": "PROJECT_ID"
  }'
```

The full key is returned only once. Store it in the agent secret store. Do not commit it to source control or logs.

Create the dedicated project-bound targeted-rescan key for a server-side
Dashboard adapter:

```bash
curl -X POST \
  -H "X-API-Key: $ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  https://crawlobserver.example.com/api/api-keys \
  -d '{
    "name": "seo-dashboard-targeted-rescan",
    "type": "project",
    "project_id": "PROJECT_ID",
    "capability": "targeted_rescan"
  }'
```

This capability is accepted only by the project-bound targeted-rescan route; it
does not satisfy general/admin write gates. Keep the key only in the server-side
adapter as `CRAWLOBSERVER_RESCAN_API_KEY`; keep the ordinary evidence-only
project key separately as `CRAWLOBSERVER_EVIDENCE_API_KEY`. No additional
CrawlObserver server environment variable is required because both keys and
their scope are stored in the existing API-key database.

List API keys:

```bash
curl -H "X-API-Key: $ADMIN_API_KEY" \
  https://crawlobserver.example.com/api/api-keys
```

Revoke an API key:

```bash
curl -X DELETE \
  -H "X-API-Key: $ADMIN_API_KEY" \
  https://crawlobserver.example.com/api/api-keys/{key_id}
```

## Agent Safety Rules

- Never log API keys, basic auth passwords, cookies, or Google OAuth tokens.
- Prefer project-scoped keys for read-only agents.
- Treat `DELETE`, `POST /crawl`, `POST /resume`, `POST /compute-*`, and provider/GSC fetch operations as privileged.
- Paginate large reads with `limit` and `offset`.
- Use URL encoding for page URLs in query parameters.
- On `401`, refresh or replace the credential.
- On `403`, do not retry with the same key; the credential is not authorized for that resource.
- On `429`, back off and retry later.
- On `5xx`, retry with exponential backoff and preserve the failed request context.

## Minimal Python Client

```python
import os
import requests

BASE_URL = "https://crawlobserver.example.com/api"
API_KEY = os.environ["CRAWLOBSERVER_API_KEY"]

session = requests.Session()
session.headers.update({"X-API-Key": API_KEY})

def get_json(path, **params):
    response = session.get(f"{BASE_URL}{path}", params=params, timeout=30)
    response.raise_for_status()
    return response.json()

projects = get_json("/projects")
print(projects)

sessions = get_json("/sessions")
completed = [s for s in sessions if s.get("Status") == "completed"]

if completed:
    sid = completed[0]["ID"]
    stats = get_json(f"/sessions/{sid}/stats")
    pages_404 = get_json(f"/sessions/{sid}/pages", status_code=404, limit=100, offset=0)
    print(stats)
    print(pages_404)
```

## Minimal JavaScript Client

```js
const BASE_URL = "https://crawlobserver.example.com/api";
const API_KEY = process.env.CRAWLOBSERVER_API_KEY;

async function api(path, options = {}) {
  const res = await fetch(`${BASE_URL}${path}`, {
    ...options,
    headers: {
      "X-API-Key": API_KEY,
      "Content-Type": "application/json",
      ...(options.headers || {}),
    },
  });

  if (!res.ok) {
    throw new Error(`CrawlObserver API ${res.status}: ${await res.text()}`);
  }

  return res.json();
}

const projects = await api("/projects");
const sessions = await api("/sessions");

console.log({ projects, sessions });
```

## Useful Endpoint Reference

All paths below are relative to:

```text
https://crawlobserver.example.com/api
```

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/health` | Health check. |
| `GET` | `/server-info` | Public API metadata. |
| `GET` | `/projects` | List visible projects. |
| `GET` | `/sessions` | List visible sessions. |
| `GET` | `/sessions/{id}` | One session, including a synthetic current or baseline snapshot. |
| `GET` | `/sessions/{id}/stats` | Session summary. |
| `GET` | `/sessions/{id}/pages` | Crawled pages. |
| `GET` | `/sessions/{id}/page-detail?url=` | One page detail. |
| `GET` | `/sessions/{id}/links` | All links. |
| `GET` | `/sessions/{id}/internal-links` | Internal links. |
| `GET` | `/sessions/{id}/external-checks` | External URL checks. |
| `GET` | `/sessions/{id}/resource-checks` | Resource checks, including `resource_type=image` for image availability. |
| `GET` | `/sessions/{id}/page-issues` | Generic page issues such as soft 404 errors and SPA metadata warnings. |
| `GET` | `/sessions/{id}/pagerank-top` | Top internal PageRank pages. |
| `GET` | `/sessions/{id}/near-duplicates` | Near duplicate page pairs. |
| `GET` | `/sessions/{id}/structured-data` | Structured data findings. |
| `GET` | `/projects/{id}/gsc/overview` | GSC overview. |
| `GET` | `/projects/{id}/gsc/queries` | GSC queries. |
| `GET` | `/projects/{id}/gsc/pages` | GSC page performance. |
| `GET` | `/projects/{id}/gsc/timeline` | GSC timeline. |
| `GET` | `/projects/{id}/delta/settings` | Daily Delta Crawl settings. |
| `PUT` | `/projects/{id}/delta/settings` | Update Daily Delta settings. Admin/general key only. |
| `GET` | `/projects/{id}/delta/preview` | Preview Daily Delta candidates. |
| `POST` | `/projects/{id}/delta/manual-queue` | Queue manual Delta URLs. Admin/general key only. |
| `POST` | `/projects/{id}/delta/run` | Start Daily Delta run. Admin/general key only. |
| `POST` | `/crawl` | Start crawl. Admin/general key only. |
| `POST` | `/sessions/{id}/resume` | Resume or full-recrawl. Admin/general key only. |
| `POST` | `/sessions/{id}/rescan-pages` | Rescan selected URLs. Admin/general key only. |
| `POST` | `/projects/{projectId}/sessions/{sessionId}/rescan-pages` | Project-bound, origin-checked, idempotent targeted rescan. Exact-project `targeted_rescan` capability key plus `Idempotency-Key`. |
| `POST` | `/sessions/{id}/stop` | Stop crawl. Admin/general key only. |
| `DELETE` | `/sessions/{id}` | Delete session. Admin/general key only. |
| `GET` | `/api-keys` | List API keys. Admin/general key only. |
| `POST` | `/api-keys` | Create API key. Admin/general key only. |
| `DELETE` | `/api-keys/{id}` | Revoke API key. Admin/general key only. |
