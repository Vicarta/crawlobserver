package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/SEObserver/crawlobserver/internal/config"
	"github.com/SEObserver/crawlobserver/internal/fetcher"
)

const maxDeltaSitemapStabilityURLs = 50000

// PageHTTPValidators returns retained response validators for exact page URLs
// in one materialized baseline session. Header names are matched
// case-insensitively because historic rows preserve their original casing.
func (s *Store) PageHTTPValidators(ctx context.Context, sessionID string, urls []string) (map[string]fetcher.RequestValidators, error) {
	if sessionID == "" || len(urls) == 0 {
		return map[string]fetcher.RequestValidators{}, nil
	}
	rows, err := s.conn.Query(ctx, `
		SELECT url, headers
		FROM crawlobserver.pages FINAL
		WHERE crawl_session_id = ? AND url IN (?)`, sessionID, urls)
	if err != nil {
		return nil, fmt.Errorf("loading page HTTP validators: %w", err)
	}
	defer rows.Close()
	validators := make(map[string]fetcher.RequestValidators, len(urls))
	for rows.Next() {
		var url string
		var headers map[string]string
		if err := rows.Scan(&url, &headers); err != nil {
			return nil, fmt.Errorf("scanning page HTTP validators: %w", err)
		}
		var value fetcher.RequestValidators
		for key, headerValue := range headers {
			switch {
			case strings.EqualFold(key, "ETag"):
				value.ETag = headerValue
			case strings.EqualFold(key, "Last-Modified"):
				value.LastModified = headerValue
			}
		}
		if value.ETag != "" || value.LastModified != "" {
			validators[url] = value
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating page HTTP validators: %w", err)
	}
	return validators, nil
}

// DeltaSitemapObservationURL retains the literal sitemap URL and lastmod
// evidence. The server applies project URL policy before passing these values
// to the pure selector.
type DeltaSitemapObservationURL struct {
	Loc     string
	LastMod string
}

// DeltaSitemapObservation identifies one complete sitemap observation.
type DeltaSitemapObservation struct {
	SessionID  string
	ObservedAt time.Time
	Truncated  bool
	URLs       []DeltaSitemapObservationURL
}

// DeltaSitemapStabilityURL is a read-only proof that one normalized sitemap
// identity produced the same valid lastmod and nonzero content hash in two
// separate completed fresh observations. Loc remains available for the
// project-aware planner; NormalizedURL is only the conservative storage
// comparison identity and is never used to widen project URL policy.
type DeltaSitemapStabilityURL struct {
	NormalizedURL string
	Loc           string
	LastMod       string
	ContentHash   uint64
}

// DeltaSitemapStability is derived from immutable raw evidence. It can avoid
// a redundant refetch, but it is never publication or quality evidence.
type DeltaSitemapStability struct {
	OlderSessionID     string
	OlderObservedAt    time.Time
	NewerSessionID     string
	NewerObservedAt    time.Time
	LegacyCompletePair bool
	ProofDigest        string
	URLs               []DeltaSitemapStabilityURL
}

// DeltaSitemapTerms are the dual sitemap terms used for changed-only Delta
// planning. Published is always the exact materialized Current Snapshot
// supplied by the caller; Raw is the newest complete fresh Delta observation,
// falling back only to the trusted raw full-crawl source.
type DeltaSitemapTerms struct {
	Raw       DeltaSitemapObservation
	Published DeltaSitemapObservation
	Stability *DeltaSitemapStability
}

// LatestProjectSession returns the newest sitemap-backed completed session for a project.
// Daily delta crawls are partial snapshots, so using the latest session blindly can
// shrink the next candidate set to the previous delta's small URL list.
func (s *Store) LatestProjectSession(ctx context.Context, projectID string) (*CrawlSession, error) {
	sess, err := s.latestProjectSession(ctx, `
		SELECT id, started_at, finished_at, status, seed_urls, config, pages_crawled, user_agent, project_id, label
		FROM crawlobserver.crawl_sessions FINAL
		WHERE project_id = ?
		  AND `+nonSyntheticSessionFilter+`
		  AND status = 'completed'
		  AND pages_crawled > 0
		  AND id IN (
			SELECT crawl_session_id
			FROM crawlobserver.sitemap_urls FINAL
			WHERE loc != ''
			GROUP BY crawl_session_id
			HAVING count() > 0
		  )
		ORDER BY started_at DESC
		LIMIT 1`, projectID)
	if err == nil {
		return sess, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("querying latest sitemap-backed project session: %w", err)
	}
	return s.latestProjectSession(ctx, `
		SELECT id, started_at, finished_at, status, seed_urls, config, pages_crawled, user_agent, project_id, label
		FROM crawlobserver.crawl_sessions FINAL
		WHERE project_id = ?
		  AND `+nonSyntheticSessionFilter+`
		ORDER BY started_at DESC
		LIMIT 1`, projectID)
}

func (s *Store) latestProjectSession(ctx context.Context, query string, args ...interface{}) (*CrawlSession, error) {
	row := s.conn.QueryRow(ctx, query, args...)
	var sess CrawlSession
	if err := row.Scan(
		&sess.ID, &sess.StartedAt, &sess.FinishedAt,
		&sess.Status, &sess.SeedURLs, &sess.Config,
		&sess.PagesCrawled, &sess.UserAgent, &sess.ProjectID, &sess.Label,
	); err != nil {
		return nil, fmt.Errorf("querying latest project session: %w", err)
	}
	return &sess, nil
}

func (s *Store) DeltaSitemapCandidateURLs(ctx context.Context, sessionID string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 5000
	}
	rows, err := s.conn.Query(ctx, `
		SELECT DISTINCT loc
		FROM crawlobserver.sitemap_urls FINAL
		WHERE crawl_session_id = ? AND loc != ''
		ORDER BY loc
		LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("querying delta sitemap candidates: %w", err)
	}
	defer rows.Close()
	return scanStringColumn(rows)
}

// LoadDeltaSitemapTerms reads bounded sitemap evidence for one already
// validated Current Snapshot lineage. It never substitutes an arbitrary latest
// crawl for the published term. Fresh raw observations must be terminal,
// non-synthetic Daily Delta sessions with a persisted Phase 21 fresh refresh.
// When two independent raw observations exist, it also derives a conservative
// per-URL stability proof. That proof is execution-only: callers must retain
// Published as the sole Current Snapshot authority.
func (s *Store) LoadDeltaSitemapTerms(ctx context.Context, projectID, publishedSessionID, rawFallbackSessionID string, limit int) (*DeltaSitemapTerms, error) {
	if projectID == "" || publishedSessionID == "" || rawFallbackSessionID == "" {
		return nil, fmt.Errorf("complete project and snapshot sitemap lineage is required")
	}
	if limit <= 0 || limit > maxDeltaSitemapStabilityURLs {
		limit = maxDeltaSitemapStabilityURLs
	}
	for _, sessionID := range []string{publishedSessionID, rawFallbackSessionID} {
		session, err := s.GetSession(ctx, sessionID)
		if err != nil {
			return nil, fmt.Errorf("validating sitemap lineage session %s: %w", sessionID, err)
		}
		if session.ProjectID == nil || *session.ProjectID != projectID || session.Status != "completed" {
			return nil, fmt.Errorf("sitemap lineage session %s does not belong to the completed project lineage", sessionID)
		}
	}
	published, err := s.loadDeltaSitemapObservation(ctx, publishedSessionID, time.Time{}, limit)
	if err != nil {
		return nil, fmt.Errorf("loading published sitemap term: %w", err)
	}
	if published.Truncated {
		return nil, fmt.Errorf("published sitemap term exceeds the bounded comparison limit")
	}

	rows, err := s.conn.Query(ctx, `
		SELECT id, finished_at, config
		FROM crawlobserver.crawl_sessions FINAL
		WHERE project_id = ?
		  AND `+nonSyntheticSessionFilter+`
		  AND status = 'completed'
		  AND label = 'Daily Delta Crawl'
		ORDER BY finished_at DESC, id DESC
		LIMIT 100`, projectID)
	if err != nil {
		return nil, fmt.Errorf("querying fresh raw sitemap observations: %w", err)
	}
	defer rows.Close()
	type freshObservationCandidate struct {
		sessionID      string
		observedAt     time.Time
		expectedUnique int
		expectedRaw    int
		selection      *config.DeltaSitemapSelection
	}
	candidates := make([]freshObservationCandidate, 0, 100)
	for rows.Next() {
		var sessionID, rawConfig string
		var observedAt time.Time
		if err := rows.Scan(&sessionID, &observedAt, &rawConfig); err != nil {
			return nil, fmt.Errorf("scanning fresh raw sitemap observation: %w", err)
		}
		var saved config.Config
		if err := json.Unmarshal([]byte(rawConfig), &saved); err != nil ||
			!deltaSitemapFreshObservationMetadataValid(saved.Crawler.DeltaPlan) {
			continue
		}
		fetchedAt := saved.Crawler.DeltaPlan.SitemapRefresh.FetchedAt
		if fetchedAt.After(observedAt) {
			continue
		}
		observedAt = fetchedAt
		candidates = append(candidates, freshObservationCandidate{
			sessionID:      sessionID,
			observedAt:     observedAt,
			expectedUnique: saved.Crawler.DeltaPlan.SitemapRefresh.FreshURLCount,
			expectedRaw:    saved.Crawler.DeltaPlan.SitemapRefresh.RawURLRowCount,
			selection:      saved.Crawler.DeltaPlan.SitemapSelection,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating fresh raw sitemap observations: %w", err)
	}
	type eligibleObservation struct {
		candidate   freshObservationCandidate
		observation DeltaSitemapObservation
	}
	eligible := make([]eligibleObservation, 0, 2)
	for _, candidate := range candidates {
		observation, loadErr := s.loadDeltaSitemapObservation(ctx, candidate.sessionID, candidate.observedAt, limit)
		if loadErr != nil {
			return nil, fmt.Errorf("loading fresh raw sitemap observation: %w", loadErr)
		}
		if observation.Truncated ||
			(candidate.expectedRaw > 0 && len(observation.URLs) != candidate.expectedRaw) ||
			(candidate.expectedRaw == 0 && len(observation.URLs) < candidate.expectedUnique) {
			continue
		}
		eligible = append(eligible, eligibleObservation{candidate: candidate, observation: observation})
		if len(eligible) == 2 {
			break
		}
	}
	if len(eligible) > 0 {
		raw := eligible[0].observation
		terms := &DeltaSitemapTerms{Raw: raw, Published: published}
		if len(eligible) == 2 {
			older := eligible[1].observation
			stability, err := s.loadDeltaSitemapStability(ctx, older, raw, eligible[1].candidate.selection, eligible[0].candidate.selection, limit)
			if err != nil {
				return nil, fmt.Errorf("deriving fresh raw sitemap stability: %w", err)
			}
			terms.Stability = stability
		}
		return terms, nil
	}

	raw, err := s.loadDeltaSitemapObservation(ctx, rawFallbackSessionID, time.Time{}, limit)
	if err != nil {
		return nil, fmt.Errorf("loading trusted raw full sitemap fallback: %w", err)
	}
	if raw.Truncated {
		return nil, fmt.Errorf("raw fallback sitemap term exceeds the bounded comparison limit")
	}
	return &DeltaSitemapTerms{Raw: raw, Published: published}, nil
}

type deltaSitemapPageEvidence struct {
	ContentHash uint64
}

// loadDeltaSitemapStability verifies the exact pair after both observations
// have passed the completed/fresh/session filters in LoadDeltaSitemapTerms.
// Legacy complete pairs are deliberately accepted only through these exact
// checks; a legacy label or plan shape by itself proves nothing.
func (s *Store) loadDeltaSitemapStability(ctx context.Context, older, newer DeltaSitemapObservation, olderSelection, newerSelection *config.DeltaSitemapSelection, limit int) (*DeltaSitemapStability, error) {
	if older.SessionID == "" || newer.SessionID == "" || older.SessionID == newer.SessionID {
		return nil, nil
	}
	if !deltaSitemapSelectorSupportsStability(olderSelection) || !deltaSitemapSelectorSupportsStability(newerSelection) {
		return nil, nil
	}
	evidenceURLs := make([]string, 0, len(older.URLs)+len(newer.URLs))
	for _, observation := range []DeltaSitemapObservation{older, newer} {
		for _, value := range observation.URLs {
			evidenceURLs = append(evidenceURLs, value.Loc)
		}
	}
	evidence, err := s.loadDeltaSitemapPageEvidence(ctx, []string{older.SessionID, newer.SessionID}, evidenceURLs, limit)
	if err != nil {
		return nil, err
	}
	stability := deriveDeltaSitemapStability(older, newer, evidence[older.SessionID], evidence[newer.SessionID])
	if stability == nil {
		return nil, nil
	}
	stability.LegacyCompletePair = deltaSitemapSelectorIsLegacy(olderSelection) || deltaSitemapSelectorIsLegacy(newerSelection)
	return stability, nil
}

func (s *Store) loadDeltaSitemapPageEvidence(ctx context.Context, sessionIDs, requestedURLs []string, limit int) (map[string]map[string]deltaSitemapPageEvidence, error) {
	result := make(map[string]map[string]deltaSitemapPageEvidence, len(sessionIDs))
	ambiguous := make(map[string]map[string]struct{}, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		result[sessionID] = map[string]deltaSitemapPageEvidence{}
		ambiguous[sessionID] = map[string]struct{}{}
	}
	if len(sessionIDs) == 0 || len(requestedURLs) == 0 || limit <= 0 {
		return result, nil
	}
	requestedSet := make(map[string]struct{}, len(requestedURLs)*2)
	for _, raw := range requestedURLs {
		raw = strings.TrimSpace(raw)
		if raw != "" {
			requestedSet[raw] = struct{}{}
		}
		if normalized, ok := normalizeDeltaSitemapStabilityURL(raw); ok {
			requestedSet[normalized] = struct{}{}
		}
	}
	boundedURLs := make([]string, 0, len(requestedSet))
	for value := range requestedSet {
		boundedURLs = append(boundedURLs, value)
	}
	sort.Strings(boundedURLs)
	maxURLs := limit * 2
	if len(boundedURLs) > maxURLs {
		boundedURLs = boundedURLs[:maxURLs]
	}
	rowLimit := len(sessionIDs) * len(boundedURLs)
	rows, err := s.conn.Query(ctx, `
		SELECT crawl_session_id, url, content_hash
		FROM crawlobserver.pages FINAL
		WHERE crawl_session_id IN (?)
		  AND url IN (?)
		  AND status_code >= 200 AND status_code < 300
		  AND error = ''
		  AND body_truncated = false
		  AND content_hash != 0
		ORDER BY crawl_session_id, url
		LIMIT ?`, sessionIDs, boundedURLs, rowLimit)
	if err != nil {
		return nil, fmt.Errorf("querying stable sitemap page evidence: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sessionID, pageURL string
		var contentHash uint64
		if err := rows.Scan(&sessionID, &pageURL, &contentHash); err != nil {
			return nil, fmt.Errorf("scanning stable sitemap page evidence: %w", err)
		}
		identity, ok := normalizeDeltaSitemapStabilityURL(pageURL)
		if !ok || contentHash == 0 {
			continue
		}
		if _, ok := result[sessionID]; !ok {
			continue
		}
		if _, blocked := ambiguous[sessionID][identity]; blocked {
			continue
		}
		// Distinct stored URLs may collapse to the same normalized identity.
		// Exclude any such identity instead of depending on ClickHouse row order.
		if _, exists := result[sessionID][identity]; exists {
			delete(result[sessionID], identity)
			ambiguous[sessionID][identity] = struct{}{}
			continue
		}
		result[sessionID][identity] = deltaSitemapPageEvidence{ContentHash: contentHash}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating stable sitemap page evidence: %w", err)
	}
	return result, nil
}

// deriveDeltaSitemapStability is pure so stability semantics can be tested
// without a ClickHouse fixture. Newer is the most recent completed raw term.
func deriveDeltaSitemapStability(older, newer DeltaSitemapObservation, olderPages, newerPages map[string]deltaSitemapPageEvidence) *DeltaSitemapStability {
	if older.SessionID == "" || newer.SessionID == "" || older.SessionID == newer.SessionID {
		return nil
	}
	olderURLs := deltaSitemapObservationURLMap(older.URLs)
	newerURLs := deltaSitemapObservationURLMap(newer.URLs)
	proofs := make([]DeltaSitemapStabilityURL, 0)
	for identity, current := range newerURLs {
		previous, exists := olderURLs[identity]
		if !exists || !sameValidSitemapLastMod(previous.LastMod, current.LastMod) {
			continue
		}
		oldPage, oldOK := olderPages[identity]
		newPage, newOK := newerPages[identity]
		if !oldOK || !newOK || oldPage.ContentHash == 0 || oldPage.ContentHash != newPage.ContentHash {
			continue
		}
		proofs = append(proofs, DeltaSitemapStabilityURL{
			NormalizedURL: identity,
			Loc:           current.Loc,
			LastMod:       current.LastMod,
			ContentHash:   newPage.ContentHash,
		})
	}
	if len(proofs) == 0 {
		return nil
	}
	sort.Slice(proofs, func(i, j int) bool { return proofs[i].NormalizedURL < proofs[j].NormalizedURL })
	return &DeltaSitemapStability{
		OlderSessionID:  older.SessionID,
		OlderObservedAt: older.ObservedAt,
		NewerSessionID:  newer.SessionID,
		NewerObservedAt: newer.ObservedAt,
		ProofDigest:     deltaSitemapStabilityDigest(older.SessionID, newer.SessionID, proofs),
		URLs:            proofs,
	}
}

func deltaSitemapObservationURLMap(values []DeltaSitemapObservationURL) map[string]DeltaSitemapObservationURL {
	result := make(map[string]DeltaSitemapObservationURL, len(values))
	ambiguous := make(map[string]struct{})
	for _, value := range values {
		identity, ok := normalizeDeltaSitemapStabilityURL(value.Loc)
		if !ok {
			continue
		}
		if _, blocked := ambiguous[identity]; blocked {
			continue
		}
		if _, exists := result[identity]; exists {
			delete(result, identity)
			ambiguous[identity] = struct{}{}
			continue
		}
		result[identity] = value
	}
	return result
}

func sameValidSitemapLastMod(left, right string) bool {
	leftTime, leftErr := fetcher.ParseSitemapLastMod(left)
	rightTime, rightErr := fetcher.ParseSitemapLastMod(right)
	return leftErr == nil && rightErr == nil &&
		leftTime.DateOnly == rightTime.DateOnly &&
		leftTime.Time.Equal(rightTime.Time)
}

func sitemapLastModValueAfter(candidate, existing string) bool {
	comparison, comparable := fetcher.CompareSitemapLastMod(candidate, existing)
	if comparable {
		if comparison != 0 {
			return comparison > 0
		}
		return candidate > existing
	}
	_, candidateErr := fetcher.ParseSitemapLastMod(candidate)
	_, existingErr := fetcher.ParseSitemapLastMod(existing)
	if candidateErr == nil || existingErr == nil {
		return candidateErr == nil
	}
	return candidate > existing
}

func normalizeDeltaSitemapStabilityURL(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" {
		return "", false
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return "", false
	}
	port := parsed.Port()
	if (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443") {
		port = ""
	}
	if strings.Contains(host, ":") {
		parsed.Host = "[" + host + "]"
	} else {
		parsed.Host = host
	}
	if port != "" {
		parsed.Host = net.JoinHostPort(host, port)
	}
	trailingSlash := strings.HasSuffix(parsed.Path, "/")
	parsed.Path = path.Clean("/" + parsed.Path)
	if trailingSlash && parsed.Path != "/" {
		parsed.Path += "/"
	}
	parsed.RawPath = ""
	parsed.Fragment = ""
	return parsed.String(), true
}

func deltaSitemapStabilityDigest(olderSessionID, newerSessionID string, proofs []DeltaSitemapStabilityURL) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(olderSessionID))
	_, _ = hash.Write([]byte{'\n'})
	_, _ = hash.Write([]byte(newerSessionID))
	for _, proof := range proofs {
		_, _ = hash.Write([]byte{'\n'})
		_, _ = hash.Write([]byte(proof.NormalizedURL))
		_, _ = hash.Write([]byte{'\x00'})
		_, _ = hash.Write([]byte(proof.LastMod))
		_, _ = hash.Write([]byte{'\x00'})
		_, _ = hash.Write([]byte(fmt.Sprintf("%d", proof.ContentHash)))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func deltaSitemapSelectorSupportsStability(selection *config.DeltaSitemapSelection) bool {
	return selection == nil || selection.SelectorRevision == "v1" || selection.SelectorRevision == "v2"
}

func deltaSitemapFreshObservationMetadataValid(plan *config.DeltaPlanConfig) bool {
	return plan != nil && plan.SitemapRefresh != nil &&
		strings.EqualFold(plan.SitemapRefresh.Mode, "fresh") &&
		!plan.SitemapRefresh.FetchedAt.IsZero() &&
		plan.SitemapRefresh.FreshURLCount >= 0 &&
		plan.SitemapRefresh.RawURLRowCount >= 0 &&
		deltaSitemapSelectorSupportsStability(plan.SitemapSelection)
}

func deltaSitemapSelectorIsLegacy(selection *config.DeltaSitemapSelection) bool {
	return selection == nil || selection.SelectorRevision == "v1"
}

func (s *Store) loadDeltaSitemapObservation(ctx context.Context, sessionID string, observedAt time.Time, limit int) (DeltaSitemapObservation, error) {
	if observedAt.IsZero() {
		session, err := s.GetSession(ctx, sessionID)
		if err != nil {
			return DeltaSitemapObservation{}, err
		}
		observedAt = session.FinishedAt
		if observedAt.IsZero() {
			observedAt = session.StartedAt
		}
	}
	rows, err := s.conn.Query(ctx, `
		SELECT loc, lastmod
		FROM crawlobserver.sitemap_urls FINAL
		WHERE crawl_session_id = ? AND loc != ''
		ORDER BY loc, lastmod
		LIMIT ?`, sessionID, limit+1)
	if err != nil {
		return DeltaSitemapObservation{}, err
	}
	defer rows.Close()
	observation := DeltaSitemapObservation{SessionID: sessionID, ObservedAt: observedAt, URLs: []DeltaSitemapObservationURL{}}
	for rows.Next() {
		var row DeltaSitemapObservationURL
		if err := rows.Scan(&row.Loc, &row.LastMod); err != nil {
			return DeltaSitemapObservation{}, err
		}
		observation.URLs = append(observation.URLs, row)
		if len(observation.URLs) > limit {
			observation.Truncated = true
			observation.URLs = observation.URLs[:limit]
			break
		}
	}
	if err := rows.Err(); err != nil {
		return DeltaSitemapObservation{}, err
	}
	return observation, nil
}

func (s *Store) CountSitemapURLs(ctx context.Context, sessionID string) (int, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT count(DISTINCT loc)
		FROM crawlobserver.sitemap_urls FINAL
		WHERE crawl_session_id = ? AND loc != ''`, sessionID)
	var count uint64
	if err := row.Scan(&count); err != nil {
		return 0, fmt.Errorf("counting sitemap urls: %w", err)
	}
	return int(count), nil
}

func (s *Store) DeltaGSCCandidateURLs(ctx context.Context, projectID string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 5000
	}
	rows, err := s.conn.Query(ctx, `
		SELECT page
		FROM (
			SELECT page, sum(impressions) AS impressions
			FROM crawlobserver.gsc_analytics FINAL
			WHERE project_id = ? AND page != ''
			GROUP BY page
			ORDER BY impressions DESC
			LIMIT ?
		)`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("querying delta gsc candidates: %w", err)
	}
	defer rows.Close()
	return scanStringColumn(rows)
}

func (s *Store) DeltaProblemPageURLs(ctx context.Context, sessionID string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := s.conn.Query(ctx, `
		SELECT url
		FROM crawlobserver.pages FINAL
		WHERE crawl_session_id = ?
		  AND `+notRedirectedFilter+`
		  AND `+deltaConfirmedSiteURLFilter+`
		  AND (
			status_code = 0 OR status_code >= 400
			OR error != ''
			OR index_reason ILIKE '%canonical%'
			OR index_reason ILIKE '%noindex%'
		  )
		ORDER BY crawled_at ASC
		LIMIT ?`, sessionID, sessionID, sessionID, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("querying delta problem page candidates: %w", err)
	}
	defer rows.Close()
	return scanStringColumn(rows)
}

func (s *Store) DeltaStalePageURLs(ctx context.Context, sessionID string, staleBefore time.Time, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := s.conn.Query(ctx, `
		SELECT url
		FROM crawlobserver.pages FINAL
		WHERE crawl_session_id = ?
		  AND `+notRedirectedFilter+`
		  AND `+deltaConfirmedSiteURLFilter+`
		  AND crawled_at < ?
		ORDER BY crawled_at ASC
		LIMIT ?`, sessionID, sessionID, sessionID, sessionID, staleBefore, limit)
	if err != nil {
		return nil, fmt.Errorf("querying delta stale page candidates: %w", err)
	}
	defer rows.Close()
	return scanStringColumn(rows)
}

const deltaConfirmedSiteURLFilter = `url IN (
	SELECT loc AS url
	FROM crawlobserver.sitemap_urls FINAL
	WHERE crawl_session_id = ? AND loc != ''

	UNION DISTINCT

	SELECT target_url AS url
	FROM crawlobserver.links
	WHERE crawl_session_id = ? AND is_internal = true AND target_url != ''

	UNION DISTINCT

	SELECT arrayJoin(seed_urls) AS url
	FROM crawlobserver.crawl_sessions FINAL
	WHERE id = ?
)`

func (s *Store) DeltaKnownPageURLs(ctx context.Context, sessionID string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 50000
	}
	rows, err := s.conn.Query(ctx, `
		SELECT url
		FROM crawlobserver.pages FINAL
		WHERE crawl_session_id = ? AND `+notRedirectedFilter+`
		LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("querying delta known page urls: %w", err)
	}
	defer rows.Close()
	return scanStringColumn(rows)
}

type stringRows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

func scanStringColumn(rows stringRows) ([]string, error) {
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}
