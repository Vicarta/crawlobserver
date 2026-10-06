# Quick 12 Verification

## Diagnosis

Production Chromium rendered the Svelte attribute `pattern="[0-9]{6}"` as
`[0-9]6`. A six-digit fixture failed native validation with patternMismatch;
ordinary Verify click did not reach the intercepted verification endpoint.
No real code, email request or authentication was used.

## Frozen Candidate Validation

- Explicit Svelte string renders exactly `[0-9]{6}`.
- Focused LoginPage tests: 10 passed; complete frontend suite: 146 passed.
- `npm run lint` and `npm run build`: passed.
- `go test ./internal/server ./internal/apikeys -run 'Passwordless|LoginCode' -count=1`: passed.
- `git diff --check`: passed.
- Independent Code and Tests critics (gpt-6.1-sol high): PASS.
- Independent UX critic (gpt-6.1-sol high): PASS in actual Chromium against
  the local Vite app, with auth fetches intercepted. Leading-zero fixture was
  natively valid; ordinary Verify reached the stub once without a format
  bubble. Full-width digits stayed invalid and did not trigger verification.
- Builder: gpt-6-luna max. Backend authentication rules unchanged.

## Release Gate

Production acceptance is recorded in 12-SUMMARY.md after the app-only rollout.
Two no-force CHECK_ONLY gates, health, unchanged healthy ClickHouse and native
production form acceptance are required. No real login or live crawl is needed.
