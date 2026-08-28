package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/SEObserver/crawlobserver/internal/applog"
	"github.com/SEObserver/crawlobserver/internal/config"
	"github.com/SEObserver/crawlobserver/internal/normalizer"
)

// InsertSession inserts or updates a crawl session.
func (s *Store) InsertSession(ctx context.Context, session *CrawlSession) error {
	return s.conn.Exec(ctx, `
		INSERT INTO crawlobserver.crawl_sessions
		(id, started_at, finished_at, status, seed_urls, config, pages_crawled, user_agent, project_id, label)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		session.ID, session.StartedAt, session.FinishedAt, session.Status,
		session.SeedURLs, session.Config, session.PagesCrawled, session.UserAgent,
		session.ProjectID, session.Label,
	)
}

const nonSyntheticSessionFilter = `(label NOT IN ('Current Snapshot', 'Current Baseline Snapshot'))`

const effectiveOriginSessionBatchSize = 50

// EffectiveOriginsForSessions derives response-only origins for a batch of
// sessions. The launched URL set comes from raw seeds for full crawls and from
// the immutable DeltaPlan for Delta crawls. Page evidence is read once for the
// whole batch so session list responses never introduce an N+1 query.
func (s *Store) EffectiveOriginsForSessions(ctx context.Context, sessions []CrawlSession) (map[string]EffectiveOrigin, error) {
	result := make(map[string]EffectiveOrigin, len(sessions))
	if len(sessions) > effectiveOriginSessionBatchSize {
		for start := 0; start < len(sessions); start += effectiveOriginSessionBatchSize {
			end := min(start+effectiveOriginSessionBatchSize, len(sessions))
			batch, err := s.EffectiveOriginsForSessions(ctx, sessions[start:end])
			if err != nil {
				return nil, err
			}
			for sessionID, origin := range batch {
				result[sessionID] = origin
			}
		}
		return result, nil
	}
	launchedBySession := make(map[string]map[string]struct{}, len(sessions))
	allSessionIDs := make([]string, 0, len(sessions))
	allURLs := make([]string, 0)
	seenSessionIDs := make(map[string]struct{}, len(sessions))
	seenURLs := make(map[string]struct{})

	for _, sess := range sessions {
		result[sess.ID] = EffectiveOrigin{State: EffectiveOriginUnavailable}
		if _, seen := seenSessionIDs[sess.ID]; !seen {
			seenSessionIDs[sess.ID] = struct{}{}
			allSessionIDs = append(allSessionIDs, sess.ID)
		}

		launchedURLs, isDelta := launchedURLsForOrigin(sess)
		if isDelta && launchedURLs == nil {
			continue
		}
		set := normalizedLaunchedURLSet(launchedURLs)
		for _, launchedURL := range launchedURLs {
			launchedURL, err := normalizer.Normalize(launchedURL)
			if err != nil || launchedURL == "" {
				continue
			}
			if _, seen := seenURLs[launchedURL]; !seen {
				seenURLs[launchedURL] = struct{}{}
				allURLs = append(allURLs, launchedURL)
			}
		}
		launchedBySession[sess.ID] = set
	}
	if len(allSessionIDs) == 0 || len(allURLs) == 0 {
		return result, nil
	}

	args := make([]interface{}, 0, len(allSessionIDs)+len(allURLs))
	args = appendStringPlaceholders(args, allSessionIDs)
	args = appendStringPlaceholders(args, allURLs)
	query := fmt.Sprintf(`
		SELECT toString(crawl_session_id), url, final_url, status_code, error
		FROM crawlobserver.pages FINAL
		WHERE toString(crawl_session_id) IN (%s) AND url IN (%s)`,
		sessionPlaceholders(len(allSessionIDs)), sessionPlaceholders(len(allURLs)))
	rows, err := s.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying session origin evidence: %w", err)
	}
	defer rows.Close()

	provedOriginsBySession := make(map[string]map[string]map[string]struct{}, len(allSessionIDs))
	for rows.Next() {
		var sessionID, requestedURL, finalURL, fetchError string
		var statusCode uint16
		if err := rows.Scan(&sessionID, &requestedURL, &finalURL, &statusCode, &fetchError); err != nil {
			return nil, fmt.Errorf("scanning session origin evidence: %w", err)
		}
		if statusCode == 0 || strings.TrimSpace(fetchError) != "" {
			continue
		}
		if _, ok := launchedBySession[sessionID][requestedURL]; !ok {
			continue
		}
		responseURL := finalURL
		if responseURL == "" {
			responseURL = requestedURL
		}
		origin, ok := normalizeEffectiveOrigin(responseURL)
		if !ok {
			continue
		}
		if provedOriginsBySession[sessionID] == nil {
			provedOriginsBySession[sessionID] = make(map[string]map[string]struct{})
		}
		if provedOriginsBySession[sessionID][requestedURL] == nil {
			provedOriginsBySession[sessionID][requestedURL] = make(map[string]struct{})
		}
		provedOriginsBySession[sessionID][requestedURL][origin] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating session origin evidence: %w", err)
	}

	for sessionID, launched := range launchedBySession {
		result[sessionID] = resolveEffectiveOrigin(launched, provedOriginsBySession[sessionID])
	}
	return result, nil
}

func normalizedLaunchedURLSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized, err := normalizer.Normalize(value)
		if err == nil && normalized != "" {
			result[normalized] = struct{}{}
		}
	}
	return result
}

func resolveEffectiveOrigin(launched map[string]struct{}, proved map[string]map[string]struct{}) EffectiveOrigin {
	origins := make(map[string]struct{})
	for requestedURL, requestedOrigins := range proved {
		if _, ok := launched[requestedURL]; !ok {
			continue
		}
		for origin := range requestedOrigins {
			origins[origin] = struct{}{}
		}
	}
	if len(origins) > 1 {
		return EffectiveOrigin{State: EffectiveOriginAmbiguous}
	}
	if len(launched) == 0 || len(proved) != len(launched) || len(origins) != 1 {
		return EffectiveOrigin{State: EffectiveOriginUnavailable}
	}
	for origin := range origins {
		return EffectiveOrigin{Origin: origin, State: EffectiveOriginProven}
	}
	return EffectiveOrigin{State: EffectiveOriginUnavailable}
}

func launchedURLsForOrigin(sess CrawlSession) ([]string, bool) {
	var saved config.Config
	if err := json.Unmarshal([]byte(sess.Config), &saved); err == nil && saved.Crawler.DeltaPlan != nil {
		return saved.Crawler.DeltaPlan.LaunchedURLs, true
	}
	if strings.EqualFold(strings.TrimSpace(sess.Label), "Daily Delta Crawl") {
		return nil, true
	}
	return sess.SeedURLs, false
}

func normalizeEffectiveOrigin(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	port := u.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host, true
}

func sessionPlaceholders(count int) string {
	if count <= 0 {
		return "NULL"
	}
	return strings.TrimRight(strings.Repeat("?,", count), ",")
}

func appendStringPlaceholders(args []interface{}, values []string) []interface{} {
	for _, value := range values {
		args = append(args, value)
	}
	return args
}

// ListSessions retrieves crawl sessions, optionally filtered by project ID.
func (s *Store) ListSessions(ctx context.Context, projectID ...string) ([]CrawlSession, error) {
	query := `
		SELECT id, started_at, finished_at, status, seed_urls, config, pages_crawled, user_agent, project_id, label
		FROM crawlobserver.crawl_sessions FINAL
		WHERE ` + nonSyntheticSessionFilter
	var args []interface{}
	if len(projectID) > 0 && projectID[0] != "" {
		query += ` AND project_id = ?`
		args = append(args, projectID[0])
	}
	query += ` ORDER BY started_at DESC`

	rows, err := s.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying sessions: %w", err)
	}
	defer rows.Close()

	var sessions []CrawlSession
	for rows.Next() {
		var sess CrawlSession
		if err := rows.Scan(
			&sess.ID, &sess.StartedAt, &sess.FinishedAt,
			&sess.Status, &sess.SeedURLs, &sess.Config,
			&sess.PagesCrawled, &sess.UserAgent, &sess.ProjectID, &sess.Label,
		); err != nil {
			return nil, fmt.Errorf("scanning session: %w", err)
		}
		sessions = append(sessions, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating sessions: %w", err)
	}
	return sessions, nil
}

// RetentionProtectedSessionIDs returns sessions that must not be pruned by
// automatic retention because other read models still reference them.
func (s *Store) RetentionProtectedSessionIDs(ctx context.Context) (map[string]struct{}, error) {
	protected := map[string]struct{}{}
	add := func(id string) {
		if isValidUUID(id) {
			protected[id] = struct{}{}
		}
	}

	rows, err := s.conn.Query(ctx, `
		SELECT project_id, toString(source_session_id), toString(content_watermark_session_id),
			toString(current_session_id), baseline_session_id, last_delta_session_id
		FROM crawlobserver.project_current_snapshot_promotions_v2 FINAL
		ORDER BY project_id, content_watermark_started_at DESC, toString(content_watermark_session_id) DESC,
			snapshot_revision DESC, updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("querying protected current snapshots: %w", err)
	}
	var seenProjects = map[string]struct{}{}
	for rows.Next() {
		var projectID, sourceID, watermarkID, currentID, baselineID, lastDeltaID string
		if err := rows.Scan(&projectID, &sourceID, &watermarkID, &currentID, &baselineID, &lastDeltaID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scanning protected current snapshots: %w", err)
		}
		if _, seen := seenProjects[projectID]; seen {
			continue
		}
		seenProjects[projectID] = struct{}{}
		add(sourceID)
		add(watermarkID)
		add(currentID)
		add(baselineID)
		add(lastDeltaID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterating protected current snapshots: %w", err)
	}
	rows.Close()

	rows, err = s.conn.Query(ctx, `
		SELECT toString(delta_session_id), toString(current_session_id)
		FROM crawlobserver.project_current_snapshot_deltas FINAL`)
	if err != nil {
		return nil, fmt.Errorf("querying protected current snapshot deltas: %w", err)
	}
	for rows.Next() {
		var deltaID, currentID string
		if err := rows.Scan(&deltaID, &currentID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scanning protected current snapshot deltas: %w", err)
		}
		add(deltaID)
		add(currentID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterating protected current snapshot deltas: %w", err)
	}
	rows.Close()

	rows, err = s.conn.Query(ctx, `
		SELECT toString(qr.session_id), qr.baseline_session_id
		FROM crawlobserver.crawl_quality_current_pointers AS pointer FINAL
		INNER JOIN crawlobserver.crawl_quality_evaluations AS qr
			ON qr.session_id = pointer.session_id AND qr.evaluation_revision = pointer.evaluation_revision
		WHERE qr.trusted = true AND qr.is_full_crawl = true`)
	if err != nil {
		return nil, fmt.Errorf("querying protected immutable quality baselines: %w", err)
	}
	for rows.Next() {
		var sessionID, baselineID string
		if err := rows.Scan(&sessionID, &baselineID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scanning protected immutable quality baselines: %w", err)
		}
		add(sessionID)
		add(baselineID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterating protected immutable quality baselines: %w", err)
	}
	rows.Close()

	// Keep legacy baselines protected until their first pointer-selected read
	// imports them into the immutable quality history.
	rows, err = s.conn.Query(ctx, `
		SELECT session_id, baseline_session_id
		FROM crawlobserver.crawl_quality_results FINAL
		WHERE trusted = true AND is_full_crawl = true`)
	if err != nil {
		return nil, fmt.Errorf("querying protected quality baselines: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sessionID, baselineID string
		if err := rows.Scan(&sessionID, &baselineID); err != nil {
			return nil, fmt.Errorf("scanning protected quality baselines: %w", err)
		}
		add(sessionID)
		add(baselineID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating protected quality baselines: %w", err)
	}
	return protected, nil
}

// ListSessionsPaginated retrieves crawl sessions with pagination, optional project and search filters.
func (s *Store) ListSessionsPaginated(ctx context.Context, limit, offset int, projectID, search string) ([]CrawlSession, int, error) {
	where := " WHERE " + nonSyntheticSessionFilter
	var args []interface{}
	if projectID != "" {
		where += ` AND project_id = ?`
		args = append(args, projectID)
	}
	if search != "" {
		where += ` AND arrayExists(x -> x ILIKE ?, seed_urls)`
		args = append(args, "%"+search+"%")
	}

	// Count
	var total uint64
	countArgs := make([]interface{}, len(args))
	copy(countArgs, args)
	if err := s.conn.QueryRow(ctx,
		`SELECT count() FROM crawlobserver.crawl_sessions FINAL`+where, countArgs...,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting sessions: %w", err)
	}

	// Fetch page
	query := `SELECT id, started_at, finished_at, status, seed_urls, config, pages_crawled, user_agent, project_id, label
		FROM crawlobserver.crawl_sessions FINAL` + where + ` ORDER BY started_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := s.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("querying sessions paginated: %w", err)
	}
	defer rows.Close()

	var sessions []CrawlSession
	for rows.Next() {
		var sess CrawlSession
		if err := rows.Scan(
			&sess.ID, &sess.StartedAt, &sess.FinishedAt,
			&sess.Status, &sess.SeedURLs, &sess.Config,
			&sess.PagesCrawled, &sess.UserAgent, &sess.ProjectID, &sess.Label,
		); err != nil {
			return nil, 0, fmt.Errorf("scanning session: %w", err)
		}
		sessions = append(sessions, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterating sessions: %w", err)
	}
	if sessions == nil {
		sessions = []CrawlSession{}
	}
	return sessions, int(total), nil
}

// GetSession retrieves a single crawl session by ID.
func (s *Store) GetSession(ctx context.Context, sessionID string) (*CrawlSession, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT id, started_at, finished_at, status, seed_urls, config, pages_crawled, user_agent, project_id, label
		FROM crawlobserver.crawl_sessions FINAL
		WHERE id = ?
	`, sessionID)

	var sess CrawlSession
	if err := row.Scan(
		&sess.ID, &sess.StartedAt, &sess.FinishedAt,
		&sess.Status, &sess.SeedURLs, &sess.Config,
		&sess.PagesCrawled, &sess.UserAgent, &sess.ProjectID, &sess.Label,
	); err != nil {
		return nil, fmt.Errorf("querying session %s: %w", sessionID, err)
	}
	return &sess, nil
}

// UpdateSessionProject re-inserts a session with a new project_id (ReplacingMergeTree pattern).
func (s *Store) UpdateSessionProject(ctx context.Context, sessionID string, projectID *string) error {
	sess, err := s.GetSession(ctx, sessionID)
	if err != nil {
		return err
	}
	sess.ProjectID = projectID
	return s.InsertSession(ctx, sess)
}

// UpdateSessionLabel re-inserts a session with a new label (ReplacingMergeTree pattern).
func (s *Store) UpdateSessionLabel(ctx context.Context, sessionID, label string) error {
	sess, err := s.GetSession(ctx, sessionID)
	if err != nil {
		return err
	}
	sess.Label = label
	return s.InsertSession(ctx, sess)
}

// DeleteSession deletes a crawl session and all its associated data.
// Uses DROP PARTITION for instant deletion on partitioned tables.
func (s *Store) DeleteSession(ctx context.Context, sessionID string) error {
	return s.deleteSession(ctx, sessionID, false)
}

func (s *Store) deleteSession(ctx context.Context, sessionID string, allowSnapshotProtected bool) error {
	if !allowSnapshotProtected {
		protected, reason, err := s.isSessionSnapshotProtected(ctx, sessionID)
		if err != nil {
			return err
		}
		if protected {
			return fmt.Errorf("cannot delete session %s: protected by current snapshot (%s)", sessionID, reason)
		}
	}

	// Drop partition on data tables (partitioned by crawl_session_id)
	dataTables := []string{
		"pages",
		"links",
		"robots_txt",
		"sitemaps",
		"sitemap_urls",
		"external_link_checks",
		"page_resource_checks",
		"page_resource_refs",
		"extractions",
		"near_duplicate_pairs",
		"retry_attempts",
		"structured_data_items",
		"hreflang_issues",
		"interlinking_opportunities",
		"interlinking_simulation_results",
		"interlinking_simulations",
		"page_embeddings",
		"crawl_quality_findings",
		"crawl_quality_evaluations",
		"crawl_quality_evaluation_findings",
		"crawl_quality_promotion_events",
		"crawl_quality_action_events",
		"pagerank_evidence",
	}
	for _, table := range dataTables {
		q := fmt.Sprintf("ALTER TABLE crawlobserver.%s DROP PARTITION ?", table)
		if err := s.conn.Exec(ctx, q, sessionID); err != nil {
			return fmt.Errorf("dropping partition on %s: %w", table, err)
		}
	}

	// crawl_sessions is not partitioned by session, use regular DELETE
	if err := s.conn.Exec(ctx, `ALTER TABLE crawlobserver.crawl_sessions DELETE WHERE id = ?`, sessionID); err != nil {
		return fmt.Errorf("deleting session row: %w", err)
	}
	if err := s.conn.Exec(ctx, `ALTER TABLE crawlobserver.crawl_quality_results DELETE WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("deleting quality result row: %w", err)
	}
	if err := s.conn.Exec(ctx, `ALTER TABLE crawlobserver.crawl_quality_current_pointers DELETE WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("deleting quality current pointer: %w", err)
	}

	return nil
}

func (s *Store) isSessionSnapshotProtected(ctx context.Context, sessionID string) (bool, string, error) {
	if !isValidUUID(sessionID) {
		return false, "", nil
	}
	if sess, err := s.GetSession(ctx, sessionID); err == nil {
		label := strings.TrimSpace(sess.Label)
		if label == CurrentSnapshotLabel || label == CurrentBaselineSnapshotLabel {
			return true, label, nil
		}
	}
	planProtected, err := s.isCurrentSnapshotDeltaPlanPredecessor(ctx, sessionID)
	if err != nil {
		return false, "", err
	}
	if planProtected {
		return true, "current_snapshot_delta_plan_predecessor", nil
	}

	var currentRefs uint64
	if err := s.conn.QueryRow(ctx, `
		SELECT count()
		FROM crawlobserver.project_current_snapshots FINAL
		WHERE toString(current_session_id) = ?
		   OR baseline_session_id = ?
		   OR last_delta_session_id = ?`,
		sessionID, sessionID, sessionID,
	).Scan(&currentRefs); err != nil {
		return false, "", fmt.Errorf("checking current snapshot references: %w", err)
	}
	if currentRefs > 0 {
		return true, "project_current_snapshots", nil
	}
	if err := s.conn.QueryRow(ctx, `
		SELECT count()
		FROM (
			SELECT source_session_id, content_watermark_session_id, current_session_id, baseline_session_id, last_delta_session_id
			FROM crawlobserver.project_current_snapshot_promotions_v2 FINAL
			ORDER BY project_id, content_watermark_started_at DESC, toString(content_watermark_session_id) DESC,
				snapshot_revision DESC, updated_at DESC
			LIMIT 1 BY project_id
		)
		WHERE toString(source_session_id) = ?
		   OR toString(content_watermark_session_id) = ?
		   OR toString(current_session_id) = ?
		   OR baseline_session_id = ?
		   OR last_delta_session_id = ?`,
		sessionID, sessionID, sessionID, sessionID, sessionID,
	).Scan(&currentRefs); err != nil {
		return false, "", fmt.Errorf("checking current snapshot promotion references: %w", err)
	}
	if currentRefs > 0 {
		return true, "project_current_snapshot_promotions_v2", nil
	}

	var deltaRefs uint64
	if err := s.conn.QueryRow(ctx, `
		SELECT count()
		FROM crawlobserver.project_current_snapshot_deltas FINAL
		WHERE toString(delta_session_id) = ?
		   OR toString(current_session_id) = ?`,
		sessionID, sessionID,
	).Scan(&deltaRefs); err != nil {
		return false, "", fmt.Errorf("checking current snapshot delta references: %w", err)
	}
	if deltaRefs > 0 {
		return true, "project_current_snapshot_deltas", nil
	}
	return false, "", nil
}

// The predecessor raw session can already be pruned, so protection is derived
// from the live D2 DeltaPlan and journal rather than session-label heuristics.
func (s *Store) isCurrentSnapshotDeltaPlanPredecessor(ctx context.Context, sessionID string) (bool, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT toString(content_watermark_session_id)
		FROM crawlobserver.project_current_snapshot_promotions_v2 FINAL
		ORDER BY project_id, content_watermark_started_at DESC, toString(content_watermark_session_id) DESC,
			snapshot_revision DESC, updated_at DESC
		LIMIT 1 BY project_id`)
	if err != nil {
		return false, fmt.Errorf("querying current delta plan predecessors: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var watermarkID string
		if err := rows.Scan(&watermarkID); err != nil {
			return false, err
		}
		watermark, err := s.GetSession(ctx, watermarkID)
		if err != nil {
			continue
		}
		var cfg config.Config
		if err := json.Unmarshal([]byte(watermark.Config), &cfg); err != nil {
			return false, fmt.Errorf("decoding current delta plan predecessor: %w", err)
		}
		if cfg.Crawler.DeltaPlan != nil && cfg.Crawler.DeltaPlan.BaselineContentWatermarkSessionID == sessionID {
			return true, nil
		}
	}
	return false, rows.Err()
}

// PageRankEntry holds a URL and its PageRank score.
type PageRankEntry struct {
	URL      string  `json:"url"`
	PageRank float64 `json:"pagerank"`
}

// SessionStats holds aggregate stats for a crawl session.
type SessionStats struct {
	TotalPages            uint64            `json:"total_pages"`
	TotalLinks            uint64            `json:"total_links"`
	InternalLinks         uint64            `json:"internal_links"`
	ExternalLinks         uint64            `json:"external_links"`
	AvgFetchMs            float64           `json:"avg_fetch_ms"`
	ErrorCount            uint64            `json:"error_count"`
	StatusCodes           map[uint16]uint64 `json:"status_codes"`
	DepthDistribution     map[uint16]uint64 `json:"depth_distribution"`
	PagesPerSecond        float64           `json:"pages_per_second"`
	CrawlDurationSec      float64           `json:"crawl_duration_sec"`
	TopPageRank           []PageRankEntry   `json:"top_pagerank"`
	JSRenderedPages       uint64            `json:"js_rendered_pages"`
	JSChangedTitleCount   uint64            `json:"js_changed_title_count"`
	JSChangedH1Count      uint64            `json:"js_changed_h1_count"`
	JSChangedContentCount uint64            `json:"js_changed_content_count"`
	AvgJSRenderMs         float64           `json:"avg_js_render_ms"`
	UniqueExtDomains      uint64            `json:"unique_ext_domains"`
}

// SessionStats retrieves aggregate statistics for a crawl session.
func (s *Store) SessionStats(ctx context.Context, sessionID string) (*SessionStats, error) {
	stats := &SessionStats{
		StatusCodes:       make(map[uint16]uint64),
		DepthDistribution: make(map[uint16]uint64),
	}

	// Page stats
	row := s.conn.QueryRow(ctx, `
		SELECT count(), avg(fetch_duration_ms), countIf(error != '')
		FROM crawlobserver.pages FINAL WHERE crawl_session_id = ? AND `+notRedirectedFilter, sessionID)
	if err := row.Scan(&stats.TotalPages, &stats.AvgFetchMs, &stats.ErrorCount); err != nil {
		return nil, fmt.Errorf("querying page stats: %w", err)
	}
	if math.IsNaN(stats.AvgFetchMs) {
		stats.AvgFetchMs = 0
	}

	// Link stats
	row = s.conn.QueryRow(ctx, `
		SELECT count(), countIf(is_internal = true), countIf(is_internal = false)
		FROM crawlobserver.links WHERE crawl_session_id = ?`, sessionID)
	if err := row.Scan(&stats.TotalLinks, &stats.InternalLinks, &stats.ExternalLinks); err != nil {
		return nil, fmt.Errorf("querying link stats: %w", err)
	}

	// Unique external domains (using PSL-aware function)
	extRow := s.conn.QueryRow(ctx, `
		SELECT count(DISTINCT cutToFirstSignificantSubdomain(target_url))
		FROM crawlobserver.links
		WHERE crawl_session_id = ? AND is_internal = false`, sessionID)
	if err := extRow.Scan(&stats.UniqueExtDomains); err != nil {
		return nil, fmt.Errorf("querying unique ext domains: %w", err)
	}

	// Status code distribution
	rows, err := s.conn.Query(ctx, `
		SELECT status_code, count() FROM crawlobserver.pages FINAL
		WHERE crawl_session_id = ? GROUP BY status_code ORDER BY status_code`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("querying status codes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var code uint16
		var cnt uint64
		if err := rows.Scan(&code, &cnt); err != nil {
			return nil, err
		}
		stats.StatusCodes[code] = cnt
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating status codes: %w", err)
	}

	// Depth distribution
	depthRows, err := s.conn.Query(ctx, `
		SELECT depth, count() FROM crawlobserver.pages FINAL
		WHERE crawl_session_id = ? AND content_type LIKE '%html%' AND `+notRedirectedFilter+`
		GROUP BY depth ORDER BY depth`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("querying depth distribution: %w", err)
	}
	defer depthRows.Close()
	for depthRows.Next() {
		var depth uint16
		var cnt uint64
		if err := depthRows.Scan(&depth, &cnt); err != nil {
			return nil, err
		}
		stats.DepthDistribution[depth] = cnt
	}
	if err := depthRows.Err(); err != nil {
		return nil, fmt.Errorf("iterating depth distribution: %w", err)
	}

	// Crawl duration and pages/sec
	var startedAt, finishedAt time.Time
	var status string
	durRow := s.conn.QueryRow(ctx, `
		SELECT started_at, finished_at, status
		FROM crawlobserver.crawl_sessions FINAL
		WHERE id = ?`, sessionID)
	if err := durRow.Scan(&startedAt, &finishedAt, &status); err == nil {
		if status == "running" {
			stats.CrawlDurationSec = time.Since(startedAt).Seconds()
			// Live pages/sec = pages crawled in last 60s / 60
			var recentCount uint64
			recentRow := s.conn.QueryRow(ctx, `
				SELECT count()
				FROM crawlobserver.pages FINAL
				WHERE crawl_session_id = ?
				  AND crawled_at >= now() - INTERVAL 60 SECOND
				  AND `+notRedirectedFilter, sessionID)
			if err := recentRow.Scan(&recentCount); err == nil && recentCount > 0 {
				stats.PagesPerSecond = float64(recentCount) / 60.0
			}
		} else if !finishedAt.IsZero() && finishedAt.After(startedAt) {
			stats.CrawlDurationSec = finishedAt.Sub(startedAt).Seconds()
			if stats.CrawlDurationSec > 0 {
				stats.PagesPerSecond = float64(stats.TotalPages) / stats.CrawlDurationSec
			}
		}
	}

	// Top PageRank
	prRows, err := s.conn.Query(ctx, `
		SELECT url, pagerank FROM crawlobserver.pages FINAL
		WHERE crawl_session_id = ? AND pagerank > 0 AND `+notRedirectedFilter+`
		ORDER BY pagerank DESC LIMIT 20`, sessionID)
	if err == nil {
		defer prRows.Close()
		for prRows.Next() {
			var e PageRankEntry
			if err := prRows.Scan(&e.URL, &e.PageRank); err == nil {
				stats.TopPageRank = append(stats.TopPageRank, e)
			}
		}
		if err := prRows.Err(); err != nil {
			applog.Warnf("audit", "iterating top pagerank: %v", err)
		}
	}

	// JS rendering stats
	jsRow := s.conn.QueryRow(ctx, `
		SELECT countIf(js_rendered),
			countIf(js_changed_title),
			countIf(js_changed_h1),
			countIf(js_changed_content),
			avgIf(js_render_duration_ms, js_rendered)
		FROM crawlobserver.pages FINAL WHERE crawl_session_id = ? AND `+notRedirectedFilter, sessionID)
	if err := jsRow.Scan(&stats.JSRenderedPages, &stats.JSChangedTitleCount,
		&stats.JSChangedH1Count, &stats.JSChangedContentCount, &stats.AvgJSRenderMs); err != nil {
		applog.Warnf("storage", "querying JS render stats: %v", err)
	}
	if math.IsNaN(stats.AvgJSRenderMs) {
		stats.AvgJSRenderMs = 0
	}

	return stats, nil
}

// --- Audit types and method ---

// AuditContent holds content-related audit metrics.
type AuditContent struct {
	Total                 uint64 `json:"total"`
	HTMLPages             uint64 `json:"html_pages"`
	TitleMissing          uint64 `json:"title_missing"`
	TitleTooLong          uint64 `json:"title_too_long"`
	TitleTooShort         uint64 `json:"title_too_short"`
	TitleDuplicates       uint64 `json:"title_duplicates"`
	GenericRenderedTitle  uint64 `json:"generic_rendered_title"`
	GenericStaticMetadata uint64 `json:"generic_static_metadata"`
	MetaDescMissing       uint64 `json:"meta_desc_missing"`
	MetaDescTooLong       uint64 `json:"meta_desc_too_long"`
	MetaDescTooShort      uint64 `json:"meta_desc_too_short"`
	H1Missing             uint64 `json:"h1_missing"`
	H1Multiple            uint64 `json:"h1_multiple"`
	ThinUnder100          uint64 `json:"thin_under_100"`
	Thin100300            uint64 `json:"thin_100_300"`
	ImagesTotal           uint64 `json:"images_total"`
	ImagesNoAltTotal      uint64 `json:"images_no_alt_total"`
	PagesWithImagesNoAlt  uint64 `json:"pages_with_images_no_alt"`
}

// NoindexReason is a reason + count for non-indexable pages.
type NoindexReason struct {
	Reason string `json:"reason"`
	Count  uint64 `json:"count"`
}

// ContentTypeCount is a content type + count.
type ContentTypeCount struct {
	ContentType string `json:"content_type"`
	Count       uint64 `json:"count"`
}

// AuditTechnical holds technical audit metrics.
type AuditTechnical struct {
	Indexable                   uint64                `json:"indexable"`
	NonIndexable                uint64                `json:"non_indexable"`
	Soft404                     uint64                `json:"soft_404"`
	SharedRenderedMetadataShell uint64                `json:"shared_rendered_metadata_shell"`
	CanonicalSelf               uint64                `json:"canonical_self"`
	CanonicalOther              uint64                `json:"canonical_other"`
	CanonicalMissing            uint64                `json:"canonical_missing"`
	HasRedirect                 uint64                `json:"has_redirect"`
	RedirectChainsOver2         uint64                `json:"redirect_chains_over_2"`
	ResponseFast                uint64                `json:"response_fast"`
	ResponseOK                  uint64                `json:"response_ok"`
	ResponseSlow                uint64                `json:"response_slow"`
	ResponseVerySlow            uint64                `json:"response_very_slow"`
	ErrorPages                  uint64                `json:"error_pages"`
	NoindexReasons              []NoindexReason       `json:"noindex_reasons"`
	ContentTypes                []ContentTypeCount    `json:"content_types"`
	CoreWebVitals               *CoreWebVitalsSummary `json:"core_web_vitals"`
}

// ExternalDomain is a domain + link count.
type ExternalDomain struct {
	Domain string `json:"domain"`
	Count  uint64 `json:"count"`
}

// AnchorCount is an anchor text + count.
type AnchorCount struct {
	Anchor string `json:"anchor"`
	Count  uint64 `json:"count"`
}

// AuditLinks holds link audit metrics.
type AuditLinks struct {
	TotalInternal        uint64           `json:"total_internal"`
	TotalExternal        uint64           `json:"total_external"`
	ExternalNofollow     uint64           `json:"external_nofollow"`
	ExternalDofollow     uint64           `json:"external_dofollow"`
	PagesNoInternalOut   uint64           `json:"pages_no_internal_out"`
	PagesHighInternalOut uint64           `json:"pages_high_internal_out"`
	PagesNoExternal      uint64           `json:"pages_no_external"`
	BrokenInternal       uint64           `json:"broken_internal"`
	TopExternalDomains   []ExternalDomain `json:"top_external_domains"`
	TopAnchors           []AnchorCount    `json:"top_anchors"`
}

// DirectoryCount is a URL directory prefix + count.
type DirectoryCount struct {
	Directory string `json:"directory"`
	Count     uint64 `json:"count"`
}

// AuditStructure holds site structure audit metrics.
type AuditStructure struct {
	Directories []DirectoryCount `json:"directories"`
	OrphanPages uint64           `json:"orphan_pages"`
}

// AuditSitemaps holds sitemap coverage audit metrics.
type AuditSitemaps struct {
	InBoth           uint64 `json:"in_both"`
	CrawledOnly      uint64 `json:"crawled_only"`
	SitemapOnly      uint64 `json:"sitemap_only"`
	TotalSitemapURLs uint64 `json:"total_sitemap_urls"`
}

// LangCount is a language + count.
type LangCount struct {
	Lang  string `json:"lang"`
	Count uint64 `json:"count"`
}

// SchemaCount is a schema type + count.
type SchemaCount struct {
	SchemaType string `json:"schema_type"`
	Count      uint64 `json:"count"`
}

// AuditInternational holds international/schema audit metrics.
type AuditInternational struct {
	PagesWithHreflang  uint64        `json:"pages_with_hreflang"`
	PagesWithLang      uint64        `json:"pages_with_lang"`
	PagesWithSchema    uint64        `json:"pages_with_schema"`
	LangDistribution   []LangCount   `json:"lang_distribution"`
	SchemaDistribution []SchemaCount `json:"schema_distribution"`
}

// AuditResult is the combined audit result.
type AuditResult struct {
	Content       *AuditContent       `json:"content"`
	Technical     *AuditTechnical     `json:"technical"`
	Links         *AuditLinks         `json:"links"`
	Structure     *AuditStructure     `json:"structure"`
	Sitemaps      *AuditSitemaps      `json:"sitemaps"`
	International *AuditInternational `json:"international"`
}

// SessionAudit computes a comprehensive SEO audit for a crawl session.
func (s *Store) SessionAudit(ctx context.Context, sessionID string) (*AuditResult, error) {
	result := &AuditResult{}

	// --- Content audit ---
	content := &AuditContent{}
	// All content-quality counts (title, meta, h1, thin content) are restricted to
	// HTML pages with status 2xx — images, PDFs, JS, CSS etc. have no <title>
	// by design and must not inflate "missing" buckets. The `total` here is the
	// count of auditable HTML pages; raw page totals are reported elsewhere.
	row := s.conn.QueryRow(ctx, `
		SELECT countIf(content_type LIKE '%html%' AND status_code >= 200 AND status_code < 300) AS total,
			countIf(content_type LIKE '%html%' AND status_code >= 200 AND status_code < 300) AS html_pages,
			countIf(content_type LIKE '%html%' AND status_code >= 200 AND status_code < 300 AND title = '') AS title_missing,
			countIf(content_type LIKE '%html%' AND status_code >= 200 AND status_code < 300 AND title_length > 60) AS title_too_long,
			countIf(content_type LIKE '%html%' AND status_code >= 200 AND status_code < 300 AND title_length > 0 AND title_length < 30) AS title_too_short,
			countIf(content_type LIKE '%html%' AND status_code >= 200 AND status_code < 300 AND meta_description = '') AS meta_desc_missing,
			countIf(content_type LIKE '%html%' AND status_code >= 200 AND status_code < 300 AND meta_desc_length > 160) AS meta_desc_too_long,
			countIf(content_type LIKE '%html%' AND status_code >= 200 AND status_code < 300 AND meta_desc_length > 0 AND meta_desc_length < 70) AS meta_desc_too_short,
			countIf(content_type LIKE '%html%' AND status_code >= 200 AND status_code < 300 AND length(h1) = 0) AS h1_missing,
			countIf(content_type LIKE '%html%' AND status_code >= 200 AND status_code < 300 AND length(h1) > 1) AS h1_multiple,
			countIf(content_type LIKE '%html%' AND status_code >= 200 AND status_code < 300 AND word_count < 100) AS thin_under_100,
			countIf(content_type LIKE '%html%' AND status_code >= 200 AND status_code < 300 AND word_count >= 100 AND word_count < 300) AS thin_100_300,
			sumIf(images_count, content_type LIKE '%html%' AND status_code >= 200 AND status_code < 300) AS images_total,
			sumIf(images_no_alt, content_type LIKE '%html%' AND status_code >= 200 AND status_code < 300) AS images_no_alt_total,
			countIf(content_type LIKE '%html%' AND status_code >= 200 AND status_code < 300 AND images_no_alt > 0) AS pages_with_images_no_alt
		FROM crawlobserver.pages FINAL WHERE crawl_session_id = ? AND `+notRedirectedFilter, sessionID)
	if err := row.Scan(
		&content.Total, &content.HTMLPages,
		&content.TitleMissing, &content.TitleTooLong, &content.TitleTooShort,
		&content.MetaDescMissing, &content.MetaDescTooLong, &content.MetaDescTooShort,
		&content.H1Missing, &content.H1Multiple,
		&content.ThinUnder100, &content.Thin100300,
		&content.ImagesTotal, &content.ImagesNoAltTotal, &content.PagesWithImagesNoAlt,
	); err != nil {
		return nil, fmt.Errorf("audit content: %w", err)
	}

	// Title duplicates
	dupRow := s.conn.QueryRow(ctx, `
		SELECT sum(cnt - 1) FROM (
			SELECT title, count() AS cnt FROM crawlobserver.pages FINAL
			WHERE crawl_session_id = ? AND content_type LIKE '%html%'
				AND status_code >= 200 AND status_code < 300
				AND title != '' AND `+notRedirectedFilter+`
			GROUP BY title HAVING cnt > 1
		)`, sessionID)
	var titleDups int64
	if err := dupRow.Scan(&titleDups); err != nil {
		applog.Warnf("audit", "scan title duplicates: %v", err)
	} else if titleDups > 0 {
		content.TitleDuplicates = uint64(titleDups)
	}
	result.Content = content

	// --- Technical audit ---
	// Indexability and canonical metrics only make sense on HTML 2xx pages —
	// images, PDFs, CSS/JS don't carry <meta robots> or <link rel=canonical>.
	// Redirects, response times and errors are meaningful for any resource type
	// and are counted across the full crawl.
	tech := &AuditTechnical{}
	techRow := s.conn.QueryRow(ctx, `
		SELECT
			countIf(content_type LIKE '%html%' AND status_code >= 200 AND status_code < 300 AND is_indexable = true) AS indexable,
			countIf(content_type LIKE '%html%' AND status_code >= 200 AND status_code < 300 AND is_indexable = false) AS non_indexable,
			countIf(content_type LIKE '%html%' AND status_code >= 200 AND status_code < 300 AND canonical_is_self = true) AS canonical_self,
			countIf(content_type LIKE '%html%' AND status_code >= 200 AND status_code < 300 AND canonical != '' AND canonical_is_self = false) AS canonical_other,
			countIf(content_type LIKE '%html%' AND status_code >= 200 AND status_code < 300 AND canonical = '') AS canonical_missing,
			countIf(length(redirect_chain) > 0) AS has_redirect,
			countIf(length(redirect_chain) > 2) AS redirect_chains_over_2,
			countIf(fetch_duration_ms < 200) AS response_fast,
			countIf(fetch_duration_ms >= 200 AND fetch_duration_ms < 500) AS response_ok,
			countIf(fetch_duration_ms >= 500 AND fetch_duration_ms < 1000) AS response_slow,
			countIf(fetch_duration_ms >= 1000) AS response_very_slow,
			countIf(error != '') AS error_pages
		FROM crawlobserver.pages FINAL WHERE crawl_session_id = ? AND `+notRedirectedFilter, sessionID)
	if err := techRow.Scan(
		&tech.Indexable, &tech.NonIndexable,
		&tech.CanonicalSelf, &tech.CanonicalOther, &tech.CanonicalMissing,
		&tech.HasRedirect, &tech.RedirectChainsOver2,
		&tech.ResponseFast, &tech.ResponseOK, &tech.ResponseSlow, &tech.ResponseVerySlow,
		&tech.ErrorPages,
	); err != nil {
		return nil, fmt.Errorf("audit technical: %w", err)
	}

	soft404, sharedRenderedMetadataShell, genericRenderedTitle, genericStaticMetadata, err := s.pageIssueCounts(ctx, sessionID)
	if err != nil {
		applog.Warnf("audit", "scan page issues: %v", err)
	} else {
		tech.Soft404 = soft404
		tech.SharedRenderedMetadataShell = sharedRenderedMetadataShell
		content.GenericRenderedTitle = genericRenderedTitle
		content.GenericStaticMetadata = genericStaticMetadata
	}

	tech.CoreWebVitals = &CoreWebVitalsSummary{}
	cwvRow := s.conn.QueryRow(ctx, `
		SELECT
			count() AS eligible_pages,
			countIf(`+cwvValidMeasurementFilter+`) AS measured_pages,
			countIf(`+cwvValidMeasurementFilter+` AND cwv_lcp_ms <= 2500 AND cwv_cls <= 0.1 AND cwv_ttfb_ms <= 800) AS good,
			countIf(`+cwvValidMeasurementFilter+` AND NOT (cwv_lcp_ms > 4000 OR cwv_cls > 0.25 OR cwv_ttfb_ms > 1800)
				AND (cwv_lcp_ms > 2500 OR cwv_cls > 0.1 OR cwv_ttfb_ms > 800)) AS needs_improvement,
			countIf(`+cwvValidMeasurementFilter+` AND (cwv_lcp_ms > 4000 OR cwv_cls > 0.25 OR cwv_ttfb_ms > 1800)) AS poor
		FROM crawlobserver.pages FINAL
		WHERE crawl_session_id = ?
			AND content_type LIKE '%html%'
			AND status_code >= 200 AND status_code < 300
			AND `+notRedirectedFilter, sessionID)
	if err := cwvRow.Scan(
		&tech.CoreWebVitals.EligiblePages,
		&tech.CoreWebVitals.MeasuredPages,
		&tech.CoreWebVitals.Good,
		&tech.CoreWebVitals.NeedsImprovement,
		&tech.CoreWebVitals.Poor,
	); err != nil {
		applog.Warnf("audit", "scan core web vitals summary: %v", err)
	} else if tech.CoreWebVitals.EligiblePages > tech.CoreWebVitals.MeasuredPages {
		tech.CoreWebVitals.UnmeasuredPages = tech.CoreWebVitals.EligiblePages - tech.CoreWebVitals.MeasuredPages
	}

	// Noindex reasons — restricted to HTML 2xx so non-HTML resources don't
	// show up as "noindex because non-HTML" and inflate the top reasons.
	niRows, err := s.conn.Query(ctx, `
		SELECT index_reason, count() AS cnt FROM crawlobserver.pages FINAL
		WHERE crawl_session_id = ? AND content_type LIKE '%html%'
			AND status_code >= 200 AND status_code < 300
			AND is_indexable = false AND index_reason != '' AND `+notRedirectedFilter+`
		GROUP BY index_reason ORDER BY cnt DESC`, sessionID)
	if err == nil {
		defer niRows.Close()
		for niRows.Next() {
			var nr NoindexReason
			if err := niRows.Scan(&nr.Reason, &nr.Count); err == nil {
				tech.NoindexReasons = append(tech.NoindexReasons, nr)
			}
		}
		if err := niRows.Err(); err != nil {
			applog.Warnf("audit", "iterating noindex reasons: %v", err)
		}
	}

	// Content types
	ctRows, err := s.conn.Query(ctx, `
		SELECT content_type, count() AS cnt FROM crawlobserver.pages FINAL
		WHERE crawl_session_id = ? AND `+notRedirectedFilter+`
		GROUP BY content_type ORDER BY cnt DESC LIMIT 20`, sessionID)
	if err == nil {
		defer ctRows.Close()
		for ctRows.Next() {
			var ct ContentTypeCount
			if err := ctRows.Scan(&ct.ContentType, &ct.Count); err == nil {
				tech.ContentTypes = append(tech.ContentTypes, ct)
			}
		}
		if err := ctRows.Err(); err != nil {
			applog.Warnf("audit", "iterating content types: %v", err)
		}
	}
	result.Technical = tech

	// --- Links audit ---
	links := &AuditLinks{}
	linkRow := s.conn.QueryRow(ctx, `
		SELECT
			countIf(is_internal = true) AS total_internal,
			countIf(is_internal = false) AS total_external,
			countIf(is_internal = false AND rel LIKE '%nofollow%') AS external_nofollow,
			countIf(is_internal = false AND (rel = '' OR rel NOT LIKE '%nofollow%')) AS external_dofollow
		FROM crawlobserver.links WHERE crawl_session_id = ?`, sessionID)
	if err := linkRow.Scan(&links.TotalInternal, &links.TotalExternal, &links.ExternalNofollow, &links.ExternalDofollow); err != nil {
		return nil, fmt.Errorf("audit links: %w", err)
	}

	// Pages link distribution — restricted to HTML 2xx pages. Non-HTML
	// resources (images, PDFs, JS, CSS...) never contain anchor tags, so
	// reporting them as "pages with no outgoing links" is misleading.
	pageDistRow := s.conn.QueryRow(ctx, `
		SELECT
			countIf(content_type LIKE '%html%' AND status_code >= 200 AND status_code < 300 AND internal_links_out = 0) AS pages_no_internal_out,
			countIf(content_type LIKE '%html%' AND status_code >= 200 AND status_code < 300 AND internal_links_out > 100) AS pages_high_internal_out,
			countIf(content_type LIKE '%html%' AND status_code >= 200 AND status_code < 300 AND external_links_out = 0) AS pages_no_external
		FROM crawlobserver.pages FINAL WHERE crawl_session_id = ? AND `+notRedirectedFilter, sessionID)
	if err := pageDistRow.Scan(&links.PagesNoInternalOut, &links.PagesHighInternalOut, &links.PagesNoExternal); err != nil {
		applog.Warnf("audit", "scan link distribution: %v", err)
	}

	// Broken internal links (LEFT ANTI JOIN to avoid in-memory hash set on large datasets)
	brokenRow := s.conn.QueryRow(ctx, `
		SELECT count(DISTINCT l.target_url)
		FROM crawlobserver.links AS l
		LEFT ANTI JOIN crawlobserver.pages AS p FINAL
			ON p.crawl_session_id = l.crawl_session_id AND p.url = l.target_url
		WHERE l.crawl_session_id = ? AND l.is_internal = true`,
		sessionID)
	if err := brokenRow.Scan(&links.BrokenInternal); err != nil {
		applog.Warnf("audit", "scan broken internal links: %v", err)
	}

	// Top external domains
	edRows, err := s.conn.Query(ctx, `
		SELECT domain(target_url) AS d, count() AS cnt FROM crawlobserver.links
		WHERE crawl_session_id = ? AND is_internal = false
		GROUP BY d ORDER BY cnt DESC LIMIT 20`, sessionID)
	if err == nil {
		defer edRows.Close()
		for edRows.Next() {
			var ed ExternalDomain
			if err := edRows.Scan(&ed.Domain, &ed.Count); err == nil {
				links.TopExternalDomains = append(links.TopExternalDomains, ed)
			}
		}
		if err := edRows.Err(); err != nil {
			applog.Warnf("audit", "iterating external domains: %v", err)
		}
	}

	// Top anchor texts
	anRows, err := s.conn.Query(ctx, `
		SELECT anchor_text, count() AS cnt FROM crawlobserver.links
		WHERE crawl_session_id = ? AND is_internal = true AND anchor_text != ''
		GROUP BY anchor_text ORDER BY cnt DESC LIMIT 20`, sessionID)
	if err == nil {
		defer anRows.Close()
		for anRows.Next() {
			var ac AnchorCount
			if err := anRows.Scan(&ac.Anchor, &ac.Count); err == nil {
				links.TopAnchors = append(links.TopAnchors, ac)
			}
		}
		if err := anRows.Err(); err != nil {
			applog.Warnf("audit", "iterating anchors: %v", err)
		}
	}
	result.Links = links

	// --- Structure audit ---
	structure := &AuditStructure{}

	// Directories (by URL path prefix up to 2nd segment)
	dirRows, err := s.conn.Query(ctx, `
		SELECT
			concat('/', arrayStringConcat(arraySlice(splitByChar('/', pathFull(url)), 2, 1), '/'), '/') AS dir,
			count() AS cnt
		FROM crawlobserver.pages FINAL
		WHERE crawl_session_id = ? AND content_type LIKE '%html%' AND `+notRedirectedFilter+`
		GROUP BY dir ORDER BY cnt DESC LIMIT 50`, sessionID)
	if err == nil {
		defer dirRows.Close()
		for dirRows.Next() {
			var dc DirectoryCount
			if err := dirRows.Scan(&dc.Directory, &dc.Count); err == nil {
				structure.Directories = append(structure.Directories, dc)
			}
		}
		if err := dirRows.Err(); err != nil {
			applog.Warnf("audit", "iterating directories: %v", err)
		}
	}

	// Orphan pages (LEFT ANTI JOIN to avoid in-memory hash set on large datasets)
	orphanRow := s.conn.QueryRow(ctx, `
		SELECT count()
		FROM crawlobserver.pages AS p FINAL
		LEFT ANTI JOIN (
			SELECT DISTINCT target_url
			FROM crawlobserver.links
			WHERE crawl_session_id = ? AND is_internal = true
		) AS l ON p.url = l.target_url
		WHERE p.crawl_session_id = ? AND p.content_type LIKE '%html%'
		  AND (p.final_url = '' OR p.final_url = p.url)`, sessionID, sessionID)
	if err := orphanRow.Scan(&structure.OrphanPages); err != nil {
		applog.Warnf("audit", "scan orphan pages: %v", err)
	}
	result.Structure = structure

	// --- Sitemaps audit ---
	sitemaps := &AuditSitemaps{}
	smRow := s.conn.QueryRow(ctx, `
		SELECT count(DISTINCT loc) FROM crawlobserver.sitemap_urls
		WHERE crawl_session_id = ?`, sessionID)
	if err := smRow.Scan(&sitemaps.TotalSitemapURLs); err != nil {
		applog.Warnf("audit", "scan sitemap URL count: %v", err)
	}

	if sitemaps.TotalSitemapURLs > 0 {
		// Sitemap coverage is meaningful only for HTML 2xx pages — sitemaps
		// list indexable content, not resources (images, JS, CSS, PDFs).
		// Counting raw crawled URLs in "Crawl only" would lump every image
		// into the bucket and break the coverage signal.
		var inBoth uint64
		ibRow := s.conn.QueryRow(ctx, `
			SELECT count() FROM (
				SELECT DISTINCT loc FROM crawlobserver.sitemap_urls WHERE crawl_session_id = ?
			) AS sm WHERE sm.loc IN (
				SELECT url FROM crawlobserver.pages FINAL
				WHERE crawl_session_id = ?
					AND content_type LIKE '%html%'
					AND status_code >= 200 AND status_code < 300
					AND `+notRedirectedFilter+`
			)`, sessionID, sessionID)
		if ibRow.Scan(&inBoth) == nil {
			sitemaps.InBoth = inBoth
			var totalCrawled uint64
			tcRow := s.conn.QueryRow(ctx, `
				SELECT count() FROM crawlobserver.pages FINAL
				WHERE crawl_session_id = ?
					AND content_type LIKE '%html%'
					AND status_code >= 200 AND status_code < 300
					AND `+notRedirectedFilter, sessionID)
			if err := tcRow.Scan(&totalCrawled); err != nil {
				applog.Warnf("audit", "scan total crawled for sitemap coverage: %v", err)
			}
			sitemaps.CrawledOnly = totalCrawled - inBoth
			sitemaps.SitemapOnly = sitemaps.TotalSitemapURLs - inBoth
		}
	}
	result.Sitemaps = sitemaps

	// --- International audit ---
	// hreflang, lang and schema markup only live on HTML pages; count on
	// HTML 2xx only so resources don't dilute the denominator.
	intl := &AuditInternational{}
	intlRow := s.conn.QueryRow(ctx, `
		SELECT
			countIf(content_type LIKE '%html%' AND status_code >= 200 AND status_code < 300 AND length(hreflang) > 0) AS pages_with_hreflang,
			countIf(content_type LIKE '%html%' AND status_code >= 200 AND status_code < 300 AND lang != '') AS pages_with_lang,
			countIf(content_type LIKE '%html%' AND status_code >= 200 AND status_code < 300 AND length(schema_types) > 0) AS pages_with_schema
		FROM crawlobserver.pages FINAL WHERE crawl_session_id = ? AND `+notRedirectedFilter, sessionID)
	if err := intlRow.Scan(&intl.PagesWithHreflang, &intl.PagesWithLang, &intl.PagesWithSchema); err != nil {
		applog.Warnf("audit", "scan international stats: %v", err)
	}

	// Lang distribution
	langRows, err := s.conn.Query(ctx, `
		SELECT lang, count() AS cnt FROM crawlobserver.pages FINAL
		WHERE crawl_session_id = ? AND lang != '' AND `+notRedirectedFilter+`
		GROUP BY lang ORDER BY cnt DESC LIMIT 20`, sessionID)
	if err == nil {
		defer langRows.Close()
		for langRows.Next() {
			var lc LangCount
			if err := langRows.Scan(&lc.Lang, &lc.Count); err == nil {
				intl.LangDistribution = append(intl.LangDistribution, lc)
			}
		}
		if err := langRows.Err(); err != nil {
			applog.Warnf("audit", "iterating languages: %v", err)
		}
	}

	// Schema distribution
	schemaRows, err := s.conn.Query(ctx, `
		SELECT arrayJoin(schema_types) AS st, count() AS cnt FROM crawlobserver.pages FINAL
		WHERE crawl_session_id = ? AND length(schema_types) > 0 AND `+notRedirectedFilter+`
		GROUP BY st ORDER BY cnt DESC LIMIT 20`, sessionID)
	if err == nil {
		defer schemaRows.Close()
		for schemaRows.Next() {
			var sc SchemaCount
			if err := schemaRows.Scan(&sc.SchemaType, &sc.Count); err == nil {
				intl.SchemaDistribution = append(intl.SchemaDistribution, sc)
			}
		}
		if err := schemaRows.Err(); err != nil {
			applog.Warnf("audit", "iterating schemas: %v", err)
		}
	}
	result.International = intl

	return result, nil
}

// TableStorageStats holds storage stats for a single table.
type TableStorageStats struct {
	Name        string `json:"name"`
	BytesOnDisk uint64 `json:"bytes_on_disk"`
	Rows        uint64 `json:"rows"`
}

// StorageStatsResult holds storage stats for all tables.
type StorageStatsResult struct {
	Tables []TableStorageStats `json:"tables"`
}

// StorageStats retrieves disk usage and row counts for all crawlobserver tables.
func (s *Store) StorageStats(ctx context.Context) (*StorageStatsResult, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT table, sum(bytes_on_disk), sum(rows)
		FROM system.parts
		WHERE database = 'crawlobserver' AND active = 1
		GROUP BY table
		ORDER BY table`)
	if err != nil {
		return nil, fmt.Errorf("querying storage stats: %w", err)
	}
	defer rows.Close()

	result := &StorageStatsResult{}
	for rows.Next() {
		var t TableStorageStats
		if err := rows.Scan(&t.Name, &t.BytesOnDisk, &t.Rows); err != nil {
			return nil, fmt.Errorf("scanning storage stats: %w", err)
		}
		result.Tables = append(result.Tables, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating storage stats: %w", err)
	}
	return result, nil
}

// SessionStorageStats returns bytes on disk per crawl session,
// computed from system.parts partitions across all data tables.
func (s *Store) SessionStorageStats(ctx context.Context) (map[string]uint64, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT partition AS session_id, sum(bytes_on_disk) AS bytes
		FROM system.parts
		WHERE database = 'crawlobserver' AND active = 1 AND table != 'crawl_sessions'
			AND match(partition, '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$')
			AND partition IN (
				SELECT toString(id)
				FROM crawlobserver.crawl_sessions FINAL
			)
		GROUP BY partition
		SETTINGS max_bytes_before_external_group_by = 100000000`)
	if err != nil {
		return nil, fmt.Errorf("querying session storage stats: %w", err)
	}
	defer rows.Close()

	result := make(map[string]uint64)
	for rows.Next() {
		var sessionID string
		var bytes uint64
		if err := rows.Scan(&sessionID, &bytes); err != nil {
			return nil, fmt.Errorf("scanning session storage stats: %w", err)
		}
		result[sessionID] = bytes
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating session storage stats: %w", err)
	}
	return result, nil
}

// GlobalSessionStats holds aggregated stats for a single session.
type GlobalSessionStats struct {
	SessionID  string  `json:"session_id"`
	TotalPages uint64  `json:"total_pages"`
	TotalLinks uint64  `json:"total_links"`
	ErrorCount uint64  `json:"error_count"`
	AvgFetchMs float64 `json:"avg_fetch_ms"`
}

// GlobalStats retrieves aggregated stats per session across all data.
func (s *Store) GlobalStats(ctx context.Context) ([]GlobalSessionStats, *StorageStatsResult, error) {
	// 1. Page stats per session
	pageRows, err := s.conn.Query(ctx, `
		SELECT crawl_session_id, count(), countIf(error != ''), avg(fetch_duration_ms)
		FROM crawlobserver.pages FINAL
		GROUP BY crawl_session_id
		SETTINGS max_bytes_before_external_group_by = 100000000`)
	if err != nil {
		return nil, nil, fmt.Errorf("querying global page stats: %w", err)
	}
	defer pageRows.Close()

	statsMap := map[string]*GlobalSessionStats{}
	for pageRows.Next() {
		var gs GlobalSessionStats
		if err := pageRows.Scan(&gs.SessionID, &gs.TotalPages, &gs.ErrorCount, &gs.AvgFetchMs); err != nil {
			return nil, nil, fmt.Errorf("scanning global page stats: %w", err)
		}
		statsMap[gs.SessionID] = &gs
	}
	if err := pageRows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterating page stats: %w", err)
	}

	// 2. Link counts per session
	linkRows, err := s.conn.Query(ctx, `
		SELECT crawl_session_id, count()
		FROM crawlobserver.links
		GROUP BY crawl_session_id
		SETTINGS max_bytes_before_external_group_by = 100000000`)
	if err != nil {
		return nil, nil, fmt.Errorf("querying global link stats: %w", err)
	}
	defer linkRows.Close()

	for linkRows.Next() {
		var sid string
		var cnt uint64
		if err := linkRows.Scan(&sid, &cnt); err != nil {
			return nil, nil, fmt.Errorf("scanning global link stats: %w", err)
		}
		if gs, ok := statsMap[sid]; ok {
			gs.TotalLinks = cnt
		} else {
			statsMap[sid] = &GlobalSessionStats{SessionID: sid, TotalLinks: cnt}
		}
	}
	if err := linkRows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterating link stats: %w", err)
	}

	// 3. Storage stats
	storage, err := s.StorageStats(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("querying storage for global stats: %w", err)
	}

	result := make([]GlobalSessionStats, 0, len(statsMap))
	for _, gs := range statsMap {
		result = append(result, *gs)
	}
	return result, storage, nil
}
