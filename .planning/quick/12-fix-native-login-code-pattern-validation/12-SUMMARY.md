# Quick 12 Summary

Completed 2026-10-06. Implementation release:
`82e118e324b019c81d0c4625a186c68e7e89c0a0`, pushed to
`origin/codex/cleanup-deployed-worktree` (Vicarta/crawlobserver).

## Change

One explicit Svelte string fixes the interpolated six-digit HTML pattern.
Regression tests check actual native validity and ordinary button submission;
ProductFeatures documents browser format versus unchanged server authentication.
All frozen-candidate tests and three independent critics passed; see
12-VERIFICATION.md. No unrelated files, dependency changes or auth redesign.

## Production Acceptance

- Both no-force CHECK_ONLY gates found no active crawls. Only app rebuilt and
  recreated; startup time `2026-10-06T19:01:09.524078319Z`.
- Image: `sha256:9cb9c2ad774f8209e9349ae961f76553b507da8d9d400350259d81db5d51a6fb`.
- `/api/health`: `{"status":"ok"}`. Startup log scan: 50 lines, 0 unexpected errors.
- ClickHouse identity unchanged:
  `685ab71b0245ebfbcb339fa3ab6dd5fe404aec72e4d7602056ecbf4eb0cf5078`,
  start `2026-10-04T01:30:46.812520785Z`, healthy; `SELECT 1` returned 1.
- Actual public production Chromium: DOM pattern `[0-9]{6}`, leading-zero
  fixture natively valid, empty validation message, normal Verify click reached
  intercepted verify exactly once. Stub 401 displayed generic invalid/expired
  feedback instead of native format rejection. Short/long/letters/non-ASCII
  fixtures stayed invalid.
- Before: DOM `[0-9]6`, native patternMismatch, zero intercepted verify calls.
- No real code request, email or authentication attempted; no live crawl,
  data mutation or credential reused. Browser and local Vite server stopped.
- Rollback source archive and tagged prior image retained under
  `/opt/crawlobserver/app/.deploy-backups/login-pattern-82e118e324b019c81d0c4625a186c68e7e89c0a0`.
- Existing Svelte/chunk-size and dependency audit warnings remain outside this
  narrow fix; no dependency updates applied.

## Outcome

The reported native format blocker is fixed and deployed. Server-side expiry,
single-use, rate limiting and credential validation remain unchanged. Refresh
the page and request a fresh code before retrying; actual owner login was not
performed as part of acceptance.
