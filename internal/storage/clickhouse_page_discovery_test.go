package storage

import "testing"

func TestClassifyPageDiscoveryPrecedence(t *testing.T) {
	tests := []struct {
		name         string
		evidence     PageDiscoveryEvidence
		wantSource   string
		availability string
	}{
		{
			name: "direct internal link wins",
			evidence: PageDiscoveryEvidence{
				DirectReferrersCount:   1,
				RedirectReferrersCount: 2,
				ReferrersCount:         3,
				IsInSitemap:            true,
				IsSeed:                 true,
			},
			wantSource:   "internal_link",
			availability: "derived",
		},
		{
			name: "redirect internal link",
			evidence: PageDiscoveryEvidence{
				RedirectReferrersCount: 1,
				ReferrersCount:         1,
			},
			wantSource:   "redirect_internal_link",
			availability: "derived",
		},
		{
			name:         "sitemap before seed",
			evidence:     PageDiscoveryEvidence{IsInSitemap: true, SitemapSourceURL: "https://example.com/sitemap.xml", IsSeed: true},
			wantSource:   "sitemap",
			availability: "derived",
		},
		{
			name:         "seed",
			evidence:     PageDiscoveryEvidence{IsSeed: true},
			wantSource:   "seed",
			availability: "derived",
		},
		{
			name:         "persisted found on",
			evidence:     PageDiscoveryEvidence{FoundOn: "https://example.com/source"},
			wantSource:   "found_on",
			availability: "derived",
		},
		{
			name:         "delta candidate",
			evidence:     PageDiscoveryEvidence{CandidateSources: []string{"problem_pages"}},
			wantSource:   "candidate",
			availability: "derived",
		},
		{
			name:         "unavailable",
			evidence:     PageDiscoveryEvidence{},
			wantSource:   "unknown",
			availability: "unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			classifyPageDiscovery(&tt.evidence)
			if tt.evidence.PrimarySource != tt.wantSource {
				t.Fatalf("PrimarySource = %q, want %q", tt.evidence.PrimarySource, tt.wantSource)
			}
			if tt.evidence.Availability != tt.availability {
				t.Fatalf("Availability = %q, want %q", tt.evidence.Availability, tt.availability)
			}
		})
	}
}

func TestPageCandidateSourcesDoesNotInventFoundOnEvidence(t *testing.T) {
	if got := pageCandidateSources("https://example.com/page", "found_on", nil); len(got) != 0 {
		t.Fatalf("pageCandidateSources(found_on) = %#v, want no inferred candidates", got)
	}
	planned := map[string][]string{"https://example.com/page": {"problem_pages"}}
	got := pageCandidateSources("https://example.com/page", "unknown", planned)
	if len(got) != 1 || got[0] != "problem_pages" {
		t.Fatalf("pageCandidateSources(planned) = %#v", got)
	}
}
