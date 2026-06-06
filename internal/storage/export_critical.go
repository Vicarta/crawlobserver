package storage

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CriticalTables lists the non-regenerable tables that must be backed up separately.
var CriticalTables = []string{
	"gsc_analytics",
	"gsc_inspection",
	"provider_domain_metrics",
	"provider_backlinks",
	"provider_refdomains",
	"provider_rankings",
	"provider_visibility",
	"provider_top_pages",
	"provider_data",
}

// ExportCriticalTables exports each non-regenerable table to a separate gzipped JSONL file.
// Files are written as <dir>/<table>_<timestamp>.jsonl.gz.
// Errors on individual tables are accumulated — the function exports as many tables as possible.
func (s *Store) ExportCriticalTables(ctx context.Context, dir string, retain int) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating export dir: %w", err)
	}

	ts := time.Now().Format("20060102T150405")

	var errs []string
	for _, table := range CriticalTables {
		if err := s.exportCriticalTable(ctx, dir, table, ts); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", table, err))
		}
	}

	// Prune old exports
	if retain > 0 {
		for _, table := range CriticalTables {
			pruneTableExports(dir, table, retain)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("export errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (s *Store) exportCriticalTable(ctx context.Context, dir, table, ts string) error {
	// Check if table has any data
	var count uint64
	if err := s.conn.QueryRow(ctx, fmt.Sprintf("SELECT count() FROM %s", table)).Scan(&count); err != nil {
		// Table might not exist yet — skip silently
		return nil
	}
	if count == 0 {
		return nil
	}

	filename := fmt.Sprintf("%s_%s.jsonl.gz", table, ts)
	path := filepath.Join(dir, filename)

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating file: %w", err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	enc := json.NewEncoder(gw)

	if err := s.exportCriticalTableRows(ctx, table, enc); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}

func (s *Store) exportCriticalTableRows(ctx context.Context, table string, enc *json.Encoder) error {
	switch table {
	case "gsc_analytics":
		return s.exportGSCAnalyticsRows(ctx, enc)
	case "gsc_inspection":
		return s.exportGSCInspectionRows(ctx, enc)
	case "provider_domain_metrics":
		return s.exportProviderDomainMetricsRows(ctx, enc)
	case "provider_backlinks":
		return s.exportProviderBacklinkRows(ctx, enc)
	case "provider_refdomains":
		return s.exportProviderRefDomainRows(ctx, enc)
	case "provider_rankings":
		return s.exportProviderRankingRows(ctx, enc)
	case "provider_visibility":
		return s.exportProviderVisibilityRows(ctx, enc)
	case "provider_top_pages":
		return s.exportProviderTopPageRows(ctx, enc)
	case "provider_data":
		return s.exportProviderDataRows(ctx, enc)
	default:
		return fmt.Errorf("unsupported critical table %q", table)
	}
}

func (s *Store) exportGSCAnalyticsRows(ctx context.Context, enc *json.Encoder) error {
	rows, err := s.conn.Query(ctx, `
		SELECT project_id, date, query, page, country, device,
			clicks, impressions, ctr, position, fetched_at
		FROM crawlobserver.gsc_analytics FINAL`)
	if err != nil {
		return fmt.Errorf("querying: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var projectID, query, page, country, device string
		var date, fetchedAt time.Time
		var clicks, impressions uint32
		var ctr, position float32
		if err := rows.Scan(&projectID, &date, &query, &page, &country, &device, &clicks, &impressions, &ctr, &position, &fetchedAt); err != nil {
			return fmt.Errorf("scanning row: %w", err)
		}
		if err := enc.Encode(map[string]interface{}{
			"project_id": projectID, "date": date, "query": query, "page": page,
			"country": country, "device": device, "clicks": clicks,
			"impressions": impressions, "ctr": ctr, "position": position, "fetched_at": fetchedAt,
		}); err != nil {
			return fmt.Errorf("encoding row: %w", err)
		}
	}
	return rows.Err()
}

func (s *Store) exportGSCInspectionRows(ctx context.Context, enc *json.Encoder) error {
	rows, err := s.conn.Query(ctx, `
		SELECT project_id, url, verdict, coverage_state, indexing_state, robots_txt_state,
			last_crawl_time, crawled_as, canonical_url, is_google_canonical,
			mobile_usability, rich_results_items, fetched_at
		FROM crawlobserver.gsc_inspection FINAL`)
	if err != nil {
		return fmt.Errorf("querying: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var projectID, url, verdict, coverageState, indexingState, robotsTxtState string
		var crawledAs, canonicalURL, mobileUsability string
		var lastCrawlTime, fetchedAt time.Time
		var isGoogleCanonical bool
		var richResultsItems uint16
		if err := rows.Scan(&projectID, &url, &verdict, &coverageState, &indexingState, &robotsTxtState, &lastCrawlTime, &crawledAs, &canonicalURL, &isGoogleCanonical, &mobileUsability, &richResultsItems, &fetchedAt); err != nil {
			return fmt.Errorf("scanning row: %w", err)
		}
		if err := enc.Encode(map[string]interface{}{
			"project_id": projectID, "url": url, "verdict": verdict,
			"coverage_state": coverageState, "indexing_state": indexingState,
			"robots_txt_state": robotsTxtState, "last_crawl_time": lastCrawlTime,
			"crawled_as": crawledAs, "canonical_url": canonicalURL,
			"is_google_canonical": isGoogleCanonical, "mobile_usability": mobileUsability,
			"rich_results_items": richResultsItems, "fetched_at": fetchedAt,
		}); err != nil {
			return fmt.Errorf("encoding row: %w", err)
		}
	}
	return rows.Err()
}

func (s *Store) exportProviderDomainMetricsRows(ctx context.Context, enc *json.Encoder) error {
	rows, err := s.conn.Query(ctx, `
		SELECT project_id, provider, domain, backlinks_total, refdomains_total, domain_rank,
			organic_keywords, organic_traffic, organic_cost, fetched_at
		FROM crawlobserver.provider_domain_metrics FINAL`)
	if err != nil {
		return fmt.Errorf("querying: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var projectID, provider, domain string
		var backlinksTotal, refdomainsTotal, organicKeywords, organicTraffic int64
		var domainRank, organicCost float64
		var fetchedAt time.Time
		if err := rows.Scan(&projectID, &provider, &domain, &backlinksTotal, &refdomainsTotal, &domainRank, &organicKeywords, &organicTraffic, &organicCost, &fetchedAt); err != nil {
			return fmt.Errorf("scanning row: %w", err)
		}
		if err := enc.Encode(map[string]interface{}{
			"project_id": projectID, "provider": provider, "domain": domain,
			"backlinks_total": backlinksTotal, "refdomains_total": refdomainsTotal,
			"domain_rank": domainRank, "organic_keywords": organicKeywords,
			"organic_traffic": organicTraffic, "organic_cost": organicCost, "fetched_at": fetchedAt,
		}); err != nil {
			return fmt.Errorf("encoding row: %w", err)
		}
	}
	return rows.Err()
}

func (s *Store) exportProviderBacklinkRows(ctx context.Context, enc *json.Encoder) error {
	rows, err := s.conn.Query(ctx, `
		SELECT project_id, provider, domain, source_url, target_url, anchor_text,
			source_domain, link_type, domain_rank, page_rank, source_ttf_topic, nofollow,
			first_seen, last_seen, fetched_at
		FROM crawlobserver.provider_backlinks FINAL`)
	if err != nil {
		return fmt.Errorf("querying: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var projectID, provider, domain, sourceURL, targetURL, anchorText, sourceDomain, linkType, sourceTTFTopic string
		var domainRank, pageRank float64
		var nofollow bool
		var firstSeen, lastSeen, fetchedAt time.Time
		if err := rows.Scan(&projectID, &provider, &domain, &sourceURL, &targetURL, &anchorText, &sourceDomain, &linkType, &domainRank, &pageRank, &sourceTTFTopic, &nofollow, &firstSeen, &lastSeen, &fetchedAt); err != nil {
			return fmt.Errorf("scanning row: %w", err)
		}
		if err := enc.Encode(map[string]interface{}{
			"project_id": projectID, "provider": provider, "domain": domain,
			"source_url": sourceURL, "target_url": targetURL, "anchor_text": anchorText,
			"source_domain": sourceDomain, "link_type": linkType, "domain_rank": domainRank,
			"page_rank": pageRank, "source_ttf_topic": sourceTTFTopic, "nofollow": nofollow,
			"first_seen": firstSeen, "last_seen": lastSeen, "fetched_at": fetchedAt,
		}); err != nil {
			return fmt.Errorf("encoding row: %w", err)
		}
	}
	return rows.Err()
}

func (s *Store) exportProviderRefDomainRows(ctx context.Context, enc *json.Encoder) error {
	rows, err := s.conn.Query(ctx, `
		SELECT project_id, provider, domain, ref_domain, backlink_count,
			domain_rank, first_seen, last_seen, fetched_at
		FROM crawlobserver.provider_refdomains FINAL`)
	if err != nil {
		return fmt.Errorf("querying: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var projectID, provider, domain, refDomain string
		var backlinkCount int64
		var domainRank float64
		var firstSeen, lastSeen, fetchedAt time.Time
		if err := rows.Scan(&projectID, &provider, &domain, &refDomain, &backlinkCount, &domainRank, &firstSeen, &lastSeen, &fetchedAt); err != nil {
			return fmt.Errorf("scanning row: %w", err)
		}
		if err := enc.Encode(map[string]interface{}{
			"project_id": projectID, "provider": provider, "domain": domain,
			"ref_domain": refDomain, "backlink_count": backlinkCount,
			"domain_rank": domainRank, "first_seen": firstSeen, "last_seen": lastSeen, "fetched_at": fetchedAt,
		}); err != nil {
			return fmt.Errorf("encoding row: %w", err)
		}
	}
	return rows.Err()
}

func (s *Store) exportProviderRankingRows(ctx context.Context, enc *json.Encoder) error {
	rows, err := s.conn.Query(ctx, `
		SELECT project_id, provider, domain, keyword, url, search_base,
			position, search_volume, cpc, traffic, traffic_pct, fetched_at
		FROM crawlobserver.provider_rankings FINAL`)
	if err != nil {
		return fmt.Errorf("querying: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var projectID, provider, domain, keyword, url, searchBase string
		var position uint16
		var searchVolume int64
		var cpc, traffic, trafficPct float64
		var fetchedAt time.Time
		if err := rows.Scan(&projectID, &provider, &domain, &keyword, &url, &searchBase, &position, &searchVolume, &cpc, &traffic, &trafficPct, &fetchedAt); err != nil {
			return fmt.Errorf("scanning row: %w", err)
		}
		if err := enc.Encode(map[string]interface{}{
			"project_id": projectID, "provider": provider, "domain": domain,
			"keyword": keyword, "url": url, "search_base": searchBase,
			"position": position, "search_volume": searchVolume, "cpc": cpc,
			"traffic": traffic, "traffic_pct": trafficPct, "fetched_at": fetchedAt,
		}); err != nil {
			return fmt.Errorf("encoding row: %w", err)
		}
	}
	return rows.Err()
}

func (s *Store) exportProviderVisibilityRows(ctx context.Context, enc *json.Encoder) error {
	rows, err := s.conn.Query(ctx, `
		SELECT project_id, provider, domain, search_base, date,
			visibility, keywords_count, fetched_at
		FROM crawlobserver.provider_visibility FINAL`)
	if err != nil {
		return fmt.Errorf("querying: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var projectID, provider, domain, searchBase string
		var date, fetchedAt time.Time
		var visibility float64
		var keywordsCount int64
		if err := rows.Scan(&projectID, &provider, &domain, &searchBase, &date, &visibility, &keywordsCount, &fetchedAt); err != nil {
			return fmt.Errorf("scanning row: %w", err)
		}
		if err := enc.Encode(map[string]interface{}{
			"project_id": projectID, "provider": provider, "domain": domain,
			"search_base": searchBase, "date": date, "visibility": visibility,
			"keywords_count": keywordsCount, "fetched_at": fetchedAt,
		}); err != nil {
			return fmt.Errorf("encoding row: %w", err)
		}
	}
	return rows.Err()
}

func (s *Store) exportProviderTopPageRows(ctx context.Context, enc *json.Encoder) error {
	rows, err := s.conn.Query(ctx, `
		SELECT project_id, provider, domain, url, title, trust_flow, citation_flow,
			ext_backlinks, ref_domains, topical_trust_flow, language, fetched_at
		FROM crawlobserver.provider_top_pages FINAL`)
	if err != nil {
		return fmt.Errorf("querying: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var projectID, provider, domain, url, title, language string
		var trustFlow, citationFlow uint8
		var extBacklinks, refDomains int64
		var topicalTrustFlow [][]interface{}
		var fetchedAt time.Time
		if err := rows.Scan(&projectID, &provider, &domain, &url, &title, &trustFlow, &citationFlow, &extBacklinks, &refDomains, &topicalTrustFlow, &language, &fetchedAt); err != nil {
			return fmt.Errorf("scanning row: %w", err)
		}
		if err := enc.Encode(map[string]interface{}{
			"project_id": projectID, "provider": provider, "domain": domain,
			"url": url, "title": title, "trust_flow": trustFlow, "citation_flow": citationFlow,
			"ext_backlinks": extBacklinks, "ref_domains": refDomains,
			"topical_trust_flow": topicalTrustFlow, "language": language, "fetched_at": fetchedAt,
		}); err != nil {
			return fmt.Errorf("encoding row: %w", err)
		}
	}
	return rows.Err()
}

func (s *Store) exportProviderDataRows(ctx context.Context, enc *json.Encoder) error {
	rows, err := s.conn.Query(ctx, `
		SELECT project_id, provider, data_type, domain, item_url,
			trust_flow, citation_flow, domain_rank, ext_backlinks, ref_domains,
			str_data, num_data, fetched_at
		FROM crawlobserver.provider_data FINAL`)
	if err != nil {
		return fmt.Errorf("querying: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var projectID, provider, dataType, domain, itemURL string
		var trustFlow, citationFlow uint8
		var domainRank float64
		var extBacklinks, refDomains int64
		var strData map[string]string
		var numData map[string]float64
		var fetchedAt time.Time
		if err := rows.Scan(&projectID, &provider, &dataType, &domain, &itemURL, &trustFlow, &citationFlow, &domainRank, &extBacklinks, &refDomains, &strData, &numData, &fetchedAt); err != nil {
			return fmt.Errorf("scanning row: %w", err)
		}
		if err := enc.Encode(map[string]interface{}{
			"project_id": projectID, "provider": provider, "data_type": dataType,
			"domain": domain, "item_url": itemURL, "trust_flow": trustFlow,
			"citation_flow": citationFlow, "domain_rank": domainRank,
			"ext_backlinks": extBacklinks, "ref_domains": refDomains,
			"str_data": strData, "num_data": numData, "fetched_at": fetchedAt,
		}); err != nil {
			return fmt.Errorf("encoding row: %w", err)
		}
	}
	return rows.Err()
}

// ImportCriticalTable reads a JSONL file and inserts rows into the named table.
func (s *Store) ImportCriticalTable(ctx context.Context, table string, r io.Reader) error {
	dec := json.NewDecoder(r)
	var batch []map[string]interface{}
	const batchSize = 1000

	for {
		var row map[string]interface{}
		if err := dec.Decode(&row); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("decoding JSONL: %w", err)
		}
		batch = append(batch, row)

		if len(batch) >= batchSize {
			if err := s.insertCriticalBatch(ctx, table, batch); err != nil {
				return err
			}
			batch = batch[:0]
		}
	}

	if len(batch) > 0 {
		return s.insertCriticalBatch(ctx, table, batch)
	}
	return nil
}

func (s *Store) insertCriticalBatch(ctx context.Context, table string, rows []map[string]interface{}) error {
	if len(rows) == 0 {
		return nil
	}

	// Build column list from first row
	cols := make([]string, 0, len(rows[0]))
	for k := range rows[0] {
		cols = append(cols, k)
	}
	sort.Strings(cols)

	placeholders := make([]string, len(cols))
	for i := range placeholders {
		placeholders[i] = "?"
	}

	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		table, strings.Join(cols, ", "), strings.Join(placeholders, ", "))

	batch, err := s.conn.PrepareBatch(ctx, query)
	if err != nil {
		return fmt.Errorf("preparing batch: %w", err)
	}

	for _, row := range rows {
		values := make([]interface{}, len(cols))
		for i, col := range cols {
			values[i] = row[col]
		}
		if err := batch.Append(values...); err != nil {
			return fmt.Errorf("appending row: %w", err)
		}
	}

	return batch.Send()
}

// pruneTableExports keeps only the most recent N exports for a given table.
func pruneTableExports(dir, table string, keep int) {
	prefix := table + "_"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	var matches []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), prefix) && strings.HasSuffix(e.Name(), ".jsonl.gz") {
			matches = append(matches, e.Name())
		}
	}

	// Sort ascending (oldest first by name since timestamp is embedded)
	sort.Strings(matches)

	if len(matches) <= keep {
		return
	}
	for _, name := range matches[:len(matches)-keep] {
		os.Remove(filepath.Join(dir, name))
	}
}
