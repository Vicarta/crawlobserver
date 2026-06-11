package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/SEObserver/crawlobserver/internal/applog"
	"github.com/SEObserver/crawlobserver/internal/config"
	"github.com/SEObserver/crawlobserver/internal/extraction"
	"github.com/SEObserver/crawlobserver/internal/normalizer"
	"github.com/SEObserver/crawlobserver/internal/storage"
	"github.com/SEObserver/crawlobserver/internal/telemetry"
	"github.com/posthog/posthog-go"
)

const (
	defaultExternalWorkers = 3
	defaultResourceWorkers = 3
	maxLastErrors          = 1000
	defaultMaxPages        = 100000
	maxRescanURLs          = 200
)

func applySavedCrawlerConfig(cfg *config.Config, saved config.CrawlerConfig) {
	cloudflareAPIKey := cfg.Crawler.Cloudflare.APIKey
	cfg.Crawler = saved
	cfg.Crawler.Cloudflare.APIKey = cloudflareAPIKey
}

func boolPtr(v bool) *bool {
	return &v
}

func boolValue(v *bool, fallback bool) bool {
	if v == nil {
		return fallback
	}
	return *v
}

// queuedCrawl holds a crawl waiting for a semaphore slot.
type queuedCrawl struct {
	sessionID string
	engine    *Engine
	seeds     []string
}

// ExtractorSetLoader loads an extractor set by ID.
type ExtractorSetLoader interface {
	GetExtractorSet(id string) (*extraction.ExtractorSet, error)
}

// Manager manages running crawl engines.
type Manager struct {
	mu              sync.RWMutex
	engines         map[string]*Engine // sessionID -> engine
	lastErrors      map[string]string  // sessionID -> error message (persists after engine cleanup)
	cfg             *config.Config
	store           *storage.Store
	extractorLoader ExtractorSetLoader

	sem       chan struct{} // semaphore limiting concurrent sessions
	queueMu   sync.Mutex
	queue     []queuedCrawl   // FIFO of crawls waiting for a slot
	queuedSet map[string]bool // sessionID -> true for fast lookup
}

// NewManager creates a new crawl manager.
func NewManager(cfg *config.Config, store *storage.Store, extractorLoader ...ExtractorSetLoader) *Manager {
	maxSessions := cfg.Crawler.MaxConcurrentSessions
	if maxSessions <= 0 {
		maxSessions = 20
	}
	m := &Manager{
		engines:    make(map[string]*Engine),
		lastErrors: make(map[string]string),
		cfg:        cfg,
		store:      store,
		sem:        make(chan struct{}, maxSessions),
		queuedSet:  make(map[string]bool),
	}
	if len(extractorLoader) > 0 {
		m.extractorLoader = extractorLoader[0]
	}
	return m
}

// LastError returns the error message from the last run of a session, if any.
func (m *Manager) LastError(sessionID string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastErrors[sessionID]
}

// CrawlRequest holds parameters for starting a new crawl.
type CrawlRequest struct {
	Seeds               []string `json:"seeds"`
	SessionSeedURLs     []string `json:"session_seed_urls,omitempty"`
	MaxPages            int      `json:"max_pages"`
	MaxDepth            int      `json:"max_depth"`
	Workers             int      `json:"workers"`
	Delay               string   `json:"delay"`
	StoreHTML           bool     `json:"store_html"`
	CrawlScope          string   `json:"crawl_scope"`
	ProjectID           *string  `json:"project_id"`
	CheckExternalLinks  *bool    `json:"check_external_links"`
	ExternalLinkWorkers int      `json:"external_link_workers"`
	RetryStatusCode     int      `json:"retry_status_code"`
	UserAgent           string   `json:"user_agent"`
	CrawlSitemapOnly    bool     `json:"crawl_sitemap_only"`
	FetchSitemaps       *bool    `json:"fetch_sitemaps"`
	CheckPageResources  *bool    `json:"check_page_resources"`
	ResourceWorkers     int      `json:"resource_workers"`
	TLSProfile          string   `json:"tls_profile"`
	JSRenderMode        string   `json:"js_render_mode"`
	JSRenderMaxPages    int      `json:"js_render_max_pages"`
	JSRenderTimeout     string   `json:"js_render_timeout"`
	FollowJSLinks       bool     `json:"follow_js_links"`
	SourceIP            string   `json:"source_ip"`
	ForceIPv4           bool     `json:"force_ipv4"`
	ExtractorSetID      string   `json:"extractor_set_id"`
	IgnoreRobots        bool     `json:"ignore_robots"`
	ExcludePatterns     []string `json:"exclude_patterns"`
	MeasureCWV          bool     `json:"measure_cwv"`
	FullRecrawl         bool     `json:"full_recrawl"`
	Label               string   `json:"label"`
	RetryMaxRetries     *int     `json:"retry_max_retries"`
	RetryBackoffSeconds int      `json:"retry_backoff_seconds"`
}

// StartCrawl launches a new crawl session in background. Returns the session ID.
// If all semaphore slots are taken, the crawl is queued and starts automatically
// when a slot becomes available.
func (m *Manager) StartCrawl(req CrawlRequest) (string, error) {
	if len(req.Seeds) == 0 {
		return "", fmt.Errorf("at least one seed URL is required")
	}

	// Ensure all seeds have a scheme (e.g. "blog.axe-net.fr" → "http://blog.axe-net.fr")
	for i, s := range req.Seeds {
		req.Seeds[i] = normalizer.EnsureScheme(s)
	}

	// Build config overrides
	cfg := *m.cfg
	crawlerCfg := cfg.Crawler
	if req.MaxPages > 0 {
		crawlerCfg.MaxPages = req.MaxPages
	}
	if req.MaxDepth > 0 {
		crawlerCfg.MaxDepth = req.MaxDepth
	}
	if req.Workers > 0 {
		crawlerCfg.Workers = req.Workers
	}
	if req.Delay != "" {
		if d, err := time.ParseDuration(req.Delay); err == nil {
			crawlerCfg.Delay = d
		}
	}
	crawlerCfg.StoreHTML = req.StoreHTML
	if req.CrawlScope != "" {
		crawlerCfg.CrawlScope = req.CrawlScope
	}

	// Guardrails: cap workers to MaxWorkers
	maxWorkers := m.cfg.Crawler.MaxWorkers
	if maxWorkers <= 0 {
		maxWorkers = 100
	}
	if crawlerCfg.Workers > maxWorkers {
		crawlerCfg.Workers = maxWorkers
	}

	// Guardrails: default max_pages to avoid infinite crawl
	if crawlerCfg.MaxPages <= 0 {
		crawlerCfg.MaxPages = defaultMaxPages
	}

	cfg.Crawler = crawlerCfg

	if req.UserAgent != "" {
		cfg.Crawler.UserAgent = req.UserAgent
	}
	if req.TLSProfile != "" {
		cfg.Crawler.TLSProfile = req.TLSProfile
	}
	if req.SourceIP != "" {
		cfg.Crawler.SourceIP = req.SourceIP
	}
	if req.ForceIPv4 {
		cfg.Crawler.ForceIPv4 = true
	}
	if req.IgnoreRobots {
		cfg.Crawler.RespectRobots = false
	}
	if req.RetryMaxRetries != nil && *req.RetryMaxRetries >= 0 {
		cfg.Crawler.Retry.MaxRetries = *req.RetryMaxRetries
	}
	if req.RetryBackoffSeconds > 0 {
		cfg.Crawler.Retry.BaseDelay = time.Duration(req.RetryBackoffSeconds) * time.Second
	}
	if len(req.ExcludePatterns) > 0 {
		cfg.Crawler.ExcludePatterns = req.ExcludePatterns
	}
	cfg.Crawler.CheckExternalLinks = boolPtr(req.CheckExternalLinks == nil || *req.CheckExternalLinks)
	cfg.Crawler.ExternalLinkWorkers = req.ExternalLinkWorkers
	if cfg.Crawler.ExternalLinkWorkers <= 0 {
		cfg.Crawler.ExternalLinkWorkers = defaultExternalWorkers
	}
	cfg.Crawler.CrawlSitemapOnly = req.CrawlSitemapOnly
	cfg.Crawler.FetchSitemaps = boolPtr(req.FetchSitemaps == nil || *req.FetchSitemaps || req.CrawlSitemapOnly)
	cfg.Crawler.CheckPageResources = boolPtr(req.CheckPageResources == nil || *req.CheckPageResources)
	cfg.Crawler.ResourceWorkers = req.ResourceWorkers
	if cfg.Crawler.ResourceWorkers <= 0 {
		cfg.Crawler.ResourceWorkers = defaultResourceWorkers
	}
	cfg.Crawler.FollowJSLinks = req.FollowJSLinks
	cfg.Crawler.MeasureCWV = req.MeasureCWV
	cfg.Crawler.ExtractorSetID = req.ExtractorSetID

	// JS rendering overrides
	if req.JSRenderMode != "" {
		cfg.Crawler.JSRender.Mode = req.JSRenderMode
	}
	if req.JSRenderMaxPages > 0 {
		cfg.Crawler.JSRender.MaxPages = req.JSRenderMaxPages
	}
	if req.JSRenderTimeout != "" {
		if d, err := time.ParseDuration(req.JSRenderTimeout); err == nil {
			cfg.Crawler.JSRender.PageTimeout = d
		}
	}

	sessionSeeds := req.Seeds
	if len(req.SessionSeedURLs) > 0 {
		sessionSeeds = req.SessionSeedURLs
	}
	for i, s := range sessionSeeds {
		sessionSeeds[i] = normalizer.EnsureScheme(s)
	}

	engine := NewEngine(&cfg, m.store)
	sessionID := engine.SessionID(sessionSeeds)
	engine.session.ProjectID = req.ProjectID
	engine.session.Label = req.Label
	engine.sitemapOnly = cfg.Crawler.CrawlSitemapOnly
	// Fetch sitemaps: default true; forced true when sitemapOnly
	engine.fetchSitemaps = boolValue(cfg.Crawler.FetchSitemaps, true) || engine.sitemapOnly

	// External link checking: default true
	engine.checkExternal = boolValue(cfg.Crawler.CheckExternalLinks, true)
	engine.externalWorkers = cfg.Crawler.ExternalLinkWorkers
	if engine.externalWorkers <= 0 {
		engine.externalWorkers = defaultExternalWorkers
	}

	// Page resource checking: default true
	engine.checkResources = boolValue(cfg.Crawler.CheckPageResources, true)
	engine.resourceWorkers = cfg.Crawler.ResourceWorkers
	if engine.resourceWorkers <= 0 {
		engine.resourceWorkers = defaultResourceWorkers
	}

	// URL exclude patterns
	engine.excludePatterns = req.ExcludePatterns

	// JS rendering
	engine.followJSLinks = cfg.Crawler.FollowJSLinks
	engine.measureCWV = cfg.Crawler.MeasureCWV

	// Load extractors if requested
	if cfg.Crawler.ExtractorSetID != "" && m.extractorLoader != nil {
		es, err := m.extractorLoader.GetExtractorSet(cfg.Crawler.ExtractorSetID)
		if err != nil {
			return "", fmt.Errorf("loading extractor set: %w", err)
		}
		engine.extractors = es.Extractors
		log.Printf("[info] crawler: Loaded extractor set %q with %d extractors", es.Name, len(es.Extractors))
	}

	// Try to acquire a semaphore slot (non-blocking)
	select {
	case m.sem <- struct{}{}:
		// Got a slot — start immediately
		m.mu.Lock()
		m.engines[sessionID] = engine
		m.mu.Unlock()
		go m.runEngine(sessionID, engine, req.Seeds)
	default:
		// All slots taken — enqueue
		m.enqueue(sessionID, engine, req.Seeds)
	}

	return sessionID, nil
}

// StopCrawl stops a running crawl session or removes it from the queue.
// It waits up to 30 seconds for the engine to fully shut down.
func (m *Manager) StopCrawl(sessionID string) error {
	// Check if queued first
	if m.dequeue(sessionID) {
		applog.Infof("crawler", "Removed queued session %s", sessionID)
		// Mark as stopped in ClickHouse
		ctx := context.Background()
		sess, err := m.store.GetSession(ctx, sessionID)
		if err == nil {
			sess.Status = "stopped"
			sess.FinishedAt = time.Now()
			_ = m.store.InsertSession(ctx, sess)
		}
		return nil
	}

	m.mu.RLock()
	engine, ok := m.engines[sessionID]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("session %s is not running", sessionID)
	}

	engine.Stop()

	// Wait for the engine workers to drain (max 15s).
	// Finalization (depths, PageRank) continues in background.
	select {
	case <-engine.Done():
	case <-time.After(15 * time.Second):
		applog.Warnf("crawler", "Timeout waiting for engine %s workers to drain", sessionID)
	}
	return nil
}

// IsRunning checks if a session is currently running.
func (m *Manager) IsRunning(sessionID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.engines[sessionID]
	return ok
}

// Progress returns current crawl progress for a running session.
func (m *Manager) Progress(sessionID string) (int64, int, bool) {
	m.mu.RLock()
	engine, ok := m.engines[sessionID]
	m.mu.RUnlock()
	if !ok {
		return 0, 0, false
	}
	return engine.PagesCrawled(), engine.QueueLen(), true
}

// Phase returns the current phase of a running session (e.g. "fetching_sitemaps", "crawling").
func (m *Manager) Phase(sessionID string) string {
	m.mu.RLock()
	engine, ok := m.engines[sessionID]
	m.mu.RUnlock()
	if !ok {
		return ""
	}
	return engine.Phase()
}

// BufferState returns the buffer error state for a running session.
func (m *Manager) BufferState(sessionID string) storage.BufferErrorState {
	m.mu.RLock()
	engine, ok := m.engines[sessionID]
	m.mu.RUnlock()
	if !ok {
		return storage.BufferErrorState{}
	}
	return engine.BufferState()
}

// ActiveSessions returns IDs of currently running sessions.
func (m *Manager) ActiveSessions() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.engines))
	for id := range m.engines {
		ids = append(ids, id)
	}
	return ids
}

// ResumeCrawl resumes a stopped/completed session by re-crawling undiscovered links.
// If overrides.FullRecrawl is true, it starts a new session with the original
// seed URLs and provided overrides, leaving the previous session intact.
func (m *Manager) ResumeCrawl(sessionID string, overrides *CrawlRequest) (string, error) {
	m.mu.RLock()
	_, running := m.engines[sessionID]
	m.mu.RUnlock()
	if running {
		return "", fmt.Errorf("session %s is already running", sessionID)
	}
	if m.IsQueued(sessionID) {
		return "", fmt.Errorf("session %s is queued", sessionID)
	}

	// Get original session info to preserve seed URLs
	originalSession, err := m.store.GetSession(context.Background(), sessionID)
	if err != nil {
		return "", fmt.Errorf("fetching original session: %w", err)
	}
	if len(originalSession.SeedURLs) == 0 {
		return "", fmt.Errorf("session %s has no seed URLs", sessionID)
	}

	fullRecrawl := overrides != nil && overrides.FullRecrawl
	seeds := originalSession.SeedURLs
	if !fullRecrawl {
		// Get uncrawled URLs from storage
		uncrawled, err := m.store.UncrawledURLs(context.Background(), sessionID)
		if err != nil {
			return "", fmt.Errorf("fetching uncrawled URLs: %w", err)
		}
		if len(uncrawled) == 0 {
			return "", fmt.Errorf("no uncrawled URLs found for session %s", sessionID)
		}
		seeds = uncrawled
	}

	// Restore config from original session so UA, TLS profile, etc. are preserved
	cfg := *m.cfg
	if originalSession.Config != "" {
		var savedCfg config.Config
		if err := json.Unmarshal([]byte(originalSession.Config), &savedCfg); err == nil {
			applySavedCrawlerConfig(&cfg, savedCfg.Crawler)
		}
	}
	if overrides != nil {
		crawlerCfg := cfg.Crawler
		if overrides.MaxPages > 0 {
			crawlerCfg.MaxPages = overrides.MaxPages
		}
		if overrides.MaxDepth > 0 {
			crawlerCfg.MaxDepth = overrides.MaxDepth
		}
		if overrides.Workers > 0 {
			crawlerCfg.Workers = overrides.Workers
		}
		if overrides.Delay != "" {
			if d, err := time.ParseDuration(overrides.Delay); err == nil {
				crawlerCfg.Delay = d
			}
		}
		crawlerCfg.StoreHTML = overrides.StoreHTML
		if overrides.CrawlScope != "" {
			crawlerCfg.CrawlScope = overrides.CrawlScope
		}
		if overrides.UserAgent != "" {
			crawlerCfg.UserAgent = overrides.UserAgent
		}
		if overrides.TLSProfile != "" {
			crawlerCfg.TLSProfile = overrides.TLSProfile
		}
		if overrides.SourceIP != "" {
			crawlerCfg.SourceIP = overrides.SourceIP
		}
		if overrides.ForceIPv4 {
			crawlerCfg.ForceIPv4 = true
		}
		if overrides.IgnoreRobots {
			crawlerCfg.RespectRobots = false
		}
		if overrides.JSRenderMode != "" {
			crawlerCfg.JSRender.Mode = overrides.JSRenderMode
		}
		if overrides.JSRenderMaxPages > 0 {
			crawlerCfg.JSRender.MaxPages = overrides.JSRenderMaxPages
		}
		if len(overrides.ExcludePatterns) > 0 {
			crawlerCfg.ExcludePatterns = overrides.ExcludePatterns
		}
		if overrides.CheckExternalLinks != nil {
			crawlerCfg.CheckExternalLinks = boolPtr(*overrides.CheckExternalLinks)
		}
		if overrides.ExternalLinkWorkers > 0 {
			crawlerCfg.ExternalLinkWorkers = overrides.ExternalLinkWorkers
		}
		crawlerCfg.CrawlSitemapOnly = overrides.CrawlSitemapOnly
		if overrides.FetchSitemaps != nil {
			crawlerCfg.FetchSitemaps = boolPtr(*overrides.FetchSitemaps || overrides.CrawlSitemapOnly)
		}
		if overrides.CheckPageResources != nil {
			crawlerCfg.CheckPageResources = boolPtr(*overrides.CheckPageResources)
		}
		if overrides.ResourceWorkers > 0 {
			crawlerCfg.ResourceWorkers = overrides.ResourceWorkers
		}
		crawlerCfg.FollowJSLinks = overrides.FollowJSLinks
		crawlerCfg.MeasureCWV = overrides.MeasureCWV
		if overrides.ExtractorSetID != "" {
			crawlerCfg.ExtractorSetID = overrides.ExtractorSetID
		}
		cfg.Crawler = crawlerCfg
	}
	engine := NewEngine(&cfg, m.store)
	engine.excludePatterns = cfg.Crawler.ExcludePatterns
	engine.sitemapOnly = cfg.Crawler.CrawlSitemapOnly
	// On resume, don't re-fetch sitemaps (already in DB) unless explicitly requested
	if overrides != nil && overrides.FetchSitemaps != nil {
		engine.fetchSitemaps = *overrides.FetchSitemaps || engine.sitemapOnly
	} else {
		engine.fetchSitemaps = false
	}

	runSessionID := sessionID
	if fullRecrawl {
		runSessionID = engine.SessionID(originalSession.SeedURLs)
	} else {
		// Restore the original session with its seed URLs, not the uncrawled URLs.
		engine.ResumeSession(sessionID, originalSession.SeedURLs)
	}
	engine.session.ProjectID = originalSession.ProjectID

	// Apply non-config overrides (external links, extractors, JS links)
	engine.checkExternal = boolValue(cfg.Crawler.CheckExternalLinks, true)
	engine.externalWorkers = cfg.Crawler.ExternalLinkWorkers
	if engine.externalWorkers <= 0 {
		engine.externalWorkers = defaultExternalWorkers
	}
	engine.checkResources = boolValue(cfg.Crawler.CheckPageResources, true)
	engine.resourceWorkers = cfg.Crawler.ResourceWorkers
	if engine.resourceWorkers <= 0 {
		engine.resourceWorkers = defaultResourceWorkers
	}
	engine.followJSLinks = cfg.Crawler.FollowJSLinks
	engine.measureCWV = cfg.Crawler.MeasureCWV
	if cfg.Crawler.ExtractorSetID != "" && m.extractorLoader != nil {
		if es, err := m.extractorLoader.GetExtractorSet(cfg.Crawler.ExtractorSetID); err == nil {
			engine.extractors = es.Extractors
		}
	}

	crawledCount := 0
	if !fullRecrawl {
		// Stream crawled URLs from ClickHouse to pre-seed dedup (no []string allocation).
		// Only the FNV hash map is kept in memory (~8 bytes/URL, scalable to millions).
		crawledCount, err = m.store.StreamCrawledURLs(context.Background(), sessionID, engine.MarkSeen)
		if err != nil {
			return "", fmt.Errorf("streaming crawled URLs for dedup: %w", err)
		}
	}
	if fullRecrawl {
		applog.Infof("crawler", "Full recrawl requested for session %s; starting new session %s with %d original seed URL(s)",
			sessionID, runSessionID, len(seeds))
	} else {
		applog.Infof("crawler", "Resuming session %s with %d uncrawled URLs (%d already crawled)",
			sessionID, len(seeds), crawledCount)
	}

	// Try to acquire a semaphore slot (non-blocking)
	select {
	case m.sem <- struct{}{}:
		m.mu.Lock()
		m.engines[runSessionID] = engine
		m.mu.Unlock()
		go m.runEngine(runSessionID, engine, seeds)
	default:
		m.enqueue(runSessionID, engine, seeds)
	}

	return runSessionID, nil
}

// RetryFailed retries pages with status_code = 0 (fetch errors) or a specific status code.
// Deletes the failed rows, then runs a mini-crawl with those URLs.
func (m *Manager) RetryFailed(sessionID string, overrides *CrawlRequest) (int, error) {
	statusCode := 0
	if overrides != nil && overrides.RetryStatusCode > 0 {
		statusCode = overrides.RetryStatusCode
	}

	m.mu.RLock()
	_, running := m.engines[sessionID]
	m.mu.RUnlock()
	if running {
		return 0, fmt.Errorf("session %s is already running", sessionID)
	}

	var failedURLs []string
	var deleted int
	var err error

	if statusCode == 0 {
		// Original behavior: retry status_code=0
		failedURLs, err = m.store.FailedURLs(context.Background(), sessionID)
		if err != nil {
			return 0, fmt.Errorf("fetching failed URLs: %w", err)
		}
		if len(failedURLs) == 0 {
			return 0, fmt.Errorf("no failed pages (status 0) found for session %s", sessionID)
		}
		deleted, err = m.store.DeleteFailedPages(context.Background(), sessionID)
		if err != nil {
			return 0, fmt.Errorf("deleting failed pages: %w", err)
		}
	} else {
		// Retry pages with specific status code
		failedURLs, err = m.store.URLsByStatus(context.Background(), sessionID, statusCode)
		if err != nil {
			return 0, fmt.Errorf("fetching URLs with status %d: %w", statusCode, err)
		}
		if len(failedURLs) == 0 {
			return 0, fmt.Errorf("no pages with status %d found for session %s", statusCode, sessionID)
		}
		deleted, err = m.store.DeletePagesByStatus(context.Background(), sessionID, statusCode)
		if err != nil {
			return 0, fmt.Errorf("deleting pages with status %d: %w", statusCode, err)
		}
	}

	// Get original session
	originalSession, err := m.store.GetSession(context.Background(), sessionID)
	if err != nil {
		return 0, fmt.Errorf("fetching original session: %w", err)
	}

	applog.Infof("crawler", "Retrying %d failed URLs for session %s", len(failedURLs), sessionID)

	// Restore config from original session so UA, TLS profile, etc. are preserved
	cfg := *m.cfg
	if originalSession.Config != "" {
		var savedCfg config.Config
		if err := json.Unmarshal([]byte(originalSession.Config), &savedCfg); err == nil {
			applySavedCrawlerConfig(&cfg, savedCfg.Crawler)
		}
	}
	if overrides != nil {
		crawlerCfg := cfg.Crawler
		if overrides.MaxDepth > 0 {
			crawlerCfg.MaxDepth = overrides.MaxDepth
		}
		if overrides.Workers > 0 {
			crawlerCfg.Workers = overrides.Workers
		}
		if overrides.Delay != "" {
			if d, err := time.ParseDuration(overrides.Delay); err == nil {
				crawlerCfg.Delay = d
			}
		}
		crawlerCfg.StoreHTML = overrides.StoreHTML
		if overrides.CrawlScope != "" {
			crawlerCfg.CrawlScope = overrides.CrawlScope
		}
		if overrides.UserAgent != "" {
			crawlerCfg.UserAgent = overrides.UserAgent
		}
		if overrides.TLSProfile != "" {
			crawlerCfg.TLSProfile = overrides.TLSProfile
		}
		if overrides.SourceIP != "" {
			crawlerCfg.SourceIP = overrides.SourceIP
		}
		if overrides.ForceIPv4 {
			crawlerCfg.ForceIPv4 = true
		}
		if overrides.IgnoreRobots {
			crawlerCfg.RespectRobots = false
		}
		if overrides.JSRenderMode != "" {
			crawlerCfg.JSRender.Mode = overrides.JSRenderMode
		}
		if overrides.JSRenderMaxPages > 0 {
			crawlerCfg.JSRender.MaxPages = overrides.JSRenderMaxPages
		}
		cfg.Crawler = crawlerCfg
	}
	cfg.Crawler.MaxPages = len(failedURLs)

	engine := NewEngine(&cfg, m.store)
	engine.ResumeSession(sessionID, originalSession.SeedURLs)
	engine.session.ProjectID = originalSession.ProjectID

	// Apply non-config overrides (external links, extractors, JS links)
	if overrides != nil {
		if overrides.CheckExternalLinks != nil {
			engine.checkExternal = *overrides.CheckExternalLinks
		}
		engine.externalWorkers = overrides.ExternalLinkWorkers
		if engine.externalWorkers <= 0 {
			engine.externalWorkers = defaultExternalWorkers
		}
		engine.followJSLinks = overrides.FollowJSLinks
		engine.measureCWV = overrides.MeasureCWV
		if overrides.ExtractorSetID != "" && m.extractorLoader != nil {
			if es, err := m.extractorLoader.GetExtractorSet(overrides.ExtractorSetID); err == nil {
				engine.extractors = es.Extractors
			}
		}
	}

	// Try to acquire a semaphore slot (non-blocking)
	select {
	case m.sem <- struct{}{}:
		m.mu.Lock()
		m.engines[sessionID] = engine
		m.mu.Unlock()
		go m.runEngine(sessionID, engine, failedURLs)
	default:
		m.enqueue(sessionID, engine, failedURLs)
	}

	return deleted, nil
}

// RescanPages refetches explicit URLs inside an existing session without recomputing depth or PageRank.
func (m *Manager) RescanPages(sessionID string, urls []string) (int, error) {
	uniqueURLs, err := validateRescanURLs(urls)
	if err != nil {
		return 0, err
	}

	m.mu.RLock()
	_, running := m.engines[sessionID]
	m.mu.RUnlock()
	if running {
		return 0, fmt.Errorf("session %s is already running", sessionID)
	}
	if m.IsQueued(sessionID) {
		return 0, fmt.Errorf("session %s is queued", sessionID)
	}

	originalSession, err := m.store.GetSession(context.Background(), sessionID)
	if err != nil {
		return 0, fmt.Errorf("fetching original session: %w", err)
	}

	rescanMeta := make(map[string]rescanPageMeta, len(uniqueURLs))
	artifactURLs := append([]string(nil), uniqueURLs...)
	for _, u := range uniqueURLs {
		page, err := m.store.GetPage(context.Background(), sessionID, u)
		if err != nil {
			return 0, fmt.Errorf("page %q not found in session: %w", u, err)
		}
		rescanMeta[u] = rescanPageMeta{
			Depth:    page.Depth,
			FoundOn:  page.FoundOn,
			PageRank: page.PageRank,
		}
		if page.FinalURL != "" && page.FinalURL != page.URL {
			artifactURLs = append(artifactURLs, page.FinalURL)
			if finalPage, err := m.store.GetPage(context.Background(), sessionID, page.FinalURL); err == nil {
				rescanMeta[page.FinalURL] = rescanPageMeta{
					Depth:    finalPage.Depth,
					FoundOn:  finalPage.FoundOn,
					PageRank: finalPage.PageRank,
				}
			}
		}
	}

	cfg := *m.cfg
	if originalSession.Config != "" {
		var savedCfg config.Config
		if err := json.Unmarshal([]byte(originalSession.Config), &savedCfg); err == nil {
			applySavedCrawlerConfig(&cfg, savedCfg.Crawler)
		}
	}
	cfg.Crawler.MaxPages = len(uniqueURLs)
	if cfg.Crawler.Workers <= 0 || cfg.Crawler.Workers > len(uniqueURLs) {
		cfg.Crawler.Workers = len(uniqueURLs)
	}

	engine := NewEngine(&cfg, m.store)
	engine.ResumeSession(sessionID, originalSession.SeedURLs)
	engine.session.ProjectID = originalSession.ProjectID
	engine.rescanOnly = true
	engine.skipSessionWrites = true
	engine.rescanMeta = rescanMeta
	engine.fetchSitemaps = false
	engine.checkExternal = false
	engine.checkResources = false
	engine.excludePatterns = cfg.Crawler.ExcludePatterns

	select {
	case m.sem <- struct{}{}:
	default:
		return 0, fmt.Errorf("crawler is busy; try again when a crawl slot is free")
	}
	defer func() {
		<-m.sem
		m.promoteNext()
	}()

	if err := m.store.DeletePageOutgoingArtifacts(context.Background(), sessionID, dedupeRescanArtifactURLs(artifactURLs)); err != nil {
		return 0, err
	}

	m.mu.Lock()
	m.engines[sessionID] = engine
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.engines, sessionID)
		m.mu.Unlock()
	}()

	applog.Infof("crawler", "Rescanning %d selected URL(s) for session %s", len(uniqueURLs), sessionID)
	if err := engine.Run(uniqueURLs); err != nil {
		m.mu.Lock()
		if len(m.lastErrors) >= maxLastErrors {
			for k := range m.lastErrors {
				delete(m.lastErrors, k)
				break
			}
		}
		m.lastErrors[sessionID] = err.Error()
		m.mu.Unlock()
		return 0, err
	}
	m.mu.Lock()
	delete(m.lastErrors, sessionID)
	m.mu.Unlock()
	return len(uniqueURLs), nil
}

func validateRescanURLs(urls []string) ([]string, error) {
	seen := make(map[string]bool, len(urls))
	unique := make([]string, 0, len(urls))
	for _, raw := range urls {
		u := strings.TrimSpace(raw)
		if u == "" || seen[u] {
			continue
		}
		parsed, err := url.Parse(u)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return nil, fmt.Errorf("invalid URL: %q", raw)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return nil, fmt.Errorf("unsupported URL scheme for %q", raw)
		}
		seen[u] = true
		unique = append(unique, u)
	}
	if len(unique) == 0 {
		return nil, fmt.Errorf("at least one URL is required")
	}
	if len(unique) > maxRescanURLs {
		return nil, fmt.Errorf("too many URLs selected; maximum is %d", maxRescanURLs)
	}
	return unique, nil
}

func dedupeRescanArtifactURLs(urls []string) []string {
	seen := make(map[string]bool, len(urls))
	unique := make([]string, 0, len(urls))
	for _, raw := range urls {
		u := strings.TrimSpace(raw)
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		unique = append(unique, u)
	}
	return unique
}

// markSessionCrashed updates a session's status to "crashed" in ClickHouse.
func (m *Manager) markSessionCrashed(sessionID string, engine *Engine, reason string) {
	ctx := context.Background()
	sess, err := m.store.GetSession(ctx, sessionID)
	if err != nil {
		applog.Errorf("crawler", "markSessionCrashed: could not load session %s: %v", sessionID, err)
		return
	}
	sess.Status = "crashed"
	sess.FinishedAt = time.Now()
	if engine != nil {
		sess.PagesCrawled = uint64(engine.PagesCrawled())
	}
	if err := m.store.InsertSession(ctx, sess); err != nil {
		applog.Errorf("crawler", "markSessionCrashed: could not update session %s: %v", sessionID, err)
	}
	applog.Warnf("crawler", "Session %s marked as crashed: %s", sessionID, reason)
}

// Shutdown gracefully stops all running engines within the given timeout.
// Engines still running after the timeout are marked as "crashed".
// Queued sessions are marked as "stopped".
func (m *Manager) Shutdown(timeout time.Duration) {
	// Drain the queue first
	m.queueMu.Lock()
	queued := m.queue
	m.queue = nil
	m.queuedSet = make(map[string]bool)
	m.queueMu.Unlock()
	for _, qc := range queued {
		ctx := context.Background()
		sess, err := m.store.GetSession(ctx, qc.sessionID)
		if err == nil {
			sess.Status = "stopped"
			sess.FinishedAt = time.Now()
			_ = m.store.InsertSession(ctx, sess)
		}
		applog.Infof("crawler", "Shutdown: queued session %s marked stopped", qc.sessionID)
	}

	m.mu.RLock()
	snapshot := make(map[string]*Engine, len(m.engines))
	for id, e := range m.engines {
		snapshot[id] = e
	}
	m.mu.RUnlock()

	if len(snapshot) == 0 {
		return
	}

	applog.Infof("crawler", "Shutdown: stopping %d running engine(s)...", len(snapshot))
	for _, e := range snapshot {
		e.Stop()
	}

	deadline := time.After(timeout)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			m.mu.Lock()
			remaining := make(map[string]*Engine, len(m.engines))
			for id, e := range m.engines {
				remaining[id] = e
			}
			m.mu.Unlock()
			for id, e := range remaining {
				applog.Errorf("crawler", "Shutdown timeout: engine %s still running, marking crashed", id)
				m.markSessionCrashed(id, e, "shutdown timeout")
			}
			return
		case <-ticker.C:
			m.mu.RLock()
			n := len(m.engines)
			m.mu.RUnlock()
			if n == 0 {
				applog.Info("crawler", "Shutdown: all engines stopped cleanly")
				return
			}
		}
	}
}

// RecoverOrphanedSessions marks any sessions still in "running" status as "crashed".
// Should be called at startup to clean up after a previous unclean shutdown.
func (m *Manager) RecoverOrphanedSessions(ctx context.Context) {
	sessions, err := m.store.ListSessions(ctx)
	if err != nil {
		applog.Errorf("crawler", "RecoverOrphanedSessions: could not list sessions: %v", err)
		return
	}
	for _, sess := range sessions {
		if sess.Status == "running" {
			sess.Status = "crashed"
			sess.FinishedAt = time.Now()
			if err := m.store.InsertSession(ctx, &sess); err != nil {
				applog.Errorf("crawler", "RecoverOrphanedSessions: could not update session %s: %v", sess.ID, err)
				continue
			}
			applog.Warnf("crawler", "Recovered orphaned session %s (was running, now crashed)", sess.ID)
		} else if sess.Status == "queued" {
			sess.Status = "stopped"
			sess.FinishedAt = time.Now()
			if err := m.store.InsertSession(ctx, &sess); err != nil {
				applog.Errorf("crawler", "RecoverOrphanedSessions: could not update session %s: %v", sess.ID, err)
				continue
			}
			applog.Warnf("crawler", "Recovered orphaned session %s (was queued, now stopped)", sess.ID)
		}
	}
}

// enqueue adds a crawl to the FIFO queue and persists a "queued" session in ClickHouse.
func (m *Manager) enqueue(sessionID string, engine *Engine, seeds []string) {
	m.queueMu.Lock()
	m.queue = append(m.queue, queuedCrawl{
		sessionID: sessionID,
		engine:    engine,
		seeds:     seeds,
	})
	m.queuedSet[sessionID] = true
	m.queueMu.Unlock()

	// Persist queued status
	engine.session.Status = "queued"
	if err := m.store.InsertSession(context.Background(), engine.session.ToStorageRow()); err != nil {
		applog.Errorf("crawler", "enqueue: could not persist queued session %s: %v", sessionID, err)
	}
	applog.Infof("crawler", "Session %s queued (all slots busy)", sessionID)
}

// dequeue removes a session from the queue. Returns true if it was found and removed.
func (m *Manager) dequeue(sessionID string) bool {
	m.queueMu.Lock()
	defer m.queueMu.Unlock()
	if !m.queuedSet[sessionID] {
		return false
	}
	for i, qc := range m.queue {
		if qc.sessionID == sessionID {
			m.queue = append(m.queue[:i], m.queue[i+1:]...)
			delete(m.queuedSet, sessionID)
			return true
		}
	}
	delete(m.queuedSet, sessionID)
	return false
}

// promoteNext pops the next queued crawl and starts it.
// Must be called after releasing a semaphore slot.
func (m *Manager) promoteNext() {
	m.queueMu.Lock()
	if len(m.queue) == 0 {
		m.queueMu.Unlock()
		return
	}
	next := m.queue[0]
	m.queue = m.queue[1:]
	delete(m.queuedSet, next.sessionID)
	m.queueMu.Unlock()

	// Acquire the semaphore slot (should succeed immediately since caller just released one)
	m.sem <- struct{}{}

	m.mu.Lock()
	m.engines[next.sessionID] = next.engine
	m.mu.Unlock()

	applog.Infof("crawler", "Promoting queued session %s", next.sessionID)
	go m.runEngine(next.sessionID, next.engine, next.seeds)
}

// IsQueued returns true if the session is waiting in the queue.
func (m *Manager) IsQueued(sessionID string) bool {
	m.queueMu.Lock()
	defer m.queueMu.Unlock()
	return m.queuedSet[sessionID]
}

// QueuedSessions returns the IDs of sessions waiting in the queue.
func (m *Manager) QueuedSessions() []string {
	m.queueMu.Lock()
	defer m.queueMu.Unlock()
	ids := make([]string, 0, len(m.queue))
	for _, qc := range m.queue {
		ids = append(ids, qc.sessionID)
	}
	return ids
}

// runEngine runs the crawl engine and records the outcome.
func (m *Manager) runEngine(sessionID string, engine *Engine, seeds []string) {
	defer func() {
		// Release semaphore slot and promote next queued crawl
		<-m.sem
		m.promoteNext()
	}()
	defer func() {
		if r := recover(); r != nil {
			applog.Errorf("crawler", "panic in crawl engine %s: %v\n%s", sessionID, r, debug.Stack())
			m.markSessionCrashed(sessionID, engine, fmt.Sprintf("panic: %v", r))
			m.mu.Lock()
			delete(m.engines, sessionID)
			m.lastErrors[sessionID] = fmt.Sprintf("panic: %v", r)
			m.mu.Unlock()
		}
	}()
	telemetry.Track("crawl_started", posthog.NewProperties().
		Set("seed_count", len(seeds)).
		Set("workers", engine.cfg.Crawler.Workers).
		Set("source", "ui"))

	// Remove engine from map as soon as workers are drained (before finalization).
	// This makes the SSE report is_running=false immediately when Stop is called.
	go func() {
		<-engine.Done()
		m.mu.Lock()
		delete(m.engines, sessionID)
		m.mu.Unlock()
	}()

	err := engine.Run(seeds)
	status := "completed"
	if err != nil {
		status = "error"
	}
	telemetry.Track("crawl_completed", posthog.NewProperties().
		Set("status", status).
		Set("source", "ui"))
	m.mu.Lock()
	// Engine already removed via Done() goroutine above, but ensure cleanup
	delete(m.engines, sessionID)
	if err != nil {
		if len(m.lastErrors) >= maxLastErrors {
			for k := range m.lastErrors {
				delete(m.lastErrors, k)
				break
			}
		}
		m.lastErrors[sessionID] = err.Error()
	} else {
		delete(m.lastErrors, sessionID)
	}
	m.mu.Unlock()
	if err != nil {
		applog.Errorf("crawler", "Crawl %s failed: %v", sessionID, err)
	}
}
