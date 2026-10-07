# Quick 13: Query Log Rolling 24-Hour Retention

## Scope

User authorizes reducing only system.query_log history from three days to
24 hours. Keep other system logs, application logs, logger level and file
rotation unchanged. Expected owned files: storage-policy.xml, deploy README,
ProductFeatures and task GSD records. Estimate 20-30 functional/doc lines.

## Acceptance And Sequence

1. Read production TTL, sizes, timestamp type, mutation load and active crawls.
2. Builder sets event_time + INTERVAL 24 HOUR DELETE, not a calendar-date TTL;
   document live ALTER for existing table and asynchronous physical cleanup.
3. Frozen candidate: structured XML isolation check, docs/diff validation,
   three fresh independent critics. Do not introduce runtime code or new tooling.
4. Commit/push owned files. Deploy only the policy/docs and live table TTL;
   use no-force CHECK_ONLY before the background TTL materialization. No app
   or ClickHouse restart, no force OPTIMIZE, no live crawl/rescan.
5. Verify persisted mounted policy, actual table DDL and 23/24/25-hour expiry
   boundaries, health/SELECT 1 and same container identities. Observe mutation
   completion and expired rows/disk delta without forced compaction. Record
   exact result, including asynchronous disk-cleanup limitations, and push
   GSD closeout. No secrets/query contents in evidence.

## Safety

Do not touch crawler/PageRank behavior or other retention settings. A shorter
TTL authorizes removal of expired query history, not production crawl data.
Restore policy/DDL if deployment fails; removed old query history is not
recoverable through a policy rollback. Active crawl blocks materialization,
never justify FORCE or interruption.
