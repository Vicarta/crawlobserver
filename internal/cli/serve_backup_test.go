package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/SEObserver/crawlobserver/internal/backup"
)

func TestScheduledSQLBackupOptions(t *testing.T) {
	original := &backup.SQLBackupOptions{
		Database:         "crawlobserver",
		ExcludeTableData: []string{"regenerable_cache"},
	}

	withCriticalExport := scheduledSQLBackupOptions(original, true)
	wantExcluded := []string{"regenerable_cache", "gsc_analytics"}
	if !reflect.DeepEqual(withCriticalExport.ExcludeTableData, wantExcluded) {
		t.Fatalf("excluded tables = %#v, want %#v", withCriticalExport.ExcludeTableData, wantExcluded)
	}
	if !reflect.DeepEqual(original.ExcludeTableData, []string{"regenerable_cache"}) {
		t.Fatalf("original options mutated: %#v", original.ExcludeTableData)
	}

	withoutCriticalExport := scheduledSQLBackupOptions(original, false)
	if !reflect.DeepEqual(withoutCriticalExport.ExcludeTableData, original.ExcludeTableData) {
		t.Fatalf("fallback must keep critical data in full backup: %#v", withoutCriticalExport.ExcludeTableData)
	}
}

func TestNextScheduledBackupDelay(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	interval := 24 * time.Hour

	t.Run("missing backup runs after startup delay", func(t *testing.T) {
		if got := nextScheduledBackupDelay(t.TempDir(), interval, now); got != scheduledBackupStartupDelay {
			t.Fatalf("delay = %s, want %s", got, scheduledBackupStartupDelay)
		}
	})

	t.Run("recent backup preserves daily schedule", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "backup-v1.0.0-20260812T060000.tar.gz")
		if err := os.WriteFile(path, []byte("backup"), 0o600); err != nil {
			t.Fatal(err)
		}
		createdAt := now.Add(-6 * time.Hour)
		if err := os.Chtimes(path, createdAt, createdAt); err != nil {
			t.Fatal(err)
		}

		if got := nextScheduledBackupDelay(dir, interval, now); got != 18*time.Hour {
			t.Fatalf("delay = %s, want 18h", got)
		}
	})

	t.Run("overdue backup runs after startup delay", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "backup-v1.0.0-20260810T120000.tar.gz")
		if err := os.WriteFile(path, []byte("backup"), 0o600); err != nil {
			t.Fatal(err)
		}
		createdAt := now.Add(-48 * time.Hour)
		if err := os.Chtimes(path, createdAt, createdAt); err != nil {
			t.Fatal(err)
		}

		if got := nextScheduledBackupDelay(dir, interval, now); got != scheduledBackupStartupDelay {
			t.Fatalf("delay = %s, want %s", got, scheduledBackupStartupDelay)
		}
	})
}

func TestNextScheduledBackupDelayAt(t *testing.T) {
	location, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatal(err)
	}
	interval := 24 * time.Hour

	t.Run("waits until configured time before daily run", func(t *testing.T) {
		now := time.Date(2026, 8, 31, 0, 10, 0, 0, location)
		if got := nextScheduledBackupDelayAt(t.TempDir(), interval, "00:20", location, now); got != 10*time.Minute {
			t.Fatalf("delay = %s, want 10m", got)
		}
	})

	t.Run("runs after startup delay when daily time was missed", func(t *testing.T) {
		now := time.Date(2026, 8, 31, 0, 30, 0, 0, location)
		if got := nextScheduledBackupDelayAt(t.TempDir(), interval, "00:20", location, now); got != scheduledBackupStartupDelay {
			t.Fatalf("delay = %s, want %s", got, scheduledBackupStartupDelay)
		}
	})

	t.Run("next run is tomorrow after today's backup", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "backup-v1.0.0-20260831T002100.tar.gz")
		if err := os.WriteFile(path, []byte("backup"), 0o600); err != nil {
			t.Fatal(err)
		}
		createdAt := time.Date(2026, 8, 31, 0, 21, 0, 0, location)
		if err := os.Chtimes(path, createdAt, createdAt); err != nil {
			t.Fatal(err)
		}

		now := time.Date(2026, 8, 31, 1, 0, 0, 0, location)
		want := 23*time.Hour + 20*time.Minute
		if got := nextScheduledBackupDelayAt(dir, interval, "00:20", location, now); got != want {
			t.Fatalf("delay = %s, want %s", got, want)
		}
	})

	t.Run("pre-update copy does not postpone today's full backup", func(t *testing.T) {
		dir := t.TempDir()
		previousFull := filepath.Join(dir, "backup-v1.0.0-20261013T002000.tar.gz")
		partial := filepath.Join(dir, "backup-v1.0.0-20261014T003000-pre-update.tar.gz")
		for _, path := range []string{previousFull, partial} {
			if err := os.WriteFile(path, []byte("backup"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		fullTime := time.Date(2026, 10, 13, 0, 20, 0, 0, location)
		partialTime := time.Date(2026, 10, 14, 0, 30, 0, 0, location)
		if err := os.Chtimes(previousFull, fullTime, fullTime); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(partial, partialTime, partialTime); err != nil {
			t.Fatal(err)
		}

		now := time.Date(2026, 10, 14, 0, 18, 30, 0, location)
		if got := nextScheduledBackupDelayAt(dir, interval, "00:20", location, now); got != 90*time.Second {
			t.Fatalf("configured-time delay = %s, want 90s despite later partial copy", got)
		}
		if got := nextScheduledBackupDelay(dir, interval, now); got != 90*time.Second {
			t.Fatalf("interval delay = %s, want 90s despite later partial copy", got)
		}
	})

	t.Run("interprets time in configured timezone", func(t *testing.T) {
		now := time.Date(2026, 8, 30, 21, 10, 0, 0, time.UTC)
		if got := nextScheduledBackupDelayAt(t.TempDir(), interval, "00:20", location, now); got != 10*time.Minute {
			t.Fatalf("delay = %s, want 10m", got)
		}
	})

	t.Run("keeps the configured wall clock across DST fallback", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "backup-v1.0.0-20261025T002100.tar.gz")
		if err := os.WriteFile(path, []byte("backup"), 0o600); err != nil {
			t.Fatal(err)
		}
		createdAt := time.Date(2026, 10, 25, 0, 21, 0, 0, location)
		if err := os.Chtimes(path, createdAt, createdAt); err != nil {
			t.Fatal(err)
		}
		now := time.Date(2026, 10, 25, 1, 0, 0, 0, location)
		want := time.Date(2026, 10, 26, 0, 20, 0, 0, location).Sub(now)
		if got := nextScheduledBackupDelayAt(dir, interval, "00:20", location, now); got != want {
			t.Fatalf("delay = %s, want %s", got, want)
		}
	})
}

func TestFinishScheduledBackupDoesNotPruneAfterFullBackupFailure(t *testing.T) {
	location, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatal(err)
	}
	backupDir := t.TempDir()
	exportDir := t.TempDir()
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, location)
	archiveDates := []time.Time{
		time.Date(2026, 10, 7, 0, 20, 0, 0, location),
		time.Date(2026, 10, 6, 0, 20, 0, 0, location),
		time.Date(2026, 10, 5, 0, 20, 0, 0, location),
		time.Date(2026, 10, 4, 0, 20, 0, 0, location),
		time.Date(2026, 9, 27, 0, 20, 0, 0, location),
	}
	var obsoleteArchive string
	for i, createdAt := range archiveDates {
		name := filepath.Join(backupDir, fmt.Sprintf("backup-v1.0.%d-%02d.tar.gz", i, i))
		if err := os.WriteFile(name, []byte("backup"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(name, createdAt, createdAt); err != nil {
			t.Fatal(err)
		}
		if i == len(archiveDates)-1 {
			obsoleteArchive = name
		}
	}
	exportDates := []time.Time{
		time.Date(2026, 10, 7, 0, 15, 0, 0, location),
		time.Date(2026, 10, 6, 0, 10, 0, 0, location),
		time.Date(2026, 10, 5, 0, 10, 0, 0, location),
		time.Date(2026, 10, 4, 0, 10, 0, 0, location),
		time.Date(2026, 9, 27, 0, 10, 0, 0, location),
	}
	var obsoleteExport string
	for i, createdAt := range exportDates {
		name := filepath.Join(exportDir, fmt.Sprintf("gsc_analytics_%s.jsonl.gz", createdAt.Format("20060102T150405")))
		if err := os.WriteFile(name, []byte("export"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(name, createdAt, createdAt); err != nil {
			t.Fatal(err)
		}
		if i == len(exportDates)-1 {
			obsoleteExport = name
		}
	}

	finishScheduledBackup(nil, errors.New("fixture archive failure"), backupDir, exportDir, 3, 1, location, now)
	for _, path := range []string{obsoleteArchive, obsoleteExport} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s was pruned after failed full backup: %v", path, err)
		}
	}
}

func TestFinishScheduledBackupPrunesAfterSuccessfulFullBackup(t *testing.T) {
	location, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatal(err)
	}
	backupDir := t.TempDir()
	exportDir := t.TempDir()
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, location)
	archiveDates := []time.Time{
		time.Date(2026, 10, 14, 0, 20, 0, 0, location),
		time.Date(2026, 10, 13, 0, 20, 0, 0, location),
		time.Date(2026, 10, 12, 0, 20, 0, 0, location),
		time.Date(2026, 10, 11, 0, 20, 0, 0, location),
		time.Date(2026, 10, 10, 0, 20, 0, 0, location),
		time.Date(2026, 10, 4, 0, 20, 0, 0, location),
	}
	archivePaths := make([]string, len(archiveDates))
	for i, createdAt := range archiveDates {
		path := filepath.Join(backupDir, fmt.Sprintf("backup-fixture-%s.tar.gz", createdAt.Format("20060102T150405")))
		if err := os.WriteFile(path, []byte("backup"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, createdAt, createdAt); err != nil {
			t.Fatal(err)
		}
		archivePaths[i] = path
	}
	partialArchive := filepath.Join(backupDir, "backup-fixture-20261004T002000-pre-update.tar.gz")
	partialTime := time.Date(2026, 10, 4, 0, 20, 0, 0, location)
	if err := os.WriteFile(partialArchive, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(partialArchive, partialTime, partialTime); err != nil {
		t.Fatal(err)
	}
	exportDates := []time.Time{
		time.Date(2026, 10, 14, 0, 15, 0, 0, location), // nearest earlier export for the October 14 full archive
		time.Date(2026, 10, 14, 1, 0, 0, 0, location),  // latest Oct 14 calendar export, after the archive
		time.Date(2026, 10, 13, 0, 10, 0, 0, location),
		time.Date(2026, 10, 12, 0, 10, 0, 0, location),
		time.Date(2026, 10, 11, 0, 10, 0, 0, location),
		time.Date(2026, 10, 10, 0, 10, 0, 0, location),
		time.Date(2026, 10, 4, 0, 15, 0, 0, location), // only the older partial copy is nearby
		time.Date(2026, 10, 4, 0, 10, 0, 0, location),
	}
	exportPaths := make([]string, len(exportDates))
	for i, createdAt := range exportDates {
		path := filepath.Join(exportDir, fmt.Sprintf("gsc_analytics_%s.jsonl.gz", createdAt.Format("20060102T150405")))
		if err := os.WriteFile(path, []byte("export"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, createdAt, createdAt); err != nil {
			t.Fatal(err)
		}
		exportPaths[i] = path
	}

	finishScheduledBackup(&backup.BackupInfo{Filename: "fixture-created.tar.gz", Size: 100}, nil, backupDir, exportDir, 3, 1, location, now)

	for _, i := range []int{0, 1, 2, 3} { // three daily dates plus the previous week's Oct 11 archive
		if _, err := os.Stat(archivePaths[i]); err != nil {
			t.Errorf("retained archive %s was removed: %v", archivePaths[i], err)
		}
	}
	for _, i := range []int{4, 5} {
		if _, err := os.Stat(archivePaths[i]); !os.IsNotExist(err) {
			t.Errorf("obsolete archive %s stat error = %v, want not-exist", archivePaths[i], err)
		}
	}
	if _, err := os.Stat(partialArchive); err != nil {
		t.Errorf("separately retained pre-update archive was removed: %v", err)
	}
	for _, i := range []int{0, 1, 2, 3, 4} { // nearest earlier dependencies and latest calendar exports
		if _, err := os.Stat(exportPaths[i]); err != nil {
			t.Errorf("retained critical export %s was removed: %v", exportPaths[i], err)
		}
	}
	for _, i := range []int{5, 6, 7} {
		if _, err := os.Stat(exportPaths[i]); !os.IsNotExist(err) {
			t.Errorf("obsolete critical export %s stat error = %v, want not-exist", exportPaths[i], err)
		}
	}
}
