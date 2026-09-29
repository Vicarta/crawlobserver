package storage

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

func (s *Store) TerminalSessionsAfter(ctx context.Context, finishedAt time.Time, sessionID string, limit int) ([]CrawlSession, error) {
	return s.terminalSessionsAfter(ctx, finishedAt, sessionID, nil, limit)
}

func (s *Store) TerminalSessionsAfterExcluding(ctx context.Context, finishedAt time.Time, sessionID string, excludedSessionIDs []string, limit int) ([]CrawlSession, error) {
	return s.terminalSessionsAfter(ctx, finishedAt, sessionID, excludedSessionIDs, limit)
}

func (s *Store) terminalSessionsAfter(ctx context.Context, finishedAt time.Time, sessionID string, excludedSessionIDs []string, limit int) ([]CrawlSession, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	query := `
		SELECT id, started_at, finished_at, status, seed_urls, config, pages_crawled, user_agent, project_id, label
		FROM crawlobserver.crawl_sessions FINAL
		WHERE label NOT IN ('Current Snapshot', 'Current Baseline Snapshot')
		  AND status IN ('completed', 'completed_with_errors', 'failed', 'crashed', 'stopped')
		  AND finished_at IS NOT NULL
		  AND (finished_at > ? OR (finished_at = ? AND toString(id) > ?))`
	args := []interface{}{finishedAt, finishedAt, sessionID}
	if len(excludedSessionIDs) > 0 {
		query += fmt.Sprintf(" AND toString(id) NOT IN (%s)", sessionPlaceholders(len(excludedSessionIDs)))
		args = appendStringPlaceholders(args, excludedSessionIDs)
	}
	query += " ORDER BY finished_at ASC, toString(id) ASC LIMIT ?"
	args = append(args, limit)
	rows, err := s.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying terminal sessions for operational email: %w", err)
	}
	defer rows.Close()
	var sessions []CrawlSession
	for rows.Next() {
		var session CrawlSession
		if err := rows.Scan(
			&session.ID, &session.StartedAt, &session.FinishedAt, &session.Status,
			&session.SeedURLs, &session.Config, &session.PagesCrawled, &session.UserAgent,
			&session.ProjectID, &session.Label,
		); err != nil {
			return nil, fmt.Errorf("scanning terminal session for operational email: %w", err)
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating terminal sessions for operational email: %w", err)
	}
	if sessions == nil {
		sessions = []CrawlSession{}
	}
	return sessions, nil
}

func (s *Store) NewPageErrorsForSession(ctx context.Context, session CrawlSession) ([]PageErrorObservation, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT url, status_code, error
		FROM crawlobserver.pages FINAL
		WHERE crawl_session_id = ? AND (status_code = 0 OR status_code >= 400 OR error != '')
		ORDER BY url ASC`, session.ID)
	if err != nil {
		return nil, fmt.Errorf("querying page errors for session %s: %w", session.ID, err)
	}
	var current []PageErrorObservation
	for rows.Next() {
		var observation PageErrorObservation
		var fetchError string
		if err := rows.Scan(&observation.URL, &observation.StatusCode, &fetchError); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scanning page error for session %s: %w", session.ID, err)
		}
		observation.FetchError = strings.TrimSpace(fetchError) != ""
		current = append(current, observation)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterating page errors for session %s: %w", session.ID, err)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(current) == 0 || session.ProjectID == nil || *session.ProjectID == "" {
		return current, nil
	}

	priorByURL := make(map[string]PageErrorObservation, len(current))
	for start := 0; start < len(current); start += 250 {
		end := min(start+250, len(current))
		urls := make([]string, 0, end-start)
		for _, page := range current[start:end] {
			urls = append(urls, page.URL)
		}
		args := []interface{}{*session.ProjectID, session.FinishedAt, session.FinishedAt, session.ID}
		for _, pageURL := range urls {
			args = append(args, pageURL)
		}
		query := fmt.Sprintf(`
			SELECT p.url,
			       argMax(p.status_code, tuple(cs.finished_at, toString(cs.id))) AS latest_status_code,
			       argMax(p.error != '', tuple(cs.finished_at, toString(cs.id))) AS latest_has_error
			FROM crawlobserver.pages AS p FINAL
			INNER JOIN crawlobserver.crawl_sessions AS cs FINAL ON toString(cs.id) = toString(p.crawl_session_id)
			WHERE cs.project_id = ?
			  AND (cs.finished_at < ? OR (cs.finished_at = ? AND toString(cs.id) < ?))
			  AND cs.label NOT IN ('Current Snapshot', 'Current Baseline Snapshot')
			  AND p.url IN (%s)
			GROUP BY p.url`, sessionPlaceholders(len(urls)))
		previousRows, err := s.conn.Query(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("querying prior page observations for session %s: %w", session.ID, err)
		}
		for previousRows.Next() {
			var pageURL string
			var statusCode uint16
			var fetchError bool
			if err := previousRows.Scan(&pageURL, &statusCode, &fetchError); err != nil {
				previousRows.Close()
				return nil, fmt.Errorf("scanning prior page observation for session %s: %w", session.ID, err)
			}
			priorByURL[pageURL] = PageErrorObservation{URL: pageURL, StatusCode: statusCode, FetchError: fetchError}
		}
		if err := previousRows.Err(); err != nil {
			previousRows.Close()
			return nil, fmt.Errorf("iterating prior page observations for session %s: %w", session.ID, err)
		}
		if err := previousRows.Close(); err != nil {
			return nil, err
		}
	}

	return newPageErrorsComparedWithPrior(current, priorByURL), nil
}

func newPageErrorsComparedWithPrior(current []PageErrorObservation, previousByURL map[string]PageErrorObservation) []PageErrorObservation {
	var newErrors []PageErrorObservation
	for _, page := range current {
		previous, seen := previousByURL[page.URL]
		if !seen || pageErrorClass(previous.StatusCode, previous.FetchError) != pageErrorClass(page.StatusCode, page.FetchError) {
			newErrors = append(newErrors, page)
		}
	}
	sort.Slice(newErrors, func(i, j int) bool { return newErrors[i].URL < newErrors[j].URL })
	return newErrors
}

func pageErrorClass(statusCode uint16, fetchError bool) string {
	if statusCode == 0 || fetchError {
		return "fetch_failed"
	}
	if statusCode >= 400 {
		return fmt.Sprintf("http_%d", statusCode)
	}
	return "ok"
}
