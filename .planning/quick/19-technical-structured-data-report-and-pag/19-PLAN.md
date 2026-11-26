# Quick 19: Technical Structured Data Report

## Scope

- Report what the crawler observes, not Schema.org conformance or Google rich-result eligibility. No Google rules, ranking claims, Quality Gate changes, extra crawl, or historical backfill.
- Reuse existing raw/rendered HTML parsing. Identify JSON-LD and detected Microdata; enumerate typed JSON-LD entities and record malformed blocks with stable issue code and block/byte location. Detected-only formats are not falsely labeled validated.
- Separate not checked, no markup, technical issues and checked without detected technical issues. Existing/historical evidence without the new check must not be promoted to a clean result.
- Add authenticated session-scoped read API with URL detail and bounded/paginated report rows, status/format/type filters and summary. Preserve the existing structured-data endpoint contract. Report URL, origin of the evidence (static or rendered), format, detected types, and diagnostics. Keep full HTML out of the summary/list.
- Add Reports > Structured Data directly after Core Web Vitals, linking URLs to URL Detail. Add Schema Type to Pages > All and HTML: actual vocabulary types such as `Product`, `Service` and `Article`, not syntax names or inferred categories. Update ProductFeatures.md and API documentation.

## Implementation

1. Capture bounded technical observations at the existing extraction points. Store per-page evidence so duplicate entities remain distinct and apply the established snapshot/Delta rules: a fresh fetch replaces old observations and 304 preserves previous evidence. Prefer additive fields and the existing storage, query and authorization patterns.
2. Wire a read-only server contract to the report and URL Detail. Give the API and UI the same status semantics, with separate loading, empty and API error states. Show types from the new per-page evidence in the Pages columns without issuing a request per URL. Keep legacy heuristics out of the new fields without changing legacy API behavior.
3. Validate with fixtures spanning malformed JSON-LD, multiple same-type entities, no markup, historical/unsupported evidence and static/rendered distinctions. Check pagination/filtering and authorization, both Pages tables and navigation from the report to the same URL detail. Test snapshot/Delta carry-forward where evidence is materialized.
4. Run independent code, tests and UX review; run Go and frontend checks. Commit/push owned files and deploy via the no-force, app-only gate. Confirm new route and report in production without launching a crawl manually.

Expected surfaces: internal/schema, parser/crawler, ClickHouse page storage and migration, server handlers and docs, ReportsHub, StructuredDataReport, PagesExplorer, UrlDetailView, tests and ProductFeatures.md. Estimate 600-900 changed lines with tests; avoid unrelated refactoring.
