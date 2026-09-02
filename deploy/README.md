# CrawlObserver Docker Compose Deployment

This deployment keeps CrawlObserver private:

- The app is published only on server loopback: `127.0.0.1:8899`.
- ClickHouse has no host-published ports.
- External access must go through Tailscale, not public firewall or public reverse proxy.

## Server

- Deploy on a Linux host with Docker Compose and Tailscale installed.
- SSH access should use a passphrase-protected key.
- Intended access model: Tailscale-only service access.

## First Setup

```bash
cp deploy/.env.example deploy/.env
cp deploy/config.production.example.yaml deploy/config.yaml
```

Edit both files:

- Set the same ClickHouse password in `deploy/.env` and `deploy/config.yaml`.
- Set a strong `server.password` in `deploy/config.yaml`.
- Optional: set `GSC_CLIENT_ID`, `GSC_CLIENT_SECRET`, and `GSC_REDIRECT_URI` in `deploy/.env` to enable Google Search Console OAuth.
- Set `APP_UID` and `APP_GID` in `deploy/.env` to the numeric server user/group that owns the deployment directory.
- Keep `server.host: 0.0.0.0` inside the container. Compose binds it to host loopback only.

Create writable app directories:

```bash
mkdir -p deploy/state deploy/backups
chmod 700 deploy/state deploy/backups
```

## Build And Start On Server

```bash
cd deploy
sudo docker compose build app
sudo docker compose --profile tools run --rm migrate
sudo docker compose up -d app
```

## Production Image Deploy

Production deploys should use a prebuilt image instead of building on the server.
Set a versioned app image in `deploy/.env`:

```dotenv
CRAWLOBSERVER_IMAGE=ghcr.io/seobserver/crawlobserver:<git-sha>
```

Then deploy only the app service:

```bash
cd deploy
./deploy-app.sh ghcr.io/seobserver/crawlobserver:<git-sha>
```

The deploy script:

- updates `CRAWLOBSERVER_IMAGE` in `deploy/.env`;
- saves the currently running app image to `.previous-app-image`;
- pulls only the app image when it is a registry image;
- runs `CHECK_ONLY=1 ./restart-app-safe.sh` with `FORCE=0` before any explicit
  migration, and runs `restart-app-safe.sh` again to recheck immediately before
  recreating the app;
- relies on `serve` startup migrations by default (`RUN_MIGRATIONS=0`);
- safely recreates only the `app` service with `--no-deps`;
- checks `http://127.0.0.1:8899/api/health`.

### Single Writer Rule

`serve`, `gui`, `crawl`, and `migrate` take an exclusive OS advisory lock in
the configured state directory (the Compose deployment shares
`/var/lib/crawlobserver`). Only one of these writer processes may run against
that directory at a time. A second process fails immediately with the lock
path; do not delete the lock file to bypass this protection. The OS releases
the lock when its owner exits or crashes, so a stale file alone does not block
a safe restart. The lock is acquired before first-run config persistence or
legacy SQLite recovery, so a rejected second writer leaves shared state intact.

`serve` runs migrations during startup while it owns this lock, so production
deploys use that path by default. For an explicit maintenance migration, run
`RUN_MIGRATIONS=1 ./deploy-app.sh <image>`: after the no-`FORCE` safety gate,
the script stops only the app, runs the `migrate` tool container, then starts
only the app. A migration failure leaves the app stopped; do not start it until
the failure is resolved. Never run a migration tool container alongside the
app service.

Do not use `docker compose up -d` without a service name for production app deploys.
Do not use `docker compose down` for app-only deploys.

## GitHub Actions Image Build

The workflow `.github/workflows/docker-image.yml` builds the Docker image and pushes it to GitHub Container Registry.

Tags:

- `ghcr.io/<owner>/<repo>:<git-sha>` for every workflow run;
- `ghcr.io/<owner>/<repo>:production` for pushes to the `production` branch;
- an optional manual tag from the `workflow_dispatch` input.

Required repository permissions:

- Actions enabled;
- workflow `permissions.packages: write`;
- GitHub Container Registry package readable by the server. Public packages can be pulled without login; private packages require `docker login ghcr.io` on the server.

Optional automated deploy from GitHub Actions requires these repository secrets:

```text
DEPLOY_HOST
DEPLOY_USER
DEPLOY_SSH_KEY
DEPLOY_SSH_PASSPHRASE
```

The deploy job runs only `./deploy-app.sh <image>` on the server.

## Rollback

Rollback to the previously running image:

```bash
cd deploy
./rollback-app.sh
```

Rollback to a specific image:

```bash
./rollback-app.sh ghcr.io/seobserver/crawlobserver:<previous-git-sha>
```

Rollback also starts only the `app` service with `--no-deps`.

## Tailscale-Only Exposure

On the server, expose the loopback service to the tailnet:

```bash
sudo tailscale serve --bg --http=8899 http://127.0.0.1:8899
```

Confirm there is no public listener:

```bash
sudo ss -ltnp | grep -E '(:8899|:9000|:8123)' || true
sudo docker compose ps
tailscale status
tailscale serve status
```

Expected result:

- Host port `8899` listens only on `127.0.0.1`.
- ClickHouse ports `9000` and `8123` are not bound on the host.
- Tailscale Serve is the only remote access path.

## Operations

### ClickHouse Memory

The production Compose profile reserves up to `2g` for ClickHouse. This leaves
enough query working memory for page-detail and aggregate reports that read
compressed stored HTML; do not lower it to `1g` on the current production host.

```bash
cd deploy
sudo docker compose ps
sudo docker compose logs -f app
sudo docker compose restart app
```

For app deploys and rollbacks, prefer `./deploy-app.sh` and `./rollback-app.sh`.

### Quality Evidence Rollout

Quality/PageRank evidence schema changes are additive and are applied by the app
startup migration. For a rollout or rollback:

1. Run `CHECK_ONLY=1 ./restart-app-safe.sh` and stop if any crawl is active.
2. Capture the current app image, health response, ClickHouse `SELECT 1`, Current
   Snapshot binding, and the affected session's current quality/evidence revision.
3. Build or select only the app image, run the safety check again, then restart
   only the `app` service through `restart-app-safe.sh`. Never use `FORCE=1`.
4. Verify health, ClickHouse continuity and recent app logs before invoking an
   admin quality re-evaluation.

Before production rollout, run the Phase 25.1 integration-tag tests against a
real isolated ClickHouse with `CRAWLOBSERVER_REQUIRE_CLICKHOUSE=1`. The gate must
execute rather than skip and covers mutation visibility, compaction, restart
readback, pointer ordering, partial-publication recovery, and snapshot retry.

Quality re-evaluation is a metadata/evaluation operation. It must use explicit
confirmation and an audit reason, and must not be substituted with a new crawl,
manual PageRank recomputation, threshold changes, or direct edits to historical
rows. Repeating the request with the same expected revisions is idempotent and
may retry only an incomplete Current Snapshot promotion.

### ClickHouse Log Retention

The ClickHouse container writes server logs to the
`deploy_clickhouse_logs` Docker volume. Install the host policy once:

```bash
cd deploy
./install-clickhouse-log-retention.sh
```

The policy:

- mounts `clickhouse/config.d/storage-policy.xml` through Compose;
- uses ClickHouse logger level `information` and native `100 MB x 3` file
  rotation;
- disables continuously persisted `trace_log` and `processors_profile_log`;
- applies a three-day TTL to operational ClickHouse system logs;
- rotates `clickhouse-server.log` and `clickhouse-server.err.log` daily or at
  100 MB;
- compresses rotated files;
- retains at most three rotations and removes archives older than three days;
- uses `copytruncate`, so ClickHouse does not need to restart;
- removes old numeric archives created by ClickHouse's native size-based
  rotation.

The app and ClickHouse Docker `json-file` logs are independently capped at
three 20 MB files. Scheduled application backups default to every 24 hours with
two retained generations; app restarts preserve the due time instead of
creating an extra archive. Set `backup.time` to a daily `HH:MM` wall-clock time
and optionally `backup.timezone` to an IANA timezone (for example,
`Europe/Kyiv`) when a fixed local schedule is required. A successful separate
critical export allows the scheduled full archive to omit `gsc_analytics` rows;
if that export fails, the full archive keeps those rows automatically. Manual
full backups always keep all table data.

The default paths assume the Compose project name is `deploy`. If the project
name changes, update `deploy/logrotate/crawlobserver-clickhouse` before
installing it.

## JS Rendering

The app image includes Alpine Chromium and sets `CRAWLOBSERVER_CHROME_BIN=/usr/bin/chromium-browser`.
This avoids Rod downloading an incompatible glibc Chromium snapshot at runtime.

## Google Search Console

Create a Google OAuth client of type `Web application` and add this redirect URI:

```text
https://crawlobserver.example.com/api/gsc/callback
```

For production, replace the example host with the real CrawlObserver host and set the same exact URI in `deploy/.env`:

```dotenv
GSC_CLIENT_ID=...
GSC_CLIENT_SECRET=...
GSC_REDIRECT_URI=https://crawlobserver.example.com/api/gsc/callback
```

Do not commit `deploy/.env` or `deploy/config.yaml`.
