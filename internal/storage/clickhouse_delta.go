package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

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
