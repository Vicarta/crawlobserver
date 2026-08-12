package renderer

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"
)

// PoolOptions configures the Chrome page pool.
type PoolOptions struct {
	MaxPages       int
	PageTimeout    time.Duration
	UserAgent      string
	BlockResources bool
	Headless       bool
}

func DefaultPoolOptions() PoolOptions {
	return PoolOptions{
		MaxPages:       4,
		PageTimeout:    15 * time.Second,
		UserAgent:      "",
		BlockResources: true,
		Headless:       true,
	}
}

// Pool manages a single Chrome browser with a pool of reusable pages.
type Pool struct {
	browser         *rod.Browser
	launcher        *launcher.Launcher
	launcherCleanup func()
	pages           chan *rod.Page
	opts            PoolOptions
	mu              sync.Mutex
	closed          bool
	closeOnce       sync.Once
	cleanupOnce     sync.Once

	// originGates prevents parallel top-level renders from overwhelming one SPA
	// origin while preserving concurrency across unrelated origins.
	originGates sync.Map // map[string]chan struct{}
}

var errPoolClosed = errors.New("renderer pool is closed")

// NewPool launches a headless Chrome and creates a page pool.
func NewPool(opts PoolOptions) (*Pool, error) {
	if opts.MaxPages <= 0 {
		opts.MaxPages = 4
	}
	if opts.PageTimeout <= 0 {
		opts.PageTimeout = 15 * time.Second
	}

	l := launcher.New().
		Headless(opts.Headless).
		Set("no-sandbox").
		Set("disable-dev-shm-usage")
	if chromeBin := os.Getenv("CRAWLOBSERVER_CHROME_BIN"); chromeBin != "" {
		l = l.Bin(chromeBin)
	}
	controlURL, err := l.Launch()
	if err != nil {
		cleanupFailedLaunch(l)
		return nil, err
	}

	browser := rod.New().ControlURL(controlURL)
	if err := browser.Connect(); err != nil {
		cleanupLaunchedBrowser(l)
		return nil, err
	}

	p := &Pool{
		browser:         browser,
		launcher:        l,
		launcherCleanup: l.Cleanup,
		pages:           make(chan *rod.Page, opts.MaxPages),
		opts:            opts,
	}

	return p, nil
}

// cleanupFailedLaunch removes a partially-created profile without waiting on a
// launcher that never started a browser process.
func cleanupFailedLaunch(l *launcher.Launcher) {
	if l == nil {
		return
	}
	if l.PID() == 0 {
		_ = os.RemoveAll(l.Get(flags.UserDataDir))
		return
	}
	cleanupLaunchedBrowser(l)
}

func cleanupLaunchedBrowser(l interface {
	Kill()
	Cleanup()
}) {
	if l == nil {
		return
	}
	l.Kill()
	l.Cleanup()
}

// Acquire returns a page from the pool or creates a new one.
func (p *Pool) Acquire() (*rod.Page, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, errPoolClosed
	}

	select {
	case page := <-p.pages:
		p.mu.Unlock()
		return page, nil
	default:
	}
	browser := p.browser
	p.mu.Unlock()
	if browser == nil {
		return nil, errors.New("renderer browser is unavailable")
	}

	page, err := browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return nil, err
	}

	if p.opts.UserAgent != "" {
		err = page.SetUserAgent(&proto.NetworkSetUserAgentOverride{
			UserAgent: p.opts.UserAgent,
		})
		if err != nil {
			page.Close()
			return nil, err
		}
	}

	return page, nil
}

func (p *Pool) acquireOrigin(ctx context.Context, rawURL string) (func(), error) {
	key := renderOrigin(rawURL)
	if key == "" {
		return func() {}, nil
	}

	value, _ := p.originGates.LoadOrStore(key, make(chan struct{}, 1))
	gate := value.(chan struct{})
	select {
	case gate <- struct{}{}:
		return func() { <-gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func renderOrigin(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host)
}

// Release returns a page to the pool or closes it if the pool is full.
func (p *Pool) Release(page *rod.Page) {
	if page == nil {
		return
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = page.Close()
		return
	}
	p.mu.Unlock()

	// Navigate to blank to free memory before reuse
	_ = page.Navigate("about:blank")

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		_ = page.Close()
		return
	}

	select {
	case p.pages <- page:
	default:
		_ = page.Close()
	}
}

// Close shuts down the browser and all pages.
func (p *Pool) Close() {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		pages := p.drainIdlePagesLocked()
		browser := p.browser
		p.mu.Unlock()
		for _, page := range pages {
			_ = page.Close()
		}
		if browser != nil {
			if err := browser.Close(); err != nil && p.launcher != nil {
				p.launcher.Kill()
			}
		}

		p.cleanupOnce.Do(func() {
			if p.launcherCleanup != nil {
				p.launcherCleanup()
			} else if p.launcher != nil {
				p.launcher.Cleanup()
			}
		})
	})
}

func (p *Pool) drainIdlePagesLocked() []*rod.Page {
	var pages []*rod.Page
	for {
		select {
		case page, ok := <-p.pages:
			if !ok {
				return pages
			}
			if page != nil {
				pages = append(pages, page)
			}
		default:
			return pages
		}
	}
}
