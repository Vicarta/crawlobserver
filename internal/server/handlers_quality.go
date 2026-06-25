package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/SEObserver/crawlobserver/internal/apikeys"
	"github.com/SEObserver/crawlobserver/internal/applog"
	"github.com/SEObserver/crawlobserver/internal/storage"
)

type qualityStorage interface {
	UpsertCrawlQualityResult(ctx context.Context, result storage.CrawlQualityResult) error
	GetCrawlQualityResult(ctx context.Context, sessionID string) (*storage.CrawlQualityResult, error)
	CrawlQualityResultsForSessions(ctx context.Context, sessionIDs []string) (map[string]storage.CrawlQualityResult, error)
	LatestTrustedFullCrawlSession(ctx context.Context, projectID, excludeSessionID string) (*storage.CrawlSession, error)
	CrawlQualityMetrics(ctx context.Context, sessionID string, topN int) (*storage.CrawlQualityMetrics, error)
	TopPageRankURLs(ctx context.Context, sessionID string, limit int) ([]string, error)
	CanaryPageCheck(ctx context.Context, sessionID, canaryURL string) (*storage.CanaryPageCheck, error)
}

func (s *Server) qualityStore() (qualityStorage, bool) {
	qs, ok := s.store.(qualityStorage)
	return qs, ok
}

func (s *Server) handleProjectQualitySettings(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !requireProjectAccess(w, r, projectID) {
		return
	}
	settings, err := s.keyStore.GetProjectQualitySettings(projectID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, settings)
}

func (s *Server) handleUpdateProjectQualitySettings(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAccess(w, r) {
		return
	}
	projectID := r.PathValue("id")
	if !requireProjectAccess(w, r, projectID) {
		return
	}
	var body apikeys.ProjectQualitySettings
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	body.ProjectID = projectID
	settings, err := s.keyStore.SaveProjectQualitySettings(body)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, settings)
}

func (s *Server) handleProjectCanaries(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !requireProjectAccess(w, r, projectID) {
		return
	}
	canaries, err := s.keyStore.ListProjectCanaries(projectID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, canaries)
}

func (s *Server) handleCreateProjectCanary(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAccess(w, r) {
		return
	}
	projectID := r.PathValue("id")
	if !requireProjectAccess(w, r, projectID) {
		return
	}
	var body apikeys.ProjectCanary
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	body.ProjectID = projectID
	canary, err := s.keyStore.SaveProjectCanary(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, canary)
}

func (s *Server) handleUpdateProjectCanary(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAccess(w, r) {
		return
	}
	projectID := r.PathValue("id")
	if !requireProjectAccess(w, r, projectID) {
		return
	}
	var body apikeys.ProjectCanary
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	body.ID = r.PathValue("canaryId")
	body.ProjectID = projectID
	canary, err := s.keyStore.SaveProjectCanary(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, canary)
}

func (s *Server) handleDeleteProjectCanary(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAccess(w, r) {
		return
	}
	projectID := r.PathValue("id")
	if !requireProjectAccess(w, r, projectID) {
		return
	}
	if err := s.keyStore.DeleteProjectCanary(projectID, r.PathValue("canaryId")); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, map[string]any{"deleted": true})
}

func (s *Server) handleSessionQuality(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if !s.requireSessionAccess(w, r, sessionID) {
		return
	}
	qs, ok := s.qualityStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "quality storage unavailable")
		return
	}
	result, err := qs.GetCrawlQualityResult(r.Context(), sessionID)
	if err != nil {
		if isNotFoundErr(err) {
			writeError(w, http.StatusNotFound, "quality result not found")
			return
		}
		internalError(w, r, err)
		return
	}
	writeJSON(w, result)
}

func (s *Server) startQualityScheduler() {
	if s.keyStore == nil || s.store == nil {
		return
	}
	s.qualitySchedulerMu.Lock()
	if s.qualitySchedulerCancel != nil {
		s.qualitySchedulerMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.qualitySchedulerCancel = cancel
	s.qualitySchedulerMu.Unlock()
	go s.runQualityScheduler(ctx)
}

func (s *Server) stopQualityScheduler() {
	s.qualitySchedulerMu.Lock()
	defer s.qualitySchedulerMu.Unlock()
	if s.qualitySchedulerCancel != nil {
		s.qualitySchedulerCancel()
		s.qualitySchedulerCancel = nil
	}
}

func (s *Server) runQualityScheduler(ctx context.Context) {
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.evaluateMissingQuality(ctx, 20)
			timer.Reset(time.Minute)
		}
	}
}

func (s *Server) evaluateMissingQuality(ctx context.Context, limit int) {
	qs, ok := s.qualityStore()
	if !ok {
		return
	}
	sessions, err := s.store.ListSessions(ctx)
	if err != nil {
		return
	}
	sessionIDs := make([]string, 0, len(sessions))
	for _, sess := range sessions {
		if sess.ProjectID == nil || !qualityEvaluableStatus(sess.Status) {
			continue
		}
		sessionIDs = append(sessionIDs, sess.ID)
	}
	existing, err := qs.CrawlQualityResultsForSessions(ctx, sessionIDs)
	if err != nil {
		return
	}
	done := 0
	for _, sess := range sessions {
		if done >= limit || ctx.Err() != nil {
			return
		}
		if sess.ProjectID == nil || !qualityEvaluableStatus(sess.Status) {
			continue
		}
		if _, ok := existing[sess.ID]; ok {
			continue
		}
		if _, err := s.evaluateSessionQuality(ctx, sess); err != nil {
			applog.Warnf("server", "quality evaluation failed for session %s: %v", sess.ID, err)
		}
		done++
	}
}

func qualityEvaluableStatus(status string) bool {
	return status == "completed" || status == "completed_with_errors"
}

func (s *Server) evaluateSessionQuality(ctx context.Context, sess storage.CrawlSession) (*storage.CrawlQualityResult, error) {
	qs, ok := s.qualityStore()
	if !ok {
		return nil, fmt.Errorf("quality storage unavailable")
	}
	if sess.ProjectID == nil || *sess.ProjectID == "" {
		return nil, fmt.Errorf("session has no project")
	}
	projectID := *sess.ProjectID
	settings, err := s.keyStore.GetProjectQualitySettings(projectID)
	if err != nil {
		return nil, err
	}
	isFull := isFullCrawlSession(sess)
	now := time.Now().UTC()
	result := storage.CrawlQualityResult{
		SessionID:   sess.ID,
		ProjectID:   projectID,
		Status:      "trusted",
		Score:       100,
		Trusted:     true,
		IsFullCrawl: isFull,
		Summary:     "Crawl data is trusted.",
		EvaluatedAt: now,
		Metrics:     map[string]interface{}{},
	}
	if !settings.Enabled {
		result.Status = "warning"
		result.Score = 100
		result.Trusted = true
		result.Summary = "Quality gate is disabled for this project."
		result.Findings = append(result.Findings, qualityFinding(sess.ID, projectID, "info", "quality_disabled", "Quality gate is disabled for this project.", "", 0, 0, 0, false, now))
		return &result, qs.UpsertCrawlQualityResult(ctx, result)
	}
	if !isFull {
		result.Status = "warning"
		result.Score = 70
		result.Trusted = false
		result.Summary = "Daily Delta or partial crawl: not eligible as a trusted full-crawl baseline."
		result.Findings = append(result.Findings, qualityFinding(sess.ID, projectID, "warning", "partial_crawl", result.Summary, "", 0, 0, 0, true, now))
		return &result, qs.UpsertCrawlQualityResult(ctx, result)
	}

	current, err := qs.CrawlQualityMetrics(ctx, sess.ID, settings.PageRankTopN)
	if err != nil {
		return nil, err
	}
	result.Metrics["html_pages"] = current.HTMLPages
	result.Metrics["internal_links"] = current.InternalLinks
	result.Metrics["status_404"] = current.Status404
	result.Metrics["noindex"] = current.Noindex
	result.Metrics["redirects"] = current.Redirects
	result.Metrics["canonical_mismatch"] = current.CanonicalMismatch
	result.Metrics["pagerank_zero_top_pages"] = current.PageRankZeroTopPages

	canaries, _ := s.keyStore.ListProjectCanaries(projectID)
	result.Findings = append(result.Findings, s.evaluateCanaries(ctx, qs, sess.ID, projectID, canaries, now)...)

	baseline, err := qs.LatestTrustedFullCrawlSession(ctx, projectID, sess.ID)
	if err != nil {
		if !isNotFoundErr(err) {
			return nil, err
		}
		result.Findings = append(result.Findings, qualityFinding(sess.ID, projectID, "info", "no_trusted_baseline", "No trusted full-crawl baseline exists yet; this session can become the initial baseline if canaries pass.", "", 0, 0, 0, false, now))
	} else {
		result.BaselineSessionID = baseline.ID
		baselineMetrics, err := qs.CrawlQualityMetrics(ctx, baseline.ID, settings.PageRankTopN)
		if err != nil {
			return nil, err
		}
		result.Findings = append(result.Findings, compareQualityMetrics(sess.ID, projectID, current, baselineMetrics, *settings, now)...)
		result.Findings = append(result.Findings, s.evaluatePageRankOverlap(ctx, qs, sess.ID, projectID, baseline.ID, *settings, now)...)
		if crawlConfigSignature(sess.Config) != crawlConfigSignature(baseline.Config) {
			result.Findings = append(result.Findings, qualityFinding(sess.ID, projectID, "warning", "crawl_config_changed", "Crawl configuration differs from the trusted baseline; compare coverage with caution.", "", 0, 0, 0, false, now))
		}
	}

	score := 100
	blocking := false
	for _, finding := range result.Findings {
		switch finding.Severity {
		case "error":
			score -= 25
		case "warning":
			score -= 10
		}
		if finding.Blocking {
			blocking = true
		}
	}
	if score < 0 {
		score = 0
	}
	result.Score = uint8(score)
	switch {
	case blocking || score < settings.UntrustedScoreBelow:
		result.Status = "untrusted"
		result.Trusted = false
		result.Summary = "Crawl Observer data stale/untrusted: blocking data-quality findings were detected."
	case score < settings.MinTrustedScore:
		result.Status = "warning"
		result.Trusted = true
		result.Summary = "Crawl data is usable with warnings."
	default:
		result.Status = "trusted"
		result.Trusted = true
		result.Summary = "Crawl data is trusted."
	}
	if err := qs.UpsertCrawlQualityResult(ctx, result); err != nil {
		return nil, err
	}
	return &result, nil
}

func isFullCrawlSession(sess storage.CrawlSession) bool {
	label := strings.ToLower(strings.TrimSpace(sess.Label))
	return !strings.Contains(label, "daily delta")
}

func compareQualityMetrics(sessionID, projectID string, current, baseline *storage.CrawlQualityMetrics, settings apikeys.ProjectQualitySettings, now time.Time) []storage.CrawlQualityFinding {
	var findings []storage.CrawlQualityFinding
	findings = append(findings, dropFinding(sessionID, projectID, "coverage_drop", "HTML page coverage dropped sharply.", "html_pages", float64(current.HTMLPages), float64(baseline.HTMLPages), settings.CoverageDropPercent, float64(settings.CoverageMinPagesDelta), true, now)...)
	findings = append(findings, growthFinding(sessionID, projectID, "coverage_growth", "HTML page coverage grew sharply.", "html_pages", float64(current.HTMLPages), float64(baseline.HTMLPages), settings.CoverageGrowthPercent, float64(settings.CoverageMinPagesDelta), false, now)...)
	findings = append(findings, dropFinding(sessionID, projectID, "internal_links_drop", "Internal link count dropped sharply.", "internal_links", float64(current.InternalLinks), float64(baseline.InternalLinks), settings.InternalLinksDropPercent, float64(settings.InternalLinksMinDelta), true, now)...)
	findings = append(findings, growthFinding(sessionID, projectID, "status_404_growth", "404 pages increased sharply.", "status_404", float64(current.Status404), float64(baseline.Status404), settings.Status404Percent, float64(settings.Status404MinDelta), false, now)...)
	findings = append(findings, growthFinding(sessionID, projectID, "noindex_growth", "Noindex pages increased sharply.", "noindex", float64(current.Noindex), float64(baseline.Noindex), settings.NoindexPercent, float64(settings.NoindexMinDelta), false, now)...)
	findings = append(findings, growthFinding(sessionID, projectID, "redirect_growth", "Redirect pages increased sharply.", "redirects", float64(current.Redirects), float64(baseline.Redirects), settings.RedirectPercent, float64(settings.RedirectMinDelta), false, now)...)
	findings = append(findings, growthFinding(sessionID, projectID, "canonical_mismatch_growth", "Canonical mismatch pages increased sharply.", "canonical_mismatch", float64(current.CanonicalMismatch), float64(baseline.CanonicalMismatch), settings.CanonicalMismatchPercent, float64(settings.CanonicalMismatchMinDelta), false, now)...)
	if int(current.PageRankZeroTopPages) > settings.PageRankZeroTopPagesMax {
		findings = append(findings, qualityFinding(sessionID, projectID, "error", "pagerank_zero_top_pages", "Too many top PageRank pages have zero PageRank.", "pagerank_zero_top_pages", float64(current.PageRankZeroTopPages), 0, float64(settings.PageRankZeroTopPagesMax), true, now))
	}
	return findings
}

func dropFinding(sessionID, projectID, typ, msg, metric string, current, baseline, percent, minDelta float64, blocking bool, now time.Time) []storage.CrawlQualityFinding {
	if baseline <= 0 {
		return nil
	}
	delta := baseline - current
	if delta < minDelta {
		return nil
	}
	dropPercent := delta / baseline * 100
	if dropPercent >= percent {
		return []storage.CrawlQualityFinding{qualityFinding(sessionID, projectID, "error", typ, msg, metric, current, baseline, percent, blocking, now)}
	}
	return nil
}

func growthFinding(sessionID, projectID, typ, msg, metric string, current, baseline, percent, minDelta float64, blocking bool, now time.Time) []storage.CrawlQualityFinding {
	delta := current - baseline
	if delta < minDelta {
		return nil
	}
	base := math.Max(baseline, 1)
	growthPercent := delta / base * 100
	if growthPercent >= percent {
		severity := "warning"
		if blocking {
			severity = "error"
		}
		return []storage.CrawlQualityFinding{qualityFinding(sessionID, projectID, severity, typ, msg, metric, current, baseline, percent, blocking, now)}
	}
	return nil
}

func (s *Server) evaluatePageRankOverlap(ctx context.Context, qs qualityStorage, sessionID, projectID, baselineID string, settings apikeys.ProjectQualitySettings, now time.Time) []storage.CrawlQualityFinding {
	current, err := qs.TopPageRankURLs(ctx, sessionID, settings.PageRankTopN)
	if err != nil || len(current) == 0 {
		return nil
	}
	baseline, err := qs.TopPageRankURLs(ctx, baselineID, settings.PageRankTopN)
	if err != nil || len(baseline) == 0 {
		return nil
	}
	set := map[string]struct{}{}
	for _, u := range baseline {
		set[u] = struct{}{}
	}
	overlap := 0
	for _, u := range current {
		if _, ok := set[u]; ok {
			overlap++
		}
	}
	overlapPercent := float64(overlap) / float64(min(len(current), len(baseline))) * 100
	if overlapPercent < settings.PageRankTopOverlapMinPercent {
		return []storage.CrawlQualityFinding{qualityFinding(sessionID, projectID, "warning", "pagerank_top_overlap_low", "Top PageRank pages changed sharply compared with the trusted baseline.", "pagerank_top_overlap_percent", overlapPercent, 100, settings.PageRankTopOverlapMinPercent, false, now)}
	}
	return nil
}

func (s *Server) evaluateCanaries(ctx context.Context, qs qualityStorage, sessionID, projectID string, canaries []apikeys.ProjectCanary, now time.Time) []storage.CrawlQualityFinding {
	var findings []storage.CrawlQualityFinding
	for _, canary := range canaries {
		if !canary.Active {
			continue
		}
		check, err := qs.CanaryPageCheck(ctx, sessionID, canary.URL)
		if err != nil || check == nil || !check.Found {
			findings = append(findings, qualityFinding(sessionID, projectID, "error", "canary_missing", "Canary page is missing from this crawl: "+canary.URL, "canary", 0, 1, 1, true, now))
			continue
		}
		if canary.ExpectedStatus > 0 && int(check.StatusCode) != canary.ExpectedStatus {
			findings = append(findings, qualityFinding(sessionID, projectID, "error", "canary_status_mismatch", "Canary page returned unexpected status: "+canary.URL, "status_code", float64(check.StatusCode), float64(canary.ExpectedStatus), float64(canary.ExpectedStatus), true, now))
		}
		if canary.ExpectedFinalURL != "" && check.FinalURL != canary.ExpectedFinalURL {
			findings = append(findings, qualityFinding(sessionID, projectID, "error", "canary_final_url_mismatch", "Canary final URL changed: "+canary.URL, "final_url", 0, 0, 0, true, now))
		}
		if canary.ExpectedCanonical != "" && check.Canonical != canary.ExpectedCanonical {
			findings = append(findings, qualityFinding(sessionID, projectID, "error", "canary_canonical_mismatch", "Canary canonical changed: "+canary.URL, "canonical", 0, 0, 0, true, now))
		}
		if canary.TitleContains != "" && !strings.Contains(strings.ToLower(check.Title), strings.ToLower(canary.TitleContains)) {
			findings = append(findings, qualityFinding(sessionID, projectID, "warning", "canary_title_changed", "Canary title does not contain expected text: "+canary.URL, "title", 0, 0, 0, false, now))
		}
		if int(check.InternalLinksOut) < canary.MinInternalLinks {
			findings = append(findings, qualityFinding(sessionID, projectID, "error", "canary_internal_links_low", "Canary has too few outgoing internal links: "+canary.URL, "internal_links_out", float64(check.InternalLinksOut), 0, float64(canary.MinInternalLinks), true, now))
		}
		if canary.ExpectIndexable && !check.IsIndexable {
			findings = append(findings, qualityFinding(sessionID, projectID, "error", "canary_not_indexable", "Canary is not indexable: "+canary.URL, "is_indexable", 0, 1, 1, true, now))
		}
	}
	return findings
}

func qualityFinding(sessionID, projectID, severity, typ, message, metric string, current, baseline, threshold float64, blocking bool, now time.Time) storage.CrawlQualityFinding {
	return storage.CrawlQualityFinding{
		SessionID:      sessionID,
		ProjectID:      projectID,
		Severity:       severity,
		FindingType:    typ,
		Message:        message,
		Metric:         metric,
		CurrentValue:   current,
		BaselineValue:  baseline,
		ThresholdValue: threshold,
		Blocking:       blocking,
		CreatedAt:      now,
	}
}

func crawlConfigSignature(configJSON string) string {
	var raw map[string]any
	if err := json.Unmarshal([]byte(configJSON), &raw); err != nil {
		return ""
	}
	crawler, _ := raw["Crawler"].(map[string]any)
	keys := []string{"CrawlScope", "MaxPages", "MaxDepth", "StoreHTML", "UserAgent", "CrawlSitemapOnly", "FetchSitemaps", "IgnoreRobots"}
	var b strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&b, "%s=%v;", key, crawler[key])
	}
	return b.String()
}

func isNotFoundErr(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || strings.Contains(strings.ToLower(err.Error()), "no rows")
}
