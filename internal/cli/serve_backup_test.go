package cli

import (
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

	t.Run("interprets time in configured timezone", func(t *testing.T) {
		now := time.Date(2026, 8, 30, 21, 10, 0, 0, time.UTC)
		if got := nextScheduledBackupDelayAt(t.TempDir(), interval, "00:20", location, now); got != 10*time.Minute {
			t.Fatalf("delay = %s, want 10m", got)
		}
	})
}
