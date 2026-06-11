package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/SEObserver/crawlobserver/internal/apikeys"
	"github.com/SEObserver/crawlobserver/internal/config"
	"github.com/SEObserver/crawlobserver/internal/crawler"
	"github.com/SEObserver/crawlobserver/internal/normalizer"
	"github.com/SEObserver/crawlobserver/internal/storage"
	"golang.org/x/net/publicsuffix"
)

type deltaPreview struct {
	ProjectID         string         `json:"project_id"`
	BaselineSessionID string         `json:"baseline_session_id"`
	TotalCandidates   int            `json:"total_candidates"`
	LaunchLimit       int            `json:"launch_limit"`
	WillLaunch        int            `json:"will_launch"`
	Deferred          int            `json:"deferred"`
	BySource          map[string]int `json:"by_source"`
	SampleURLs        []string       `json:"sample_urls"`
}

type deltaCandidateResult struct {
	settings *apikeys.ProjectDeltaSettings
	baseline *storage.CrawlSession
	urls     []string
	manual   []string
	preview  deltaPreview
}

func (s *Server) handleProjectDeltaSettings(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !requireProjectAccess(w, r, projectID) {
		return
	}
	settings, err := s.keyStore.GetProjectDeltaSettings(projectID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, settings)
}

func (s *Server) handleUpdateProjectDeltaSettings(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAccess(w, r) {
		return
	}
	projectID := r.PathValue("id")
	if !requireProjectAccess(w, r, projectID) {
		return
	}
	var body apikeys.ProjectDeltaSettings
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	body.ProjectID = projectID
	settings, err := s.keyStore.SaveProjectDeltaSettings(body)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, settings)
}

func (s *Server) handleProjectDeltaManualQueue(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAccess(w, r) {
		return
	}
	projectID := r.PathValue("id")
	if !requireProjectAccess(w, r, projectID) {
		return
	}
	var body struct {
		URLs []string `json:"urls"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	added, err := s.keyStore.AddProjectDeltaManualURLs(projectID, body.URLs)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, map[string]interface{}{"added": added})
}

func (s *Server) handleProjectDeltaPreview(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !requireProjectAccess(w, r, projectID) {
		return
	}
	result, err := s.buildDeltaCandidates(r.Context(), projectID)
	if err != nil {
		if strings.Contains(err.Error(), "no baseline session") {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		internalError(w, r, err)
		return
	}
	writeJSON(w, result.preview)
}

func (s *Server) handleProjectDeltaRun(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAccess(w, r) {
		return
	}
	projectID := r.PathValue("id")
	if !requireProjectAccess(w, r, projectID) {
		return
	}

	result, err := s.buildDeltaCandidates(r.Context(), projectID)
	if err != nil {
		if strings.Contains(err.Error(), "no baseline session") || strings.Contains(err.Error(), "no delta candidates") {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		internalError(w, r, err)
		return
	}
	if result.preview.WillLaunch == 0 {
		writeError(w, http.StatusBadRequest, "no delta candidates to crawl")
		return
	}
	if result.settings.PauseDeltaWhenFullCrawlRunning && s.projectHasRunningSession(r.Context(), projectID) {
		writeError(w, http.StatusConflict, "project has a running or queued crawl session")
		return
	}

	req, err := s.deltaCrawlRequest(result)
	if err != nil {
		internalError(w, r, err)
		return
	}
	sessionID, err := s.manager.StartCrawl(req)
	if err != nil {
		internalError(w, r, err)
		return
	}
	now := time.Now().UTC()
	if err := s.keyStore.MarkProjectDeltaRun(projectID, sessionID, now); err != nil {
		internalError(w, r, err)
		return
	}
	if len(result.manual) > 0 {
		_ = s.keyStore.MarkProjectDeltaManualURLsConsumed(projectID, result.manual, now)
	}
	writeJSON(w, map[string]interface{}{
		"session_id": sessionID,
		"preview":    result.preview,
	})
}

func (s *Server) startDeltaScheduler() {
	if s.keyStore == nil || s.store == nil || s.manager == nil {
		return
	}
	s.deltaSchedulerMu.Lock()
	if s.deltaSchedulerCancel != nil {
		s.deltaSchedulerMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.deltaSchedulerCancel = cancel
	s.deltaSchedulerMu.Unlock()

	go s.runDeltaScheduler(ctx)
}

func (s *Server) stopDeltaScheduler() {
	s.deltaSchedulerMu.Lock()
	defer s.deltaSchedulerMu.Unlock()
	if s.deltaSchedulerCancel != nil {
		s.deltaSchedulerCancel()
		s.deltaSchedulerCancel = nil
	}
}

func (s *Server) runDeltaScheduler(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runDueDeltaProjects(ctx)
		}
	}
}

func (s *Server) runDueDeltaProjects(ctx context.Context) {
	settings, err := s.keyStore.ListEnabledProjectDeltaSettings()
	if err != nil {
		return
	}
	for _, st := range settings {
		if ctx.Err() != nil {
			return
		}
		if !deltaScheduleDue(st, time.Now()) {
			continue
		}
		if st.PauseDeltaWhenFullCrawlRunning && s.projectHasRunningSession(ctx, st.ProjectID) {
			continue
		}
		result, err := s.buildDeltaCandidates(ctx, st.ProjectID)
		if err != nil || len(result.urls) == 0 {
			continue
		}
		req, err := s.deltaCrawlRequest(result)
		if err != nil {
			continue
		}
		sessionID, err := s.manager.StartCrawl(req)
		if err != nil {
			continue
		}
		now := time.Now().UTC()
		_ = s.keyStore.MarkProjectDeltaRun(st.ProjectID, sessionID, now)
		if len(result.manual) > 0 {
			_ = s.keyStore.MarkProjectDeltaManualURLsConsumed(st.ProjectID, result.manual, now)
		}
	}
}

func deltaScheduleDue(settings apikeys.ProjectDeltaSettings, now time.Time) bool {
	loc := time.UTC
	if settings.Timezone != "" {
		if loaded, err := time.LoadLocation(settings.Timezone); err == nil {
			loc = loaded
		}
	}
	localNow := now.In(loc)
	if settings.LastRunAt != nil {
		last := settings.LastRunAt.In(loc)
		if last.Year() == localNow.Year() && last.YearDay() == localNow.YearDay() {
			return false
		}
	}
	hour, minute := parseScheduleTime(settings.ScheduleTime)
	scheduled := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), hour, minute, 0, 0, loc)
	return !localNow.Before(scheduled)
}

func parseScheduleTime(value string) (int, int) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return 3, 0
	}
	hour, errH := strconv.Atoi(parts[0])
	minute, errM := strconv.Atoi(parts[1])
	if errH != nil || errM != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 3, 0
	}
	return hour, minute
}

func (s *Server) buildDeltaCandidates(ctx context.Context, projectID string) (*deltaCandidateResult, error) {
	settings, err := s.keyStore.GetProjectDeltaSettings(projectID)
	if err != nil {
		return nil, err
	}
	baseline, err := s.store.LatestProjectSession(ctx, projectID)
	if err != nil {
		if strings.Contains(err.Error(), sql.ErrNoRows.Error()) {
			return nil, fmt.Errorf("no baseline session found for project")
		}
		return nil, err
	}

	bySource := map[string]int{}
	candidates := make([]string, 0)
	manualRaw := []string{}
	addSource := func(source string, urls []string) {
		bySource[source] += len(urls)
		candidates = append(candidates, urls...)
	}

	perSourceLimit := max(1, settings.MaxCandidatesPerRun)
	if settings.SourceSitemap {
		urls, err := s.store.DeltaSitemapCandidateURLs(ctx, baseline.ID, perSourceLimit)
		if err != nil {
			return nil, err
		}
		addSource("sitemap", urls)
	}
	if settings.SourceGSC {
		urls, err := s.store.DeltaGSCCandidateURLs(ctx, projectID, perSourceLimit)
		if err != nil {
			return nil, err
		}
		addSource("gsc", urls)
	}
	if settings.SourceProblemPages {
		urls, err := s.store.DeltaProblemPageURLs(ctx, baseline.ID, settings.MaxChangedPagesPerRun)
		if err != nil {
			return nil, err
		}
		addSource("problem_pages", urls)
	}
	if settings.SourceStalePages {
		staleBefore := time.Now().UTC().AddDate(0, 0, -settings.StaleAfterDays)
		urls, err := s.store.DeltaStalePageURLs(ctx, baseline.ID, staleBefore, settings.MaxChangedPagesPerRun)
		if err != nil {
			return nil, err
		}
		addSource("stale_pages", urls)
	}
	if settings.SourceManualQueue {
		urls, err := s.keyStore.ListProjectDeltaManualURLs(projectID, perSourceLimit)
		if err != nil {
			return nil, err
		}
		manualRaw = append(manualRaw, urls...)
		addSource("manual_queue", urls)
	}

	scope := baselineCrawlScope(baseline)
	filteredAll := filterDeltaURLs(candidates, baseline.SeedURLs, scope, settings)
	knownSet, err := s.deltaKnownURLSet(ctx, baseline.ID, settings)
	if err != nil {
		return nil, err
	}
	filtered, deferred := boundDeltaCandidates(filteredAll, knownSet, settings)
	manual := launchedManualURLs(manualRaw, filtered, settings)
	launchLimit := len(filtered) + settings.MaxDiscoveredPagesPerRun
	if launchLimit <= 0 {
		launchLimit = len(filtered)
	}
	sample := filtered
	if len(sample) > 20 {
		sample = sample[:20]
	}
	preview := deltaPreview{
		ProjectID:         projectID,
		BaselineSessionID: baseline.ID,
		TotalCandidates:   len(filteredAll),
		LaunchLimit:       launchLimit,
		WillLaunch:        len(filtered),
		Deferred:          deferred,
		BySource:          bySource,
		SampleURLs:        sample,
	}
	return &deltaCandidateResult{
		settings: settings,
		baseline: baseline,
		urls:     filtered,
		manual:   manual,
		preview:  preview,
	}, nil
}

func (s *Server) deltaCrawlRequest(result *deltaCandidateResult) (crawler.CrawlRequest, error) {
	cfg := *s.cfg
	if result.baseline.Config != "" {
		var saved config.Config
		if err := json.Unmarshal([]byte(result.baseline.Config), &saved); err == nil {
			cloudflareAPIKey := cfg.Crawler.Cloudflare.APIKey
			cfg.Crawler = saved.Crawler
			cfg.Crawler.Cloudflare.APIKey = cloudflareAPIKey
		}
	}
	delay := cfg.Crawler.Delay
	if result.settings.RateLimitRequestsPerSecond > 0 {
		delay = time.Duration(float64(time.Second) / result.settings.RateLimitRequestsPerSecond)
	}
	maxPages := result.preview.LaunchLimit
	if maxPages <= 0 {
		maxPages = len(result.urls)
	}
	projectID := result.settings.ProjectID
	checkExternal := false
	checkResources := false
	retries := result.settings.RetryCount
	req := crawler.CrawlRequest{
		Seeds:               result.urls,
		SessionSeedURLs:     append([]string(nil), result.baseline.SeedURLs...),
		MaxPages:            maxPages,
		MaxDepth:            result.settings.MaxDiscoveryDepth,
		Workers:             cfg.Crawler.Workers,
		Delay:               delay.String(),
		StoreHTML:           cfg.Crawler.StoreHTML,
		CrawlScope:          cfg.Crawler.CrawlScope,
		ProjectID:           &projectID,
		CheckExternalLinks:  &checkExternal,
		ExternalLinkWorkers: cfg.Crawler.ExternalLinkWorkers,
		UserAgent:           cfg.Crawler.UserAgent,
		FetchSitemaps:       boolPtr(false),
		CheckPageResources:  &checkResources,
		ResourceWorkers:     cfg.Crawler.ResourceWorkers,
		TLSProfile:          cfg.Crawler.TLSProfile,
		JSRenderMode:        cfg.Crawler.JSRender.Mode,
		JSRenderMaxPages:    cfg.Crawler.JSRender.MaxPages,
		JSRenderTimeout:     cfg.Crawler.JSRender.PageTimeout.String(),
		FollowJSLinks:       cfg.Crawler.FollowJSLinks,
		SourceIP:            cfg.Crawler.SourceIP,
		ForceIPv4:           cfg.Crawler.ForceIPv4,
		ExtractorSetID:      cfg.Crawler.ExtractorSetID,
		IgnoreRobots:        !result.settings.RespectRobotsTxt,
		ExcludePatterns:     append([]string{}, result.settings.BlockedURLPatterns...),
		MeasureCWV:          cfg.Crawler.MeasureCWV,
		Label:               "Daily Delta Crawl",
		RetryMaxRetries:     &retries,
		RetryBackoffSeconds: result.settings.RetryBackoffSeconds,
	}
	if result.settings.EnableJSRenderingForDelta == "off" ||
		result.settings.EnableJSRenderingForDelta == "auto" ||
		result.settings.EnableJSRenderingForDelta == "always" {
		req.JSRenderMode = result.settings.EnableJSRenderingForDelta
	}
	return req, nil
}

func (s *Server) deltaKnownURLSet(ctx context.Context, sessionID string, settings *apikeys.ProjectDeltaSettings) (map[string]struct{}, error) {
	limit := max(50000, settings.MaxCandidatesPerRun*20)
	urls, err := s.store.DeltaKnownPageURLs(ctx, sessionID, limit)
	if err != nil {
		return nil, err
	}
	known := make(map[string]struct{}, len(urls))
	for _, raw := range urls {
		norm, err := normalizeDeltaURL(raw, settings)
		if err != nil || norm == "" {
			continue
		}
		known[norm] = struct{}{}
	}
	return known, nil
}

func boundDeltaCandidates(candidates []string, known map[string]struct{}, settings *apikeys.ProjectDeltaSettings) ([]string, int) {
	changedLimit := max(0, settings.MaxChangedPagesPerRun)
	newLimit := max(0, settings.MaxNewPagesPerRun)
	totalLimit := max(1, settings.MaxCandidatesPerRun)
	changedCount := 0
	newCount := 0
	deferred := 0
	out := make([]string, 0, min(len(candidates), totalLimit))
	for _, u := range candidates {
		_, exists := known[u]
		if exists {
			if changedCount >= changedLimit {
				deferred++
				continue
			}
			changedCount++
		} else {
			if newCount >= newLimit {
				deferred++
				continue
			}
			newCount++
		}
		if len(out) >= totalLimit {
			deferred++
			continue
		}
		out = append(out, u)
	}
	return out, deferred
}

func launchedManualURLs(manualRaw, launched []string, settings *apikeys.ProjectDeltaSettings) []string {
	if len(manualRaw) == 0 || len(launched) == 0 {
		return []string{}
	}
	launchedSet := make(map[string]struct{}, len(launched))
	for _, u := range launched {
		launchedSet[u] = struct{}{}
	}
	seen := map[string]struct{}{}
	var out []string
	for _, raw := range manualRaw {
		norm, err := normalizeDeltaURL(raw, settings)
		if err != nil || norm == "" {
			continue
		}
		if _, ok := launchedSet[norm]; !ok {
			continue
		}
		if _, ok := seen[norm]; ok {
			continue
		}
		seen[norm] = struct{}{}
		out = append(out, norm)
	}
	return out
}

func (s *Server) projectHasRunningSession(ctx context.Context, projectID string) bool {
	sessions, err := s.store.ListSessions(ctx, projectID)
	if err != nil {
		return false
	}
	for _, sess := range sessions {
		if s.manager.IsRunning(sess.ID) || s.manager.IsQueued(sess.ID) {
			return true
		}
	}
	return false
}

func baselineCrawlScope(sess *storage.CrawlSession) string {
	if sess == nil || sess.Config == "" {
		return "host"
	}
	var cfg config.Config
	if err := json.Unmarshal([]byte(sess.Config), &cfg); err != nil {
		return "host"
	}
	if cfg.Crawler.CrawlScope == "" {
		return "host"
	}
	return cfg.Crawler.CrawlScope
}

func filterDeltaURLs(raw []string, seedURLs []string, scope string, settings *apikeys.ProjectDeltaSettings) []string {
	seen := make(map[string]struct{}, len(raw))
	var out []string
	for _, candidate := range raw {
		norm, err := normalizeDeltaURL(candidate, settings)
		if err != nil || norm == "" {
			continue
		}
		if !deltaURLAllowedByPatterns(norm, settings) || !deltaURLInScope(norm, seedURLs, scope) {
			continue
		}
		if _, ok := seen[norm]; ok {
			continue
		}
		seen[norm] = struct{}{}
		out = append(out, norm)
	}
	return out
}

func normalizeDeltaURL(raw string, settings *apikeys.ProjectDeltaSettings) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if settings.StripFragments {
		if u, err := url.Parse(raw); err == nil {
			u.Fragment = ""
			raw = u.String()
		}
	}
	if settings.StripTrackingParams {
		return normalizer.Normalize(raw)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if settings.NormalizeTrailingSlash && u.Host != "" && u.Path == "" {
		u.Path = "/"
	}
	return u.String(), nil
}

func deltaURLAllowedByPatterns(candidate string, settings *apikeys.ProjectDeltaSettings) bool {
	for _, pat := range settings.BlockedURLPatterns {
		pat = strings.TrimSpace(pat)
		if pat != "" && strings.Contains(candidate, pat) {
			return false
		}
	}
	if len(settings.AllowedURLPatterns) == 0 {
		return true
	}
	for _, pat := range settings.AllowedURLPatterns {
		pat = strings.TrimSpace(pat)
		if pat != "" && strings.Contains(candidate, pat) {
			return true
		}
	}
	return false
}

func deltaURLInScope(candidate string, seedURLs []string, scope string) bool {
	u, err := url.Parse(candidate)
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	domain, _ := publicsuffix.EffectiveTLDPlusOne(host)
	candidateLower := strings.ToLower(candidate)
	for _, seed := range seedURLs {
		su, err := url.Parse(seed)
		if err != nil || su.Hostname() == "" {
			continue
		}
		seedHost := strings.ToLower(su.Hostname())
		switch scope {
		case "domain":
			seedDomain, _ := publicsuffix.EffectiveTLDPlusOne(seedHost)
			if domain != "" && seedDomain != "" && domain == seedDomain {
				return true
			}
		case "subdirectory":
			prefix := strings.ToLower(su.Scheme) + "://" + strings.ToLower(su.Host) + deltaSubdirectoryPrefix(su.Path)
			if strings.HasPrefix(candidateLower, prefix) {
				return true
			}
		default:
			if host == seedHost {
				return true
			}
		}
	}
	return false
}

func deltaSubdirectoryPrefix(p string) string {
	if p == "" || p == "." {
		return "/"
	}
	if strings.HasSuffix(p, "/") {
		return p
	}
	idx := strings.LastIndex(p, "/")
	if idx < 0 {
		return "/"
	}
	prefix := p[:idx+1]
	if prefix == "" {
		return "/"
	}
	return prefix
}

func boolPtr(v bool) *bool {
	return &v
}
