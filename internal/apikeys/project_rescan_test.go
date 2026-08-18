package apikeys

import (
	"path/filepath"
	"testing"
	"time"
)

func TestProjectRescanRequestPersistsStableTerminalResult(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "control-plane.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject("durable-rescan")
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	if _, err := store.CreateProjectRescanRequest(project.ID, "publish-1", "session-1", "sha256:request", []string{"https://example.com/page"}, startedAt); err != nil {
		t.Fatal(err)
	}
	completedAt := startedAt.Add(time.Minute)
	finished, err := store.FinishProjectRescanRequest(project.ID, "publish-1", ProjectRescanStatusCompleted, 1, "", "", completedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, err := reopened.GetProjectRescanRequest(project.ID, "publish-1")
	if err != nil {
		t.Fatal(err)
	}
	if restored.RequestID != finished.RequestID || restored.RequestDigest != "sha256:request" || restored.Status != ProjectRescanStatusCompleted || restored.AcceptedCount != 1 || restored.CompletedAt == nil {
		t.Fatalf("restored record = %#v", restored)
	}
	if len(restored.URLs) != 1 || restored.URLs[0] != "https://example.com/page" {
		t.Fatalf("restored URLs = %#v", restored.URLs)
	}
}
