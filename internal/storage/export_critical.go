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

	if table == "gsc_analytics" {
		if err := s.exportGSCAnalyticsRows(ctx, enc); err != nil {
			os.Remove(path)
			return err
		}
		return nil
	}

	// Stream all rows without LIMIT/OFFSET (deterministic, no missed/duplicated rows).
	// The driver handles streaming natively — rows are fetched in blocks.
	if err := s.exportCriticalTableQueryRows(ctx, enc, fmt.Sprintf("SELECT * FROM %s", table)); err != nil {
		os.Remove(path)
		return err
	}

	return nil
}

func (s *Store) exportCriticalTableQueryRows(ctx context.Context, enc *json.Encoder, query string, args ...interface{}) error {
	rows, err := s.conn.Query(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("querying: %w", err)
	}
	defer rows.Close()

	colNames := rows.Columns()

	for rows.Next() {
		// Use interface{} for all columns — the driver handles type
		// conversion including Nullable, LowCardinality, Array, Tuple, etc.
		values := make([]interface{}, len(colNames))
		for i := range values {
			values[i] = new(interface{})
		}
		if err := rows.Scan(values...); err != nil {
			return fmt.Errorf("scanning row: %w", err)
		}

		row := make(map[string]interface{}, len(colNames))
		for i, name := range colNames {
			// Dereference the *interface{}
			row[name] = *(values[i].(*interface{}))
		}
		if err := enc.Encode(row); err != nil {
			return fmt.Errorf("encoding row: %w", err)
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating rows: %w", err)
	}

	return nil
}

func (s *Store) exportGSCAnalyticsRows(ctx context.Context, enc *json.Encoder) error {
	chunks, err := s.gscAnalyticsExportChunks(ctx)
	if err != nil {
		return err
	}
	for _, chunk := range chunks {
		if err := s.exportGSCAnalyticsChunk(ctx, enc, chunk); err != nil {
			return err
		}
	}
	return nil
}

type gscAnalyticsExportChunk struct {
	ProjectID string
	StartDate time.Time
	EndDate   time.Time
}

func (s *Store) gscAnalyticsExportChunks(ctx context.Context) ([]gscAnalyticsExportChunk, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT project_id, min(date), max(date)
		FROM gsc_analytics
		GROUP BY project_id
		ORDER BY project_id`)
	if err != nil {
		return nil, fmt.Errorf("querying gsc_analytics export ranges: %w", err)
	}
	defer rows.Close()

	var chunks []gscAnalyticsExportChunk
	for rows.Next() {
		var projectID string
		var minDate, maxDate time.Time
		if err := rows.Scan(&projectID, &minDate, &maxDate); err != nil {
			return nil, fmt.Errorf("scanning gsc_analytics export range: %w", err)
		}
		chunks = append(chunks, gscAnalyticsDailyChunks(projectID, minDate, maxDate)...)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return chunks, nil
}

func gscAnalyticsDailyChunks(projectID string, minDate, maxDate time.Time) []gscAnalyticsExportChunk {
	start := dateOnly(minDate)
	last := dateOnly(maxDate)
	if projectID == "" || start.IsZero() || last.IsZero() || last.Before(start) {
		return nil
	}
	chunks := make([]gscAnalyticsExportChunk, 0, int(last.Sub(start).Hours()/24)+1)
	for day := start; !day.After(last); day = day.AddDate(0, 0, 1) {
		chunks = append(chunks, gscAnalyticsExportChunk{
			ProjectID: projectID,
			StartDate: day,
			EndDate:   day.AddDate(0, 0, 1),
		})
	}
	return chunks
}

func dateOnly(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func (s *Store) exportGSCAnalyticsChunk(ctx context.Context, enc *json.Encoder, chunk gscAnalyticsExportChunk) error {
	rows, err := s.conn.Query(ctx, `
		SELECT project_id, date, query, page, country, device,
			clicks, impressions, ctr, position, fetched_at
		FROM gsc_analytics FINAL
		WHERE project_id = ? AND date >= ? AND date < ?
		ORDER BY date, query, page, country, device`,
		chunk.ProjectID, chunk.StartDate, chunk.EndDate)
	if err != nil {
		return fmt.Errorf("querying gsc_analytics chunk project=%s date=%s: %w", chunk.ProjectID, chunk.StartDate.Format("2006-01-02"), err)
	}
	defer rows.Close()

	for rows.Next() {
		var projectID, query, page, country, device string
		var date, fetchedAt time.Time
		var clicks, impressions uint32
		var ctr, position float32
		if err := rows.Scan(&projectID, &date, &query, &page, &country, &device, &clicks, &impressions, &ctr, &position, &fetchedAt); err != nil {
			return fmt.Errorf("scanning gsc_analytics chunk project=%s date=%s: %w", chunk.ProjectID, chunk.StartDate.Format("2006-01-02"), err)
		}
		if err := enc.Encode(map[string]interface{}{
			"project_id": projectID, "date": date, "query": query, "page": page,
			"country": country, "device": device, "clicks": clicks,
			"impressions": impressions, "ctr": ctr, "position": position, "fetched_at": fetchedAt,
		}); err != nil {
			return fmt.Errorf("encoding gsc_analytics chunk project=%s date=%s: %w", chunk.ProjectID, chunk.StartDate.Format("2006-01-02"), err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("reading gsc_analytics chunk project=%s date=%s: %w", chunk.ProjectID, chunk.StartDate.Format("2006-01-02"), err)
	}
	return nil
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
