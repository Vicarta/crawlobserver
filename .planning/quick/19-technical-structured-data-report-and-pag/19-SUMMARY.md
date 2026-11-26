# Quick 19: Technical Structured Data Report

Implementation verified locally and deployed to production on 2026-11-26 as `crawlobserver:12d721c`.

- Crawls now capture technical structured-data evidence for static and rendered HTML. JSON-LD checks syntax and extracts typed entities; Microdata is detection-only. No Google eligibility, Schema.org conformance, ranking, or quality-gate checks were added.
- Reports > Structured Data shows session totals and filterable URL evidence, with block-relative diagnostics and links to URL Detail. Pages > All and HTML show observed vocabulary types such as `Product` and `Service`; historical heuristic results remain available only through their existing surfaces.
- Authenticated access reuses the session scope of the existing API. `GET /api/sessions/{id}/structured-data/report` supports status, format, type, pagination, and exact URL detail. Snapshot materialization replaces evidence on a fresh fetch and preserves it on Delta 304.
- Verification: full Go suite, targeted Go vet, Go build, frontend tests/lint/build, isolated ClickHouse report and Delta integration, and browser fixture checks for report placement, filters, Pages All/HTML, and URL Detail. Independent code and test reviews found no release blockers.
- At a 390px viewport, the new report uses its own table scroller. The shared page shell has the same document width as Reports > Overview and Pages > All (486px); no shared layout changes were made.

Production verification: no-force safety checks found no active crawl before the app-only restart. `/api/health` returned `ok`, ClickHouse stayed healthy without a restart (`SELECT 1` passed), and there were no errors in the sampled startup logs. In Astrogen Current Snapshot `bbeb3cca-74fa-414d-bb98-a49a714c580f`, Structured Data loaded next to Core Web Vitals and reported all 325 HTML pages as `Not checked`. Pages > All and Pages > HTML both showed Schema Type with blank values in the sampled historical rows. No crawl or backfill was initiated; populated types were verified only with local fixtures.
