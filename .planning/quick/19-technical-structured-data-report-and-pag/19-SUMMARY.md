# Quick 19: Technical Structured Data Report

Implementation verified locally. Production rollout and production smoke test are pending SSH authorization.

- Crawls now capture technical structured-data evidence for static and rendered HTML. JSON-LD checks syntax and extracts typed entities; Microdata is detection-only. No Google eligibility, Schema.org conformance, ranking, or quality-gate checks were added.
- Reports > Structured Data shows session totals and filterable URL evidence, with block-relative diagnostics and links to URL Detail. Pages > All and HTML show observed vocabulary types such as `Product` and `Service`; historical heuristic results remain available only through their existing surfaces.
- Authenticated access reuses the session scope of the existing API. `GET /api/sessions/{id}/structured-data/report` supports status, format, type, pagination, and exact URL detail. Snapshot materialization replaces evidence on a fresh fetch and preserves it on Delta 304.
- Verification: full Go suite, targeted Go vet, Go build, frontend tests/lint/build, isolated ClickHouse report and Delta integration, and browser fixture checks for report placement, filters, Pages All/HTML, and URL Detail. Independent code and test reviews found no release blockers.
- At a 390px viewport, the new report uses its own table scroller. The shared page shell has the same document width as Reports > Overview and Pages > All (486px); no shared layout changes were made.

No live crawl, historical backfill, or production data changes were made for verification. Deploy using the existing no-force app-only procedure after SSH access is available; verify application health, unchanged ClickHouse health, and the new route without triggering a crawl.
