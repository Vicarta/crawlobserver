package cli

import (
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/SEObserver/crawlobserver/internal/writerlock"
	"github.com/spf13/viper"
)

func TestLockedWriterLeavesFirstRunStateUntouched(t *testing.T) {
	releaseAllWriterLocks()
	viper.Reset()
	t.Cleanup(func() {
		releaseAllWriterLocks()
		viper.Reset()
	})

	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	configPath := filepath.Join(root, "config.yaml")
	legacyPath := filepath.Join(root, "crawlobserver.db")
	destinationPath := filepath.Join(stateDir, "crawlobserver.db")
	configBody := "server:\n  sqlite_path: " + destinationPath + "\n  username: admin\n  password: \"\"\ntelemetry:\n  instance_id: \"\"\n"
	if err := os.WriteFile(configPath, []byte(configBody), 0600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	if err := os.WriteFile(destinationPath, nil, 0600); err != nil && !os.IsNotExist(err) {
		t.Fatalf("creating empty destination: %v", err)
	}

	db, err := sql.Open("sqlite", legacyPath)
	if err != nil {
		t.Fatalf("opening legacy SQLite: %v", err)
	}
	if _, err := db.Exec("CREATE TABLE projects (id TEXT); INSERT INTO projects (id) VALUES ('legacy-project')"); err != nil {
		db.Close()
		t.Fatalf("creating legacy SQLite: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("closing legacy SQLite: %v", err)
	}

	if err := viper.ReadInConfig(); err == nil {
		t.Fatal("ReadInConfig unexpectedly succeeded before a config file was selected")
	}
	viper.SetConfigFile(configPath)
	if err := viper.ReadInConfig(); err != nil {
		t.Fatalf("reading config: %v", err)
	}

	beforeConfig := digestFile(t, configPath)
	beforeLegacy := digestFile(t, legacyPath)
	lock, err := writerlock.Acquire(stateDir)
	if err != nil {
		t.Fatalf("acquiring held writer lock: %v", err)
	}
	defer lock.Release()

	if err := runMigrate(migrateCmd, nil); err == nil {
		t.Fatal("migrate unexpectedly acquired a held writer lock")
	}

	if got := digestFile(t, configPath); got != beforeConfig {
		t.Fatal("locked writer changed first-run config")
	}
	if got := digestFile(t, legacyPath); got != beforeLegacy {
		t.Fatal("locked writer changed legacy SQLite")
	}
	if info, err := os.Stat(destinationPath); err == nil && info.Size() != 0 {
		t.Fatal("locked writer imported legacy SQLite into the destination")
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatalf("stat destination SQLite: %v", err)
	}
}

func digestFile(t *testing.T, path string) [sha256.Size]byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return sha256.Sum256(contents)
}
