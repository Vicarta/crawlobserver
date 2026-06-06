package server

import (
	"context"
	"database/sql"
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

const gscFetchChunkDays = 7

type gscDateChunk struct {
	Start time.Time
	End   time.Time
}

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

	propertyChanged := body.PropertyURL != "" && body.PropertyURL != conn.PropertyURL

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

	startDate, endDate, err := gscEffectiveDateRange(body.StartDate, body.EndDate, time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	checkpoint, err := s.keyStore.GetGSCFetchCheckpoint(projectID)
	if err != nil && err != sql.ErrNoRows {
		internalError(w, r, err)
		return
	}
	resuming := err == nil &&
		checkpoint.PropertyURL == conn.PropertyURL &&
		checkpoint.StartDate == startDate &&
		checkpoint.EndDate == endDate &&
		!checkpoint.Completed
	bootstrapExisting := false
	rangeExplicit := strings.TrimSpace(body.StartDate) != "" || strings.TrimSpace(body.EndDate) != ""
	if err == sql.ErrNoRows && !propertyChanged && !rangeExplicit {
		bootstrapExisting = s.hasExistingGSCData(r.Context(), projectID)
	}

	if !resuming {
		// A new property/range is a replacement import. A resume of the same
		// property/range must not clear already fetched chunks.
		if !bootstrapExisting {
			if err := s.store.DeleteGSCData(r.Context(), projectID); err != nil {
				internalError(w, r, err)
				return
			}
		} else {
			applog.Infof("gsc", "bootstrapping chunked fetch from existing project data without clearing project %s", projectID)
		}
		checkpoint = &apikeys.GSCFetchCheckpoint{
			ProjectID:     projectID,
			PropertyURL:   conn.PropertyURL,
			StartDate:     startDate,
			EndDate:       endDate,
			NextStartDate: startDate,
			RowsFetched:   0,
			Completed:     false,
		}
		if err := s.keyStore.SaveGSCFetchCheckpoint(checkpoint); err != nil {
			internalError(w, r, err)
			return
		}
	} else if checkpoint.NextStartDate == "" {
		checkpoint.NextStartDate = startDate
	}
	statusResuming := resuming || bootstrapExisting

	// Cancel any existing fetch for this project
	s.gscFetchMu.Lock()
	if s.gscFetchStatus == nil {
		s.gscFetchStatus = make(map[string]*gscFetchStatus)
	}
	if existing := s.gscFetchStatus[projectID]; existing != nil && existing.cancel != nil {
		existing.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.gscFetchStatus[projectID] = &gscFetchStatus{
		Fetching:      true,
		RowsSoFar:     checkpoint.RowsFetched,
		PropertyURL:   conn.PropertyURL,
		StartDate:     startDate,
		EndDate:       endDate,
		NextStartDate: checkpoint.NextStartDate,
		Resuming:      statusResuming,
		cancel:        cancel,
	}
	s.gscFetchMu.Unlock()

	// Fetch in background with incremental batch insertion
	go func() {
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				applog.Errorf("gsc", "fetch PANIC: %v", r)
				s.gscFetchMu.Lock()
				if s.gscFetchStatus[projectID] != nil {
					s.gscFetchStatus[projectID] = &gscFetchStatus{Fetching: false, Error: fmt.Sprintf("panic: %v", r)}
				}
				s.gscFetchMu.Unlock()
			}
		}()
		applog.Infof("gsc", "fetch started for project %s, property %s, range %s to %s, next=%s, resume=%t", projectID, conn.PropertyURL, startDate, endDate, checkpoint.NextStartDate, statusResuming)

		client, newToken, err := gsc.NewClientFromTokens(ctx, &s.cfg.GSC, conn.AccessToken, conn.RefreshToken, conn.TokenExpiry)
		if err != nil {
			s.setGSCFetchError(projectID, checkpoint.RowsFetched, fmt.Errorf("GSC authentication failed, please reconnect: %w", err))
			return
		}
		if newToken.AccessToken != conn.AccessToken {
			conn.AccessToken = newToken.AccessToken
			conn.TokenExpiry = newToken.Expiry
			_ = s.keyStore.SaveGSCConnection(conn)
		}

		total, err := s.fetchGSCAnalyticsChunks(ctx, client, conn.PropertyURL, checkpoint)
		s.gscFetchMu.Lock()
		if s.gscFetchStatus[projectID] != nil {
			if err != nil {
				applog.Errorf("gsc", "fetch error: %v", err)
				s.gscFetchStatus[projectID] = &gscFetchStatus{Fetching: false, RowsSoFar: total, Error: err.Error()}
			} else {
				applog.Infof("gsc", "fetch completed for project %s: %d total rows", projectID, total)
				delete(s.gscFetchStatus, projectID)
			}
		}
		s.gscFetchMu.Unlock()
	}()

	writeJSON(w, map[string]string{"status": "fetching"})
}

func (s *Server) hasExistingGSCData(ctx context.Context, projectID string) bool {
	stats, err := s.store.GSCOverview(ctx, projectID)
	if err != nil || stats == nil {
		return false
	}
	return stats.TotalClicks > 0 ||
		stats.TotalImpressions > 0 ||
		stats.TotalQueries > 0 ||
		stats.TotalPages > 0
}

func (s *Server) fetchGSCAnalyticsChunks(ctx context.Context, client *gsc.Client, propertyURL string, checkpoint *apikeys.GSCFetchCheckpoint) (int, error) {
	start, err := time.Parse("2006-01-02", checkpoint.NextStartDate)
	if err != nil {
		return checkpoint.RowsFetched, fmt.Errorf("invalid gsc checkpoint next_start_date %q: %w", checkpoint.NextStartDate, err)
	}
	rangeEnd, err := time.Parse("2006-01-02", checkpoint.EndDate)
	if err != nil {
		return checkpoint.RowsFetched, fmt.Errorf("invalid gsc checkpoint end_date %q: %w", checkpoint.EndDate, err)
	}

	total := checkpoint.RowsFetched
	chunks := gscDateChunks(start, rangeEnd, gscFetchChunkDays)
	if len(chunks) == 0 {
		checkpoint.Completed = true
		checkpoint.NextStartDate = rangeEnd.AddDate(0, 0, 1).Format("2006-01-02")
		checkpoint.RowsFetched = total
		if err := s.keyStore.SaveGSCFetchCheckpoint(checkpoint); err != nil {
			return total, fmt.Errorf("saving completed gsc checkpoint: %w", err)
		}
		return total, nil
	}

	for _, chunk := range chunks {
		chunkStart := chunk.Start.Format("2006-01-02")
		chunkEnd := chunk.End.Format("2006-01-02")
		s.setGSCFetchProgress(checkpoint.ProjectID, gscFetchStatus{
			Fetching:          true,
			RowsSoFar:         total,
			PropertyURL:       propertyURL,
			StartDate:         checkpoint.StartDate,
			EndDate:           checkpoint.EndDate,
			CurrentChunkStart: chunkStart,
			CurrentChunkEnd:   chunkEnd,
			NextStartDate:     checkpoint.NextStartDate,
		})
		applog.Infof("gsc", "fetching chunk project=%s property=%s range=%s..%s", checkpoint.ProjectID, propertyURL, chunkStart, chunkEnd)

		baseTotal := total
		chunkTotal, err := client.FetchSearchAnalytics(ctx, propertyURL, chunkStart, chunkEnd,
			func(rows []gsc.AnalyticsRow, chunkRowsSoFar int) error {
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
				if err := s.store.InsertGSCAnalytics(ctx, checkpoint.ProjectID, insertRows); err != nil {
					return fmt.Errorf("inserting batch: %w", err)
				}
				s.setGSCFetchProgress(checkpoint.ProjectID, gscFetchStatus{
					Fetching:          true,
					RowsSoFar:         baseTotal + chunkRowsSoFar,
					PropertyURL:       propertyURL,
					StartDate:         checkpoint.StartDate,
					EndDate:           checkpoint.EndDate,
					CurrentChunkStart: chunkStart,
					CurrentChunkEnd:   chunkEnd,
					NextStartDate:     checkpoint.NextStartDate,
				})
				applog.Infof("gsc", "inserted %d rows (chunk: %d, total: %d)", len(rows), chunkRowsSoFar, baseTotal+chunkRowsSoFar)
				return nil
			})
		if err != nil {
			return baseTotal + chunkTotal, err
		}

		total += chunkTotal
		checkpoint.RowsFetched = total
		checkpoint.NextStartDate = chunk.End.AddDate(0, 0, 1).Format("2006-01-02")
		checkpoint.Completed = chunk.End.Equal(rangeEnd)
		if err := s.keyStore.SaveGSCFetchCheckpoint(checkpoint); err != nil {
			return total, fmt.Errorf("saving gsc checkpoint: %w", err)
		}
	}

	return total, nil
}

func (s *Server) setGSCFetchProgress(projectID string, next gscFetchStatus) {
	s.gscFetchMu.Lock()
	current := s.gscFetchStatus[projectID]
	if current == nil {
		s.gscFetchMu.Unlock()
		return
	}
	next.cancel = current.cancel
	s.gscFetchStatus[projectID] = &next
	s.gscFetchMu.Unlock()
}

func (s *Server) setGSCFetchError(projectID string, rows int, err error) {
	applog.Errorf("gsc", "fetch error: %v", err)
	s.gscFetchMu.Lock()
	if s.gscFetchStatus[projectID] != nil {
		s.gscFetchStatus[projectID] = &gscFetchStatus{Fetching: false, RowsSoFar: rows, Error: err.Error()}
	}
	s.gscFetchMu.Unlock()
}

func gscEffectiveDateRange(startDate, endDate string, now time.Time) (string, string, error) {
	startDate = strings.TrimSpace(startDate)
	endDate = strings.TrimSpace(endDate)
	if endDate == "" {
		endDate = now.AddDate(0, 0, -3).Format("2006-01-02")
	}
	if startDate == "" {
		startDate = now.AddDate(-1, -4, 0).Format("2006-01-02")
	}
	start, err := time.Parse("2006-01-02", startDate)
	if err != nil {
		return "", "", fmt.Errorf("invalid start_date")
	}
	end, err := time.Parse("2006-01-02", endDate)
	if err != nil {
		return "", "", fmt.Errorf("invalid end_date")
	}
	if start.After(end) {
		return "", "", fmt.Errorf("start_date must be on or before end_date")
	}
	return start.Format("2006-01-02"), end.Format("2006-01-02"), nil
}

func gscDateChunks(start, end time.Time, days int) []gscDateChunk {
	if days <= 0 || start.After(end) {
		return []gscDateChunk{}
	}
	var chunks []gscDateChunk
	for cur := start; !cur.After(end); {
		chunkEnd := cur.AddDate(0, 0, days-1)
		if chunkEnd.After(end) {
			chunkEnd = end
		}
		chunks = append(chunks, gscDateChunk{Start: cur, End: chunkEnd})
		cur = chunkEnd.AddDate(0, 0, 1)
	}
	return chunks
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
	s.keyStore.DeleteGSCFetchCheckpoint(projectID)
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
	rows, total, err := s.store.GSCTopQueries(r.Context(), projectID, storage.GSCListOptions{
		Limit:     limit,
		Offset:    offset,
		Search:    r.URL.Query().Get("q"),
		Sort:      r.URL.Query().Get("sort"),
		Direction: r.URL.Query().Get("dir"),
	})
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
	rows, total, err := s.store.GSCTopPages(r.Context(), projectID, storage.GSCListOptions{
		Limit:     limit,
		Offset:    offset,
		Search:    r.URL.Query().Get("q"),
		Sort:      r.URL.Query().Get("sort"),
		Direction: r.URL.Query().Get("dir"),
	})
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, map[string]interface{}{"rows": rows, "total": total})
}

func (s *Server) handleGSCPageQueries(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !requireProjectAccess(w, r, projectID) {
		return
	}
	page := strings.TrimSpace(r.URL.Query().Get("page"))
	if page == "" {
		writeError(w, http.StatusBadRequest, "page required")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 {
		limit = 50
	}
	limit, offset = clampPagination(limit, offset)
	sort := r.URL.Query().Get("sort")
	if sort == "" {
		sort = "impressions"
	}
	dir := r.URL.Query().Get("dir")
	if dir == "" {
		dir = "desc"
	}
	startDate := r.URL.Query().Get("start_date")
	endDate := r.URL.Query().Get("end_date")
	if r.URL.Query().Get("period") == "28d" && startDate == "" && endDate == "" {
		end := time.Now().AddDate(0, 0, -3)
		start := end.AddDate(0, 0, -27)
		startDate = start.Format("2006-01-02")
		endDate = end.Format("2006-01-02")
	}
	rows, total, err := s.store.GSCQueriesForPage(r.Context(), projectID, page, storage.GSCListOptions{
		Limit:     limit,
		Offset:    offset,
		Search:    r.URL.Query().Get("q"),
		Sort:      sort,
		Direction: dir,
		StartDate: startDate,
		EndDate:   endDate,
	})
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, map[string]interface{}{"rows": rows, "total": total, "page": page})
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
