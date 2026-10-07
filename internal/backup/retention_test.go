package backup

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupsToRetainWeeklyPolicy(t *testing.T) {
	location, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, location)
	backups := []BackupInfo{
		retentionArchive("today-old", time.Date(2026, 10, 7, 0, 30, 0, 0, location)),
		retentionArchive("today-new", time.Date(2026, 10, 7, 1, 0, 0, 0, location)),
		retentionArchive("yesterday", time.Date(2026, 10, 6, 0, 20, 0, 0, location)),
		retentionArchive("monday", time.Date(2026, 10, 5, 0, 20, 0, 0, location)),
		retentionArchive("sunday", time.Date(2026, 10, 4, 23, 50, 0, 0, location)),
		retentionArchive("previous-sunday", time.Date(2026, 9, 27, 23, 50, 0, 0, location)),
	}

	got, err := BackupsToRetain(backups, 3, 1, location, now)
	if err != nil {
		t.Fatal(err)
	}
	if names := retentionNames(got); !equalRetentionNames(names, []string{"today-new", "yesterday", "monday", "sunday"}) {
		t.Fatalf("retained archives = %v, want latest three dates plus previous week Sunday", names)
	}
}

func TestBackupsToRetainWeeklySeparatesPreUpdateCopies(t *testing.T) {
	location, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, location)
	backups := []BackupInfo{
		retentionArchive("backup-v1-20261014T100000-pre-update.tar.gz", time.Date(2026, 10, 14, 10, 0, 0, 0, location)),
		retentionArchive("full-wednesday", time.Date(2026, 10, 14, 0, 20, 0, 0, location)),
		retentionArchive("full-tuesday", time.Date(2026, 10, 13, 0, 20, 0, 0, location)),
		retentionArchive("full-monday", time.Date(2026, 10, 12, 0, 20, 0, 0, location)),
		retentionArchive("full-previous-sunday", time.Date(2026, 10, 11, 23, 30, 0, 0, location)),
		retentionArchive("backup-v1-20261010T100000-pre-update.tar.gz", time.Date(2026, 10, 10, 10, 0, 0, 0, location)),
	}

	got, err := BackupsToRetain(backups, 1, 1, location, now)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"backup-v1-20261014T100000-pre-update.tar.gz",
		"full-wednesday",
		"full-tuesday",
		"full-monday",
		"full-previous-sunday",
	}
	if names := retentionNames(got); !equalRetentionNames(names, want) {
		t.Fatalf("retained archives = %v, want separate partial pool and full calendar set %v", names, want)
	}
}

func TestIsPreUpdateBackupFilename(t *testing.T) {
	for _, test := range []struct {
		name string
		want bool
	}{
		{"backup-v1.2.3-20261014T100000-pre-update.tar.gz", true},
		{"backup-v1.2.3-20261014T100000.tar.gz", false},
		{"other-v1.2.3-20261014T100000-pre-update.tar.gz", false},
		{"backup-v1.2.3-20261014T100000-pre-update-extra.tar.gz", false},
	} {
		if got := IsPreUpdateBackupFilename(test.name); got != test.want {
			t.Errorf("IsPreUpdateBackupFilename(%q) = %v, want %v", test.name, got, test.want)
		}
	}
}

func TestCreateMarksPreUpdateBackupFilename(t *testing.T) {
	dir := t.TempDir()
	info, err := Create(BackupOptions{BackupDir: dir, PreUpdate: true}, "v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if !IsPreUpdateBackupFilename(info.Filename) {
		t.Fatalf("filename = %q, want marked pre-update suffix", info.Filename)
	}
	listed, err := ListBackups(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Version != "v1.2.3" {
		t.Fatalf("listed backups = %#v, want one recognized archive with version v1.2.3", listed)
	}
}

func TestBackupsToRetainWeeklyCalendarBoundaries(t *testing.T) {
	location, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("previous week ends at local Monday midnight across year", func(t *testing.T) {
		now := time.Date(2027, 1, 6, 12, 0, 0, 0, location)
		backups := []BackupInfo{
			retentionArchive("wednesday", time.Date(2027, 1, 6, 0, 20, 0, 0, location)),
			retentionArchive("tuesday", time.Date(2027, 1, 5, 0, 20, 0, 0, location)),
			retentionArchive("monday", time.Date(2027, 1, 4, 0, 20, 0, 0, location)),
			retentionArchive("sunday-before-midnight", time.Date(2027, 1, 3, 23, 59, 0, 0, location)),
			retentionArchive("previous-sunday", time.Date(2026, 12, 27, 23, 59, 0, 0, location)),
		}
		got, err := BackupsToRetain(backups, 3, 1, location, now)
		if err != nil {
			t.Fatal(err)
		}
		if names := retentionNames(got); !equalRetentionNames(names, []string{"wednesday", "tuesday", "monday", "sunday-before-midnight"}) {
			t.Fatalf("retained archives = %v, want three current-week dates and the prior week's Sunday", names)
		}
	})

	t.Run("DST repeated hour remains one local date", func(t *testing.T) {
		now := time.Date(2026, 10, 26, 12, 0, 0, 0, location)
		firstRepeatedHour := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC)
		secondRepeatedHour := time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC)
		backups := []BackupInfo{
			retentionArchive("dst-later", secondRepeatedHour),
			retentionArchive("dst-earlier", firstRepeatedHour),
			retentionArchive("oct-24", time.Date(2026, 10, 24, 12, 0, 0, 0, location)),
		}
		got, err := BackupsToRetain(backups, 3, 1, location, now)
		if err != nil {
			t.Fatal(err)
		}
		if names := retentionNames(got); !equalRetentionNames(names, []string{"dst-later", "oct-24"}) {
			t.Fatalf("retained archives = %v, want one repeated-hour archive and October 24", names)
		}
	})
}

func TestBackupsToRetainLegacyCountPolicy(t *testing.T) {
	backups := []BackupInfo{
		retentionArchive("newest", time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)),
		retentionArchive("middle", time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)),
		retentionArchive("oldest", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
	}
	got, err := BackupsToRetain(backups, 2, 0, nil, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if names := retentionNames(got); !equalRetentionNames(names, []string{"newest", "middle"}) {
		t.Fatalf("retained archives = %v, want legacy count selection", names)
	}
}

func TestPruneBackupsWithPolicyFailsClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backup-v1.0.0-20261007T120000.tar.gz")
	if err := os.WriteFile(path, []byte("backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PruneBackupsWithPolicy(dir, 3, 2, time.UTC, time.Now()); err == nil {
		t.Fatal("expected invalid weekly retention error")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("backup was removed after policy error: %v", err)
	}
}

func retentionArchive(name string, createdAt time.Time) BackupInfo {
	return BackupInfo{Filename: name, Path: name, CreatedAt: createdAt}
}

func retentionNames(backups []BackupInfo) []string {
	names := make([]string, len(backups))
	for i, archive := range backups {
		names[i] = archive.Filename
	}
	return names
}

func equalRetentionNames(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
