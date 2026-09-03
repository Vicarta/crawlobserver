package storage

import (
	"testing"
	"time"

	"github.com/SEObserver/crawlobserver/internal/config"
)

func TestDeltaSitemapStabilityDerivesOnlyExactCompletedEvidence(t *testing.T) {
	older := DeltaSitemapObservation{
		SessionID:  "older",
		ObservedAt: time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC),
		URLs: []DeltaSitemapObservationURL{
			{Loc: "https://EXAMPLE.test:443/a/../stable", LastMod: "2026-08-15T16:53:17Z"},
			{Loc: "https://example.test/changed-hash", LastMod: "2026-08-15"},
			{Loc: "https://example.test/invalid-lastmod", LastMod: "invalid"},
			{Loc: "https://example.test/missing-page", LastMod: "2026-08-15"},
		},
	}
	newer := DeltaSitemapObservation{
		SessionID:  "newer",
		ObservedAt: time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC),
		URLs: []DeltaSitemapObservationURL{
			{Loc: "https://example.test/stable", LastMod: "2026-08-15T16:53:17Z"},
			{Loc: "https://example.test/changed-hash", LastMod: "2026-08-15T00:00:00Z"},
			{Loc: "https://example.test/invalid-lastmod", LastMod: "invalid"},
			{Loc: "https://example.test/missing-page", LastMod: "2026-08-15"},
		},
	}
	olderPages := map[string]deltaSitemapPageEvidence{
		"https://example.test/stable":          {ContentHash: 7},
		"https://example.test/changed-hash":    {ContentHash: 8},
		"https://example.test/invalid-lastmod": {ContentHash: 9},
		"https://example.test/missing-page":    {ContentHash: 10},
	}
	newerPages := map[string]deltaSitemapPageEvidence{
		"https://example.test/stable":          {ContentHash: 7},
		"https://example.test/changed-hash":    {ContentHash: 11},
		"https://example.test/invalid-lastmod": {ContentHash: 9},
	}

	stability := deriveDeltaSitemapStability(older, newer, olderPages, newerPages)
	if stability == nil || len(stability.URLs) != 1 {
		t.Fatalf("stability = %#v, want one exact proof", stability)
	}
	proof := stability.URLs[0]
	if proof.NormalizedURL != "https://example.test/stable" || proof.LastMod != "2026-08-15T16:53:17Z" || proof.ContentHash != 7 {
		t.Fatalf("proof = %#v", proof)
	}
	if stability.ProofDigest == "" {
		t.Fatal("missing deterministic proof digest")
	}
	if replay := deriveDeltaSitemapStability(older, newer, olderPages, newerPages); replay == nil || replay.ProofDigest != stability.ProofDigest {
		t.Fatalf("proof digest changed on replay: first=%#v replay=%#v", stability, replay)
	}
}

func TestDeltaSitemapStabilityRejectsNoProofAndInvalidURL(t *testing.T) {
	older := DeltaSitemapObservation{SessionID: "same", URLs: []DeltaSitemapObservationURL{{Loc: "https://example.test/a", LastMod: "2026-08-15"}}}
	newer := DeltaSitemapObservation{SessionID: "same", URLs: []DeltaSitemapObservationURL{{Loc: "https://example.test/a", LastMod: "2026-08-15"}}}
	if got := deriveDeltaSitemapStability(older, newer, nil, nil); got != nil {
		t.Fatalf("same session proof = %#v, want nil", got)
	}
	if _, ok := normalizeDeltaSitemapStabilityURL("mailto:test@example.test"); ok {
		t.Fatal("non-http URL accepted as sitemap stability identity")
	}
}

func TestDeltaSitemapStabilityRejectsAmbiguousSitemapIdentityAndPrecisionDrift(t *testing.T) {
	older := DeltaSitemapObservation{SessionID: "older", URLs: []DeltaSitemapObservationURL{
		{Loc: "https://example.test/path", LastMod: "2026-08-15"},
		{Loc: "https://example.test:443/path", LastMod: "2026-08-15T00:00:00Z"},
	}}
	newer := DeltaSitemapObservation{SessionID: "newer", URLs: []DeltaSitemapObservationURL{
		{Loc: "https://example.test/path", LastMod: "2026-08-15"},
	}}
	pages := map[string]deltaSitemapPageEvidence{"https://example.test/path": {ContentHash: 7}}
	if got := deriveDeltaSitemapStability(older, newer, pages, pages); got != nil {
		t.Fatalf("ambiguous sitemap identity produced proof: %#v", got)
	}

	older.URLs = []DeltaSitemapObservationURL{{Loc: "https://example.test/path", LastMod: "2026-08-15"}}
	newer.URLs = []DeltaSitemapObservationURL{{Loc: "https://example.test/path", LastMod: "2026-08-15T00:00:00Z"}}
	if got := deriveDeltaSitemapStability(older, newer, pages, pages); got != nil {
		t.Fatalf("mixed lastmod precision produced proof: %#v", got)
	}
}

func TestDeltaSitemapStabilityRejectsUnknownSelectorRevision(t *testing.T) {
	if deltaSitemapSelectorSupportsStability(&config.DeltaSitemapSelection{SelectorRevision: "v4"}) {
		t.Fatal("unknown selector revision accepted for stability")
	}
	if !deltaSitemapSelectorSupportsStability(nil) ||
		!deltaSitemapSelectorSupportsStability(&config.DeltaSitemapSelection{SelectorRevision: "v1"}) ||
		!deltaSitemapSelectorSupportsStability(&config.DeltaSitemapSelection{SelectorRevision: "v2"}) ||
		!deltaSitemapSelectorSupportsStability(&config.DeltaSitemapSelection{SelectorRevision: "v3"}) {
		t.Fatal("supported legacy/current selector revision rejected")
	}
}

func TestDeltaSitemapFreshObservationMetadataFailsClosed(t *testing.T) {
	valid := &config.DeltaPlanConfig{SitemapRefresh: &config.DeltaSitemapRefresh{
		Mode: "fresh", FetchedAt: time.Now().UTC(), FreshURLCount: 1,
	}}
	if !deltaSitemapFreshObservationMetadataValid(valid) {
		t.Fatal("valid fresh metadata rejected")
	}
	valid.SitemapRefresh.FreshURLCount = 0
	valid.SitemapRefresh.RawURLRowCount = 0
	if !deltaSitemapFreshObservationMetadataValid(valid) {
		t.Fatal("complete empty sitemap metadata rejected")
	}

	cases := []*config.DeltaPlanConfig{
		nil,
		{},
		{SitemapRefresh: &config.DeltaSitemapRefresh{Mode: "fresh", FreshURLCount: 1}},
		{SitemapRefresh: &config.DeltaSitemapRefresh{Mode: "snapshot_fallback", FetchedAt: time.Now().UTC(), FreshURLCount: 1}},
		{SitemapRefresh: &config.DeltaSitemapRefresh{Mode: "fresh", FetchedAt: time.Now().UTC(), FreshURLCount: -1}},
		{SitemapRefresh: &config.DeltaSitemapRefresh{Mode: "fresh", FetchedAt: time.Now().UTC(), FreshURLCount: 1}, SitemapSelection: &config.DeltaSitemapSelection{SelectorRevision: "v4"}},
	}
	for i, plan := range cases {
		if deltaSitemapFreshObservationMetadataValid(plan) {
			t.Fatalf("invalid metadata case %d accepted: %#v", i, plan)
		}
	}
}
