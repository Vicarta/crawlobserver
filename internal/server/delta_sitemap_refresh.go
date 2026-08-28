package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/SEObserver/crawlobserver/internal/apikeys"
	"github.com/SEObserver/crawlobserver/internal/config"
	"github.com/SEObserver/crawlobserver/internal/fetcher"
	"github.com/SEObserver/crawlobserver/internal/storage"
)

const (
	deltaSitemapRefreshFresh            = "fresh"
	deltaSitemapRefreshSkipped          = "skipped"
	deltaSitemapRefreshSnapshotFallback = "snapshot_fallback"

	deltaSitemapComparisonLimit = 50000
)

type deltaSitemapRefreshResult struct {
	Refresh        *config.DeltaSitemapRefresh
	Candidates     []string
	SitemapRows    []storage.SitemapRow
	SitemapURLRows []storage.SitemapURLRow
}

// refreshDeltaSitemap obtains one complete sitemap observation before a Delta
// plan is built. It deliberately does not alter a session or the current
// snapshot: preview is therefore read-only, while a launched request persists
// this exact observation through CrawlRequest.InitialSitemaps.
func (s *Server) refreshDeltaSitemap(ctx context.Context, baseline *storage.CrawlSession, settings *apikeys.ProjectDeltaSettings, perSourceLimit int) (*deltaSitemapRefreshResult, error) {
	if baseline == nil {
		return nil, fmt.Errorf("baseline session is required for sitemap refresh")
	}
	baselineURLs, err := s.store.DeltaSitemapCandidateURLs(ctx, baseline.ID, deltaSitemapComparisonLimit)
	if err != nil {
		return nil, fmt.Errorf("loading current snapshot sitemap URLs: %w", err)
	}

	crawlerCfg := s.deltaCrawlerConfig(baseline)
	dialOpts := fetcher.DialOptions{
		SourceIP:        crawlerCfg.SourceIP,
		ForceIPv4:       crawlerCfg.ForceIPv4,
		AllowPrivateIPs: crawlerCfg.AllowPrivateIPs,
	}
	robots := fetcher.NewRobotsCache(crawlerCfg.UserAgent, crawlerCfg.Timeout, dialOpts, fetcher.TLSProfile(crawlerCfg.TLSProfile))
	for _, seed := range baseline.SeedURLs {
		// IsAllowed primes the cache. The return value is irrelevant here: sitemap
		// declarations are an independent robots.txt capability.
		robots.IsAllowed(seed)
	}

	roots := stableUniqueURLs(append(append([]string(nil), crawlerCfg.SitemapURLs...), robots.DeclaredSitemapURLs()...))
	if len(roots) == 0 {
		roots = stableUniqueURLs(robots.SitemapFallbackURLs())
	}
	if len(roots) == 0 {
		return s.deltaSitemapRefreshFailure(ctx, baseline.ID, baselineURLs, settings, time.Now().UTC(), "no declared or conventional sitemap URLs were available")
	}

	client := fetcher.New(crawlerCfg.UserAgent, crawlerCfg.Timeout, crawlerCfg.MaxBodySize, dialOpts, fetcher.TLSProfile(crawlerCfg.TLSProfile)).Client()
	observation := fetcher.ObserveSitemaps(ctx, client, crawlerCfg.UserAgent, roots)
	if !observation.Complete {
		return s.deltaSitemapRefreshFailure(ctx, baseline.ID, baselineURLs, settings, observation.FetchedAt, sitemapObservationWarning(observation))
	}

	result := &deltaSitemapRefreshResult{
		Refresh: &config.DeltaSitemapRefresh{
			Mode:                deltaSitemapRefreshFresh,
			FetchedAt:           observation.FetchedAt,
			DeclaredSitemapURLs: append([]string(nil), roots...),
			FetchedSitemapURLs:  append([]string(nil), observation.AttemptedURLs...),
			SnapshotURLCount:    len(uniqueNormalizedDeltaURLs(baselineURLs, settings)),
		},
	}

	freshSet := make(map[string]struct{})
	for _, entry := range observation.Entries {
		result.SitemapRows = append(result.SitemapRows, storage.SitemapRow{
			URL:        entry.URL,
			Type:       entry.Type,
			URLCount:   uint32(len(entry.URLs)),
			ParentURL:  entry.ParentURL,
			StatusCode: uint16(entry.StatusCode),
			FetchedAt:  observation.FetchedAt,
		})
		for _, sitemapURL := range entry.URLs {
			rawLoc := sitemapURL.RawLoc
			if rawLoc == "" {
				rawLoc = sitemapURL.Loc
			}
			result.SitemapURLRows = append(result.SitemapURLRows, storage.SitemapURLRow{
				SitemapURL: entry.URL,
				Loc:        rawLoc,
				LastMod:    sitemapURL.LastMod,
				ChangeFreq: sitemapURL.ChangeFreq,
				Priority:   sitemapURL.Priority,
			})
			if len(result.Refresh.RawEvidence) < config.DeltaSitemapEvidenceLimit {
				result.Refresh.RawEvidence = append(result.Refresh.RawEvidence, config.DeltaSitemapEvidenceRef{
					SitemapURL: entry.URL,
					RawLoc:     rawLoc,
				})
			}
			normalized, normalizeErr := normalizeDeltaURL(rawLoc, settings)
			if normalizeErr != nil || normalized == "" {
				result.Refresh.InvalidEntryCount++
				continue
			}
			freshSet[normalized] = struct{}{}
		}
	}
	result.Candidates = sortedSet(freshSet)
	result.Refresh.FreshURLCount = len(result.Candidates)
	result.Refresh.RawURLRowCount = len(result.SitemapURLRows)
	baselineSet := uniqueNormalizedDeltaURLs(baselineURLs, settings)
	result.Refresh.AddedCount = countSetDifference(freshSet, baselineSet)
	result.Refresh.RemovedCount = countSetDifference(baselineSet, freshSet)
	return result, nil
}

func (s *Server) deltaSitemapRefreshFailure(ctx context.Context, baselineID string, baselineURLs []string, settings *apikeys.ProjectDeltaSettings, fetchedAt time.Time, warning string) (*deltaSitemapRefreshResult, error) {
	if fetchedAt.IsZero() {
		fetchedAt = time.Now().UTC()
	}
	refresh := &config.DeltaSitemapRefresh{
		Mode:             deltaSitemapRefreshSkipped,
		FetchedAt:        fetchedAt,
		SnapshotURLCount: len(uniqueNormalizedDeltaURLs(baselineURLs, settings)),
		Warnings:         []string{warning},
	}
	if settings.SitemapRefreshFailureMode != apikeys.SitemapRefreshFailureModeSnapshotFallback {
		return &deltaSitemapRefreshResult{Refresh: refresh}, nil
	}
	fallback, err := s.store.DeltaSitemapCandidateURLs(ctx, baselineID, max(deltaSitemapComparisonLimit, len(baselineURLs)))
	if err != nil {
		return nil, fmt.Errorf("loading explicit snapshot sitemap fallback: %w", err)
	}
	refresh.Mode = deltaSitemapRefreshSnapshotFallback
	refresh.Warnings = append(refresh.Warnings, "Using explicit snapshot fallback; this sitemap input is not fresh.")
	return &deltaSitemapRefreshResult{Refresh: refresh, Candidates: fallback}, nil
}

func (s *Server) deltaCrawlerConfig(baseline *storage.CrawlSession) config.CrawlerConfig {
	crawlerCfg := s.cfg.Crawler
	if baseline == nil || strings.TrimSpace(baseline.Config) == "" {
		return crawlerCfg
	}
	var saved config.Config
	if err := json.Unmarshal([]byte(baseline.Config), &saved); err == nil && saved.Crawler.UserAgent != "" {
		crawlerCfg = saved.Crawler
	}
	return crawlerCfg
}

func sitemapObservationWarning(observation fetcher.SitemapObservation) string {
	if observation.Failure == nil {
		return "sitemap traversal did not complete"
	}
	if observation.Failure.URL == "" {
		return fmt.Sprintf("sitemap refresh failed (%s): %s", observation.Failure.Kind, observation.Failure.Message)
	}
	return fmt.Sprintf("sitemap refresh failed (%s) for %s: %s", observation.Failure.Kind, observation.Failure.URL, observation.Failure.Message)
}

func stableUniqueURLs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		result = append(result, trimmed)
	}
	return result
}

func uniqueNormalizedDeltaURLs(values []string, settings *apikeys.ProjectDeltaSettings) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized, err := normalizeDeltaURL(value, settings)
		if err == nil && normalized != "" {
			result[normalized] = struct{}{}
		}
	}
	return result
}

func countSetDifference(left, right map[string]struct{}) int {
	count := 0
	for value := range left {
		if _, ok := right[value]; !ok {
			count++
		}
	}
	return count
}

func sortedSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func cloneDeltaSitemapRefresh(value *config.DeltaSitemapRefresh) *config.DeltaSitemapRefresh {
	if value == nil {
		return nil
	}
	clone := *value
	clone.DeclaredSitemapURLs = append([]string(nil), value.DeclaredSitemapURLs...)
	clone.FetchedSitemapURLs = append([]string(nil), value.FetchedSitemapURLs...)
	clone.Warnings = append([]string(nil), value.Warnings...)
	clone.RawEvidence = append([]config.DeltaSitemapEvidenceRef(nil), value.RawEvidence...)
	return &clone
}

func deltaSitemapSelectionURLs(rows []storage.SitemapURLRow, settings *apikeys.ProjectDeltaSettings) []DeltaSitemapSelectionURL {
	values := make([]DeltaSitemapSelectionURL, 0, len(rows))
	for _, row := range rows {
		normalized, err := normalizeDeltaURL(row.Loc, settings)
		if err != nil || normalized == "" {
			continue
		}
		values = append(values, DeltaSitemapSelectionURL{URL: normalized, LastMod: row.LastMod})
	}
	return values
}

func deltaSitemapSelectionURLsFromObservation(observation storage.DeltaSitemapObservation, settings *apikeys.ProjectDeltaSettings) []DeltaSitemapSelectionURL {
	values := make([]DeltaSitemapSelectionURL, 0, len(observation.URLs))
	for _, row := range observation.URLs {
		normalized, err := normalizeDeltaURL(row.Loc, settings)
		if err != nil || normalized == "" {
			continue
		}
		values = append(values, DeltaSitemapSelectionURL{URL: normalized, LastMod: row.LastMod})
	}
	return values
}

func deltaSitemapRotationEpoch(observedAt time.Time) time.Time {
	if observedAt.IsZero() {
		return time.Time{}
	}
	utc := observedAt.UTC()
	return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
}

func deltaSitemapSelectionConfig(selection DeltaSitemapSelection, terms *storage.DeltaSitemapTerms, lineage *storage.ProjectCurrentSnapshot, observedAt time.Time) *config.DeltaSitemapSelection {
	if terms == nil || lineage == nil {
		return nil
	}
	return &config.DeltaSitemapSelection{
		SelectorRevision:                   DeltaSitemapSelectorRevision,
		RawObservationSessionID:            terms.Raw.SessionID,
		RawObservedAt:                      terms.Raw.ObservedAt,
		PublishedSessionID:                 lineage.CurrentSessionID,
		PublishedSnapshotRevision:          lineage.SnapshotRevision,
		PublishedContentWatermarkSessionID: lineage.ContentWatermarkSessionID,
		RotationEpoch:                      deltaSitemapRotationEpoch(observedAt),
		EventTotal:                         selection.EventTotal,
		EventSelected:                      selection.EventSelected,
		EventDeferred:                      selection.EventDeferred,
		PublishedDifferenceTotal:           selection.PublishedDifferenceTotal,
		ActionableTotal:                    selection.ActionableTotal,
		StableAcknowledgedTotal:            selection.StableAcknowledgedTotal,
		SelectedTotal:                      selection.SelectedTotal,
		CanarySelected:                     selection.CanarySelected,
		SelectionComplete:                  selection.SelectionComplete,
		PublicationHeld:                    selection.PublicationHeld,
		StabilityOlderSessionID:            selection.StabilityOlderSessionID,
		StabilityNewerSessionID:            selection.StabilityNewerSessionID,
		StabilityProofDigest:               selection.StabilityProofDigest,
		StabilityLegacyCompletePair:        selection.StabilityLegacyPair,
		SourceByURL:                        copyStringStringMap(selection.SourceByURL),
	}
}

func cloneDeltaSitemapSelection(value *config.DeltaSitemapSelection) *config.DeltaSitemapSelection {
	if value == nil {
		return nil
	}
	clone := *value
	clone.SourceByURL = copyStringStringMap(value.SourceByURL)
	return &clone
}

func copyStringStringMap(value map[string]string) map[string]string {
	if value == nil {
		return nil
	}
	clone := make(map[string]string, len(value))
	for key, item := range value {
		clone[key] = item
	}
	return clone
}

func sitemapSelectionPendingCount(selection *config.DeltaSitemapSelection) int {
	if selection == nil {
		return 0
	}
	count := 0
	for _, source := range selection.SourceByURL {
		if source == DeltaSitemapSourcePendingUnpublished {
			count++
		}
	}
	return count
}

func copySitemapRows(rows []storage.SitemapRow) []storage.SitemapRow {
	return append([]storage.SitemapRow(nil), rows...)
}

func copySitemapURLRows(rows []storage.SitemapURLRow) []storage.SitemapURLRow {
	return append([]storage.SitemapURLRow(nil), rows...)
}
