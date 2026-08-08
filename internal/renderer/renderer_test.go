package renderer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRenderWaitsForDeferredSPARouteCommit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html>
<html>
  <head><title>Shared shell</title></head>
  <body>
    <h1>Shared shell heading</h1>
    <script>
      setTimeout(() => {
        document.title = 'Rendered route title';
        document.querySelector('h1').textContent = 'Rendered route heading';
      }, 650);
    </script>
  </body>
</html>`)
	}))
	defer server.Close()

	opts := DefaultPoolOptions()
	opts.MaxPages = 1
	opts.PageTimeout = 5 * time.Second
	opts.BlockResources = false
	pool, err := NewPool(opts)
	if err != nil {
		t.Skipf("Chrome is unavailable: %v", err)
	}
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := pool.Render(ctx, server.URL)
	if result.Error != nil {
		t.Fatalf("render failed: %v", result.Error)
	}
	if !strings.Contains(result.RenderedHTML, "<title>Rendered route title</title>") {
		t.Fatalf("captured HTML before deferred title commit: %s", result.RenderedHTML)
	}
	if !strings.Contains(result.RenderedHTML, "<h1>Rendered route heading</h1>") {
		t.Fatalf("captured HTML before deferred body commit: %s", result.RenderedHTML)
	}
}

func TestRenderSerializesConcurrentSameOriginNavigations(t *testing.T) {
	var active atomic.Int32
	var maximum atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			previous := maximum.Load()
			if current <= previous || maximum.CompareAndSwap(previous, current) {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, "<html><head><title>%s</title></head><body><h1>%s</h1></body></html>", r.URL.Path, r.URL.Path)
	}))
	defer server.Close()

	opts := DefaultPoolOptions()
	opts.MaxPages = 2
	opts.PageTimeout = 5 * time.Second
	opts.BlockResources = false
	pool, err := NewPool(opts)
	if err != nil {
		t.Skipf("Chrome is unavailable: %v", err)
	}
	defer pool.Close()

	var wg sync.WaitGroup
	results := make(chan *RenderResult, 2)
	for _, path := range []string{"/one", "/two"} {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			results <- pool.Render(ctx, server.URL+path)
		}(path)
	}
	wg.Wait()
	close(results)

	for result := range results {
		if result.Error != nil {
			t.Fatalf("render failed: %v", result.Error)
		}
	}
	if got := maximum.Load(); got != 1 {
		t.Fatalf("maximum concurrent same-origin navigations = %d, want 1", got)
	}
}

func TestRenderPageTimeoutStartsAfterOriginSlotAcquired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><head><title>Queued route</title></head><body><h1>Queued route</h1></body></html>")
	}))
	defer server.Close()

	opts := DefaultPoolOptions()
	opts.MaxPages = 1
	opts.PageTimeout = 5 * time.Second
	opts.BlockResources = false
	pool, err := NewPool(opts)
	if err != nil {
		t.Skipf("Chrome is unavailable: %v", err)
	}
	defer pool.Close()

	releaseOrigin, err := pool.acquireOrigin(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("occupy origin slot: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resultCh := make(chan *RenderResult, 1)
	go func() {
		resultCh <- pool.Render(ctx, server.URL+"/queued")
	}()

	time.Sleep(5200 * time.Millisecond)
	releaseOrigin()

	result := <-resultCh
	if result.Error != nil {
		t.Fatalf("render failed after waiting for origin slot: %v", result.Error)
	}
	if !strings.Contains(result.RenderedHTML, "<title>Queued route</title>") {
		t.Fatalf("unexpected rendered HTML: %s", result.RenderedHTML)
	}
}

func TestRenderWithCWVCollectsPositiveLCPAndTTFB(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(75 * time.Millisecond)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html>
<html>
  <head><title>CWV fixture</title></head>
  <body><main><h1 style="font-size: 64px">Largest visible content</h1></main></body>
</html>`)
	}))
	defer server.Close()

	opts := DefaultPoolOptions()
	opts.MaxPages = 1
	opts.PageTimeout = 5 * time.Second
	opts.BlockResources = false
	pool, err := NewPool(opts)
	if err != nil {
		t.Skipf("Chrome is unavailable: %v", err)
	}
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := pool.RenderWithCWV(ctx, server.URL)
	if result.Error != nil {
		t.Fatalf("render failed: %v", result.Error)
	}
	if !result.CWVMeasured {
		t.Fatalf("CWV was not measured: %s (LCP=%f CLS=%f TTFB=%f)", result.CWVError, result.CWVLCP, result.CWVCLS, result.CWVTTFB)
	}
	if result.CWVLCP <= 0 {
		t.Fatalf("LCP = %f, want a positive duration", result.CWVLCP)
	}
	if result.CWVTTFB <= 0 {
		t.Fatalf("TTFB = %f, want a positive duration", result.CWVTTFB)
	}
	if result.CWVCLS < 0 {
		t.Fatalf("CLS = %f, want a non-negative score", result.CWVCLS)
	}
	if result.CWVError != "" {
		t.Fatalf("unexpected CWV diagnostic: %s", result.CWVError)
	}
}

func TestValidCWVMeasurement(t *testing.T) {
	tests := []struct {
		name           string
		lcp, cls, ttfb float64
		want           bool
	}{
		{name: "valid zero CLS", lcp: 1200, cls: 0, ttfb: 300, want: true},
		{name: "missing LCP", lcp: 0, cls: 0.1, ttfb: 300, want: false},
		{name: "missing TTFB", lcp: 1200, cls: 0.1, ttfb: 0, want: false},
		{name: "negative CLS", lcp: 1200, cls: -0.1, ttfb: 300, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validCWVMeasurement(tt.lcp, tt.cls, tt.ttfb); got != tt.want {
				t.Fatalf("validCWVMeasurement(%f, %f, %f) = %t, want %t", tt.lcp, tt.cls, tt.ttfb, got, tt.want)
			}
		})
	}
}
