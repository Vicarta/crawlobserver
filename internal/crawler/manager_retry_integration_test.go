//go:build integration

package crawler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/SEObserver/crawlobserver/internal/config"
	"github.com/SEObserver/crawlobserver/internal/storage"
	"github.com/google/uuid"
)

func retryPreserveTestStore(t *testing.T) *storage.Store {
	t.Helper()

	host := os.Getenv("CH_HOST")
	if host == "" {
		host = "localhost"
	}
	port := 19000
	if value := os.Getenv("CH_PORT"); value != "" {
		if _, err := fmt.Sscanf(value, "%d", &port); err != nil {
			t.Fatalf("invalid CH_PORT %q: %v", value, err)
		}
	}
	username := os.Getenv("CH_USER")
	if username == "" {
		username = "default"
	}
	store, err := storage.NewStore(host, port, "default", username, os.Getenv("CH_PASSWORD"))
	if err != nil {
		if os.Getenv("CRAWLOBSERVER_REQUIRE_CLICKHOUSE") == "1" {
			t.Fatalf("ClickHouse required but unavailable: %v", err)
		}
		t.Skipf("ClickHouse not available: %v", err)
	}
	if err := store.Conn().Exec(context.Background(), storage.CreateDatabase); err != nil {
		t.Fatalf("creating ClickHouse database: %v", err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrating ClickHouse schema: %v", err)
	}
	return store
}

func TestRetryFailedPreservesRowsUntilNewResultIsStored(t *testing.T) {
	store := retryPreserveTestStore(t)
	ctx := context.Background()
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Logf("closing retry test store: %v", err)
		}
	})
	sessionID := uuid.NewString()
	t.Cleanup(func() {
		if err := store.DeleteSession(ctx, sessionID); err != nil {
			t.Logf("cleaning retry test session: %v", err)
		}
	})

	retry404Started := make(chan struct{}, 1)
	retry404Release := make(chan struct{})
	var releaseRetry404 sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /blocked\n"))
			return
		}
		if r.URL.Path == "/success" || r.URL.Path == "/retry-404" {
			if r.URL.Path == "/retry-404" {
				retry404Started <- struct{}{}
				<-retry404Release
			}
			w.Header().Set("Content-Type", "text/html")
			title := "Retry result"
			if r.URL.Path == "/retry-404" {
				title = "Retry 404 result"
			}
			_, _ = fmt.Fprintf(w, "<html><head><title>%s</title></head><body>updated</body></html>", title)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	defer releaseRetry404.Do(func() { close(retry404Release) })

	blockedURL := server.URL + "/blocked"
	successURL := server.URL + "/success"
	retry404URL := server.URL + "/retry-404"
	unselectedURL := server.URL + "/not-selected"
	cfg := testManagerConfig()
	cfg.Crawler.AllowPrivateIPs = true
	cfg.Crawler.RespectRobots = true
	cfg.Crawler.MaxConcurrentSessions = 1
	cfg.Crawler.Workers = 1
	cfg.Crawler.Delay = 0
	cfg.Crawler.StoreHTML = false
	cfg.Crawler.CrawlScope = "host"
	savedConfig, err := config.SessionConfigJSON(cfg)
	if err != nil {
		t.Fatalf("serializing session config: %v", err)
	}
	if err := store.InsertSession(ctx, &storage.CrawlSession{
		ID:        sessionID,
		StartedAt: time.Now().Add(-time.Hour),
		Status:    "completed",
		SeedURLs:  []string{blockedURL, successURL, retry404URL, unselectedURL},
		Config:    savedConfig,
	}); err != nil {
		t.Fatalf("inserting test session: %v", err)
	}

	oldCrawledAt := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	oldPages := []storage.PageRow{
		{CrawlSessionID: sessionID, URL: blockedURL, StatusCode: 0, Error: "previous timeout", Depth: 2, FoundOn: server.URL + "/source", CrawledAt: oldCrawledAt},
		{CrawlSessionID: sessionID, URL: successURL, StatusCode: 0, Error: "previous timeout", Depth: 1, FoundOn: server.URL + "/source", CrawledAt: oldCrawledAt},
		{CrawlSessionID: sessionID, URL: retry404URL, StatusCode: http.StatusNotFound, Error: "existing 404", Depth: 1, FoundOn: server.URL + "/source", CrawledAt: oldCrawledAt},
		{CrawlSessionID: sessionID, URL: unselectedURL, StatusCode: http.StatusInternalServerError, Error: "existing 500", Depth: 1, FoundOn: server.URL + "/source", CrawledAt: oldCrawledAt},
	}
	if err := store.InsertPages(ctx, oldPages); err != nil {
		t.Fatalf("inserting prior page results: %v", err)
	}

	manager := NewManager(cfg, store)
	defer func() {
		releaseRetry404.Do(func() { close(retry404Release) })
		waitForRetryManagerStop(manager, sessionID)
	}()
	count, err := manager.RetryFailed(sessionID, nil)
	if err != nil {
		t.Fatalf("retrying status-0 pages: %v", err)
	}
	if count != 2 {
		t.Fatalf("retry receipt = %d URLs, want 2", count)
	}
	assertNoPageMutationForSession(t, store, sessionID)

	waitForRetryResult(t, store, sessionID, successURL, "Retry result")
	waitForRetryIdle(t, manager, store, sessionID)

	blocked, err := store.GetPage(ctx, sessionID, blockedURL)
	if err != nil {
		t.Fatalf("reading robots-blocked retry page: %v", err)
	}
	if blocked.StatusCode != 0 || blocked.Error != "previous timeout" || !blocked.CrawledAt.Equal(oldCrawledAt) {
		t.Fatalf("robots-blocked page was mutated without a new result: status=%d error=%q crawled_at=%s, want original result at %s", blocked.StatusCode, blocked.Error, blocked.CrawledAt, oldCrawledAt)
	}

	unchanged404, err := store.GetPage(ctx, sessionID, retry404URL)
	if err != nil {
		t.Fatalf("reading explicit-status retry page: %v", err)
	}
	if unchanged404.StatusCode != http.StatusNotFound || unchanged404.Error != "existing 404" || !unchanged404.CrawledAt.Equal(oldCrawledAt) {
		t.Fatalf("unselected 404 page changed: status=%d error=%q crawled_at=%s, want original result at %s", unchanged404.StatusCode, unchanged404.Error, unchanged404.CrawledAt, oldCrawledAt)
	}

	count, err = manager.RetryFailed(sessionID, &CrawlRequest{RetryStatusCode: http.StatusNotFound})
	if err != nil {
		t.Fatalf("retrying explicit status 404: %v", err)
	}
	if count != 1 {
		t.Fatalf("explicit 404 retry receipt = %d URLs, want 1", count)
	}
	assertNoPageMutationForSession(t, store, sessionID)
	select {
	case <-retry404Started:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the explicit 404 retry request")
	}

	stillVisible, err := store.GetPage(ctx, sessionID, retry404URL)
	if err != nil {
		t.Fatalf("reading in-flight 404 retry page: %v", err)
	}
	if stillVisible.StatusCode != http.StatusNotFound || stillVisible.Error != "existing 404" || !stillVisible.CrawledAt.Equal(oldCrawledAt) {
		t.Fatalf("prior 404 result disappeared before a replacement was stored: status=%d error=%q crawled_at=%s", stillVisible.StatusCode, stillVisible.Error, stillVisible.CrawledAt)
	}
	releaseRetry404.Do(func() { close(retry404Release) })
	waitForRetryResult(t, store, sessionID, retry404URL, "Retry 404 result")
	waitForRetryIdle(t, manager, store, sessionID)

	unselected, err := store.GetPage(ctx, sessionID, unselectedURL)
	if err != nil {
		t.Fatalf("reading unselected page: %v", err)
	}
	if unselected.StatusCode != http.StatusInternalServerError || unselected.Error != "existing 500" || !unselected.CrawledAt.Equal(oldCrawledAt) {
		t.Fatalf("unselected page changed: status=%d error=%q crawled_at=%s, want original 500 at %s", unselected.StatusCode, unselected.Error, unselected.CrawledAt, oldCrawledAt)
	}
}

func assertNoPageMutationForSession(t *testing.T, store *storage.Store, sessionID string) {
	t.Helper()
	var mutations uint64
	if err := store.Conn().QueryRow(context.Background(), `
		SELECT count()
		FROM system.mutations
		WHERE database = 'crawlobserver' AND table = 'pages'
			AND position(command, ?) > 0`, sessionID).Scan(&mutations); err != nil {
		t.Fatalf("checking page mutations for session %s: %v", sessionID, err)
	}
	if mutations != 0 {
		t.Fatalf("retry created %d page mutation(s) for session %s", mutations, sessionID)
	}
}

func waitForRetryResult(t *testing.T, store *storage.Store, sessionID, url, title string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		page, err := store.GetPage(context.Background(), sessionID, url)
		if err != nil {
			t.Fatalf("reading retry result: %v", err)
		}
		if page.StatusCode == http.StatusOK && page.Title == title {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("retry result was not persisted; latest page: status=%d title=%q error=%q", page.StatusCode, page.Title, page.Error)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func waitForRetryIdle(t *testing.T, manager *Manager, store *storage.Store, sessionID string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		manager.mu.RLock()
		_, running := manager.engines[sessionID]
		manager.mu.RUnlock()
		session, err := store.GetSession(context.Background(), sessionID)
		if err != nil {
			t.Fatalf("reading retry session: %v", err)
		}
		if !running && session.Status == "completed" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("retry session did not become idle: running=%t status=%q error=%q", running, session.Status, manager.LastError(sessionID))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func waitForRetryManagerStop(manager *Manager, sessionID string) {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		manager.mu.RLock()
		_, running := manager.engines[sessionID]
		manager.mu.RUnlock()
		if !running {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
