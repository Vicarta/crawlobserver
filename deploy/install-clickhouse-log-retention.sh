#!/usr/bin/env sh
set -eu

cd "$(dirname "$0")"

if [ "$(id -u)" -ne 0 ]; then
  exec sudo "$0" "$@"
fi

install -m 0755 \
  ./crawlobserver-clickhouse-log-retention \
  /usr/local/sbin/crawlobserver-clickhouse-log-retention
install -m 0644 \
  ./logrotate/crawlobserver-clickhouse \
  /etc/logrotate.d/crawlobserver-clickhouse

logrotate -d /etc/logrotate.d/crawlobserver-clickhouse

echo "CrawlObserver ClickHouse log retention installed."
echo "Rotation: daily; retention: 7 rotations and at most 7 days."
