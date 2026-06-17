//go:build integration

package storage

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"
)

const deltaConfirmedTestSessionID = "22222222-3333-4444-5555-666666666666"

func cleanupDeltaTestSession(t *testing.T, s *Store, sessionID string) {
	t.Helper()
	ctx := context.Background()
	tables := []string{"pages", "links", "sitemap_urls"}
	for _, tbl := range tables {
		if err := s.conn.Exec(ctx, fmt.Sprintf(
			"ALTER TABLE crawlobserver.%s DELETE WHERE crawl_session_id = ?",
			tbl,
		), sessionID); err != nil {
			t.Logf("cleanup %s: %v", tbl, err)
		}
	}
	if err := s.conn.Exec(ctx, "ALTER TABLE crawlobserver.crawl_sessions DELETE WHERE id = ?", sessionID); err != nil {
		t.Logf("cleanup crawl_sessions: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
}

func setupDeltaConfirmedTestData(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Add(-48 * time.Hour)
	projectID := "delta-confirmed-project"

	if err := s.InsertSession(ctx, &CrawlSession{
		ID:           deltaConfirmedTestSessionID,
		StartedAt:    now,
		FinishedAt:   now.Add(time.Minute),
		Status:       "completed",
		SeedURLs:     []string{"https://example.com/"},
		PagesCrawled: 5,
		UserAgent:    "crawlobserver-test",
		ProjectID:    &projectID,
	}); err != nil {
		t.Fatalf("inserting session: %v", err)
	}

	pages := []PageRow{
		{CrawlSessionID: deltaConfirmedTestSessionID, URL: "https://example.com/", StatusCode: 200, ContentType: "text/html", CrawledAt: now},
		{CrawlSessionID: deltaConfirmedTestSessionID, URL: "https://example.com/sitemap-404", StatusCode: 404, ContentType: "text/html", CrawledAt: now.Add(time.Second)},
		{CrawlSessionID: deltaConfirmedTestSessionID, URL: "https://example.com/internal-404", StatusCode: 404, ContentType: "text/html", CrawledAt: now.Add(2 * time.Second)},
		{CrawlSessionID: deltaConfirmedTestSessionID, URL: "https://example.com/gsc-only-404", StatusCode: 404, ContentType: "text/html", CrawledAt: now.Add(3 * time.Second)},
		{CrawlSessionID: deltaConfirmedTestSessionID, URL: "https://example.com/stale-orphan", StatusCode: 200, ContentType: "text/html", CrawledAt: now.Add(4 * time.Second)},
	}
	if err := s.InsertPages(ctx, pages); err != nil {
		t.Fatalf("inserting pages: %v", err)
	}
	if err := s.InsertSitemapURLs(ctx, []SitemapURLRow{
		{
			CrawlSessionID: deltaConfirmedTestSessionID,
			SitemapURL:     "https://example.com/sitemap.xml",
			Loc:            "https://example.com/sitemap-404",
		},
	}); err != nil {
		t.Fatalf("inserting sitemap urls: %v", err)
	}
	if err := s.InsertLinks(ctx, []LinkRow{
		{
			CrawlSessionID: deltaConfirmedTestSessionID,
			SourceURL:      "https://example.com/",
			TargetURL:      "https://example.com/internal-404",
			IsInternal:     true,
			CrawledAt:      now,
		},
	}); err != nil {
		t.Fatalf("inserting links: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
}

func TestDeltaProblemPageURLs_ExcludeUnconfirmedOrphans(t *testing.T) {
	s := testStore(t)
	t.Cleanup(func() { cleanupDeltaTestSession(t, s, deltaConfirmedTestSessionID) })
	cleanupDeltaTestSession(t, s, deltaConfirmedTestSessionID)
	setupDeltaConfirmedTestData(t, s)

	got, err := s.DeltaProblemPageURLs(context.Background(), deltaConfirmedTestSessionID, 10)
	if err != nil {
		t.Fatalf("DeltaProblemPageURLs: %v", err)
	}
	want := []string{
		"https://example.com/sitemap-404",
		"https://example.com/internal-404",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DeltaProblemPageURLs = %#v, want %#v", got, want)
	}
}

func TestDeltaStalePageURLs_ExcludeUnconfirmedOrphans(t *testing.T) {
	s := testStore(t)
	t.Cleanup(func() { cleanupDeltaTestSession(t, s, deltaConfirmedTestSessionID) })
	cleanupDeltaTestSession(t, s, deltaConfirmedTestSessionID)
	setupDeltaConfirmedTestData(t, s)

	got, err := s.DeltaStalePageURLs(context.Background(), deltaConfirmedTestSessionID, time.Now().UTC().Add(-24*time.Hour), 10)
	if err != nil {
		t.Fatalf("DeltaStalePageURLs: %v", err)
	}
	want := []string{
		"https://example.com/",
		"https://example.com/sitemap-404",
		"https://example.com/internal-404",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DeltaStalePageURLs = %#v, want %#v", got, want)
	}
}
