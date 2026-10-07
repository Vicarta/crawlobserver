# Quick 15: Daily And Previous-Week Backup Retention

## Owner Request

Keep three recent daily backups and one previous-week copy. Preserve the
production 00:20 Europe/Kyiv schedule and deployment safety. Calendar week is
Monday-Sunday by default; an optional clarification was requested without
blocking work. No archive format, restore workflow, frontend, task scheduler
architecture or unrelated changes.

## Scope And Selection

Opt-in backup.retain_weekly=1 with existing retain=3 selects latest archives
on three distinct local dates, plus latest previous-calendar-week archive.
Deduplicate a file serving both buckets; legacy retain-only configs remain
count-based. Use modification times as current archive listing does, converted
to the configured timezone; filenames currently carry container UTC timestamps.

Apply consistent archive pruning after scheduler, manual and pre-update copies.
Critical exports must retain the recovery dependencies of retained archives;
calendar-mode scheduled export pruning occurs only after full archive success.
Manual export must not delete the weekly archive's nearest earlier recovery
copy. Preserve failed-export fallback that includes GSC data in the full archive.

Independent review found lightweight SQLite/config pre-update snapshots could
displace full recovery archives when deduplicating a local date. New weekly-mode
pre-update snapshots use a filename suffix and a separate legacy count pool;
archive contents, generic listing and restore remain unchanged. Historical
unmarked partial snapshots are not reclassified. Production has only SQL
archives and Docker self-update is disabled. No metadata parser or migration.

Expected files: backup/config/cli/server/storage and focused tests, production
config example, README and ProductFeatures. Estimated 350-500 lines; sole
builder Luna/max. Root owns GSD and production deployment. Fresh Code/Tests/UX
Sol/high critics review the frozen complete result. No subagent commits/deploys.

## Acceptance And Release

1. Fixture-only checks cover distinct days, calendar/week/year/DST boundaries,
   legacy count mode, manual paths, recovery dependency preservation and failure
   behavior; relevant Go race/vet/build and existing frontend gates pass.
2. Commit and push only owned files. No-force production preflight, update
   committed source and only backup retain/retain_weekly settings, app image
   build, second preflight, safe app-only restart. No live crawl/rescan/backup,
   restore, backup deletion or ClickHouse restart during acceptance.
3. Confirm API health, unchanged healthy ClickHouse, scheduler startup with
   00:20 Europe/Kyiv and 3+1 policy; existing archive/export names/sizes preserved.
   Config changes use structured YAML read/write locally without printing secrets.
4. Record production acceptance, commit and push GSD closeout.

## Initial Read-Only Evidence

Production has retain=2 and no weekly option. Two archive/export generations
exist for local October 7 and October 6, 2026. No previous-week generation is
available; deleted historical backups cannot be reconstructed by retention.
The third daily copy and subsequent weekly bucket populate from future runs;
do not make a duplicate file and falsely present it as older recovery evidence.
