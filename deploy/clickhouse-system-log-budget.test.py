import importlib.util
import json
import re
import subprocess
import unittest
from pathlib import Path
from unittest import mock


HERE = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("system_log_budget", HERE / "clickhouse-system-log-budget.py")
budget = importlib.util.module_from_spec(SPEC)
import sys

sys.modules[SPEC.name] = budget
SPEC.loader.exec_module(budget)


class FakeClickHouse:
    def __init__(self, parts, ages=None):
        self.parts = [dict(part) for part in parts]
        self.ages = dict(ages or {})
        self.queries = []
        self.drops = []
        self.fail_drop = None
        self.merge_before_drop = False
        self.noop_drop = False
        self.before_age_query = None
        self.after_age_query = None
        self.duplicate_age_rows = False

    def __call__(self, sql):
        self.queries.append(sql)
        if sql.startswith("SELECT table, name,"):
            rows = [
                {
                    "table": part["table"],
                    "name": part["name"],
                    "active": int(part["active"]),
                    "bytes_on_disk": str(part["bytes_on_disk"]),
                }
                for part in self.parts
            ]
            return "".join(json.dumps(row) + "\n" for row in rows)
        if "AS oldest_event_time" in sql:
            if self.before_age_query is not None:
                self.before_age_query()
            rows = []
            for select in sql.split(" UNION ALL "):
                match = re.search(
                    r"FROM system.`([a-z0-9_]+)` WHERE _part IN \(([^)]*)\)", select
                )
                if not match:
                    raise AssertionError(f"Missing explicit age filter: {select}")
                table = match.group(1)
                requested = set(re.findall(r"'([A-Za-z0-9_]+)'", match.group(2)))
                for part in self.parts:
                    key = (table, part["name"])
                    if part["table"] == table and part["active"] and key in self.ages and key[1] in requested:
                        rows.append(
                            {
                                "table": table,
                                "part": part["name"],
                                "oldest_event_time": self.ages[key],
                            }
                        )
            rows.sort(key=lambda row: (row["oldest_event_time"], row["table"], row["part"]))
            if self.duplicate_age_rows and rows:
                rows.append(dict(rows[0]))
            output = "".join(json.dumps(row) + "\n" for row in rows)
            if self.after_age_query is not None:
                self.after_age_query()
            return output
        match = re.fullmatch(r"ALTER TABLE system.`([a-z0-9_]+)` DROP PART '([A-Za-z0-9_]+)'", sql)
        if match:
            key = (match.group(1), match.group(2))
            self.drops.append(key)
            if self.fail_drop is not None:
                error = self.fail_drop
                self.fail_drop = None
                if self.merge_before_drop:
                    self.parts = [
                        part for part in self.parts if (part["table"], part["name"]) != key
                    ]
                raise error
            if not self.noop_drop:
                for part in self.parts:
                    if (part["table"], part["name"]) == key:
                        part["active"] = False
            return ""
        raise AssertionError(f"Unexpected SQL: {sql}")


def part(name, size, *, table="query_log", active=True):
    return {"table": table, "name": name, "active": active, "bytes_on_disk": size}


class BudgetValidationTests(unittest.TestCase):
    def manager(self, parts, ages=None, budget_bytes=budget.DEFAULT_BUDGET_BYTES):
        fake = FakeClickHouse(parts, ages)
        return budget.LogBudgetManager(fake, budget_bytes), fake

    def test_default_budget_is_decimal_150_mb(self):
        self.assertEqual(budget.DEFAULT_BUDGET_BYTES, 150_000_000)

    def test_below_and_equal_budget_skip_age_query(self):
        for size in (99, 100):
            with self.subTest(size=size):
                manager, fake = self.manager([part("p1_1_1_0", size)], budget_bytes=100)
                self.assertEqual(manager.run(), 0)
                self.assertFalse(any("AS oldest_event_time" in sql for sql in fake.queries))
                self.assertEqual(fake.drops, [])

    def test_oldest_event_time_is_used_for_eviction(self):
        parts = [part("newer_event", 60), part("older_event", 50)]
        ages = {("query_log", "newer_event"): 900, ("query_log", "older_event"): 100}
        manager, fake = self.manager(parts, ages, budget_bytes=60)
        self.assertEqual(manager.run(), 0)
        self.assertEqual(fake.drops, [("query_log", "older_event")])
        age_sql = next(sql for sql in fake.queries if "AS oldest_event_time" in sql)
        self.assertIn("min(event_time)", age_sql)
        self.assertIn("GROUP BY _part", age_sql)
        self.assertNotIn("modification_time", age_sql)

    def test_inactive_only_bytes_are_pending_gc_not_a_reason_to_delete(self):
        manager, fake = self.manager(
            [part("old_inactive", 900, active=False)], budget_bytes=1
        )
        self.assertEqual(manager.run(), 0)
        self.assertEqual(fake.drops, [])

    def test_numeric_rotated_log_table_is_allowed(self):
        self.assertTrue(budget.is_allowed_table("query_log_3"))
        manager, fake = self.manager(
            [part("p1_1_1_0", 101, table="query_log_3")],
            {("query_log_3", "p1_1_1_0"): 1},
            budget_bytes=100,
        )
        self.assertEqual(manager.run(), 0)
        self.assertEqual(fake.drops, [("query_log_3", "p1_1_1_0")])

    def test_opentelemetry_span_log_is_outside_the_timestamp_allowlist(self):
        self.assertFalse(budget.is_allowed_table("opentelemetry_span_log"))
        self.assertNotIn("opentelemetry_span_log", budget.inventory_sql())

    def test_unknown_and_business_tables_are_rejected(self):
        self.assertFalse(budget.is_allowed_table("application_logs"))
        self.assertFalse(budget.is_allowed_table("query_log_3x"))
        self.assertFalse(budget.is_allowed_table("query_log; DROP TABLE app"))
        manager, _ = self.manager([part("p1_1_1_0", 1)])
        manager._execute = lambda _: json.dumps(
            {"table": "application_logs", "name": "p1_1_1_0", "active": 1, "bytes_on_disk": "1"}
        )
        with self.assertRaises(budget.BudgetError):
            manager.inventory()

    def test_malformed_part_and_age_rows_fail_closed(self):
        manager, _ = self.manager([part("bad-name", 2)])
        with self.assertRaises(budget.BudgetError):
            manager.inventory()
        self.assertEqual(
            budget._json_rows('{"x":1}\n')[0],
            {"x": 1},
        )
        with self.assertRaises(budget.BudgetError):
            budget._json_rows("not-json\n")
        with self.assertRaises(budget.BudgetError):
            budget.drop_part_sql("query_log", "p1'; DROP TABLE app")

    def test_oversized_single_part_can_be_evicted_with_physical_overshoot(self):
        manager, fake = self.manager(
            [part("large_part", 180)], {("query_log", "large_part"): 1}, budget_bytes=150
        )
        self.assertEqual(manager.run(), 0)
        self.assertEqual(fake.drops, [("query_log", "large_part")])
        self.assertEqual(manager.inventory().inactive_bytes, 180)

    def test_dry_run_reports_projected_retained_bytes_without_ddl(self):
        parts = [part("older", 60), part("newer", 70)]
        ages = {("query_log", "older"): 1, ("query_log", "newer"): 2}
        manager, fake = self.manager(parts, ages, budget_bytes=100)
        with mock.patch("builtins.print") as output:
            self.assertEqual(manager.run(dry_run=True), 0)
        self.assertEqual(fake.drops, [])
        self.assertIn("projected_retained_bytes=70", output.call_args.args[0])

    def test_dry_run_fails_if_age_candidates_cannot_reach_budget(self):
        manager, fake = self.manager(
            [part("only_candidate", 60), part("unknown_age", 50)],
            {("query_log", "only_candidate"): 1},
            budget_bytes=40,
        )
        with self.assertRaisesRegex(budget.BudgetError, "kept changing"):
            manager.run(dry_run=True)
        self.assertEqual(fake.drops, [])

    def test_merge_during_age_query_retries_and_drops_the_merged_oldest_part(self):
        parts = [part("older_a", 60), part("older_b", 60), part("newer", 70)]
        ages = {
            ("query_log", "older_a"): 1,
            ("query_log", "older_b"): 2,
            ("query_log", "newer"): 3,
        }
        manager, fake = self.manager(parts, ages, budget_bytes=150)

        def merge_old_parts():
            fake.before_age_query = None
            fake.parts = [
                item for item in fake.parts if item["name"] not in {"older_a", "older_b"}
            ]
            fake.parts.append(part("older_merged", 120))
            fake.ages[("query_log", "older_merged")] = 1

        fake.before_age_query = merge_old_parts
        self.assertEqual(manager.run(), 0)
        self.assertEqual(fake.drops, [("query_log", "older_merged")])
        self.assertNotIn(("query_log", "newer"), fake.drops)
        age_queries = [sql for sql in fake.queries if "AS oldest_event_time" in sql]
        self.assertEqual(len(age_queries), 2)
        self.assertIn("_part IN ('older_merged')", age_queries[1])
        self.assertNotIn("'newer'", age_queries[1])

    def test_age_retry_that_falls_under_budget_skips_another_age_query(self):
        manager, fake = self.manager(
            [part("merged_out", 60), part("newer", 50)],
            {("query_log", "merged_out"): 1, ("query_log", "newer"): 2},
            budget_bytes=100,
        )

        def merge_to_under_budget():
            fake.before_age_query = None
            fake.parts = [item for item in fake.parts if item["name"] == "newer"]

        fake.before_age_query = merge_to_under_budget
        self.assertEqual(manager.run(dry_run=True), 0)
        self.assertEqual(len([sql for sql in fake.queries if "AS oldest_event_time" in sql]), 1)
        self.assertEqual(fake.drops, [])

    def test_ttl_removal_after_age_readback_prevents_deleting_the_remaining_old_part(self):
        manager, fake = self.manager(
            [part("old", 60), part("new", 60)],
            {("query_log", "old"): 1, ("query_log", "new"): 2},
            budget_bytes=100,
        )

        def ttl_removes_new_part():
            fake.after_age_query = None
            fake.parts = [item for item in fake.parts if item["name"] != "new"]

        fake.after_age_query = ttl_removes_new_part
        self.assertEqual(manager.run(), 0)
        self.assertEqual(fake.drops, [])
        self.assertEqual(len([sql for sql in fake.queries if "AS oldest_event_time" in sql]), 1)

    def test_same_identity_size_change_retries_age_snapshot_before_drop(self):
        manager, fake = self.manager(
            [part("old", 60), part("new", 60)],
            {("query_log", "old"): 1, ("query_log", "new"): 2},
            budget_bytes=100,
        )

        def grow_new_part():
            fake.after_age_query = None
            next(item for item in fake.parts if item["name"] == "new")["bytes_on_disk"] = 90

        fake.after_age_query = grow_new_part
        self.assertEqual(manager.run(), 0)
        self.assertEqual(fake.drops, [("query_log", "old")])
        age_queries = [sql for sql in fake.queries if "AS oldest_event_time" in sql]
        self.assertEqual(len(age_queries), 2)
        self.assertIn("_part IN ('new')", age_queries[1])
        self.assertNotIn("'old'", age_queries[1])

    def test_cached_ages_are_reused_across_successful_drop_iterations(self):
        manager, fake = self.manager(
            [part("oldest", 60), part("middle", 60), part("newest", 60)],
            {
                ("query_log", "oldest"): 1,
                ("query_log", "middle"): 2,
                ("query_log", "newest"): 3,
            },
            budget_bytes=100,
        )
        self.assertEqual(manager.run(), 0)
        self.assertEqual(fake.drops, [("query_log", "oldest"), ("query_log", "middle")])
        self.assertEqual(len([sql for sql in fake.queries if "AS oldest_event_time" in sql]), 1)

    def test_repeated_missing_ages_fail_after_three_snapshot_attempts(self):
        manager, fake = self.manager(
            [part("has_no_age", 60), part("has_age", 50)],
            {("query_log", "has_age"): 1},
            budget_bytes=40,
        )
        with self.assertRaisesRegex(budget.BudgetError, "kept changing"):
            manager.run()
        self.assertEqual(len([sql for sql in fake.queries if "AS oldest_event_time" in sql]), 3)
        self.assertEqual(fake.drops, [])

    def test_repeated_identity_churn_is_bounded_and_never_drops(self):
        manager, fake = self.manager(
            [part("changing_0_1_1_0", 200)],
            {("query_log", f"changing_{i}_1_1_0"): i for i in range(5)},
            budget_bytes=100,
        )
        sequence = iter(range(1, 4))

        def rename_part():
            fake.parts[0]["name"] = f"changing_{next(sequence)}_1_1_0"

        fake.before_age_query = rename_part
        with self.assertRaisesRegex(budget.BudgetError, "kept changing"):
            manager.run()
        self.assertEqual(len([sql for sql in fake.queries if "AS oldest_event_time" in sql]), 3)
        self.assertEqual(fake.drops, [])

    def test_duplicate_age_rows_fail_closed(self):
        manager, fake = self.manager(
            [part("duplicate_age", 101)], {("query_log", "duplicate_age"): 1}, budget_bytes=100
        )
        fake.duplicate_age_rows = True
        with self.assertRaisesRegex(budget.BudgetError, "kept changing"):
            manager.run(dry_run=True)
        self.assertEqual(fake.drops, [])

    def test_missing_part_merge_race_is_reinventoried_and_retried(self):
        parts = [part("merged_away", 60), part("still_here", 60)]
        ages = {("query_log", "merged_away"): 1, ("query_log", "still_here"): 2}
        manager, fake = self.manager(parts, ages, budget_bytes=50)
        fake.fail_drop = budget.ClickHouseError("drop race", stderr="No such part merged_away")
        fake.merge_before_drop = True
        self.assertEqual(manager.run(), 0)
        self.assertEqual(fake.drops, [("query_log", "merged_away"), ("query_log", "still_here")])

    def test_non_missing_provider_error_is_not_swallowed(self):
        manager, fake = self.manager(
            [part("p1_1_1_0", 101)], {("query_log", "p1_1_1_0"): 1}, budget_bytes=100
        )
        fake.fail_drop = budget.ClickHouseError("failed", stderr="Access denied")
        with self.assertRaisesRegex(budget.ClickHouseError, "failed"):
            manager.run()
        self.assertEqual(len(fake.drops), 1)

    def test_no_verified_progress_is_bounded(self):
        manager, fake = self.manager(
            [part("stuck_part", 101)], {("query_log", "stuck_part"): 1}, budget_bytes=100
        )
        fake.noop_drop = True
        with self.assertRaisesRegex(budget.BudgetError, "no verified"):
            manager.run()
        self.assertEqual(len(fake.drops), 3)

    def test_maximum_iterations_is_bounded(self):
        with mock.patch.object(budget, "MAX_ITERATIONS", 2):
            parts = [part(f"p{i}_1_1_0", 2) for i in range(3)]
            ages = {("query_log", p["name"]): i for i, p in enumerate(parts)}
            manager, fake = self.manager(parts, ages, budget_bytes=1)
            with self.assertRaisesRegex(budget.BudgetError, "2 verified iterations"):
                manager.run()
        self.assertEqual(len(fake.drops), 2)

    def test_budget_argument_validation(self):
        self.assertEqual(budget.positive_budget("150000000"), 150_000_000)
        for value in ("0", "-1", str(budget.MAX_BUDGET_BYTES + 1), "1;drop", "1.5"):
            with self.subTest(value=value), self.assertRaises(Exception):
                budget.positive_budget(value)

    def test_client_sends_stdin_without_putting_sql_or_password_in_argv(self):
        client = budget.ClickHouseClient()
        result = subprocess.CompletedProcess([], 0, stdout="ok\n", stderr="")
        with mock.patch.object(budget.subprocess, "run", return_value=result) as run:
            self.assertEqual(client.execute("SELECT 42"), "ok\n")
        argv = run.call_args.args[0]
        self.assertIn("-i", argv)
        self.assertIn('"$CLICKHOUSE_PASSWORD"', argv[-1])
        self.assertNotIn("SELECT 42", " ".join(argv))
        self.assertEqual(run.call_args.kwargs["input"], "SELECT 42")
        self.assertIn("--max_threads 1", argv[-1])
        self.assertIn("--max_execution_time 20", argv[-1])
        self.assertIn("--log_queries 0", argv[-1])
        self.assertEqual(run.call_args.kwargs["timeout"], 25)

    def test_query_execution_settings_are_bounded(self):
        settings = budget._query_settings()
        self.assertIn("max_threads = 1", settings)
        self.assertIn("max_execution_time = 20", settings)
        self.assertIn("log_queries = 0", settings)
        self.assertEqual(budget.DOCKER_COMMAND_TIMEOUT_SECONDS, 25)
        self.assertEqual(budget.MAX_RUNTIME_SECONDS, 110)

    def test_systemd_timer_and_installer_wire_the_budgeter(self):
        service = (HERE / "systemd/crawlobserver-clickhouse-log-budget.service").read_text()
        timer = (HERE / "systemd/crawlobserver-clickhouse-log-budget.timer").read_text()
        installer = (HERE / "install-clickhouse-log-retention.sh").read_text()
        self.assertIn("TimeoutStartSec=120s", service)
        self.assertIn("--budget-bytes 150000000", service)
        self.assertIn("OnUnitActiveSec=5min", timer)
        self.assertIn("Type=oneshot", service)
        self.assertIn("enable --now crawlobserver-clickhouse-log-budget.timer", installer)
        self.assertIn("python3", installer)
        self.assertNotIn("restart clickhouse", installer.lower())


if __name__ == "__main__":
    unittest.main()
