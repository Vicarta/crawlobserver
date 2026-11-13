//go:build integration

package storage

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestListPagesUnifiedSearchAgainstClickHouse(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	sessionID := uuid.NewString()
	t.Cleanup(func() { cleanupRedirectTestSession(t, s, sessionID) })
	cleanupRedirectTestSession(t, s, sessionID)

	now := time.Now().UTC()
	pages := []PageRow{
		{
			CrawlSessionID: sessionID, URL: "https://search.test/URLNeedle", StatusCode: 200,
			ContentType: "text/html", Title: "URL page", MetaDescription: "URL description",
			BodyHTML: "<html><body>URL content</body></html>", CrawledAt: now,
		},
		{
			CrawlSessionID: sessionID, URL: "https://search.test/title", StatusCode: 200,
			ContentType: "text/html", Title: "TitleNeedle", MetaDescription: "Title description",
			BodyHTML: "<html><body>Title content</body></html>", CrawledAt: now,
		},
		{
			CrawlSessionID: sessionID, URL: "https://search.test/meta", StatusCode: 200,
			ContentType: "text/html", Title: "Meta page", MetaDescription: "MetaNeedle",
			BodyHTML: "<html><body>Meta content</body></html>", CrawledAt: now,
		},
		{
			CrawlSessionID: sessionID, URL: "https://search.test/body", StatusCode: 200,
			ContentType: "text/html", Title: "Body page", MetaDescription: "Body description",
			BodyHTML: "<html><body>Body &amp; Needle</body></html>", CrawledAt: now,
		},
		{
			CrawlSessionID: sessionID, URL: "https://search.test/no-html", StatusCode: 200,
			ContentType: "text/html", Title: "No HTML page", MetaDescription: "No HTML description",
			BodyHTML: "", CrawledAt: now,
		},
	}
	if err := s.InsertPages(ctx, pages); err != nil {
		t.Fatalf("inserting pages: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	for _, tc := range []struct {
		query    string
		regex    bool
		wantURL  string
		wantRows int
	}{
		{query: "urlneedle", wantURL: "https://search.test/URLNeedle", wantRows: 1},
		{query: "titleneedle", wantURL: "https://search.test/title", wantRows: 1},
		{query: "metaneedle", wantURL: "https://search.test/meta", wantRows: 1},
		{query: "body & needle", wantURL: "https://search.test/body", wantRows: 1},
		{query: `Body.*Needle`, regex: true, wantURL: "https://search.test/body", wantRows: 1},
		{query: `^$`, regex: true, wantRows: 0},
	} {
		filter, include, err := NewPageSearchFilter(tc.query, tc.regex)
		if err != nil || !include {
			t.Fatalf("NewPageSearchFilter(%q) = (%+v, %v, %v)", tc.query, filter, include, err)
		}
		got, err := s.ListPages(ctx, sessionID, 100, 0, []ParsedFilter{filter}, nil)
		if err != nil {
			t.Fatalf("ListPages(%q): %v", tc.query, err)
		}
		if len(got) != tc.wantRows {
			t.Fatalf("ListPages(%q) returned %d rows, want %d", tc.query, len(got), tc.wantRows)
		}
		if tc.wantURL != "" && got[0].URL != tc.wantURL {
			t.Errorf("ListPages(%q) URL = %q, want %q", tc.query, got[0].URL, tc.wantURL)
		}
	}
}
