# Quick 20: Depth writeback without mutation waits

- Keep the existing BFS and terminal status policy. Replace only the `depth` and `found_on` persistence with PageRank's session-scoped foreground replacement of `FINAL` rows, keeping `crawled_at` and other page fields unchanged.
- Prove the writeback completes with an unrelated failed mutation already queued. Assert both derived fields, page data and session isolation, and no new depth mutation. Rerun the existing PageRank finalization tests and relevant Go gates.
- Owner-approved release unblock: make Retry Failed select current error rows without deleting them. Publish a new observation by the existing versioned page insert; skipped or unfinished URLs keep the previous row. Exercise this with a real retry and ClickHouse fixture, including the existing response count and no new page mutation.
- Update `ProductFeatures.md`. Commit and push only owned files on the configured branch. Deploy only the application with the existing no-force active-crawl gate before and after building. Verify app and database health and leave the existing mutation backlog and historical sessions untouched.
