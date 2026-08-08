//go:build integration

package storage

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPageRankEvidenceSequenceOrdersEqualTimestampsAcrossRetryAndRestart(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	sessionID := uuid.NewString()
	cleanupPageRankEvidenceSession(t, s, sessionID)
	t.Cleanup(func() { cleanupPageRankEvidenceSession(t, s, sessionID) })
	sameTime := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

	firstAttemptID := uuid.NewString()
	started := PageRankEvidence{SessionID: sessionID, AttemptID: firstAttemptID, State: PageRankEvidenceStarted, OccurredAt: sameTime}
	if err := s.appendPageRankEvidence(ctx, &started); err != nil {
		t.Fatalf("append first started: %v", err)
	}
	finalized := started
	finalized.State = PageRankEvidenceFinalized
	finalized.OccurredAt = sameTime
	if err := s.appendPageRankEvidence(ctx, &finalized); err != nil {
		t.Fatalf("append first finalized: %v", err)
	}
	if started.EventSequence != 1 || finalized.EventSequence != 2 {
		t.Fatalf("first lifecycle sequences = %d/%d, want 1/2", started.EventSequence, finalized.EventSequence)
	}

	retryAttemptID := uuid.NewString()
	retryStarted := PageRankEvidence{SessionID: sessionID, AttemptID: retryAttemptID, State: PageRankEvidenceStarted, OccurredAt: sameTime}
	if err := s.appendPageRankEvidence(ctx, &retryStarted); err != nil {
		t.Fatalf("append retry started: %v", err)
	}
	if _, err := s.LatestFinalizedPageRankEvidence(ctx, sessionID); !errors.Is(err, ErrNoFinalizedPageRankEvidence) {
		t.Fatalf("newer started event must fail closed, got %v", err)
	}
	retryFailed := retryStarted
	retryFailed.State = PageRankEvidenceFailed
	retryFailed.OccurredAt = sameTime
	retryFailed.Failure = "mutation rejected: Bearer integration-secret token=integration-token"
	if err := s.appendPageRankEvidence(ctx, &retryFailed); err != nil {
		t.Fatalf("append retry failed: %v", err)
	}

	restarted := &Store{conn: s.conn}
	latest, err := restarted.LatestPageRankEvidence(ctx, sessionID)
	if err != nil {
		t.Fatalf("restart readback: %v", err)
	}
	if latest.State != PageRankEvidenceFailed || latest.EventSequence != 4 {
		t.Fatalf("restart latest = %#v, want failed sequence 4", latest)
	}
	if strings.Contains(latest.Failure, "integration-secret") || strings.Contains(latest.Failure, "integration-token") {
		t.Fatalf("restart readback exposed credential material: %s", latest.Failure)
	}

	recoveryStarted := retryStarted
	recoveryStarted.OccurredAt = sameTime
	if err := restarted.appendPageRankEvidence(ctx, &recoveryStarted); err != nil {
		t.Fatalf("append recovery started: %v", err)
	}
	recoveryFinalized := recoveryStarted
	recoveryFinalized.State = PageRankEvidenceFinalized
	recoveryFinalized.OccurredAt = sameTime
	if err := restarted.appendPageRankEvidence(ctx, &recoveryFinalized); err != nil {
		t.Fatalf("append recovery finalized: %v", err)
	}
	if err := s.conn.Exec(ctx, `OPTIMIZE TABLE crawlobserver.pagerank_evidence FINAL`); err != nil {
		t.Fatalf("compact pagerank evidence: %v", err)
	}
	latest, err = restarted.LatestFinalizedPageRankEvidence(ctx, sessionID)
	if err != nil {
		t.Fatalf("recovery finalized readback: %v", err)
	}
	if latest.AttemptID != retryAttemptID || latest.EventSequence != 6 {
		t.Fatalf("recovery latest = %#v, want retry attempt sequence 6", latest)
	}
}

func cleanupPageRankEvidenceSession(t *testing.T, s *Store, sessionID string) {
	t.Helper()
	ctx := context.Background()
	for _, table := range []string{"pages", "links", "pagerank_evidence"} {
		if err := s.conn.Exec(ctx, "ALTER TABLE crawlobserver."+table+" DROP PARTITION ?", sessionID); err != nil {
			t.Logf("cleanup %s: %v", table, err)
		}
	}
}

func assertPageRankReportsUnavailable(t *testing.T, s *Store, sessionID string) {
	t.Helper()
	ctx := context.Background()
	checks := []struct {
		name string
		run  func() error
	}{
		{"distribution", func() error { _, err := s.PageRankDistribution(ctx, sessionID, 20); return err }},
		{"treemap", func() error { _, err := s.PageRankTreemap(ctx, sessionID, 2, 1); return err }},
		{"top", func() error { _, err := s.PageRankTop(ctx, sessionID, 20, 0, ""); return err }},
		{"weighted", func() error {
			_, err := s.WeightedPageRankTop(ctx, sessionID, "pagerank-report-test", 20, 0, "", "", "")
			return err
		}},
	}
	for _, check := range checks {
		if err := check.run(); !errors.Is(err, ErrPageRankReportUnavailable) {
			t.Errorf("%s report error = %v, want ErrPageRankReportUnavailable", check.name, err)
		}
	}
}

func TestPageRankEvidenceComputeUsesEligiblePopulation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	sessionID := uuid.NewString()
	cleanupPageRankEvidenceSession(t, s, sessionID)
	t.Cleanup(func() { cleanupPageRankEvidenceSession(t, s, sessionID) })
	now := time.Now().UTC()
	pages := []PageRow{
		{CrawlSessionID: sessionID, URL: "https://example.test/a", FinalURL: "https://example.test/a", StatusCode: 200, ContentType: "text/html", Canonical: "https://example.test/a", CanonicalIsSelf: true, IsIndexable: true, CrawledAt: now},
		{CrawlSessionID: sessionID, URL: "https://example.test/b", StatusCode: 200, ContentType: "text/html", Canonical: "", IsIndexable: false, CrawledAt: now},
		{CrawlSessionID: sessionID, URL: "https://example.test/missing", StatusCode: 404, ContentType: "text/html", CrawledAt: now},
		{CrawlSessionID: sessionID, URL: "https://example.test/redirect", FinalURL: "https://example.test/a", StatusCode: 200, ContentType: "text/html", CrawledAt: now},
		{CrawlSessionID: sessionID, URL: "https://example.test/alias", StatusCode: 200, ContentType: "text/html", Canonical: "https://example.test/a", CrawledAt: now},
		{CrawlSessionID: sessionID, URL: "https://example.test/style.css", StatusCode: 200, ContentType: "text/css", CrawledAt: now},
	}
	if err := s.InsertPages(ctx, pages); err != nil {
		t.Fatalf("InsertPages: %v", err)
	}
	if err := s.InsertLinks(ctx, []LinkRow{
		{CrawlSessionID: sessionID, SourceURL: "https://example.test/a", TargetURL: "https://example.test/b", IsInternal: true, CrawledAt: now},
		{CrawlSessionID: sessionID, SourceURL: "https://example.test/b", TargetURL: "https://example.test/a", IsInternal: true, CrawledAt: now},
	}); err != nil {
		t.Fatalf("InsertLinks: %v", err)
	}
	if err := s.ComputePageRankWithOptions(ctx, sessionID, PageRankOptions{IncludeFooterLinks: true}); err != nil {
		t.Fatalf("ComputePageRankWithOptions: %v", err)
	}
	evidence, err := s.LatestFinalizedPageRankEvidence(ctx, sessionID)
	if err != nil {
		t.Fatalf("LatestFinalizedPageRankEvidence: %v", err)
	}
	if evidence.State != PageRankEvidenceFinalized || evidence.Source != PageRankEvidenceComputed {
		t.Fatalf("unexpected evidence: %#v", evidence)
	}
	if evidence.EligiblePageCount != 2 || evidence.PositivePageCount != 2 || evidence.ZeroPageCount != 0 {
		t.Fatalf("evidence population = %d/%d/%d, want 2/2/0", evidence.EligiblePageCount, evidence.PositivePageCount, evidence.ZeroPageCount)
	}
	population, revised, err := s.PageRankPopulationForRevision(ctx, sessionID, evidence.AttemptID)
	if err != nil {
		t.Fatalf("PageRankPopulationForRevision: %v", err)
	}
	if population.Eligible != 2 || population.Positive != 2 || population.Zero != 0 || revised != 2 {
		t.Fatalf("FINAL population/revision = %#v, %d; want 2/2/0, 2", population, revised)
	}
	top, err := s.PageRankTop(ctx, sessionID, 20, 0, "")
	if err != nil {
		t.Fatalf("PageRankTop: %v", err)
	}
	if top.Total != 2 || top.Eligible != 2 || top.Positive != 2 || top.Zero != 0 || top.Evidence == nil || top.Evidence.AttemptID != evidence.AttemptID {
		t.Fatalf("top provenance = %#v", top)
	}

	newer := *evidence
	newer.AttemptID = uuid.NewString()
	newer.State = PageRankEvidenceStarted
	newer.OccurredAt = now
	if err := s.appendPageRankEvidence(ctx, &newer); err != nil {
		t.Fatalf("append newer started evidence: %v", err)
	}
	assertPageRankReportsUnavailable(t, s, sessionID)

	newer.State = PageRankEvidenceFailed
	newer.OccurredAt = now
	newer.Failure = "injected report lifecycle failure"
	if err := s.appendPageRankEvidence(ctx, &newer); err != nil {
		t.Fatalf("append newer failed evidence: %v", err)
	}
	assertPageRankReportsUnavailable(t, s, sessionID)

	// The newest event is terminal, but no eligible page carries its revision.
	newer.State = PageRankEvidenceFinalized
	newer.OccurredAt = now
	newer.Failure = ""
	if err := s.appendPageRankEvidence(ctx, &newer); err != nil {
		t.Fatalf("append partial-revision finalized evidence: %v", err)
	}
	assertPageRankReportsUnavailable(t, s, sessionID)

	wrongPredicate := *evidence
	wrongPredicate.AttemptID = uuid.NewString()
	wrongPredicate.Source = PageRankEvidenceObservedExisting
	wrongPredicate.PredicateVersion = "pagerank-eligible-legacy"
	wrongPredicate.OccurredAt = now
	if err := s.appendPageRankEvidence(ctx, &wrongPredicate); err != nil {
		t.Fatalf("append wrong-predicate finalized evidence: %v", err)
	}
	assertPageRankReportsUnavailable(t, s, sessionID)
}

func TestLegacyEvidenceAdoptionIsDeterministicAndReadOnly(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	sessionID := uuid.NewString()
	cleanupPageRankEvidenceSession(t, s, sessionID)
	t.Cleanup(func() { cleanupPageRankEvidenceSession(t, s, sessionID) })
	now := time.Now().UTC()
	if err := s.InsertPages(ctx, []PageRow{{
		CrawlSessionID: sessionID, URL: "https://legacy.test/", StatusCode: 200, ContentType: "text/html",
		CanonicalIsSelf: true, IsIndexable: true, PageRank: 42, CrawledAt: now,
	}}); err != nil {
		t.Fatalf("InsertPages: %v", err)
	}
	first, err := s.AdoptObservedPageRankEvidence(ctx, sessionID, PageRankOptions{IncludeFooterLinks: true})
	if err != nil {
		t.Fatalf("AdoptObservedPageRankEvidence first: %v", err)
	}
	second, err := s.AdoptObservedPageRankEvidence(ctx, sessionID, PageRankOptions{IncludeFooterLinks: true})
	if err != nil {
		t.Fatalf("AdoptObservedPageRankEvidence second: %v", err)
	}
	if first.AttemptID != second.AttemptID || first.Source != PageRankEvidenceObservedExisting || first.State != PageRankEvidenceFinalized {
		t.Fatalf("adoption was not deterministic: first=%#v second=%#v", first, second)
	}
	var revision string
	if err := s.conn.QueryRow(ctx, `SELECT toString(pagerank_revision) FROM crawlobserver.pages FINAL WHERE crawl_session_id = ?`, sessionID).Scan(&revision); err != nil {
		t.Fatalf("reading legacy revision: %v", err)
	}
	if revision != "00000000-0000-0000-0000-000000000000" {
		t.Fatalf("adoption must not mutate pages, revision=%s", revision)
	}
}

func TestLegacyEvidenceAdoptionConcurrentCallsShareRevision(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	sessionID := uuid.NewString()
	cleanupPageRankEvidenceSession(t, s, sessionID)
	t.Cleanup(func() { cleanupPageRankEvidenceSession(t, s, sessionID) })
	if err := s.InsertPages(ctx, []PageRow{{
		CrawlSessionID: sessionID, URL: "https://concurrent.test/", StatusCode: 200, ContentType: "text/html",
		CanonicalIsSelf: true, PageRank: 10, CrawledAt: time.Now().UTC(),
	}}); err != nil {
		t.Fatalf("InsertPages: %v", err)
	}
	const callers = 4
	ids := make(chan string, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			evidence, err := s.AdoptObservedPageRankEvidence(ctx, sessionID, PageRankOptions{IncludeFooterLinks: true})
			if err != nil {
				errs <- err
				return
			}
			ids <- evidence.AttemptID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent adoption: %v", err)
	}
	var revision string
	for id := range ids {
		if revision == "" {
			revision = id
		} else if id != revision {
			t.Fatalf("adoption revisions differ: %s != %s", id, revision)
		}
	}
}
