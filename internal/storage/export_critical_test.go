package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGSCAnalyticsDailyChunks(t *testing.T) {
	minDate := time.Date(2026, 6, 7, 15, 30, 0, 0, time.FixedZone("test", 3*60*60))
	maxDate := time.Date(2026, 6, 9, 2, 0, 0, 0, time.UTC)

	chunks := gscAnalyticsDailyChunks("project-1", minDate, maxDate)
	if len(chunks) != 3 {
		t.Fatalf("len(chunks) = %d, want 3", len(chunks))
	}

	wantStarts := []time.Time{
		time.Date(2026, 6, 7, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC),
	}
	for i, want := range wantStarts {
		if chunks[i].ProjectID != "project-1" {
			t.Fatalf("chunks[%d].ProjectID = %q, want project-1", i, chunks[i].ProjectID)
		}
		if !chunks[i].StartDate.Equal(want) {
			t.Fatalf("chunks[%d].StartDate = %s, want %s", i, chunks[i].StartDate, want)
		}
		if !chunks[i].EndDate.Equal(want.AddDate(0, 0, 1)) {
			t.Fatalf("chunks[%d].EndDate = %s, want %s", i, chunks[i].EndDate, want.AddDate(0, 0, 1))
		}
	}
}

func TestGSCAnalyticsDailyChunksRejectsInvalidRange(t *testing.T) {
	minDate := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	maxDate := time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC)

	if chunks := gscAnalyticsDailyChunks("project-1", minDate, maxDate); chunks != nil {
		t.Fatalf("chunks = %#v, want nil", chunks)
	}
	if chunks := gscAnalyticsDailyChunks("", minDate, minDate); chunks != nil {
		t.Fatalf("chunks = %#v, want nil for empty project", chunks)
	}
}

func TestPruneWeeklyCriticalExportsPreservesCalendarAndBackupDependencies(t *testing.T) {
	location, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, location)
	exports := []struct {
		name string
		when time.Time
	}{
		{"gsc_analytics_20261007T001500.jsonl.gz", time.Date(2026, 10, 7, 0, 15, 0, 0, location)},
		{"gsc_analytics_20261007T010000.jsonl.gz", time.Date(2026, 10, 7, 1, 0, 0, 0, location)},
		{"gsc_analytics_20261006T001000.jsonl.gz", time.Date(2026, 10, 6, 0, 10, 0, 0, location)},
		{"gsc_analytics_20261005T001000.jsonl.gz", time.Date(2026, 10, 5, 0, 10, 0, 0, location)},
		{"gsc_analytics_20261004T001000.jsonl.gz", time.Date(2026, 10, 4, 0, 10, 0, 0, location)},
		{"gsc_analytics_20261003T001000.jsonl.gz", time.Date(2026, 10, 3, 0, 10, 0, 0, location)},
	}
	for _, export := range exports {
		path := filepath.Join(dir, export.name)
		if err := os.WriteFile(path, []byte("export"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, export.when, export.when); err != nil {
			t.Fatal(err)
		}
	}
	invalidName := filepath.Join(dir, "gsc_analytics_not-a-timestamp.jsonl.gz")
	unknownName := filepath.Join(dir, "not_critical_20261003T001000.jsonl.gz")
	for _, path := range []string{invalidName, unknownName} {
		if err := os.WriteFile(path, []byte("preserve"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	archiveTimes := []time.Time{
		time.Date(2026, 10, 7, 0, 20, 0, 0, location),
		time.Date(2026, 10, 6, 0, 20, 0, 0, location),
		time.Date(2026, 10, 5, 0, 20, 0, 0, location),
		time.Date(2026, 10, 4, 0, 20, 0, 0, location),
	}
	deleted, err := PruneWeeklyCriticalExports(dir, location, archiveTimes, now)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted exports = %d, want only the unneeded October 3 export", deleted)
	}
	for _, name := range []string{
		"gsc_analytics_20261007T001500.jsonl.gz", // nearest export before the 00:20 local archive
		"gsc_analytics_20261007T010000.jsonl.gz", // latest export for the local date
		"gsc_analytics_20261006T001000.jsonl.gz",
		"gsc_analytics_20261005T001000.jsonl.gz",
		"gsc_analytics_20261004T001000.jsonl.gz", // previous calendar week
		filepath.Base(invalidName),
		filepath.Base(unknownName),
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected export %s to remain: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "gsc_analytics_20261003T001000.jsonl.gz")); !os.IsNotExist(err) {
		t.Fatalf("unneeded October 3 export stat error = %v, want not-exist", err)
	}
}

func TestPruneWeeklyCriticalExportsKeepsLocalMidnightArchiveDependency(t *testing.T) {
	location, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, location)
	exports := []struct {
		name string
		when time.Time
	}{
		{"gsc_analytics_20261014T001000.jsonl.gz", time.Date(2026, 10, 14, 0, 10, 0, 0, location)},
		{"gsc_analytics_20261013T001000.jsonl.gz", time.Date(2026, 10, 13, 0, 10, 0, 0, location)},
		{"gsc_analytics_20261012T001000.jsonl.gz", time.Date(2026, 10, 12, 0, 10, 0, 0, location)},
		{"gsc_analytics_20261011T001000.jsonl.gz", time.Date(2026, 10, 11, 0, 10, 0, 0, location)},
		{"gsc_analytics_20261004T235500.jsonl.gz", time.Date(2026, 10, 4, 23, 55, 0, 0, location)},
		{"gsc_analytics_20261004T233000.jsonl.gz", time.Date(2026, 10, 4, 23, 30, 0, 0, location)},
	}
	for _, export := range exports {
		path := filepath.Join(dir, export.name)
		if err := os.WriteFile(path, []byte("export"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, export.when, export.when); err != nil {
			t.Fatal(err)
		}
	}

	archiveTime := time.Date(2026, 10, 5, 0, 20, 0, 0, location)
	deleted, err := PruneWeeklyCriticalExports(dir, location, []time.Time{archiveTime}, now)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted exports = %d, want only the older October 4 export", deleted)
	}
	for _, name := range []string{
		"gsc_analytics_20261014T001000.jsonl.gz",
		"gsc_analytics_20261013T001000.jsonl.gz",
		"gsc_analytics_20261012T001000.jsonl.gz",
		"gsc_analytics_20261011T001000.jsonl.gz",
		"gsc_analytics_20261004T235500.jsonl.gz",
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected export %s to remain: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "gsc_analytics_20261004T233000.jsonl.gz")); !os.IsNotExist(err) {
		t.Fatalf("older October 4 export stat error = %v, want not-exist", err)
	}
}

func TestPruneWeeklyCriticalExportsFailsClosedWithoutTimezone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gsc_analytics_20261007T001500.jsonl.gz")
	if err := os.WriteFile(path, []byte("export"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PruneWeeklyCriticalExports(dir, nil, nil, time.Now()); err == nil {
		t.Fatal("expected missing timezone error")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("export was removed after policy error: %v", err)
	}
}
