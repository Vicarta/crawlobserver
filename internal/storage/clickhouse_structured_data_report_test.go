//go:build integration

package storage

import (
	"context"
	"testing"
	"time"

	"github.com/SEObserver/crawlobserver/internal/schema"
	"github.com/google/uuid"
)

func TestStructuredDataReportInsertFiltersAndDeltaCarryForward(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	baselineID, snapshotID, deltaID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, id := range []string{baselineID, snapshotID, deltaID} {
		cleanupDeltaTestSession(t, s, id)
	}
	t.Cleanup(func() {
		for _, id := range []string{baselineID, snapshotID, deltaID} {
			cleanupDeltaTestSession(t, s, id)
		}
	})

	now := time.Now().UTC()
	for _, session := range []*CrawlSession{
		{ID: baselineID, StartedAt: now.Add(-time.Hour), Status: "completed", Label: CurrentSnapshotLabel},
		{ID: snapshotID, StartedAt: now, Status: "completed", Label: CurrentSnapshotLabel},
		{ID: deltaID, StartedAt: now.Add(time.Minute), Status: "completed", Label: "Daily Delta Crawl"},
	} {
		if err := s.InsertSession(ctx, session); err != nil {
			t.Fatal(err)
		}
	}

	unchangedURL := "https://example.test/unchanged"
	freshURL := "https://example.test/fresh"
	historicalURL := "https://example.test/historical"
	noMarkupURL := "https://example.test/no-markup"
	baselineClean := schema.ObserveTechnicalStructuredData(
		[]string{`{"@type":["Product","Service"]}`}, []string{"json-ld"}, nil, "static",
	)
	baselineIssue := schema.ObserveTechnicalStructuredData(
		[]string{`{"@type":}`}, []string{"json-ld"}, nil, "static",
	)
	noMarkup := schema.ObserveTechnicalStructuredData(nil, nil, nil, "static")
	if err := s.InsertPages(ctx, []PageRow{
		{CrawlSessionID: baselineID, URL: unchangedURL, FinalURL: unchangedURL, StatusCode: 200, ContentType: "text/html", StructuredDataFields: StructuredDataFields{StructuredDataReport: baselineClean}, CrawledAt: now.Add(-time.Hour)},
		{CrawlSessionID: baselineID, URL: freshURL, FinalURL: freshURL, StatusCode: 200, ContentType: "text/html", StructuredDataFields: StructuredDataFields{StructuredDataReport: baselineIssue}, CrawledAt: now.Add(-time.Hour)},
		{CrawlSessionID: baselineID, URL: noMarkupURL, FinalURL: noMarkupURL, StatusCode: 200, ContentType: "text/html", SchemaTypes: []string{"LegacyFalseType"}, StructuredDataFields: StructuredDataFields{StructuredDataReport: noMarkup}, CrawledAt: now.Add(-time.Hour)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.conn.Exec(ctx, `
		INSERT INTO crawlobserver.pages (crawl_session_id, url, final_url, status_code, content_type, schema_types, crawled_at)
		VALUES (?, ?, ?, 200, 'text/html', ['@context', 'HistoryOnly'], ?)`, baselineID, historicalURL, historicalURL, now.Add(-time.Hour)); err != nil {
		t.Fatalf("inserting historical page row: %v", err)
	}
	if err := s.copySnapshotPages(ctx, baselineID, snapshotID); err != nil {
		t.Fatalf("copy snapshot pages: %v", err)
	}

	renderedUpdate := schema.ObserveTechnicalStructuredData(nil, []string{"microdata"}, []string{"https://schema.org/Service"}, "static")
	if err := s.InsertPages(ctx, []PageRow{
		{CrawlSessionID: deltaID, URL: unchangedURL, FinalURL: unchangedURL, StatusCode: 304, ContentType: "text/html", CrawledAt: now.Add(time.Minute)},
		{CrawlSessionID: deltaID, URL: freshURL, FinalURL: freshURL, StatusCode: 200, ContentType: "text/html", StructuredDataFields: StructuredDataFields{StructuredDataReport: renderedUpdate}, CrawledAt: now.Add(time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.overlayDeltaPages(ctx, snapshotID, deltaID); err != nil {
		t.Fatalf("overlay Delta pages: %v", err)
	}

	unchanged, err := s.GetPage(ctx, snapshotID, unchangedURL)
	if err != nil || unchanged.StructuredDataStatus != schema.StructuredDataClean || len(unchanged.StructuredDataTypes) != 2 || unchanged.StructuredDataTypes[0] != "Product" || unchanged.StructuredDataTypes[1] != "Service" {
		t.Fatalf("304 did not preserve evidence: page=%#v err=%v", unchanged, err)
	}
	fresh, err := s.GetPage(ctx, snapshotID, freshURL)
	if err != nil || fresh.StructuredDataStatus != schema.StructuredDataClean || fresh.StructuredDataReport.Coverage != "microdata_detection_only" {
		t.Fatalf("fresh fetch did not replace evidence: page=%#v err=%v", fresh, err)
	}
	emptyObserved, err := s.GetPage(ctx, snapshotID, noMarkupURL)
	if err != nil || len(emptyObserved.StructuredDataTypes) != 0 || len(emptyObserved.SchemaTypes) != 1 {
		t.Fatalf("observed empty types inherited legacy types: page=%#v err=%v", emptyObserved, err)
	}
	listPages, err := s.ListPages(ctx, snapshotID, 100, 0, nil, nil)
	if err != nil {
		t.Fatalf("list pages: %v", err)
	}
	listedEmptyTypes := false
	listedHistoricalTypes := false
	for _, page := range listPages {
		if page.URL == noMarkupURL {
			listedEmptyTypes = true
			if len(page.StructuredDataTypes) != 0 {
				t.Fatalf("Pages row inherited legacy types: %#v", page.StructuredDataTypes)
			}
		}
		if page.URL == historicalURL {
			listedHistoricalTypes = true
			if len(page.StructuredDataTypes) != 0 {
				t.Fatalf("historical heuristics entered new Pages column: %#v", page.StructuredDataTypes)
			}
		}
	}
	if !listedEmptyTypes {
		t.Fatal("Pages list omitted the observed empty-types row")
	}
	if !listedHistoricalTypes {
		t.Fatal("Pages list omitted the historical row")
	}

	report, err := s.StructuredDataReport(ctx, snapshotID, schema.StructuredDataReportOptions{Limit: 50})
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if report.Summary.Total != 4 || report.Summary.Clean != 2 || report.Summary.NotChecked != 1 || report.Summary.NoMarkup != 1 || len(report.Pages) != 4 {
		t.Fatalf("summary/rows = %#v / %d", report.Summary, len(report.Pages))
	}

	filtered, err := s.StructuredDataReport(ctx, snapshotID, schema.StructuredDataReportOptions{
		Limit: 10, Status: schema.StructuredDataNotChecked, Type: "HistoryOnly",
	})
	if err != nil || filtered.Total != 0 || len(filtered.Pages) != 0 {
		t.Fatalf("historical heuristics entered type filter = %#v err=%v", filtered, err)
	}
	historical, err := s.GetPage(ctx, snapshotID, historicalURL)
	if err != nil || len(historical.SchemaTypes) != 2 || historical.SchemaTypes[0] != "@context" || historical.SchemaTypes[1] != "HistoryOnly" || len(historical.StructuredDataTypes) != 0 {
		t.Fatalf("historical URL detail types = %#v err=%v", historical, err)
	}
	historicalDetail, err := s.StructuredDataReport(ctx, snapshotID, schema.StructuredDataReportOptions{URL: historicalURL})
	if err != nil || len(historicalDetail.Pages) != 1 || len(historicalDetail.Pages[0].Types) != 0 || historicalDetail.Pages[0].Status != schema.StructuredDataNotChecked {
		t.Fatalf("historical report detail = %#v err=%v", historicalDetail, err)
	}
	filtered, err = s.StructuredDataReport(ctx, snapshotID, schema.StructuredDataReportOptions{Limit: 10, Type: "LegacyFalseType"})
	if err != nil || filtered.Total != 0 {
		t.Fatalf("observed legacy type filter = %#v err=%v", filtered, err)
	}

	filtered, err = s.StructuredDataReport(ctx, snapshotID, schema.StructuredDataReportOptions{Limit: 10, Format: "microdata"})
	if err != nil || filtered.Total != 1 || filtered.Pages[0].URL != freshURL || filtered.Pages[0].Coverage != "microdata_detection_only" || filtered.Pages[0].JSONLDEntityCount != 0 {
		t.Fatalf("format filter = %#v err=%v", filtered, err)
	}

	detail, err := s.StructuredDataReport(ctx, snapshotID, schema.StructuredDataReportOptions{URL: unchangedURL})
	if err != nil || len(detail.Pages) != 1 || len(detail.Pages[0].Entities) != 2 || detail.Pages[0].Entities[0].Type != "Product" || detail.Pages[0].JSONLDEntityCount != 1 {
		t.Fatalf("URL detail = %#v err=%v", detail, err)
	}
	emptyDetail, err := s.StructuredDataReport(ctx, snapshotID, schema.StructuredDataReportOptions{URL: noMarkupURL})
	if err != nil || len(emptyDetail.Pages) != 1 || len(emptyDetail.Pages[0].Types) != 0 {
		t.Fatalf("observed empty report types = %#v err=%v", emptyDetail, err)
	}
}
