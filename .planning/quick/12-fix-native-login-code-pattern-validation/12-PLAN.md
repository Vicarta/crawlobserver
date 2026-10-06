# Quick 12: Native Login Code Validation

## Goal And Scope

Allow a valid six-digit login code through native browser validation without
weakening server-side single-use, expiry, rate-limit or session checks.
Expected files: LoginPage.svelte, LoginPage.test.js, ProductFeatures.md, this
task's GSD records. Estimate 40-70 functional/test/doc lines. No unrelated
formatting, backend auth redesign, live code requests or real-code verification.

## Tasks

1. Reproduce the rendered pattern, then preserve its quantifier using an
   explicit Svelte string. Regression-test native form validity and ordinary
   button submission for six ASCII digits including a leading zero; reject
   shorter/longer/non-digit codes. Tests use fake auth APIs, not real codes.
2. Run focused and full frontend tests/lint/build, relevant backend auth tests,
   and independent Code/Tests/UX reviews after builder freeze. Validate the
   browser form itself with mocked auth requests; do not bypass native form
   validation using a manually dispatched submit event.
3. Commit/push owned files and deploy the app only through no-force safety
   gates before build and restart. Preserve ClickHouse and active crawls.
   Confirm production login DOM and native submit with mocked auth endpoints,
   health and ClickHouse continuity; record acceptance and push GSD closeout.

## Acceptance

- Rendered input pattern is exactly `[0-9]{6}`.
- Six ASCII digits are natively valid and normal submit reaches verify API;
  leading zeros remain intact. Invalid lengths/characters remain rejected.
- Real credential validity still belongs to the unchanged backend.
- ProductFeatures, regression tests, reviews, commit/push and safe production
  release are complete. No user code or credential is logged or reused.
