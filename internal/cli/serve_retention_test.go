package cli

import (
	"testing"
	"time"

	"github.com/SEObserver/crawlobserver/internal/storage"
)

func TestSessionsToPruneKeepsNewestInactivePerProject(t *testing.T) {
	projectA := "project-a"
	projectB := "project-b"
	base := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)

	sessions := []storage.CrawlSession{
		{ID: "a-running", ProjectID: &projectA, Status: "running", StartedAt: base.Add(4 * time.Hour)},
		{ID: "a-newest", ProjectID: &projectA, Status: "completed", StartedAt: base.Add(3 * time.Hour)},
		{ID: "a-previous", ProjectID: &projectA, Status: "completed", StartedAt: base.Add(2 * time.Hour)},
		{ID: "a-old", ProjectID: &projectA, Status: "completed", StartedAt: base.Add(1 * time.Hour)},
		{ID: "b-newest", ProjectID: &projectB, Status: "completed", StartedAt: base.Add(3 * time.Hour)},
		{ID: "b-previous", ProjectID: &projectB, Status: "error", StartedAt: base.Add(2 * time.Hour)},
		{ID: "unassigned-newest", Status: "completed", StartedAt: base.Add(3 * time.Hour)},
		{ID: "unassigned-previous", Status: "stopped", StartedAt: base.Add(2 * time.Hour)},
		{ID: "unassigned-old", Status: "completed", StartedAt: base.Add(1 * time.Hour)},
	}

	got := sessionsToPrune(sessions, 2, nil)
	gotIDs := make(map[string]bool, len(got))
	for _, sess := range got {
		gotIDs[sess.ID] = true
	}

	want := []string{"unassigned-old", "a-old"}
	if len(gotIDs) != len(want) {
		t.Fatalf("got %v, want %v", gotIDs, want)
	}
	for _, id := range want {
		if !gotIDs[id] {
			t.Fatalf("got %v, want %v", gotIDs, want)
		}
	}
}

func TestSessionsToPruneProtectsReferencedAndNewestFullSessions(t *testing.T) {
	projectA := "project-a"
	base := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)

	sessions := []storage.CrawlSession{
		{ID: "delta-newest", ProjectID: &projectA, Status: "completed", Label: "Daily Delta Crawl", StartedAt: base.Add(5 * time.Hour)},
		{ID: "delta-previous", ProjectID: &projectA, Status: "completed", Label: "Daily Delta Crawl", StartedAt: base.Add(4 * time.Hour)},
		{ID: "protected-delta", ProjectID: &projectA, Status: "completed", Label: "Daily Delta Crawl", StartedAt: base.Add(3 * time.Hour)},
		{ID: "full-baseline", ProjectID: &projectA, Status: "completed", Label: "", StartedAt: base.Add(2 * time.Hour)},
		{ID: "full-old", ProjectID: &projectA, Status: "completed", Label: "", StartedAt: base.Add(1 * time.Hour)},
	}
	protected := map[string]struct{}{"protected-delta": {}}

	got := sessionsToPrune(sessions, 2, protected)
	gotIDs := make(map[string]bool, len(got))
	for _, sess := range got {
		gotIDs[sess.ID] = true
	}

	if gotIDs["protected-delta"] {
		t.Fatalf("protected delta should not be pruned: %v", gotIDs)
	}
	if gotIDs["full-baseline"] {
		t.Fatalf("newest full crawl should not be pruned: %v", gotIDs)
	}
	if !gotIDs["full-old"] {
		t.Fatalf("old unprotected full crawl should be pruned: %v", gotIDs)
	}
}
