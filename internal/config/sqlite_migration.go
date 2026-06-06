package config

import (
	"archive/tar"
	"compress/gzip"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/viper"
	_ "modernc.org/sqlite"
)

var sqliteUserDataTables = []string{
	"projects",
	"api_keys",
	"gsc_connections",
	"gsc_fetch_checkpoints",
	"rulesets",
	"rules",
	"provider_connections",
	"extractor_sets",
	"extractors",
}

type sqliteCandidate struct {
	path        string
	description string
	userRows    int
	cleanup     func()
}

// migrateLegacySQLite recovers a populated SQLite store when the configured
// destination is missing or only contains an empty schema. This covers upgrades
// where a fresh app-data database was created before the legacy database or a
// pre-update backup could be copied.
func migrateLegacySQLite(destPath, baseName string) {
	destRows, destReadable := sqliteUserDataRows(destPath)
	if !destReadable {
		return
	}
	if destRows > 0 {
		return
	}

	candidate, ok := bestSQLiteFileCandidate(destPath, baseName, destRows)
	if !ok {
		candidate, ok = firstBackupSQLiteCandidate(destRows)
	}
	if !ok {
		return
	}
	defer candidate.cleanup()

	if err := copySQLiteDatabase(candidate.path, destPath); err != nil {
		fmt.Fprintf(os.Stderr, "  Warning: failed to recover database from %s: %v\n", candidate.description, err)
		return
	}
	fmt.Fprintf(os.Stderr, "  Recovered database from %s to %s\n", candidate.description, destPath)
}

func bestSQLiteFileCandidate(destPath, baseName string, minRows int) (sqliteCandidate, bool) {
	var best sqliteCandidate
	seen := map[string]struct{}{}
	for _, src := range legacySQLitePaths(baseName) {
		if samePath(src, destPath) {
			continue
		}
		if _, ok := seen[filepath.Clean(src)]; ok {
			continue
		}
		seen[filepath.Clean(src)] = struct{}{}

		rows, readable := sqliteUserDataRows(src)
		if !readable || rows <= minRows || rows <= best.userRows {
			continue
		}
		best = sqliteCandidate{
			path:        src,
			description: src,
			userRows:    rows,
			cleanup:     func() {},
		}
	}
	return best, best.path != ""
}

func legacySQLitePaths(baseName string) []string {
	var paths []string
	if cwd, err := os.Getwd(); err == nil {
		paths = append(paths, filepath.Join(cwd, baseName))
	}
	if cfgFile := viper.ConfigFileUsed(); cfgFile != "" {
		paths = append(paths, filepath.Join(filepath.Dir(cfgFile), baseName))
	}
	if execPath, err := os.Executable(); err == nil {
		execDir := filepath.Dir(execPath)
		paths = append(paths, filepath.Join(execDir, baseName))
		if appPath, ok := enclosingAppBundle(execPath); ok {
			paths = append(paths,
				filepath.Join(appPath, baseName),
				filepath.Join(appPath, "Contents", "Resources", baseName),
				filepath.Join(filepath.Dir(appPath), baseName),
			)
		}
	}
	return paths
}

func enclosingAppBundle(path string) (string, bool) {
	current := path
	for {
		if strings.HasSuffix(current, ".app") {
			return current, true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false
		}
		current = parent
	}
}

func firstBackupSQLiteCandidate(minRows int) (sqliteCandidate, bool) {
	dataDir, err := DefaultDataDir()
	if err != nil {
		return sqliteCandidate{}, false
	}
	for _, archivePath := range backupArchives(filepath.Join(dataDir, "backups")) {
		sqlitePath, cleanup, err := extractSQLiteFromBackup(archivePath)
		if err != nil {
			continue
		}
		rows, readable := sqliteUserDataRows(sqlitePath)
		if readable && rows > minRows {
			return sqliteCandidate{
				path:        sqlitePath,
				description: archivePath,
				userRows:    rows,
				cleanup:     cleanup,
			}, true
		}
		cleanup()
	}
	return sqliteCandidate{}, false
}

func backupArchives(backupDir string) []string {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return nil
	}
	type backupFile struct {
		path    string
		modTime time.Time
	}
	var backups []backupFile
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "backup-") || !strings.HasSuffix(name, ".tar.gz") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		backups = append(backups, backupFile{
			path:    filepath.Join(backupDir, name),
			modTime: info.ModTime(),
		})
	}
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].modTime.After(backups[j].modTime)
	})

	paths := make([]string, 0, len(backups))
	for _, backup := range backups {
		paths = append(paths, backup.path)
	}
	return paths
}

func extractSQLiteFromBackup(archivePath string) (string, func(), error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", func() {}, err
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return "", func() {}, err
	}
	defer gr.Close()

	tmp, err := os.CreateTemp("", "crawlobserver-sqlite-*.db")
	if err != nil {
		return "", func() {}, err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			tmp.Close()
			cleanup()
			return "", func() {}, fmt.Errorf("crawlobserver.db not found")
		}
		if err != nil {
			tmp.Close()
			cleanup()
			return "", func() {}, err
		}
		if filepath.Clean(hdr.Name) != "crawlobserver.db" || hdr.Typeflag != tar.TypeReg {
			continue
		}
		if _, err := io.Copy(tmp, tr); err != nil {
			tmp.Close()
			cleanup()
			return "", func() {}, err
		}
		if err := tmp.Close(); err != nil {
			cleanup()
			return "", func() {}, err
		}
		return tmpPath, cleanup, nil
	}
}

func sqliteUserDataRows(path string) (int, bool) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return 0, true
	}
	if err != nil || info.IsDir() {
		return 0, false
	}
	if info.Size() == 0 {
		return 0, true
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return 0, false
	}
	defer db.Close()

	total := 0
	for _, table := range sqliteUserDataTables {
		var exists int
		if err := db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&exists); err != nil {
			return 0, false
		}
		if exists == 0 {
			continue
		}
		var rows int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&rows); err != nil {
			return 0, false
		}
		total += rows
	}
	return total, true
}

func copySQLiteDatabase(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	_ = os.Remove(tmpPath)

	if err := vacuumSQLiteInto(src, tmpPath); err != nil {
		if copyErr := copyFile(src, tmpPath); copyErr != nil {
			return fmt.Errorf("sqlite copy failed: %w; fallback file copy failed: %v", err, copyErr)
		}
	}

	if info, err := os.Stat(src); err == nil {
		_ = os.Chmod(tmpPath, info.Mode())
	} else {
		_ = os.Chmod(tmpPath, 0600)
	}

	return replaceSQLiteDatabase(tmpPath, dst)
}

func vacuumSQLiteInto(src, dst string) error {
	db, err := sql.Open("sqlite", src)
	if err != nil {
		return err
	}
	defer db.Close()

	_, err = db.Exec("VACUUM INTO " + sqliteStringLiteral(dst))
	return err
}

func replaceSQLiteDatabase(tmpPath, dst string) error {
	var preserved string
	if info, err := os.Stat(dst); err == nil && !info.IsDir() {
		preserved = fmt.Sprintf("%s.empty-%s", dst, time.Now().UTC().Format("20060102T150405"))
		if err := os.Rename(dst, preserved); err != nil {
			_ = os.Remove(tmpPath)
			return err
		}
	}

	_ = os.Remove(dst + "-wal")
	_ = os.Remove(dst + "-shm")

	if err := os.Rename(tmpPath, dst); err != nil {
		if preserved != "" {
			_ = os.Rename(preserved, dst)
		}
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func sqliteStringLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func samePath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA == nil && errB == nil {
		return filepath.Clean(absA) == filepath.Clean(absB)
	}
	return filepath.Clean(a) == filepath.Clean(b)
}
