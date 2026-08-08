package storage

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// InsertLinks batch inserts link rows.
func (s *Store) InsertLinks(ctx context.Context, links []LinkRow) error {
	if len(links) == 0 {
		return nil
	}

	batch, err := s.conn.PrepareBatch(ctx, `
		INSERT INTO crawlobserver.links (
			crawl_session_id, source_url, target_url, anchor_text, rel,
			is_internal, tag, link_location, crawled_at
		)`)
	if err != nil {
		return fmt.Errorf("preparing links batch: %w", err)
	}

	for _, l := range links {
		if err := batch.Append(
			l.CrawlSessionID, l.SourceURL, l.TargetURL, l.AnchorText, l.Rel,
			l.IsInternal, l.Tag, normalizeLinkLocation(l.LinkLocation), l.CrawledAt,
		); err != nil {
			return fmt.Errorf("appending link row: %w", err)
		}
	}

	return batch.Send()
}

func normalizeLinkLocation(location string) string {
	if location == "footer" {
		return "footer"
	}
	return "body"
}

// ExistingLinkPairs reports whether each source->target pair exists in a session.
func (s *Store) ExistingLinkPairs(ctx context.Context, sessionID string, links []VirtualLink) (map[VirtualLink]bool, error) {
	resolved, err := s.ResolveExistingLinkPairs(ctx, sessionID, links)
	if err != nil {
		return nil, err
	}
	result := make(map[VirtualLink]bool, len(links))
	for _, raw := range links {
		link := VirtualLink{
			SourceURL: strings.TrimSpace(raw.SourceURL),
			TargetURL: strings.TrimSpace(raw.TargetURL),
		}
		if link.SourceURL == "" || link.TargetURL == "" {
			continue
		}
		if _, seen := result[link]; seen {
			continue
		}
		_, ok := resolved[link]
		result[link] = ok
	}
	return result, nil
}

// ResolveExistingLinkPairs maps requested source->target pairs to their stored form.
func (s *Store) ResolveExistingLinkPairs(ctx context.Context, sessionID string, links []VirtualLink) (map[VirtualLink]VirtualLink, error) {
	result := make(map[VirtualLink]VirtualLink, len(links))
	for _, raw := range links {
		link := VirtualLink{
			SourceURL: strings.TrimSpace(raw.SourceURL),
			TargetURL: strings.TrimSpace(raw.TargetURL),
		}
		if link.SourceURL == "" || link.TargetURL == "" {
			continue
		}
		if _, seen := result[link]; seen {
			continue
		}
		sourceCandidates := urlWWWVariants(link.SourceURL)
		targetCandidates := urlWWWVariants(link.TargetURL)
		args := []interface{}{sessionID}
		query := `
			SELECT source_url, target_url
			FROM crawlobserver.links
			WHERE crawl_session_id = ? AND is_internal = true
			  AND source_url IN (` + placeholders(len(sourceCandidates)) + `)
			  AND target_url IN (` + placeholders(len(targetCandidates)) + `)
			LIMIT 1`
		for _, candidate := range sourceCandidates {
			args = append(args, candidate)
		}
		for _, candidate := range targetCandidates {
			args = append(args, candidate)
		}
		var resolved VirtualLink
		if err := s.conn.QueryRow(ctx, query, args...).Scan(&resolved.SourceURL, &resolved.TargetURL); err != nil {
			if strings.Contains(err.Error(), "no rows") {
				continue
			}
			return nil, fmt.Errorf("checking existing link %s -> %s: %w", link.SourceURL, link.TargetURL, err)
		}
		result[link] = resolved
	}
	return result, nil
}

func urlWWWVariants(rawURL string) []string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return nil
	}
	seen := map[string]struct{}{trimmed: {}}
	result := []string{trimmed}
	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return result
	}
	host := u.Host
	if strings.HasPrefix(strings.ToLower(host), "www.") {
		u.Host = host[4:]
	} else {
		u.Host = "www." + host
	}
	alt := u.String()
	if _, ok := seen[alt]; !ok {
		result = append(result, alt)
	}
	return result
}

// ExternalLinks retrieves external links for a given session (or all sessions).
func (s *Store) ExternalLinks(ctx context.Context, sessionID string) ([]LinkRow, error) {
	query := `
		SELECT crawl_session_id, source_url, target_url, anchor_text, rel, is_internal, tag, link_location, crawled_at
		FROM crawlobserver.links
		WHERE is_internal = false`
	args := []interface{}{}

	if sessionID != "" {
		query += ` AND crawl_session_id = ?`
		args = append(args, sessionID)
	}
	query += ` ORDER BY source_url, target_url`

	rows, err := s.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying external links: %w", err)
	}
	defer rows.Close()

	var links []LinkRow
	for rows.Next() {
		var l LinkRow
		if err := rows.Scan(
			&l.CrawlSessionID, &l.SourceURL, &l.TargetURL, &l.AnchorText,
			&l.Rel, &l.IsInternal, &l.Tag, &l.LinkLocation, &l.CrawledAt,
		); err != nil {
			return nil, fmt.Errorf("scanning link: %w", err)
		}
		links = append(links, l)
	}
	return links, nil
}

// ExternalLinksPaginated retrieves external links with pagination and optional filters.
func (s *Store) ExternalLinksPaginated(ctx context.Context, sessionID string, limit, offset int, filters []ParsedFilter, sort *SortParam) ([]LinkRow, error) {
	query := `
		SELECT crawl_session_id, source_url, target_url, anchor_text, rel, is_internal, tag, link_location, crawled_at
		FROM crawlobserver.links
		WHERE is_internal = false`
	args := []interface{}{}

	if sessionID != "" {
		query += ` AND crawl_session_id = ?`
		args = append(args, sessionID)
	}

	whereExtra, filterArgs, err := BuildWhereClause(filters)
	if err != nil {
		return nil, fmt.Errorf("building filter clause: %w", err)
	}
	if whereExtra != "" {
		query += " AND " + whereExtra
		args = append(args, filterArgs...)
	}

	query += BuildOrderByClause(sort, "source_url, target_url") + ` LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := s.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying external links: %w", err)
	}
	defer rows.Close()

	var links []LinkRow
	for rows.Next() {
		var l LinkRow
		if err := rows.Scan(
			&l.CrawlSessionID, &l.SourceURL, &l.TargetURL, &l.AnchorText,
			&l.Rel, &l.IsInternal, &l.Tag, &l.LinkLocation, &l.CrawledAt,
		); err != nil {
			return nil, fmt.Errorf("scanning link: %w", err)
		}
		links = append(links, l)
	}
	return links, nil
}

// InternalLinksPaginated retrieves internal links with pagination and optional filters.
func (s *Store) InternalLinksPaginated(ctx context.Context, sessionID string, limit, offset int, filters []ParsedFilter, sort *SortParam) ([]LinkRow, error) {
	query := `
		SELECT crawl_session_id, source_url, target_url, anchor_text, rel, is_internal, tag, link_location, crawled_at
		FROM crawlobserver.links
		WHERE is_internal = true AND crawl_session_id = ?`
	args := []interface{}{sessionID}

	whereExtra, filterArgs, err := BuildWhereClause(filters)
	if err != nil {
		return nil, fmt.Errorf("building filter clause: %w", err)
	}
	if whereExtra != "" {
		query += " AND " + whereExtra
		args = append(args, filterArgs...)
	}

	query += BuildOrderByClause(sort, "source_url, target_url") + ` LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := s.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying internal links: %w", err)
	}
	defer rows.Close()

	var links []LinkRow
	for rows.Next() {
		var l LinkRow
		if err := rows.Scan(
			&l.CrawlSessionID, &l.SourceURL, &l.TargetURL, &l.AnchorText,
			&l.Rel, &l.IsInternal, &l.Tag, &l.LinkLocation, &l.CrawledAt,
		); err != nil {
			return nil, fmt.Errorf("scanning link: %w", err)
		}
		links = append(links, l)
	}
	return links, nil
}
