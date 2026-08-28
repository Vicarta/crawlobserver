package storage

import (
	"encoding/json"
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
			if got := resolveEffectiveOrigin(launched, tt.proved); got != tt.want {
				t.Fatalf("resolveEffectiveOrigin() = %#v; want %#v", got, tt.want)
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
