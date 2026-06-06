package server

import (
	"context"
	"testing"

	"github.com/SEObserver/crawlobserver/internal/gsc"
	"github.com/SEObserver/crawlobserver/internal/storage"
)

func TestSortGSCPropertiesPrefersProjectDomainAndHTTPS(t *testing.T) {
	projectID := "project-di"
	store := &mockStore{
		sessions: []storage.CrawlSession{
			{
				ID:        "session-di",
				SeedURLs:  []string{"https://diskinternals.com/raid-recovery/"},
				ProjectID: &projectID,
			},
		},
	}
	server := &Server{store: store}
	props := []gsc.Property{
		{SiteURL: "http://www.diskinternals.com/", PermissionLevel: "siteOwner"},
		{SiteURL: "https://example.com/", PermissionLevel: "siteOwner"},
		{SiteURL: "https://www.diskinternals.com/", PermissionLevel: "siteOwner"},
		{SiteURL: "sc-domain:diskinternals.com", PermissionLevel: "siteOwner"},
	}

	server.sortGSCProperties(context.Background(), projectID, props)

	got := []string{props[0].SiteURL, props[1].SiteURL, props[2].SiteURL, props[3].SiteURL}
	want := []string{
		"sc-domain:diskinternals.com",
		"https://www.diskinternals.com/",
		"http://www.diskinternals.com/",
		"https://example.com/",
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("property order = %#v, want %#v", got, want)
		}
	}
}

func TestGSCPropertyScoreHandlesInvalidAndUnknownProperties(t *testing.T) {
	candidates := map[string]bool{"diskinternals.com": true}

	if got := gscPropertyScore("not a url", candidates); got != 0 {
		t.Fatalf("invalid URL score = %d, want 0", got)
	}
	if got := gscPropertyScore("sc-domain:other.example", candidates); got <= 0 {
		t.Fatalf("unknown domain property score = %d, want positive baseline", got)
	}
	if matching := gscPropertyScore("sc-domain:diskinternals.com", candidates); matching <= gscPropertyScore("sc-domain:other.example", candidates) {
		t.Fatalf("matching domain property should outrank unrelated domain property")
	}
}
