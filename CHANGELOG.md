# Changelog

All notable changes to CrawlObserver are documented here.

## Unreleased

This is the initial open-source release of CrawlObserver by [SEObserver](https://www.seobserver.com).

### Operations

- Add daily rotation and seven-day retention for the Docker ClickHouse server
  logs without requiring a ClickHouse restart.

### Fixed
- Quality Gate and Internal PageRank now consume the same versioned eligible-page
  population and durable PageRank evidence. Crawl completion waits for finalized
  evidence, while PageRank failures finish as `completed_with_errors` instead of
  exposing an apparently trustworthy terminal session.
- Quality evaluations and findings are immutable revisions with a separately
  published current pointer. Evidence/rules/baseline changes refresh in either
  direction, and Current Snapshot promotion records and rechecks the exact
  evaluation and PageRank evidence binding.
- Added admin-only, audited, idempotent quality re-evaluation for completed
  sessions. It can adopt verified existing PageRank evidence without a new crawl
  or PageRank recomputation, preserves the prior stale result in history, and
  reports typed provenance and optimistic-concurrency conflicts in API/UI.
- PageRank reports now fail closed on a newer pending/failed attempt, stale
  predicate, partial page revision, or graph/rank fingerprint mismatch instead
  of attaching an older successful evidence revision to current rows.
- PageRank, quality, promotion, repair-audit, and Current Snapshot lifecycles use
  monotonic persisted sequences. Snapshot repair/fold publishes and verifies the
  pointer before replayable cleanup, preventing a retry from losing baseline or
  Delta recovery state.
- Quality repair records a redacted durable start before mutation and an ordered
  terminal event afterward. The scheduler scans sessions fairly across restarts,
  and read APIs immediately invalidate evaluator/rules/evidence drift rather
  than waiting for a later scheduler cycle.
- URL Detail now explains where every URL was discovered before the GSC block,
  including direct or redirect-derived internal referrers, anchor and DOM
  evidence, sitemap/seed/Delta candidate sources, and explicit unavailable
  provenance for legacy rows. Pages and URL Detail share the same source
  classification, so a final URL reached through a retained redirect alias is
  no longer mislabelled as an orphan.
- JS-rendered crawls now serialize Chrome navigations per origin so concurrent
  SPA routes cannot overwrite one another's document metadata. Each page's
  render timeout starts after it receives the origin slot.
- Page Issues now distinguishes rendered-title duplication from duplicated
  metadata in the server HTML shell, and SEO title/description lengths count
  Unicode characters instead of UTF-8 bytes.
- JavaScript-rendered crawls now use the stabilized rendered DOM as the
  authoritative source for SEO metadata, headings, content metrics, canonical
  state, duplicate detection, and the internal link graph. Raw response values
  remain available as `static_*` diagnostics.
- JS-rendered pages no longer persist the static link set twice, and PageRank
  now receives rendered links and rendered canonicals.
- Selectable Pages, Crawl Sessions, and Interlinking tables can now export only the explicitly selected rows to CSV. Selection remains stable across pagination and sorting, including Interlinking simulations.
- Added 5xx spike quality gates so transient Daily Delta server errors cannot silently promote into the project current snapshot.
- Fixed report drilldown filters to use explicit status-code ranges for 2xx/3xx/4xx/5xx groups.
- Daily Delta now refreshes sitemap inputs at plan time. Removed historical sitemap URLs no longer re-enter crawls merely because an older snapshot retained them; incomplete refreshes skip the sitemap source by default and explicit fallback runs are marked non-fresh.
- Fresh sitemap provenance, raw `<loc>` evidence, and added/removed counts are retained with the Delta plan and replace Current Snapshot sitemap membership only after trusted promotion.

### Crawler Engine
- Concurrent crawl workers with per-host delay and robots.txt compliance
- 45+ SEO signals extracted per page (title, canonical, meta tags, headings, hreflang, Open Graph, schema.org, images, links, indexability)
- Redirect chain tracking with full hop-by-hop detail
- Sitemap-only crawl mode (`--sitemap-only`) to skip link following
- Configurable crawl scope: `host` (exact match) or `domain` (eTLD+1)
- Per-crawl User-Agent override with browser presets
- TLS fingerprinting via utls to match User-Agent identity
- SSRF protection: private IP blocking, DNS rebinding defense
- Per-status-code retry policy with configurable backoff
- Disk-full resilience: auto-stop on data loss, unlimited resume

### Storage
- ClickHouse backend with columnar storage, partitioned by month
- Managed mode: auto-download and run ClickHouse without Docker
- Batch insert buffer with configurable flush interval
- ZSTD-compressed HTML storage (opt-in)

### Web UI
- Svelte 5 frontend embedded in the Go binary
- Session management: start, stop, resume, delete, compare
- Page explorer with filtering by status code, content type, depth, word count
- Tabs: overview, titles, meta, headings, images, indexability, response codes, internal/external links
- PageRank: distribution histogram, treemap by path, top-N pages
- robots.txt tester and sitemap viewer
- Google Search Console integration
- Real-time crawl progress via Server-Sent Events
- Custom accent color, dark mode, SEObserver branding
- API key management with project-scoped access

### CLI
- `crawl` — start a crawl with seed URLs or seeds file
- `serve` — start the web server and REST API
- `migrate` — create or update ClickHouse tables
- `sessions` — list crawl sessions
- `report external-links` — export external links (table or CSV)
- `update` — self-update from GitHub releases
- `install-clickhouse` — download ClickHouse binary for managed mode

### API
- 40+ REST endpoints for sessions, pages, links, analytics, robots.txt, sitemaps
- Basic Auth and API key authentication (`X-API-Key` header)
- Paginated responses with filtering and sorting

### Security
- Parameterized SQL queries throughout
- Constant-time API key comparison
- SHA256-hashed API key storage with salt
- Content Security Policy headers
- Input validation on all user-facing endpoints
