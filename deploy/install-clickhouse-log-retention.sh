#!/usr/bin/env sh
set -eu

cd "$(dirname "$0")"

if [ "$(id -u)" -ne 0 ]; then
  exec sudo "$0" "$@"
fi

if ! command -v python3 >/dev/null 2>&1; then
  echo "python3 is required for ClickHouse system-log budgeting." >&2
  exit 1
fi

python3 -c 'import argparse, dataclasses, json, re, subprocess, sys, typing'

install -m 0755 \
  ./crawlobserver-clickhouse-log-retention \
  /usr/local/sbin/crawlobserver-clickhouse-log-retention
install -m 0644 \
  ./logrotate/crawlobserver-clickhouse \
  /etc/logrotate.d/crawlobserver-clickhouse

logrotate -d /etc/logrotate.d/crawlobserver-clickhouse

install -m 0755 \
  ./clickhouse-system-log-budget.py \
  /usr/local/sbin/crawlobserver-clickhouse-system-log-budget
install -m 0644 \
  ./systemd/crawlobserver-clickhouse-log-budget.service \
  /etc/systemd/system/crawlobserver-clickhouse-log-budget.service
install -m 0644 \
  ./systemd/crawlobserver-clickhouse-log-budget.timer \
  /etc/systemd/system/crawlobserver-clickhouse-log-budget.timer

systemctl daemon-reload
systemctl enable --now crawlobserver-clickhouse-log-budget.timer

echo "CrawlObserver ClickHouse log retention installed."
echo "Rotation: daily or at 100 MB; retention: 3 rotations and at most 3 days."
echo "System-log parts: automatic 150,000,000-byte active-size budget, checked every 5 minutes."
