package storage

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// --- GSC Analytics & Inspection ---

type GSCQueryRow struct {
	Query       string  `json:"query"`
	Clicks      uint64  `json:"clicks"`
	Impressions uint64  `json:"impressions"`
	CTR         float64 `json:"ctr"`
	Position    float64 `json:"position"`
}

type GSCPageRow struct {
	Page        string  `json:"page"`
	Clicks      uint64  `json:"clicks"`
	Impressions uint64  `json:"impressions"`
	CTR         float64 `json:"ctr"`
	Position    float64 `json:"position"`
}

type GSCListOptions struct {
	Limit     int
	Offset    int
	Search    string
	Sort      string
	Direction string
}

type GSCCountryRow struct {
	Country     string  `json:"country"`
	Clicks      uint64  `json:"clicks"`
	Impressions uint64  `json:"impressions"`
	CTR         float64 `json:"ctr"`
	Position    float64 `json:"position"`
}

type GSCDeviceRow struct {
	Device      string  `json:"device"`
	Clicks      uint64  `json:"clicks"`
	Impressions uint64  `json:"impressions"`
	CTR         float64 `json:"ctr"`
	Position    float64 `json:"position"`
}

type GSCOverviewStats struct {
	TotalClicks      uint64  `json:"total_clicks"`
	TotalImpressions uint64  `json:"total_impressions"`
	AvgCTR           float64 `json:"avg_ctr"`
	AvgPosition      float64 `json:"avg_position"`
	DateMin          string  `json:"date_min"`
	DateMax          string  `json:"date_max"`
	TotalQueries     uint64  `json:"total_queries"`
	TotalPages       uint64  `json:"total_pages"`
}

type GSCTimelineRow struct {
	Date        string `json:"date"`
	Clicks      uint64 `json:"clicks"`
	Impressions uint64 `json:"impressions"`
}

type GSCInspectionRow struct {
	URL               string `json:"url"`
	Verdict           string `json:"verdict"`
	CoverageState     string `json:"coverage_state"`
	IndexingState     string `json:"indexing_state"`
	RobotsTxtState    string `json:"robots_txt_state"`
	LastCrawlTime     string `json:"last_crawl_time"`
	CrawledAs         string `json:"crawled_as"`
	CanonicalURL      string `json:"canonical_url"`
	IsGoogleCanonical bool   `json:"is_google_canonical"`
	MobileUsability   string `json:"mobile_usability"`
	RichResultsItems  uint16 `json:"rich_results_items"`
}

func (s *Store) InsertGSCAnalytics(ctx context.Context, projectID string, rows []GSCAnalyticsInsertRow) error {
	if len(rows) == 0 {
		return nil
	}
	batch, err := s.conn.PrepareBatch(ctx, `
		INSERT INTO crawlobserver.gsc_analytics (
			project_id, date, query, page, country, device,
			clicks, impressions, ctr, position, fetched_at
		)`)
	if err != nil {
		return fmt.Errorf("preparing gsc_analytics batch: %w", err)
	}
	now := time.Now()
	for _, r := range rows {
		if err := batch.Append(
			projectID, r.Date, r.Query, r.Page, r.Country, r.Device,
			r.Clicks, r.Impressions, r.CTR, r.Position, now,
		); err != nil {
			return fmt.Errorf("appending gsc_analytics row: %w", err)
		}
	}
	return batch.Send()
}

// GSCAnalyticsInsertRow is the input row for batch inserts.
type GSCAnalyticsInsertRow struct {
	Date        time.Time
	Query       string
	Page        string
	Country     string
	Device      string
	Clicks      uint32
	Impressions uint32
	CTR         float32
	Position    float32
}

func (s *Store) InsertGSCInspection(ctx context.Context, projectID string, rows []GSCInspectionInsertRow) error {
	if len(rows) == 0 {
		return nil
	}
	batch, err := s.conn.PrepareBatch(ctx, `
		INSERT INTO crawlobserver.gsc_inspection (
			project_id, url, verdict, coverage_state, indexing_state, robots_txt_state,
			last_crawl_time, crawled_as, canonical_url, is_google_canonical,
			mobile_usability, rich_results_items, fetched_at
		)`)
	if err != nil {
		return fmt.Errorf("preparing gsc_inspection batch: %w", err)
	}
	now := time.Now()
	for _, r := range rows {
		if err := batch.Append(
			projectID, r.URL, r.Verdict, r.CoverageState, r.IndexingState, r.RobotsTxtState,
			r.LastCrawlTime, r.CrawledAs, r.CanonicalURL, r.IsGoogleCanonical,
			r.MobileUsability, r.RichResultsItems, now,
		); err != nil {
			return fmt.Errorf("appending gsc_inspection row: %w", err)
		}
	}
	return batch.Send()
}

type GSCInspectionInsertRow struct {
	URL               string
	Verdict           string
	CoverageState     string
	IndexingState     string
	RobotsTxtState    string
	LastCrawlTime     time.Time
	CrawledAs         string
	CanonicalURL      string
	IsGoogleCanonical bool
	MobileUsability   string
	RichResultsItems  uint16
}

func (s *Store) GSCOverview(ctx context.Context, projectID string) (*GSCOverviewStats, error) {
	var stats GSCOverviewStats
	err := s.conn.QueryRow(ctx, `
		SELECT
			sum(clicks), sum(impressions),
			if(sum(impressions) > 0, sum(clicks) / sum(impressions), 0),
			if(sum(impressions) > 0, sum(position * impressions) / sum(impressions), 0),
			toString(min(date)), toString(max(date)),
			uniqExact(query), uniqExact(page)
		FROM crawlobserver.gsc_analytics FINAL
		WHERE project_id = ?`, projectID).Scan(
		&stats.TotalClicks, &stats.TotalImpressions,
		&stats.AvgCTR, &stats.AvgPosition,
		&stats.DateMin, &stats.DateMax,
		&stats.TotalQueries, &stats.TotalPages,
	)
	if err != nil {
		return nil, fmt.Errorf("querying gsc overview: %w", err)
	}
	return &stats, nil
}

func (s *Store) GSCTopQueries(ctx context.Context, projectID string, opts GSCListOptions) ([]GSCQueryRow, int, error) {
	sortExpr, sortDir := gscSortClause(opts.Sort, opts.Direction, "query")
	where, args := gscDimensionFilter("query", projectID, opts.Search)

	var total uint64
	countQuery := `SELECT uniqExact(query) FROM crawlobserver.gsc_analytics FINAL WHERE ` + where
	if err := s.conn.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting gsc queries: %w", err)
	}

	query := `
			SELECT query,
				sum(clicks) AS total_clicks,
				sum(impressions) AS total_impressions,
				if(sum(impressions) > 0, sum(clicks) / sum(impressions), 0) AS ctr,
				if(sum(impressions) > 0, sum(position * impressions) / sum(impressions), 0) AS avg_position
			FROM crawlobserver.gsc_analytics FINAL
			WHERE ` + where + `
			GROUP BY query
			ORDER BY ` + sortExpr + ` ` + sortDir + `
			LIMIT ? OFFSET ?`
	args = append(args, opts.Limit, opts.Offset)
	rows, err := s.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("querying gsc top queries: %w", err)
	}
	defer rows.Close()

	var result []GSCQueryRow
	for rows.Next() {
		var r GSCQueryRow
		if err := rows.Scan(&r.Query, &r.Clicks, &r.Impressions, &r.CTR, &r.Position); err != nil {
			return nil, 0, fmt.Errorf("scanning gsc query row: %w", err)
		}
		result = append(result, r)
	}
	if result == nil {
		result = []GSCQueryRow{}
	}
	return result, int(total), nil
}

func (s *Store) GSCTopPages(ctx context.Context, projectID string, opts GSCListOptions) ([]GSCPageRow, int, error) {
	sortExpr, sortDir := gscSortClause(opts.Sort, opts.Direction, "page")
	where, args := gscDimensionFilter("page", projectID, opts.Search)

	var total uint64
	countQuery := `SELECT uniqExact(page) FROM crawlobserver.gsc_analytics FINAL WHERE ` + where
	if err := s.conn.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting gsc pages: %w", err)
	}

	query := `
			SELECT page,
				sum(clicks) AS total_clicks,
				sum(impressions) AS total_impressions,
				if(sum(impressions) > 0, sum(clicks) / sum(impressions), 0) AS ctr,
				if(sum(impressions) > 0, sum(position * impressions) / sum(impressions), 0) AS avg_position
			FROM crawlobserver.gsc_analytics FINAL
			WHERE ` + where + `
			GROUP BY page
			ORDER BY ` + sortExpr + ` ` + sortDir + `
			LIMIT ? OFFSET ?`
	args = append(args, opts.Limit, opts.Offset)
	rows, err := s.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("querying gsc top pages: %w", err)
	}
	defer rows.Close()

	var result []GSCPageRow
	for rows.Next() {
		var r GSCPageRow
		if err := rows.Scan(&r.Page, &r.Clicks, &r.Impressions, &r.CTR, &r.Position); err != nil {
			return nil, 0, fmt.Errorf("scanning gsc page row: %w", err)
		}
		result = append(result, r)
	}
	if result == nil {
		result = []GSCPageRow{}
	}
	return result, int(total), nil
}

func (s *Store) GSCQueriesForPage(ctx context.Context, projectID, page string, opts GSCListOptions) ([]GSCQueryRow, int, error) {
	sortExpr, sortDir := gscSortClause(opts.Sort, opts.Direction, "query")
	where, args := gscPageQueryFilter(projectID, page, opts.Search)

	var total uint64
	countQuery := `SELECT uniqExact(query) FROM crawlobserver.gsc_analytics FINAL WHERE ` + where
	if err := s.conn.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting gsc page queries: %w", err)
	}

	query := `
			SELECT query,
				sum(clicks) AS total_clicks,
				sum(impressions) AS total_impressions,
				if(sum(impressions) > 0, sum(clicks) / sum(impressions), 0) AS ctr,
				if(sum(impressions) > 0, sum(position * impressions) / sum(impressions), 0) AS avg_position
			FROM crawlobserver.gsc_analytics FINAL
			WHERE ` + where + `
			GROUP BY query
			ORDER BY ` + sortExpr + ` ` + sortDir + `
			LIMIT ? OFFSET ?`
	args = append(args, opts.Limit, opts.Offset)
	rows, err := s.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("querying gsc page queries: %w", err)
	}
	defer rows.Close()

	var result []GSCQueryRow
	for rows.Next() {
		var r GSCQueryRow
		if err := rows.Scan(&r.Query, &r.Clicks, &r.Impressions, &r.CTR, &r.Position); err != nil {
			return nil, 0, fmt.Errorf("scanning gsc page query row: %w", err)
		}
		result = append(result, r)
	}
	if result == nil {
		result = []GSCQueryRow{}
	}
	return result, int(total), nil
}

func gscDimensionFilter(dimension, projectID, search string) (string, []any) {
	where := "project_id = ?"
	args := []any{projectID}
	search = strings.TrimSpace(search)
	if search != "" {
		where += " AND positionCaseInsensitive(" + dimension + ", ?) > 0"
		args = append(args, search)
	}
	return where, args
}

func gscPageQueryFilter(projectID, page, search string) (string, []any) {
	where := "project_id = ? AND page = ?"
	args := []any{projectID, page}
	search = strings.TrimSpace(search)
	if search != "" {
		where += " AND positionCaseInsensitive(query, ?) > 0"
		args = append(args, search)
	}
	return where, args
}

func gscSortClause(sort, direction, dimension string) (string, string) {
	sortMap := map[string]string{
		dimension:     dimension,
		"clicks":      "total_clicks",
		"impressions": "total_impressions",
		"ctr":         "ctr",
		"position":    "avg_position",
	}
	sortExpr, ok := sortMap[sort]
	if !ok {
		sortExpr = "total_clicks"
	}
	sortDir := "DESC"
	if strings.EqualFold(direction, "asc") {
		sortDir = "ASC"
	}
	return sortExpr, sortDir
}

func (s *Store) GSCByCountry(ctx context.Context, projectID string) ([]GSCCountryRow, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT country, sum(clicks), sum(impressions),
			if(sum(impressions) > 0, sum(clicks) / sum(impressions), 0),
			if(sum(impressions) > 0, sum(position * impressions) / sum(impressions), 0)
		FROM crawlobserver.gsc_analytics FINAL
		WHERE project_id = ?
		GROUP BY country
		ORDER BY sum(clicks) DESC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("querying gsc by country: %w", err)
	}
	defer rows.Close()

	var result []GSCCountryRow
	for rows.Next() {
		var r GSCCountryRow
		if err := rows.Scan(&r.Country, &r.Clicks, &r.Impressions, &r.CTR, &r.Position); err != nil {
			return nil, fmt.Errorf("scanning gsc country row: %w", err)
		}
		result = append(result, r)
	}
	if result == nil {
		result = []GSCCountryRow{}
	}
	return result, nil
}

func (s *Store) GSCByDevice(ctx context.Context, projectID string) ([]GSCDeviceRow, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT device, sum(clicks), sum(impressions),
			if(sum(impressions) > 0, sum(clicks) / sum(impressions), 0),
			if(sum(impressions) > 0, sum(position * impressions) / sum(impressions), 0)
		FROM crawlobserver.gsc_analytics FINAL
		WHERE project_id = ?
		GROUP BY device
		ORDER BY sum(clicks) DESC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("querying gsc by device: %w", err)
	}
	defer rows.Close()

	var result []GSCDeviceRow
	for rows.Next() {
		var r GSCDeviceRow
		if err := rows.Scan(&r.Device, &r.Clicks, &r.Impressions, &r.CTR, &r.Position); err != nil {
			return nil, fmt.Errorf("scanning gsc device row: %w", err)
		}
		result = append(result, r)
	}
	if result == nil {
		result = []GSCDeviceRow{}
	}
	return result, nil
}

func (s *Store) GSCTimeline(ctx context.Context, projectID string) ([]GSCTimelineRow, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT toString(date), sum(clicks), sum(impressions)
		FROM crawlobserver.gsc_analytics FINAL
		WHERE project_id = ?
		GROUP BY date
		ORDER BY date`, projectID)
	if err != nil {
		return nil, fmt.Errorf("querying gsc timeline: %w", err)
	}
	defer rows.Close()

	var result []GSCTimelineRow
	for rows.Next() {
		var r GSCTimelineRow
		if err := rows.Scan(&r.Date, &r.Clicks, &r.Impressions); err != nil {
			return nil, fmt.Errorf("scanning gsc timeline row: %w", err)
		}
		result = append(result, r)
	}
	if result == nil {
		result = []GSCTimelineRow{}
	}
	return result, nil
}

func (s *Store) GSCInspectionResults(ctx context.Context, projectID string, limit, offset int) ([]GSCInspectionRow, int, error) {
	var total uint64
	if err := s.conn.QueryRow(ctx, `
		SELECT count() FROM crawlobserver.gsc_inspection FINAL WHERE project_id = ?`, projectID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting gsc inspections: %w", err)
	}

	rows, err := s.conn.Query(ctx, `
		SELECT url, verdict, coverage_state, indexing_state, robots_txt_state,
			toString(last_crawl_time), crawled_as, canonical_url, is_google_canonical,
			mobile_usability, rich_results_items
		FROM crawlobserver.gsc_inspection FINAL
		WHERE project_id = ?
		ORDER BY url
		LIMIT ? OFFSET ?`, projectID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("querying gsc inspections: %w", err)
	}
	defer rows.Close()

	var result []GSCInspectionRow
	for rows.Next() {
		var r GSCInspectionRow
		if err := rows.Scan(&r.URL, &r.Verdict, &r.CoverageState, &r.IndexingState,
			&r.RobotsTxtState, &r.LastCrawlTime, &r.CrawledAs, &r.CanonicalURL,
			&r.IsGoogleCanonical, &r.MobileUsability, &r.RichResultsItems); err != nil {
			return nil, 0, fmt.Errorf("scanning gsc inspection row: %w", err)
		}
		result = append(result, r)
	}
	if result == nil {
		result = []GSCInspectionRow{}
	}
	return result, int(total), nil
}

func (s *Store) DeleteGSCData(ctx context.Context, projectID string) error {
	if err := s.conn.Exec(ctx, `ALTER TABLE crawlobserver.gsc_analytics DELETE WHERE project_id = ?`, projectID); err != nil {
		return fmt.Errorf("deleting gsc analytics: %w", err)
	}
	if err := s.conn.Exec(ctx, `ALTER TABLE crawlobserver.gsc_inspection DELETE WHERE project_id = ?`, projectID); err != nil {
		return fmt.Errorf("deleting gsc inspection: %w", err)
	}
	return nil
}
