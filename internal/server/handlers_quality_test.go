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

func TestDeltaQualityBindsImmutableSnapshotLineageAndRejectsStalePlan(t *testing.T) {
	keyStore, err := apikeys.NewStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer keyStore.Close()

	projectID := "project-delta-lineage"
	fullID := "25100000-0000-4000-8000-000000000001"
	deltaID := "25100000-0000-4000-8000-000000000002"
	materializedID := "25100000-0000-4000-8000-000000000003"
	watermarkID := "25100000-0000-4000-8000-000000000004"
	fullEval := "25100000-0000-4000-8000-000000000011"
	watermarkEval := "25100000-0000-4000-8000-000000000012"
	fullEvidenceID := "25100000-0000-4000-8000-000000000021"
	deltaEvidenceID := "25100000-0000-4000-8000-000000000022"

	predecessor := &storage.ProjectCurrentSnapshot{
		ProjectID: projectID, SnapshotRevision: 7, CurrentSessionID: materializedID,
		SourceSessionID: fullID, ContentWatermarkSessionID: watermarkID,
		QualityEvaluationRevision: watermarkEval, BaselineQualityEvaluationRevision: fullEval,
		QualityBaselineSessionID: fullID, PageRankEvidenceRevision: deltaEvidenceID,
		QualityPromotionStatus: "applied",
	}
	snapshot := *predecessor
	currentQuality := &storage.CrawlQualityResult{SessionID: watermarkID, ProjectID: projectID, EvaluationRevision: watermarkEval, Trusted: true}
	fullQuality := &storage.CrawlQualityResult{
		SessionID: fullID, ProjectID: projectID, EvaluationRevision: fullEval,
		EvaluatorRevision: qualityEvaluatorRevision, PageRankEvidenceRevision: fullEvidenceID,
		PageRankPredicateVersion: storage.PageRankEligiblePredicateVersion,
		Trusted:                  true, IsFullCrawl: true, Status: "trusted",
	}
	fullEvidence := &storage.PageRankEvidence{
		SessionID: fullID, AttemptID: fullEvidenceID, State: storage.PageRankEvidenceFinalized,
		PredicateVersion: storage.PageRankEligiblePredicateVersion,
	}
	deltaEvidence := &storage.PageRankEvidence{
		SessionID: deltaID, AttemptID: deltaEvidenceID, State: storage.PageRankEvidenceFinalized,
		PredicateVersion: storage.PageRankEligiblePredicateVersion,
	}
	currents := map[string]*storage.CrawlQualityResult{fullID: fullQuality}
	promotions := []storage.CrawlQualityPromotionEvent{}
	publishes := []string{}
	supersededSessions := map[string]bool{}
	deltaCalls := 0
	var deltaBinding storage.CrawlQualityPromotionEvent
	store := qualitySnapshotServerStore{
		mockStore: &mockStore{getSessionByID: map[string]*storage.CrawlSession{
			fullID: {ID: fullID, ProjectID: &projectID, Status: "completed", Label: "full crawl"},
		}},
		qualityGateMock: qualityGateMock{
			metrics: &storage.CrawlQualityMetrics{HTMLPages: 10}, currents: currents, promotions: &promotions,
			evidences: map[string]*storage.PageRankEvidence{fullID: fullEvidence, deltaID: deltaEvidence}, publishes: &publishes,
		},
		currentSnapshotGateMock: currentSnapshotGateMock{
			snapshot: &snapshot, snapshotRevisions: map[uint64]*storage.ProjectCurrentSnapshot{7: predecessor},
			quality: currentQuality, evidence: deltaEvidence,
			historicalQuality: currentQuality, historicalEvidence: deltaEvidence, supersededSessions: supersededSessions,
			deltaCalls: &deltaCalls, deltaBinding: &deltaBinding,
		},
	}
	srv := &Server{store: store, keyStore: keyStore}
	fullQuality.RulesRevision, err = srv.currentQualityRulesRevision(projectID)
	if err != nil {
		t.Fatal(err)
	}
	planJSON := func(plan config.DeltaPlanConfig) string {
		return fmt.Sprintf(`{"Crawler":{"DeltaPlan":{"baseline_session_id":%q,"baseline_source_session_id":%q,"baseline_evaluation_revision":%q,"baseline_source_evaluation_revision":%q,"baseline_snapshot_revision":%d,"baseline_content_watermark_session_id":%q}}}`,
			plan.BaselineSessionID, plan.BaselineSourceSessionID, plan.BaselineEvaluationRevision,
			plan.BaselineSourceEvaluationRevision, plan.BaselineSnapshotRevision, plan.BaselineContentWatermarkSessionID)
	}
	plan := config.DeltaPlanConfig{
		BaselineSessionID: materializedID, BaselineSourceSessionID: fullID,
		BaselineEvaluationRevision: watermarkEval, BaselineSourceEvaluationRevision: fullEval,
		BaselineSnapshotRevision: 7, BaselineContentWatermarkSessionID: watermarkID,
	}
	sess := storage.CrawlSession{
		ID: deltaID, ProjectID: &projectID, Status: "completed", Label: "Daily Delta Crawl",
		PagesCrawled: 10, Config: planJSON(plan),
	}

	first, changed, promotionChanged, promotion, err := srv.evaluateAndPublishSessionQuality(
		context.Background(), sess, deltaEvidence, "test", "", "delta lifecycle test",
	)
	if err != nil {
		t.Fatalf("evaluate and promote current delta plan: %v", err)
	}
	if !changed || !promotionChanged || promotion == nil || promotion.Status != "applied" || deltaCalls != 1 ||
		!first.Trusted || first.BaselineSessionID != fullID || first.BaselineEvaluationRevision != fullEval ||
		deltaBinding.BaselineSessionID != fullID || deltaBinding.BaselineEvaluationRevision != fullEval ||
		hasFinding(first.Findings, "stale_delta_baseline") {
		t.Fatalf("current delta lineage was not bound to raw full source: %#v", first)
	}
	// A restarted scheduler resolves the immutable predecessor from journal
	// history. The applied Delta remains trusted and its content is not overlaid.
	restarted := &Server{store: store, keyStore: keyStore}
	second, changed, promotionChanged, promotion, err := restarted.evaluateAndPublishSessionQuality(
		context.Background(), sess, deltaEvidence, "scheduler", first.EvaluationRevision, "scheduler reconciliation",
	)
	if err != nil || changed || promotionChanged || promotion == nil || promotion.Status != "applied" ||
		second.EvaluationRevision != first.EvaluationRevision || deltaCalls != 1 {
		t.Fatalf("applied delta replay was not idempotent from durable predecessor: first=%#v second=%#v promotion=%#v calls=%d err=%v", first, second, promotion, deltaCalls, err)
	}
	// A fold replaces the materialized baseline and clears applied-delta markers,
	// but the canonical source/watermark and immutable predecessor journal remain.
	snapshot.SnapshotRevision = 9
	snapshot.BaselineSessionID = "25100000-0000-4000-8000-000000000006"
	snapshot.DeltaCount = 0
	foldedRestart := &Server{store: store, keyStore: keyStore}
	folded, changed, promotionChanged, promotion, err := foldedRestart.evaluateAndPublishSessionQuality(
		context.Background(), sess, deltaEvidence, "scheduler", second.EvaluationRevision, "scheduler reconciliation",
	)
	if err != nil || changed || promotionChanged || promotion == nil || promotion.Status != "applied" ||
		!folded.Trusted || folded.EvaluationRevision != first.EvaluationRevision || deltaCalls != 1 {
		t.Fatalf("fold cleanup broke applied delta replay: result=%#v promotion=%#v calls=%d err=%v", folded, promotion, deltaCalls, err)
	}
	if snapshot.SnapshotRevision != 9 || snapshot.ContentWatermarkSessionID != deltaID ||
		snapshot.QualityEvaluationRevision != first.EvaluationRevision {
		t.Fatalf("fold replay mutated Current Snapshot binding: %#v", snapshot)
	}
	if _, err := store.GetSession(context.Background(), watermarkID); err == nil {
		t.Fatal("folded D1 raw crawl session remained scheduler-visible")
	}
	for _, publishedSessionID := range publishes {
		if publishedSessionID == watermarkID {
			t.Fatalf("scheduler replay re-evaluated folded D1 raw session: publishes=%#v", publishes)
		}
	}
	// Re-evaluating the source or predecessor can move their current pointers,
	// but cannot invalidate the immutable facts captured by journal revision 7.
	currents[fullID] = &storage.CrawlQualityResult{SessionID: fullID, EvaluationRevision: "25100000-0000-4000-8000-000000000099"}
	store.currentSnapshotGateMock.quality = &storage.CrawlQualityResult{SessionID: watermarkID, EvaluationRevision: "25100000-0000-4000-8000-000000000098"}
	pointerAdvanced, err := restarted.evaluateSessionQualityResult(context.Background(), sess, deltaEvidence, "test")
	if err != nil || !pointerAdvanced.Trusted || pointerAdvanced.EvaluationRevision != first.EvaluationRevision ||
		hasFinding(pointerAdvanced.Findings, "stale_delta_baseline") {
		t.Fatalf("current-pointer changes invalidated immutable delta lineage: result=%#v err=%v", pointerAdvanced, err)
	}

	// F2 or a later Delta can supersede snapshot promotion without corrupting
	// this crawl's immutable quality evaluation.
	snapshot.SnapshotRevision = 10
	snapshot.ContentWatermarkSessionID = "25100000-0000-4000-8000-000000000005"
	historical, err := srv.evaluateSessionQualityResult(context.Background(), sess, deltaEvidence, "test")
	if err != nil {
		t.Fatalf("evaluate historical delta plan: %v", err)
	}
	if !historical.Trusted || historical.EvaluationRevision != first.EvaluationRevision {
		t.Fatalf("newer snapshot corrupted historical delta quality: %#v", historical)
	}
	supersededSessions[deltaID] = true
	supersededSrv := &Server{store: store, keyStore: keyStore}
	historical, changed, promotionChanged, promotion, err = supersededSrv.evaluateAndPublishSessionQuality(
		context.Background(), sess, deltaEvidence, "scheduler", second.EvaluationRevision, "scheduler reconciliation",
	)
	if err != nil || changed || !promotionChanged || promotion == nil || promotion.Status != "superseded" || len(promotions) != 3 {
		t.Fatalf("historical delta promotion was not typed superseded: changed=%t promotion=%#v events=%#v err=%v", changed, promotion, promotions, err)
	}

	legacyPlan := plan
	legacyPlan.BaselineSnapshotRevision = 0
	sess.Config = planJSON(legacyPlan)
	legacy, err := srv.evaluateSessionQualityResult(context.Background(), sess, deltaEvidence, "test")
	if err != nil || legacy.Trusted || !hasFinding(legacy.Findings, "stale_delta_baseline") {
		t.Fatalf("legacy delta plan did not fail closed: result=%#v err=%v", legacy, err)
	}
	missingHistoryPlan := plan
	missingHistoryPlan.BaselineSnapshotRevision = 6
	sess.Config = planJSON(missingHistoryPlan)
	missingHistory, err := srv.evaluateSessionQualityResult(context.Background(), sess, deltaEvidence, "test")
	if err != nil || missingHistory.Trusted || !hasFinding(missingHistory.Findings, "stale_delta_baseline") {
		t.Fatalf("missing predecessor journal did not fail closed: result=%#v err=%v", missingHistory, err)
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

func TestLegacyCurrentSnapshotAllowsAuditedFullRecoveryButBlocksDelta(t *testing.T) {
	projectID := "project-legacy-current-snapshot"
	fullID := "25100000-0000-4000-8000-000000000301"
	evidence := &storage.PageRankEvidence{
		SessionID: fullID, AttemptID: "evidence", State: storage.PageRankEvidenceFinalized,
		PredicateVersion: storage.PageRankEligiblePredicateVersion,
	}
	full := &storage.CrawlQualityResult{
		SessionID: fullID, ProjectID: projectID, EvaluationRevision: "evaluation", PageRankEvidenceRevision: evidence.AttemptID,
		EvaluatorRevision: qualityEvaluatorRevision, RulesRevision: "rules", Trusted: true, IsFullCrawl: true,
	}
	promotions := []storage.CrawlQualityPromotionEvent{}
	initCalls := 0
	store := qualitySnapshotServerStore{
		mockStore:       &mockStore{},
		qualityGateMock: qualityGateMock{current: full, evidence: evidence, promotions: &promotions},
		currentSnapshotGateMock: currentSnapshotGateMock{
			snapshot: &storage.ProjectCurrentSnapshot{ProjectID: projectID}, snapshotErr: storage.ErrCurrentSnapshotBindingConflict,
			promotionGuardErr: storage.ErrCurrentSnapshotBindingConflict, initCalls: &initCalls,
		},
	}
	legacyRead := httptest.NewRecorder()
	legacyRequest := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/current-snapshot", nil)
	legacyRequest.SetPathValue("id", projectID)
	(&Server{store: store}).handleProjectCurrentSnapshot(legacyRead, legacyRequest)
	if legacyRead.Code != http.StatusConflict {
		t.Fatalf("unprovable legacy snapshot GET = %d %s, want 409", legacyRead.Code, legacyRead.Body.String())
	}
	sess := storage.CrawlSession{ID: fullID, ProjectID: &projectID, Status: "completed"}
	changed, terminal, err := (&Server{store: store}).reconcileCurrentSnapshotPromotion(context.Background(), store, sess, full, evidence, "admin legacy recovery")
	if err != nil || !changed || terminal == nil || terminal.Status != "applied" || initCalls != 1 ||
		len(promotions) != 2 || promotions[0].Status != "started" || promotions[1].Status != "applied" {
		t.Fatalf("trusted full did not recover legacy snapshot: changed=%t terminal=%#v calls=%d events=%#v err=%v", changed, terminal, initCalls, promotions, err)
	}
	if store.snapshot.SourceSessionID != fullID || store.snapshot.ContentWatermarkSessionID != fullID ||
		store.snapshot.QualityEvaluationRevision != full.EvaluationRevision ||
		store.snapshot.PageRankEvidenceRevision != evidence.AttemptID ||
		store.snapshot.QualityPromotionStatus != "applied" {
		t.Fatalf("legacy recovery did not publish complete v2 provenance: %#v", store.snapshot)
	}

	deltaID := "25100000-0000-4000-8000-000000000302"
	deltaEvidence := &storage.PageRankEvidence{
		SessionID: deltaID, AttemptID: "delta-evidence", State: storage.PageRankEvidenceFinalized,
		PredicateVersion: storage.PageRankEligiblePredicateVersion,
	}
	delta := &storage.CrawlQualityResult{
		SessionID: deltaID, ProjectID: projectID, EvaluationRevision: "delta-evaluation", PageRankEvidenceRevision: deltaEvidence.AttemptID,
		EvaluatorRevision: qualityEvaluatorRevision, RulesRevision: "rules", Trusted: true,
		BaselineSessionID: fullID, BaselineEvaluationRevision: full.EvaluationRevision,
	}
	deltaEvents := []storage.CrawlQualityPromotionEvent{}
	deltaCalls := 0
	deltaStore := qualitySnapshotServerStore{
		mockStore:       &mockStore{},
		qualityGateMock: qualityGateMock{current: delta, evidence: deltaEvidence, promotions: &deltaEvents},
		currentSnapshotGateMock: currentSnapshotGateMock{
			snapshot:          &storage.ProjectCurrentSnapshot{ProjectID: projectID},
			promotionGuardErr: storage.ErrCurrentSnapshotBindingConflict, deltaCalls: &deltaCalls,
		},
	}
	deltaSession := storage.CrawlSession{ID: deltaID, ProjectID: &projectID, Status: "completed", Label: "Daily Delta Crawl"}
	changed, terminal, err = (&Server{store: deltaStore}).reconcileCurrentSnapshotPromotion(context.Background(), deltaStore, deltaSession, delta, deltaEvidence, "delta recovery denied")
	if err != nil || !changed || terminal == nil || terminal.Status != "conflict" || deltaCalls != 0 || len(deltaEvents) != 1 {
		t.Fatalf("Delta recovered unprovable legacy snapshot: changed=%t terminal=%#v calls=%d events=%#v err=%v", changed, terminal, deltaCalls, deltaEvents, err)
	}
}

func TestFullCrawlPromotionRetryCompletesPublishedPointerAuditAttempt(t *testing.T) {
	projectID := "project-published-pointer-retry"
	sessionID := "25100000-0000-4000-8000-000000000303"
	promotionID := "25100000-0000-4000-8000-000000000304"
	evidence := &storage.PageRankEvidence{
		SessionID: sessionID, AttemptID: "evidence", State: storage.PageRankEvidenceFinalized,
		PredicateVersion: storage.PageRankEligiblePredicateVersion,
	}
	result := &storage.CrawlQualityResult{
		SessionID: sessionID, ProjectID: projectID, EvaluationRevision: "evaluation", PageRankEvidenceRevision: evidence.AttemptID,
		EvaluatorRevision: qualityEvaluatorRevision, RulesRevision: "rules", Trusted: true, IsFullCrawl: true,
	}
	promotions := []storage.CrawlQualityPromotionEvent{{
		ProjectID: projectID, SessionID: sessionID, PromotionID: promotionID,
		EvaluationRevision: result.EvaluationRevision, PageRankEvidenceRevision: evidence.AttemptID,
		BaselineSessionID: sessionID, BaselineEvaluationRevision: result.EvaluationRevision,
		EvaluatorRevision: result.EvaluatorRevision, RulesRevision: result.RulesRevision,
		Status: "started",
	}}
	initCalls := 0
	snapshot := &storage.ProjectCurrentSnapshot{
		ProjectID: projectID, SourceSessionID: sessionID, ContentWatermarkSessionID: sessionID,
		QualityBaselineSessionID: sessionID, QualityEvaluationRevision: result.EvaluationRevision,
		BaselineQualityEvaluationRevision: result.EvaluationRevision, PageRankEvidenceRevision: evidence.AttemptID,
		QualityEvaluatorRevision: result.EvaluatorRevision, QualityRulesRevision: result.RulesRevision,
		QualityPromotionStatus: "applied",
	}
	store := qualitySnapshotServerStore{
		mockStore:       &mockStore{},
		qualityGateMock: qualityGateMock{current: result, evidence: evidence, promotions: &promotions},
		currentSnapshotGateMock: currentSnapshotGateMock{
			snapshot: snapshot, initCalls: &initCalls,
		},
	}
	sess := storage.CrawlSession{ID: sessionID, ProjectID: &projectID, Status: "completed"}
	changed, terminal, err := (&Server{store: store}).reconcileCurrentSnapshotPromotion(context.Background(), store, sess, result, evidence, "retry published pointer")
	if err != nil || !changed || terminal == nil || terminal.Status != "applied" || initCalls != 1 {
		t.Fatalf("published-pointer retry failed: changed=%t terminal=%#v calls=%d err=%v", changed, terminal, initCalls, err)
	}
	if len(promotions) != 2 || promotions[0].Status != "started" || promotions[1].Status != "applied" {
		t.Fatalf("retry did not complete one started attempt: %#v", promotions)
	}
	if promotions[0].PromotionID != promotionID || promotions[1].PromotionID != promotionID {
		t.Fatalf("retry created a second promotion attempt: %#v", promotions)
	}
	if promotions[1].EvaluationRevision != result.EvaluationRevision {
		t.Fatalf("retry changed evaluation revision: got %q want %q", promotions[1].EvaluationRevision, result.EvaluationRevision)
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

func TestHistoricalFullCrawlPromotionIsSupersededBeforeMutation(t *testing.T) {
	projectID := "project-gerus"
	historicalID := "25100000-0000-4000-8000-000000000001"
	newestID := "25100000-0000-4000-8000-000000000003"
	result := &storage.CrawlQualityResult{
		SessionID: historicalID, ProjectID: projectID, EvaluationRevision: "evaluation", PageRankEvidenceRevision: "evidence",
		EvaluatorRevision: qualityEvaluatorRevision, RulesRevision: "rules", Trusted: true, IsFullCrawl: true,
	}
	evidence := &storage.PageRankEvidence{
		SessionID: historicalID, AttemptID: "evidence", State: storage.PageRankEvidenceFinalized,
		PredicateVersion: storage.PageRankEligiblePredicateVersion,
	}
	promotions := []storage.CrawlQualityPromotionEvent{}
	initCalls := 0
	guardCalls := []string{}
	store := qualitySnapshotServerStore{
		mockStore:       &mockStore{},
		qualityGateMock: qualityGateMock{current: result, evidence: evidence, promotions: &promotions},
		currentSnapshotGateMock: currentSnapshotGateMock{
			snapshot:           &storage.ProjectCurrentSnapshot{ProjectID: projectID, CurrentSessionID: newestID},
			supersededSessions: map[string]bool{historicalID: true}, promotionGuardCalls: &guardCalls,
			initCalls: &initCalls,
		},
	}
	sess := storage.CrawlSession{ID: historicalID, ProjectID: &projectID, Status: "completed"}
	changed, terminal, err := (&Server{store: store}).reconcileCurrentSnapshotPromotion(context.Background(), store, sess, result, evidence, "scheduler reconciliation")
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !changed || terminal == nil || terminal.Status != "superseded" {
		t.Fatalf("historical promotion = changed %t, event %#v", changed, terminal)
	}
	if initCalls != 0 || store.snapshot.CurrentSessionID != newestID {
		t.Fatalf("historical promotion mutated current snapshot: calls=%d snapshot=%#v", initCalls, store.snapshot)
	}
	if len(guardCalls) != 1 || guardCalls[0] != historicalID || len(promotions) != 1 {
		t.Fatalf("guard calls=%#v promotions=%#v", guardCalls, promotions)
	}
}

func TestConcurrentNewerSnapshotMakesPromotionSuperseded(t *testing.T) {
	projectID := "project-gerus"
	sessionID := "25100000-0000-4000-8000-000000000001"
	result := &storage.CrawlQualityResult{
		SessionID: sessionID, ProjectID: projectID, EvaluationRevision: "evaluation", PageRankEvidenceRevision: "evidence",
		EvaluatorRevision: qualityEvaluatorRevision, RulesRevision: "rules", Trusted: true, IsFullCrawl: true,
	}
	evidence := &storage.PageRankEvidence{
		SessionID: sessionID, AttemptID: "evidence", State: storage.PageRankEvidenceFinalized,
		PredicateVersion: storage.PageRankEligiblePredicateVersion,
	}
	promotions := []storage.CrawlQualityPromotionEvent{}
	store := qualitySnapshotServerStore{
		mockStore:       &mockStore{},
		qualityGateMock: qualityGateMock{current: result, evidence: evidence, promotions: &promotions},
		currentSnapshotGateMock: currentSnapshotGateMock{
			snapshot: &storage.ProjectCurrentSnapshot{}, initErr: storage.ErrCurrentSnapshotSourceSuperseded,
		},
	}
	sess := storage.CrawlSession{ID: sessionID, ProjectID: &projectID, Status: "completed"}
	_, terminal, err := (&Server{store: store}).reconcileCurrentSnapshotPromotion(context.Background(), store, sess, result, evidence, "scheduler reconciliation")
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if terminal == nil || terminal.Status != "superseded" || len(promotions) != 2 || promotions[0].Status != "started" || promotions[1].Status != "superseded" {
		t.Fatalf("concurrent supersession audit = terminal %#v, events %#v", terminal, promotions)
	}
	for _, event := range promotions {
		if event.Status == "applied" || event.Status == "failed" {
			t.Fatalf("concurrent supersession recorded invalid terminal state: %#v", promotions)
		}
	}
}

func TestQualitySchedulerHistoricalReplayConvergesAtNewestSnapshot(t *testing.T) {
	keyStore, err := apikeys.NewStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer keyStore.Close()

	projectID := "project-fixed-point"
	startedAt := time.Now().UTC().Add(-time.Hour)
	sessionA := storage.CrawlSession{ID: "25100000-0000-4000-8000-000000000001", ProjectID: &projectID, Status: "completed", StartedAt: startedAt, PagesCrawled: 1}
	sessionB := storage.CrawlSession{ID: "25100000-0000-4000-8000-000000000002", ProjectID: &projectID, Status: "completed", StartedAt: startedAt.Add(time.Minute), PagesCrawled: 1}
	sessionC := storage.CrawlSession{ID: "25100000-0000-4000-8000-000000000003", ProjectID: &projectID, Status: "completed", StartedAt: startedAt.Add(2 * time.Minute), PagesCrawled: 1}
	sessions := []storage.CrawlSession{sessionB, sessionA, sessionC}
	evidences := map[string]*storage.PageRankEvidence{}
	for _, sess := range sessions {
		evidences[sess.ID] = &storage.PageRankEvidence{
			SessionID: sess.ID, AttemptID: sess.ID,
			State: storage.PageRankEvidenceFinalized, PredicateVersion: storage.PageRankEligiblePredicateVersion,
			EligiblePageCount: 1, PositivePageCount: 1,
		}
	}
	currents := map[string]*storage.CrawlQualityResult{}
	publishes := []string{}
	promotions := []storage.CrawlQualityPromotionEvent{}
	initCalls := 0
	snapshot := &storage.ProjectCurrentSnapshot{ProjectID: projectID}
	store := qualitySnapshotServerStore{
		mockStore: &mockStore{sessions: sessions},
		qualityGateMock: qualityGateMock{
			metrics: &storage.CrawlQualityMetrics{HTMLPages: 1}, currents: currents, evidences: evidences,
			publishes: &publishes, promotions: &promotions,
			trustedBaselines: map[string]*storage.CrawlSession{sessionB.ID: &sessionA, sessionC.ID: &sessionB},
		},
		currentSnapshotGateMock: currentSnapshotGateMock{
			snapshot: snapshot, supersededSessions: map[string]bool{sessionA.ID: true, sessionB.ID: true}, initCalls: &initCalls,
		},
	}

	for minute := int64(0); minute < 2; minute++ {
		srv := &Server{store: store, keyStore: keyStore, qualitySchedulerNow: func() time.Time { return time.Unix(minute*60, 0) }}
		srv.evaluateMissingQuality(context.Background(), 20)
	}
	publicationsAtFixedPoint := len(publishes)
	promotionsAtFixedPoint := len(promotions)
	snapshotWritesAtFixedPoint := initCalls
	srvAfterRestart := &Server{store: store, keyStore: keyStore, qualitySchedulerNow: func() time.Time { return time.Unix(120, 0) }}
	srvAfterRestart.evaluateMissingQuality(context.Background(), 20)

	if len(publishes) != publicationsAtFixedPoint || len(promotions) != promotionsAtFixedPoint || initCalls != snapshotWritesAtFixedPoint {
		t.Fatalf("scheduler did not reach fixed point: publications %d->%d promotions %d->%d snapshot writes %d->%d",
			publicationsAtFixedPoint, len(publishes), promotionsAtFixedPoint, len(promotions), snapshotWritesAtFixedPoint, initCalls)
	}
	if snapshot.CurrentSessionID != sessionC.ID {
		t.Fatalf("historical replay moved current snapshot to %q, want newest %q", snapshot.CurrentSessionID, sessionC.ID)
	}
	if currents[sessionA.ID].BaselineSessionID != "" || currents[sessionB.ID].BaselineSessionID != sessionA.ID || currents[sessionC.ID].BaselineSessionID != sessionB.ID {
		t.Fatalf("strict predecessor lineage: A=%#v B=%#v C=%#v", currents[sessionA.ID], currents[sessionB.ID], currents[sessionC.ID])
	}
	if initCalls != 2 {
		t.Fatalf("newest snapshot should publish once per converging evaluation revision, calls=%d", initCalls)
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
	matched          int
	metrics          *storage.CrawlQualityMetrics
	current          *storage.CrawlQualityResult
	currents         map[string]*storage.CrawlQualityResult
	actions          *[]storage.CrawlQualityActionEvent
	actionErr        error
	evidence         *storage.PageRankEvidence
	evidences        map[string]*storage.PageRankEvidence
	adoptCalls       *int
	publishes        *[]string
	promotions       *[]storage.CrawlQualityPromotionEvent
	promotionErr     error
	trustedBaseline  *storage.CrawlSession
	trustedBaselines map[string]*storage.CrawlSession
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

func (m qualityGateMock) LatestTrustedFullCrawlSession(_ context.Context, _ string, excludeSessionID string) (*storage.CrawlSession, error) {
	if m.trustedBaselines != nil {
		if baseline := m.trustedBaselines[excludeSessionID]; baseline != nil {
			return baseline, nil
		}
		return nil, sql.ErrNoRows
	}
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
	snapshot            *storage.ProjectCurrentSnapshot
	snapshotErr         error
	snapshotRevisions   map[uint64]*storage.ProjectCurrentSnapshot
	quality             *storage.CrawlQualityResult
	evidence            *storage.PageRankEvidence
	historicalQuality   *storage.CrawlQualityResult
	historicalEvidence  *storage.PageRankEvidence
	validateErr         error
	promotionGuardErr   error
	supersededSessions  map[string]bool
	promotionGuardCalls *[]string
	initBinding         *storage.CrawlQualityPromotionEvent
	initCalls           *int
	initErr             error
	deltaCalls          *int
	deltaBinding        *storage.CrawlQualityPromotionEvent
	deltaErr            error
}

func (m currentSnapshotGateMock) GetProjectCurrentSnapshot(context.Context, string) (*storage.ProjectCurrentSnapshot, error) {
	if m.snapshotErr != nil {
		return nil, m.snapshotErr
	}
	if m.snapshot == nil {
		return nil, sql.ErrNoRows
	}
	return m.snapshot, nil
}

func (m currentSnapshotGateMock) GetProjectCurrentSnapshotRevision(_ context.Context, _ string, snapshotRevision uint64) (*storage.ProjectCurrentSnapshot, error) {
	if m.snapshotRevisions != nil {
		if snap := m.snapshotRevisions[snapshotRevision]; snap != nil {
			return snap, nil
		}
	}
	if m.snapshot != nil && m.snapshot.SnapshotRevision == snapshotRevision {
		return m.snapshot, nil
	}
	return nil, sql.ErrNoRows
}

func (m currentSnapshotGateMock) CanPromoteCurrentSnapshotSource(_ context.Context, _ string, candidateSessionID string) (bool, *storage.ProjectCurrentSnapshot, error) {
	if m.promotionGuardCalls != nil {
		*m.promotionGuardCalls = append(*m.promotionGuardCalls, candidateSessionID)
	}
	if m.promotionGuardErr != nil {
		return false, m.snapshot, m.promotionGuardErr
	}
	if m.supersededSessions != nil && m.supersededSessions[candidateSessionID] {
		return false, m.snapshot, storage.ErrCurrentSnapshotSourceSuperseded
	}
	return true, m.snapshot, nil
}

func (m currentSnapshotGateMock) ValidateProjectCurrentSnapshotBinding(context.Context, storage.ProjectCurrentSnapshot) (*storage.CrawlQualityResult, *storage.PageRankEvidence, error) {
	return m.quality, m.evidence, m.validateErr
}

func (m currentSnapshotGateMock) ValidateProjectCurrentSnapshotHistoricalBinding(context.Context, storage.ProjectCurrentSnapshot) (*storage.CrawlQualityResult, *storage.PageRankEvidence, error) {
	if m.historicalQuality != nil || m.historicalEvidence != nil {
		return m.historicalQuality, m.historicalEvidence, m.validateErr
	}
	return m.quality, m.evidence, m.validateErr
}

func (m currentSnapshotGateMock) InitializeProjectCurrentSnapshot(_ context.Context, _ string, baselineSessionID string, binding storage.CrawlQualityPromotionEvent) (*storage.ProjectCurrentSnapshot, error) {
	if m.initCalls != nil {
		*m.initCalls++
	}
	if m.initBinding != nil {
		*m.initBinding = binding
	}
	if m.initErr == nil && m.snapshot != nil {
		m.snapshot.CurrentSessionID = baselineSessionID
		m.snapshot.SourceSessionID = baselineSessionID
		m.snapshot.ContentWatermarkSessionID = baselineSessionID
		m.snapshot.QualityBaselineSessionID = binding.BaselineSessionID
		m.snapshot.QualityEvaluationRevision = binding.EvaluationRevision
		m.snapshot.BaselineQualityEvaluationRevision = binding.BaselineEvaluationRevision
		m.snapshot.PageRankEvidenceRevision = binding.PageRankEvidenceRevision
		m.snapshot.QualityEvaluatorRevision = binding.EvaluatorRevision
		m.snapshot.QualityRulesRevision = binding.RulesRevision
		m.snapshot.QualityPromotionStatus = "applied"
	}
	return m.snapshot, m.initErr
}

func (m currentSnapshotGateMock) PromoteDeltaToCurrentSnapshot(_ context.Context, _ string, deltaSessionID, _ string, _ int, _ int, _ storage.PageRankOptions, binding storage.CrawlQualityPromotionEvent) (*storage.ProjectCurrentSnapshot, error) {
	if m.deltaCalls != nil {
		*m.deltaCalls++
	}
	if m.deltaBinding != nil {
		*m.deltaBinding = binding
	}
	if m.deltaErr != nil {
		return m.snapshot, m.deltaErr
	}
	if m.snapshot != nil {
		m.snapshot.SnapshotRevision++
		m.snapshot.ContentWatermarkSessionID = deltaSessionID
		m.snapshot.QualityEvaluationRevision = binding.EvaluationRevision
		m.snapshot.BaselineQualityEvaluationRevision = binding.BaselineEvaluationRevision
		m.snapshot.QualityBaselineSessionID = binding.BaselineSessionID
		m.snapshot.PageRankEvidenceRevision = binding.PageRankEvidenceRevision
	}
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
		for i := len(*m.promotions) - 1; i >= 0; i-- {
			existing := &(*m.promotions)[i]
			if existing.ProjectID == event.ProjectID && existing.SessionID == event.SessionID {
				if existing.EvaluationRevision == event.EvaluationRevision &&
					existing.PageRankEvidenceRevision == event.PageRankEvidenceRevision &&
					existing.Status == event.Status {
					return false, existing, nil
				}
				break
			}
		}
		*m.promotions = append(*m.promotions, event)
	}
	return true, &event, nil
}

func (m qualityGateMock) LatestQualityPromotionEvent(_ context.Context, projectID, sessionID string) (*storage.CrawlQualityPromotionEvent, error) {
	if m.promotions != nil {
		for i := len(*m.promotions) - 1; i >= 0; i-- {
			event := &(*m.promotions)[i]
			if event.ProjectID == projectID && event.SessionID == sessionID {
				return event, nil
			}
		}
	}
	return nil, sql.ErrNoRows
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
