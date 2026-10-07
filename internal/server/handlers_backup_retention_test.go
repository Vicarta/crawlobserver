package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SEObserver/crawlobserver/internal/backup"
	"github.com/SEObserver/crawlobserver/internal/config"
)

type retentionExportRecorder struct {
	StorageService
	retain int
}

func (r *retentionExportRecorder) ExportCriticalTables(_ context.Context, _ string, retain int) error {
	r.retain = retain
	return nil
}

func TestManualBackupUsesWeeklyRetentionPolicy(t *testing.T) {
	srv, handler, _ := newTestServer(t)
	location, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatal(err)
	}
	backupDir := t.TempDir()
	now := time.Now().In(location)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 20, 0, 0, location)
	for i := 0; i < 3; i++ {
		writeRetentionBackup(t, backupDir, fmt.Sprintf("backup-v1.0.%d-older.tar.gz", i), today.AddDate(0, 0, -i))
	}
	thisWeekStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location).AddDate(0, 0, -(int(now.Weekday())+6)%7)
	previousWeek := thisWeekStart.AddDate(0, 0, -1).Add(12 * time.Hour)
	olderWeek := thisWeekStart.AddDate(0, 0, -8).Add(12 * time.Hour)
	writeRetentionBackup(t, backupDir, "backup-v1.0.4-previous-week.tar.gz", previousWeek)
	obsolete := writeRetentionBackup(t, backupDir, "backup-v1.0.5-obsolete.tar.gz", olderWeek)

	srv.BackupOpts = &backup.BackupOptions{BackupDir: backupDir}
	srv.cfg.Backup = config.BackupConfig{Retain: 3, RetainWeekly: 1, Timezone: "Europe/Kyiv"}
	request := authRequest(httptest.NewRequest(http.MethodPost, "/api/backups", nil))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("POST /api/backups status = %d, want 201: %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(obsolete); !os.IsNotExist(err) {
		t.Fatalf("obsolete archive stat error = %v, want not-exist", err)
	}

	backups, err := backup.ListBackups(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := backup.BackupsToRetain(backups, 3, 1, location, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != len(retained) {
		t.Fatalf("remaining backups = %d, policy-selected backups = %d", len(backups), len(retained))
	}
	if _, err := os.Stat(filepath.Join(backupDir, "backup-v1.0.4-previous-week.tar.gz")); err != nil {
		t.Fatalf("previous calendar week's archive was removed: %v", err)
	}
}

func TestManualCriticalExportUsesWeeklyRetentionWithoutDeletingWeeklyBackup(t *testing.T) {
	srv, handler, _ := newTestServer(t)
	location, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatal(err)
	}
	backupDir := t.TempDir()
	exportDir := t.TempDir()
	backupDates := []time.Time{
		time.Date(2026, 10, 7, 0, 20, 0, 0, location),
		time.Date(2026, 10, 6, 0, 20, 0, 0, location),
		time.Date(2026, 10, 5, 0, 20, 0, 0, location),
		time.Date(2026, 10, 4, 0, 20, 0, 0, location),
	}
	var weeklyArchive string
	var archives []string
	for i, createdAt := range backupDates {
		path := writeRetentionBackup(t, backupDir, fmt.Sprintf("backup-v1.0.%d-fixture.tar.gz", i), createdAt)
		archives = append(archives, path)
		if i == len(backupDates)-1 {
			weeklyArchive = path
		}
	}
	var dependencies []string
	for _, archiveDate := range backupDates {
		createdAt := archiveDate.Add(-5 * time.Minute)
		dependencies = append(dependencies, writeRetentionExport(t, exportDir, createdAt.Format("20060102T150405"), createdAt))
	}
	obsoleteExport := writeRetentionExport(t, exportDir, "20261001T001000", time.Date(2026, 10, 1, 0, 10, 0, 0, location))

	// The failed-week scenario is evaluated in the following local week: Oct 4
	// is no longer selected by archive retention, but its existing dependency
	// remains protected until a later successful full archive prunes it.
	nextWeek := time.Date(2026, 10, 14, 12, 0, 0, 0, location)
	backups, err := backup.ListBackups(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := backup.BackupsToRetain(backups, 3, 1, location, nextWeek)
	if err != nil {
		t.Fatal(err)
	}
	for _, archive := range selected {
		if archive.Path == weeklyArchive {
			t.Fatal("Oct 4 archive unexpectedly selected by the following week's policy")
		}
	}

	recorder := &retentionExportRecorder{StorageService: srv.store}
	srv.store = recorder
	srv.SQLBackupOpts = &backup.SQLBackupOptions{BackupDir: backupDir}
	srv.ExportDir = exportDir
	srv.ExportRetain = 3
	srv.cfg.Backup = config.BackupConfig{Retain: 3, RetainWeekly: 1, Timezone: "Europe/Kyiv"}
	request := authRequest(httptest.NewRequest(http.MethodPost, "/api/admin/export-critical", nil))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("POST /api/admin/export-critical status = %d, want 200: %s", response.Code, response.Body.String())
	}
	if recorder.retain != 0 {
		t.Fatalf("export retain argument = %d, want 0 to defer calendar pruning", recorder.retain)
	}
	for _, path := range append(archives, dependencies...) {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("retained recovery file %s was removed: %v", path, err)
		}
	}
	if _, err := os.Stat(weeklyArchive); err != nil {
		t.Fatalf("Oct 4 weekly archive was removed by manual export: %v", err)
	}
	if _, err := os.Stat(dependencies[len(dependencies)-1]); err != nil {
		t.Fatalf("Oct 4 archive dependency was removed by manual export: %v", err)
	}
	if _, err := os.Stat(obsoleteExport); err != nil {
		t.Fatalf("obsolete export was pruned before a full archive succeeded: %v", err)
	}
}

func writeRetentionBackup(t *testing.T, dir, name string, modTime time.Time) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeRetentionExport(t *testing.T, dir, timestamp string, modTime time.Time) string {
	t.Helper()
	path := filepath.Join(dir, "gsc_analytics_"+timestamp+".jsonl.gz")
	if err := os.WriteFile(path, []byte("export"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
	return path
}
