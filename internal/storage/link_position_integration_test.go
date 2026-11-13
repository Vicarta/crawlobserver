//go:build integration

package storage

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestLinkPositionStorageAndSnapshotPropagation(t *testing.T) {
	s := testStore(t)
	t.Cleanup(func() { s.Close() })

	ctx := context.Background()
	deltaID, currentID, snapshotID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	cleanup := func(sessionIDs ...string) {
		for _, sessionID := range sessionIDs {
			cleanupSession(t, s, sessionID)
		}
	}
	t.Cleanup(func() { cleanup(deltaID, currentID, snapshotID) })

	want := LinkRow{
		CrawlSessionID: deltaID,
		SourceURL:      "https://fixture.example/source",
		TargetURL:      "https://fixture.example/target",
		AnchorText:     "Target",
		IsInternal:     true,
		Tag:            "a",
		LinkLocation:   "body",
		Landmark:       "nav",
		XPath:          "/html/body/nav/ul/li[2]/a",
		Depth:          5,
		DocumentIndex:  7,
		BlockSignature: 18446744073709551613,
		CrawledAt:      time.Now().UTC(),
	}
	if err := s.InsertLinks(ctx, []LinkRow{want}); err != nil {
		t.Fatalf("inserting link: %v", err)
	}

	filters := []ParsedFilter{{Def: LinkFilters["landmark"], Value: "nav"}}
	got, err := s.InternalLinksPaginated(ctx, deltaID, 10, 0, filters, ParseSort("depth", "desc", LinkSortColumns))
	if err != nil {
		t.Fatalf("reading filtered links: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("filtered links = %d, want 1", len(got))
	}
	assertLinkPositionRow(t, got[0], want)

	encoded, err := json.Marshal(got[0])
	if err != nil {
		t.Fatalf("encoding API link row: %v", err)
	}
	if !strings.Contains(string(encoded), `"BlockSignature":"18446744073709551613"`) {
		t.Errorf("BlockSignature was not JSON encoded as an exact decimal string: %s", encoded)
	}

	pageLinks, err := s.GetPageLinks(ctx, deltaID, want.SourceURL, 10, 0, 10, 0)
	if err != nil {
		t.Fatalf("reading page links: %v", err)
	}
	if len(pageLinks.OutLinks) != 1 {
		t.Fatalf("outbound links = %d, want 1", len(pageLinks.OutLinks))
	}
	assertLinkPositionRow(t, pageLinks.OutLinks[0], want)

	if err := s.overlayDeltaLinks(ctx, currentID, deltaID); err != nil {
		t.Fatalf("overlaying delta links: %v", err)
	}
	assertSessionLinkPosition(t, s, ctx, currentID, want)
	if err := s.copySnapshotLinks(ctx, currentID, snapshotID); err != nil {
		t.Fatalf("copying current snapshot links: %v", err)
	}
	assertSessionLinkPosition(t, s, ctx, snapshotID, want)
}

func TestLinkPositionRefreshRecomputesOnlyPreviouslyEnabledSources(t *testing.T) {
	s := testStore(t)
	t.Cleanup(func() { s.Close() })

	ctx := context.Background()
	sessionID := uuid.NewString()
	pageWithPosition := "https://fixture.example/positioned"
	pageWithoutPosition := "https://fixture.example/legacy"
	t.Cleanup(func() {
		cleanupSession(t, s, sessionID)
		if err := s.conn.Exec(ctx, `ALTER TABLE crawlobserver.pages DELETE WHERE crawl_session_id = ? SETTINGS mutations_sync = 2`, sessionID); err != nil {
			t.Logf("cleanup pages: %v", err)
		}
	})

	now := time.Now().UTC()
	pages := []PageRow{
		{
			CrawlSessionID: sessionID,
			URL:            pageWithPosition,
			BodyHTML:       `<html><body><nav><a href="/static-only">Static only</a></nav></body></html>`,
			CrawledAt:      now,
		},
		{
			CrawlSessionID: sessionID,
			URL:            pageWithoutPosition,
			BodyHTML:       `<html><body><main><a href="/legacy-only">Legacy only</a></main></body></html>`,
			CrawledAt:      now,
		},
	}
	if err := s.InsertPages(ctx, pages); err != nil {
		t.Fatalf("inserting page fixtures: %v", err)
	}
	if err := s.InsertLinks(ctx, []LinkRow{
		{
			CrawlSessionID: sessionID,
			SourceURL:      pageWithPosition,
			TargetURL:      "https://fixture.example/rendered-only",
			AnchorText:     "Rendered only",
			IsInternal:     true,
			Tag:            "a",
			XPath:          "/html/body/div/a",
			Landmark:       "main",
			Depth:          3,
			DocumentIndex:  0,
			BlockSignature: 999,
			CrawledAt:      now,
		},
		{
			CrawlSessionID: sessionID,
			SourceURL:      pageWithoutPosition,
			TargetURL:      "https://fixture.example/old-only",
			AnchorText:     "Old only",
			IsInternal:     true,
			Tag:            "a",
			CrawledAt:      now,
		},
	}); err != nil {
		t.Fatalf("inserting previous graph fixtures: %v", err)
	}

	if err := s.refreshLinkLocations(ctx, sessionID, nil); err != nil {
		t.Fatalf("refreshing link locations: %v", err)
	}
	got, err := s.InternalLinksPaginated(ctx, sessionID, 10, 0, nil, nil)
	if err != nil {
		t.Fatalf("reading refreshed links: %v", err)
	}
	bySource := make(map[string]LinkRow, len(got))
	for _, link := range got {
		bySource[link.SourceURL] = link
	}
	positioned := bySource[pageWithPosition]
	if positioned.TargetURL != "https://fixture.example/static-only" || positioned.Landmark != "nav" || positioned.XPath != "/html/body/nav/a" || positioned.Depth != 3 || positioned.DocumentIndex != 0 || positioned.BlockSignature == 0 {
		t.Errorf("refreshed position metadata = %+v", positioned)
	}
	legacy := bySource[pageWithoutPosition]
	if legacy.TargetURL != "https://fixture.example/legacy-only" || legacy.Landmark != "" || legacy.XPath != "" || legacy.Depth != 0 || legacy.DocumentIndex != 0 || legacy.BlockSignature != 0 {
		t.Errorf("refresh backfilled metadata for a source without prior position evidence: %+v", legacy)
	}
}

func assertSessionLinkPosition(t *testing.T, s *Store, ctx context.Context, sessionID string, want LinkRow) {
	t.Helper()
	got, err := s.InternalLinksPaginated(ctx, sessionID, 10, 0, nil, nil)
	if err != nil {
		t.Fatalf("reading session %s links: %v", sessionID, err)
	}
	if len(got) != 1 {
		t.Fatalf("session %s links = %d, want 1", sessionID, len(got))
	}
	assertLinkPositionRow(t, got[0], want)
}

func assertLinkPositionRow(t *testing.T, got, want LinkRow) {
	t.Helper()
	if got.Landmark != want.Landmark || got.XPath != want.XPath || got.Depth != want.Depth ||
		got.DocumentIndex != want.DocumentIndex || got.BlockSignature != want.BlockSignature {
		t.Errorf("link position = {%q %q %d %d %d}, want {%q %q %d %d %d}",
			got.Landmark, got.XPath, got.Depth, got.DocumentIndex, got.BlockSignature,
			want.Landmark, want.XPath, want.Depth, want.DocumentIndex, want.BlockSignature)
	}
}
