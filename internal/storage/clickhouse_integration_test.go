//go:build integration

package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// testStore creates a Store connected to a local ClickHouse, runs migrations,
// and returns it. Required integration gates fail rather than silently skip.
func testStore(t *testing.T) *Store {
	t.Helper()

	host := os.Getenv("CH_HOST")
	if host == "" {
		host = "localhost"
	}
	port := 19000 // default mapped port for crawlobserver-clickhouse
	if p := os.Getenv("CH_PORT"); p != "" {
		fmt.Sscanf(p, "%d", &port)
	}
	username := os.Getenv("CH_USER")
	if username == "" {
		username = "default"
	}
	password := os.Getenv("CH_PASSWORD")

	s, err := NewStore(host, port, "default", username, password)
	if err != nil {
		if os.Getenv("CRAWLOBSERVER_REQUIRE_CLICKHOUSE") == "1" {
			t.Fatalf("ClickHouse required but unavailable: %v", err)
		}
		t.Skipf("ClickHouse not available: %v", err)
	}

	ctx := context.Background()

	// Create test database
	if err := s.conn.Exec(ctx, "CREATE DATABASE IF NOT EXISTS crawlobserver"); err != nil {
		t.Fatalf("creating test database: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	return s
}

// cleanupSession removes all test data for a given session ID.
func cleanupSession(t *testing.T, s *Store, sessionID string) {
	t.Helper()
	ctx := context.Background()
	tables := []string{"external_link_checks", "links"}
	for _, tbl := range tables {
		if err := s.conn.Exec(ctx, fmt.Sprintf(
			"ALTER TABLE crawlobserver.%s DELETE WHERE crawl_session_id = ?", tbl,
		), sessionID); err != nil {
			t.Logf("cleanup %s: %v", tbl, err)
		}
	}
}

func TestGetExpiredDomains_NoData(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	result, err := s.GetExpiredDomains(ctx, "00000000-0000-0000-0000-000000000000", 100, 0, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 0 {
		t.Errorf("expected total 0, got %d", result.Total)
	}
	if len(result.Domains) != 0 {
		t.Errorf("expected 0 domains, got %d", len(result.Domains))
	}
}

func TestGetExpiredDomains_FullScenario(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	sessionID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	t.Cleanup(func() { cleanupSession(t, s, sessionID) })
	// Clean before in case previous run left data
	cleanupSession(t, s, sessionID)

	now := time.Now()

	// Insert external link checks:
	// - expired.com: 3 URLs, all dns_not_found → expired
	// - gone.org: 1 URL with old-style "no such host" error → expired
	// - alive.com: 1 URL with dns_not_found + 1 URL with status 200 → NOT expired
	// - down.net: 1 URL with connection_refused → NOT expired (DNS works)
	checks := []ExternalLinkCheck{
		{CrawlSessionID: sessionID, URL: "https://www.expired.com/page1", Error: "dns_not_found", CheckedAt: now},
		{CrawlSessionID: sessionID, URL: "https://expired.com/page2", Error: "dns_not_found", CheckedAt: now},
		{CrawlSessionID: sessionID, URL: "https://cdn.expired.com/asset", Error: "dns_not_found", CheckedAt: now},
		{CrawlSessionID: sessionID, URL: "https://gone.org/old", Error: `Get "https://gone.org/old": dial tcp: lookup gone.org: no such host`, CheckedAt: now},
		{CrawlSessionID: sessionID, URL: "https://alive.com/page", Error: "dns_not_found", CheckedAt: now},
		{CrawlSessionID: sessionID, URL: "https://alive.com/ok", StatusCode: 200, CheckedAt: now},
		{CrawlSessionID: sessionID, URL: "https://down.net/x", Error: "connection_refused", CheckedAt: now},
	}
	if err := s.InsertExternalLinkChecks(ctx, checks); err != nil {
		t.Fatalf("inserting checks: %v", err)
	}

	// Insert links (source pages → target URLs)
	links := []LinkRow{
		{CrawlSessionID: sessionID, SourceURL: "https://site-a.com/page1", TargetURL: "https://www.expired.com/page1", IsInternal: false, CrawledAt: now},
		{CrawlSessionID: sessionID, SourceURL: "https://site-b.com/links", TargetURL: "https://expired.com/page2", IsInternal: false, CrawledAt: now},
		{CrawlSessionID: sessionID, SourceURL: "https://site-a.com/page1", TargetURL: "https://gone.org/old", IsInternal: false, CrawledAt: now},
		{CrawlSessionID: sessionID, SourceURL: "https://site-a.com/page1", TargetURL: "https://alive.com/page", IsInternal: false, CrawledAt: now},
		// Internal link (should NOT appear in sources)
		{CrawlSessionID: sessionID, SourceURL: "https://site-a.com/page1", TargetURL: "https://site-a.com/page2", IsInternal: true, CrawledAt: now},
	}
	if err := s.InsertLinks(ctx, links); err != nil {
		t.Fatalf("inserting links: %v", err)
	}

	// Wait for ClickHouse to process mutations
	time.Sleep(500 * time.Millisecond)

	// Test: get all expired domains
	result, err := s.GetExpiredDomains(ctx, sessionID, 100, 0, false)
	if err != nil {
		t.Fatalf("GetExpiredDomains: %v", err)
	}

	if result.Total != 2 {
		t.Fatalf("expected total 2 expired domains, got %d", result.Total)
	}
	if len(result.Domains) != 2 {
		t.Fatalf("expected 2 domains, got %d", len(result.Domains))
	}

	// expired.com should be first (3 dead URLs > 1)
	domainMap := make(map[string]ExpiredDomain)
	for _, d := range result.Domains {
		domainMap[d.RegistrableDomain] = d
	}

	// Verify expired.com
	exp, ok := domainMap["expired.com"]
	if !ok {
		t.Fatal("expired.com not found in results")
	}
	if exp.DeadURLsChecked != 3 {
		t.Errorf("expired.com: expected 3 dead URLs, got %d", exp.DeadURLsChecked)
	}
	if len(exp.Sources) < 1 {
		t.Errorf("expired.com: expected sources, got %d", len(exp.Sources))
	}

	// Verify gone.org (old-style error format)
	gone, ok := domainMap["gone.org"]
	if !ok {
		t.Fatal("gone.org not found in results — old-style error not matched by hybrid condition")
	}
	if gone.DeadURLsChecked != 1 {
		t.Errorf("gone.org: expected 1 dead URL, got %d", gone.DeadURLsChecked)
	}

	// Verify alive.com and down.net are NOT in results
	if _, ok := domainMap["alive.com"]; ok {
		t.Error("alive.com should NOT be expired (has a 200 response)")
	}
	if _, ok := domainMap["down.net"]; ok {
		t.Error("down.net should NOT be expired (connection_refused, not DNS)")
	}
}

func TestGetExpiredDomains_Pagination(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	sessionID := "aaaaaaaa-bbbb-cccc-dddd-ffffffffffff"
	t.Cleanup(func() { cleanupSession(t, s, sessionID) })
	cleanupSession(t, s, sessionID)

	now := time.Now()

	// Insert 3 expired domains with different URL counts for deterministic ordering
	checks := []ExternalLinkCheck{
		// domain-a.com: 3 URLs
		{CrawlSessionID: sessionID, URL: "https://domain-a.com/1", Error: "dns_not_found", CheckedAt: now},
		{CrawlSessionID: sessionID, URL: "https://domain-a.com/2", Error: "dns_not_found", CheckedAt: now},
		{CrawlSessionID: sessionID, URL: "https://domain-a.com/3", Error: "dns_not_found", CheckedAt: now},
		// domain-b.com: 2 URLs
		{CrawlSessionID: sessionID, URL: "https://domain-b.com/1", Error: "dns_not_found", CheckedAt: now},
		{CrawlSessionID: sessionID, URL: "https://domain-b.com/2", Error: "dns_not_found", CheckedAt: now},
		// domain-c.com: 1 URL
		{CrawlSessionID: sessionID, URL: "https://domain-c.com/1", Error: "dns_not_found", CheckedAt: now},
	}
	if err := s.InsertExternalLinkChecks(ctx, checks); err != nil {
		t.Fatalf("inserting checks: %v", err)
	}

	time.Sleep(500 * time.Millisecond)

	// Page 1: limit=2, offset=0 → should get domain-a.com (3) and domain-b.com (2)
	page1, err := s.GetExpiredDomains(ctx, sessionID, 2, 0, false)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if page1.Total != 3 {
		t.Errorf("expected total 3, got %d", page1.Total)
	}
	if len(page1.Domains) != 2 {
		t.Fatalf("expected 2 domains on page 1, got %d", len(page1.Domains))
	}
	if page1.Domains[0].RegistrableDomain != "domain-a.com" {
		t.Errorf("expected first domain domain-a.com, got %s", page1.Domains[0].RegistrableDomain)
	}

	// Page 2: limit=2, offset=2 → should get domain-c.com (1)
	page2, err := s.GetExpiredDomains(ctx, sessionID, 2, 2, false)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if page2.Total != 3 {
		t.Errorf("expected total 3, got %d", page2.Total)
	}
	if len(page2.Domains) != 1 {
		t.Fatalf("expected 1 domain on page 2, got %d", len(page2.Domains))
	}
	if page2.Domains[0].RegistrableDomain != "domain-c.com" {
		t.Errorf("expected domain-c.com, got %s", page2.Domains[0].RegistrableDomain)
	}
}

func TestCurrentSnapshotDeltaRetryFinalizesBindingWithoutReapplyingContent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := "phase-25-1-retry"
	baselineID := "25100000-0000-4000-8000-000000000001"
	deltaID := "25100000-0000-4000-8000-000000000002"
	currentID := "25100000-0000-4000-8000-000000000003"
	foldedID := "25100000-0000-4000-8000-000000000004"
	obsoleteID := "25100000-0000-4000-8000-000000000005"
	newerFullID := "25100000-0000-4000-8000-000000000006"
	baselineEval := "25100000-0000-4000-8000-000000000011"
	deltaEval := "25100000-0000-4000-8000-000000000012"
	baselinePR := "25100000-0000-4000-8000-000000000021"
	deltaPR := "25100000-0000-4000-8000-000000000022"
	now := time.Now().UTC()

	t.Cleanup(func() {
		for _, table := range []string{"project_current_snapshot_deltas", "project_current_snapshots", "project_current_snapshot_promotions_v2"} {
			_ = s.conn.Exec(ctx, fmt.Sprintf("ALTER TABLE crawlobserver.%s DELETE WHERE project_id = ? SETTINGS mutations_sync = 1", table), projectID)
		}
		for _, table := range []string{"crawl_quality_current_pointers", "crawl_quality_evaluations", "pagerank_evidence"} {
			_ = s.conn.Exec(ctx, fmt.Sprintf("ALTER TABLE crawlobserver.%s DELETE WHERE session_id IN (?, ?, ?, ?, ?, ?) SETTINGS mutations_sync = 1", table), baselineID, deltaID, currentID, foldedID, obsoleteID, newerFullID)
		}
		_ = s.conn.Exec(ctx, "ALTER TABLE crawlobserver.crawl_sessions DELETE WHERE id IN (?, ?, ?, ?, ?, ?) SETTINGS mutations_sync = 1", baselineID, deltaID, currentID, foldedID, obsoleteID, newerFullID)
		for _, table := range []string{"pages", "links"} {
			_ = s.conn.Exec(ctx, fmt.Sprintf("ALTER TABLE crawlobserver.%s DELETE WHERE crawl_session_id IN (?, ?, ?, ?, ?, ?) SETTINGS mutations_sync = 1", table), baselineID, deltaID, currentID, foldedID, obsoleteID, newerFullID)
		}
	})

	for _, session := range []*CrawlSession{
		{ID: baselineID, StartedAt: now.Add(-time.Hour), FinishedAt: now.Add(-50 * time.Minute), Status: "completed", ProjectID: &projectID, Label: "baseline"},
		{ID: deltaID, StartedAt: now.Add(-time.Minute), FinishedAt: now, Status: "completed", ProjectID: &projectID, Label: "Daily Delta Crawl"},
		{ID: currentID, StartedAt: now.Add(-time.Hour), FinishedAt: now, Status: "completed", ProjectID: &projectID, Label: CurrentSnapshotLabel},
	} {
		if err := s.InsertSession(ctx, session); err != nil {
			t.Fatalf("insert session %s: %v", session.ID, err)
		}
	}
	for _, evidence := range []PageRankEvidence{
		{SessionID: baselineID, AttemptID: baselinePR, State: PageRankEvidenceFinalized, Source: PageRankEvidenceComputed, AlgorithmVersion: PageRankAlgorithmVersion, PredicateVersion: PageRankEligiblePredicateVersion, OccurredAt: now},
		{SessionID: deltaID, AttemptID: deltaPR, State: PageRankEvidenceFinalized, Source: PageRankEvidenceComputed, AlgorithmVersion: PageRankAlgorithmVersion, PredicateVersion: PageRankEligiblePredicateVersion, OccurredAt: now},
	} {
		if err := s.appendPageRankEvidence(ctx, &evidence); err != nil {
			t.Fatalf("append evidence: %v", err)
		}
	}
	for _, quality := range []CrawlQualityResult{
		{SessionID: baselineID, ProjectID: projectID, EvaluationRevision: baselineEval, EvaluatorRevision: "eval-v2", RulesRevision: "rules-v2", PageRankEvidenceRevision: baselinePR, PageRankEvidenceStatus: PageRankEvidenceFinalized, PageRankPredicateVersion: PageRankEligiblePredicateVersion, Trusted: true, IsFullCrawl: true, Status: "trusted", Score: 100, EvaluatedAt: now},
		{SessionID: deltaID, ProjectID: projectID, BaselineSessionID: baselineID, BaselineEvaluationRevision: baselineEval, EvaluationRevision: deltaEval, EvaluatorRevision: "eval-v3", RulesRevision: "rules-v3", PageRankEvidenceRevision: deltaPR, PageRankEvidenceStatus: PageRankEvidenceFinalized, PageRankPredicateVersion: PageRankEligiblePredicateVersion, Trusted: true, Status: "trusted", Score: 100, EvaluatedAt: now},
	} {
		if _, _, err := s.PublishCrawlQualityEvaluation(ctx, quality, ""); err != nil {
			t.Fatalf("publish quality: %v", err)
		}
	}
	initial := ProjectCurrentSnapshot{
		ProjectID: projectID, CurrentSessionID: currentID, BaselineSessionID: baselineID,
		SourceSessionID: baselineID, SourceStartedAt: now.Add(-time.Hour),
		ContentWatermarkSessionID: baselineID, ContentWatermarkStartedAt: now.Add(-time.Hour),
		QualityBaselineSessionID:  baselineID,
		QualityEvaluationRevision: baselineEval, BaselineQualityEvaluationRevision: baselineEval,
		PageRankEvidenceRevision: baselinePR, QualityEvaluatorRevision: "eval-v2", QualityRulesRevision: "rules-v2",
		QualityPromotionStatus: "applied", BaselineCreatedAt: now, UpdatedAt: now,
	}
	if err := s.upsertProjectCurrentSnapshot(ctx, &initial); err != nil {
		t.Fatalf("insert initial snapshot: %v", err)
	}
	if initial.SnapshotRevision == 0 {
		t.Fatal("initial snapshot revision was not allocated")
	}
	// Failure injection: content was already overlaid and marked, but the bound
	// project pointer was never published.
	if err := s.conn.Exec(ctx, `INSERT INTO crawlobserver.project_current_snapshot_deltas
		(project_id, delta_session_id, current_session_id, applied_at) VALUES (?, toUUID(?), toUUID(?), ?)`,
		projectID, deltaID, currentID, now); err != nil {
		t.Fatalf("insert content-stage marker: %v", err)
	}
	binding := CrawlQualityPromotionEvent{
		ProjectID: projectID, SessionID: deltaID, EvaluationRevision: deltaEval, PageRankEvidenceRevision: deltaPR,
		BaselineSessionID: baselineID, BaselineEvaluationRevision: baselineEval, EvaluatorRevision: "eval-v3", RulesRevision: "rules-v3",
	}
	result, err := s.PromoteDeltaToCurrentSnapshot(ctx, projectID, deltaID, baselineID, 14, 30, PageRankOptions{}, binding)
	if err != nil {
		t.Fatalf("retry promotion: %v", err)
	}
	if !currentSnapshotBindingMatches(*result, binding) || result.LastDeltaSessionID != deltaID {
		t.Fatalf("retry did not finalize bound pointer: %#v", result)
	}
	if result.SnapshotRevision <= initial.SnapshotRevision {
		t.Fatalf("snapshot revision did not advance: initial=%d result=%d", initial.SnapshotRevision, result.SnapshotRevision)
	}
	if _, err := s.LatestPageRankEvidence(ctx, currentID); !errors.Is(err, ErrNoFinalizedPageRankEvidence) {
		t.Fatalf("retry unexpectedly recomputed PageRank: %v", err)
	}
	if _, _, err := s.ValidateProjectCurrentSnapshotBinding(ctx, *result); err != nil {
		t.Fatalf("fresh delta baseline binding rejected: %v", err)
	}
	baselinePointer, err := s.getCrawlQualityCurrentPointer(ctx, baselineID)
	if err != nil {
		t.Fatalf("read F1 quality pointer: %v", err)
	}
	restartedStore := &Store{conn: s.conn}
	replayed, err := restartedStore.PromoteDeltaToCurrentSnapshot(ctx, projectID, deltaID, baselineID, 14, 30, PageRankOptions{}, binding)
	if err != nil || replayed.CurrentSessionID != result.CurrentSessionID || replayed.SnapshotRevision != result.SnapshotRevision {
		t.Fatalf("restart mixed-evaluator replay=%#v err=%v", replayed, err)
	}
	baselineAfter, err := restartedStore.getCrawlQualityCurrentPointer(ctx, baselineID)
	if err != nil || baselineAfter.EvaluationRevision != baselinePointer.EvaluationRevision {
		t.Fatalf("restart replay advanced F1 quality pointer before=%#v after=%#v err=%v", baselinePointer, baselineAfter, err)
	}
	markerCount, err := s.countProjectCurrentSnapshotDeltas(ctx, projectID)
	if err != nil {
		t.Fatalf("count delta markers before baseline mismatch: %v", err)
	}
	emptyBaseline := binding
	emptyBaseline.BaselineSessionID = ""
	if _, err := s.PromoteDeltaToCurrentSnapshot(ctx, projectID, deltaID, baselineID, 14, 30, PageRankOptions{}, emptyBaseline); !errors.Is(err, ErrCurrentSnapshotBindingConflict) {
		t.Fatalf("empty delta baseline err=%v, want binding conflict", err)
	}
	mismatchedBaseline := binding
	mismatchedBaseline.BaselineSessionID = uuid.NewString()
	if _, err := s.PromoteDeltaToCurrentSnapshot(ctx, projectID, deltaID, baselineID, 14, 30, PageRankOptions{}, mismatchedBaseline); !errors.Is(err, ErrCurrentSnapshotBindingConflict) {
		t.Fatalf("mismatched delta binding baseline err=%v, want binding conflict", err)
	}
	if count, countErr := s.countProjectCurrentSnapshotDeltas(ctx, projectID); countErr != nil || count != markerCount {
		t.Fatalf("baseline mismatch mutated delta markers before=%d after=%d err=%v", markerCount, count, countErr)
	}
	if err := s.appendPageRankEvidence(ctx, &PageRankEvidence{
		SessionID: baselineID, AttemptID: baselinePR, State: PageRankEvidenceFinalized,
		Source: PageRankEvidenceComputed, AlgorithmVersion: PageRankAlgorithmVersion,
		PredicateVersion: "pagerank-eligible-old", OccurredAt: now.Add(time.Millisecond),
	}); err != nil {
		t.Fatalf("append stale-predicate baseline evidence: %v", err)
	}
	if _, _, err := s.ValidateProjectCurrentSnapshotBinding(ctx, *result); !errors.Is(err, ErrCurrentSnapshotBindingConflict) {
		t.Fatalf("stale baseline predicate did not invalidate snapshot binding: %v", err)
	}
	if err := s.appendPageRankEvidence(ctx, &PageRankEvidence{
		SessionID: baselineID, AttemptID: baselinePR, State: PageRankEvidenceFinalized,
		Source: PageRankEvidenceComputed, AlgorithmVersion: PageRankAlgorithmVersion,
		PredicateVersion: PageRankEligiblePredicateVersion, OccurredAt: now.Add(2 * time.Millisecond),
	}); err != nil {
		t.Fatalf("restore current-predicate baseline evidence: %v", err)
	}
	if _, _, err := s.ValidateProjectCurrentSnapshotBinding(ctx, *result); err != nil {
		t.Fatalf("restored baseline predicate rejected: %v", err)
	}
	if err := s.copySessionForSnapshot(ctx, currentID, foldedID, CurrentBaselineSnapshotLabel, now.Add(time.Second)); err != nil {
		t.Fatalf("create folded baseline: %v", err)
	}
	if err := s.copySessionForSnapshot(ctx, currentID, obsoleteID, CurrentBaselineSnapshotLabel, now.Add(time.Second)); err != nil {
		t.Fatalf("create obsolete baseline: %v", err)
	}
	folded := *result
	folded.BaselineSessionID = foldedID
	folded.LastDeltaSessionID = ""
	folded.DeltaCount = 0
	folded.BaselineCreatedAt = now.Add(time.Second)
	folded.UpdatedAt = now.Add(time.Second)
	if err := s.upsertProjectCurrentSnapshot(ctx, &folded); err != nil {
		t.Fatalf("publish folded pointer: %v", err)
	}
	if folded.SnapshotRevision <= result.SnapshotRevision {
		t.Fatalf("folded snapshot revision did not advance: result=%d folded=%d", result.SnapshotRevision, folded.SnapshotRevision)
	}
	result, err = s.PromoteDeltaToCurrentSnapshot(ctx, projectID, deltaID, baselineID, 14, 30, PageRankOptions{}, binding)
	if err != nil {
		t.Fatalf("retry post-pointer fold cleanup: %v", err)
	}
	if count, err := s.countProjectCurrentSnapshotDeltas(ctx, projectID); err != nil || count != 0 {
		t.Fatalf("fold cleanup markers count=%d err=%v", count, err)
	}
	if _, err := s.GetSession(ctx, obsoleteID); err == nil {
		t.Fatal("obsolete synthetic baseline survived cleanup retry")
	}
	// The full-crawl promotion is newer than the folded delta chain. Replaying
	// the old delta must fail before it can overlay content or recreate a marker.
	if err := s.InsertSession(ctx, &CrawlSession{ID: newerFullID, StartedAt: now.Add(time.Hour), FinishedAt: now.Add(time.Hour), Status: "completed", ProjectID: &projectID, Label: "full"}); err != nil {
		t.Fatalf("insert newer full crawl: %v", err)
	}
	newer := *result
	newer.SourceSessionID, newer.SourceStartedAt = newerFullID, now.Add(time.Hour)
	newer.ContentWatermarkSessionID, newer.ContentWatermarkStartedAt = newerFullID, now.Add(time.Hour)
	newer.BaselineSessionID, newer.UpdatedAt = newerFullID, now.Add(time.Hour)
	if err := s.upsertProjectCurrentSnapshot(ctx, &newer); err != nil {
		t.Fatalf("publish newer full watermark: %v", err)
	}
	staleBinding := binding
	staleBinding.BaselineSessionID = newerFullID
	staleBinding.BaselineEvaluationRevision = uuid.NewString()
	if _, err := s.PromoteDeltaToCurrentSnapshot(ctx, projectID, deltaID, newerFullID, 14, 30, PageRankOptions{}, staleBinding); !errors.Is(err, ErrCurrentSnapshotSourceSuperseded) {
		t.Fatalf("old delta after newer full err=%v, want superseded", err)
	}
	if got, err := s.GetProjectCurrentSnapshot(ctx, projectID); err != nil || got.ContentWatermarkSessionID != newerFullID {
		t.Fatalf("old delta changed authoritative watermark: snapshot=%#v err=%v", got, err)
	}
	if count, err := s.countProjectCurrentSnapshotDeltas(ctx, projectID); err != nil || count != 0 {
		t.Fatalf("old delta recreated content marker count=%d err=%v", count, err)
	}
	if err := s.appendPageRankEvidence(ctx, &PageRankEvidence{
		SessionID: baselineID, AttemptID: "25100000-0000-4000-8000-000000000023",
		State: PageRankEvidenceFailed, Source: PageRankEvidenceComputed,
		AlgorithmVersion: PageRankAlgorithmVersion, PredicateVersion: PageRankEligiblePredicateVersion,
		OccurredAt: now.Add(time.Second), Failure: "injected newer baseline failure",
	}); err != nil {
		t.Fatalf("append newer baseline failure: %v", err)
	}
	if _, _, err := s.ValidateProjectCurrentSnapshotBinding(ctx, *result); !errors.Is(err, ErrCurrentSnapshotBindingConflict) {
		t.Fatalf("newer failed baseline evidence did not invalidate snapshot binding: %v", err)
	}
	// A later evaluator generation changes the current pointer, but replay must
	// still validate the exact immutable facts captured by the old journal row.
	if _, _, err := s.ValidateProjectCurrentSnapshotHistoricalBinding(ctx, *result); err != nil {
		t.Fatalf("historical binding rejected before quality pointer advance: %v", err)
	}
	_, advanced, err := s.PublishCrawlQualityEvaluation(ctx, CrawlQualityResult{
		SessionID: deltaID, ProjectID: projectID, BaselineSessionID: baselineID, BaselineEvaluationRevision: baselineEval,
		EvaluationRevision: uuid.NewString(), EvaluatorRevision: "eval-v2", RulesRevision: "rules-v2",
		PageRankEvidenceRevision: deltaPR, PageRankEvidenceStatus: PageRankEvidenceFinalized,
		PageRankPredicateVersion: PageRankEligiblePredicateVersion, Trusted: true, Status: "trusted", Score: 100, EvaluatedAt: now.Add(2 * time.Hour),
	}, deltaEval)
	if err != nil {
		t.Fatalf("advance delta quality pointer: %v", err)
	}
	revised := *result
	revised.QualityEvaluationRevision = advanced.EvaluationRevision
	revised.UpdatedAt = now.Add(3 * time.Hour)
	if err := s.upsertProjectCurrentSnapshot(ctx, &revised); err != nil {
		t.Fatalf("publish same-watermark revised binding: %v", err)
	}
	if err := s.conn.Exec(ctx, `OPTIMIZE TABLE crawlobserver.project_current_snapshot_promotions_v2 FINAL`); err != nil {
		t.Fatalf("compact same-watermark journal revisions: %v", err)
	}
	restarted := &Store{conn: s.conn}
	if old, oldErr := restarted.GetProjectCurrentSnapshotRevision(ctx, projectID, result.SnapshotRevision); oldErr != nil || old.QualityEvaluationRevision != result.QualityEvaluationRevision {
		t.Fatalf("historical journal revision readback=%#v err=%v", old, oldErr)
	}
	if current, currentErr := restarted.GetProjectCurrentSnapshotRevision(ctx, projectID, revised.SnapshotRevision); currentErr != nil || current.QualityEvaluationRevision != revised.QualityEvaluationRevision {
		t.Fatalf("revised journal revision readback=%#v err=%v", current, currentErr)
	}
	if _, _, err := restarted.ValidateProjectCurrentSnapshotHistoricalBinding(ctx, *result); err != nil {
		t.Fatalf("historical binding rejected after quality pointer advance: %v", err)
	}
}

func TestFoldCleanupPrunesRawDeltaPredecessorButPreservesReplayFacts(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := "snapshot-fold-replay-" + uuid.NewString()
	f1, d1, d2, d3, d0, current := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	f1Eval, d1Eval, d2Eval, d3Eval, d0Eval := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	f1PR, d1PR, d2PR, d3PR, d0PR := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Now().UTC().Truncate(time.Millisecond)
	t.Cleanup(func() {
		_ = s.conn.Exec(ctx, `ALTER TABLE crawlobserver.project_current_snapshot_promotions_v2 DELETE WHERE project_id = ? SETTINGS mutations_sync = 1`, projectID)
		_ = s.conn.Exec(ctx, `ALTER TABLE crawlobserver.crawl_sessions DELETE WHERE id IN (?, ?, ?, ?, ?, ?) SETTINGS mutations_sync = 1`, f1, d1, d2, d3, d0, current)
	})
	for id, spec := range map[string]struct {
		started    time.Time
		label, cfg string
	}{
		f1: {now.Add(-4 * time.Hour), "full", "{}"}, d1: {now.Add(-3 * time.Hour), "Daily Delta Crawl", "{}"},
		d2: {now.Add(-2 * time.Hour), "Daily Delta Crawl", `{"Crawler":{"DeltaPlan":{"baseline_content_watermark_session_id":"` + d1 + `"}}}`},
		d3: {now.Add(-time.Hour), "Daily Delta Crawl", `{"Crawler":{"DeltaPlan":{"baseline_content_watermark_session_id":"` + d2 + `"}}}`},
		d0: {now.Add(-5 * time.Hour), "Daily Delta Crawl", "{}"}, current: {now.Add(-4 * time.Hour), CurrentSnapshotLabel, "{}"},
	} {
		if err := s.InsertSession(ctx, &CrawlSession{ID: id, StartedAt: spec.started, FinishedAt: spec.started, Status: "completed", ProjectID: &projectID, Label: spec.label, Config: spec.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.InsertPages(ctx, []PageRow{{CrawlSessionID: d1, URL: "https://example.test/d1", FinalURL: "https://example.test/d1", StatusCode: 200}}); err != nil {
		t.Fatal(err)
	}
	quality := func(sessionID, evaluationID, evidenceID, baselineID, baselineEval string, full bool) {
		if err := s.appendPageRankEvidence(ctx, &PageRankEvidence{SessionID: sessionID, AttemptID: evidenceID, State: PageRankEvidenceFinalized, Source: PageRankEvidenceComputed, AlgorithmVersion: PageRankAlgorithmVersion, PredicateVersion: PageRankEligiblePredicateVersion, OccurredAt: now}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.PublishCrawlQualityEvaluation(ctx, CrawlQualityResult{SessionID: sessionID, ProjectID: projectID, BaselineSessionID: baselineID, BaselineEvaluationRevision: baselineEval, EvaluationRevision: evaluationID, EvaluatorRevision: "eval", RulesRevision: "rules", PageRankEvidenceRevision: evidenceID, PageRankEvidenceStatus: PageRankEvidenceFinalized, PageRankPredicateVersion: PageRankEligiblePredicateVersion, Trusted: true, IsFullCrawl: full, Status: "trusted", Score: 100, EvaluatedAt: now}, ""); err != nil {
			t.Fatal(err)
		}
	}
	quality(f1, f1Eval, f1PR, "", "", true)
	quality(d1, d1Eval, d1PR, f1, f1Eval, false)
	quality(d2, d2Eval, d2PR, f1, f1Eval, false)
	quality(d3, d3Eval, d3PR, f1, f1Eval, false)
	quality(d0, d0Eval, d0PR, f1, f1Eval, false)
	base := ProjectCurrentSnapshot{ProjectID: projectID, SourceSessionID: f1, SourceStartedAt: now.Add(-4 * time.Hour), ContentWatermarkSessionID: d1, ContentWatermarkStartedAt: now.Add(-3 * time.Hour), CurrentSessionID: current, BaselineSessionID: f1, QualityBaselineSessionID: f1, QualityEvaluationRevision: d1Eval, BaselineQualityEvaluationRevision: f1Eval, PageRankEvidenceRevision: d1PR, QualityEvaluatorRevision: "eval", QualityRulesRevision: "rules", QualityPromotionStatus: "applied", BaselineCreatedAt: now, UpdatedAt: now}
	if err := s.upsertProjectCurrentSnapshot(ctx, &base); err != nil {
		t.Fatal(err)
	}
	live := base
	live.ContentWatermarkSessionID, live.ContentWatermarkStartedAt, live.QualityEvaluationRevision, live.PageRankEvidenceRevision = d2, now.Add(-2*time.Hour), d2Eval, d2PR
	live.UpdatedAt = now.Add(time.Second)
	if err := s.upsertProjectCurrentSnapshot(ctx, &live); err != nil {
		t.Fatal(err)
	}
	if err := s.conn.Exec(ctx, `INSERT INTO crawlobserver.project_current_snapshot_deltas (project_id, delta_session_id, current_session_id, applied_at) VALUES (?, toUUID(?), toUUID(?), ?)`, projectID, d1, current, now); err != nil {
		t.Fatal(err)
	}
	if err := s.completeFoldedSnapshotCleanup(ctx, live); err != nil {
		t.Fatal(err)
	}
	if count, err := s.countProjectCurrentSnapshotDeltas(ctx, projectID); err != nil || count != 0 {
		t.Fatalf("fold cleanup markers=%d err=%v", count, err)
	}
	if _, err := s.GetSession(ctx, d1); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("D1 raw session survived prune: %v", err)
	}
	if pages, err := s.CountPages(ctx, d1); err != nil || pages != 0 {
		t.Fatalf("D1 heavy pages remain=%d err=%v", pages, err)
	}
	if _, err := s.GetCrawlQualityEvaluation(ctx, d1, d1Eval); err != nil {
		t.Fatalf("D1 immutable quality lost: %v", err)
	}
	if _, err := s.GetPageRankEvidence(ctx, d1, d1PR); err != nil {
		t.Fatalf("D1 immutable evidence lost: %v", err)
	}
	if err := s.DeleteSession(ctx, d1); err == nil || !strings.Contains(err.Error(), "current_snapshot_delta_plan_predecessor") {
		t.Fatalf("D1 immutable facts unprotected err=%v", err)
	}
	restarted := &Store{conn: s.conn}
	if _, _, err := restarted.ValidateProjectCurrentSnapshotHistoricalBinding(ctx, base); err != nil {
		t.Fatalf("D1 historical replay invalid after restart: %v", err)
	}
	if _, _, err := restarted.ValidateProjectCurrentSnapshotHistoricalBinding(ctx, live); err != nil {
		t.Fatalf("D2 historical replay invalid after restart: %v", err)
	}
	liveD3 := live
	liveD3.ContentWatermarkSessionID, liveD3.ContentWatermarkStartedAt, liveD3.QualityEvaluationRevision, liveD3.PageRankEvidenceRevision = d3, now.Add(-time.Hour), d3Eval, d3PR
	liveD3.UpdatedAt = now.Add(2 * time.Second)
	if err := s.upsertProjectCurrentSnapshot(ctx, &liveD3); err != nil {
		t.Fatal(err)
	}
	if err := s.cleanupSupersededDeltaPlanPredecessor(ctx, liveD3); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetCrawlQualityEvaluation(ctx, d1, d1Eval); err == nil {
		t.Fatal("D1 metadata survived after D3 advanced")
	}
	if _, err := s.GetPageRankEvidence(ctx, d1, d1PR); err == nil {
		t.Fatal("D1 evidence survived after D3 advanced")
	}
	if _, err := s.GetCrawlQualityEvaluation(ctx, d2, d2Eval); err != nil {
		t.Fatalf("D2 metadata was not retained for D3: %v", err)
	}
	if err := s.deleteDeltaSnapshotSessionChecked(ctx, d0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetCrawlQualityEvaluation(ctx, d0, d0Eval); err == nil {
		t.Fatal("unrelated D0 immutable quality survived")
	}
}

func TestTrustedFullInitializeRecoversUnprovableLegacySnapshotIntoV2(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID, fullID, deltaID := "snapshot-legacy-recovery-"+uuid.NewString(), uuid.NewString(), uuid.NewString()
	fullEval, fullPR := uuid.NewString(), uuid.NewString()
	now := time.Now().UTC().Truncate(time.Millisecond)
	t.Cleanup(func() {
		for _, table := range []string{"project_current_snapshots", "project_current_snapshot_promotions", "project_current_snapshot_promotions_v2"} {
			_ = s.conn.Exec(ctx, `ALTER TABLE crawlobserver.`+table+` DELETE WHERE project_id = ? SETTINGS mutations_sync = 1`, projectID)
		}
		_ = s.conn.Exec(ctx, `ALTER TABLE crawlobserver.crawl_sessions DELETE WHERE id IN (?, ?) SETTINGS mutations_sync = 1`, fullID, deltaID)
	})
	for id, spec := range map[string]struct {
		started time.Time
		label   string
	}{fullID: {now, "full"}, deltaID: {now.Add(time.Minute), "Daily Delta Crawl"}} {
		if err := s.InsertSession(ctx, &CrawlSession{ID: id, StartedAt: spec.started, FinishedAt: spec.started, Status: "completed", ProjectID: &projectID, Label: spec.label}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.conn.Exec(ctx, `INSERT INTO crawlobserver.project_current_snapshots (project_id, current_session_id, baseline_session_id, baseline_created_at, last_delta_session_id, delta_count, updated_at) VALUES (?, toUUID(?), '', ?, '', 0, ?)`, projectID, uuid.NewString(), now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetProjectCurrentSnapshot(ctx, projectID); !errors.Is(err, ErrCurrentSnapshotBindingConflict) {
		t.Fatalf("legacy GET err=%v, want binding conflict", err)
	}
	if _, err := s.PromoteDeltaToCurrentSnapshot(ctx, projectID, deltaID, fullID, 14, 30, PageRankOptions{}, CrawlQualityPromotionEvent{}); err == nil {
		t.Fatal("delta unexpectedly recovered unprovable legacy snapshot")
	}
	if err := s.appendPageRankEvidence(ctx, &PageRankEvidence{SessionID: fullID, AttemptID: fullPR, State: PageRankEvidenceFinalized, Source: PageRankEvidenceComputed, AlgorithmVersion: PageRankAlgorithmVersion, PredicateVersion: PageRankEligiblePredicateVersion, OccurredAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PublishCrawlQualityEvaluation(ctx, CrawlQualityResult{SessionID: fullID, ProjectID: projectID, EvaluationRevision: fullEval, EvaluatorRevision: "eval", RulesRevision: "rules", PageRankEvidenceRevision: fullPR, PageRankEvidenceStatus: PageRankEvidenceFinalized, PageRankPredicateVersion: PageRankEligiblePredicateVersion, Trusted: true, IsFullCrawl: true, Status: "trusted", Score: 100, EvaluatedAt: now}, ""); err != nil {
		t.Fatal(err)
	}
	binding := CrawlQualityPromotionEvent{ProjectID: projectID, SessionID: fullID, EvaluationRevision: fullEval, PageRankEvidenceRevision: fullPR, BaselineSessionID: fullID, BaselineEvaluationRevision: fullEval, EvaluatorRevision: "eval", RulesRevision: "rules"}
	badBinding := binding
	badBinding.BaselineSessionID = deltaID
	badBinding.BaselineEvaluationRevision = uuid.NewString()
	if _, err := s.InitializeProjectCurrentSnapshot(ctx, projectID, fullID, badBinding); !errors.Is(err, ErrCurrentSnapshotBindingConflict) {
		t.Fatalf("non-self full binding err=%v, want conflict", err)
	}
	var v2Before uint64
	if err := s.conn.QueryRow(ctx, `SELECT count() FROM crawlobserver.project_current_snapshot_promotions_v2 WHERE project_id = ?`, projectID).Scan(&v2Before); err != nil || v2Before != 0 {
		t.Fatalf("non-self binding published v2 count=%d err=%v", v2Before, err)
	}
	snap, err := s.InitializeProjectCurrentSnapshot(ctx, projectID, fullID, binding)
	if err != nil {
		t.Fatalf("trusted full recovery initialize: %v", err)
	}
	var journalBefore uint64
	if err := s.conn.QueryRow(ctx, `SELECT count() FROM crawlobserver.project_current_snapshot_promotions_v2 WHERE project_id = ?`, projectID).Scan(&journalBefore); err != nil {
		t.Fatal(err)
	}
	retry, err := (&Store{conn: s.conn}).InitializeProjectCurrentSnapshot(ctx, projectID, fullID, binding)
	if err != nil || retry.CurrentSessionID != snap.CurrentSessionID || retry.SnapshotRevision != snap.SnapshotRevision {
		t.Fatalf("full initialize retry=%#v err=%v, want existing pointer", retry, err)
	}
	var journalAfter uint64
	if err := s.conn.QueryRow(ctx, `SELECT count() FROM crawlobserver.project_current_snapshot_promotions_v2 WHERE project_id = ?`, projectID).Scan(&journalAfter); err != nil || journalAfter != journalBefore {
		t.Fatalf("full initialize retry journal before=%d after=%d err=%v", journalBefore, journalAfter, err)
	}
	var syntheticCount uint64
	if err := s.conn.QueryRow(ctx, `SELECT count() FROM crawlobserver.crawl_sessions FINAL WHERE project_id = ? AND label = ?`, projectID, CurrentSnapshotLabel).Scan(&syntheticCount); err != nil || syntheticCount != 1 {
		t.Fatalf("full initialize retry synthetic session count=%d err=%v", syntheticCount, err)
	}
	if _, _, err := s.ValidateProjectCurrentSnapshotBinding(ctx, *snap); err != nil {
		t.Fatalf("recovered v2 snapshot invalid: %v", err)
	}
	var legacyRows uint64
	if err := s.conn.QueryRow(ctx, `SELECT count() FROM crawlobserver.project_current_snapshots WHERE project_id = ?`, projectID).Scan(&legacyRows); err != nil || legacyRows != 1 {
		t.Fatalf("legacy audit row changed count=%d err=%v", legacyRows, err)
	}
	if got, err := s.GetProjectCurrentSnapshot(ctx, projectID); err != nil || got.SourceSessionID != fullID {
		t.Fatalf("v2 canonical recovery=%#v err=%v", got, err)
	}
}
