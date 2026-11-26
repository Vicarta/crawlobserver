package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SEObserver/crawlobserver/internal/schema"
)

func (s *Store) StructuredDataReport(ctx context.Context, sessionID string, options schema.StructuredDataReportOptions) (*schema.StructuredDataReportResponse, error) {
	limit := options.Limit
	if limit < 1 {
		limit = 100
	}
	if limit > 100 {
		limit = 100
	}
	offset := options.Offset
	if offset < 0 {
		offset = 0
	}
	if options.URL != "" {
		limit = 1
		offset = 0
	}

	base := `crawl_session_id = ? AND status_code != 304 AND positionCaseInsensitiveUTF8(content_type, 'html') > 0 AND ` + notRedirectedFilter
	var summary schema.StructuredDataReportSummary
	if err := s.conn.QueryRow(ctx, `
		SELECT count(),
			countIf(structured_data_status = ?),
			countIf(structured_data_status = ?),
			countIf(structured_data_status = ?),
			countIf(structured_data_status = ?)
		FROM crawlobserver.pages FINAL
		WHERE `+base,
		schema.StructuredDataNotChecked,
		schema.StructuredDataNoMarkup,
		schema.StructuredDataIssues,
		schema.StructuredDataClean,
		sessionID,
	).Scan(&summary.Total, &summary.NotChecked, &summary.NoMarkup, &summary.Issues, &summary.Clean); err != nil {
		return nil, fmt.Errorf("querying structured-data summary: %w", err)
	}

	where := base
	args := []interface{}{sessionID}
	if options.URL != "" {
		where += " AND url = ?"
		args = append(args, options.URL)
	}
	if options.Status != "" {
		switch options.Status {
		case schema.StructuredDataNotChecked, schema.StructuredDataNoMarkup, schema.StructuredDataIssues, schema.StructuredDataClean:
		default:
			return nil, fmt.Errorf("unsupported structured-data status %q", options.Status)
		}
		where += " AND structured_data_status = ?"
		args = append(args, options.Status)
	}
	if options.Format != "" {
		format := strings.ToLower(strings.TrimSpace(options.Format))
		if format != "json-ld" && format != "microdata" {
			return nil, fmt.Errorf("unsupported structured-data format %q", options.Format)
		}
		where += " AND has(structured_data_formats, ?)"
		args = append(args, format)
	}
	if options.Type != "" {
		typeName := strings.TrimSpace(options.Type)
		if len(typeName) > 256 {
			return nil, fmt.Errorf("structured-data type filter is too long")
		}
		where += " AND arrayExists(value -> lowerUTF8(value) = lowerUTF8(?), p.structured_data_types)"
		args = append(args, typeName)
	}

	var total uint64
	if err := s.conn.QueryRow(ctx, `SELECT count() FROM crawlobserver.pages AS p FINAL WHERE `+where, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("counting structured-data report rows: %w", err)
	}

	response := &schema.StructuredDataReportResponse{
		Summary: summary, Pages: []schema.StructuredDataReportPage{}, Total: total, Limit: limit, Offset: offset,
	}
	args = append(args, limit, offset)
	rows, err := s.conn.Query(ctx, `
		SELECT url, structured_data_status, structured_data_formats, structured_data_types,
			structured_data_report
		FROM crawlobserver.pages AS p FINAL
		WHERE `+where+`
		ORDER BY url
		LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("querying structured-data report rows: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var page schema.StructuredDataReportPage
		var formats, types []string
		var reportJSON string
		if err := rows.Scan(&page.URL, &page.Status, &formats, &types, &reportJSON); err != nil {
			return nil, fmt.Errorf("scanning structured-data report row: %w", err)
		}
		var report schema.TechnicalStructuredDataReport
		if err := json.Unmarshal([]byte(reportJSON), &report); err != nil {
			return nil, fmt.Errorf("decoding structured-data report for %s: %w", page.URL, err)
		}
		if page.Status == "" {
			page.Status = schema.StructuredDataNotChecked
		}
		page.Sources = uniqueReportStrings(reportSourceNames(report.Sources))
		page.Formats = uniqueReportStrings(formats)
		page.Types = uniqueReportStrings(types)
		page.Coverage = report.Coverage
		if page.Coverage == "" {
			page.Coverage = "not_checked"
		}
		page.JSONLDEntityCount = structuredDataJSONLDEntityCount(report.Entities)
		page.DiagnosticCount = uint64(len(report.Diagnostics))
		page.Truncated = report.Truncated
		page.DiagnosticsTruncated = options.URL == "" && len(report.Diagnostics) > 10
		page.Diagnostics = report.Diagnostics
		if len(page.Diagnostics) > 10 && options.URL == "" {
			page.Diagnostics = page.Diagnostics[:10]
		}
		if page.Diagnostics == nil {
			page.Diagnostics = []schema.TechnicalStructuredDataDiagnostic{}
		}
		if options.URL != "" {
			page.Entities = report.Entities
			if page.Entities == nil {
				page.Entities = []schema.TechnicalStructuredDataEntity{}
			}
		}
		response.Pages = append(response.Pages, page)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating structured-data report rows: %w", err)
	}
	return response, nil
}

func structuredDataJSONLDEntityCount(entities []schema.TechnicalStructuredDataEntity) uint64 {
	type entityKey struct {
		source string
		index  int
	}
	seen := make(map[entityKey]struct{}, len(entities))
	for _, entity := range entities {
		if entity.Format != "json-ld" {
			continue
		}
		seen[entityKey{source: entity.Source, index: entity.EntityIndex}] = struct{}{}
	}
	return uint64(len(seen))
}

func reportSourceNames(sources []schema.TechnicalStructuredDataSource) []string {
	names := make([]string, 0, len(sources))
	for _, source := range sources {
		names = append(names, source.Name)
	}
	return names
}

func uniqueReportStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
