//go:build integration

package storage

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestFinalizationMutationsStayInSessionPartition(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	targetSessionID, unrelatedSessionID := uuid.NewString(), uuid.NewString()
	cleanupPageRankEvidenceSession(t, s, targetSessionID)
	cleanupPageRankEvidenceSession(t, s, unrelatedSessionID)
	t.Cleanup(func() {
		cleanupPageRankEvidenceSession(t, s, targetSessionID)
		cleanupPageRankEvidenceSession(t, s, unrelatedSessionID)
	})

	seedURL, nextURL := "https://finalization.test/", "https://finalization.test/next"
	unrelatedURL := "https://unrelated.test/"
	now := time.Now().UTC()
	if err := s.InsertPages(ctx, []PageRow{
		{CrawlSessionID: targetSessionID, URL: seedURL, FinalURL: seedURL, StatusCode: 200, ContentType: "text/html", Canonical: seedURL, CanonicalIsSelf: true, IsIndexable: true, Depth: 8, FoundOn: "stale-seed-parent", CrawledAt: now},
		{CrawlSessionID: targetSessionID, URL: nextURL, FinalURL: nextURL, StatusCode: 200, ContentType: "text/html", Canonical: nextURL, CanonicalIsSelf: true, IsIndexable: true, Depth: 8, FoundOn: "stale-page-parent", CrawledAt: now},
		{CrawlSessionID: unrelatedSessionID, URL: unrelatedURL, FinalURL: unrelatedURL, StatusCode: 200, ContentType: "text/html", Canonical: unrelatedURL, CanonicalIsSelf: true, IsIndexable: true, Depth: 7, FoundOn: "preserved-parent", PageRank: 42, CrawledAt: now},
	}); err != nil {
		t.Fatalf("InsertPages: %v", err)
	}
	if err := s.InsertLinks(ctx, []LinkRow{
		{CrawlSessionID: targetSessionID, SourceURL: seedURL, TargetURL: nextURL, IsInternal: true, CrawledAt: now},
		{CrawlSessionID: targetSessionID, SourceURL: nextURL, TargetURL: seedURL, IsInternal: true, CrawledAt: now},
	}); err != nil {
		t.Fatalf("InsertLinks: %v", err)
	}

	var beforeDepth uint16
	var beforeFoundOn, beforeRevision string
	var beforeRank float64
	if err := s.conn.QueryRow(ctx, `
		SELECT depth, found_on, pagerank, toString(pagerank_revision)
		FROM crawlobserver.pages FINAL WHERE crawl_session_id = ? AND url = ?`, unrelatedSessionID, unrelatedURL,
	).Scan(&beforeDepth, &beforeFoundOn, &beforeRank, &beforeRevision); err != nil {
		t.Fatalf("read unrelated baseline: %v", err)
	}

	if err := s.RecomputeDepths(ctx, targetSessionID, []string{seedURL}); err != nil {
		t.Fatalf("RecomputeDepths: %v", err)
	}
	if err := s.ComputePageRankWithOptions(ctx, targetSessionID, PageRankOptions{IncludeFooterLinks: true}); err != nil {
		t.Fatalf("ComputePageRankWithOptions: %v", err)
	}

	for _, want := range []struct {
		url     string
		depth   uint16
		foundOn string
	}{{seedURL, 0, ""}, {nextURL, 1, seedURL}} {
		var depth uint16
		var foundOn string
		var rank float64
		if err := s.conn.QueryRow(ctx, `
			SELECT depth, found_on, pagerank FROM crawlobserver.pages FINAL
			WHERE crawl_session_id = ? AND url = ?`, targetSessionID, want.url,
		).Scan(&depth, &foundOn, &rank); err != nil {
			t.Fatalf("read target %s: %v", want.url, err)
		}
		if depth != want.depth || foundOn != want.foundOn || rank <= 0 {
			t.Errorf("target %s = depth %d, found_on %q, pagerank %g; want depth %d, found_on %q, positive rank",
				want.url, depth, foundOn, rank, want.depth, want.foundOn)
		}
	}

	evidence, err := s.LatestFinalizedPageRankEvidence(ctx, targetSessionID)
	if err != nil {
		t.Fatalf("LatestFinalizedPageRankEvidence: %v", err)
	}
	if evidence.State != PageRankEvidenceFinalized || evidence.EligiblePageCount != 2 || evidence.PositivePageCount != 2 || evidence.ZeroPageCount != 0 {
		t.Fatalf("finalized PageRank evidence = %#v", evidence)
	}
	population, revised, err := s.PageRankPopulationForRevision(ctx, targetSessionID, evidence.AttemptID)
	if err != nil {
		t.Fatalf("PageRankPopulationForRevision: %v", err)
	}
	if population.Eligible != 2 || population.Positive != 2 || population.Zero != 0 || revised != 2 {
		t.Fatalf("finalized PageRank population/revision = %#v/%d, want 2/2/0 and 2 revised", population, revised)
	}

	var afterDepth uint16
	var afterFoundOn, afterRevision string
	var afterRank float64
	if err := s.conn.QueryRow(ctx, `
		SELECT depth, found_on, pagerank, toString(pagerank_revision)
		FROM crawlobserver.pages FINAL WHERE crawl_session_id = ? AND url = ?`, unrelatedSessionID, unrelatedURL,
	).Scan(&afterDepth, &afterFoundOn, &afterRank, &afterRevision); err != nil {
		t.Fatalf("read unrelated result: %v", err)
	}
	if afterDepth != beforeDepth || afterFoundOn != beforeFoundOn || afterRank != beforeRank || afterRevision != beforeRevision {
		t.Fatalf("unrelated page changed: before=(%d,%q,%g,%s), after=(%d,%q,%g,%s)",
			beforeDepth, beforeFoundOn, beforeRank, beforeRevision, afterDepth, afterFoundOn, afterRank, afterRevision)
	}

	for _, tempTable := range []string{
		"tmp_depths_" + strings.ReplaceAll(targetSessionID, "-", ""),
		"tmp_pagerank_" + strings.ReplaceAll(targetSessionID, "-", ""),
	} {
		var command string
		if err := s.conn.QueryRow(ctx, `
			SELECT command FROM system.mutations
			WHERE database = 'crawlobserver' AND table = 'pages' AND position(command, ?) > 0
			ORDER BY create_time DESC LIMIT 1`, tempTable,
		).Scan(&command); err != nil {
			t.Fatalf("read mutation command for %s: %v", tempTable, err)
		}
		if !strings.Contains(strings.ToUpper(command), "IN PARTITION") || !strings.Contains(command, "crawl_session_id") {
			t.Errorf("mutation for %s is not partition- and session-scoped: %s", tempTable, command)
		}
	}
}
