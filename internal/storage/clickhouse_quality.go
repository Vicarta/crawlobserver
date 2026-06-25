package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

func (s *Store) UpsertCrawlQualityResult(ctx context.Context, result CrawlQualityResult) error {
	metricsJSON := "{}"
	if result.Metrics != nil {
		if b, err := json.Marshal(result.Metrics); err == nil {
			metricsJSON = string(b)
		}
	}
	if result.EvaluatedAt.IsZero() {
		result.EvaluatedAt = time.Now().UTC()
	}
	if err := s.conn.Exec(ctx, `
		INSERT INTO crawlobserver.crawl_quality_results (
			session_id, project_id, baseline_session_id, status, score, trusted,
			is_full_crawl, summary, metrics, evaluated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		result.SessionID, result.ProjectID, result.BaselineSessionID, result.Status, result.Score, result.Trusted,
		result.IsFullCrawl, result.Summary, metricsJSON, result.EvaluatedAt,
	); err != nil {
		return fmt.Errorf("inserting crawl quality result: %w", err)
	}

	if err := s.conn.Exec(ctx, `ALTER TABLE crawlobserver.crawl_quality_findings DROP PARTITION ?`, result.SessionID); err != nil {
		return fmt.Errorf("clearing crawl quality findings: %w", err)
	}
	for _, f := range result.Findings {
		if f.CreatedAt.IsZero() {
			f.CreatedAt = result.EvaluatedAt
		}
		if f.SessionID == "" {
			f.SessionID = result.SessionID
		}
		if f.ProjectID == "" {
			f.ProjectID = result.ProjectID
		}
		if err := s.conn.Exec(ctx, `
			INSERT INTO crawlobserver.crawl_quality_findings (
				session_id, project_id, severity, finding_type, message, metric,
				current_value, baseline_value, threshold_value, blocking, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			f.SessionID, f.ProjectID, f.Severity, f.FindingType, f.Message, f.Metric,
			f.CurrentValue, f.BaselineValue, f.ThresholdValue, f.Blocking, f.CreatedAt,
		); err != nil {
			return fmt.Errorf("inserting crawl quality finding: %w", err)
		}
	}
	return nil
}

func (s *Store) GetCrawlQualityResult(ctx context.Context, sessionID string) (*CrawlQualityResult, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT session_id, project_id, baseline_session_id, status, score, trusted,
			is_full_crawl, summary, metrics, evaluated_at
		FROM crawlobserver.crawl_quality_results FINAL
		WHERE session_id = ?`, sessionID)
	result, err := scanQualityResult(row)
	if err != nil {
		return nil, err
	}
	findings, err := s.GetCrawlQualityFindings(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	result.Findings = findings
	return result, nil
}

func (s *Store) GetCrawlQualityFindings(ctx context.Context, sessionID string) ([]CrawlQualityFinding, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT session_id, project_id, severity, finding_type, message, metric,
			current_value, baseline_value, threshold_value, blocking, created_at
		FROM crawlobserver.crawl_quality_findings
		WHERE session_id = ?
		ORDER BY blocking DESC, severity DESC, finding_type ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var findings []CrawlQualityFinding
	for rows.Next() {
		var f CrawlQualityFinding
		if err := rows.Scan(
			&f.SessionID, &f.ProjectID, &f.Severity, &f.FindingType, &f.Message, &f.Metric,
			&f.CurrentValue, &f.BaselineValue, &f.ThresholdValue, &f.Blocking, &f.CreatedAt,
		); err != nil {
			return nil, err
		}
		findings = append(findings, f)
	}
	if findings == nil {
		findings = []CrawlQualityFinding{}
	}
	return findings, rows.Err()
}

func (s *Store) CrawlQualityResultsForSessions(ctx context.Context, sessionIDs []string) (map[string]CrawlQualityResult, error) {
	result := map[string]CrawlQualityResult{}
	if len(sessionIDs) == 0 {
		return result, nil
	}
	rows, err := s.conn.Query(ctx, `
		SELECT session_id, project_id, baseline_session_id, status, score, trusted,
			is_full_crawl, summary, metrics, evaluated_at
		FROM crawlobserver.crawl_quality_results FINAL
		WHERE session_id IN (?)`, sessionIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		qr, err := scanQualityResult(rows)
		if err != nil {
			return nil, err
		}
		result[qr.SessionID] = *qr
	}
	return result, rows.Err()
}

func (s *Store) LatestTrustedFullCrawlSession(ctx context.Context, projectID, excludeSessionID string) (*CrawlSession, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT cs.id, cs.started_at, cs.finished_at, cs.status, cs.seed_urls, cs.config,
			cs.pages_crawled, cs.user_agent, cs.project_id, cs.label
		FROM crawlobserver.crawl_sessions FINAL cs
		INNER JOIN crawlobserver.crawl_quality_results FINAL qr ON qr.session_id = cs.id
		WHERE cs.project_id = ? AND qr.project_id = ? AND qr.trusted = true
			AND qr.is_full_crawl = true AND cs.id != ?
		ORDER BY cs.started_at DESC
		LIMIT 1`, projectID, projectID, excludeSessionID)
	var sess CrawlSession
	if err := row.Scan(
		&sess.ID, &sess.StartedAt, &sess.FinishedAt, &sess.Status,
		&sess.SeedURLs, &sess.Config, &sess.PagesCrawled, &sess.UserAgent, &sess.ProjectID, &sess.Label,
	); err != nil {
		return nil, err
	}
	return &sess, nil
}

func (s *Store) CrawlQualityMetrics(ctx context.Context, sessionID string, topN int) (*CrawlQualityMetrics, error) {
	if topN <= 0 {
		topN = 20
	}
	row := s.conn.QueryRow(ctx, `
		SELECT
			countIf(page_type = 'html' OR content_type ILIKE '%html%'),
			countIf(status_code = 404),
			countIf(status_code >= 300 AND status_code < 400),
			countIf((page_type = 'html' OR content_type ILIKE '%html%') AND is_indexable = false),
			countIf((page_type = 'html' OR content_type ILIKE '%html%') AND canonical != '' AND canonical_is_self = false),
			sum(internal_links_out)
		FROM crawlobserver.pages FINAL
		WHERE crawl_session_id = ?`, sessionID)
	var m CrawlQualityMetrics
	if err := row.Scan(&m.HTMLPages, &m.Status404, &m.Redirects, &m.Noindex, &m.CanonicalMismatch, &m.InternalLinks); err != nil {
		return nil, err
	}
	row = s.conn.QueryRow(ctx, `
		SELECT count()
		FROM (
			SELECT url
			FROM crawlobserver.pages FINAL
			WHERE crawl_session_id = ? AND (page_type = 'html' OR content_type ILIKE '%html%')
			ORDER BY pagerank DESC
			LIMIT ?
		)
		WHERE url IN (
			SELECT url
			FROM crawlobserver.pages FINAL
			WHERE crawl_session_id = ? AND (page_type = 'html' OR content_type ILIKE '%html%') AND pagerank = 0
		)`, sessionID, topN, sessionID)
	_ = row.Scan(&m.PageRankZeroTopPages)
	return &m, nil
}

func (s *Store) TopPageRankURLs(ctx context.Context, sessionID string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.conn.Query(ctx, `
		SELECT url
		FROM crawlobserver.pages FINAL
		WHERE crawl_session_id = ? AND (page_type = 'html' OR content_type ILIKE '%html%')
		ORDER BY pagerank DESC
		LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var urls []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		urls = append(urls, u)
	}
	return urls, rows.Err()
}

func (s *Store) CanaryPageCheck(ctx context.Context, sessionID, canaryURL string) (*CanaryPageCheck, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT url, final_url, status_code, title, canonical, is_indexable, internal_links_out, pagerank
		FROM crawlobserver.pages FINAL
		WHERE crawl_session_id = ? AND (url = ? OR final_url = ?)
		ORDER BY crawled_at DESC
		LIMIT 1`, sessionID, canaryURL, canaryURL)
	var check CanaryPageCheck
	if err := row.Scan(
		&check.URL, &check.FinalURL, &check.StatusCode, &check.Title, &check.Canonical,
		&check.IsIndexable, &check.InternalLinksOut, &check.PageRank,
	); err != nil {
		return &CanaryPageCheck{Found: false}, nil
	}
	check.Found = true
	return &check, nil
}

type qualityScanner interface {
	Scan(dest ...interface{}) error
}

func scanQualityResult(row qualityScanner) (*CrawlQualityResult, error) {
	var result CrawlQualityResult
	var metricsJSON string
	if err := row.Scan(
		&result.SessionID, &result.ProjectID, &result.BaselineSessionID, &result.Status, &result.Score,
		&result.Trusted, &result.IsFullCrawl, &result.Summary, &metricsJSON, &result.EvaluatedAt,
	); err != nil {
		return nil, err
	}
	if metricsJSON != "" {
		_ = json.Unmarshal([]byte(metricsJSON), &result.Metrics)
	}
	if result.Metrics == nil {
		result.Metrics = map[string]interface{}{}
	}
	return &result, nil
}
