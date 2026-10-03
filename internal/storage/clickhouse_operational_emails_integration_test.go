//go:build integration

package storage

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestOperationalEmailNewPageErrorsIntegration(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	projectID := "email-alerts-" + uuid.NewString()
	otherProjectID := "email-alerts-other-" + uuid.NewString()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	priorID, healthyID, otherProjectPriorID, currentID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	ids := []string{priorID, healthyID, otherProjectPriorID, currentID}
	t.Cleanup(func() {
		for _, sessionID := range ids {
			if err := store.conn.Exec(ctx, `ALTER TABLE crawlobserver.pages DELETE WHERE toString(crawl_session_id) = ?`, sessionID); err != nil {
				t.Logf("cleaning operational email page fixture %s: %v", sessionID, err)
			}
			if err := store.conn.Exec(ctx, `ALTER TABLE crawlobserver.crawl_sessions DELETE WHERE toString(id) = ?`, sessionID); err != nil {
				t.Logf("cleaning operational email session fixture %s: %v", sessionID, err)
			}
		}
		if err := store.Close(); err != nil {
			t.Logf("closing ClickHouse fixture: %v", err)
		}
	})

	insertObservation := func(sessionID, owner string, finishedAt time.Time, observations ...PageErrorObservation) {
		t.Helper()
		project := owner
		session := &CrawlSession{
			ID: sessionID, StartedAt: finishedAt.Add(-time.Minute), FinishedAt: finishedAt,
			Status: "completed", Config: "{}", ProjectID: &project,
		}
		if err := store.InsertSession(ctx, session); err != nil {
			t.Fatalf("inserting session %s: %v", sessionID, err)
		}
		pages := make([]PageRow, 0, len(observations))
		for _, observation := range observations {
			fetchReason := observation.FetchReason
			if observation.FetchError && fetchReason == "" {
				fetchReason = "fixture fetch error"
			}
			pages = append(pages, PageRow{CrawlSessionID: sessionID, URL: observation.URL, StatusCode: observation.StatusCode, Error: fetchReason})
		}
		if err := store.InsertPages(ctx, pages); err != nil {
			t.Fatalf("inserting pages for %s: %v", sessionID, err)
		}
	}

	repeatURL := "https://example.test/repeat?key=one"
	reappearURL := "https://example.test/reappear?key=one"
	queryPriorURL := "https://example.test/query?key=old"
	queryCurrentURL := "https://example.test/query?key=current"
	projectScopedURL := "https://example.test/project-scoped?key=one"
	fetchURL := "https://example.test/retry?lang=en&access_token=secret"
	insertObservation(priorID, projectID, base,
		PageErrorObservation{URL: repeatURL, StatusCode: 404},
		PageErrorObservation{URL: reappearURL, StatusCode: 404},
		PageErrorObservation{URL: queryPriorURL, StatusCode: 404},
	)
	insertObservation(otherProjectPriorID, otherProjectID, base.Add(30*time.Second),
		PageErrorObservation{URL: projectScopedURL, StatusCode: 404},
	)
	insertObservation(healthyID, projectID, base.Add(time.Minute),
		PageErrorObservation{URL: reappearURL, StatusCode: 200},
	)
	current := CrawlSession{ID: currentID, StartedAt: base.Add(2 * time.Minute), FinishedAt: base.Add(3 * time.Minute), Status: "completed", Config: "{}", ProjectID: &projectID}
	if err := store.InsertSession(ctx, &current); err != nil {
		t.Fatalf("inserting current session: %v", err)
	}
	if err := store.InsertPages(ctx, []PageRow{
		{CrawlSessionID: currentID, URL: repeatURL, StatusCode: 404},
		{CrawlSessionID: currentID, URL: reappearURL, StatusCode: 404},
		{CrawlSessionID: currentID, URL: queryCurrentURL, StatusCode: 404},
		{CrawlSessionID: currentID, URL: projectScopedURL, StatusCode: 404},
		{CrawlSessionID: currentID, URL: fetchURL, StatusCode: 0, Error: "GET https://user:password@example.test/retry?lang=en&access_token=secret: context deadline exceeded"},
	}); err != nil {
		t.Fatalf("inserting current page observations: %v", err)
	}
	allErrors, err := store.PageErrorsForSession(ctx, current)
	if err != nil {
		t.Fatalf("PageErrorsForSession: %v", err)
	}
	var fetchObservation *PageErrorObservation
	for i := range allErrors {
		if allErrors[i].URL == fetchURL {
			fetchObservation = &allErrors[i]
			break
		}
	}
	if fetchObservation == nil || !fetchObservation.FetchError || fetchObservation.FetchReason == "" {
		t.Fatalf("fetch observation = %#v; want the actual fetch error reason", fetchObservation)
	}
	for _, secret := range []string{"user:password", "access_token=secret"} {
		if strings.Contains(fetchObservation.FetchReason, secret) {
			t.Fatalf("fetch reason leaked %q: %s", secret, fetchObservation.FetchReason)
		}
	}
	if !strings.Contains(fetchObservation.FetchReason, "context deadline exceeded") || !strings.Contains(fetchObservation.FetchReason, "/retry") || !strings.Contains(fetchObservation.FetchReason, "lang=en") {
		t.Fatalf("sanitized fetch reason lost useful diagnostic context: %s", fetchObservation.FetchReason)
	}

	got, err := store.NewPageErrorsForSession(ctx, current)
	if err != nil {
		t.Fatalf("NewPageErrorsForSession: %v", err)
	}
	gotURLs := make([]string, 0, len(got))
	for _, observation := range got {
		gotURLs = append(gotURLs, observation.URL)
	}
	wantURLs := []string{projectScopedURL, queryCurrentURL, reappearURL, fetchURL}
	if !reflect.DeepEqual(gotURLs, wantURLs) {
		t.Fatalf("new error URLs = %#v; want %#v", gotURLs, wantURLs)
	}
}
