#!/usr/bin/env sh
set -eu

cd "$(dirname "$0")"

ENV_FILE="${ENV_FILE:-.env}"
SERVICE="${SERVICE:-app}"
RUN_MIGRATIONS="${RUN_MIGRATIONS:-1}"
IMAGE="${1:-${CRAWLOBSERVER_IMAGE:-}}"
PREVIOUS_IMAGE_FILE="${PREVIOUS_IMAGE_FILE:-.previous-app-image}"

if [ ! -f "$ENV_FILE" ]; then
  echo "Missing $ENV_FILE. Copy deploy/.env.example to deploy/.env first." >&2
  exit 1
fi

docker_cmd() {
  ${DOCKER:-sudo docker} "$@"
}

compose() {
  ${DOCKER_COMPOSE:-sudo docker compose} --env-file "$ENV_FILE" "$@"
}

set_env_var() {
  key="$1"
  value="$2"
  tmp="${ENV_FILE}.tmp"
  if grep -q "^${key}=" "$ENV_FILE"; then
    awk -v key="$key" -v value="$value" '
      BEGIN { replaced = 0 }
      $0 ~ "^" key "=" {
        print key "=" value
        replaced = 1
        next
      }
      { print }
      END {
        if (!replaced) print key "=" value
      }
    ' "$ENV_FILE" > "$tmp"
  else
    cp "$ENV_FILE" "$tmp"
    printf '%s=%s\n' "$key" "$value" >> "$tmp"
  fi
  mv "$tmp" "$ENV_FILE"
}

env_value() {
  key="$1"
  grep "^${key}=" "$ENV_FILE" | tail -1 | cut -d= -f2- || true
}

HOST_PORT="${CRAWLOBSERVER_HOST_PORT:-$(env_value CRAWLOBSERVER_HOST_PORT)}"
HEALTH_URL="${HEALTH_URL:-http://127.0.0.1:${HOST_PORT:-8899}/api/health}"

should_pull_image() {
  image="$1"
  case "$image" in
    ""|crawlobserver:*) return 1 ;;
    *) return 0 ;;
  esac
}

current_image="$(docker_cmd inspect crawlobserver-app --format '{{.Config.Image}}' 2>/dev/null || true)"
if [ -n "$current_image" ]; then
  printf '%s\n' "$current_image" > "$PREVIOUS_IMAGE_FILE"
fi

if [ -n "$IMAGE" ]; then
  set_env_var CRAWLOBSERVER_IMAGE "$IMAGE"
else
  IMAGE="$(env_value CRAWLOBSERVER_IMAGE)"
fi

if should_pull_image "$IMAGE"; then
  echo "Pulling app image: $IMAGE"
  compose pull "$SERVICE"
else
  echo "Skipping pull for local image: ${IMAGE:-crawlobserver local fallback}"
fi

if [ "$RUN_MIGRATIONS" = "1" ]; then
  echo "Running migrations with the app image"
  compose --profile tools run --rm --no-deps migrate
fi

echo "Starting only $SERVICE"
compose up -d --no-deps "$SERVICE"

echo "Checking health: $HEALTH_URL"
i=1
while [ "$i" -le 30 ]; do
  if curl -fsS "$HEALTH_URL" >/dev/null; then
    echo "Health check passed"
    compose ps "$SERVICE"
    exit 0
  fi
  sleep 2
  i=$((i + 1))
done

echo "Health check failed. Recent app logs:" >&2
compose logs --tail 80 "$SERVICE" >&2
exit 1
