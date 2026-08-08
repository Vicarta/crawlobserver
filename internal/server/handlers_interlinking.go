package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/SEObserver/crawlobserver/internal/applog"
	"github.com/SEObserver/crawlobserver/internal/interlinking"
	"github.com/SEObserver/crawlobserver/internal/storage"
	"github.com/google/uuid"
)

type pageRankSimulationRequest struct {
	Links       []storage.VirtualLink `json:"links"`
	RemoveLinks []storage.VirtualLink `json:"remove_links"`
}

// handleComputeInterlinking launches async interlinking analysis.
func (s *Server) handleComputeInterlinking(w http.ResponseWriter, r *http.Request) {
	if !requireFullAccess(w, r) {
		return
	}
	sessionID := r.PathValue("id")

	// Check HTML is stored
	hasHTML, err := s.store.HasStoredHTML(r.Context(), sessionID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	if !hasHTML {
		writeError(w, http.StatusBadRequest, "no stored HTML for this session (enable store_html in config)")
		return
	}

	// Parse options from body
	var opts struct {
		Method              string  `json:"method"`
		SimilarityThreshold float64 `json:"similarity_threshold"`
		MaxOpportunities    int     `json:"max_opportunities"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&opts)
	}
	if opts.Method == "" {
		opts.Method = "tfidf"
	}
	if opts.SimilarityThreshold == 0 {
		opts.SimilarityThreshold = s.cfg.Interlinking.SimilarityThreshold
	}
	if opts.MaxOpportunities == 0 {
		opts.MaxOpportunities = s.cfg.Interlinking.MaxOpportunities
	}

	go func() {
		if err := interlinking.ComputeOpportunities(context.Background(), s.store, interlinking.ComputeOpportunitiesOptions{
			SessionID:           sessionID,
			Method:              opts.Method,
			SimilarityThreshold: opts.SimilarityThreshold,
			MaxOpportunities:    opts.MaxOpportunities,
		}); err != nil {
			applog.Errorf("server", "ComputeInterlinking %s: %v", sessionID, err)
		}
	}()

	writeJSON(w, map[string]string{
		"status":  "ok",
		"message": fmt.Sprintf("Interlinking analysis started for session %s", sessionID),
	})
}

// handleInterlinkingOpportunities returns paginated interlinking opportunities.
func (s *Server) handleInterlinkingOpportunities(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if !s.requireSessionAccess(w, r, sessionID) {
		return
	}
	limit, offset := clampPagination(queryInt(r, "limit", 100), queryInt(r, "offset", 0))
	filters := parseFilters(r, storage.InterlinkingFilters)
	sort := parseSort(r, storage.InterlinkingSortColumns)

	opps, total, err := s.store.ListInterlinkingOpportunities(r.Context(), sessionID, limit, offset, filters, sort)
	if err != nil {
		internalError(w, r, err)
		return
	}
	if opps == nil {
		opps = []storage.InterlinkingOpportunity{}
	}
	writeJSON(w, map[string]interface{}{
		"opportunities": opps,
		"total":         total,
	})
}

// handleSimulateInterlinking launches a PageRank simulation with virtual links.
func (s *Server) handleSimulateInterlinking(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if !s.requireSessionAccess(w, r, sessionID) {
		return
	}

	var body pageRankSimulationRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	addLinks := cleanVirtualLinks(body.Links)
	removeLinks := cleanVirtualLinks(body.RemoveLinks)
	if len(addLinks) == 0 && len(removeLinks) == 0 {
		writeError(w, http.StatusBadRequest, "no link changes provided")
		return
	}
	var err error
	var missing []storage.VirtualLink
	removeLinks, missing, err = s.resolveRemovalLinks(r.Context(), sessionID, removeLinks)
	if err != nil {
		internalError(w, r, err)
		return
	}
	if len(missing) > 0 {
		writeError(w, http.StatusBadRequest, missingRemovalLinksMessage(missing))
		return
	}

	simID := uuid.New().String()

	go func() {
		if _, err := interlinking.SimulatePageRankChanges(context.Background(), s.store, sessionID, simID, addLinks, removeLinks); err != nil {
			applog.Errorf("server", "SimulateInterlinking %s: %v", sessionID, err)
		}
	}()

	writeJSON(w, map[string]string{
		"status":        "ok",
		"simulation_id": simID,
		"message":       fmt.Sprintf("PageRank simulation started for session %s", sessionID),
	})
}

func (s *Server) handleProjectCurrentSnapshotSimulateInterlinking(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !requireProjectAccess(w, r, projectID) {
		return
	}
	cs, ok := s.currentSnapshotStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "current snapshot storage unavailable")
		return
	}
	snap, err := cs.GetProjectCurrentSnapshot(r.Context(), projectID)
	if err != nil {
		if isNotFoundErr(err) {
			snap, err = s.initializeCurrentSnapshotFromTrustedBaseline(r.Context(), projectID, cs)
			if err != nil {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
		} else {
			internalError(w, r, err)
			return
		}
	}
	if _, err := s.store.GetSession(r.Context(), snap.CurrentSessionID); err != nil {
		snap, err = s.initializeCurrentSnapshotFromTrustedBaseline(r.Context(), projectID, cs)
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
	}
	sessionID := snap.CurrentSessionID

	var body pageRankSimulationRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	addLinks := cleanVirtualLinks(body.Links)
	removeLinks := cleanVirtualLinks(body.RemoveLinks)
	if len(addLinks) == 0 && len(removeLinks) == 0 {
		writeError(w, http.StatusBadRequest, "no link changes provided")
		return
	}
	var missing []storage.VirtualLink
	removeLinks, missing, err = s.resolveRemovalLinks(r.Context(), sessionID, removeLinks)
	if err != nil {
		internalError(w, r, err)
		return
	}
	if len(missing) > 0 {
		writeError(w, http.StatusBadRequest, missingRemovalLinksMessage(missing))
		return
	}

	simID := uuid.New().String()
	go func() {
		if _, err := interlinking.SimulatePageRankChanges(context.Background(), s.store, sessionID, simID, addLinks, removeLinks); err != nil {
			applog.Errorf("server", "ProjectCurrentSnapshotSimulateInterlinking %s: %v", sessionID, err)
		}
	}()

	writeJSON(w, map[string]string{
		"status":             "ok",
		"simulation_id":      simID,
		"current_session_id": sessionID,
		"message":            fmt.Sprintf("PageRank simulation started for project current snapshot %s", projectID),
	})
}

func cleanVirtualLinks(links []storage.VirtualLink) []storage.VirtualLink {
	seen := make(map[storage.VirtualLink]struct{}, len(links))
	result := make([]storage.VirtualLink, 0, len(links))
	for _, raw := range links {
		link := storage.VirtualLink{
			SourceURL: strings.TrimSpace(raw.SourceURL),
			TargetURL: strings.TrimSpace(raw.TargetURL),
		}
		if link.SourceURL == "" || link.TargetURL == "" {
			continue
		}
		if _, ok := seen[link]; ok {
			continue
		}
		seen[link] = struct{}{}
		result = append(result, link)
	}
	return result
}

func (s *Server) resolveRemovalLinks(ctx context.Context, sessionID string, links []storage.VirtualLink) ([]storage.VirtualLink, []storage.VirtualLink, error) {
	if len(links) == 0 {
		return nil, nil, nil
	}
	resolvedMap, err := s.store.ResolveExistingLinkPairs(ctx, sessionID, links)
	if err != nil {
		return nil, nil, err
	}
	resolved := make([]storage.VirtualLink, 0, len(links))
	missing := make([]storage.VirtualLink, 0)
	for _, link := range links {
		if existing, ok := resolvedMap[link]; ok {
			resolved = append(resolved, existing)
		} else {
			missing = append(missing, link)
		}
	}
	return resolved, missing, nil
}

func missingRemovalLinksMessage(missing []storage.VirtualLink) string {
	if len(missing) == 0 {
		return ""
	}
	const maxExamples = 5
	limit := len(missing)
	if limit > maxExamples {
		limit = maxExamples
	}
	parts := make([]string, 0, limit)
	for _, link := range missing[:limit] {
		parts = append(parts, fmt.Sprintf("%s -> %s", truncateLinkForMessage(link.SourceURL), truncateLinkForMessage(link.TargetURL)))
	}
	suffix := ""
	if len(missing) > maxExamples {
		suffix = fmt.Sprintf(" and %d more", len(missing)-maxExamples)
	}
	return fmt.Sprintf("cannot remove links that are not present in this crawl: %s%s", strings.Join(parts, "; "), suffix)
}

func truncateLinkForMessage(value string) string {
	const maxLen = 180
	if len(value) <= maxLen {
		return value
	}
	return value[:maxLen-3] + "..."
}

// handleListSimulations returns all simulations for a session.
func (s *Server) handleListSimulations(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if !s.requireSessionAccess(w, r, sessionID) {
		return
	}

	sims, err := s.store.ListSimulations(r.Context(), sessionID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	if sims == nil {
		sims = []storage.SimulationMeta{}
	}
	writeJSON(w, map[string]interface{}{
		"simulations": sims,
	})
}

// handleGetSimulationResults returns paginated results for a simulation.
func (s *Server) handleGetSimulationResults(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	simID := r.PathValue("simId")
	if !s.requireSessionAccess(w, r, sessionID) {
		return
	}

	limit, offset := clampPagination(queryInt(r, "limit", 100), queryInt(r, "offset", 0))
	filters := parseFilters(r, storage.SimulationResultFilters)
	sort := parseSort(r, storage.SimulationResultSortColumns)
	htmlOnly := r.URL.Query().Get("html_only") == "1" || strings.EqualFold(r.URL.Query().Get("html_only"), "true")

	// Get simulation meta
	meta, err := s.store.GetSimulation(r.Context(), sessionID, simID)
	if err != nil {
		internalError(w, r, err)
		return
	}

	results, total, err := s.store.ListSimulationResults(r.Context(), sessionID, simID, limit, offset, filters, sort, htmlOnly)
	if err != nil {
		internalError(w, r, err)
		return
	}
	if results == nil {
		results = []storage.SimulationResultRow{}
	}

	writeJSON(w, map[string]interface{}{
		"simulation": meta,
		"results":    results,
		"total":      total,
	})
}

// handleImportVirtualLinks allows importing a list of source/target URL pairs
// for PageRank simulation from external tools.
func (s *Server) handleImportVirtualLinks(w http.ResponseWriter, r *http.Request) {
	if !requireFullAccess(w, r) {
		return
	}
	sessionID := r.PathValue("id")

	var body struct {
		Links []storage.VirtualLink `json:"links"`
		Name  string                `json:"name"` // optional label
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(body.Links) == 0 {
		writeError(w, http.StatusBadRequest, "no links provided")
		return
	}

	simID := uuid.New().String()

	go func() {
		if _, err := interlinking.SimulatePageRank(context.Background(), s.store, sessionID, simID, body.Links); err != nil {
			applog.Errorf("server", "ImportVirtualLinks simulation %s: %v", sessionID, err)
		}
	}()

	writeJSON(w, map[string]string{
		"status":        "ok",
		"simulation_id": simID,
		"message":       fmt.Sprintf("Simulation with %d imported links started for session %s", len(body.Links), sessionID),
	})
}
