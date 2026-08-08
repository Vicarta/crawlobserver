package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SEObserver/crawlobserver/internal/apikeys"
	"github.com/SEObserver/crawlobserver/internal/config"
	"github.com/SEObserver/crawlobserver/internal/storage"
)

func TestCrawlConfigDeltaPlannedPagesPrefersDeltaMetadata(t *testing.T) {
	configJSON := `{"Crawler":{"MaxPages":1610,"DeltaPlannedPages":70}}`
	if got := crawlConfigDeltaPlannedPages(configJSON); got != 70 {
		t.Fatalf("crawlConfigDeltaPlannedPages() = %d, want 70", got)
	}
}

func TestCrawlConfigDeltaPlannedPagesFallsBackToMaxPages(t *testing.T) {
	configJSON := `{"Crawler":{"MaxPages":1610}}`
	if got := crawlConfigDeltaPlannedPages(configJSON); got != 1610 {
		t.Fatalf("crawlConfigDeltaPlannedPages() = %d, want 1610", got)
	}
}

func TestCrawlConfigDeltaPlanOverridesPlannedPages(t *testing.T) {
	configJSON := `{"Crawler":{"MaxPages":1610,"DeltaPlannedPages":70,"DeltaPlan":{"launched_candidates":2643,"source_counts":{"sitemap":2638}}}}`
	if got := crawlConfigDeltaPlannedPages(configJSON); got != 2643 {
		t.Fatalf("crawlConfigDeltaPlannedPages() = %d, want 2643", got)
	}
	plan := crawlConfigDeltaPlan(configJSON)
	if plan == nil {
		t.Fatal("crawlConfigDeltaPlan() = nil")
	}
	if plan.SourceCounts["sitemap"] != 2638 {
		t.Fatalf("sitemap source count = %d, want 2638", plan.SourceCounts["sitemap"])
	}
}

func TestEvaluateDeltaPlanGateBlocksTinySitemapPlan(t *testing.T) {
	srv := &Server{}
	qs := qualityGateMock{matched: 5}
	settings := apikeys.DefaultProjectQualitySettings("project-di")
	settings.DeltaMinSitemapPercent = 30
	settings.DeltaMinSitemapCandidates = 1
	plan := &config.DeltaPlanConfig{
		TotalCandidates:         5,
		LaunchedCandidates:      5,
		SourceCounts:            map[string]int{"sitemap": 5},
		BaselineSitemapURLCount: 2638,
		LaunchedURLs:            []string{"https://example.com/a"},
	}
	findings := srv.evaluateDeltaPlanGate(context.Background(), qs, "session-1", "project-di", plan, settings, time.Now())
	if !hasFinding(findings, "delta_sitemap_candidate_ratio_low") {
		t.Fatalf("expected delta_sitemap_candidate_ratio_low, got %#v", findings)
	}
}

func TestEvaluateDeltaPlanGateUsesFreshSitemapCounts(t *testing.T) {
	settings := apikeys.DefaultProjectQualitySettings("project-di")
	settings.DeltaMinSitemapPercent = 50
	settings.DeltaMinSitemapCandidates = 10
	plan := &config.DeltaPlanConfig{
		SourceCounts:            map[string]int{"sitemap": 9999},
		BaselineSitemapURLCount: 9999,
		SitemapRefresh: &config.DeltaSitemapRefresh{
			Mode:             deltaSitemapRefreshFresh,
			FreshURLCount:    4,
			SnapshotURLCount: 100,
		},
	}
	findings := (&Server{}).evaluateDeltaPlanGate(context.Background(), qualityGateMock{}, "session-1", "project-di", plan, settings, time.Now())
	if !hasFinding(findings, "delta_sitemap_candidates_low") {
		t.Fatalf("fresh sitemap count must be used instead of source_counts, got %#v", findings)
	}
}

func TestEvaluateDeltaPlanGateMarksSitemapFallbackNonFresh(t *testing.T) {
	settings := apikeys.DefaultProjectQualitySettings("project-di")
	plan := &config.DeltaPlanConfig{
		SitemapRefresh: &config.DeltaSitemapRefresh{
			Mode:             deltaSitemapRefreshSnapshotFallback,
			SnapshotURLCount: 123,
		},
	}
	findings := (&Server{}).evaluateDeltaPlanGate(context.Background(), qualityGateMock{}, "session-1", "project-di", plan, settings, time.Now())
	if !hasFinding(findings, "delta_sitemap_snapshot_fallback") {
		t.Fatalf("expected explicit fallback finding, got %#v", findings)
	}
	for _, finding := range findings {
		if finding.FindingType == "delta_sitemap_snapshot_fallback" && finding.Blocking {
			t.Fatalf("snapshot fallback should be visible but not masquerade as a fresh blocking ratio failure")
		}
	}
}

func TestEvaluateDeltaPlanGateBlocksMissingLaunchedCandidateCoverage(t *testing.T) {
	srv := &Server{}
	qs := qualityGateMock{matched: 1}
	settings := apikeys.DefaultProjectQualitySettings("project-di")
	settings.DeltaCandidateCoveragePercent = 100
	settings.DeltaMinSitemapPercent = 0
	plan := &config.DeltaPlanConfig{
		TotalCandidates:    2,
		LaunchedCandidates: 2,
		SourceCounts:       map[string]int{"problem_pages": 2},
		LaunchedURLs:       []string{"https://example.com/a", "https://example.com/b"},
	}
	findings := srv.evaluateDeltaPlanGate(context.Background(), qs, "session-1", "project-di", plan, settings, time.Now())
	if !hasFinding(findings, "delta_candidate_coverage_low") {
		t.Fatalf("expected delta_candidate_coverage_low, got %#v", findings)
	}
}

func TestEvaluateDeltaPromotionGateBlocksHigh5xxRate(t *testing.T) {
	srv := &Server{}
	qs := qualityGateMock{matched: 100}
	settings := apikeys.DefaultProjectQualitySettings("project-astro")
	settings.DeltaStatus5xxPercent = 5
	settings.DeltaStatus5xxMinPages = 5
	settings.DeltaCandidateCoveragePercent = 0
	settings.DeltaMinCrawledPercent = 0
	settings.DeltaMinSitemapPercent = 0
	sess := storage.CrawlSession{
		ID:           "delta-1",
		PagesCrawled: 100,
		Config:       `{"Crawler":{"DeltaPlannedPages":100}}`,
	}
	metrics := &storage.CrawlQualityMetrics{Status5xx: 10}
	findings := srv.evaluateDeltaPromotionGate(context.Background(), qs, sess, "project-astro", settings, metrics, time.Now())
	if !hasFinding(findings, "delta_status_5xx_high") {
		t.Fatalf("expected delta_status_5xx_high, got %#v", findings)
	}
}

func TestCompareQualityMetricsBlocks5xxGrowth(t *testing.T) {
	settings := apikeys.DefaultProjectQualitySettings("project-astro")
	settings.Status5xxPercent = 5
	settings.Status5xxMinDelta = 5
	current := &storage.CrawlQualityMetrics{Status5xx: 10}
	baseline := &storage.CrawlQualityMetrics{Status5xx: 0}
	findings := compareQualityMetrics("session-1", "project-astro", current, baseline, settings, time.Now())
	if !hasFinding(findings, "status_5xx_growth") {
		t.Fatalf("expected status_5xx_growth, got %#v", findings)
	}
}

func TestQualityEvaluationUsesFinalizedPageRankEvidence(t *testing.T) {
	keyStore, err := apikeys.NewStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer keyStore.Close()
	projectID := "project-gerus"
	qs := qualityGateMock{metrics: &storage.CrawlQualityMetrics{HTMLPages: 51, PageRankZeroTopPages: 0}}
	srv := &Server{store: qualityServerStore{mockStore: &mockStore{}, qualityGateMock: qs}, keyStore: keyStore}
	evidence := &storage.PageRankEvidence{
		SessionID: "cecabb70-b621-48a1-9dc4-1feb3c3757cb", AttemptID: "d6d5f1ef-b7b1-4b10-b1e0-d8e8e9416108",
		State: storage.PageRankEvidenceFinalized, Source: storage.PageRankEvidenceComputed,
		PredicateVersion: storage.PageRankEligiblePredicateVersion, EligiblePageCount: 51, PositivePageCount: 51,
	}
	result, err := srv.evaluateSessionQualityResult(context.Background(), storage.CrawlSession{
		ID: evidence.SessionID, ProjectID: &projectID, Status: "completed", PagesCrawled: 51,
	}, evidence, "test")
	if err != nil {
		t.Fatalf("evaluateSessionQualityResult: %v", err)
	}
	if result.PageRankZero != 0 || hasFinding(result.Findings, "pagerank_zero_top_pages") {
		t.Fatalf("51/51 positive evidence produced a zero-PageRank blocker: %#v", result)
	}
	if result.EvaluationRevision == "" || result.PageRankEvidenceRevision != evidence.AttemptID {
		t.Fatalf("missing deterministic provenance: %#v", result)
	}
}

func TestQualityEvaluationFailsClosedOnStalePageRankPredicate(t *testing.T) {
	keyStore, err := apikeys.NewStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer keyStore.Close()
	projectID := "project-gerus"
	srv := &Server{store: qualityServerStore{mockStore: &mockStore{}, qualityGateMock: qualityGateMock{}}, keyStore: keyStore}
	result, err := srv.evaluateSessionQualityResult(context.Background(), storage.CrawlSession{
		ID: "cecabb70-b621-48a1-9dc4-1feb3c3757cb", ProjectID: &projectID, Status: "completed",
	}, &storage.PageRankEvidence{
		SessionID: "cecabb70-b621-48a1-9dc4-1feb3c3757cb", AttemptID: "d6d5f1ef-b7b1-4b10-b1e0-d8e8e9416108",
		State: storage.PageRankEvidenceFinalized, PredicateVersion: "pagerank-eligible-old",
	}, "test")
	if err != nil {
		t.Fatalf("evaluateSessionQualityResult: %v", err)
	}
	if result.Trusted || !result.Stale || !hasFinding(result.Findings, "pagerank_predicate_version_changed") {
		t.Fatalf("stale PageRank predicate did not fail closed: %#v", result)
	}
}

func TestPageRankEvidenceResponseRedactsFailureCredentials(t *testing.T) {
	sessionID := "cecabb70-b621-48a1-9dc4-1feb3c3757cb"
	store := qualityServerStore{
		mockStore: &mockStore{},
		qualityGateMock: qualityGateMock{evidence: &storage.PageRankEvidence{
			SessionID: sessionID, AttemptID: "d6d5f1ef-b7b1-4b10-b1e0-d8e8e9416108",
			State: storage.PageRankEvidenceFailed, Failure: "Bearer api-secret token=token-secret password=password-secret",
		}},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/id/pagerank/evidence", nil)
	req.SetPathValue("id", sessionID)
	recorder := httptest.NewRecorder()
	(&Server{store: store}).handleSessionPageRankEvidence(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
	for _, secret := range []string{"api-secret", "token-secret", "password-secret"} {
		if strings.Contains(recorder.Body.String(), secret) {
			t.Fatalf("response exposed %q: %s", secret, recorder.Body.String())
		}
	}
	if !strings.Contains(recorder.Body.String(), "[REDACTED]") {
		t.Fatalf("response omitted redaction marker: %s", recorder.Body.String())
	}
}

func TestQualityEvaluationFailsClosedOnNonFinalPageRankEvidence(t *testing.T) {
	keyStore, err := apikeys.NewStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer keyStore.Close()
	projectID := "project-gerus"
	qs := qualityGateMock{}
	srv := &Server{store: qualityServerStore{mockStore: &mockStore{}, qualityGateMock: qs}, keyStore: keyStore}
	result, err := srv.evaluateSessionQualityResult(context.Background(), storage.CrawlSession{
		ID: "cecabb70-b621-48a1-9dc4-1feb3c3757cb", ProjectID: &projectID, Status: "completed",
	}, &storage.PageRankEvidence{AttemptID: "d6d5f1ef-b7b1-4b10-b1e0-d8e8e9416108", State: storage.PageRankEvidenceFailed}, "test")
	if err != nil {
		t.Fatalf("evaluateSessionQualityResult: %v", err)
	}
	if result.Trusted || result.Status != "untrusted" || !hasFinding(result.Findings, "pagerank_evidence_not_finalized") {
		t.Fatalf("non-final evidence did not fail closed: %#v", result)
	}
}

func TestQualityPromotionLockSerializesProjectWork(t *testing.T) {
	lock := qualityPromotionLock("project-serialized")
	lock.Lock()
	acquired := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		qualityPromotionLock("project-serialized").Lock()
		close(acquired)
		qualityPromotionLock("project-serialized").Unlock()
	}()
	select {
	case <-acquired:
		t.Fatal("concurrent project promotion entered the critical section")
	case <-time.After(20 * time.Millisecond):
	}
	lock.Unlock()
	wg.Wait()
}

func TestQualityAuditActorDoesNotContainAPIKey(t *testing.T) {
	keyStore, err := apikeys.NewStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer keyStore.Close()
	created, err := keyStore.CreateAPIKey("audit", "general", nil)
	if err != nil {
		t.Fatal(err)
	}
	var actor string
	handler := apikeys.Authenticate(keyStore, "", "")(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		actor = qualityAuditActor(r)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/id/quality/re-evaluate", strings.NewReader("{}"))
	req.Header.Set("X-API-Key", created.FullKey)
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if actor != "apikey:general" || strings.Contains(actor, created.FullKey) {
		t.Fatalf("unsafe audit actor %q", actor)
	}
}

func TestReevaluateQualityRequiresExplicitConfirmation(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/session/quality/re-evaluate", strings.NewReader(`{"confirm":false,"reason":"repair"}`))
	req.SetPathValue("id", "session")
	recorder := httptest.NewRecorder()
	(&Server{}).handleReevaluateSessionQuality(recorder, req)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "confirm must be true") {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestReevaluateQualityReturnsCurrentProvenanceOnConflict(t *testing.T) {
	sessionID := "cecabb70-b621-48a1-9dc4-1feb3c3757cb"
	projectID := "project-gerus"
	currentRevision := "d6d5f1ef-b7b1-4b10-b1e0-d8e8e9416108"
	actions := []storage.CrawlQualityActionEvent{}
	store := qualityServerStore{
		mockStore: &mockStore{getSessionByID: map[string]*storage.CrawlSession{
			sessionID: {ID: sessionID, ProjectID: &projectID, Status: "completed"},
		}},
		qualityGateMock: qualityGateMock{actions: &actions, current: &storage.CrawlQualityResult{
			SessionID: sessionID, ProjectID: projectID, EvaluationRevision: currentRevision,
		}},
	}
	srv := &Server{store: store, manager: newMockManager()}
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/id/quality/re-evaluate", strings.NewReader(`{"confirm":true,"reason":"repair stale fact","expected_evaluation_revision":"c4212fbb-e76a-47f1-b74e-f0113551eabd"}`))
	req.SetPathValue("id", sessionID)
	recorder := httptest.NewRecorder()
	srv.handleReevaluateSessionQuality(recorder, req)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), currentRevision) || !strings.Contains(recorder.Body.String(), "current_quality") {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
	if len(actions) != 1 || actions[0].Status != "conflict" || actions[0].PreviousEvaluationRevision != currentRevision {
		t.Fatalf("conflict audit was not persisted: %#v", actions)
	}
}

func TestReevaluateQualityDoesNotAdoptOverNewerFailedEvidence(t *testing.T) {
	sessionID := "cecabb70-b621-48a1-9dc4-1feb3c3757cb"
	projectID := "project-gerus"
	adoptCalls := 0
	actions := []storage.CrawlQualityActionEvent{}
	store := qualityServerStore{
		mockStore: &mockStore{getSessionByID: map[string]*storage.CrawlSession{
			sessionID: {ID: sessionID, ProjectID: &projectID, Status: "completed"},
		}},
		qualityGateMock: qualityGateMock{
			actions: &actions, adoptCalls: &adoptCalls,
			evidence: &storage.PageRankEvidence{
				SessionID: sessionID, AttemptID: "d6d5f1ef-b7b1-4b10-b1e0-d8e8e9416108", State: storage.PageRankEvidenceFailed,
			},
		},
	}
	srv := &Server{store: store, manager: newMockManager()}
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/id/quality/re-evaluate", strings.NewReader(`{"confirm":true,"reason":"verify failed evidence"}`))
	req.SetPathValue("id", sessionID)
	recorder := httptest.NewRecorder()
	srv.handleReevaluateSessionQuality(recorder, req)
	if recorder.Code != http.StatusConflict || adoptCalls != 0 {
		t.Fatalf("failed evidence response=%d adopt_calls=%d body=%s", recorder.Code, adoptCalls, recorder.Body.String())
	}
	if len(actions) != 1 || actions[0].Status != "conflict" || actions[0].PageRankEvidenceRevision == "" {
		t.Fatalf("failed evidence audit missing: %#v", actions)
	}
}

func TestQualityReadFailsClosedWhenNewestEvidenceChanged(t *testing.T) {
	sessionID := "cecabb70-b621-48a1-9dc4-1feb3c3757cb"
	projectID := "project-gerus"
	store := qualityServerStore{
		mockStore: &mockStore{},
		qualityGateMock: qualityGateMock{
			current: &storage.CrawlQualityResult{
				SessionID: sessionID, ProjectID: projectID, EvaluationRevision: "quality-revision",
				EvaluatorRevision: qualityEvaluatorRevision, PageRankEvidenceRevision: "older-evidence",
				Trusted: true, Status: "trusted",
			},
			evidence: &storage.PageRankEvidence{SessionID: sessionID, AttemptID: "newer-evidence", State: storage.PageRankEvidenceFailed},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/id/quality", nil)
	req.SetPathValue("id", sessionID)
	recorder := httptest.NewRecorder()
	(&Server{store: store}).handleSessionQuality(recorder, req)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"stale":true`) ||
		!strings.Contains(recorder.Body.String(), `"trusted":false`) ||
		!strings.Contains(recorder.Body.String(), "pagerank_evidence_not_finalized") {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestQualityReadFailsClosedWhenPageRankPredicateChanged(t *testing.T) {
	stored := &storage.CrawlQualityResult{
		SessionID: "cecabb70-b621-48a1-9dc4-1feb3c3757cb", EvaluatorRevision: qualityEvaluatorRevision,
		PageRankEvidenceRevision: "evidence", PageRankPredicateVersion: "pagerank-eligible-old",
		Trusted: true, Status: "trusted",
	}
	result := deriveCurrentQualityReadState(context.Background(), qualityGateMock{evidence: &storage.PageRankEvidence{
		SessionID: stored.SessionID, AttemptID: "evidence", State: storage.PageRankEvidenceFinalized,
		PredicateVersion: "pagerank-eligible-old",
	}}, stored)
	if result.Trusted || !result.Stale || !strings.Contains(strings.Join(result.StaleReasons, ","), "pagerank_predicate_version_changed") {
		t.Fatalf("stale PageRank predicate did not invalidate read: %#v", result)
	}
}

func TestReevaluateConflictFailsClosedWhenAuditWriteFails(t *testing.T) {
	sessionID := "cecabb70-b621-48a1-9dc4-1feb3c3757cb"
	projectID := "project-gerus"
	store := qualityServerStore{
		mockStore: &mockStore{getSessionByID: map[string]*storage.CrawlSession{
			sessionID: {ID: sessionID, ProjectID: &projectID, Status: "completed"},
		}},
		qualityGateMock: qualityGateMock{
			current:   &storage.CrawlQualityResult{SessionID: sessionID, ProjectID: projectID, EvaluationRevision: "current"},
			actionErr: errors.New("audit unavailable"),
		},
	}
	srv := &Server{store: store, manager: newMockManager()}
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/id/quality/re-evaluate", strings.NewReader(`{"confirm":true,"reason":"repair","expected_evaluation_revision":"older"}`))
	req.SetPathValue("id", sessionID)
	recorder := httptest.NewRecorder()
	srv.handleReevaluateSessionQuality(recorder, req)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("audit failure must fail closed, response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestReevaluatePreAuditFailurePreventsQualityMutation(t *testing.T) {
	sessionID := "cecabb70-b621-48a1-9dc4-1feb3c3757cb"
	projectID := "project-gerus"
	publishes := []string{}
	store := qualityServerStore{
		mockStore: &mockStore{getSessionByID: map[string]*storage.CrawlSession{
			sessionID: {ID: sessionID, ProjectID: &projectID, Status: "completed"},
		}},
		qualityGateMock: qualityGateMock{
			actionErr: errors.New("audit unavailable"), publishes: &publishes,
			evidence: &storage.PageRankEvidence{SessionID: sessionID, AttemptID: "evidence", State: storage.PageRankEvidenceFinalized},
			metrics:  &storage.CrawlQualityMetrics{HTMLPages: 1},
		},
	}
	srv := &Server{store: store, manager: newMockManager()}
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/id/quality/re-evaluate", strings.NewReader(`{"confirm":true,"reason":"repair evidence"}`))
	req.SetPathValue("id", sessionID)
	recorder := httptest.NewRecorder()
	srv.handleReevaluateSessionQuality(recorder, req)
	if recorder.Code != http.StatusInternalServerError || len(publishes) != 0 {
		t.Fatalf("pre-audit failure response=%d publishes=%#v body=%s", recorder.Code, publishes, recorder.Body.String())
	}
}

func TestReevaluateQualityWritesOrderedStartedAndAppliedAudit(t *testing.T) {
	keyStore, err := apikeys.NewStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer keyStore.Close()
	sessionID := "cecabb70-b621-48a1-9dc4-1feb3c3757cb"
	projectID := "project-gerus"
	actions := []storage.CrawlQualityActionEvent{}
	store := qualityServerStore{
		mockStore: &mockStore{getSessionByID: map[string]*storage.CrawlSession{
			sessionID: {ID: sessionID, ProjectID: &projectID, Status: "completed", PagesCrawled: 1},
		}},
		qualityGateMock: qualityGateMock{
			actions: &actions, metrics: &storage.CrawlQualityMetrics{HTMLPages: 1},
			evidence: &storage.PageRankEvidence{
				SessionID: sessionID, AttemptID: "d6d5f1ef-b7b1-4b10-b1e0-d8e8e9416108",
				State: storage.PageRankEvidenceFinalized, PredicateVersion: storage.PageRankEligiblePredicateVersion,
			},
		},
	}
	srv := &Server{store: store, manager: newMockManager(), keyStore: keyStore}
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/id/quality/re-evaluate", strings.NewReader(`{"confirm":true,"reason":"repair evidence"}`))
	req.SetPathValue("id", sessionID)
	recorder := httptest.NewRecorder()
	srv.handleReevaluateSessionQuality(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
	if len(actions) != 2 || actions[0].Status != "started" || actions[1].Status != "applied" ||
		actions[0].ActionID == "" || actions[0].ActionID != actions[1].ActionID ||
		actions[1].EventSequence <= actions[0].EventSequence {
		t.Fatalf("ordered action audit missing: %#v", actions)
	}
}

func TestFullCrawlPromotionAttemptKeepsSelfBaselineLineage(t *testing.T) {
	for _, tc := range []struct {
		name     string
		initErr  error
		terminal string
	}{
		{name: "success", terminal: "applied"},
		{name: "failure", initErr: errors.New("snapshot unavailable"), terminal: "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projectID := "project-gerus"
			sessionID := "cecabb70-b621-48a1-9dc4-1feb3c3757cb"
			result := &storage.CrawlQualityResult{
				SessionID: sessionID, ProjectID: projectID, EvaluationRevision: "evaluation", PageRankEvidenceRevision: "evidence",
				EvaluatorRevision: qualityEvaluatorRevision, RulesRevision: "rules", Trusted: true, IsFullCrawl: true,
			}
			evidence := &storage.PageRankEvidence{SessionID: sessionID, AttemptID: "evidence", State: storage.PageRankEvidenceFinalized, PredicateVersion: storage.PageRankEligiblePredicateVersion}
			promotions := []storage.CrawlQualityPromotionEvent{}
			var initBinding storage.CrawlQualityPromotionEvent
			store := qualitySnapshotServerStore{
				mockStore:       &mockStore{},
				qualityGateMock: qualityGateMock{current: result, evidence: evidence, promotions: &promotions},
				currentSnapshotGateMock: currentSnapshotGateMock{
					snapshot: &storage.ProjectCurrentSnapshot{}, initBinding: &initBinding, initErr: tc.initErr,
				},
			}
			sess := storage.CrawlSession{ID: sessionID, ProjectID: &projectID, Status: "completed"}
			_, terminal, err := (&Server{store: store}).reconcileCurrentSnapshotPromotion(context.Background(), store, sess, result, evidence, "repair")
			if err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			if terminal == nil || terminal.Status != tc.terminal || len(promotions) != 2 {
				t.Fatalf("terminal=%#v promotions=%#v", terminal, promotions)
			}
			started := promotions[0]
			finished := promotions[1]
			if started.Status != "started" || started.PromotionID == "" || started.PromotionID != finished.PromotionID ||
				started.BaselineSessionID != sessionID || finished.BaselineSessionID != sessionID ||
				started.BaselineEvaluationRevision != result.EvaluationRevision || initBinding.BaselineSessionID != sessionID {
				t.Fatalf("inconsistent full-crawl promotion lineage: started=%#v finished=%#v binding=%#v", started, finished, initBinding)
			}
		})
	}
}

func TestPromotionStartAuditFailurePreventsSnapshotMutation(t *testing.T) {
	projectID := "project-gerus"
	sessionID := "cecabb70-b621-48a1-9dc4-1feb3c3757cb"
	result := &storage.CrawlQualityResult{
		SessionID: sessionID, ProjectID: projectID, EvaluationRevision: "evaluation", PageRankEvidenceRevision: "evidence",
		EvaluatorRevision: qualityEvaluatorRevision, RulesRevision: "rules", Trusted: true, IsFullCrawl: true,
	}
	evidence := &storage.PageRankEvidence{SessionID: sessionID, AttemptID: "evidence", State: storage.PageRankEvidenceFinalized}
	initCalls := 0
	store := qualitySnapshotServerStore{
		mockStore: &mockStore{},
		qualityGateMock: qualityGateMock{
			current: result, evidence: evidence, promotionErr: errors.New("promotion audit unavailable"),
		},
		currentSnapshotGateMock: currentSnapshotGateMock{snapshot: &storage.ProjectCurrentSnapshot{}, initCalls: &initCalls},
	}
	sess := storage.CrawlSession{ID: sessionID, ProjectID: &projectID, Status: "completed"}
	if _, _, err := (&Server{store: store}).reconcileCurrentSnapshotPromotion(context.Background(), store, sess, result, evidence, "repair"); err == nil {
		t.Fatal("promotion audit failure must be returned")
	}
	if initCalls != 0 {
		t.Fatalf("snapshot initialized before durable promotion start: calls=%d", initCalls)
	}
}

func TestQualityAuditRedactsSecretLikeValues(t *testing.T) {
	event := sanitizeQualityActionEvent(storage.CrawlQualityActionEvent{
		Actor: "apikey:general sk-secretvalue123456", Reason: "token=abc123456789 password:visible Bearer qwertyuiop123456",
	})
	combined := event.Actor + " " + event.Reason
	for _, secret := range []string{"secretvalue123456", "abc123456789", "visible", "qwertyuiop123456"} {
		if strings.Contains(combined, secret) {
			t.Fatalf("secret %q survived audit sanitization: %q", secret, combined)
		}
	}
	if !strings.Contains(combined, "[REDACTED]") {
		t.Fatalf("redaction marker missing: %q", combined)
	}
}

func TestQualitySchedulerCursorSurvivesNoOpsAndReachesOlderStaleSession(t *testing.T) {
	keyStore, err := apikeys.NewStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer keyStore.Close()
	projectID := "project-fairness"
	now := time.Now().UTC()
	sessions := []storage.CrawlSession{
		{ID: "newest", ProjectID: &projectID, Status: "completed", StartedAt: now, PagesCrawled: 1},
		{ID: "middle", ProjectID: &projectID, Status: "completed", StartedAt: now.Add(-time.Minute), PagesCrawled: 1},
		{ID: "oldest", ProjectID: &projectID, Status: "completed", StartedAt: now.Add(-2 * time.Minute), PagesCrawled: 1},
	}
	evidences := map[string]*storage.PageRankEvidence{}
	for _, sess := range sessions {
		evidences[sess.ID] = &storage.PageRankEvidence{SessionID: sess.ID, AttemptID: sess.ID + "-evidence", State: storage.PageRankEvidenceFinalized, PredicateVersion: storage.PageRankEligiblePredicateVersion}
	}
	currents := map[string]*storage.CrawlQualityResult{}
	publishes := []string{}
	gate := qualityGateMock{metrics: &storage.CrawlQualityMetrics{HTMLPages: 1}, currents: currents, evidences: evidences, publishes: &publishes}
	store := qualityServerStore{mockStore: &mockStore{sessions: sessions}, qualityGateMock: gate}
	epoch := func() time.Time { return time.Unix(0, 0) }
	(&Server{store: store, keyStore: keyStore, qualitySchedulerNow: epoch}).evaluateMissingQuality(context.Background(), 1)
	if len(publishes) != 1 || publishes[0] != "newest" {
		t.Fatalf("first bounded pass = %#v", publishes)
	}
	// A restarted scheduler begins at cursor zero. The already-current newest
	// session must not consume the action limit, so the older stale session runs.
	(&Server{store: store, keyStore: keyStore, qualitySchedulerNow: epoch}).evaluateMissingQuality(context.Background(), 1)
	if len(publishes) != 2 || publishes[1] != "middle" {
		t.Fatalf("restart fairness pass = %#v", publishes)
	}
	(&Server{store: store, keyStore: keyStore, qualitySchedulerNow: epoch}).evaluateMissingQuality(context.Background(), 1)
	if len(publishes) != 3 || publishes[2] != "oldest" {
		t.Fatalf("oldest session starved across replay: %#v", publishes)
	}
}

func TestQualitySchedulerRestartOffsetAdvancesBoundedScan(t *testing.T) {
	keyStore, err := apikeys.NewStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer keyStore.Close()
	projectID := "project-restart-fairness"
	now := time.Now().UTC()
	sessions := make([]storage.CrawlSession, 25)
	evidences := make(map[string]*storage.PageRankEvidence, len(sessions))
	for i := range sessions {
		id := fmt.Sprintf("session-%02d", i)
		sessions[i] = storage.CrawlSession{ID: id, ProjectID: &projectID, Status: "completed", StartedAt: now.Add(-time.Duration(i) * time.Minute), PagesCrawled: 1}
		evidences[id] = &storage.PageRankEvidence{SessionID: id, AttemptID: id + "-evidence", State: storage.PageRankEvidenceFinalized, PredicateVersion: storage.PageRankEligiblePredicateVersion}
	}
	currents := map[string]*storage.CrawlQualityResult{}
	publishes := []string{}
	gate := qualityGateMock{metrics: &storage.CrawlQualityMetrics{HTMLPages: 1}, currents: currents, evidences: evidences, publishes: &publishes}
	store := qualityServerStore{mockStore: &mockStore{sessions: sessions}, qualityGateMock: gate}

	// Seed the newest bounded window as already current.
	(&Server{store: store, keyStore: keyStore, qualitySchedulerNow: func() time.Time { return time.Unix(0, 0) }}).evaluateMissingQuality(context.Background(), 20)
	if len(publishes) != 20 {
		t.Fatalf("initial bounded publications = %d, want 20", len(publishes))
	}
	// A fresh process in the next minute starts at offset one, so the first
	// stale session outside the previous 20-item window is reached.
	(&Server{store: store, keyStore: keyStore, qualitySchedulerNow: func() time.Time { return time.Unix(60, 0) }}).evaluateMissingQuality(context.Background(), 1)
	if len(publishes) != 21 || publishes[20] != "session-20" {
		t.Fatalf("restart-safe bounded publications = %#v", publishes)
	}
}

func TestCurrentSnapshotReadRejectsStalePersistedBinding(t *testing.T) {
	projectID := "project-gerus"
	currentID := "25100000-0000-4000-8000-000000000003"
	baselineID := "25100000-0000-4000-8000-000000000001"
	snap := &storage.ProjectCurrentSnapshot{
		ProjectID: projectID, CurrentSessionID: currentID, BaselineSessionID: baselineID,
		QualityEvaluationRevision: "quality", PageRankEvidenceRevision: "evidence", QualityPromotionStatus: "applied",
	}
	store := qualitySnapshotServerStore{
		mockStore: &mockStore{getSessionByID: map[string]*storage.CrawlSession{
			currentID: {ID: currentID, ProjectID: &projectID}, baselineID: {ID: baselineID, ProjectID: &projectID},
		}},
		qualityGateMock:         qualityGateMock{},
		currentSnapshotGateMock: currentSnapshotGateMock{snapshot: snap, validateErr: storage.ErrCurrentSnapshotBindingConflict},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/projects/id/current-snapshot", nil)
	req.SetPathValue("id", projectID)
	recorder := httptest.NewRecorder()
	(&Server{store: store}).handleProjectCurrentSnapshot(recorder, req)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "current_snapshot_binding_stale") ||
		!strings.Contains(recorder.Body.String(), "current_snapshot") {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestCurrentSnapshotReadRejectsDeployedEvaluatorChange(t *testing.T) {
	projectID := "project-gerus"
	currentID := "25100000-0000-4000-8000-000000000003"
	baselineID := "25100000-0000-4000-8000-000000000001"
	evidence := &storage.PageRankEvidence{SessionID: baselineID, AttemptID: "evidence", State: storage.PageRankEvidenceFinalized, PredicateVersion: storage.PageRankEligiblePredicateVersion}
	quality := &storage.CrawlQualityResult{
		SessionID: baselineID, ProjectID: projectID, EvaluationRevision: "quality", EvaluatorRevision: "quality-evaluator-old",
		RulesRevision: "rules", PageRankEvidenceRevision: evidence.AttemptID, PageRankPredicateVersion: storage.PageRankEligiblePredicateVersion,
		Trusted: true, Status: "trusted",
	}
	snap := &storage.ProjectCurrentSnapshot{
		ProjectID: projectID, CurrentSessionID: currentID, BaselineSessionID: baselineID,
		QualityEvaluationRevision: quality.EvaluationRevision, PageRankEvidenceRevision: evidence.AttemptID, QualityPromotionStatus: "applied",
	}
	store := qualitySnapshotServerStore{
		mockStore: &mockStore{getSessionByID: map[string]*storage.CrawlSession{
			currentID: {ID: currentID, ProjectID: &projectID}, baselineID: {ID: baselineID, ProjectID: &projectID},
		}},
		qualityGateMock:         qualityGateMock{current: quality, evidence: evidence},
		currentSnapshotGateMock: currentSnapshotGateMock{snapshot: snap, quality: quality, evidence: evidence},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/projects/id/current-snapshot", nil)
	req.SetPathValue("id", projectID)
	recorder := httptest.NewRecorder()
	(&Server{store: store}).handleProjectCurrentSnapshot(recorder, req)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "evaluator_revision_changed") {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestCurrentSnapshotReadRejectsQualityRulesChange(t *testing.T) {
	keyStore, err := apikeys.NewStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer keyStore.Close()
	project, err := keyStore.CreateProject("rules-change")
	if err != nil {
		t.Fatal(err)
	}
	projectID := project.ID
	currentID := "25100000-0000-4000-8000-000000000003"
	baselineID := "25100000-0000-4000-8000-000000000001"
	probe := &Server{keyStore: keyStore}
	oldRules, err := probe.currentQualityRulesRevision(projectID)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := keyStore.GetProjectQualitySettings(projectID)
	if err != nil {
		t.Fatal(err)
	}
	settings.PageRankTopN++
	if _, err := keyStore.SaveProjectQualitySettings(*settings); err != nil {
		t.Fatal(err)
	}
	evidence := &storage.PageRankEvidence{SessionID: baselineID, AttemptID: "evidence", State: storage.PageRankEvidenceFinalized, PredicateVersion: storage.PageRankEligiblePredicateVersion}
	quality := &storage.CrawlQualityResult{
		SessionID: baselineID, ProjectID: projectID, EvaluationRevision: "quality", EvaluatorRevision: qualityEvaluatorRevision,
		RulesRevision: oldRules, PageRankEvidenceRevision: evidence.AttemptID, PageRankPredicateVersion: storage.PageRankEligiblePredicateVersion,
		Trusted: true, Status: "trusted",
	}
	snap := &storage.ProjectCurrentSnapshot{
		ProjectID: projectID, CurrentSessionID: currentID, BaselineSessionID: baselineID,
		QualityEvaluationRevision: quality.EvaluationRevision, PageRankEvidenceRevision: evidence.AttemptID, QualityPromotionStatus: "applied",
	}
	store := qualitySnapshotServerStore{
		mockStore: &mockStore{getSessionByID: map[string]*storage.CrawlSession{
			currentID: {ID: currentID, ProjectID: &projectID}, baselineID: {ID: baselineID, ProjectID: &projectID},
		}},
		qualityGateMock:         qualityGateMock{current: quality, evidence: evidence},
		currentSnapshotGateMock: currentSnapshotGateMock{snapshot: snap, quality: quality, evidence: evidence},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/projects/id/current-snapshot", nil)
	req.SetPathValue("id", projectID)
	recorder := httptest.NewRecorder()
	(&Server{store: store, keyStore: keyStore}).handleProjectCurrentSnapshot(recorder, req)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "quality_rules_revision_changed") {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestLazyCurrentSnapshotInitializationRejectsStaleEvaluator(t *testing.T) {
	projectID := "project-gerus"
	baselineID := "25100000-0000-4000-8000-000000000001"
	evidence := &storage.PageRankEvidence{SessionID: baselineID, AttemptID: "evidence", State: storage.PageRankEvidenceFinalized, PredicateVersion: storage.PageRankEligiblePredicateVersion}
	quality := &storage.CrawlQualityResult{
		SessionID: baselineID, ProjectID: projectID, EvaluationRevision: "quality", EvaluatorRevision: "quality-evaluator-old",
		RulesRevision: "rules", PageRankEvidenceRevision: evidence.AttemptID, PageRankPredicateVersion: storage.PageRankEligiblePredicateVersion,
		Trusted: true, Status: "trusted", IsFullCrawl: true,
	}
	initCalls := 0
	store := qualitySnapshotServerStore{
		mockStore: &mockStore{},
		qualityGateMock: qualityGateMock{
			current: quality, evidence: evidence,
			trustedBaseline: &storage.CrawlSession{ID: baselineID, ProjectID: &projectID, Status: "completed"},
		},
		currentSnapshotGateMock: currentSnapshotGateMock{initCalls: &initCalls},
	}
	if _, err := (&Server{store: store}).initializeCurrentSnapshotFromTrustedBaseline(context.Background(), projectID, store); err == nil {
		t.Fatal("lazy initialization accepted stale evaluator")
	}
	if initCalls != 0 {
		t.Fatalf("snapshot initialized from stale evaluator: calls=%d", initCalls)
	}
}

func hasFinding(findings []storage.CrawlQualityFinding, findingType string) bool {
	for _, finding := range findings {
		if finding.FindingType == findingType {
			return true
		}
	}
	return false
}

type qualityGateMock struct {
	matched         int
	metrics         *storage.CrawlQualityMetrics
	current         *storage.CrawlQualityResult
	currents        map[string]*storage.CrawlQualityResult
	actions         *[]storage.CrawlQualityActionEvent
	actionErr       error
	evidence        *storage.PageRankEvidence
	evidences       map[string]*storage.PageRankEvidence
	adoptCalls      *int
	publishes       *[]string
	promotions      *[]storage.CrawlQualityPromotionEvent
	promotionErr    error
	trustedBaseline *storage.CrawlSession
}

func (m qualityGateMock) UpsertCrawlQualityResult(context.Context, storage.CrawlQualityResult) error {
	return nil
}

func (m qualityGateMock) PublishCrawlQualityEvaluation(_ context.Context, result storage.CrawlQualityResult, _ string) (bool, *storage.CrawlQualityResult, error) {
	if current := m.currentForSession(result.SessionID); current != nil && current.EvaluationRevision == result.EvaluationRevision {
		return false, current, nil
	}
	copyResult := result
	if m.currents != nil {
		m.currents[result.SessionID] = &copyResult
	}
	if m.publishes != nil {
		*m.publishes = append(*m.publishes, result.SessionID)
	}
	return true, &copyResult, nil
}

func (m qualityGateMock) EnsureLegacyQualityImported(_ context.Context, sessionID string) (*storage.CrawlQualityResult, error) {
	return m.currentForSession(sessionID), nil
}

func (m qualityGateMock) GetCrawlQualityResult(_ context.Context, sessionID string) (*storage.CrawlQualityResult, error) {
	current := m.currentForSession(sessionID)
	if current == nil {
		return nil, sql.ErrNoRows
	}
	return current, nil
}

func (m qualityGateMock) currentForSession(sessionID string) *storage.CrawlQualityResult {
	if m.currents != nil {
		return m.currents[sessionID]
	}
	return m.current
}

func (m qualityGateMock) ListCrawlQualityHistory(context.Context, string) ([]storage.CrawlQualityResult, error) {
	return nil, nil
}

func (m qualityGateMock) CrawlQualityResultsForSessions(context.Context, []string) (map[string]storage.CrawlQualityResult, error) {
	return nil, nil
}

func (m qualityGateMock) LatestTrustedFullCrawlSession(context.Context, string, string) (*storage.CrawlSession, error) {
	if m.trustedBaseline != nil {
		return m.trustedBaseline, nil
	}
	return nil, sql.ErrNoRows
}

type qualityServerStore struct {
	*mockStore
	qualityGateMock
}

type currentSnapshotGateMock struct {
	snapshot    *storage.ProjectCurrentSnapshot
	quality     *storage.CrawlQualityResult
	evidence    *storage.PageRankEvidence
	validateErr error
	initBinding *storage.CrawlQualityPromotionEvent
	initCalls   *int
	initErr     error
}

func (m currentSnapshotGateMock) GetProjectCurrentSnapshot(context.Context, string) (*storage.ProjectCurrentSnapshot, error) {
	if m.snapshot == nil {
		return nil, sql.ErrNoRows
	}
	return m.snapshot, nil
}

func (m currentSnapshotGateMock) ValidateProjectCurrentSnapshotBinding(context.Context, storage.ProjectCurrentSnapshot) (*storage.CrawlQualityResult, *storage.PageRankEvidence, error) {
	return m.quality, m.evidence, m.validateErr
}

func (m currentSnapshotGateMock) InitializeProjectCurrentSnapshot(_ context.Context, _ string, _ string, binding storage.CrawlQualityPromotionEvent) (*storage.ProjectCurrentSnapshot, error) {
	if m.initCalls != nil {
		*m.initCalls++
	}
	if m.initBinding != nil {
		*m.initBinding = binding
	}
	return m.snapshot, m.initErr
}

func (m currentSnapshotGateMock) PromoteDeltaToCurrentSnapshot(context.Context, string, string, string, int, int, storage.PageRankOptions, storage.CrawlQualityPromotionEvent) (*storage.ProjectCurrentSnapshot, error) {
	return m.snapshot, nil
}

type qualitySnapshotServerStore struct {
	*mockStore
	qualityGateMock
	currentSnapshotGateMock
}

func (m qualityGateMock) CrawlQualityMetrics(context.Context, string, int) (*storage.CrawlQualityMetrics, error) {
	return m.metrics, nil
}

func (m qualityGateMock) TopPageRankURLs(context.Context, string, int) ([]string, error) {
	return nil, nil
}

func (m qualityGateMock) CanaryPageCheck(context.Context, string, string) (*storage.CanaryPageCheck, error) {
	return nil, nil
}

func (m qualityGateMock) CountMatchedPagesForURLs(context.Context, string, []string) (int, error) {
	return m.matched, nil
}

func (m qualityGateMock) LatestPageRankEvidence(_ context.Context, sessionID string) (*storage.PageRankEvidence, error) {
	if m.evidences != nil {
		if evidence := m.evidences[sessionID]; evidence != nil {
			return evidence, nil
		}
	}
	if m.evidence != nil {
		return m.evidence, nil
	}
	return nil, storage.ErrNoFinalizedPageRankEvidence
}

func (m qualityGateMock) LatestFinalizedPageRankEvidence(context.Context, string) (*storage.PageRankEvidence, error) {
	return nil, storage.ErrNoFinalizedPageRankEvidence
}

func (m qualityGateMock) AdoptObservedPageRankEvidence(context.Context, string, storage.PageRankOptions) (*storage.PageRankEvidence, error) {
	if m.adoptCalls != nil {
		*m.adoptCalls++
	}
	return nil, storage.ErrNoFinalizedPageRankEvidence
}

func (m qualityGateMock) RecordQualityPromotionEvent(_ context.Context, event storage.CrawlQualityPromotionEvent) (bool, *storage.CrawlQualityPromotionEvent, error) {
	if m.promotionErr != nil {
		return false, nil, m.promotionErr
	}
	if m.promotions != nil {
		*m.promotions = append(*m.promotions, event)
	}
	return true, &event, nil
}

func (m qualityGateMock) LatestQualityPromotionEvent(context.Context, string, string) (*storage.CrawlQualityPromotionEvent, error) {
	return nil, nil
}

func (m qualityGateMock) RecordQualityActionEvent(_ context.Context, event storage.CrawlQualityActionEvent) (*storage.CrawlQualityActionEvent, error) {
	if m.actionErr != nil {
		return nil, m.actionErr
	}
	if m.actions != nil {
		for _, existing := range *m.actions {
			if existing.ActionID == event.ActionID && existing.EventSequence >= event.EventSequence {
				event.EventSequence = existing.EventSequence + 1
			}
		}
		if event.EventSequence == 0 {
			event.EventSequence = 1
		}
		*m.actions = append(*m.actions, event)
	}
	return &event, nil
}
