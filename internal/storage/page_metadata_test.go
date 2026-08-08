package storage

import "testing"

func TestSelectSitemapEvidencePrefersExactRawLoc(t *testing.T) {
	tests := []struct {
		name                            string
		exactSourceURL, exactRawLoc     string
		decodedSourceURL, decodedRawLoc string
		wantSourceURL, wantRawLoc       string
	}{
		{
			name:             "exact match takes precedence",
			exactSourceURL:   "https://example.com/exact.xml",
			exactRawLoc:      "https://example.com/a%20exact",
			decodedSourceURL: "https://example.com/decoded.xml",
			decodedRawLoc:    "https://example.com/a exact",
			wantSourceURL:    "https://example.com/exact.xml",
			wantRawLoc:       "https://example.com/a%20exact",
		},
		{
			name:             "decoded match preserves literal space",
			decodedSourceURL: "https://example.com/raw.xml",
			decodedRawLoc:    "https://example.com/b raw",
			wantSourceURL:    "https://example.com/raw.xml",
			wantRawLoc:       "https://example.com/b raw",
		},
		{
			name: "no sitemap match is empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSourceURL, gotRawLoc := selectSitemapEvidence(
				tt.exactSourceURL,
				tt.exactRawLoc,
				tt.decodedSourceURL,
				tt.decodedRawLoc,
			)
			if gotSourceURL != tt.wantSourceURL || gotRawLoc != tt.wantRawLoc {
				t.Fatalf("selectSitemapEvidence() = (%q, %q), want (%q, %q)", gotSourceURL, gotRawLoc, tt.wantSourceURL, tt.wantRawLoc)
			}
		})
	}
}

func TestPageProblemOriginClassifiesOrphan404(t *testing.T) {
	p := PageRow{
		StatusCode:      404,
		InternalLinksIn: 0,
		IsInSitemap:     false,
	}
	if got := pageProblemOrigin(p); got != "orphan_problem_candidate" {
		t.Fatalf("pageProblemOrigin(orphan 404) = %q, want orphan_problem_candidate", got)
	}
}

func TestPageProblemOriginKeepsLinkedAndSitemap404Separate(t *testing.T) {
	linked := PageRow{StatusCode: 404, InternalLinksIn: 2, DiscoverySource: "internal_link"}
	if got := pageProblemOrigin(linked); got != "internal_link" {
		t.Fatalf("pageProblemOrigin(linked 404) = %q, want internal_link", got)
	}
	redirectLinked := PageRow{StatusCode: 404, InternalLinksIn: 1, DiscoverySource: "redirect_internal_link"}
	if got := pageProblemOrigin(redirectLinked); got != "redirect_internal_link" {
		t.Fatalf("pageProblemOrigin(redirect-linked 404) = %q, want redirect_internal_link", got)
	}

	sitemap := PageRow{StatusCode: 404, IsInSitemap: true}
	if got := pageProblemOrigin(sitemap); got != "sitemap" {
		t.Fatalf("pageProblemOrigin(sitemap 404) = %q, want sitemap", got)
	}
	candidate := PageRow{StatusCode: 404, CandidateSources: []string{"problem_pages"}}
	if got := pageProblemOrigin(candidate); got != "candidate" {
		t.Fatalf("pageProblemOrigin(candidate 404) = %q, want candidate", got)
	}

	ok := PageRow{StatusCode: 200}
	if got := pageProblemOrigin(ok); got != "" {
		t.Fatalf("pageProblemOrigin(200) = %q, want empty", got)
	}
}
