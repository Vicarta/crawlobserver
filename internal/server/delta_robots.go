package server

import (
	"encoding/json"
	"sort"

	"github.com/SEObserver/crawlobserver/internal/config"
	"github.com/SEObserver/crawlobserver/internal/fetcher"
	"github.com/SEObserver/crawlobserver/internal/storage"
)

const deltaRobotsExcludedSampleLimit = 20

type deltaRobotsChecker interface {
	IsAllowed(string) bool
}

func newDeltaRobotsCache(crawlerCfg config.CrawlerConfig) *fetcher.RobotsCache {
	return fetcher.NewRobotsCache(crawlerCfg.UserAgent, crawlerCfg.Timeout, deltaDialOptions(crawlerCfg), fetcher.TLSProfile(crawlerCfg.TLSProfile))
}

// deltaRobotsCrawlerConfig mirrors the request fields StartCrawl overlays on
// the current runtime config; timeout and network permissions remain runtime-owned.
func (s *Server) deltaRobotsCrawlerConfig(baseline *storage.CrawlSession) config.CrawlerConfig {
	crawlerCfg := s.cfg.Crawler
	if baseline == nil || baseline.Config == "" {
		return crawlerCfg
	}
	var saved config.Config
	if err := json.Unmarshal([]byte(baseline.Config), &saved); err != nil {
		return crawlerCfg
	}
	if saved.Crawler.UserAgent != "" {
		crawlerCfg.UserAgent = saved.Crawler.UserAgent
	}
	if saved.Crawler.TLSProfile != "" {
		crawlerCfg.TLSProfile = saved.Crawler.TLSProfile
	}
	if saved.Crawler.SourceIP != "" {
		crawlerCfg.SourceIP = saved.Crawler.SourceIP
	}
	if saved.Crawler.ForceIPv4 {
		crawlerCfg.ForceIPv4 = true
	}
	return crawlerCfg
}

func deltaDialOptions(crawlerCfg config.CrawlerConfig) fetcher.DialOptions {
	return fetcher.DialOptions{
		SourceIP:        crawlerCfg.SourceIP,
		ForceIPv4:       crawlerCfg.ForceIPv4,
		AllowPrivateIPs: crawlerCfg.AllowPrivateIPs,
	}
}

func deltaRobotsExcludedURLs(urls []DeltaSitemapSelectionURL, robots deltaRobotsChecker) map[string]struct{} {
	excluded := make(map[string]struct{})
	if robots == nil {
		return excluded
	}
	for _, candidate := range urls {
		if !robots.IsAllowed(candidate.URL) {
			excluded[candidate.URL] = struct{}{}
		}
	}
	return excluded
}

// selectDeltaSitemapCandidatesWithRobotsExclusions keeps raw published-vs-fresh
// evidence and stable acknowledgement intact, while only eligible execution
// events and canaries participate in the bounded selection.
func selectDeltaSitemapCandidatesWithRobotsExclusions(input DeltaSitemapSelectionInput, excluded map[string]struct{}) (DeltaSitemapSelection, map[string]string) {
	full := SelectDeltaSitemapCandidates(input)
	if len(excluded) == 0 {
		return full, nil
	}

	eligibleInput := input
	eligibleInput.Fresh = make([]DeltaSitemapSelectionURL, 0, len(input.Fresh))
	excludedSources := make(map[string]string)
	for _, candidate := range input.Fresh {
		if _, blocked := excluded[candidate.URL]; blocked {
			if source, planned := full.SourceByURL[candidate.URL]; planned && source != DeltaSitemapSourceStableUnpublished {
				excludedSources[candidate.URL] = source
			}
			continue
		}
		eligibleInput.Fresh = append(eligibleInput.Fresh, candidate)
	}

	eligible := SelectDeltaSitemapCandidates(eligibleInput)
	eligible.PublishedDifferenceTotal = full.PublishedDifferenceTotal
	eligible.StableAcknowledgedTotal = full.StableAcknowledgedTotal
	eligible.PublicationHeld = full.PublicationHeld
	if eligible.SourceByURL == nil {
		eligible.SourceByURL = make(map[string]string)
	}
	for url, source := range full.SourceByURL {
		if source == DeltaSitemapSourceStableUnpublished {
			eligible.SourceByURL[url] = source
		}
	}
	eligible.SelectedTotal = eligible.EventSelected + eligible.CanarySelected
	return eligible, excludedSources
}

func filterDeltaCandidatesByRobots(candidates []string, robots deltaRobotsChecker, excluded map[string]struct{}) ([]string, map[string]struct{}) {
	if excluded == nil {
		excluded = make(map[string]struct{})
	}
	if robots == nil {
		return candidates, excluded
	}
	allowed := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if _, blocked := excluded[candidate]; blocked || !robots.IsAllowed(candidate) {
			excluded[candidate] = struct{}{}
			continue
		}
		allowed = append(allowed, candidate)
	}
	return allowed, excluded
}

func mergeDeltaRobotsExcludedSources(target map[string]map[string]struct{}, excluded map[string]string) {
	for url, source := range excluded {
		if source == "" {
			continue
		}
		if target[url] == nil {
			target[url] = make(map[string]struct{})
		}
		target[url][source] = struct{}{}
	}
}

func mergeDeltaRobotsExcludedCandidateSources(target map[string]map[string]struct{}, excluded map[string]struct{}, sourceSets map[string]map[string]struct{}) {
	for url := range excluded {
		for source := range sourceSets[url] {
			if target[url] == nil {
				target[url] = make(map[string]struct{})
			}
			target[url][source] = struct{}{}
		}
	}
}

func deltaRobotsExclusionSummary(excluded map[string]map[string]struct{}) (int, []string, map[string][]string) {
	urls := make([]string, 0, len(excluded))
	for url := range excluded {
		urls = append(urls, url)
	}
	sort.Strings(urls)
	count := len(urls)
	if len(urls) > deltaRobotsExcludedSampleLimit {
		urls = urls[:deltaRobotsExcludedSampleLimit]
	}
	sources := make(map[string][]string, len(urls))
	for _, url := range urls {
		sources[url] = orderedDeltaCandidateSources(excluded[url])
	}
	return count, urls, sources
}
