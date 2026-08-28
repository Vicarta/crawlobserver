//go:build integration

package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/SEObserver/crawlobserver/internal/config"
	"github.com/google/uuid"
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

func TestPageHTTPValidatorsRetainExactCaseInsensitiveHeaders(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	sessionID := uuid.NewString()
	t.Cleanup(func() { cleanupDeltaTestSession(t, s, sessionID) })
	if err := s.InsertSession(ctx, &CrawlSession{ID: sessionID, StartedAt: time.Now().UTC(), Status: "completed", Label: "Current Snapshot"}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertPages(ctx, []PageRow{
		{CrawlSessionID: sessionID, URL: "https://example.test/both", Headers: map[string]string{"eTaG": `W/"weak"`, "LAST-modified": "Wed, 21 Oct 2015 07:28:00 GMT"}, CrawledAt: time.Now().UTC()},
		{CrawlSessionID: sessionID, URL: "https://example.test/none", Headers: map[string]string{"Content-Type": "text/html"}, CrawledAt: time.Now().UTC()},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.PageHTTPValidators(ctx, sessionID, []string{"https://example.test/both", "https://example.test/none", "https://example.test/missing"})
	if err != nil {
		t.Fatal(err)
	}
	if value := got["https://example.test/both"]; value.ETag != `W/"weak"` || value.LastModified != "Wed, 21 Oct 2015 07:28:00 GMT" {
		t.Fatalf("validators = %#v", got)
	}
	if _, ok := got["https://example.test/none"]; ok {
		t.Fatalf("unexpected empty validators: %#v", got)
	}
}

func TestDeltaSitemapPageEvidenceRejectsAmbiguousNormalizedIdentity(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	sessionID := uuid.NewString()
	t.Cleanup(func() { cleanupDeltaTestSession(t, s, sessionID) })
	if err := s.InsertSession(ctx, &CrawlSession{ID: sessionID, StartedAt: time.Now().UTC(), Status: "completed", Label: "Daily Delta Crawl"}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertPages(ctx, []PageRow{
		{CrawlSessionID: sessionID, URL: "https://example.test/path", StatusCode: 200, ContentHash: 101, CrawledAt: time.Now().UTC()},
		{CrawlSessionID: sessionID, URL: "https://example.test:443/path", StatusCode: 200, ContentHash: 101, CrawledAt: time.Now().UTC()},
	}); err != nil {
		t.Fatal(err)
	}

	evidence, err := s.loadDeltaSitemapPageEvidence(ctx, []string{sessionID}, []string{
		"https://example.test/path",
		"https://example.test:443/path",
	}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence[sessionID]) != 0 {
		t.Fatalf("ambiguous normalized identity produced evidence: %#v", evidence[sessionID])
	}
}

func TestDeltaSitemapPageEvidenceRequiresCompleteSuccessfulBody(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	sessionID := uuid.NewString()
	t.Cleanup(func() { cleanupDeltaTestSession(t, s, sessionID) })
	if err := s.InsertSession(ctx, &CrawlSession{ID: sessionID, StartedAt: time.Now().UTC(), Status: "completed", Label: "Daily Delta Crawl"}); err != nil {
		t.Fatal(err)
	}
	urls := []string{
		"https://example.test/good",
		"https://example.test/truncated",
		"https://example.test/not-found",
		"https://example.test/server-error",
	}
	if err := s.InsertPages(ctx, []PageRow{
		{CrawlSessionID: sessionID, URL: urls[0], StatusCode: 200, ContentHash: 101, CrawledAt: time.Now().UTC()},
		{CrawlSessionID: sessionID, URL: urls[1], StatusCode: 200, ContentHash: 102, BodyTruncated: true, CrawledAt: time.Now().UTC()},
		{CrawlSessionID: sessionID, URL: urls[2], StatusCode: 404, ContentHash: 103, CrawledAt: time.Now().UTC()},
		{CrawlSessionID: sessionID, URL: urls[3], StatusCode: 500, ContentHash: 104, CrawledAt: time.Now().UTC()},
	}); err != nil {
		t.Fatal(err)
	}

	evidence, err := s.loadDeltaSitemapPageEvidence(ctx, []string{sessionID}, urls, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence[sessionID]) != 1 || evidence[sessionID][urls[0]].ContentHash != 101 {
		t.Fatalf("ineligible page evidence was accepted: %#v", evidence[sessionID])
	}
}

func TestConditional304OverlayPreservesCurrentPageAndLinks(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	currentID, deltaID := uuid.NewString(), uuid.NewString()
	for _, id := range []string{currentID, deltaID} {
		cleanupDeltaTestSession(t, s, id)
	}
	t.Cleanup(func() {
		for _, id := range []string{currentID, deltaID} {
			cleanupDeltaTestSession(t, s, id)
		}
	})
	now := time.Now().UTC()
	for _, session := range []*CrawlSession{
		{ID: currentID, StartedAt: now.Add(-time.Hour), Status: "completed", Label: CurrentSnapshotLabel},
		{ID: deltaID, StartedAt: now, Status: "completed", Label: "Daily Delta Crawl"},
	} {
		if err := s.InsertSession(ctx, session); err != nil {
			t.Fatal(err)
		}
	}
	unchangedURL := "https://example.test/unchanged"
	updatedURL := "https://example.test/updated"
	if err := s.InsertPages(ctx, []PageRow{
		{CrawlSessionID: currentID, URL: unchangedURL, FinalURL: unchangedURL, StatusCode: 200, Title: "retained", ContentHash: 101, Headers: map[string]string{"ETag": `"retained"`}, CrawledAt: now.Add(-time.Hour)},
		{CrawlSessionID: currentID, URL: updatedURL, FinalURL: updatedURL, StatusCode: 200, Title: "old", ContentHash: 202, CrawledAt: now.Add(-time.Hour)},
		{CrawlSessionID: deltaID, URL: unchangedURL, FinalURL: unchangedURL, StatusCode: 304, Headers: map[string]string{"ETag": `"retained"`}, CrawledAt: now},
		{CrawlSessionID: deltaID, URL: updatedURL, FinalURL: updatedURL, StatusCode: 200, Title: "new", ContentHash: 303, CrawledAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertLinks(ctx, []LinkRow{
		{CrawlSessionID: currentID, SourceURL: unchangedURL, TargetURL: "https://example.test/retained-target", IsInternal: true, CrawledAt: now.Add(-time.Hour)},
		{CrawlSessionID: currentID, SourceURL: updatedURL, TargetURL: "https://example.test/old-target", IsInternal: true, CrawledAt: now.Add(-time.Hour)},
		{CrawlSessionID: deltaID, SourceURL: unchangedURL, TargetURL: "https://example.test/must-not-copy", IsInternal: true, CrawledAt: now},
		{CrawlSessionID: deltaID, SourceURL: updatedURL, TargetURL: "https://example.test/new-target", IsInternal: true, CrawledAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.overlayDeltaPages(ctx, currentID, deltaID); err != nil {
		t.Fatal(err)
	}
	if err := s.overlayDeltaLinks(ctx, currentID, deltaID); err != nil {
		t.Fatal(err)
	}
	unchanged, err := s.GetPage(ctx, currentID, unchangedURL)
	if err != nil || unchanged.StatusCode != 200 || unchanged.Title != "retained" || unchanged.ContentHash != 101 {
		t.Fatalf("unchanged page = %#v, err=%v", unchanged, err)
	}
	updated, err := s.GetPage(ctx, currentID, updatedURL)
	if err != nil || updated.Title != "new" || updated.ContentHash != 303 {
		t.Fatalf("updated page = %#v, err=%v", updated, err)
	}
	rows, err := s.conn.Query(ctx, `SELECT source_url, target_url FROM crawlobserver.links FINAL WHERE crawl_session_id = ? ORDER BY source_url, target_url`, currentID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var links []string
	for rows.Next() {
		var source, target string
		if err := rows.Scan(&source, &target); err != nil {
			t.Fatal(err)
		}
		links = append(links, source+" -> "+target)
	}
	wantLinks := []string{unchangedURL + " -> https://example.test/retained-target", updatedURL + " -> https://example.test/new-target"}
	if !reflect.DeepEqual(links, wantLinks) {
		t.Fatalf("links = %#v, want %#v", links, wantLinks)
	}
	if err := s.overlayDeltaPages(ctx, currentID, deltaID); err != nil {
		t.Fatal(err)
	}
	if err := s.overlayDeltaLinks(ctx, currentID, deltaID); err != nil {
		t.Fatal(err)
	}
}

func TestDeltaSitemapObservationTermsUseNewestFreshRawAndExactPublishedTerm(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := "delta-sitemap-terms-" + uuid.NewString()
	fullID := uuid.NewString()
	publishedID := uuid.NewString()
	olderRawID := uuid.NewString()
	newerRawID := uuid.NewString()
	failedRawID := uuid.NewString()
	ids := []string{fullID, publishedID, olderRawID, newerRawID, failedRawID}
	for _, id := range ids {
		cleanupDeltaTestSession(t, s, id)
	}
	t.Cleanup(func() {
		for _, id := range ids {
			cleanupDeltaTestSession(t, s, id)
		}
	})

	freshConfig, err := json.Marshal(config.Config{Crawler: config.CrawlerConfig{DeltaPlan: &config.DeltaPlanConfig{
		SitemapRefresh: &config.DeltaSitemapRefresh{Mode: "fresh", FetchedAt: time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC), FreshURLCount: 1, RawURLRowCount: 1},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	for _, session := range []*CrawlSession{
		{ID: fullID, StartedAt: now.Add(-5 * time.Hour), FinishedAt: now.Add(-4 * time.Hour), Status: "completed", ProjectID: &projectID, Label: "Full Crawl"},
		{ID: publishedID, StartedAt: now.Add(-4 * time.Hour), FinishedAt: now.Add(-3 * time.Hour), Status: "completed", ProjectID: &projectID, Label: CurrentSnapshotLabel},
		{ID: olderRawID, StartedAt: now.Add(-3 * time.Hour), FinishedAt: now.Add(-2 * time.Hour), Status: "completed", ProjectID: &projectID, Label: "Daily Delta Crawl", Config: string(freshConfig)},
		{ID: newerRawID, StartedAt: now.Add(-2 * time.Hour), FinishedAt: now.Add(-time.Hour), Status: "completed", ProjectID: &projectID, Label: "Daily Delta Crawl", Config: string(freshConfig)},
		{ID: failedRawID, StartedAt: now.Add(-time.Hour), FinishedAt: now, Status: "failed", ProjectID: &projectID, Label: "Daily Delta Crawl", Config: string(freshConfig)},
	} {
		if err := s.InsertSession(ctx, session); err != nil {
			t.Fatalf("insert session %s: %v", session.ID, err)
		}
	}
	for sessionID, loc := range map[string]string{
		fullID: "https://example.test/full", publishedID: "https://example.test/published",
		olderRawID: "https://example.test/older", newerRawID: "https://example.test/newer", failedRawID: "https://example.test/failed",
	} {
		if err := s.InsertSitemapURLs(ctx, []SitemapURLRow{{CrawlSessionID: sessionID, SitemapURL: "https://example.test/sitemap.xml", Loc: loc, LastMod: "2026-08-26"}}); err != nil {
			t.Fatalf("insert sitemap url %s: %v", sessionID, err)
		}
	}
	time.Sleep(500 * time.Millisecond)

	terms, err := s.LoadDeltaSitemapTerms(ctx, projectID, publishedID, fullID, 100)
	if err != nil {
		t.Fatalf("LoadDeltaSitemapTerms: %v", err)
	}
	if terms.Raw.SessionID != newerRawID || len(terms.Raw.URLs) != 1 || terms.Raw.URLs[0].Loc != "https://example.test/newer" {
		t.Fatalf("raw term = %#v, want newest completed fresh observation", terms.Raw)
	}
	if terms.Published.SessionID != publishedID || len(terms.Published.URLs) != 1 || terms.Published.URLs[0].Loc != "https://example.test/published" {
		t.Fatalf("published term = %#v, want exact materialized session", terms.Published)
	}
}

func TestDeltaSitemapStabilityUsesOnlyExactCompletedFreshPair(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := "delta-sitemap-stability-" + uuid.NewString()
	publishedID := uuid.NewString()
	olderID := uuid.NewString()
	newerID := uuid.NewString()
	ids := []string{publishedID, olderID, newerID}
	for _, id := range ids {
		cleanupDeltaTestSession(t, s, id)
	}
	t.Cleanup(func() {
		for _, id := range ids {
			cleanupDeltaTestSession(t, s, id)
		}
	})

	freshConfig, err := json.Marshal(config.Config{Crawler: config.CrawlerConfig{DeltaPlan: &config.DeltaPlanConfig{
		SitemapRefresh: &config.DeltaSitemapRefresh{Mode: "fresh", FetchedAt: time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC), FreshURLCount: 2, RawURLRowCount: 2},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	for _, session := range []*CrawlSession{
		{ID: publishedID, StartedAt: now.Add(-4 * time.Hour), FinishedAt: now.Add(-3 * time.Hour), Status: "completed", ProjectID: &projectID, Label: CurrentSnapshotLabel},
		{ID: olderID, StartedAt: now.Add(-3 * time.Hour), FinishedAt: now.Add(-2 * time.Hour), Status: "completed", ProjectID: &projectID, Label: "Daily Delta Crawl", Config: string(freshConfig)},
		{ID: newerID, StartedAt: now.Add(-2 * time.Hour), FinishedAt: now.Add(-time.Hour), Status: "completed", ProjectID: &projectID, Label: "Daily Delta Crawl", Config: string(freshConfig)},
	} {
		if err := s.InsertSession(ctx, session); err != nil {
			t.Fatal(err)
		}
	}
	stableURL := "https://example.test/stable"
	changedURL := "https://example.test/changed"
	for _, row := range []SitemapURLRow{
		{CrawlSessionID: publishedID, SitemapURL: "https://example.test/sitemap.xml", Loc: stableURL, LastMod: "2026-08-01"},
		{CrawlSessionID: olderID, SitemapURL: "https://example.test/sitemap.xml", Loc: stableURL, LastMod: "2026-08-15T16:53:17Z"},
		{CrawlSessionID: olderID, SitemapURL: "https://example.test/sitemap.xml", Loc: changedURL, LastMod: "2026-08-15T16:53:17Z"},
		{CrawlSessionID: newerID, SitemapURL: "https://example.test/sitemap.xml", Loc: stableURL, LastMod: "2026-08-15T16:53:17Z"},
		{CrawlSessionID: newerID, SitemapURL: "https://example.test/sitemap.xml", Loc: changedURL, LastMod: "2026-08-15T16:53:17Z"},
	} {
		if err := s.InsertSitemapURLs(ctx, []SitemapURLRow{row}); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []PageRow{
		{CrawlSessionID: olderID, URL: stableURL, StatusCode: 200, ContentHash: 42, CrawledAt: now.Add(-2 * time.Hour)},
		{CrawlSessionID: olderID, URL: changedURL, StatusCode: 200, ContentHash: 43, CrawledAt: now.Add(-2 * time.Hour)},
		{CrawlSessionID: newerID, URL: stableURL, StatusCode: 200, ContentHash: 42, CrawledAt: now.Add(-time.Hour)},
		{CrawlSessionID: newerID, URL: changedURL, StatusCode: 200, ContentHash: 44, CrawledAt: now.Add(-time.Hour)},
	} {
		if err := s.InsertPages(ctx, []PageRow{row}); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(500 * time.Millisecond)

	terms, err := s.LoadDeltaSitemapTerms(ctx, projectID, publishedID, publishedID, 100)
	if err != nil {
		t.Fatalf("LoadDeltaSitemapTerms: %v", err)
	}
	if terms.Raw.SessionID != newerID || terms.Stability == nil {
		t.Fatalf("terms = %#v, want newest raw plus stability", terms)
	}
	if terms.Stability.OlderSessionID != olderID || terms.Stability.NewerSessionID != newerID || !terms.Stability.LegacyCompletePair || len(terms.Stability.URLs) != 1 {
		t.Fatalf("stability = %#v", terms.Stability)
	}
	if proof := terms.Stability.URLs[0]; proof.Loc != stableURL || proof.LastMod != "2026-08-15T16:53:17Z" || proof.ContentHash != 42 || terms.Stability.ProofDigest == "" {
		t.Fatalf("proof = %#v, stability = %#v", proof, terms.Stability)
	}
	bounded, err := s.loadDeltaSitemapObservation(ctx, newerID, now, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !bounded.Truncated || len(bounded.URLs) != 1 {
		t.Fatalf("bounded observation = %#v, want one row plus truncation sentinel", bounded)
	}
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
