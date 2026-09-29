package storage

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/SEObserver/crawlobserver/internal/config"
)

func TestNormalizeEffectiveOrigin(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
		valid bool
	}{
		{name: "redirect target", input: "HTTPS://WWW.Example.test/path", want: "https://www.example.test", valid: true},
		{name: "default port", input: "http://example.test:80/path", want: "http://example.test", valid: true},
		{name: "non default port", input: "https://example.test:8443/path", want: "https://example.test:8443", valid: true},
		{name: "ipv6 default port", input: "https://[2001:db8::1]:443/path", want: "https://[2001:db8::1]", valid: true},
		{name: "ipv6 non default port", input: "http://[2001:db8::1]:8080/path", want: "http://[2001:db8::1]:8080", valid: true},
		{name: "invalid scheme", input: "ftp://example.test/path", valid: false},
		{name: "missing host", input: "https:///path", valid: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, valid := normalizeEffectiveOrigin(tt.input)
			if valid != tt.valid || got != tt.want {
				t.Fatalf("normalizeEffectiveOrigin(%q) = %q, %v; want %q, %v", tt.input, got, valid, tt.want, tt.valid)
			}
		})
	}
}

func TestLaunchedURLsForOriginUsesDeltaPlanOnly(t *testing.T) {
	deltaConfig, err := json.Marshal(config.Config{Crawler: config.CrawlerConfig{
		DeltaPlan: &config.DeltaPlanConfig{LaunchedURLs: []string{"https://delta.example/changed"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, isDelta := launchedURLsForOrigin(CrawlSession{
		Label:    "Daily Delta Crawl",
		SeedURLs: []string{"https://raw-seed.example/"},
		Config:   string(deltaConfig),
	})
	if !isDelta || len(got) != 1 || got[0] != "https://delta.example/changed" {
		t.Fatalf("delta launched URLs = %#v, isDelta=%v", got, isDelta)
	}

	got, isDelta = launchedURLsForOrigin(CrawlSession{
		Label:    "Daily Delta Crawl",
		SeedURLs: []string{"https://raw-seed.example/"},
		Config:   "{}",
	})
	if !isDelta || got != nil {
		t.Fatalf("legacy delta launched URLs = %#v, isDelta=%v; want nil, true", got, isDelta)
	}

	got, isDelta = launchedURLsForOrigin(CrawlSession{
		Label:    "Full Crawl",
		SeedURLs: []string{"https://seed.example/"},
		Config:   "{}",
	})
	if isDelta || len(got) != 1 || got[0] != "https://seed.example/" {
		t.Fatalf("full launched URLs = %#v, isDelta=%v", got, isDelta)
	}
}

func TestNewPageErrorsComparedWithLatestPriorURLObservation(t *testing.T) {
	current := []PageErrorObservation{
		{URL: "https://example.test/persistent", StatusCode: 404},
		{URL: "https://example.test/changed", StatusCode: 500},
		{URL: "https://example.test/fetch", StatusCode: 503, FetchError: true},
		{URL: "https://example.test/reappeared", StatusCode: 404},
		{URL: "https://example.test/query?key=current", StatusCode: 404},
	}
	previous := map[string]PageErrorObservation{
		"https://example.test/persistent": {URL: "https://example.test/persistent", StatusCode: 404},
		"https://example.test/changed":    {URL: "https://example.test/changed", StatusCode: 404},
		"https://example.test/fetch":      {URL: "https://example.test/fetch", StatusCode: 503},
		// The latest earlier observation was healthy, so this is a reappearance
		// even if an older crawl had previously seen the same 404.
		"https://example.test/reappeared": {URL: "https://example.test/reappeared", StatusCode: 200},
		// Query variants remain distinct observation keys.
		"https://example.test/query?key=older": {URL: "https://example.test/query?key=older", StatusCode: 404},
		"https://example.test/resolved":        {URL: "https://example.test/resolved", StatusCode: 404},
	}

	got := newPageErrorsComparedWithPrior(current, previous)
	want := []string{
		"https://example.test/changed",
		"https://example.test/fetch",
		"https://example.test/query?key=current",
		"https://example.test/reappeared",
	}
	if len(got) != len(want) {
		t.Fatalf("new errors = %#v; want URLs %#v", got, want)
	}
	for i, page := range got {
		if page.URL != want[i] {
			t.Fatalf("new error URL[%d] = %q; want %q", i, page.URL, want[i])
		}
	}
	for _, page := range got {
		if page.URL == "https://example.test/persistent" || page.URL == "https://example.test/resolved" {
			t.Fatalf("unchanged/resolved page error was included: %q", page.URL)
		}
	}
}

func TestResolveEffectiveOriginRequiresCompleteConsistentProof(t *testing.T) {
	launched := map[string]struct{}{"http://example.test/a": {}, "http://example.test/b": {}}
	tests := []struct {
		name   string
		proved map[string]map[string]struct{}
		want   EffectiveOrigin
	}{
		{
			name: "all launched URLs prove one redirected origin",
			proved: map[string]map[string]struct{}{
				"http://example.test/a": {"https://www.example.test": {}},
				"http://example.test/b": {"https://www.example.test": {}},
			},
			want: EffectiveOrigin{Origin: "https://www.example.test", State: EffectiveOriginProven},
		},
		{
			name: "partial proof fails closed",
			proved: map[string]map[string]struct{}{
				"http://example.test/a": {"https://www.example.test": {}},
			},
			want: EffectiveOrigin{State: EffectiveOriginUnavailable},
		},
		{
			name: "conflicting proof is ambiguous",
			proved: map[string]map[string]struct{}{
				"http://example.test/a": {"https://www.example.test": {}},
				"http://example.test/b": {"https://other.example.test": {}},
			},
			want: EffectiveOrigin{State: EffectiveOriginAmbiguous},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveEffectiveOrigin(launched, tt.proved); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("resolveEffectiveOrigin() = %#v; want %#v", got, tt.want)
			}
		})
	}
}

func TestResolveDeltaEffectiveOriginProvesPrimaryAndListsRelatedHosts(t *testing.T) {
	launched := map[string]struct{}{
		"https://www.example.com/":      {},
		"https://www.example.com/about": {},
		"https://de.example.com/page":   {},
		"https://es.example.com/page":   {},
		"https://fr.example.com/page":   {},
	}
	proved := map[string]map[string]struct{}{
		"https://www.example.com/":      {"https://www.example.com": {}},
		"https://www.example.com/about": {"https://www.example.com": {}},
		"https://de.example.com/page":   {"https://de.example.com": {}},
		"https://es.example.com/page":   {"https://es.example.com": {}},
		"https://fr.example.com/page":   {"https://fr.example.com": {}},
	}
	got := resolveDeltaEffectiveOrigin(launched, proved, nil, "https://www.example.com/start")
	want := EffectiveOrigin{
		Origin:       "https://www.example.com",
		OtherOrigins: []string{"https://de.example.com", "https://es.example.com", "https://fr.example.com"},
		State:        EffectiveOriginProven,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolveDeltaEffectiveOrigin() = %#v; want %#v", got, want)
	}
}

func TestResolveDeltaEffectiveOriginPreservesUnprovenAndAmbiguousStates(t *testing.T) {
	baseLaunched := map[string]struct{}{
		"https://www.example.com/": {},
		"https://de.example.com/":  {},
	}
	for _, tt := range []struct {
		name      string
		launched  map[string]struct{}
		proved    map[string]map[string]struct{}
		redirects map[string]map[string]bool
		want      EffectiveOrigin
	}{
		{
			name:     "partial evidence",
			launched: baseLaunched,
			proved:   map[string]map[string]struct{}{"https://www.example.com/": {"https://www.example.com": {}}},
			want:     EffectiveOrigin{State: EffectiveOriginUnavailable},
		},
		{
			name:     "unrelated host",
			launched: baseLaunched,
			proved: map[string]map[string]struct{}{
				"https://www.example.com/": {"https://www.example.com": {}},
				"https://de.example.com/":  {"https://elsewhere.test": {}},
			},
			want: EffectiveOrigin{State: EffectiveOriginAmbiguous},
		},
		{
			name:     "conflicting primary proof",
			launched: map[string]struct{}{"https://www.example.com/": {}, "https://www.example.com/about": {}},
			proved: map[string]map[string]struct{}{
				"https://www.example.com/":      {"https://www.example.com": {}},
				"https://www.example.com/about": {"https://shop.example.com": {}},
			},
			redirects: map[string]map[string]bool{"https://www.example.com/about": {"https://shop.example.com": true}},
			want:      EffectiveOrigin{State: EffectiveOriginAmbiguous},
		},
		{
			name:     "missing evidence does not hide conflicting primary proof",
			launched: map[string]struct{}{"https://www.example.com/": {}, "https://www.example.com/about": {}, "https://de.example.com/": {}},
			proved: map[string]map[string]struct{}{
				"https://www.example.com/":      {"https://www.example.com": {}},
				"https://www.example.com/about": {"https://shop.example.com": {}},
			},
			redirects: map[string]map[string]bool{"https://www.example.com/about": {"https://shop.example.com": true}},
			want:      EffectiveOrigin{State: EffectiveOriginAmbiguous},
		},
		{
			name:     "other host without primary seed evidence",
			launched: map[string]struct{}{"https://de.example.com/": {}},
			proved:   map[string]map[string]struct{}{"https://de.example.com/": {"https://de.example.com": {}}},
			want:     EffectiveOrigin{State: EffectiveOriginUnavailable},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveDeltaEffectiveOrigin(tt.launched, tt.proved, tt.redirects, "https://www.example.com/")
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("resolveDeltaEffectiveOrigin() = %#v; want %#v", got, tt.want)
			}
		})
	}
}

func TestNormalizedLaunchedURLSetMatchesCrawlerIdentity(t *testing.T) {
	got := normalizedLaunchedURLSet([]string{
		"HTTP://WWW.Example.test",
		"https://example.test/path?utm_source=a&b=2",
		"https://example.test/path?b=2",
	})
	for _, want := range []string{
		"http://www.example.test/",
		"https://example.test/path?b=2",
	} {
		if _, ok := got[want]; !ok {
			t.Fatalf("normalized launched identities = %#v; missing %q", got, want)
		}
	}
	if len(got) != 2 {
		t.Fatalf("normalized launched identities = %#v; want two unique crawler identities", got)
	}
}
