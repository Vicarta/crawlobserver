package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/SEObserver/crawlobserver/internal/apikeys"
	"github.com/SEObserver/crawlobserver/internal/applog"
	"github.com/SEObserver/crawlobserver/internal/gsc"
	"github.com/SEObserver/crawlobserver/internal/storage"
)

func (s *Server) handleGSCAuthorize(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("project_id")
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "project_id required")
		return
	}
	if s.cfg.GSC.ClientID == "" || s.cfg.GSC.ClientSecret == "" {
		writeError(w, http.StatusBadRequest, "GSC not configured: set gsc.client_id and gsc.client_secret in config.yaml")
		return
	}
	url := gsc.AuthorizeURL(&s.cfg.GSC, projectID)
	writeJSON(w, map[string]string{"url": url})
}

func (s *Server) handleGSCCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state") // project_id
	if code == "" || state == "" {
		writeError(w, http.StatusBadRequest, "missing code or state")
		return
	}

	token, err := gsc.ExchangeCode(r.Context(), &s.cfg.GSC, code)
	if err != nil {
		applog.Errorf("gsc", "OAuth exchange error: %v", err)
		writeError(w, http.StatusBadRequest, "failed to exchange code")
		return
	}

	conn := &apikeys.GSCConnection{
		ProjectID:    state,
		PropertyURL:  "", // will be set when user selects property
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		TokenExpiry:  token.Expiry,
	}
	if err := s.keyStore.SaveGSCConnection(conn); err != nil {
		applog.Errorf("gsc", "save connection error: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to save connection")
		return
	}

	// Return to the project Search Console tab so the user can select a property.
	redirectURL := fmt.Sprintf("/projects/%s/gsc", url.PathEscape(state))
	http.Redirect(w, r, redirectURL, http.StatusFound)
}

func (s *Server) handleGSCStatus(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")

	conn, err := s.keyStore.GetGSCConnection(projectID)
	if err != nil {
		writeJSON(w, map[string]interface{}{
			"connected":    false,
			"property_url": "",
			"properties":   []gsc.Property{},
		})
		return
	}

	result := map[string]interface{}{
		"connected":    true,
		"property_url": conn.PropertyURL,
	}

	// Include fetch status if available
	s.gscFetchMu.Lock()
	if fs, ok := s.gscFetchStatus[projectID]; ok {
		result["fetch_status"] = fs
	}
	s.gscFetchMu.Unlock()

	// Admins need the property list both before and after selection so they can
	// correct a wrong HTTP/HTTPS/domain property without disconnecting.
	if auth := apikeys.FromContext(r.Context()); auth == nil || auth.IsAdmin() {
		result["properties"] = s.gscProperties(r.Context(), projectID, conn)
	}

	writeJSON(w, result)
}

func (s *Server) gscProperties(ctx context.Context, projectID string, conn *apikeys.GSCConnection) []gsc.Property {
	client, newToken, err := gsc.NewClientFromTokens(ctx, &s.cfg.GSC, conn.AccessToken, conn.RefreshToken, conn.TokenExpiry)
	if err != nil {
		applog.Errorf("gsc", "client error: %v", err)
		return []gsc.Property{}
	}
	if newToken.AccessToken != conn.AccessToken {
		conn.AccessToken = newToken.AccessToken
		conn.TokenExpiry = newToken.Expiry
		_ = s.keyStore.SaveGSCConnection(conn)
	}

	props, err := client.ListProperties(ctx)
	if err != nil {
		applog.Errorf("gsc", "list properties error: %v", err)
		return []gsc.Property{}
	}
	if props == nil {
		props = []gsc.Property{}
	}
	s.sortGSCProperties(ctx, projectID, props)
	return props
}

func (s *Server) sortGSCProperties(ctx context.Context, projectID string, props []gsc.Property) {
	candidates := s.gscProjectDomainCandidates(ctx, projectID)
	sort.SliceStable(props, func(i, j int) bool {
		si := gscPropertyScore(props[i].SiteURL, candidates)
		sj := gscPropertyScore(props[j].SiteURL, candidates)
		if si != sj {
			return si > sj
		}
		return props[i].SiteURL < props[j].SiteURL
	})
}

func (s *Server) gscProjectDomainCandidates(ctx context.Context, projectID string) map[string]bool {
	candidates := map[string]bool{}
	sessions, err := s.store.ListSessions(ctx, projectID)
	if err != nil {
		return candidates
	}
	for _, sess := range sessions {
		for _, seed := range sess.SeedURLs {
			u, err := url.Parse(seed)
			if err != nil || u.Hostname() == "" {
				continue
			}
			host := strings.ToLower(u.Hostname())
			candidates[host] = true
			candidates[strings.TrimPrefix(host, "www.")] = true
		}
	}
	return candidates
}

func gscPropertyScore(siteURL string, candidates map[string]bool) int {
	site := strings.ToLower(strings.TrimSpace(siteURL))
	score := 0
	if strings.HasPrefix(site, "sc-domain:") {
		score += 30
		domain := strings.TrimPrefix(site, "sc-domain:")
		if candidates[domain] {
			score += 100
		}
		return score
	}

	u, err := url.Parse(site)
	if err != nil || u.Hostname() == "" {
		return score
	}
	host := strings.ToLower(u.Hostname())
	if candidates[host] {
		score += 90
	}
	if candidates[strings.TrimPrefix(host, "www.")] {
		score += 70
	}
	if u.Scheme == "https" {
		score += 20
	}
	if strings.HasPrefix(host, "www.") {
		score += 5
	}
	return score
}

func (s *Server) handleGSCFetch(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")

	// Parse optional property_url from body (for initial property selection)
	var body struct {
		PropertyURL string `json:"property_url"`
		StartDate   string `json:"start_date"`
		EndDate     string `json:"end_date"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	conn, err := s.keyStore.GetGSCConnection(projectID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "no GSC connection for this project")
		return
	}

	// Update property URL if provided.
	if body.PropertyURL != "" {
		conn.PropertyURL = body.PropertyURL
		if err := s.keyStore.SaveGSCConnection(conn); err != nil {
			internalError(w, r, err)
			return
		}
	}

	if conn.PropertyURL == "" {
		writeError(w, http.StatusBadRequest, "no property selected")
		return
	}

	client, newToken, err := gsc.NewClientFromTokens(r.Context(), &s.cfg.GSC, conn.AccessToken, conn.RefreshToken, conn.TokenExpiry)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "GSC authentication failed, please reconnect")
		return
	}
	// Update token if refreshed
	if newToken.AccessToken != conn.AccessToken {
		conn.AccessToken = newToken.AccessToken
		conn.TokenExpiry = newToken.Expiry
		s.keyStore.SaveGSCConnection(conn)
	}

	// Treat every manual fetch as a replacement for this project's GSC dataset.
	// Otherwise repeated refreshes append duplicate rows and inflate totals.
	if err := s.store.DeleteGSCData(r.Context(), projectID); err != nil {
		internalError(w, r, err)
		return
	}

	// Default date range: last 16 months (GSC maximum)
	endDate := body.EndDate
	startDate := body.StartDate
	if endDate == "" {
		endDate = time.Now().AddDate(0, 0, -3).Format("2006-01-02")
	}
	if startDate == "" {
		startDate = time.Now().AddDate(-1, -4, 0).Format("2006-01-02")
	}

	// Cancel any existing fetch for this project
	s.gscFetchMu.Lock()
	if s.gscFetchStatus == nil {
		s.gscFetchStatus = make(map[string]*gscFetchStatus)
	}
	if existing := s.gscFetchStatus[projectID]; existing != nil && existing.cancel != nil {
		existing.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.gscFetchStatus[projectID] = &gscFetchStatus{Fetching: true, cancel: cancel}
	s.gscFetchMu.Unlock()

	// Fetch in background with incremental batch insertion
	go func() {
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				applog.Errorf("gsc", "fetch PANIC: %v", r)
				s.gscFetchMu.Lock()
				s.gscFetchStatus[projectID] = &gscFetchStatus{Fetching: false, Error: fmt.Sprintf("panic: %v", r)}
				s.gscFetchMu.Unlock()
			}
		}()
		applog.Infof("gsc", "fetch started for project %s, property %s, range %s to %s", projectID, conn.PropertyURL, startDate, endDate)

		total, err := client.FetchSearchAnalytics(ctx, conn.PropertyURL, startDate, endDate,
			func(rows []gsc.AnalyticsRow, totalSoFar int) error {
				insertRows := make([]storage.GSCAnalyticsInsertRow, len(rows))
				for i, r := range rows {
					d, _ := time.Parse("2006-01-02", r.Date)
					insertRows[i] = storage.GSCAnalyticsInsertRow{
						Date:        d,
						Query:       r.Query,
						Page:        r.Page,
						Country:     r.Country,
						Device:      r.Device,
						Clicks:      uint32(r.Clicks),
						Impressions: uint32(r.Impressions),
						CTR:         float32(r.CTR),
						Position:    float32(r.Position),
					}
				}
				if err := s.store.InsertGSCAnalytics(ctx, projectID, insertRows); err != nil {
					return fmt.Errorf("inserting batch: %w", err)
				}
				s.gscFetchMu.Lock()
				s.gscFetchStatus[projectID] = &gscFetchStatus{Fetching: true, RowsSoFar: totalSoFar}
				s.gscFetchMu.Unlock()
				applog.Infof("gsc", "inserted %d rows (total: %d)", len(rows), totalSoFar)
				return nil
			})
		s.gscFetchMu.Lock()
		if err != nil {
			applog.Errorf("gsc", "fetch error: %v", err)
			s.gscFetchStatus[projectID] = &gscFetchStatus{Fetching: false, RowsSoFar: total, Error: err.Error()}
		} else {
			applog.Infof("gsc", "fetch completed for project %s: %d total rows", projectID, total)
			delete(s.gscFetchStatus, projectID)
		}
		s.gscFetchMu.Unlock()
	}()

	writeJSON(w, map[string]string{"status": "fetching"})
}

func (s *Server) handleGSCStopFetch(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	s.gscFetchMu.Lock()
	if fs, ok := s.gscFetchStatus[projectID]; ok && fs.cancel != nil {
		fs.cancel()
	}
	delete(s.gscFetchStatus, projectID)
	s.gscFetchMu.Unlock()
	applog.Infof("gsc", "fetch stopped for project %s", projectID)
	writeJSON(w, map[string]string{"status": "stopped"})
}

func (s *Server) handleGSCDisconnect(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	s.keyStore.DeleteGSCConnection(projectID)
	s.store.DeleteGSCData(r.Context(), projectID)
	writeJSON(w, map[string]string{"status": "disconnected"})
}

func (s *Server) handleGSCOverview(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	stats, err := s.store.GSCOverview(r.Context(), projectID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, stats)
}

func (s *Server) handleGSCQueries(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 {
		limit = 100
	}
	limit, offset = clampPagination(limit, offset)
	rows, total, err := s.store.GSCTopQueries(r.Context(), projectID, limit, offset)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, map[string]interface{}{"rows": rows, "total": total})
}

func (s *Server) handleGSCPages(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 {
		limit = 100
	}
	limit, offset = clampPagination(limit, offset)
	rows, total, err := s.store.GSCTopPages(r.Context(), projectID, limit, offset)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, map[string]interface{}{"rows": rows, "total": total})
}

func (s *Server) handleGSCCountries(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	rows, err := s.store.GSCByCountry(r.Context(), projectID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, rows)
}

func (s *Server) handleGSCDevices(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	rows, err := s.store.GSCByDevice(r.Context(), projectID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, rows)
}

func (s *Server) handleGSCTimeline(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	rows, err := s.store.GSCTimeline(r.Context(), projectID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, rows)
}

func (s *Server) handleGSCInspection(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 {
		limit = 100
	}
	limit, offset = clampPagination(limit, offset)
	rows, total, err := s.store.GSCInspectionResults(r.Context(), projectID, limit, offset)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, map[string]interface{}{"rows": rows, "total": total})
}
