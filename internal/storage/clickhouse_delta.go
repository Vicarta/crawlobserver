package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SEObserver/crawlobserver/internal/config"
	"github.com/SEObserver/crawlobserver/internal/fetcher"
)

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
	URLs       []DeltaSitemapObservationURL
}

// DeltaSitemapTerms are the dual sitemap terms used for changed-only Delta
// planning. Published is always the exact materialized Current Snapshot
// supplied by the caller; Raw is the newest complete fresh Delta observation,
// falling back only to the trusted raw full-crawl source.
type DeltaSitemapTerms struct {
	Raw       DeltaSitemapObservation
	Published DeltaSitemapObservation
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
func (s *Store) LoadDeltaSitemapTerms(ctx context.Context, projectID, publishedSessionID, rawFallbackSessionID string, limit int) (*DeltaSitemapTerms, error) {
	if projectID == "" || publishedSessionID == "" || rawFallbackSessionID == "" {
		return nil, fmt.Errorf("complete project and snapshot sitemap lineage is required")
	}
	if limit <= 0 {
		limit = 50000
	}
	published, err := s.loadDeltaSitemapObservation(ctx, publishedSessionID, time.Time{}, limit)
	if err != nil {
		return nil, fmt.Errorf("loading published sitemap term: %w", err)
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
	for rows.Next() {
		var sessionID, rawConfig string
		var observedAt time.Time
		if err := rows.Scan(&sessionID, &observedAt, &rawConfig); err != nil {
			return nil, fmt.Errorf("scanning fresh raw sitemap observation: %w", err)
		}
		var saved config.Config
		if err := json.Unmarshal([]byte(rawConfig), &saved); err != nil || saved.Crawler.DeltaPlan == nil ||
			saved.Crawler.DeltaPlan.SitemapRefresh == nil ||
			!strings.EqualFold(saved.Crawler.DeltaPlan.SitemapRefresh.Mode, "fresh") {
			continue
		}
		if !saved.Crawler.DeltaPlan.SitemapRefresh.FetchedAt.IsZero() {
			observedAt = saved.Crawler.DeltaPlan.SitemapRefresh.FetchedAt
		}
		raw, err := s.loadDeltaSitemapObservation(ctx, sessionID, observedAt, limit)
		if err != nil {
			return nil, fmt.Errorf("loading fresh raw sitemap observation: %w", err)
		}
		return &DeltaSitemapTerms{Raw: raw, Published: published}, nil
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating fresh raw sitemap observations: %w", err)
	}

	raw, err := s.loadDeltaSitemapObservation(ctx, rawFallbackSessionID, time.Time{}, limit)
	if err != nil {
		return nil, fmt.Errorf("loading trusted raw full sitemap fallback: %w", err)
	}
	return &DeltaSitemapTerms{Raw: raw, Published: published}, nil
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
		LIMIT ?`, sessionID, limit)
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
