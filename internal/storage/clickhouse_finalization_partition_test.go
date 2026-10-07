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

	depthTable := "tmp_depths_" + strings.ReplaceAll(targetSessionID, "-", "")
	var depthMutation string
	if err := s.conn.QueryRow(ctx, `
		SELECT command FROM system.mutations
		WHERE database = 'crawlobserver' AND table = 'pages' AND position(command, ?) > 0
		ORDER BY create_time DESC LIMIT 1`, depthTable,
	).Scan(&depthMutation); err != nil {
		t.Fatalf("read depth mutation command: %v", err)
	}
	if !strings.Contains(strings.ToUpper(depthMutation), "IN PARTITION") || !strings.Contains(depthMutation, "crawl_session_id") {
		t.Errorf("depth mutation is not partition- and session-scoped: %s", depthMutation)
	}

	rankTable := "tmp_pagerank_" + strings.ReplaceAll(targetSessionID, "-", "")
	var rankMutationCount uint64
	if err := s.conn.QueryRow(ctx, `
		SELECT count() FROM system.mutations
		WHERE database = 'crawlobserver' AND table = 'pages' AND position(command, ?) > 0`, rankTable,
	).Scan(&rankMutationCount); err != nil {
		t.Fatalf("count PageRank mutations: %v", err)
	}
	if rankMutationCount != 0 {
		t.Errorf("PageRank writeback unexpectedly created %d mutations", rankMutationCount)
	}
}

func TestPageRankInsertWritebackIgnoresUnrelatedPendingMutation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	targetSessionID, unrelatedSessionID := uuid.NewString(), uuid.NewString()
	cleanupPageRankEvidenceSession(t, s, targetSessionID)
	cleanupPageRankEvidenceSession(t, s, unrelatedSessionID)
	t.Cleanup(func() {
		cleanupPageRankEvidenceSession(t, s, targetSessionID)
		cleanupPageRankEvidenceSession(t, s, unrelatedSessionID)
	})

	targetURL := "https://rank-writeback.test/"
	childURL := "https://rank-writeback.test/child"
	missingURL := "https://rank-writeback.test/missing"
	assetURL := "https://rank-writeback.test/style.css"
	unrelatedURL := "https://rank-writeback-unrelated.test/"
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.InsertPages(ctx, []PageRow{
		{CrawlSessionID: targetSessionID, URL: targetURL, FinalURL: targetURL, StatusCode: 200, ContentType: "text/html", Title: "stale title", BodyHTML: "<main>stale</main>", Depth: 9, PageRank: 91, CrawledAt: now.Add(-time.Minute)},
		{CrawlSessionID: targetSessionID, URL: targetURL, FinalURL: targetURL, StatusCode: 200, ContentType: "text/html", Title: "retained title", BodyHTML: "<main>retained</main>", Depth: 4, FoundOn: "retained parent", Canonical: targetURL, CanonicalIsSelf: true, IsIndexable: true, PageRank: 92, CrawledAt: now},
		{CrawlSessionID: targetSessionID, URL: childURL, FinalURL: childURL, StatusCode: 200, ContentType: "text/html", Title: "child", Canonical: childURL, CanonicalIsSelf: true, IsIndexable: true, PageRank: 93, CrawledAt: now.Add(time.Second)},
		{CrawlSessionID: targetSessionID, URL: missingURL, FinalURL: missingURL, StatusCode: 404, ContentType: "text/html", Title: "missing", PageRank: 94, CrawledAt: now.Add(2 * time.Second)},
		{CrawlSessionID: targetSessionID, URL: assetURL, FinalURL: assetURL, StatusCode: 200, ContentType: "text/css", Title: "asset", PageRank: 95, CrawledAt: now.Add(3 * time.Second)},
		{CrawlSessionID: unrelatedSessionID, URL: unrelatedURL, FinalURL: unrelatedURL, StatusCode: 200, ContentType: "text/html", Title: "unrelated", Depth: 7, PageRank: 42, CrawledAt: now},
	}); err != nil {
		t.Fatalf("InsertPages: %v", err)
	}
	if err := s.InsertLinks(ctx, []LinkRow{
		{CrawlSessionID: targetSessionID, SourceURL: targetURL, TargetURL: childURL, IsInternal: true, CrawledAt: now},
		{CrawlSessionID: targetSessionID, SourceURL: childURL, TargetURL: targetURL, IsInternal: true, CrawledAt: now},
	}); err != nil {
		t.Fatalf("InsertLinks: %v", err)
	}

	oldRankTable := "tmp_pagerank_" + strings.ReplaceAll(targetSessionID, "-", "")
	if err := s.conn.Exec(ctx, "CREATE TABLE crawlobserver."+oldRankTable+" (page_url String, new_pagerank Float64) ENGINE = Join(ANY, LEFT, page_url)"); err != nil {
		t.Fatalf("create old session-named PageRank table: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.conn.Exec(cleanupCtx, "DROP TABLE IF EXISTS crawlobserver."+oldRankTable); err != nil {
			t.Errorf("drop old session-named PageRank table: %v", err)
		}
	})

	poisonRankTable := "crawlobserver.tmp_pagerank_missing_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	var poisonMutationID string
	mergesStopped := false
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := s.conn.Exec(cleanupCtx, "DROP TABLE IF EXISTS "+poisonRankTable); err != nil {
			t.Errorf("drop poison PageRank table before cleanup: %v", err)
		}
		if mergesStopped {
			if err := s.conn.Exec(cleanupCtx, "SYSTEM START MERGES crawlobserver.pages"); err != nil {
				t.Errorf("restart page merges before cleanup: %v", err)
			}
			mergesStopped = false
		}
		if poisonMutationID == "" {
			_ = s.conn.QueryRow(cleanupCtx, `SELECT mutation_id FROM system.mutations
				WHERE database = 'crawlobserver' AND table = 'pages' AND is_done = 0 AND position(command, ?) > 0
				ORDER BY create_time DESC LIMIT 1`, poisonRankTable).Scan(&poisonMutationID)
		}
		if poisonMutationID != "" {
			if err := s.conn.Exec(cleanupCtx, `KILL MUTATION
				WHERE database = 'crawlobserver' AND table = 'pages' AND mutation_id = ? SYNC`, poisonMutationID); err != nil {
				t.Errorf("kill unrelated PageRank mutation before cleanup: %v", err)
			}
		}
	})
	if err := s.conn.Exec(ctx, "CREATE TABLE "+poisonRankTable+" (page_url String, new_pagerank Float64) ENGINE = Join(ANY, LEFT, page_url)"); err != nil {
		t.Fatalf("create valid poison PageRank table: %v", err)
	}
	if err := s.conn.Exec(ctx, "SYSTEM STOP MERGES crawlobserver.pages"); err != nil {
		t.Fatalf("stop page merges for mutation fixture: %v", err)
	}
	mergesStopped = true
	if err := s.conn.Exec(ctx, `ALTER TABLE crawlobserver.pages UPDATE
		pagerank = joinGet('`+poisonRankTable+`', 'new_pagerank', url)
		WHERE crawl_session_id = ? SETTINGS mutations_sync = 0`, unrelatedSessionID); err != nil {
		t.Fatalf("queue unrelated failed mutation: %v", err)
	}
	if err := s.conn.Exec(ctx, "DROP TABLE "+poisonRankTable); err != nil {
		t.Fatalf("drop poison PageRank table while mutation is queued: %v", err)
	}
	if err := s.conn.Exec(ctx, "SYSTEM START MERGES crawlobserver.pages"); err != nil {
		t.Fatalf("start page merges for mutation fixture: %v", err)
	}
	mergesStopped = false

	mutationWaitCtx, cancelMutationWait := context.WithTimeout(ctx, 15*time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var lastMutationID, lastCommand, lastFailureReason string
	var lastIsDone bool
	var lastQueryErr error
	for poisonMutationID == "" {
		var mutationID, command, failureReason string
		var isDone bool
		err := s.conn.QueryRow(mutationWaitCtx, `
			SELECT mutation_id, command, latest_fail_reason, is_done
			FROM system.mutations
			WHERE database = 'crawlobserver' AND table = 'pages' AND position(command, ?) > 0
			ORDER BY create_time DESC LIMIT 1`, poisonRankTable,
		).Scan(&mutationID, &command, &failureReason, &isDone)
		lastQueryErr = err
		if err == nil {
			lastMutationID, lastCommand, lastFailureReason, lastIsDone = mutationID, command, failureReason, isDone
			failureLower := strings.ToLower(failureReason)
			unknownTableFailure := strings.Contains(failureLower, "unknown table") ||
				strings.Contains(failureLower, "unknown_table") || strings.Contains(failureLower, "code: 60")
			if !isDone && unknownTableFailure {
				poisonMutationID = mutationID
				break
			}
		}
		select {
		case <-mutationWaitCtx.Done():
			cancelMutationWait()
			t.Fatalf("unrelated mutation did not remain pending with UNKNOWN_TABLE: last_id=%q done=%t command=%q failure=%q query_err=%v", lastMutationID, lastIsDone, lastCommand, lastFailureReason, lastQueryErr)
		case <-ticker.C:
		}
	}
	cancelMutationWait()

	computeCtx, cancelCompute := context.WithTimeout(ctx, 30*time.Second)
	if err := s.ComputePageRankWithOptions(computeCtx, targetSessionID, PageRankOptions{IncludeFooterLinks: true}); err != nil {
		cancelCompute()
		t.Fatalf("ComputePageRankWithOptions with unrelated pending mutation: %v", err)
	}
	cancelCompute()
	firstEvidence, err := s.LatestFinalizedPageRankEvidence(ctx, targetSessionID)
	if err != nil {
		t.Fatalf("read finalized PageRank evidence: %v", err)
	}
	if firstEvidence.State != PageRankEvidenceFinalized {
		t.Fatalf("PageRank evidence state = %s, want finalized", firstEvidence.State)
	}
	assertPageRankWritebackRows(t, s, targetSessionID, firstEvidence.AttemptID, targetURL, childURL, missingURL, assetURL, unrelatedSessionID, unrelatedURL, now)
	assertPageRankTempTableDropped(t, s, targetSessionID, firstEvidence.AttemptID)

	var oldTableCount uint64
	if err := s.conn.QueryRow(ctx, `SELECT count() FROM system.tables WHERE database = 'crawlobserver' AND name = ?`, oldRankTable).Scan(&oldTableCount); err != nil {
		t.Fatalf("check old session-named PageRank table: %v", err)
	}
	if oldTableCount != 1 {
		t.Fatalf("old session-named PageRank table count = %d, want 1", oldTableCount)
	}
	var mutationStillPending uint64
	if err := s.conn.QueryRow(ctx, `SELECT count() FROM system.mutations
		WHERE database = 'crawlobserver' AND table = 'pages' AND mutation_id = ? AND is_done = 0`, poisonMutationID).Scan(&mutationStillPending); err != nil {
		t.Fatalf("check unrelated mutation after PageRank: %v", err)
	}
	if mutationStillPending != 1 {
		t.Fatalf("unrelated failed mutation count after PageRank = %d, want 1 pending", mutationStillPending)
	}

	if err := s.conn.Exec(ctx, `KILL MUTATION
		WHERE database = 'crawlobserver' AND table = 'pages' AND mutation_id = ? SYNC`, poisonMutationID); err != nil {
		t.Fatalf("kill unrelated mutation before compaction: %v", err)
	}
	poisonMutationID = ""
	if err := s.conn.Exec(ctx, "OPTIMIZE TABLE crawlobserver.pages FINAL"); err != nil {
		t.Fatalf("compact pages after PageRank writeback: %v", err)
	}
	assertPageRankWritebackRows(t, s, targetSessionID, firstEvidence.AttemptID, targetURL, childURL, missingURL, assetURL, unrelatedSessionID, unrelatedURL, now)

	computeCtx, cancelCompute = context.WithTimeout(ctx, 30*time.Second)
	if err := s.ComputePageRankWithOptions(computeCtx, targetSessionID, PageRankOptions{IncludeFooterLinks: true}); err != nil {
		cancelCompute()
		t.Fatalf("second ComputePageRankWithOptions: %v", err)
	}
	cancelCompute()
	secondEvidence, err := s.LatestFinalizedPageRankEvidence(ctx, targetSessionID)
	if err != nil {
		t.Fatalf("read second finalized PageRank evidence: %v", err)
	}
	if secondEvidence.State != PageRankEvidenceFinalized || secondEvidence.AttemptID == firstEvidence.AttemptID {
		t.Fatalf("second PageRank evidence = %#v, want a new finalized attempt", secondEvidence)
	}
	assertPageRankWritebackRows(t, s, targetSessionID, secondEvidence.AttemptID, targetURL, childURL, missingURL, assetURL, unrelatedSessionID, unrelatedURL, now)
	assertPageRankTempTableDropped(t, s, targetSessionID, secondEvidence.AttemptID)
	if err := s.conn.Exec(ctx, "OPTIMIZE TABLE crawlobserver.pages FINAL"); err != nil {
		t.Fatalf("compact pages after second PageRank writeback: %v", err)
	}
	assertPageRankWritebackRows(t, s, targetSessionID, secondEvidence.AttemptID, targetURL, childURL, missingURL, assetURL, unrelatedSessionID, unrelatedURL, now)
}

func assertPageRankWritebackRows(t *testing.T, s *Store, sessionID, attemptID, targetURL, childURL, missingURL, assetURL, unrelatedSessionID, unrelatedURL string, expectedCrawledAt time.Time) {
	t.Helper()
	ctx := context.Background()
	for _, want := range []struct {
		url       string
		title     string
		bodyHTML  string
		depth     uint16
		status    uint16
		content   string
		positive  bool
		oldRank   float64
		crawledAt time.Time
	}{
		{targetURL, "retained title", "<main>retained</main>", 4, 200, "text/html", true, 92, expectedCrawledAt},
		{childURL, "child", "", 0, 200, "text/html", true, 93, expectedCrawledAt.Add(time.Second)},
		{missingURL, "missing", "", 0, 404, "text/html", false, 94, expectedCrawledAt.Add(2 * time.Second)},
		{assetURL, "asset", "", 0, 200, "text/css", false, 95, expectedCrawledAt.Add(3 * time.Second)},
	} {
		var title, bodyHTML, contentType, revision string
		var depth, status uint16
		var rank float64
		var crawledAt time.Time
		if err := s.conn.QueryRow(ctx, `SELECT title, body_html, depth, status_code, content_type, pagerank, toString(pagerank_revision), crawled_at
			FROM crawlobserver.pages FINAL WHERE crawl_session_id = ? AND url = ?`, sessionID, want.url,
		).Scan(&title, &bodyHTML, &depth, &status, &contentType, &rank, &revision, &crawledAt); err != nil {
			t.Fatalf("read PageRank writeback row %s: %v", want.url, err)
		}
		if title != want.title || bodyHTML != want.bodyHTML || depth != want.depth || status != want.status || contentType != want.content || !crawledAt.Equal(want.crawledAt) || revision != attemptID {
			t.Errorf("page %s fields changed: title=%q html=%q depth=%d status=%d type=%q crawled_at=%s revision=%s", want.url, title, bodyHTML, depth, status, contentType, crawledAt, revision)
		}
		if (rank > 0) != want.positive {
			t.Errorf("page %s rank = %g, positive=%t; want positive=%t", want.url, rank, rank > 0, want.positive)
		}
		if rank == want.oldRank {
			t.Errorf("page %s retained stale PageRank %g", want.url, rank)
		}
	}

	var totalRows, revisedRows, zeroRankRows uint64
	if err := s.conn.QueryRow(ctx, `SELECT count(), countIf(pagerank_revision = toUUID(?)), countIf(pagerank = 0)
		FROM crawlobserver.pages FINAL WHERE crawl_session_id = ?`, attemptID, sessionID,
	).Scan(&totalRows, &revisedRows, &zeroRankRows); err != nil {
		t.Fatalf("count all PageRank writeback rows: %v", err)
	}
	if totalRows != 4 || revisedRows != totalRows || zeroRankRows != 2 {
		t.Errorf("PageRank writeback totals = rows:%d revised:%d zero:%d, want 4/4/2", totalRows, revisedRows, zeroRankRows)
	}

	var unrelatedTitle string
	var unrelatedDepth uint16
	var unrelatedRank float64
	var unrelatedRevision string
	if err := s.conn.QueryRow(ctx, `SELECT title, depth, pagerank, toString(pagerank_revision)
		FROM crawlobserver.pages FINAL WHERE crawl_session_id = ? AND url = ?`, unrelatedSessionID, unrelatedURL,
	).Scan(&unrelatedTitle, &unrelatedDepth, &unrelatedRank, &unrelatedRevision); err != nil {
		t.Fatalf("read unrelated PageRank row: %v", err)
	}
	if unrelatedTitle != "unrelated" || unrelatedDepth != 7 || unrelatedRank != 42 || unrelatedRevision != "00000000-0000-0000-0000-000000000000" {
		t.Errorf("unrelated page changed: title=%q depth=%d rank=%g revision=%s", unrelatedTitle, unrelatedDepth, unrelatedRank, unrelatedRevision)
	}
}

func assertPageRankTempTableDropped(t *testing.T, s *Store, sessionID, attemptID string) {
	t.Helper()
	tableName := "tmp_pagerank_" + strings.ReplaceAll(sessionID, "-", "") + "_" + strings.ReplaceAll(attemptID, "-", "")
	var count uint64
	if err := s.conn.QueryRow(context.Background(), `SELECT count() FROM system.tables WHERE database = 'crawlobserver' AND name = ?`, tableName).Scan(&count); err != nil {
		t.Fatalf("check attempt PageRank table cleanup: %v", err)
	}
	if count != 0 {
		t.Errorf("attempt PageRank table %s remains after writeback", tableName)
	}
}
