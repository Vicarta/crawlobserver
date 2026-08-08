#!/usr/bin/env sh
set -eu

COMPOSE_FILE_DIR="${COMPOSE_FILE_DIR:-$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)}"
ENV_FILE="${ENV_FILE:-.env}"
APP_SERVICE="${APP_SERVICE:-app}"
CLICKHOUSE_SERVICE="${CLICKHOUSE_SERVICE:-clickhouse}"
CLICKHOUSE_DB="${CLICKHOUSE_DB:-crawlobserver}"

cd "$COMPOSE_FILE_DIR"

if ! docker compose --env-file "$ENV_FILE" ps --services --status running | grep -qx "$CLICKHOUSE_SERVICE"; then
  echo "ClickHouse service '$CLICKHOUSE_SERVICE' is not running; refusing to restart app safely." >&2
  exit 2
fi

active_query="
      SELECT concat(id, '\t', status, '\t', if(label = '', '-', label), '\t', arrayStringConcat(seed_urls, ', '))
      FROM ${CLICKHOUSE_DB}.crawl_sessions FINAL
      WHERE status IN ('running', 'queued')
      ORDER BY started_at DESC
      FORMAT TSVRaw
    "

if ! active_sessions=$(
  docker compose --env-file "$ENV_FILE" exec -T "$CLICKHOUSE_SERVICE" sh -lc \
    'clickhouse-client --user "$CLICKHOUSE_USER" --password "$CLICKHOUSE_PASSWORD" --query "$1"' \
    sh "$active_query" 2>/dev/null
); then
  echo "Could not query active crawl sessions; refusing to restart app safely." >&2
  exit 2
fi

if [ -n "$active_sessions" ] && [ "${FORCE:-0}" != "1" ]; then
  echo "Refusing to restart CrawlObserver: active crawl sessions exist." >&2
  echo "$active_sessions" | awk -F '\t' '{ printf "  id=%s status=%s label=%s seeds=%s\n", $1, $2, $3, $4 }' >&2
  echo "Wait for them to finish, stop them intentionally, or rerun with FORCE=1." >&2
  exit 3
fi

if [ -n "$active_sessions" ] && [ "${FORCE:-0}" = "1" ]; then
  echo "FORCE=1 set; restarting despite active crawl sessions:" >&2
  echo "$active_sessions" | awk -F '\t' '{ printf "  id=%s status=%s label=%s seeds=%s\n", $1, $2, $3, $4 }' >&2
fi

if [ "${CHECK_ONLY:-0}" = "1" ]; then
  echo "No active crawl sessions found. Restart is safe."
  exit 0
fi

docker compose --env-file "$ENV_FILE" up -d --force-recreate --no-deps "$APP_SERVICE"
