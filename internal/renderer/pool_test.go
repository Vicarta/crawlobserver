package renderer

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
)

func TestDefaultPoolOptions(t *testing.T) {
	opts := DefaultPoolOptions()

	if opts.MaxPages != 4 {
		t.Errorf("MaxPages = %d, want 4", opts.MaxPages)
	}
	if opts.PageTimeout != 15*time.Second {
		t.Errorf("PageTimeout = %v, want 15s", opts.PageTimeout)
	}
	if opts.UserAgent != "" {
		t.Errorf("UserAgent = %q, want empty string", opts.UserAgent)
	}
	if !opts.BlockResources {
		t.Error("BlockResources should default to true")
	}
	if !opts.Headless {
		t.Error("Headless should default to true")
	}
}

func TestDefaultPoolOptions_Immutability(t *testing.T) {
	opts1 := DefaultPoolOptions()
	opts2 := DefaultPoolOptions()

	// Modifying one should not affect the other (they are value types, not pointers)
	opts1.MaxPages = 99
	if opts2.MaxPages != 4 {
		t.Errorf("modifying opts1 affected opts2: MaxPages = %d, want 4", opts2.MaxPages)
	}
}

func TestRenderOriginNormalizesSchemeAndHost(t *testing.T) {
	got := renderOrigin("HTTPS://Example.COM:443/path?q=1")
	if got != "https://example.com:443" {
		t.Fatalf("renderOrigin() = %q", got)
	}
	if got := renderOrigin("/relative"); got != "" {
		t.Fatalf("relative renderOrigin() = %q, want empty", got)
	}
}

func TestOriginGateSerializesSameOrigin(t *testing.T) {
	pool := &Pool{}
	releaseFirst, err := pool.acquireOrigin(context.Background(), "https://example.com/one")
	if err != nil {
		t.Fatal(err)
	}

	acquiredSecond := make(chan func(), 1)
	go func() {
		release, acquireErr := pool.acquireOrigin(context.Background(), "https://example.com/two")
		if acquireErr == nil {
			acquiredSecond <- release
		}
	}()

	select {
	case <-acquiredSecond:
		t.Fatal("same-origin render acquired before the first render released")
	case <-time.After(50 * time.Millisecond):
	}

	releaseFirst()
	select {
	case releaseSecond := <-acquiredSecond:
		releaseSecond()
	case <-time.After(time.Second):
		t.Fatal("same-origin render did not acquire after release")
	}
}

func TestOriginGateAllowsDifferentOrigins(t *testing.T) {
	pool := &Pool{}
	releaseFirst, err := pool.acquireOrigin(context.Background(), "https://one.example/page")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseFirst()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	releaseSecond, err := pool.acquireOrigin(ctx, "https://two.example/page")
	if err != nil {
		t.Fatalf("different origin was blocked: %v", err)
	}
	releaseSecond()
}

func TestOriginGateHonorsCancellation(t *testing.T) {
	pool := &Pool{}
	releaseFirst, err := pool.acquireOrigin(context.Background(), "https://example.com/one")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseFirst()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	release, err := pool.acquireOrigin(ctx, "https://example.com/two")
	if release != nil {
		t.Fatal("cancelled origin wait returned a release function")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled origin wait error = %v", err)
	}
}

func TestPoolCloseCleansUpLauncherExactlyOnce(t *testing.T) {
	var cleanupCalls atomic.Int32
	pool := &Pool{
		pages: make(chan *rod.Page, 1),
		launcherCleanup: func() {
			cleanupCalls.Add(1)
		},
	}

	pool.Close()
	pool.Close()

	if got := cleanupCalls.Load(); got != 1 {
		t.Fatalf("launcher cleanup calls = %d, want 1", got)
	}
	if _, err := pool.Acquire(); !errors.Is(err, errPoolClosed) {
		t.Fatalf("Acquire after Close error = %v, want %v", err, errPoolClosed)
	}
}

func TestPoolCloseIsConcurrentSafe(t *testing.T) {
	var cleanupCalls atomic.Int32
	pool := &Pool{
		pages: make(chan *rod.Page, 1),
		launcherCleanup: func() {
			cleanupCalls.Add(1)
		},
	}

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pool.Close()
		}()
	}
	wg.Wait()

	if got := cleanupCalls.Load(); got != 1 {
		t.Fatalf("concurrent launcher cleanup calls = %d, want 1", got)
	}
}
