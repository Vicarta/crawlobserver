package storage

import (
	"context"
	"fmt"
	"strings"
)

const cwvEligibleFilter = `content_type LIKE '%html%'
	AND status_code >= 200 AND status_code < 300
	AND ` + notRedirectedFilter

const cwvValidMeasurementFilter = `cwv_measured
	AND cwv_lcp_ms > 0
	AND cwv_ttfb_ms > 0`

const cwvOverallRatingExpression = `multiIf(
	cwv_lcp_ms > 4000 OR cwv_cls > 0.25 OR cwv_ttfb_ms > 1800, 'poor',
	cwv_lcp_ms > 2500 OR cwv_cls > 0.1 OR cwv_ttfb_ms > 800, 'needs_improvement',
	'good'
)`

// CoreWebVitalsReport returns a filtered, sortable page of CWV lab results and
// an unfiltered summary for the crawl session.
func (s *Store) CoreWebVitalsReport(
	ctx context.Context,
	sessionID string,
	limit, offset int,
	rating, sort, order string,
) (*CoreWebVitalsReport, error) {
	rating = normalizeCWVRating(rating)
	sortExpr, order := cwvSort(sort, order)

	result := &CoreWebVitalsReport{Pages: []CoreWebVitalsPage{}}
	summaryRow := s.conn.QueryRow(ctx, `
		SELECT
			count() AS eligible_pages,
			countIf(`+cwvValidMeasurementFilter+`) AS measured_pages,
			countIf(`+cwvValidMeasurementFilter+` AND cwv_lcp_ms <= 2500 AND cwv_cls <= 0.1 AND cwv_ttfb_ms <= 800) AS good,
			countIf(`+cwvValidMeasurementFilter+` AND NOT (cwv_lcp_ms > 4000 OR cwv_cls > 0.25 OR cwv_ttfb_ms > 1800)
				AND (cwv_lcp_ms > 2500 OR cwv_cls > 0.1 OR cwv_ttfb_ms > 800)) AS needs_improvement,
			countIf(`+cwvValidMeasurementFilter+` AND (cwv_lcp_ms > 4000 OR cwv_cls > 0.25 OR cwv_ttfb_ms > 1800)) AS poor
		FROM crawlobserver.pages FINAL
		WHERE crawl_session_id = ? AND `+cwvEligibleFilter, sessionID)
	if err := summaryRow.Scan(
		&result.Summary.EligiblePages,
		&result.Summary.MeasuredPages,
		&result.Summary.Good,
		&result.Summary.NeedsImprovement,
		&result.Summary.Poor,
	); err != nil {
		return nil, fmt.Errorf("querying core web vitals summary: %w", err)
	}
	if result.Summary.EligiblePages > result.Summary.MeasuredPages {
		result.Summary.UnmeasuredPages = result.Summary.EligiblePages - result.Summary.MeasuredPages
	}

	baseQuery := `
		WITH rated AS (
			SELECT
				url,
				cwv_lcp_ms,
				cwv_cls,
				cwv_ttfb_ms,
				` + cwvOverallRatingExpression + ` AS overall_rating
			FROM crawlobserver.pages FINAL
			WHERE crawl_session_id = ? AND ` + cwvValidMeasurementFilter + ` AND ` + cwvEligibleFilter + `
		)
`
	countQuery := baseQuery + `SELECT count() FROM rated WHERE (? = '' OR overall_rating = ?)`
	if err := s.conn.QueryRow(ctx, countQuery, sessionID, rating, rating).Scan(&result.Total); err != nil {
		return nil, fmt.Errorf("counting core web vitals pages: %w", err)
	}

	pageQuery := baseQuery + `
		SELECT url, cwv_lcp_ms, cwv_cls, cwv_ttfb_ms, overall_rating
		FROM rated
		WHERE (? = '' OR overall_rating = ?)
		ORDER BY ` + sortExpr + ` ` + order + `, url ASC
		LIMIT ? OFFSET ?`
	rows, err := s.conn.Query(ctx, pageQuery, sessionID, rating, rating, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("querying core web vitals pages: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var page CoreWebVitalsPage
		if err := rows.Scan(&page.URL, &page.LCPMs, &page.CLS, &page.TTFBMs, &page.OverallRating); err != nil {
			return nil, fmt.Errorf("scanning core web vitals page: %w", err)
		}
		page.LCPRating = coreWebVitalRating(page.LCPMs, 2500, 4000)
		page.CLSRating = coreWebVitalRating(page.CLS, 0.1, 0.25)
		page.TTFBRating = coreWebVitalRating(page.TTFBMs, 800, 1800)
		result.Pages = append(result.Pages, page)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating core web vitals pages: %w", err)
	}
	return result, nil
}

func coreWebVitalRating(value, goodMax, needsImprovementMax float64) string {
	if value <= goodMax {
		return "good"
	}
	if value <= needsImprovementMax {
		return "needs_improvement"
	}
	return "poor"
}

func normalizeCWVRating(rating string) string {
	switch strings.ToLower(strings.TrimSpace(rating)) {
	case "good", "needs_improvement", "poor":
		return strings.ToLower(strings.TrimSpace(rating))
	default:
		return ""
	}
}

func cwvSort(sort, order string) (string, string) {
	sortExpressions := map[string]string{
		"url":     "url",
		"lcp":     "cwv_lcp_ms",
		"cls":     "cwv_cls",
		"ttfb":    "cwv_ttfb_ms",
		"overall": "multiIf(overall_rating = 'poor', 3, overall_rating = 'needs_improvement', 2, 1)",
	}
	sortExpr, ok := sortExpressions[strings.ToLower(sort)]
	if !ok {
		sortExpr = sortExpressions["overall"]
	}
	order = strings.ToUpper(order)
	if order != "ASC" && order != "DESC" {
		order = "DESC"
	}
	return sortExpr, order
}
