package server

import (
	"reflect"
	"testing"

	"github.com/SEObserver/crawlobserver/internal/apikeys"
	"github.com/SEObserver/crawlobserver/internal/config"
	"github.com/SEObserver/crawlobserver/internal/storage"
)

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
			LaunchLimit: len(deltaURLs),
		},
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
}
