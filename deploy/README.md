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
sudo docker compose down
```

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
