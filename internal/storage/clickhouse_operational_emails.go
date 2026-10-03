package storage

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

var operationalEmailURLPattern = regexp.MustCompile(`(?i)https?://[^\s<>"]+`)

// SanitizeOperationalEmailURL removes URL credentials, fragments, and values
// for credential-like query parameters while keeping public paths and useful
// query parameters.
func SanitizeOperationalEmailURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", false
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", false
	}
	u.User = nil
	u.Fragment = ""
	u.RawFragment = ""
	u.RawQuery = sanitizeOperationalEmailRawQuery(u.RawQuery)
	return u.String(), true
}

func sanitizeOperationalEmailRawQuery(raw string) string {
	if raw == "" {
		return ""
	}
	var sanitized strings.Builder
	start := 0
	for i := 0; i <= len(raw); i++ {
		if i < len(raw) && raw[i] != '&' && raw[i] != ';' {
			continue
		}
		part := raw[start:i]
		key, value, hasValue := strings.Cut(part, "=")
		decodedKey, err := url.QueryUnescape(key)
		if err == nil && sensitiveOperationalEmailQueryKey(decodedKey) {
			sanitized.WriteString(key)
			sanitized.WriteString("=%5BREDACTED%5D")
		} else if hasValue && operationalEmailQueryValueHasCredentials(value, 0) {
			sanitized.WriteString(key)
			sanitized.WriteString("=%5BREDACTED%5D")
		} else {
			sanitized.WriteString(part)
		}
		if i < len(raw) {
			sanitized.WriteByte(raw[i])
		}
		start = i + 1
	}
	return sanitized.String()
}

func operationalEmailQueryValueHasCredentials(raw string, depth int) bool {
	if depth >= 2 {
		return false
	}
	candidate := raw
	if decoded, err := url.QueryUnescape(raw); err == nil {
		candidate = decoded
	}
	nestedURL, err := url.Parse(candidate)
	if err != nil || (nestedURL.Scheme != "http" && nestedURL.Scheme != "https") || nestedURL.Hostname() == "" {
		return false
	}
	if nestedURL.User != nil {
		return true
	}
	for _, part := range strings.FieldsFunc(nestedURL.RawQuery, func(r rune) bool { return r == '&' || r == ';' }) {
		key, value, hasValue := strings.Cut(part, "=")
		decodedKey, err := url.QueryUnescape(key)
		if err == nil && sensitiveOperationalEmailQueryKey(decodedKey) {
			return true
		}
		if hasValue && operationalEmailQueryValueHasCredentials(value, depth+1) {
			return true
		}
	}
	return false
}

// SanitizeOperationalEmailReason reuses the existing credential sanitizer,
// then redacts sensitive URLs embedded in fetch or execution errors.
func SanitizeOperationalEmailReason(reason string) string {
	message := SanitizePageRankEvidenceFailure(reason)
	matches := operationalEmailURLPattern.FindAllStringIndex(message, -1)
	if len(matches) == 0 {
		return message
	}
	var sanitized strings.Builder
	last := 0
	for _, match := range matches {
		sanitized.WriteString(message[last:match[0]])
		raw := message[match[0]:match[1]]
		urlEnd := len(raw)
		for urlEnd > 0 && strings.ContainsRune(".,;:!?)]}", rune(raw[urlEnd-1])) {
			urlEnd--
		}
		trailing := raw[urlEnd:]
		if safeURL, ok := SanitizeOperationalEmailURL(raw[:urlEnd]); ok {
			sanitized.WriteString(safeURL)
		} else {
			sanitized.WriteString("[URL omitted]")
		}
		sanitized.WriteString(trailing)
		last = match[1]
	}
	sanitized.WriteString(message[last:])
	result := sanitized.String()
	if len(result) > 1024 {
		result = result[:1024]
	}
	return result
}

func sensitiveOperationalEmailQueryKey(key string) bool {
	normalized := strings.ToLower(strings.NewReplacer("-", "", "_", "", ".", "").Replace(key))
	for _, marker := range []string{"token", "secret", "password", "passwd", "credential", "signature", "authorization", "apikey", "accesskey"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return normalized == "key" || normalized == "auth" || normalized == "code" || strings.HasSuffix(normalized, "oauthcode")
}

// PageErrorsForSession returns every page error observed in the terminal
// session, independent of whether an error is new to the project.
func (s *Store) PageErrorsForSession(ctx context.Context, session CrawlSession) ([]PageErrorObservation, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT url, status_code, error
		FROM crawlobserver.pages FINAL
		WHERE crawl_session_id = ? AND (status_code = 0 OR status_code >= 400 OR error != '')
		ORDER BY url ASC`, session.ID)
	if err != nil {
		return nil, fmt.Errorf("querying page errors for session %s: %w", session.ID, err)
	}
	defer rows.Close()
	var current []PageErrorObservation
	for rows.Next() {
		var observation PageErrorObservation
		var fetchError string
		if err := rows.Scan(&observation.URL, &observation.StatusCode, &fetchError); err != nil {
			return nil, fmt.Errorf("scanning page error for session %s: %w", session.ID, err)
		}
		observation.FetchError = strings.TrimSpace(fetchError) != ""
		observation.FetchReason = SanitizeOperationalEmailReason(fetchError)
		current = append(current, observation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating page errors for session %s: %w", session.ID, err)
	}
	if current == nil {
		current = []PageErrorObservation{}
	}
	return current, nil
}

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
	current, err := s.PageErrorsForSession(ctx, session)
	if err != nil {
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
