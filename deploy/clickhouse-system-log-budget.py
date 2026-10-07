#!/usr/bin/env python3
"""Keep active ClickHouse system-log parts under a bounded byte budget."""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
import time
from dataclasses import dataclass
from typing import Callable, Iterable


DEFAULT_BUDGET_BYTES = 150_000_000
MAX_BUDGET_BYTES = (1 << 63) - 1
MAX_ITERATIONS = 100
MAX_QUERY_ROWS = 10_000
MAX_RESPONSE_BYTES = 1_000_000
MAX_RUNTIME_SECONDS = 110
DOCKER_COMMAND_TIMEOUT_SECONDS = 25
MAX_AGE_SNAPSHOT_ATTEMPTS = 3
CONTAINER_NAME = "crawlobserver-clickhouse"

# Only persisted ClickHouse system-log tables are eligible. Numeric suffixes
# cover ClickHouse's rotated log tables; other names remain out of scope.
SYSTEM_LOG_TABLES = (
    "aggregated_zookeeper_log",
    "asynchronous_insert_log",
    "asynchronous_metric_log",
    "backup_log",
    "blob_storage_log",
    "crash_log",
    "error_log",
    "filesystem_cache_log",
    "filesystem_read_prefetches_log",
    "latency_log",
    "metric_log",
    "part_log",
    "processors_profile_log",
    "query_log",
    "query_metric_log",
    "query_thread_log",
    "query_views_log",
    "s3queue_log",
    "session_log",
    "text_log",
    "trace_log",
    "transactions_info_log",
    "zookeeper_connection_log",
    "zookeeper_log",
)
_TABLE_PATTERN = re.compile(r"^(?:" + "|".join(SYSTEM_LOG_TABLES) + r")(?:_[0-9]+)?$")
_PART_PATTERN = re.compile(r"^[A-Za-z0-9_]+$")
_MISSING_PART_PATTERN = re.compile(
    r"(?:no such part|part does not exist|cannot find part|not found in data parts)",
    re.IGNORECASE,
)


class BudgetError(RuntimeError):
    pass


class ClickHouseError(BudgetError):
    def __init__(self, message: str, *, stderr: str = "") -> None:
        super().__init__(message)
        self.stderr = stderr

    def is_missing_part(self, part: str) -> bool:
        return part in self.stderr and bool(_MISSING_PART_PATTERN.search(self.stderr))


class CandidateSnapshotChanged(BudgetError):
    def __init__(self, message: str, inventory: Inventory | None = None) -> None:
        super().__init__(message)
        self.inventory = inventory


@dataclass(frozen=True)
class Part:
    table: str
    name: str
    active: bool
    bytes_on_disk: int


@dataclass(frozen=True)
class Candidate:
    table: str
    name: str
    oldest_event_time: int
    bytes_on_disk: int


@dataclass(frozen=True)
class Inventory:
    parts: tuple[Part, ...]

    @property
    def active_bytes(self) -> int:
        return sum(part.bytes_on_disk for part in self.parts if part.active)

    @property
    def inactive_bytes(self) -> int:
        return sum(part.bytes_on_disk for part in self.parts if not part.active)

    @property
    def active_parts(self) -> dict[tuple[str, str], Part]:
        return {(part.table, part.name): part for part in self.parts if part.active}


def is_allowed_table(name: object) -> bool:
    return isinstance(name, str) and _TABLE_PATTERN.fullmatch(name) is not None


def _query_settings() -> str:
    return "SETTINGS max_threads = 1, max_execution_time = 20, log_queries = 0"


def _allowed_tables_sql() -> str:
    names = ", ".join("'" + name + "'" for name in SYSTEM_LOG_TABLES)
    rotated_pattern = "^(" + "|".join(SYSTEM_LOG_TABLES) + ")_[0-9]+$"
    return f"(table IN ({names}) OR match(table, '{rotated_pattern}'))"


def inventory_sql() -> str:
    return (
        "SELECT table, name, toUInt8(active) AS active, "
        "toString(bytes_on_disk) AS bytes_on_disk FROM system.parts "
        "WHERE database = 'system' AND " + _allowed_tables_sql() + " "
        f"ORDER BY table, name LIMIT {MAX_QUERY_ROWS + 1} "
        f"{_query_settings()} FORMAT JSONEachRow"
    )


def event_age_sql(parts: Iterable[tuple[str, str]]) -> str:
    parts_by_table: dict[str, set[str]] = {}
    for table, name in parts:
        if not is_allowed_table(table) or not isinstance(name, str) or not _PART_PATTERN.fullmatch(name):
            raise BudgetError("Refusing an invalid table or part in event-time lookup")
        parts_by_table.setdefault(table, set()).add(name)
    selects = []
    for table, names in sorted(parts_by_table.items()):
        requested_names = ", ".join("'" + name + "'" for name in sorted(names))
        selects.append(
            "SELECT '" + table + "' AS table, _part AS part, "
            "toUnixTimestamp(min(event_time)) AS oldest_event_time "
            "FROM system.`" + table + "` "
            "WHERE _part IN (" + requested_names + ") AND _part IN (SELECT name FROM system.parts "
            "WHERE database = 'system' AND table = '" + table + "' AND active) "
            "GROUP BY _part"
        )
    if not selects:
        return ""
    return (
        " UNION ALL ".join(selects)
        + f" ORDER BY oldest_event_time ASC, table ASC, part ASC LIMIT {MAX_QUERY_ROWS + 1} "
        + f"{_query_settings()} FORMAT JSONEachRow"
    )


def drop_part_sql(table: str, part: str) -> str:
    if not is_allowed_table(table) or not _PART_PATTERN.fullmatch(part):
        raise BudgetError("Refusing an invalid system-log table or part name")
    return f"ALTER TABLE system.`{table}` DROP PART '{part}'"


class ClickHouseClient:
    """Run one SQL statement per docker exec without exposing container secrets."""

    _SHELL_COMMAND = (
        'exec clickhouse-client --user "$CLICKHOUSE_USER" '
        '--password "$CLICKHOUSE_PASSWORD" --format JSONEachRow '
        "--max_threads 1 --max_execution_time 20 --log_queries 0"
    )

    def __init__(self, *, now: Callable[[], float] = time.monotonic) -> None:
        self._deadline = now() + MAX_RUNTIME_SECONDS
        self._now = now

    def execute(self, sql: str) -> str:
        remaining = self._deadline - self._now()
        if remaining <= 0:
            raise BudgetError("ClickHouse log-budget run exceeded its time limit")
        timeout = min(DOCKER_COMMAND_TIMEOUT_SECONDS, max(1, remaining))
        command = [
            "docker",
            "exec",
            "-i",
            CONTAINER_NAME,
            "sh",
            "-lc",
            self._SHELL_COMMAND,
        ]
        try:
            result = subprocess.run(
                command,
                input=sql,
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=timeout,
                check=False,
            )
        except subprocess.TimeoutExpired as error:
            raise BudgetError("ClickHouse command timed out") from error
        except OSError as error:
            raise BudgetError("Could not run Docker for ClickHouse log budgeting") from error
        if result.returncode != 0:
            raise ClickHouseError(
                f"ClickHouse command failed with exit code {result.returncode}",
                stderr=result.stderr or "",
            )
        if len(result.stdout.encode("utf-8")) > MAX_RESPONSE_BYTES:
            raise BudgetError("ClickHouse response exceeded the configured size limit")
        return result.stdout


def _json_rows(output: str) -> list[dict[str, object]]:
    if len(output.encode("utf-8")) > MAX_RESPONSE_BYTES:
        raise BudgetError("ClickHouse response exceeded the configured size limit")
    lines = [line for line in output.splitlines() if line.strip()]
    if len(lines) > MAX_QUERY_ROWS:
        raise BudgetError("ClickHouse returned more rows than the configured limit")
    rows: list[dict[str, object]] = []
    for line in lines:
        try:
            value = json.loads(line)
        except (TypeError, json.JSONDecodeError) as error:
            raise BudgetError("ClickHouse returned malformed JSONEachRow data") from error
        if not isinstance(value, dict):
            raise BudgetError("ClickHouse returned a non-object JSONEachRow row")
        rows.append(value)
    return rows


def _nonnegative_int(value: object, field: str) -> int:
    if isinstance(value, bool):
        raise BudgetError(f"ClickHouse returned an invalid {field}")
    if isinstance(value, int):
        integer = value
    elif isinstance(value, str) and re.fullmatch(r"[0-9]+", value):
        integer = int(value, 10)
    else:
        raise BudgetError(f"ClickHouse returned an invalid {field}")
    if integer < 0:
        raise BudgetError(f"ClickHouse returned an invalid {field}")
    return integer


def _active_flag(value: object) -> bool:
    if isinstance(value, bool):
        return value
    if (type(value) is int and value == 1) or value == "1":
        return True
    if (type(value) is int and value == 0) or value == "0":
        return False
    raise BudgetError("ClickHouse returned an invalid part active flag")


class LogBudgetManager:
    def __init__(self, execute: Callable[[str], str], budget_bytes: int) -> None:
        self._execute = execute
        self.budget_bytes = budget_bytes
        self._age_cache: dict[tuple[str, str], tuple[int, int]] = {}

    def inventory(self) -> Inventory:
        rows = _json_rows(self._execute(inventory_sql()))
        if len(rows) > MAX_QUERY_ROWS:
            raise BudgetError("ClickHouse has too many parts to inventory safely")
        parts: list[Part] = []
        for row in rows:
            table = row.get("table")
            name = row.get("name")
            active = row.get("active")
            if not is_allowed_table(table) or not isinstance(name, str) or not _PART_PATTERN.fullmatch(name):
                raise BudgetError("ClickHouse returned an invalid system-log part identity")
            parts.append(
                Part(
                    table=table,
                    name=name,
                    active=_active_flag(active),
                    bytes_on_disk=_nonnegative_int(row.get("bytes_on_disk"), "part size"),
                )
            )
        return Inventory(tuple(parts))

    def _read_age_facts(
        self, requested: dict[tuple[str, str], int]
    ) -> tuple[dict[tuple[str, str], int], bool]:
        sql = event_age_sql(requested)
        if not sql:
            return {}, False
        rows = _json_rows(self._execute(sql))
        if len(rows) > MAX_QUERY_ROWS:
            raise BudgetError("ClickHouse has too many log parts to age safely")
        facts: dict[tuple[str, str], int] = {}
        seen: set[tuple[str, str]] = set()
        mismatch = False
        for row in rows:
            table = row.get("table")
            name = row.get("part")
            if not is_allowed_table(table) or not isinstance(name, str) or not _PART_PATTERN.fullmatch(name):
                raise BudgetError("ClickHouse returned an invalid log-part age identity")
            identity = (table, name)
            if identity not in requested:
                mismatch = True
                continue
            if identity in seen:
                facts.pop(identity, None)
                mismatch = True
                continue
            seen.add(identity)
            facts[identity] = _nonnegative_int(row.get("oldest_event_time"), "oldest event time")
        if seen != requested.keys():
            mismatch = True
        return facts, mismatch

    def _prune_age_cache(self, inventory: Inventory) -> None:
        active_parts = inventory.active_parts
        for identity, (cached_bytes, _) in tuple(self._age_cache.items()):
            current = active_parts.get(identity)
            if current is None or current.bytes_on_disk != cached_bytes:
                del self._age_cache[identity]

    @staticmethod
    def _active_sizes(inventory: Inventory) -> dict[tuple[str, str], int]:
        return {
            identity: part.bytes_on_disk
            for identity, part in inventory.active_parts.items()
        }

    def _cached_candidates(self, inventory: Inventory) -> list[Candidate]:
        candidates = []
        for identity, part in inventory.active_parts.items():
            cached = self._age_cache.get(identity)
            if cached is None or cached[0] != part.bytes_on_disk:
                raise CandidateSnapshotChanged("An active log part has no verified event-time age")
            candidates.append(Candidate(part.table, part.name, cached[1], part.bytes_on_disk))
        return sorted(candidates, key=lambda item: (item.oldest_event_time, item.table, item.name))

    def consistent_candidates(self, inventory: Inventory) -> tuple[Inventory, list[Candidate]]:
        for attempt in range(MAX_AGE_SNAPSHOT_ATTEMPTS):
            if inventory.active_bytes <= self.budget_bytes:
                self._prune_age_cache(inventory)
                return inventory, []
            try:
                self._prune_age_cache(inventory)
                active_parts = inventory.active_parts
                requested = {
                    identity: part.bytes_on_disk
                    for identity, part in active_parts.items()
                    if self._age_cache.get(identity, (None, None))[0] != part.bytes_on_disk
                }
                new_ages: dict[tuple[str, str], int] = {}
                age_mismatch = False
                if requested:
                    new_ages, age_mismatch = self._read_age_facts(requested)

                # A fresh read-back is required even when all ages were cached.
                latest = self.inventory()
                if latest.active_bytes <= self.budget_bytes:
                    self._prune_age_cache(latest)
                    return latest, []

                before = self._active_sizes(inventory)
                after = self._active_sizes(latest)
                self._prune_age_cache(latest)
                latest_parts = latest.active_parts
                for identity, oldest_event_time in new_ages.items():
                    original_bytes = requested[identity]
                    current = latest_parts.get(identity)
                    if current is not None and current.bytes_on_disk == original_bytes:
                        self._age_cache[identity] = (original_bytes, oldest_event_time)

                if before != after or age_mismatch:
                    raise CandidateSnapshotChanged(
                        "Active ClickHouse log parts changed during age discovery or read-back", latest
                    )
                return latest, self._cached_candidates(latest)
            except CandidateSnapshotChanged as error:
                if attempt + 1 == MAX_AGE_SNAPSHOT_ATTEMPTS:
                    raise BudgetError(
                        "Active ClickHouse log parts kept changing during age discovery or read-back"
                    ) from error
                inventory = error.inventory or self.inventory()
        raise BudgetError("Could not obtain a consistent ClickHouse log-part age snapshot")

    @staticmethod
    def _report(inventory: Inventory, budget: int, *, dropped_parts: int, projected: bool = False) -> None:
        active = inventory.active_bytes
        inactive = inventory.inactive_bytes
        print(
            " ".join(
                (
                    f"retained_bytes={active}",
                    f"inactive_bytes={inactive}",
                    f"budget_bytes={budget}",
                    f"pending_gc_bytes={inactive}",
                    f"dropped_parts={dropped_parts}",
                    f"projected_retained_bytes={active}" if projected else "",
                )
            ).strip()
        )

    def _dry_run(self, inventory: Inventory) -> int:
        if inventory.active_bytes <= self.budget_bytes:
            print(
                f"retained_bytes={inventory.active_bytes} inactive_bytes={inventory.inactive_bytes} "
                f"budget_bytes={self.budget_bytes} pending_gc_bytes={inventory.inactive_bytes} "
                "planned_drop_parts=0 projected_retained_bytes=" + str(inventory.active_bytes)
            )
            return 0
        inventory, candidates = self.consistent_candidates(inventory)
        if inventory.active_bytes <= self.budget_bytes:
            print(
                f"retained_bytes={inventory.active_bytes} inactive_bytes={inventory.inactive_bytes} "
                f"budget_bytes={self.budget_bytes} pending_gc_bytes={inventory.inactive_bytes} "
                "planned_drop_parts=0 projected_retained_bytes=" + str(inventory.active_bytes)
            )
            return 0
        projected = inventory.active_bytes
        drops = 0
        for candidate in candidates:
            if projected <= self.budget_bytes:
                break
            projected -= candidate.bytes_on_disk
            drops += 1
        print(
            f"retained_bytes={inventory.active_bytes} inactive_bytes={inventory.inactive_bytes} "
            f"budget_bytes={self.budget_bytes} pending_gc_bytes={inventory.inactive_bytes} "
            f"planned_drop_parts={drops} projected_retained_bytes={projected}"
        )
        if projected > self.budget_bytes:
            raise BudgetError("Dry-run candidates cannot reduce active system logs to the requested budget")
        return 0

    def run(self, *, dry_run: bool = False) -> int:
        inventory = self.inventory()
        if dry_run:
            return self._dry_run(inventory)
        dropped = 0
        consecutive_nonprogress = 0
        race_retries = 0
        while True:
            if inventory.active_bytes <= self.budget_bytes:
                self._report(inventory, self.budget_bytes, dropped_parts=dropped)
                return 0
            if dropped >= MAX_ITERATIONS:
                break
            inventory, candidates = self.consistent_candidates(inventory)
            if inventory.active_bytes <= self.budget_bytes:
                continue
            if not candidates:
                raise BudgetError("Over-budget active logs have no verified event-time eviction candidate")
            candidate = candidates[0]
            try:
                self._execute(drop_part_sql(candidate.table, candidate.name))
            except ClickHouseError as error:
                if not error.is_missing_part(candidate.name):
                    raise
                latest = self.inventory()
                if (candidate.table, candidate.name) in latest.active_parts:
                    raise
                race_retries += 1
                if race_retries > 3:
                    raise BudgetError("Repeated part-merge races prevented a verified log eviction") from error
                inventory = latest
                continue

            latest = self.inventory()
            dropped += 1
            target_removed = (candidate.table, candidate.name) not in latest.active_parts
            if latest.active_bytes < inventory.active_bytes or target_removed:
                consecutive_nonprogress = 0
            else:
                consecutive_nonprogress += 1
                if consecutive_nonprogress >= 3:
                    raise BudgetError("ClickHouse log eviction made no verified read-back progress")
            inventory = latest

        raise BudgetError(f"Could not reduce active system logs to budget in {MAX_ITERATIONS} verified iterations")


def positive_budget(value: str) -> int:
    try:
        budget = int(value, 10)
    except ValueError as error:
        raise argparse.ArgumentTypeError("budget must be an integer byte count") from error
    if budget <= 0 or budget > MAX_BUDGET_BYTES:
        raise argparse.ArgumentTypeError(f"budget must be between 1 and {MAX_BUDGET_BYTES} bytes")
    return budget


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--budget-bytes", type=positive_budget, default=DEFAULT_BUDGET_BYTES)
    parser.add_argument("--dry-run", action="store_true", help="show the oldest-part plan without issuing DDL")
    args = parser.parse_args(argv)
    try:
        return LogBudgetManager(ClickHouseClient().execute, args.budget_bytes).run(dry_run=args.dry_run)
    except BudgetError as error:
        print(f"clickhouse system-log budget failed: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
