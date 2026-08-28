package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/SEObserver/crawlobserver/internal/apikeys"
	"github.com/SEObserver/crawlobserver/internal/config"
	"github.com/SEObserver/crawlobserver/internal/crawler"
	"github.com/SEObserver/crawlobserver/internal/storage"
)

func TestRefreshDeltaSitemapUsesConfiguredRootAndExcludesHistoricalURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nAllow: /\n"))
		case "/configured-sitemap.xml":
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<urlset><url><loc>` + serverURL(r) + `/fresh</loc></url><url><loc>` + serverURL(r) + `/raw path</loc></url></urlset>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	projectID := "project-di"
	saved, err := json.Marshal(config.Config{Crawler: config.CrawlerConfig{
		Timeout:         2 * time.Second,
		MaxBodySize:     1024 * 1024,
		UserAgent:       "crawlobserver-test",
		CrawlScope:      "host",
		AllowPrivateIPs: true,
		SitemapURLs:     []string{server.URL + "/configured-sitemap.xml"},
		RespectRobots:   true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	store := &mockStore{deltaSitemapURLs: []string{server.URL + "/removed"}}
	srv := &Server{cfg: &config.Config{Crawler: config.CrawlerConfig{Timeout: 2 * time.Second, MaxBodySize: 1024 * 1024, UserAgent: "crawlobserver-test", AllowPrivateIPs: true}}, store: store}
	settings := apikeys.DefaultProjectDeltaSettings(projectID)
	settings.SourceGSC = false
	settings.SourceProblemPages = false
	settings.SourceStalePages = false
	settings.SourceManualQueue = false
	settings.MaxCandidatesPerRun = 100
	result, err := srv.refreshDeltaSitemap(context.Background(), &storage.CrawlSession{
		ID:       "baseline-session",
		SeedURLs: []string{server.URL + "/"},
		Config:   string(saved),
	}, &settings, 100)
	if err != nil {
		t.Fatalf("refreshDeltaSitemap() error = %v", err)
	}
	if result.Refresh.Mode != deltaSitemapRefreshFresh {
		t.Fatalf("refresh mode = %q, want fresh", result.Refresh.Mode)
	}
	if result.Refresh.RemovedCount != 1 || result.Refresh.AddedCount != 2 {
		t.Fatalf("added/removed = %d/%d, want 2/1", result.Refresh.AddedCount, result.Refresh.RemovedCount)
	}
	if len(result.Candidates) != 2 || result.Candidates[0] != server.URL+"/fresh" {
		t.Fatalf("fresh candidates = %#v, want only current sitemap URLs", result.Candidates)
	}
	if len(result.SitemapURLRows) != 2 || result.SitemapURLRows[1].Loc != server.URL+"/raw path" {
		t.Fatalf("raw sitemap evidence = %#v, literal loc whitespace must be preserved", result.SitemapURLRows)
	}
	if result.Refresh.RawURLRowCount != 2 {
		t.Fatalf("raw sitemap row count = %d, want 2", result.Refresh.RawURLRowCount)
	}
}

func TestDeltaSitemapRefreshFailureSkipsByDefaultAndFallbackIsExplicit(t *testing.T) {
	settings := apikeys.DefaultProjectDeltaSettings("project-di")
	store := &mockStore{deltaSitemapURLs: []string{"https://example.com/old"}}
	srv := &Server{store: store}

	skipped, err := srv.deltaSitemapRefreshFailure(context.Background(), "baseline-session", store.deltaSitemapURLs, &settings, time.Now(), "timeout")
	if err != nil {
		t.Fatalf("skip failure result error = %v", err)
	}
	if skipped.Refresh.Mode != deltaSitemapRefreshSkipped || len(skipped.Candidates) != 0 {
		t.Fatalf("default policy = %#v, want skipped with no candidates", skipped)
	}

	settings.SitemapRefreshFailureMode = apikeys.SitemapRefreshFailureModeSnapshotFallback
	fallback, err := srv.deltaSitemapRefreshFailure(context.Background(), "baseline-session", store.deltaSitemapURLs, &settings, time.Now(), "timeout")
	if err != nil {
		t.Fatalf("fallback result error = %v", err)
	}
	if fallback.Refresh.Mode != deltaSitemapRefreshSnapshotFallback || !reflect.DeepEqual(fallback.Candidates, store.deltaSitemapURLs) {
		t.Fatalf("fallback result = %#v, want explicit snapshot fallback", fallback)
	}
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
}

func TestDeltaCrawlRequestPreservesBaselineSeedURLs(t *testing.T) {
	projectID := "project-di"
	baselineSeeds := []string{"https://www.diskinternals.com/"}
	deltaURLs := []string{"https://www.diskinternals.com/raid-recovery/raid-levels-and-types/"}

	srv := &Server{
		cfg: &config.Config{
			Crawler: config.CrawlerConfig{
				CrawlScope: "host",
				UserAgent:  "crawlobserver-test",
			},
		},
	}
	result := &deltaCandidateResult{
		settings: &apikeys.ProjectDeltaSettings{
			ProjectID:                projectID,
			UseConditionalRequests:   true,
			MaxDiscoveryDepth:        1,
			RespectRobotsTxt:         true,
			RetryCount:               2,
			RetryBackoffSeconds:      3,
			MaxDiscoveredPagesPerRun: 0,
		},
		baseline: &storage.CrawlSession{
			ID:       "baseline-session",
			SeedURLs: baselineSeeds,
		},
		baselineSourceID:         "raw-full-session",
		baselineEvaluation:       "watermark-evaluation",
		baselineSourceEvaluation: "raw-full-evaluation",
		baselineSnapshotRev:      7,
		baselineWatermarkID:      "latest-delta-session",
		urls:                     deltaURLs,
		preview: deltaPreview{
			TotalCandidates: len(deltaURLs),
			LaunchLimit:     len(deltaURLs),
			WillLaunch:      len(deltaURLs),
			BySource:        map[string]int{"sitemap": len(deltaURLs)},
		},
		baselineSitemapCount: 42,
	}

	req, err := srv.deltaCrawlRequest(result)
	if err != nil {
		t.Fatalf("deltaCrawlRequest() error = %v", err)
	}

	if !reflect.DeepEqual(req.Seeds, deltaURLs) {
		t.Fatalf("req.Seeds = %#v, want %#v", req.Seeds, deltaURLs)
	}
	if !reflect.DeepEqual(req.SessionSeedURLs, baselineSeeds) {
		t.Fatalf("req.SessionSeedURLs = %#v, want %#v", req.SessionSeedURLs, baselineSeeds)
	}
	if req.DiscoveryBudget == nil || *req.DiscoveryBudget != 0 {
		t.Fatalf("req.DiscoveryBudget = %#v, want explicit 0", req.DiscoveryBudget)
	}
	if req.CheckPageResources == nil || !*req.CheckPageResources {
		t.Fatalf("req.CheckPageResources = %#v, want true", req.CheckPageResources)
	}
	if req.DeltaPlan == nil {
		t.Fatal("req.DeltaPlan = nil")
	}
	if req.DeltaPlan.BaselineSessionID != "baseline-session" {
		t.Fatalf("DeltaPlan.BaselineSessionID = %q", req.DeltaPlan.BaselineSessionID)
	}
	if req.DeltaPlan.ConditionalRequestBaselineSessionID != "baseline-session" || !req.DeltaPlan.UseConditionalRequests {
		t.Fatalf("conditional DeltaPlan = %#v", req.DeltaPlan)
	}
	if result.preview.ConditionalRequestBaselineSessionID != "" || result.preview.UseConditionalRequests {
		t.Fatal("deltaCrawlRequest must not mutate the preview result")
	}
	if req.DeltaPlan.BaselineSourceSessionID != "raw-full-session" ||
		req.DeltaPlan.BaselineEvaluationRevision != "watermark-evaluation" ||
		req.DeltaPlan.BaselineSourceEvaluationRevision != "raw-full-evaluation" ||
		req.DeltaPlan.BaselineSnapshotRevision != 7 ||
		req.DeltaPlan.BaselineContentWatermarkSessionID != "latest-delta-session" {
		t.Fatalf("DeltaPlan lineage = %#v", req.DeltaPlan)
	}
	if req.DeltaPlan.BaselineSitemapURLCount != 42 {
		t.Fatalf("DeltaPlan.BaselineSitemapURLCount = %d, want 42", req.DeltaPlan.BaselineSitemapURLCount)
	}
	if !reflect.DeepEqual(req.DeltaPlan.LaunchedURLs, deltaURLs) {
		t.Fatalf("DeltaPlan.LaunchedURLs = %#v, want %#v", req.DeltaPlan.LaunchedURLs, deltaURLs)
	}
}

func TestDeltaCrawlRequestPersistsDisabledConditionalRequests(t *testing.T) {
	srv := &Server{cfg: &config.Config{}}
	req, err := srv.deltaCrawlRequest(&deltaCandidateResult{
		settings: &apikeys.ProjectDeltaSettings{ProjectID: "project-1", UseConditionalRequests: false},
		baseline: &storage.CrawlSession{ID: "current-snapshot"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.DeltaPlan == nil || req.DeltaPlan.UseConditionalRequests || req.DeltaPlan.ConditionalRequestBaselineSessionID != "current-snapshot" {
		t.Fatalf("disabled conditional DeltaPlan = %#v", req.DeltaPlan)
	}
}

func TestBoundDeltaCandidatesHonorsExplicitZeroAndInputPriority(t *testing.T) {
	settings := &apikeys.ProjectDeltaSettings{
		MaxCandidatesPerRun:   0,
		MaxChangedPagesPerRun: 10,
		MaxNewPagesPerRun:     10,
	}
	got, deferred := boundDeltaCandidates([]string{"https://example.test/event"}, map[string]struct{}{}, settings)
	if len(got) != 0 || deferred != 1 {
		t.Fatalf("zero bound = %#v, deferred=%d; want none, 1", got, deferred)
	}

	settings.MaxCandidatesPerRun = 2
	candidates := []string{
		"https://example.test/event",
		"https://example.test/manual",
		"https://example.test/canary",
	}
	got, deferred = boundDeltaCandidates(candidates, map[string]struct{}{}, settings)
	if !reflect.DeepEqual(got, candidates[:2]) || deferred != 1 {
		t.Fatalf("priority bound = %#v, deferred=%d; want %#v, 1", got, deferred, candidates[:2])
	}
}

func TestDeltaCrawlRequestPersistsSitemapSelectionLineage(t *testing.T) {
	srv := &Server{cfg: &config.Config{}}
	selection := &config.DeltaSitemapSelection{
		SelectorRevision: "v1", RawObservationSessionID: "raw-session", RawObservedAt: time.Now().UTC(),
		PublishedSessionID: "materialized-session", PublishedSnapshotRevision: 9,
		PublishedContentWatermarkSessionID: "watermark-session", SelectionComplete: false,
		EventTotal: 2, EventSelected: 1, EventDeferred: 1, SourceByURL: map[string]string{
			"https://example.test/pending": DeltaSitemapSourcePendingUnpublished,
		},
	}
	req, err := srv.deltaCrawlRequest(&deltaCandidateResult{
		settings:         &apikeys.ProjectDeltaSettings{ProjectID: "project-1"},
		baseline:         &storage.CrawlSession{ID: "materialized-session"},
		sitemapSelection: selection,
	})
	if err != nil {
		t.Fatalf("deltaCrawlRequest: %v", err)
	}
	if req.DeltaPlan == nil || !reflect.DeepEqual(req.DeltaPlan.SitemapSelection, selection) {
		t.Fatalf("persisted sitemap selection = %#v, want %#v", req.DeltaPlan, selection)
	}
	if req.DeltaPlan.SitemapSelection == selection {
		t.Fatal("DeltaPlan must own a copy of sitemap selection lineage")
	}
	selection.SourceByURL["https://example.test/pending"] = "changed-after-request"
	if req.DeltaPlan.SitemapSelection.SourceByURL["https://example.test/pending"] == "changed-after-request" {
		t.Fatal("DeltaPlan must own a copy of sitemap selection source provenance")
	}
}

func TestDeltaSitemapSelectionConfigPersistsV2StabilityProvenance(t *testing.T) {
	observedAt := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	selection := DeltaSitemapSelection{
		EventTotal:               4,
		EventSelected:            2,
		EventDeferred:            2,
		PublishedDifferenceTotal: 14,
		ActionableTotal:          4,
		StableAcknowledgedTotal:  10,
		SelectedTotal:            3,
		CanarySelected:           1,
		SelectionComplete:        false,
		PublicationHeld:          true,
		StabilityOlderSessionID:  "raw-older",
		StabilityNewerSessionID:  "raw-newer",
		StabilityProofDigest:     "proof-digest",
		StabilityLegacyPair:      true,
		SourceByURL:              map[string]string{"https://example.test/stable": DeltaSitemapSourceStableUnpublished},
	}
	terms := &storage.DeltaSitemapTerms{Raw: storage.DeltaSitemapObservation{
		SessionID:  "raw-newer",
		ObservedAt: observedAt,
	}}
	lineage := &storage.ProjectCurrentSnapshot{
		CurrentSessionID:          "current-snapshot",
		SnapshotRevision:          12,
		ContentWatermarkSessionID: "content-watermark",
	}

	got := deltaSitemapSelectionConfig(selection, terms, lineage, observedAt)
	if got == nil {
		t.Fatal("deltaSitemapSelectionConfig() = nil")
	}
	if got.SelectorRevision != DeltaSitemapSelectorRevision ||
		got.PublishedDifferenceTotal != 14 || got.ActionableTotal != 4 ||
		got.StableAcknowledgedTotal != 10 || got.SelectedTotal != 3 ||
		!got.PublicationHeld || got.StabilityOlderSessionID != "raw-older" ||
		got.StabilityNewerSessionID != "raw-newer" || got.StabilityProofDigest != "proof-digest" ||
		!got.StabilityLegacyCompletePair {
		t.Fatalf("v2 selector config = %#v", got)
	}
	if got.SourceByURL["https://example.test/stable"] != DeltaSitemapSourceStableUnpublished {
		t.Fatalf("v2 source provenance = %#v", got.SourceByURL)
	}
}

func TestApplyDeltaSitemapPreviewExposesOnlyV2Counts(t *testing.T) {
	preview := deltaPreview{}
	selection := &config.DeltaSitemapSelection{
		SelectorRevision:         DeltaSitemapSelectorRevision,
		EventSelected:            2,
		EventDeferred:            3,
		CanarySelected:           1,
		PublishedDifferenceTotal: 15,
		ActionableTotal:          5,
		StableAcknowledgedTotal:  10,
		SourceByURL: map[string]string{
			"https://example.test/pending": DeltaSitemapSourcePendingUnpublished,
		},
	}
	applyDeltaSitemapPreview(&preview, selection)
	if preview.SitemapPublishedDifferences == nil || *preview.SitemapPublishedDifferences != 15 ||
		preview.SitemapActionable == nil || *preview.SitemapActionable != 5 ||
		preview.SitemapStableAcknowledged == nil || *preview.SitemapStableAcknowledged != 10 {
		t.Fatalf("v2 preview counts = %#v", preview)
	}
	if preview.SitemapEvents != 2 || preview.SitemapPending != 1 || preview.SitemapCanaries != 1 || preview.SitemapDeferred != 3 {
		t.Fatalf("preview legacy fields = %#v", preview)
	}

	legacy := deltaPreview{}
	applyDeltaSitemapPreview(&legacy, &config.DeltaSitemapSelection{SelectorRevision: DeltaSitemapSelectorRevisionV1})
	if legacy.SitemapPublishedDifferences != nil || legacy.SitemapActionable != nil || legacy.SitemapStableAcknowledged != nil {
		t.Fatalf("legacy preview must not fabricate v2 fields: %#v", legacy)
	}
}

func TestDeltaSitemapStabilityProofsNormalizeEvidence(t *testing.T) {
	settings := &apikeys.ProjectDeltaSettings{StripFragments: true, NormalizeTrailingSlash: true}
	proofs := deltaSitemapStabilityProofs(&storage.DeltaSitemapStability{URLs: []storage.DeltaSitemapStabilityURL{
		{Loc: "https://example.test/stable#old-fragment", LastMod: "2026-08-15T16:53:17Z"},
		{Loc: "https://other.test/cross-origin", LastMod: "2026-08-15T16:53:17Z"},
		{Loc: "", LastMod: "2026-08-15T16:53:17Z"},
	}}, []string{"https://example.test/"}, "host", settings)
	if !reflect.DeepEqual(proofs, []DeltaSitemapStabilityProof{{
		URL: "https://example.test/stable", LastMod: "2026-08-15T16:53:17Z",
	}}) {
		t.Fatalf("stability proofs = %#v; cross-origin proof must remain actionable", proofs)
	}
}

func TestDeltaSitemapSelectionInputExcludesCrossOriginTerms(t *testing.T) {
	settings := &apikeys.ProjectDeltaSettings{StripFragments: true, NormalizeTrailingSlash: true, MaxCandidatesPerRun: 10}
	baseline := &storage.CrawlSession{SeedURLs: []string{"https://example.test/"}}
	refresh := &config.DeltaSitemapRefresh{Mode: deltaSitemapRefreshFresh, FetchedAt: time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)}
	foreignURL := "https://other.test/published-difference"
	terms := &storage.DeltaSitemapTerms{
		Raw:       storage.DeltaSitemapObservation{URLs: []storage.DeltaSitemapObservationURL{{Loc: foreignURL, LastMod: "2026-08-15T00:00:00Z"}}},
		Published: storage.DeltaSitemapObservation{URLs: []storage.DeltaSitemapObservationURL{{Loc: foreignURL, LastMod: "2026-08-01T00:00:00Z"}}},
		Stability: &storage.DeltaSitemapStability{
			OlderSessionID: "older", NewerSessionID: "newer", ProofDigest: "foreign-proof",
			URLs: []storage.DeltaSitemapStabilityURL{{Loc: foreignURL, LastMod: "2026-08-15T00:00:00Z"}},
		},
	}
	selection := SelectDeltaSitemapCandidates(deltaSitemapSelectionInput(
		"project", nil, refresh,
		[]storage.SitemapURLRow{{Loc: foreignURL, LastMod: "2026-08-15T00:00:00Z"}},
		terms, settings, baseline,
	))
	if selection.PublishedDifferenceTotal != 0 || selection.ActionableTotal != 0 || selection.StableAcknowledgedTotal != 0 {
		t.Fatalf("cross-origin sitemap term affected project counts: %#v", selection)
	}
	if _, exists := selection.SourceByURL[foreignURL]; exists {
		t.Fatalf("cross-origin source leaked into project selection: %#v", selection.SourceByURL)
	}
}

func TestDeltaLaunchReservationRejectsStaleSnapshotAndProof(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*deltaLaunchFixture, *deltaCandidateResult)
	}{
		{
			name: "snapshot revision",
			mutate: func(fixture *deltaLaunchFixture, _ *deltaCandidateResult) {
				fixture.snapshot.SnapshotRevision++
			},
		},
		{
			name: "proof digest",
			mutate: func(fixture *deltaLaunchFixture, _ *deltaCandidateResult) {
				fixture.terms.Stability.ProofDigest = "replacement-digest"
			},
		},
		{
			name: "proof pair",
			mutate: func(fixture *deltaLaunchFixture, _ *deltaCandidateResult) {
				fixture.terms.Stability.OlderSessionID = "replacement-older"
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newDeltaLaunchFixture(t)
			result := fixture.sitemapCandidateResult(t)
			tt.mutate(fixture, result)

			lock := qualityPromotionLock(fixture.projectID)
			lock.Lock()
			_, _, err := fixture.server.launchDeltaCandidateLocked(context.Background(), fixture.projectID, result)
			lock.Unlock()
			if !errors.Is(err, errDeltaLaunchEvidenceStale) {
				t.Fatalf("launch error = %v, want stale launch evidence", err)
			}
			if len(fixture.manager.startCalls) != 0 {
				t.Fatalf("stale reservation started crawl calls = %#v", fixture.manager.startCalls)
			}
		})
	}
}

func TestDeltaLaunchReservationKeepsConditionalValidatorsOnCurrentSnapshot(t *testing.T) {
	fixture := newDeltaLaunchFixture(t)
	result := fixture.sitemapCandidateResult(t)
	req, err := fixture.server.deltaCrawlRequest(result)
	if err != nil {
		t.Fatal(err)
	}
	if req.DeltaPlan == nil || req.DeltaPlan.ConditionalRequestBaselineSessionID != fixture.baseline.ID {
		t.Fatalf("conditional validator baseline = %#v, want Current Snapshot %q", req.DeltaPlan, fixture.baseline.ID)
	}
	if req.DeltaPlan.ConditionalRequestBaselineSessionID == fixture.terms.Raw.SessionID {
		t.Fatalf("raw stability observation %q became a conditional validator source", fixture.terms.Raw.SessionID)
	}
}

func TestDeltaLaunchReservationSerializesSchedulerAndManualRun(t *testing.T) {
	fixture := newDeltaLaunchFixture(t)
	fixture.settings.Enabled = true
	fixture.settings.ScheduleTime = "00:00"
	if _, err := fixture.keyStore.SaveProjectDeltaSettings(*fixture.settings); err != nil {
		t.Fatal(err)
	}

	fixture.manager.startResult = "daily-delta-race"
	fixture.manager.shouldQueue = true
	fixture.manager.queued = map[string]bool{}
	blocking := &blockingDeltaLaunchManager{
		mockManager:  fixture.manager,
		startEntered: make(chan struct{}),
		startRelease: make(chan struct{}),
	}
	fixture.server.manager = blocking

	schedulerDone := make(chan struct{})
	go func() {
		fixture.server.runDueDeltaProjects(context.Background())
		close(schedulerDone)
	}()
	select {
	case <-blocking.startEntered:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not enter StartCrawl")
	}

	manualDone := make(chan error, 1)
	go func() {
		_, _, err := fixture.server.launchProjectDelta(context.Background(), fixture.projectID, deltaLaunchManual)
		manualDone <- err
	}()
	close(blocking.startRelease)
	select {
	case <-schedulerDone:
	case <-time.After(time.Second):
		t.Fatal("scheduler launch did not finish")
	}
	select {
	case err := <-manualDone:
		if !errors.Is(err, errDeltaLaunchBusy) {
			t.Fatalf("manual concurrent launch error = %v, want active reservation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("manual launch did not finish")
	}
	if len(fixture.manager.startCalls) != 1 {
		t.Fatalf("StartCrawl calls = %d, want exactly one", len(fixture.manager.startCalls))
	}
	settings, err := fixture.keyStore.GetProjectDeltaSettings(fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	if settings.LastSessionID != "daily-delta-race" || settings.LastRunAt == nil {
		t.Fatalf("durable delta reservation = %#v", settings)
	}
}

func TestDeltaLaunchReservationSchedulerRechecksDueAfterManualRun(t *testing.T) {
	fixture := newDeltaLaunchFixture(t)
	fixture.settings.Enabled = true
	fixture.settings.ScheduleTime = "00:00"
	fixture.settings.Timezone = "UTC"
	if _, err := fixture.keyStore.SaveProjectDeltaSettings(*fixture.settings); err != nil {
		t.Fatal(err)
	}
	settings, err := fixture.keyStore.GetProjectDeltaSettings(fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.settings = settings

	fixture.manager.startResult = "manual-delta-race"
	blocking := &blockingDeltaLaunchManager{
		mockManager:  fixture.manager,
		startEntered: make(chan struct{}),
		startRelease: make(chan struct{}),
	}
	fixture.server.manager = blocking

	manualDone := make(chan error, 1)
	go func() {
		_, _, err := fixture.server.launchProjectDelta(context.Background(), fixture.projectID, deltaLaunchManual)
		manualDone <- err
	}()
	select {
	case <-blocking.startEntered:
	case <-time.After(time.Second):
		t.Fatal("manual launch did not enter StartCrawl")
	}

	schedulerDone := make(chan error, 1)
	go func() {
		_, _, err := fixture.server.launchProjectDelta(context.Background(), fixture.projectID, deltaLaunchScheduled)
		schedulerDone <- err
	}()
	close(blocking.startRelease)
	select {
	case err := <-manualDone:
		if err != nil {
			t.Fatalf("manual launch error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("manual launch did not finish")
	}
	select {
	case err := <-schedulerDone:
		if !errors.Is(err, errDeltaLaunchNotDue) {
			t.Fatalf("scheduler launch error = %v, want no longer due", err)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler launch did not finish")
	}
	if len(fixture.manager.startCalls) != 1 {
		t.Fatalf("StartCrawl calls = %d, want exactly one manual crawl", len(fixture.manager.startCalls))
	}
	settings, err = fixture.keyStore.GetProjectDeltaSettings(fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	if settings.LastRunAt == nil || settings.LastSessionID != "manual-delta-race" {
		t.Fatalf("manual run receipt = %#v", settings)
	}
}

func TestDeltaLaunchReservationMarkFailureRollsBackBeforeRetry(t *testing.T) {
	fixture := newDeltaLaunchFixture(t)
	fixture.manager.startResult = "mark-failure-first"
	fixture.manager.shouldQueue = true
	fixture.manager.queued = map[string]bool{}
	markCalls := 0
	fixture.server.markProjectDeltaRun = func(projectID, sessionID string, when time.Time) error {
		markCalls++
		if markCalls == 1 {
			return errors.New("forced delta receipt failure")
		}
		return fixture.keyStore.MarkProjectDeltaRun(projectID, sessionID, when)
	}

	_, firstSessionID, err := fixture.server.launchProjectDelta(context.Background(), fixture.projectID, deltaLaunchManual)
	if err == nil || firstSessionID != "mark-failure-first" {
		t.Fatalf("first launch = (%q, %v), want failed started receipt", firstSessionID, err)
	}
	if !reflect.DeepEqual(fixture.manager.stopCalls, []string{"mark-failure-first"}) {
		t.Fatalf("rollback stop calls = %#v", fixture.manager.stopCalls)
	}
	if fixture.manager.IsQueued("mark-failure-first") {
		t.Fatal("rolled-back session remained queued")
	}
	if _, retained := fixture.server.deltaLaunchReservations.Load(fixture.projectID); retained {
		t.Fatal("verified rollback retained a duplicate-launch reservation")
	}

	fixture.manager.startResult = "mark-failure-retry"
	_, retrySessionID, err := fixture.server.launchProjectDelta(context.Background(), fixture.projectID, deltaLaunchManual)
	if err != nil || retrySessionID != "mark-failure-retry" {
		t.Fatalf("safe retry = (%q, %v)", retrySessionID, err)
	}
	if len(fixture.manager.startCalls) != 2 {
		t.Fatalf("StartCrawl calls = %d, want failed start plus one safe retry", len(fixture.manager.startCalls))
	}
	settings, err := fixture.keyStore.GetProjectDeltaSettings(fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	if settings.LastSessionID != "mark-failure-retry" || settings.LastRunAt == nil {
		t.Fatalf("retry receipt = %#v", settings)
	}
}

func TestDeltaLaunchReservationMarkFailureBlocksRetryWhenRollbackIsUnconfirmed(t *testing.T) {
	fixture := newDeltaLaunchFixture(t)
	fixture.manager.startResult = "mark-failure-unconfirmed"
	fixture.manager.stopErr = errors.New("forced rollback failure")
	fixture.server.markProjectDeltaRun = func(string, string, time.Time) error {
		return errors.New("forced delta receipt failure")
	}

	_, sessionID, err := fixture.server.launchProjectDelta(context.Background(), fixture.projectID, deltaLaunchManual)
	if err == nil || sessionID != "mark-failure-unconfirmed" {
		t.Fatalf("first launch = (%q, %v), want failed receipt with retained reservation", sessionID, err)
	}
	if reservation, retained := fixture.server.deltaLaunchReservations.Load(fixture.projectID); !retained || reservation != sessionID {
		t.Fatalf("retained reservation = (%#v, %t), want %q", reservation, retained, sessionID)
	}

	fixture.server.markProjectDeltaRun = nil
	_, _, err = fixture.server.launchProjectDelta(context.Background(), fixture.projectID, deltaLaunchManual)
	if !errors.Is(err, errDeltaLaunchBusy) {
		t.Fatalf("retry after unconfirmed rollback = %v, want busy reservation", err)
	}
	if len(fixture.manager.startCalls) != 1 {
		t.Fatalf("StartCrawl calls = %d, want no duplicate retry", len(fixture.manager.startCalls))
	}
}

type blockingDeltaLaunchManager struct {
	*mockManager
	startEntered chan struct{}
	startRelease chan struct{}
	startOnce    sync.Once
}

func (m *blockingDeltaLaunchManager) StartCrawl(req crawler.CrawlRequest) (string, error) {
	m.startOnce.Do(func() { close(m.startEntered) })
	<-m.startRelease
	return m.mockManager.StartCrawl(req)
}

type deltaLaunchFixture struct {
	projectID string
	keyStore  *apikeys.Store
	server    *Server
	manager   *mockManager
	settings  *apikeys.ProjectDeltaSettings
	baseline  *storage.CrawlSession
	snapshot  *storage.ProjectCurrentSnapshot
	terms     *storage.DeltaSitemapTerms
}

func newDeltaLaunchFixture(t *testing.T) *deltaLaunchFixture {
	t.Helper()
	keyStore, err := apikeys.NewStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = keyStore.Close() })
	project, err := keyStore.CreateProject("delta-launch-reservation")
	if err != nil {
		t.Fatal(err)
	}
	projectID := project.ID
	baselineID := "25300000-0000-4000-8000-000000000001"
	fullID := "25300000-0000-4000-8000-000000000002"
	watermarkID := "25300000-0000-4000-8000-000000000003"
	fullEval := "25300000-0000-4000-8000-000000000011"
	watermarkEval := "25300000-0000-4000-8000-000000000012"
	fullEvidenceID := "25300000-0000-4000-8000-000000000021"

	baseline := &storage.CrawlSession{
		ID: baselineID, ProjectID: &projectID, Status: "completed", PagesCrawled: 2,
		SeedURLs: []string{"https://example.test/"},
	}
	snapshot := &storage.ProjectCurrentSnapshot{
		ProjectID: projectID, SnapshotRevision: 7, CurrentSessionID: baselineID,
		SourceSessionID: fullID, ContentWatermarkSessionID: watermarkID,
		QualityEvaluationRevision: watermarkEval, BaselineQualityEvaluationRevision: fullEval,
		QualityPromotionStatus: "applied",
	}
	fullEvidence := &storage.PageRankEvidence{
		SessionID: fullID, AttemptID: fullEvidenceID, State: storage.PageRankEvidenceFinalized,
		PredicateVersion: storage.PageRankEligiblePredicateVersion,
	}
	fullQuality := &storage.CrawlQualityResult{
		SessionID: fullID, ProjectID: projectID, EvaluationRevision: fullEval,
		EvaluatorRevision: qualityEvaluatorRevision, PageRankEvidenceRevision: fullEvidenceID,
		PageRankPredicateVersion: storage.PageRankEligiblePredicateVersion,
		Trusted:                  true, IsFullCrawl: true,
	}
	baseStore := &mockStore{
		sessions: []storage.CrawlSession{*baseline},
		getSessionByID: map[string]*storage.CrawlSession{
			baselineID: baseline,
			fullID:     {ID: fullID, ProjectID: &projectID, Status: "completed", Label: "full crawl"},
		},
		deltaProblemURLs: []string{"https://example.test/problem"},
	}
	store := qualitySnapshotServerStore{
		mockStore: baseStore,
		qualityGateMock: qualityGateMock{
			currents:  map[string]*storage.CrawlQualityResult{fullID: fullQuality},
			evidences: map[string]*storage.PageRankEvidence{fullID: fullEvidence},
		},
		currentSnapshotGateMock: currentSnapshotGateMock{
			snapshot: snapshot,
			quality: &storage.CrawlQualityResult{
				SessionID: watermarkID, ProjectID: projectID, EvaluationRevision: watermarkEval, Trusted: true,
			},
			evidence: fullEvidence,
		},
	}
	manager := newMockManager()
	srv := &Server{
		cfg:      &config.Config{Crawler: config.CrawlerConfig{CrawlScope: "host"}},
		store:    store,
		keyStore: keyStore,
		manager:  manager,
	}
	fullQuality.RulesRevision, err = srv.currentQualityRulesRevision(projectID)
	if err != nil {
		t.Fatal(err)
	}
	settings := apikeys.DefaultProjectDeltaSettings(projectID)
	settings.SourceSitemap = false
	settings.SourceGSC = false
	settings.SourceProblemPages = true
	settings.SourceStalePages = false
	settings.SourceManualQueue = false
	settings.MaxCandidatesPerRun = 10
	settings.MaxChangedPagesPerRun = 10
	settings.MaxNewPagesPerRun = 10
	settings.MaxDiscoveredPagesPerRun = 0
	if _, err := keyStore.SaveProjectDeltaSettings(settings); err != nil {
		t.Fatal(err)
	}
	settingsPtr, err := keyStore.GetProjectDeltaSettings(projectID)
	if err != nil {
		t.Fatal(err)
	}
	return &deltaLaunchFixture{
		projectID: projectID,
		keyStore:  keyStore,
		server:    srv,
		manager:   manager,
		settings:  settingsPtr,
		baseline:  baseline,
		snapshot:  snapshot,
	}
}

func (f *deltaLaunchFixture) sitemapCandidateResult(t *testing.T) *deltaCandidateResult {
	t.Helper()
	observedAt := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	stableURL := "https://example.test/stable"
	actionURL := "https://example.test/action"
	f.terms = &storage.DeltaSitemapTerms{
		Raw: storage.DeltaSitemapObservation{
			SessionID: "raw-newer", ObservedAt: observedAt,
			URLs: []storage.DeltaSitemapObservationURL{
				{Loc: stableURL, LastMod: "2026-08-15T16:53:17Z"},
				{Loc: actionURL, LastMod: "2026-08-16T12:00:00Z"},
			},
		},
		Published: storage.DeltaSitemapObservation{
			SessionID: f.baseline.ID,
			URLs: []storage.DeltaSitemapObservationURL{
				{Loc: stableURL, LastMod: "2026-07-01T00:00:00Z"},
				{Loc: actionURL, LastMod: "2026-07-01T00:00:00Z"},
			},
		},
		Stability: &storage.DeltaSitemapStability{
			OlderSessionID: "raw-older", NewerSessionID: "raw-newer", ProofDigest: "proof-digest",
			LegacyCompletePair: true,
			URLs:               []storage.DeltaSitemapStabilityURL{{Loc: stableURL, LastMod: "2026-08-15T16:53:17Z", ContentHash: 42}},
		},
	}
	store := f.server.store.(qualitySnapshotServerStore).mockStore
	store.deltaSitemapTerms = f.terms
	refresh := &config.DeltaSitemapRefresh{Mode: deltaSitemapRefreshFresh, FetchedAt: observedAt}
	freshRows := []storage.SitemapURLRow{
		{Loc: stableURL, LastMod: "2026-08-15T16:53:17Z"},
		{Loc: actionURL, LastMod: "2026-08-16T12:00:00Z"},
	}
	selection := SelectDeltaSitemapCandidates(deltaSitemapSelectionInput(f.projectID, f.snapshot, refresh, freshRows, f.terms, f.settings, f.baseline))
	if selection.ActionableTotal != 1 || selection.StableAcknowledgedTotal != 1 {
		t.Fatalf("fixture selector = %#v", selection)
	}
	persisted := deltaSitemapSelectionConfig(selection, f.terms, f.snapshot, observedAt)
	return &deltaCandidateResult{
		settings:                 f.settings,
		baseline:                 f.baseline,
		baselineSourceID:         f.snapshot.SourceSessionID,
		baselineEvaluation:       f.snapshot.QualityEvaluationRevision,
		baselineSourceEvaluation: f.snapshot.BaselineQualityEvaluationRevision,
		baselineSnapshotRev:      f.snapshot.SnapshotRevision,
		baselineWatermarkID:      f.snapshot.ContentWatermarkSessionID,
		urls:                     []string{actionURL},
		sitemapURLRows:           freshRows,
		sitemapRefresh:           refresh,
		sitemapSelection:         persisted,
		preview:                  deltaPreview{WillLaunch: 1, LaunchLimit: 1},
	}
}

func TestDeltaPlanLineageCapturesD1WatermarkAndRawF1Source(t *testing.T) {
	keyStore, err := apikeys.NewStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer keyStore.Close()
	projectID := "project-delta-plan-lineage"
	fullID := "25100000-0000-4000-8000-000000000001"
	deltaID := "25100000-0000-4000-8000-000000000002"
	materializedID := "25100000-0000-4000-8000-000000000003"
	fullEval := "25100000-0000-4000-8000-000000000011"
	deltaEval := "25100000-0000-4000-8000-000000000012"
	fullEvidenceID := "25100000-0000-4000-8000-000000000021"
	snap := &storage.ProjectCurrentSnapshot{
		ProjectID: projectID, SnapshotRevision: 8, CurrentSessionID: materializedID,
		SourceSessionID: fullID, ContentWatermarkSessionID: deltaID,
		QualityEvaluationRevision: deltaEval, BaselineQualityEvaluationRevision: fullEval,
		QualityPromotionStatus: "applied",
	}
	deltaQuality := &storage.CrawlQualityResult{SessionID: deltaID, ProjectID: projectID, EvaluationRevision: deltaEval, Trusted: true}
	fullQuality := &storage.CrawlQualityResult{
		SessionID: fullID, ProjectID: projectID, EvaluationRevision: fullEval,
		EvaluatorRevision: qualityEvaluatorRevision, PageRankEvidenceRevision: fullEvidenceID,
		PageRankPredicateVersion: storage.PageRankEligiblePredicateVersion,
		Trusted:                  true, IsFullCrawl: true,
	}
	fullEvidence := &storage.PageRankEvidence{
		SessionID: fullID, AttemptID: fullEvidenceID, State: storage.PageRankEvidenceFinalized,
		PredicateVersion: storage.PageRankEligiblePredicateVersion,
	}
	store := qualitySnapshotServerStore{
		mockStore: &mockStore{getSessionByID: map[string]*storage.CrawlSession{
			fullID: {ID: fullID, ProjectID: &projectID, Status: "completed", Label: "full crawl"},
		}},
		qualityGateMock: qualityGateMock{
			currents:  map[string]*storage.CrawlQualityResult{fullID: fullQuality},
			evidences: map[string]*storage.PageRankEvidence{fullID: fullEvidence},
		},
		currentSnapshotGateMock: currentSnapshotGateMock{snapshot: snap, quality: deltaQuality, evidence: fullEvidence},
	}
	srv := &Server{store: store, keyStore: keyStore, cfg: &config.Config{}}
	fullQuality.RulesRevision, err = srv.currentQualityRulesRevision(projectID)
	if err != nil {
		t.Fatal(err)
	}
	lineage, err := srv.deltaPlanLineage(context.Background(), projectID, materializedID)
	if err != nil {
		t.Fatalf("deltaPlanLineage: %v", err)
	}
	if lineage.SnapshotRevision != 8 || lineage.CurrentSessionID != materializedID || lineage.SourceSessionID != fullID ||
		lineage.ContentWatermarkSessionID != deltaID || lineage.QualityEvaluationRevision != deltaEval ||
		lineage.BaselineQualityEvaluationRevision != fullEval {
		t.Fatalf("D1 -> D2 plan lineage = %#v", lineage)
	}
	settings := apikeys.DefaultProjectDeltaSettings(projectID)
	req, err := srv.deltaCrawlRequest(&deltaCandidateResult{
		settings:                 &settings,
		baseline:                 &storage.CrawlSession{ID: materializedID},
		baselineSourceID:         lineage.SourceSessionID,
		baselineEvaluation:       lineage.QualityEvaluationRevision,
		baselineSourceEvaluation: lineage.BaselineQualityEvaluationRevision,
		baselineSnapshotRev:      lineage.SnapshotRevision,
		baselineWatermarkID:      lineage.ContentWatermarkSessionID,
	})
	if err != nil {
		t.Fatalf("deltaCrawlRequest: %v", err)
	}
	if req.DeltaPlan == nil || req.DeltaPlan.BaselineSessionID != materializedID ||
		req.DeltaPlan.BaselineSourceSessionID != fullID ||
		req.DeltaPlan.BaselineContentWatermarkSessionID != deltaID ||
		req.DeltaPlan.BaselineEvaluationRevision != deltaEval ||
		req.DeltaPlan.BaselineSourceEvaluationRevision != fullEval ||
		req.DeltaPlan.BaselineSnapshotRevision != 8 {
		t.Fatalf("D1 -> D2 persisted DeltaPlan = %#v", req.DeltaPlan)
	}
}

func TestDeltaPlanningSerializesLineageAndCandidateReadsAgainstPromotion(t *testing.T) {
	keyStore, err := apikeys.NewStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer keyStore.Close()
	project, err := keyStore.CreateProject("delta-planning-lock")
	if err != nil {
		t.Fatal(err)
	}
	projectID := project.ID
	fullID := "25100000-0000-4000-8000-000000000101"
	watermarkID := "25100000-0000-4000-8000-000000000102"
	materializedID := "25100000-0000-4000-8000-000000000103"
	fullEval := "25100000-0000-4000-8000-000000000111"
	watermarkEval := "25100000-0000-4000-8000-000000000112"
	fullEvidenceID := "25100000-0000-4000-8000-000000000121"
	problemEntered := make(chan struct{})
	problemRelease := make(chan struct{})
	snapshot := &storage.ProjectCurrentSnapshot{
		ProjectID: projectID, SnapshotRevision: 7, CurrentSessionID: materializedID,
		SourceSessionID: fullID, ContentWatermarkSessionID: watermarkID,
		QualityEvaluationRevision: watermarkEval, BaselineQualityEvaluationRevision: fullEval,
		QualityPromotionStatus: "applied",
	}
	fullQuality := &storage.CrawlQualityResult{
		SessionID: fullID, ProjectID: projectID, EvaluationRevision: fullEval,
		EvaluatorRevision: qualityEvaluatorRevision, PageRankEvidenceRevision: fullEvidenceID,
		PageRankPredicateVersion: storage.PageRankEligiblePredicateVersion,
		Trusted:                  true, IsFullCrawl: true,
	}
	fullEvidence := &storage.PageRankEvidence{
		SessionID: fullID, AttemptID: fullEvidenceID, State: storage.PageRankEvidenceFinalized,
		PredicateVersion: storage.PageRankEligiblePredicateVersion,
	}
	baseStore := &mockStore{
		getSessionByID: map[string]*storage.CrawlSession{
			materializedID: {
				ID: materializedID, ProjectID: &projectID, Status: "completed", PagesCrawled: 2,
				SeedURLs: []string{"https://example.com/"},
			},
			fullID: {ID: fullID, ProjectID: &projectID, Status: "completed", PagesCrawled: 1},
		},
		deltaProblemURLs:    []string{"https://example.com/problem"},
		deltaProblemEntered: problemEntered,
		deltaProblemRelease: problemRelease,
	}
	store := qualitySnapshotServerStore{
		mockStore: baseStore,
		qualityGateMock: qualityGateMock{
			currents:  map[string]*storage.CrawlQualityResult{fullID: fullQuality},
			evidences: map[string]*storage.PageRankEvidence{fullID: fullEvidence},
		},
		currentSnapshotGateMock: currentSnapshotGateMock{
			snapshot: snapshot,
			quality: &storage.CrawlQualityResult{
				SessionID: watermarkID, ProjectID: projectID, EvaluationRevision: watermarkEval, Trusted: true,
			},
			evidence: fullEvidence,
		},
	}
	srv := &Server{store: store, keyStore: keyStore, cfg: &config.Config{}}
	fullQuality.RulesRevision, err = srv.currentQualityRulesRevision(projectID)
	if err != nil {
		t.Fatal(err)
	}
	settings := apikeys.DefaultProjectDeltaSettings(projectID)
	settings.SourceSitemap = false
	settings.SourceGSC = false
	settings.SourceProblemPages = true
	settings.SourceStalePages = false
	settings.SourceManualQueue = false
	settings.MaxDiscoveredPagesPerRun = 0
	if _, err := keyStore.SaveProjectDeltaSettings(settings); err != nil {
		t.Fatal(err)
	}

	type planningResult struct {
		result *deltaCandidateResult
		err    error
	}
	planned := make(chan planningResult, 1)
	go func() {
		result, err := srv.buildDeltaCandidates(context.Background(), projectID)
		planned <- planningResult{result: result, err: err}
	}()
	select {
	case <-problemEntered:
	case <-time.After(time.Second):
		t.Fatal("Delta planning did not reach the candidate-read barrier")
	}
	projectLock := qualityPromotionLock(projectID)
	if projectLock.TryLock() {
		projectLock.Unlock()
		close(problemRelease)
		t.Fatal("Delta planning did not hold the shared project lock at the candidate-read barrier")
	}

	promotionAttempted := make(chan struct{})
	promotionEntered := make(chan struct{})
	promotionDone := make(chan struct{})
	go func() {
		close(promotionAttempted)
		lock := qualityPromotionLock(projectID)
		lock.Lock()
		close(promotionEntered)
		snapshot.SnapshotRevision = 8
		snapshot.ContentWatermarkSessionID = "25100000-0000-4000-8000-000000000104"
		lock.Unlock()
		close(promotionDone)
	}()
	<-promotionAttempted
	close(problemRelease)
	got := <-planned
	if got.err != nil {
		t.Fatalf("buildDeltaCandidates: %v", got.err)
	}
	if got.result.baselineSnapshotRev != 7 || got.result.baselineWatermarkID != watermarkID ||
		!reflect.DeepEqual(got.result.urls, []string{"https://example.com/problem"}) {
		t.Fatalf("planning mixed snapshot revisions: %#v", got.result)
	}
	select {
	case <-promotionEntered:
	case <-time.After(time.Second):
		t.Fatal("promotion did not resume after Delta planning released the lock")
	}
	<-promotionDone
	if !projectLock.TryLock() {
		t.Fatal("project lock remained held after planning and promotion completed")
	}
	projectLock.Unlock()
}

func TestOrphanCleanupKeepsPlanningLockedThroughPageRankFinalization(t *testing.T) {
	keyStore, err := apikeys.NewStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer keyStore.Close()
	project, err := keyStore.CreateProject("orphan-cleanup-lock")
	if err != nil {
		t.Fatal(err)
	}
	projectID := project.ID
	currentID := "25100000-0000-4000-8000-000000000201"
	pagerankEntered := make(chan struct{})
	pagerankRelease := make(chan struct{})
	store := qualitySnapshotServerStore{
		mockStore: &mockStore{
			orphanCandidates: []storage.Orphan404CleanupCandidate{{URL: "https://example.com/missing"}},
			pagerankEntered:  pagerankEntered, pagerankRelease: pagerankRelease,
		},
		currentSnapshotGateMock: currentSnapshotGateMock{snapshot: &storage.ProjectCurrentSnapshot{
			ProjectID: projectID, CurrentSessionID: currentID,
		}},
	}
	srv := &Server{store: store, keyStore: keyStore}
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/current-snapshot/orphan-404-cleanup", jsonBody(t, map[string]interface{}{
		"confirm": true,
		"limit":   10,
	}))
	req.SetPathValue("id", projectID)
	rec := httptest.NewRecorder()
	srv.handleProjectOrphan404Cleanup(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cleanup response = %d %s", rec.Code, rec.Body.String())
	}
	select {
	case <-pagerankEntered:
	case <-time.After(time.Second):
		t.Fatal("cleanup PageRank finalization did not start")
	}
	projectLock := qualityPromotionLock(projectID)
	if projectLock.TryLock() {
		projectLock.Unlock()
		close(pagerankRelease)
		t.Fatal("cleanup released the project lock before PageRank finalization")
	}

	planningAttempted := make(chan struct{})
	planningEntered := make(chan struct{})
	go func() {
		close(planningAttempted)
		lock := qualityPromotionLock(projectID)
		lock.Lock()
		close(planningEntered)
		lock.Unlock()
	}()
	<-planningAttempted
	close(pagerankRelease)
	select {
	case <-planningEntered:
	case <-time.After(time.Second):
		t.Fatal("planning did not resume after cleanup PageRank finalization")
	}
	if !projectLock.TryLock() {
		t.Fatal("project lock remained held after cleanup PageRank finalization")
	}
	projectLock.Unlock()
}

func TestDeltaCandidateSourcesForLaunchedOrdersKnownSources(t *testing.T) {
	launched := []string{
		"https://example.com/a",
		"https://example.com/b",
	}
	sourceSets := map[string]map[string]struct{}{
		"https://example.com/a": {
			"stale_pages":   {},
			"manual_queue":  {},
			"problem_pages": {},
		},
		"https://example.com/b": {
			"sitemap": {},
		},
		"https://example.com/not-launched": {
			"manual_queue": {},
		},
	}

	got := deltaCandidateSourcesForLaunched(launched, sourceSets)
	want := map[string][]string{
		"https://example.com/a": {"manual_queue", "problem_pages", "stale_pages"},
		"https://example.com/b": {"sitemap"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("deltaCandidateSourcesForLaunched() = %#v, want %#v", got, want)
	}
}
