package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/SEObserver/crawlobserver/internal/apikeys"
	"github.com/SEObserver/crawlobserver/internal/config"
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
		urls: deltaURLs,
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
	if req.CheckPageResources == nil || !*req.CheckPageResources {
		t.Fatalf("req.CheckPageResources = %#v, want true", req.CheckPageResources)
	}
	if req.DeltaPlan == nil {
		t.Fatal("req.DeltaPlan = nil")
	}
	if req.DeltaPlan.BaselineSessionID != "baseline-session" {
		t.Fatalf("DeltaPlan.BaselineSessionID = %q", req.DeltaPlan.BaselineSessionID)
	}
	if req.DeltaPlan.BaselineSitemapURLCount != 42 {
		t.Fatalf("DeltaPlan.BaselineSitemapURLCount = %d, want 42", req.DeltaPlan.BaselineSitemapURLCount)
	}
	if !reflect.DeepEqual(req.DeltaPlan.LaunchedURLs, deltaURLs) {
		t.Fatalf("DeltaPlan.LaunchedURLs = %#v, want %#v", req.DeltaPlan.LaunchedURLs, deltaURLs)
	}
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
