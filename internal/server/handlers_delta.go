package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/SEObserver/crawlobserver/internal/apikeys"
	"github.com/SEObserver/crawlobserver/internal/applog"
	"github.com/SEObserver/crawlobserver/internal/config"
	"github.com/SEObserver/crawlobserver/internal/crawler"
	"github.com/SEObserver/crawlobserver/internal/normalizer"
	"github.com/SEObserver/crawlobserver/internal/storage"
	"golang.org/x/net/publicsuffix"
)

type deltaPreview struct {
	ProjectID                           string                        `json:"project_id"`
	BaselineSessionID                   string                        `json:"baseline_session_id"`
	ConditionalRequestBaselineSessionID string                        `json:"conditional_request_baseline_session_id"`
	UseConditionalRequests              bool                          `json:"use_conditional_requests"`
	TotalCandidates                     int                           `json:"total_candidates"`
	LaunchLimit                         int                           `json:"launch_limit"`
	WillLaunch                          int                           `json:"will_launch"`
	Deferred                            int                           `json:"deferred"`
	BySource                            map[string]int                `json:"by_source"`
	SampleURLs                          []string                      `json:"sample_urls"`
	SitemapRefresh                      *config.DeltaSitemapRefresh   `json:"sitemap_refresh,omitempty"`
	SitemapSelection                    *config.DeltaSitemapSelection `json:"sitemap_selection,omitempty"`
	SitemapEvents                       int                           `json:"sitemap_events"`
	SitemapPending                      int                           `json:"sitemap_pending_unpublished"`
	SitemapCanaries                     int                           `json:"sitemap_canaries"`
	SitemapDeferred                     int                           `json:"sitemap_deferred"`
	SitemapPublishedDifferences         *int                          `json:"sitemap_published_differences,omitempty"`
	SitemapActionable                   *int                          `json:"sitemap_actionable,omitempty"`
	SitemapStableAcknowledged           *int                          `json:"sitemap_stable_acknowledged,omitempty"`
	HeldPublicationReason               string                        `json:"held_publication_reason,omitempty"`
}

type deltaCandidateResult struct {
	settings                 *apikeys.ProjectDeltaSettings
	baseline                 *storage.CrawlSession
	baselineSourceID         string
	baselineEvaluation       string
	baselineSourceEvaluation string
	baselineSnapshotRev      uint64
	baselineWatermarkID      string
	urls                     []string
	manual                   []string
	candidateSources         map[string][]string
	baselineSitemapCount     int
	sitemapRows              []storage.SitemapRow
	sitemapURLRows           []storage.SitemapURLRow
	sitemapRefresh           *config.DeltaSitemapRefresh
	sitemapSelection         *config.DeltaSitemapSelection
	heldPublicationReason    string
	preview                  deltaPreview
}

var (
	errDeltaLaunchNoCandidates  = errors.New("no delta candidates to crawl")
	errDeltaLaunchBusy          = errors.New("project has a running or queued crawl session")
	errDeltaLaunchEvidenceStale = errors.New("delta launch evidence changed before the crawl could be reserved")
	errDeltaLaunchNotDue        = errors.New("scheduled daily delta is no longer due")
)

type deltaLaunchMode uint8

const (
	deltaLaunchManual deltaLaunchMode = iota
	deltaLaunchScheduled
)

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
	var previous *apikeys.ProjectDeltaSettings
	if s.keyStore != nil {
		previous, _ = s.keyStore.GetProjectDeltaSettings(projectID)
	}
	body.ProjectID = projectID
	settings, err := s.keyStore.SaveProjectDeltaSettings(body)
	if err != nil {
		internalError(w, r, err)
		return
	}
	if projectPageRankSettingsChanged(previous, settings) {
		s.startProjectPageRankRecompute(projectID, storage.PageRankOptions{
			IncludeFooterLinks:  settings.IncludeFooterLinksInPageRank,
			FooterSelectors:     append([]string(nil), settings.FooterSelectorPatterns...),
			RefreshLinkLocation: true,
		})
	}
	writeJSON(w, settings)
}

func projectPageRankSettingsChanged(previous, current *apikeys.ProjectDeltaSettings) bool {
	if previous == nil || current == nil {
		return false
	}
	if previous.IncludeFooterLinksInPageRank != current.IncludeFooterLinksInPageRank {
		return true
	}
	return !equalStringSlices(previous.FooterSelectorPatterns, current.FooterSelectorPatterns)
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *Server) handleProjectPageRankRecomputeStatus(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !requireProjectAccess(w, r, projectID) {
		return
	}
	writeJSON(w, s.getProjectPageRankRecomputeStatus(projectID))
}

func (s *Server) handleProjectOrphan404CleanupPreview(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAccess(w, r) {
		return
	}
	projectID := r.PathValue("id")
	if !requireProjectAccess(w, r, projectID) {
		return
	}
	lock := qualityPromotionLock(projectID)
	lock.Lock()
	result, err := s.projectOrphan404CleanupCandidates(r.Context(), projectID, queryInt(r, "limit", 5000))
	lock.Unlock()
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, result)
}

func (s *Server) handleProjectOrphan404Cleanup(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAccess(w, r) {
		return
	}
	projectID := r.PathValue("id")
	if !requireProjectAccess(w, r, projectID) {
		return
	}
	var body struct {
		Confirm bool `json:"confirm"`
		Limit   int  `json:"limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !body.Confirm {
		writeError(w, http.StatusBadRequest, "confirm must be true")
		return
	}
	lock := qualityPromotionLock(projectID)
	lock.Lock()
	result, err := s.projectOrphan404CleanupCandidates(r.Context(), projectID, body.Limit)
	if err != nil {
		lock.Unlock()
		internalError(w, r, err)
		return
	}
	urls := make([]string, 0, len(result.Candidates))
	for _, c := range result.Candidates {
		urls = append(urls, c.URL)
	}
	deleted, err := s.store.DeletePagesAndReferences(r.Context(), result.CurrentSessionID, urls)
	if err != nil {
		lock.Unlock()
		internalError(w, r, err)
		return
	}
	if deleted > 0 {
		opts := storage.PageRankOptions{}
		if settings, settingsErr := s.keyStore.GetProjectDeltaSettings(projectID); settingsErr == nil {
			opts.IncludeFooterLinks = settings.IncludeFooterLinksInPageRank
			opts.FooterSelectors = append([]string(nil), settings.FooterSelectorPatterns...)
		} else {
			applog.Warnf("server", "Orphan404Cleanup %s: using default PageRank options: %v", projectID, settingsErr)
		}
		// Transfer the held project lock to PageRank finalization so a Delta
		// planner cannot observe deleted graph rows with pre-cleanup scores.
		go func() {
			defer lock.Unlock()
			s.recomputeExpectedCurrentSnapshotPageRankLocked(context.Background(), projectID, result.CurrentSessionID, opts)
		}()
	} else {
		lock.Unlock()
	}
	writeJSON(w, map[string]interface{}{
		"status":                         "ok",
		"current_session_id":             result.CurrentSessionID,
		"deleted":                        deleted,
		"pagerank_recalculation_started": deleted > 0,
	})
}

type orphan404CleanupPreview struct {
	ProjectID        string                              `json:"project_id"`
	CurrentSessionID string                              `json:"current_session_id"`
	OlderThanDays    int                                 `json:"older_than_days"`
	OlderThan        time.Time                           `json:"older_than"`
	Count            int                                 `json:"count"`
	Candidates       []storage.Orphan404CleanupCandidate `json:"candidates"`
}

func (s *Server) projectOrphan404CleanupCandidates(ctx context.Context, projectID string, limit int) (*orphan404CleanupPreview, error) {
	settings, err := s.keyStore.GetProjectDeltaSettings(projectID)
	if err != nil {
		return nil, err
	}
	days := settings.Orphan404CleanupDays
	if days <= 0 {
		days = 30
	}
	cs, ok := s.currentSnapshotStore()
	if !ok {
		return nil, fmt.Errorf("current snapshot storage is not available")
	}
	snap, err := cs.GetProjectCurrentSnapshot(ctx, projectID)
	if err != nil {
		return nil, err
	}
	olderThan := time.Now().UTC().AddDate(0, 0, -days)
	candidates, err := s.store.ListOrphan404CleanupCandidates(ctx, snap.CurrentSessionID, olderThan, limit)
	if err != nil {
		return nil, err
	}
	return &orphan404CleanupPreview{
		ProjectID:        projectID,
		CurrentSessionID: snap.CurrentSessionID,
		OlderThanDays:    days,
		OlderThan:        olderThan,
		Count:            len(candidates),
		Candidates:       candidates,
	}, nil
}

func (s *Server) startProjectPageRankRecompute(projectID string, opts storage.PageRankOptions) {
	started := time.Now().UTC()
	s.setProjectPageRankRecomputeStatus(projectID, pageRankRecomputeStatus{
		Status:    "running",
		Message:   "Internal PageRank recalculation started.",
		StartedAt: &started,
	})

	go func() {
		sessionID, err := s.recomputeProjectCurrentSnapshotPageRank(context.Background(), projectID, opts)
		finished := time.Now().UTC()
		if err != nil {
			s.setProjectPageRankRecomputeStatus(projectID, pageRankRecomputeStatus{
				Status:     "failed",
				Message:    "Internal PageRank recalculation failed.",
				SessionID:  sessionID,
				StartedAt:  &started,
				FinishedAt: &finished,
				Error:      err.Error(),
			})
			return
		}
		s.setProjectPageRankRecomputeStatus(projectID, pageRankRecomputeStatus{
			Status:     "completed",
			Message:    "Internal PageRank recalculation completed.",
			SessionID:  sessionID,
			StartedAt:  &started,
			FinishedAt: &finished,
		})
	}()
}

func (s *Server) setProjectPageRankRecomputeStatus(projectID string, status pageRankRecomputeStatus) {
	s.pageRankRecomputeMu.Lock()
	defer s.pageRankRecomputeMu.Unlock()
	if s.pageRankRecomputeStatus == nil {
		s.pageRankRecomputeStatus = make(map[string]*pageRankRecomputeStatus)
	}
	cp := status
	s.pageRankRecomputeStatus[projectID] = &cp
}

func (s *Server) getProjectPageRankRecomputeStatus(projectID string) pageRankRecomputeStatus {
	s.pageRankRecomputeMu.Lock()
	defer s.pageRankRecomputeMu.Unlock()
	if s.pageRankRecomputeStatus == nil || s.pageRankRecomputeStatus[projectID] == nil {
		return pageRankRecomputeStatus{Status: "idle"}
	}
	return *s.pageRankRecomputeStatus[projectID]
}

func (s *Server) recomputeProjectCurrentSnapshotPageRank(ctx context.Context, projectID string, opts storage.PageRankOptions) (string, error) {
	cs, ok := s.currentSnapshotStore()
	if !ok {
		err := fmt.Errorf("current snapshot storage unavailable")
		applog.Warnf("server", "ProjectPageRankRecompute %s: %v", projectID, err)
		return "", err
	}
	snap, err := cs.GetProjectCurrentSnapshot(ctx, projectID)
	if err != nil {
		if isNotFoundErr(err) {
			snap, err = s.initializeCurrentSnapshotFromTrustedBaseline(ctx, projectID, cs)
		}
		if err != nil {
			applog.Warnf("server", "ProjectPageRankRecompute %s: current snapshot lookup failed: %v", projectID, err)
			return "", err
		}
	}
	lock := qualityPromotionLock(projectID)
	lock.Lock()
	defer lock.Unlock()
	snap, err = cs.GetProjectCurrentSnapshot(ctx, projectID)
	if err != nil {
		return "", err
	}
	if err := s.store.ComputePageRankWithOptions(ctx, snap.CurrentSessionID, opts); err != nil {
		applog.Errorf("server", "ProjectPageRankRecompute %s/%s: %v", projectID, snap.CurrentSessionID, err)
		return snap.CurrentSessionID, err
	}
	applog.Infof("server", "ProjectPageRankRecompute %s/%s complete", projectID, snap.CurrentSessionID)
	return snap.CurrentSessionID, nil
}

func (s *Server) recomputeExpectedCurrentSnapshotPageRankLocked(ctx context.Context, projectID, expectedSessionID string, opts storage.PageRankOptions) {
	cs, ok := s.currentSnapshotStore()
	if !ok {
		applog.Warnf("server", "Orphan404Cleanup PageRank recompute %s: current snapshot storage unavailable", projectID)
		return
	}
	snap, err := cs.GetProjectCurrentSnapshot(ctx, projectID)
	if err != nil || snap.CurrentSessionID != expectedSessionID {
		applog.Warnf("server", "Orphan404Cleanup PageRank recompute %s/%s skipped because Current Snapshot advanced", projectID, expectedSessionID)
		return
	}
	if err := s.store.ComputePageRankWithOptions(ctx, expectedSessionID, opts); err != nil {
		applog.Errorf("server", "Orphan404Cleanup PageRank recompute %s/%s: %v", projectID, expectedSessionID, err)
	}
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

	result, sessionID, err := s.launchProjectDelta(r.Context(), projectID, deltaLaunchManual)
	if err != nil {
		if strings.Contains(err.Error(), "no baseline session") || errors.Is(err, errDeltaLaunchNoCandidates) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if errors.Is(err, errDeltaLaunchBusy) || errors.Is(err, errDeltaLaunchEvidenceStale) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		internalError(w, r, err)
		return
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
		if _, _, err := s.launchProjectDelta(ctx, st.ProjectID, deltaLaunchScheduled); err != nil {
			continue
		}
	}
}

// launchProjectDelta uses qualityPromotionLock(projectID) as the per-project
// launch reservation. The lazy snapshot initialization must finish before the
// reservation is acquired because it may use the same lock. Once held, every
// executable entry point shares the final planning re-read, active-session
// check, crawler start, and durable last-run mark.
func (s *Server) launchProjectDelta(ctx context.Context, projectID string, mode deltaLaunchMode) (*deltaCandidateResult, string, error) {
	if _, err := s.deltaBaselineSession(ctx, projectID); err != nil {
		if strings.Contains(err.Error(), sql.ErrNoRows.Error()) {
			return nil, "", fmt.Errorf("no baseline session found for project")
		}
		return nil, "", err
	}

	lock := qualityPromotionLock(projectID)
	lock.Lock()
	defer lock.Unlock()
	if reservation, retained := s.deltaLaunchReservations.Load(projectID); retained && reservation != nil {
		return nil, "", errDeltaLaunchBusy
	}
	if mode == deltaLaunchScheduled {
		settings, err := s.keyStore.GetProjectDeltaSettings(projectID)
		if err != nil {
			return nil, "", err
		}
		if !settings.Enabled || !deltaScheduleDue(*settings, time.Now()) {
			return nil, "", errDeltaLaunchNotDue
		}
	}

	result, err := s.buildDeltaCandidatesLocked(ctx, projectID)
	if err != nil {
		return nil, "", err
	}
	return s.launchDeltaCandidateLocked(ctx, projectID, result)
}

// launchDeltaCandidateLocked consumes one already-planned candidate set while
// the caller owns qualityPromotionLock(projectID). Keeping this narrow helper
// separate makes it impossible for manual and scheduler launches to leave a
// gap between evidence validation and the durable run reservation.
func (s *Server) launchDeltaCandidateLocked(ctx context.Context, projectID string, result *deltaCandidateResult) (*deltaCandidateResult, string, error) {
	if result == nil || result.settings == nil || result.baseline == nil || result.preview.WillLaunch == 0 || len(result.urls) == 0 {
		return result, "", errDeltaLaunchNoCandidates
	}
	if err := s.validateDeltaLaunchReservation(ctx, projectID, result); err != nil {
		return result, "", err
	}
	if s.deltaLaunchHasActiveSession(ctx, projectID, result.settings) {
		return result, "", errDeltaLaunchBusy
	}

	req, err := s.deltaCrawlRequest(result)
	if err != nil {
		return result, "", err
	}
	sessionID, err := s.manager.StartCrawl(req)
	if err != nil {
		return result, "", err
	}
	// A started session must be visible to every local launch path before the
	// SQLite receipt is written. Otherwise a transient MarkProjectDeltaRun
	// failure leaves no durable last_session_id and permits a duplicate launch.
	s.deltaLaunchReservations.Store(projectID, sessionID)
	now := time.Now().UTC()
	if err := s.markDeltaRun(projectID, sessionID, now); err != nil {
		// Stop only the session created by this failed reservation. StopCrawl
		// waits for the engine's terminal finalization; retaining the reservation
		// when that cannot be confirmed is safer than allowing a duplicate retry.
		if stopErr := s.manager.StopCrawl(sessionID); stopErr == nil &&
			!s.manager.IsRunning(sessionID) && !s.manager.IsQueued(sessionID) {
			s.deltaLaunchReservations.Delete(projectID)
			return result, sessionID, fmt.Errorf("marking started delta crawl: %w; started session %s was rolled back", err, sessionID)
		} else if stopErr != nil {
			return result, sessionID, fmt.Errorf("marking started delta crawl: %w; rollback of session %s failed: %v; reservation retained", err, sessionID, stopErr)
		}
		return result, sessionID, fmt.Errorf("marking started delta crawl: %w; rollback of session %s is not yet terminal; reservation retained", err, sessionID)
	}
	s.deltaLaunchReservations.Delete(projectID)
	if len(result.manual) > 0 {
		_ = s.keyStore.MarkProjectDeltaManualURLsConsumed(projectID, result.manual, now)
	}
	return result, sessionID, nil
}

// validateDeltaLaunchReservation re-reads only durable Current Snapshot and
// raw-proof facts. The fetched sitemap observation is already retained on the
// candidate result; re-fetching it here would make Preview/run disagree and
// would turn a read-only validation into another external operation.
func (s *Server) validateDeltaLaunchReservation(ctx context.Context, projectID string, result *deltaCandidateResult) error {
	if result == nil || result.baseline == nil {
		return errDeltaLaunchEvidenceStale
	}
	lineage, err := s.deltaPlanLineage(ctx, projectID, result.baseline.ID)
	if err != nil {
		return fmt.Errorf("%w: current snapshot lineage is unavailable: %v", errDeltaLaunchEvidenceStale, err)
	}
	if lineage.CurrentSessionID != result.baseline.ID ||
		lineage.SourceSessionID != result.baselineSourceID ||
		lineage.SnapshotRevision != result.baselineSnapshotRev ||
		lineage.ContentWatermarkSessionID != result.baselineWatermarkID ||
		lineage.QualityEvaluationRevision != result.baselineEvaluation ||
		lineage.BaselineQualityEvaluationRevision != result.baselineSourceEvaluation {
		return fmt.Errorf("%w: current snapshot lineage changed", errDeltaLaunchEvidenceStale)
	}

	selection := result.sitemapSelection
	if selection == nil {
		return nil
	}
	if result.sitemapRefresh == nil || result.sitemapRefresh.Mode != deltaSitemapRefreshFresh {
		return fmt.Errorf("%w: sitemap selection is not backed by a fresh observation", errDeltaLaunchEvidenceStale)
	}
	terms, err := s.store.LoadDeltaSitemapTerms(ctx, projectID, lineage.CurrentSessionID, lineage.SourceSessionID, deltaSitemapComparisonLimit)
	if err != nil || terms == nil {
		if err == nil {
			err = errors.New("empty terms")
		}
		return fmt.Errorf("%w: raw sitemap proof is unavailable: %v", errDeltaLaunchEvidenceStale, err)
	}
	reloaded := SelectDeltaSitemapCandidates(deltaSitemapSelectionInput(projectID, lineage, result.sitemapRefresh, result.sitemapURLRows, terms, result.settings, result.baseline))
	expected := deltaSitemapSelectionConfig(reloaded, terms, lineage, result.sitemapRefresh.FetchedAt)
	if !deltaSitemapSelectionReservationMatches(selection, expected) {
		return fmt.Errorf("%w: sitemap proof pair, digest, or URL partition changed", errDeltaLaunchEvidenceStale)
	}
	return nil
}

// deltaSitemapSelectionReservationMatches intentionally compares the durable
// Published-vs-Raw partition, rather than the later global launch cap. Manual,
// stale, and GSC sources may constrain the final selected count without
// invalidating the sitemap evidence that was read under this reservation.
func deltaSitemapSelectionReservationMatches(planned, reloaded *config.DeltaSitemapSelection) bool {
	if planned == nil || reloaded == nil {
		return planned == reloaded
	}
	if planned.SelectorRevision != reloaded.SelectorRevision ||
		planned.RawObservationSessionID != reloaded.RawObservationSessionID ||
		!planned.RawObservedAt.Equal(reloaded.RawObservedAt) ||
		planned.PublishedSessionID != reloaded.PublishedSessionID ||
		planned.PublishedSnapshotRevision != reloaded.PublishedSnapshotRevision ||
		planned.PublishedContentWatermarkSessionID != reloaded.PublishedContentWatermarkSessionID ||
		planned.PublishedDifferenceTotal != reloaded.PublishedDifferenceTotal ||
		planned.ActionableTotal != reloaded.ActionableTotal ||
		planned.StableAcknowledgedTotal != reloaded.StableAcknowledgedTotal ||
		planned.PublicationHeld != reloaded.PublicationHeld ||
		planned.StabilityOlderSessionID != reloaded.StabilityOlderSessionID ||
		planned.StabilityNewerSessionID != reloaded.StabilityNewerSessionID ||
		planned.StabilityProofDigest != reloaded.StabilityProofDigest ||
		planned.StabilityLegacyCompletePair != reloaded.StabilityLegacyCompletePair {
		return false
	}
	return deltaSitemapSourcePartitionMatches(planned.SourceByURL, reloaded.SourceByURL)
}

func deltaSitemapSourcePartitionMatches(planned, reloaded map[string]string) bool {
	for url, source := range planned {
		if source == DeltaSitemapSourceCanary {
			continue
		}
		if reloaded[url] != source {
			return false
		}
	}
	for url, source := range reloaded {
		if source == DeltaSitemapSourceCanary {
			continue
		}
		if planned[url] != source {
			return false
		}
	}
	return true
}

func (s *Server) deltaLaunchHasActiveSession(ctx context.Context, projectID string, settings *apikeys.ProjectDeltaSettings) bool {
	if reservation, ok := s.deltaLaunchReservations.Load(projectID); ok && reservation != nil {
		// A MarkProjectDeltaRun failure after StartCrawl is resolved only after an
		// explicit rollback verifies the session is inactive. Until then this is
		// an unconditional per-project reservation, independent of any optional
		// full-crawl pause setting.
		return true
	}
	if settings != nil && settings.LastSessionID != "" &&
		(s.manager.IsRunning(settings.LastSessionID) || s.manager.IsQueued(settings.LastSessionID)) {
		return true
	}
	// Existing settings keep their meaning: a project can opt out of waiting for
	// an unrelated full crawl. The LastSessionID check above is unconditional so
	// two Daily Delta entry points cannot race into duplicate sessions.
	return settings != nil && settings.PauseDeltaWhenFullCrawlRunning && s.projectHasRunningSession(ctx, projectID)
}

func (s *Server) markDeltaRun(projectID, sessionID string, when time.Time) error {
	if s.markProjectDeltaRun != nil {
		return s.markProjectDeltaRun(projectID, sessionID, when)
	}
	return s.keyStore.MarkProjectDeltaRun(projectID, sessionID, when)
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
	// Lazy initialization owns this same project lock, so complete it before the
	// planning transaction enters its critical section. The locked re-read below
	// is authoritative; this result is only an initialization preflight.
	if _, err := s.deltaBaselineSession(ctx, projectID); err != nil {
		if strings.Contains(err.Error(), sql.ErrNoRows.Error()) {
			return nil, fmt.Errorf("no baseline session found for project")
		}
		return nil, err
	}
	lock := qualityPromotionLock(projectID)
	lock.Lock()
	defer lock.Unlock()
	return s.buildDeltaCandidatesLocked(ctx, projectID)
}

func (s *Server) buildDeltaCandidatesLocked(ctx context.Context, projectID string) (*deltaCandidateResult, error) {
	settings, err := s.keyStore.GetProjectDeltaSettings(projectID)
	if err != nil {
		return nil, err
	}
	baseline, err := s.deltaBaselineSessionReadOnly(ctx, projectID)
	if err != nil {
		if strings.Contains(err.Error(), sql.ErrNoRows.Error()) {
			return nil, fmt.Errorf("no baseline session found for project")
		}
		return nil, err
	}
	lineage, err := s.deltaPlanLineage(ctx, projectID, baseline.ID)
	if err != nil {
		return nil, err
	}

	bySource := map[string]int{}
	sourceSets := map[string]map[string]struct{}{}
	candidates := make([]string, 0)
	manualRaw := []string{}
	addSource := func(source string, urls []string) {
		bySource[source] += len(urls)
		candidates = append(candidates, urls...)
		for _, raw := range urls {
			norm, err := normalizeDeltaURL(raw, settings)
			if err != nil || norm == "" {
				continue
			}
			if _, ok := sourceSets[norm]; !ok {
				sourceSets[norm] = map[string]struct{}{}
			}
			sourceSets[norm][source] = struct{}{}
		}
	}

	perSourceLimit := max(1, settings.MaxCandidatesPerRun)
	var sitemapRows []storage.SitemapRow
	var sitemapURLRows []storage.SitemapURLRow
	var sitemapRefresh *config.DeltaSitemapRefresh
	var sitemapSelection *config.DeltaSitemapSelection
	var sitemapCanaryURLs []string
	heldPublicationReason := ""
	if settings.SourceSitemap {
		terms, termsErr := s.store.LoadDeltaSitemapTerms(ctx, projectID, lineage.CurrentSessionID, lineage.SourceSessionID, deltaSitemapComparisonLimit)
		if termsErr != nil {
			return nil, fmt.Errorf("loading delta sitemap safety terms: %w", termsErr)
		}
		if terms == nil {
			return nil, fmt.Errorf("loading delta sitemap safety terms: empty result")
		}
		refreshed, refreshErr := s.refreshDeltaSitemap(ctx, baseline, settings, perSourceLimit)
		if refreshErr != nil {
			return nil, refreshErr
		}
		sitemapRefresh = refreshed.Refresh
		sitemapRows = refreshed.SitemapRows
		sitemapURLRows = refreshed.SitemapURLRows
		switch sitemapRefresh.Mode {
		case deltaSitemapRefreshFresh:
			selection := SelectDeltaSitemapCandidates(deltaSitemapSelectionInput(projectID, lineage, sitemapRefresh, refreshed.SitemapURLRows, terms, settings, baseline))
			sitemapSelection = deltaSitemapSelectionConfig(selection, terms, lineage, sitemapRefresh.FetchedAt)
			selected := make([]string, 0, len(selection.Selected))
			for _, candidate := range selection.Selected {
				selected = append(selected, candidate.URL)
				if candidate.Source == DeltaSitemapSourceCanary {
					sitemapCanaryURLs = append(sitemapCanaryURLs, candidate.URL)
					continue
				}
				addSource(candidate.Source, []string{candidate.URL})
			}
			bySource["sitemap"] = len(selected)
			heldPublicationReason = deltaSitemapPublicationHoldReason(selection.PublicationHeld, selection.SelectionComplete, selection.EventDeferred)
		case deltaSitemapRefreshSnapshotFallback:
			heldPublicationReason = "Sitemap publication held: the sitemap refresh used the snapshot fallback."
		case deltaSitemapRefreshSkipped:
			heldPublicationReason = "Sitemap publication held: the sitemap refresh was not complete."
		}
	}
	if settings.SourceManualQueue {
		urls, err := s.keyStore.ListProjectDeltaManualURLs(projectID, perSourceLimit)
		if err != nil {
			return nil, err
		}
		manualRaw = append(manualRaw, urls...)
		addSource("manual_queue", urls)
	}
	if settings.SourceProblemPages {
		urls, err := s.store.DeltaProblemPageURLs(ctx, baseline.ID, settings.MaxChangedPagesPerRun)
		if err != nil {
			return nil, err
		}
		addSource("problem_pages", urls)
	}
	if settings.SourceGSC {
		urls, err := s.store.DeltaGSCCandidateURLs(ctx, projectID, perSourceLimit)
		if err != nil {
			return nil, err
		}
		addSource("gsc", urls)
	}
	if settings.SourceStalePages {
		staleBefore := time.Now().UTC().AddDate(0, 0, -settings.StaleAfterDays)
		urls, err := s.store.DeltaStalePageURLs(ctx, baseline.ID, staleBefore, settings.MaxChangedPagesPerRun)
		if err != nil {
			return nil, err
		}
		addSource("stale_pages", urls)
	}
	if len(sitemapCanaryURLs) > 0 {
		addSource(DeltaSitemapSourceCanary, sitemapCanaryURLs)
	}

	scope := baselineCrawlScope(baseline)
	filteredAll := filterDeltaURLs(candidates, baseline.SeedURLs, scope, settings)
	knownSet, err := s.deltaKnownURLSet(ctx, baseline.ID, settings)
	if err != nil {
		return nil, err
	}
	filtered, deferred := boundDeltaCandidates(filteredAll, knownSet, settings)
	if sitemapSelection != nil {
		launchedSet := make(map[string]struct{}, len(filtered))
		for _, candidate := range filtered {
			launchedSet[candidate] = struct{}{}
		}
		unlaunchedEvents := 0
		launchedCanaries := 0
		for url, source := range sitemapSelection.SourceByURL {
			if source == DeltaSitemapSourceCanary {
				if _, launched := launchedSet[url]; launched {
					launchedCanaries++
				} else {
					delete(sitemapSelection.SourceByURL, url)
				}
				continue
			}
			if _, selected := sourceSets[url][source]; !selected {
				continue
			}
			if _, launched := launchedSet[url]; !launched && (source == DeltaSitemapSourceAdded || source == DeltaSitemapSourceLastModForward || source == DeltaSitemapSourcePendingUnpublished) {
				unlaunchedEvents++
			}
		}
		sitemapSelection.CanarySelected = launchedCanaries
		if unlaunchedEvents > 0 {
			sitemapSelection.EventSelected -= unlaunchedEvents
			if sitemapSelection.EventSelected < 0 {
				sitemapSelection.EventSelected = 0
			}
			sitemapSelection.EventDeferred += unlaunchedEvents
			sitemapSelection.SelectionComplete = false
		}
		sitemapSelection.SelectedTotal = sitemapSelection.EventSelected + sitemapSelection.CanarySelected
		heldPublicationReason = deltaSitemapPublicationHoldReason(sitemapSelection.PublicationHeld, sitemapSelection.SelectionComplete, sitemapSelection.EventDeferred)
	}
	candidateSources := deltaCandidateSourcesForLaunched(filtered, sourceSets)
	manual := launchedManualURLs(manualRaw, filtered, settings)
	// Delta seeds are the complete execution plan. Link discovery is disabled
	// for the request below, so the launch limit must describe only selected
	// candidates rather than adding an unrelated discovery allowance.
	launchLimit := len(filtered)
	baselineSitemapCount := 0
	if settings.SourceSitemap {
		if count, countErr := s.store.CountSitemapURLs(ctx, baseline.ID); countErr == nil {
			baselineSitemapCount = count
		} else {
			applog.Warnf("server", "delta baseline sitemap count failed for session %s: %v", baseline.ID, countErr)
		}
	}
	sample := filtered
	if len(sample) > 20 {
		sample = sample[:20]
	}
	preview := deltaPreview{
		ProjectID:                           projectID,
		BaselineSessionID:                   baseline.ID,
		ConditionalRequestBaselineSessionID: baseline.ID,
		UseConditionalRequests:              settings.UseConditionalRequests,
		TotalCandidates:                     len(filteredAll),
		LaunchLimit:                         launchLimit,
		WillLaunch:                          len(filtered),
		Deferred:                            deferred,
		BySource:                            bySource,
		SampleURLs:                          sample,
		SitemapRefresh:                      cloneDeltaSitemapRefresh(sitemapRefresh),
		SitemapSelection:                    cloneDeltaSitemapSelection(sitemapSelection),
		HeldPublicationReason:               heldPublicationReason,
	}
	if sitemapSelection != nil {
		applyDeltaSitemapPreview(&preview, sitemapSelection)
	}
	return &deltaCandidateResult{
		settings:                 settings,
		baseline:                 baseline,
		baselineSourceID:         lineage.SourceSessionID,
		baselineEvaluation:       lineage.QualityEvaluationRevision,
		baselineSourceEvaluation: lineage.BaselineQualityEvaluationRevision,
		baselineSnapshotRev:      lineage.SnapshotRevision,
		baselineWatermarkID:      lineage.ContentWatermarkSessionID,
		urls:                     filtered,
		manual:                   manual,
		candidateSources:         candidateSources,
		baselineSitemapCount:     baselineSitemapCount,
		sitemapRows:              sitemapRows,
		sitemapURLRows:           sitemapURLRows,
		sitemapRefresh:           cloneDeltaSitemapRefresh(sitemapRefresh),
		sitemapSelection:         cloneDeltaSitemapSelection(sitemapSelection),
		heldPublicationReason:    heldPublicationReason,
		preview:                  preview,
	}, nil
}

func deltaSitemapSelectionInput(projectID string, lineage *storage.ProjectCurrentSnapshot, refresh *config.DeltaSitemapRefresh, freshRows []storage.SitemapURLRow, terms *storage.DeltaSitemapTerms, settings *apikeys.ProjectDeltaSettings, baseline *storage.CrawlSession) DeltaSitemapSelectionInput {
	var seedURLs []string
	scope := "host"
	if baseline != nil {
		seedURLs = baseline.SeedURLs
		scope = baselineCrawlScope(baseline)
	}
	input := DeltaSitemapSelectionInput{
		ProjectID:     projectID,
		Fresh:         deltaSitemapSelectionURLsInScope(deltaSitemapSelectionURLs(freshRows, settings), seedURLs, scope, settings),
		ChangedLimit:  settings.SitemapChangedLimit,
		CanaryCount:   settings.SitemapCanaryCount,
		MaxCandidates: settings.MaxCandidatesPerRun,
	}
	if lineage != nil {
		input.PublishedSnapshotRevision = lineage.SnapshotRevision
	}
	if refresh != nil {
		input.RotationEpoch = deltaSitemapRotationEpoch(refresh.FetchedAt)
	}
	if terms == nil {
		return input
	}
	input.Raw = deltaSitemapSelectionURLsInScope(deltaSitemapSelectionURLsFromObservation(terms.Raw, settings), seedURLs, scope, settings)
	input.Published = deltaSitemapSelectionURLsInScope(deltaSitemapSelectionURLsFromObservation(terms.Published, settings), seedURLs, scope, settings)
	if stability := terms.Stability; stability != nil {
		input.Stable = deltaSitemapStabilityProofs(stability, seedURLs, scope, settings)
		input.StabilityOlderSessionID = stability.OlderSessionID
		input.StabilityNewerSessionID = stability.NewerSessionID
		input.StabilityProofDigest = stability.ProofDigest
		input.StabilityLegacyPair = stability.LegacyCompletePair
	}
	return input
}

func deltaSitemapSelectionURLsInScope(values []DeltaSitemapSelectionURL, seedURLs []string, scope string, settings *apikeys.ProjectDeltaSettings) []DeltaSitemapSelectionURL {
	result := make([]DeltaSitemapSelectionURL, 0, len(values))
	for _, value := range values {
		if deltaURLAllowedByPatterns(value.URL, settings) && deltaURLInScope(value.URL, seedURLs, scope) {
			result = append(result, value)
		}
	}
	return result
}

func deltaSitemapStabilityProofs(stability *storage.DeltaSitemapStability, seedURLs []string, scope string, settings *apikeys.ProjectDeltaSettings) []DeltaSitemapStabilityProof {
	if stability == nil || len(stability.URLs) == 0 {
		return nil
	}
	proofs := make([]DeltaSitemapStabilityProof, 0, len(stability.URLs))
	for _, proof := range stability.URLs {
		normalized, err := normalizeDeltaURL(proof.Loc, settings)
		if err != nil || normalized == "" {
			continue
		}
		if !deltaURLAllowedByPatterns(normalized, settings) || !deltaURLInScope(normalized, seedURLs, scope) {
			continue
		}
		proofs = append(proofs, DeltaSitemapStabilityProof{URL: normalized, LastMod: proof.LastMod})
	}
	return proofs
}

func deltaSitemapPublicationHoldReason(publicationHeld, selectionComplete bool, eventDeferred int) string {
	if publicationHeld && !selectionComplete {
		return fmt.Sprintf("Sitemap publication held: %d changed event candidates are deferred; raw-stable sitemap differences are not publication evidence.", eventDeferred)
	}
	if publicationHeld {
		return "Sitemap publication held: raw-stable sitemap differences are not publication evidence."
	}
	if !selectionComplete {
		return fmt.Sprintf("Sitemap publication held: %d changed event candidates are deferred.", eventDeferred)
	}
	return ""
}

func applyDeltaSitemapPreview(preview *deltaPreview, selection *config.DeltaSitemapSelection) {
	if preview == nil || selection == nil {
		return
	}
	preview.SitemapEvents = selection.EventSelected
	preview.SitemapPending = sitemapSelectionPendingCount(selection)
	preview.SitemapCanaries = selection.CanarySelected
	preview.SitemapDeferred = selection.EventDeferred
	if selection.SelectorRevision != DeltaSitemapSelectorRevision {
		return
	}
	publishedDifferences := selection.PublishedDifferenceTotal
	actionable := selection.ActionableTotal
	stableAcknowledged := selection.StableAcknowledgedTotal
	preview.SitemapPublishedDifferences = &publishedDifferences
	preview.SitemapActionable = &actionable
	preview.SitemapStableAcknowledged = &stableAcknowledged
}

// deltaBaselineSessionReadOnly resolves the canonical materialized session
// while the caller holds qualityPromotionLock(projectID). It never performs
// lazy initialization, which would recursively acquire that lock.
func (s *Server) deltaBaselineSessionReadOnly(ctx context.Context, projectID string) (*storage.CrawlSession, error) {
	cs, ok := s.currentSnapshotStore()
	if !ok {
		return nil, fmt.Errorf("current snapshot storage unavailable")
	}
	snap, err := cs.GetProjectCurrentSnapshot(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if snap == nil || snap.CurrentSessionID == "" {
		return nil, fmt.Errorf("current snapshot baseline is unavailable")
	}
	current, err := s.store.GetSession(ctx, snap.CurrentSessionID)
	if err != nil {
		return nil, err
	}
	if current.ProjectID == nil || *current.ProjectID != projectID || current.PagesCrawled <= 0 {
		return nil, fmt.Errorf("current snapshot baseline is invalid")
	}
	return current, nil
}

func (s *Server) deltaPlanLineage(ctx context.Context, projectID, materializedSessionID string) (*storage.ProjectCurrentSnapshot, error) {
	cs, ok := s.currentSnapshotStore()
	if !ok {
		return nil, fmt.Errorf("current snapshot storage unavailable for delta baseline lineage")
	}
	qs, ok := s.qualityStore()
	if !ok {
		return nil, fmt.Errorf("quality storage unavailable for delta baseline lineage")
	}
	snap, err := cs.GetProjectCurrentSnapshot(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("loading current snapshot delta baseline: %w", err)
	}
	if snap.CurrentSessionID != materializedSessionID || snap.SourceSessionID == "" ||
		snap.ContentWatermarkSessionID == "" || snap.SnapshotRevision == 0 {
		return nil, fmt.Errorf("current snapshot delta baseline lineage is incomplete or changed")
	}
	currentQuality, _, err := cs.ValidateProjectCurrentSnapshotBinding(ctx, *snap)
	if err != nil {
		return nil, fmt.Errorf("current snapshot delta baseline is stale: %w", err)
	}
	if currentQuality == nil || currentQuality.EvaluationRevision != snap.QualityEvaluationRevision {
		return nil, fmt.Errorf("current snapshot delta baseline quality revision is stale")
	}
	source, err := s.store.GetSession(ctx, snap.SourceSessionID)
	if err != nil || source.ProjectID == nil || *source.ProjectID != projectID || !isFullCrawlSession(*source) {
		return nil, fmt.Errorf("current snapshot raw full-crawl source is unavailable")
	}
	quality, err := qs.GetCrawlQualityResult(ctx, snap.SourceSessionID)
	if err != nil {
		return nil, fmt.Errorf("loading current snapshot source quality: %w", err)
	}
	quality = s.deriveCurrentQualityReadState(ctx, qs, quality)
	if quality == nil || quality.Stale || !quality.Trusted || !quality.IsFullCrawl ||
		quality.EvaluationRevision == "" || quality.EvaluationRevision != snap.BaselineQualityEvaluationRevision {
		return nil, fmt.Errorf("current snapshot raw full-crawl source quality is stale")
	}
	return snap, nil
}

func (s *Server) deltaBaselineSession(ctx context.Context, projectID string) (*storage.CrawlSession, error) {
	if cs, ok := s.currentSnapshotStore(); ok {
		snap, err := cs.GetProjectCurrentSnapshot(ctx, projectID)
		if err != nil && isNotFoundErr(err) {
			if initialized, initErr := s.initializeCurrentSnapshotFromTrustedBaseline(ctx, projectID, cs); initErr == nil {
				snap = initialized
				err = nil
			}
		}
		if err == nil && snap != nil && snap.CurrentSessionID != "" {
			current, currentErr := s.store.GetSession(ctx, snap.CurrentSessionID)
			if currentErr == nil && current.ProjectID != nil && *current.ProjectID == projectID && current.PagesCrawled > 0 {
				return current, nil
			}
		}
	}
	return s.store.LatestProjectSession(ctx, projectID)
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
	maxPages := len(result.urls)
	if maxPages <= 0 {
		maxPages = result.preview.LaunchLimit
	}
	projectID := result.settings.ProjectID
	checkExternal := false
	checkResources := cfg.Crawler.CheckPageResources == nil || *cfg.Crawler.CheckPageResources
	retries := result.settings.RetryCount
	// A Delta plan is an explicit URL set. Do not turn links found on selected
	// pages into a second crawl; regular full crawls keep their own discovery
	// policy.
	discoveryBudget := 0
	req := crawler.CrawlRequest{
		Seeds:               result.urls,
		SessionSeedURLs:     append([]string(nil), result.baseline.SeedURLs...),
		MaxPages:            maxPages,
		DiscoveryBudget:     &discoveryBudget,
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
		DeltaPlannedPages:   len(result.urls),
		DeltaPlan: &config.DeltaPlanConfig{
			BaselineSessionID:                   result.baseline.ID,
			ConditionalRequestBaselineSessionID: result.baseline.ID,
			UseConditionalRequests:              result.settings.UseConditionalRequests,
			BaselineSourceSessionID:             result.baselineSourceID,
			BaselineEvaluationRevision:          result.baselineEvaluation,
			BaselineSourceEvaluationRevision:    result.baselineSourceEvaluation,
			BaselineSnapshotRevision:            result.baselineSnapshotRev,
			BaselineContentWatermarkSessionID:   result.baselineWatermarkID,
			TotalCandidates:                     result.preview.TotalCandidates,
			LaunchedCandidates:                  len(result.urls),
			DeferredCandidates:                  result.preview.Deferred,
			LaunchLimit:                         result.preview.LaunchLimit,
			SourceCounts:                        copyStringIntMap(result.preview.BySource),
			BaselineSitemapURLCount:             result.baselineSitemapCount,
			LaunchedURLs:                        append([]string(nil), result.urls...),
			CandidateSources:                    copyStringSliceMap(result.candidateSources),
			SitemapRefresh:                      cloneDeltaSitemapRefresh(result.sitemapRefresh),
			SitemapSelection:                    cloneDeltaSitemapSelection(result.sitemapSelection),
		},
		InitialSitemaps:              copySitemapRows(result.sitemapRows),
		InitialSitemapURLs:           copySitemapURLRows(result.sitemapURLRows),
		RetryMaxRetries:              &retries,
		RetryBackoffSeconds:          result.settings.RetryBackoffSeconds,
		IncludeFooterLinksInPageRank: &result.settings.IncludeFooterLinksInPageRank,
		FooterSelectorPatterns:       append([]string(nil), result.settings.FooterSelectorPatterns...),
	}
	if result.settings.EnableJSRenderingForDelta == "off" ||
		result.settings.EnableJSRenderingForDelta == "auto" ||
		result.settings.EnableJSRenderingForDelta == "always" {
		req.JSRenderMode = result.settings.EnableJSRenderingForDelta
	}
	return req, nil
}

func copyStringIntMap(in map[string]int) map[string]int {
	if in == nil {
		return nil
	}
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copyStringSliceMap(in map[string][]string) map[string][]string {
	if in == nil {
		return nil
	}
	out := make(map[string][]string, len(in))
	for k, v := range in {
		out[k] = append([]string(nil), v...)
	}
	return out
}

func deltaCandidateSourcesForLaunched(launched []string, sourceSets map[string]map[string]struct{}) map[string][]string {
	if len(launched) == 0 || len(sourceSets) == 0 {
		return nil
	}
	out := make(map[string][]string, len(launched))
	for _, u := range launched {
		sources := orderedDeltaCandidateSources(sourceSets[u])
		if len(sources) > 0 {
			out[u] = sources
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func orderedDeltaCandidateSources(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	order := []string{"manual_queue", DeltaSitemapSourceAdded, DeltaSitemapSourceLastModForward, DeltaSitemapSourcePendingUnpublished, DeltaSitemapSourceCanary, "sitemap_fresh", "sitemap_snapshot_fallback", "sitemap", "problem_pages", "stale_pages", "gsc", "discovered"}
	out := make([]string, 0, len(set))
	for _, source := range order {
		if _, ok := set[source]; ok {
			out = append(out, source)
		}
	}
	for source := range set {
		known := false
		for _, ordered := range order {
			if source == ordered {
				known = true
				break
			}
		}
		if !known {
			out = append(out, source)
		}
	}
	return out
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
	totalLimit := max(0, settings.MaxCandidatesPerRun)
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
