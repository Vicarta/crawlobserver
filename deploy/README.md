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
- runs migrations through the `migrate` tool container with `--no-deps`;
- starts only the `app` service with `--no-deps`;
- checks `http://127.0.0.1:8899/api/health`.

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

```bash
cd deploy
sudo docker compose ps
sudo docker compose logs -f app
sudo docker compose restart app
```

For app deploys and rollbacks, prefer `./deploy-app.sh` and `./rollback-app.sh`.

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
