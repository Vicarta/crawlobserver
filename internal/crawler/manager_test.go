package crawler

import (
	"sync"
	"testing"

	"github.com/SEObserver/crawlobserver/internal/config"
)

// newTestManager creates a Manager with manually initialized fields,
// bypassing NewManager so we don't need a real store.
func newTestManager(maxSessions int) *Manager {
	if maxSessions <= 0 {
		maxSessions = 20
	}
	return &Manager{
		engines:    make(map[string]*Engine),
		lastErrors: make(map[string]string),
		cfg:        &config.Config{},
		sem:        make(chan struct{}, maxSessions),
		queuedSet:  make(map[string]bool),
	}
}

func TestApplySavedCrawlerConfigLinkPositionOptIn(t *testing.T) {
	tests := []struct {
		name       string
		current    bool
		saved      bool
		newSession bool
		want       bool
	}{
		{name: "existing session requires both opt-ins", current: true, saved: true, want: true},
		{name: "process opt-out disables existing session", current: false, saved: true, want: false},
		{name: "existing session opt-out stays disabled", current: true, saved: false, want: false},
		{name: "new full recrawl uses current opt-in", current: true, saved: false, newSession: true, want: true},
		{name: "new full recrawl respects process opt-out", current: false, saved: true, newSession: true, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Crawler: config.CrawlerConfig{StoreLinkPosition: tt.current}}
			applySavedCrawlerConfig(cfg, config.CrawlerConfig{StoreLinkPosition: tt.saved}, tt.newSession)
			if cfg.Crawler.StoreLinkPosition != tt.want {
				t.Errorf("StoreLinkPosition = %t, want %t", cfg.Crawler.StoreLinkPosition, tt.want)
			}
		})
	}
}

func TestRestoreSavedCrawlerConfigLinkPositionOptIn(t *testing.T) {
	tests := []struct {
		name             string
		serialized       string
		newSession       bool
		wantLinkPosition bool
		wantMaxPages     int
		wantStoreHTML    bool
	}{
		{name: "empty existing session config disables opt-in", wantLinkPosition: false, wantMaxPages: 77, wantStoreHTML: true},
		{name: "empty object preserves process defaults", serialized: `{}`, wantLinkPosition: false, wantMaxPages: 77, wantStoreHTML: true},
		{name: "null config preserves process defaults", serialized: `null`, wantLinkPosition: false, wantMaxPages: 77, wantStoreHTML: true},
		{name: "malformed existing session config disables opt-in", serialized: `{"crawler":`, wantLinkPosition: false, wantMaxPages: 77, wantStoreHTML: true},
		{name: "malformed full recrawl keeps process config", serialized: `{"crawler":`, newSession: true, wantLinkPosition: true, wantMaxPages: 77, wantStoreHTML: true},
		{name: "legacy saved config without flag stays opt-out", serialized: `{"Crawler":{"MaxPages":23,"StoreHTML":false}}`, wantLinkPosition: false, wantMaxPages: 23, wantStoreHTML: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Crawler: config.CrawlerConfig{
				MaxPages:          77,
				StoreHTML:         true,
				StoreLinkPosition: true,
			}}
			restoreSavedCrawlerConfig(cfg, tt.serialized, tt.newSession)
			if cfg.Crawler.StoreLinkPosition != tt.wantLinkPosition {
				t.Errorf("StoreLinkPosition = %t, want %t", cfg.Crawler.StoreLinkPosition, tt.wantLinkPosition)
			}
			if cfg.Crawler.MaxPages != tt.wantMaxPages {
				t.Errorf("MaxPages = %d, want %d", cfg.Crawler.MaxPages, tt.wantMaxPages)
			}
			if cfg.Crawler.StoreHTML != tt.wantStoreHTML {
				t.Errorf("StoreHTML = %t, want %t", cfg.Crawler.StoreHTML, tt.wantStoreHTML)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// NewManager
// ---------------------------------------------------------------------------

func TestNewManagerSemaphoreCapacity(t *testing.T) {
	cfg := &config.Config{}
	cfg.Crawler.MaxConcurrentSessions = 5
	m := NewManager(cfg, nil)
	if cap(m.sem) != 5 {
		t.Fatalf("expected sem capacity 5, got %d", cap(m.sem))
	}
}

func TestNewManagerDefaultCapacity(t *testing.T) {
	cfg := &config.Config{}
	cfg.Crawler.MaxConcurrentSessions = 0 // should default to 20
	m := NewManager(cfg, nil)
	if cap(m.sem) != 20 {
		t.Fatalf("expected sem capacity 20, got %d", cap(m.sem))
	}
}

// ---------------------------------------------------------------------------
// dequeue
// ---------------------------------------------------------------------------

func TestDequeueExisting(t *testing.T) {
	m := newTestManager(5)
	// Manually populate queue with 3 items.
	m.queue = []queuedCrawl{
		{sessionID: "aaa"},
		{sessionID: "bbb"},
		{sessionID: "ccc"},
	}
	m.queuedSet["aaa"] = true
	m.queuedSet["bbb"] = true
	m.queuedSet["ccc"] = true

	ok := m.dequeue("bbb")
	if !ok {
		t.Fatal("dequeue should return true for existing item")
	}
	if len(m.queue) != 2 {
		t.Fatalf("expected queue length 2, got %d", len(m.queue))
	}
	if m.queuedSet["bbb"] {
		t.Fatal("bbb should be removed from queuedSet")
	}
	// Verify remaining order.
	if m.queue[0].sessionID != "aaa" || m.queue[1].sessionID != "ccc" {
		t.Fatalf("unexpected queue order: %v, %v", m.queue[0].sessionID, m.queue[1].sessionID)
	}
}

func TestDequeueNotFound(t *testing.T) {
	m := newTestManager(5)
	m.queue = []queuedCrawl{{sessionID: "aaa"}}
	m.queuedSet["aaa"] = true

	ok := m.dequeue("zzz")
	if ok {
		t.Fatal("dequeue should return false for non-existent item")
	}
	if len(m.queue) != 1 {
		t.Fatal("queue should remain unchanged")
	}
}

func TestDequeueEmpty(t *testing.T) {
	m := newTestManager(5)
	ok := m.dequeue("anything")
	if ok {
		t.Fatal("dequeue on empty queue should return false")
	}
}

// ---------------------------------------------------------------------------
// IsQueued
// ---------------------------------------------------------------------------

func TestIsQueued(t *testing.T) {
	m := newTestManager(5)
	m.queuedSet["abc"] = true

	if !m.IsQueued("abc") {
		t.Fatal("IsQueued should return true for existing ID")
	}
	if m.IsQueued("xyz") {
		t.Fatal("IsQueued should return false for unknown ID")
	}
}

// ---------------------------------------------------------------------------
// QueuedSessions
// ---------------------------------------------------------------------------

func TestQueuedSessions(t *testing.T) {
	m := newTestManager(5)
	m.queue = []queuedCrawl{
		{sessionID: "first"},
		{sessionID: "second"},
		{sessionID: "third"},
	}
	m.queuedSet["first"] = true
	m.queuedSet["second"] = true
	m.queuedSet["third"] = true

	ids := m.QueuedSessions()
	if len(ids) != 3 {
		t.Fatalf("expected 3 queued sessions, got %d", len(ids))
	}
	// Verify FIFO order.
	expected := []string{"first", "second", "third"}
	for i, want := range expected {
		if ids[i] != want {
			t.Fatalf("QueuedSessions[%d] = %q, want %q", i, ids[i], want)
		}
	}
}

func TestQueuedSessionsEmpty(t *testing.T) {
	m := newTestManager(5)
	ids := m.QueuedSessions()
	if len(ids) != 0 {
		t.Fatalf("expected 0 queued sessions, got %d", len(ids))
	}
}

// ---------------------------------------------------------------------------
// Semaphore
// ---------------------------------------------------------------------------

func TestSemaphoreFull(t *testing.T) {
	m := newTestManager(2)
	// Fill semaphore to capacity.
	m.sem <- struct{}{}
	m.sem <- struct{}{}

	// Non-blocking send should fail.
	select {
	case m.sem <- struct{}{}:
		t.Fatal("semaphore should be full; non-blocking send should not succeed")
	default:
		// expected
	}
}

func TestSemaphoreRelease(t *testing.T) {
	m := newTestManager(2)
	// Fill semaphore.
	m.sem <- struct{}{}
	m.sem <- struct{}{}

	// Release one slot.
	<-m.sem

	// Non-blocking send should now succeed.
	select {
	case m.sem <- struct{}{}:
		// expected
	default:
		t.Fatal("semaphore should have a free slot after release")
	}
}

// ---------------------------------------------------------------------------
// promoteNext
// ---------------------------------------------------------------------------

func TestPromoteNextEmpty(t *testing.T) {
	m := newTestManager(5)
	// Should not panic on empty queue.
	m.promoteNext()
	if len(m.queue) != 0 {
		t.Fatal("queue should still be empty after promoteNext on empty queue")
	}
}

// ---------------------------------------------------------------------------
// Concurrent access
// ---------------------------------------------------------------------------

func TestQueueConcurrentAccess(t *testing.T) {
	m := newTestManager(5)
	// Pre-populate queue.
	m.queue = []queuedCrawl{
		{sessionID: "s1"},
		{sessionID: "s2"},
		{sessionID: "s3"},
	}
	m.queuedSet["s1"] = true
	m.queuedSet["s2"] = true
	m.queuedSet["s3"] = true

	var wg sync.WaitGroup
	const goroutines = 50

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_ = m.IsQueued("s1")
			_ = m.IsQueued("s2")
			_ = m.IsQueued("unknown")
			_ = m.QueuedSessions()
		}()
	}
	wg.Wait()

	// If we get here without a race detector complaint, the test passes.
	// Verify queue is still intact.
	ids := m.QueuedSessions()
	if len(ids) != 3 {
		t.Fatalf("expected 3 queued sessions after concurrent reads, got %d", len(ids))
	}
}
