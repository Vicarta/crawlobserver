package config

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/spf13/viper"
)

func TestDeltaSitemapRefreshJSONContract(t *testing.T) {
	plan := DeltaPlanConfig{
		SitemapRefresh: &DeltaSitemapRefresh{
			Mode:                "fresh",
			FetchedAt:           time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
			DeclaredSitemapURLs: []string{"https://example.com/sitemap.xml"},
			FetchedSitemapURLs:  []string{"https://example.com/sitemap.xml"},
			FreshURLCount:       4,
			SnapshotURLCount:    5,
			AddedCount:          1,
			RemovedCount:        2,
			InvalidEntryCount:   1,
			Warnings:            []string{"one invalid loc"},
			RawEvidence: []DeltaSitemapEvidenceRef{{
				SitemapURL: "https://example.com/sitemap.xml",
				RawLoc:     "https://example.com/path with space",
			}},
		},
	}

	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var decoded DeltaPlanConfig
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if !reflect.DeepEqual(decoded.SitemapRefresh, plan.SitemapRefresh) {
		t.Fatalf("SitemapRefresh round trip = %#v, want %#v", decoded.SitemapRefresh, plan.SitemapRefresh)
	}

	legacy, err := json.Marshal(DeltaPlanConfig{})
	if err != nil {
		t.Fatalf("Marshal legacy plan error = %v", err)
	}
	if string(legacy) == "" || decoded.SitemapRefresh == nil {
		t.Fatal("test setup did not produce the expected JSON states")
	}
	var legacyDecoded DeltaPlanConfig
	if err := json.Unmarshal(legacy, &legacyDecoded); err != nil {
		t.Fatalf("Unmarshal legacy plan error = %v", err)
	}
	if legacyDecoded.SitemapRefresh != nil {
		t.Fatalf("legacy plan SitemapRefresh = %#v, want nil", legacyDecoded.SitemapRefresh)
	}
}

func TestCrawlerConfigSitemapURLsMapstructureAndJSON(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set("crawler.sitemap_urls", []string{
		"https://example.com/sitemap.xml",
		"https://example.com/news.xml",
	})

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	want := []string{"https://example.com/sitemap.xml", "https://example.com/news.xml"}
	if !reflect.DeepEqual(cfg.Crawler.SitemapURLs, want) {
		t.Fatalf("Crawler.SitemapURLs = %v, want %v", cfg.Crawler.SitemapURLs, want)
	}

	encoded, err := json.Marshal(CrawlerConfig{SitemapURLs: want})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !containsJSONField(encoded, "sitemap_urls") {
		t.Fatalf("CrawlerConfig JSON = %s, missing sitemap_urls", encoded)
	}
}

func containsJSONField(data []byte, field string) bool {
	var value map[string]json.RawMessage
	return json.Unmarshal(data, &value) == nil && value[field] != nil
}
