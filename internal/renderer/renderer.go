package renderer

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// RenderResult holds the outcome of rendering a URL with Chrome.
type RenderResult struct {
	RenderedHTML   string
	RenderDuration time.Duration
	JSErrors       []string
	Error          error

	// Core Web Vitals
	CWVMeasured bool
	CWVLCP      float64 // Largest Contentful Paint (ms)
	CWVCLS      float64 // Cumulative Layout Shift
	CWVTTFB     float64 // Time to First Byte (ms)
	CWVError    string  // Non-fatal measurement failure; rendered HTML can still be used.
}

// Render navigates to the given URL in a headless Chrome page, waits for
// the page to stabilise, and returns the rendered HTML.
func (p *Pool) Render(ctx context.Context, url string) *RenderResult {
	start := time.Now()
	result := &RenderResult{}

	releaseOrigin, err := p.acquireOrigin(ctx, url)
	if err != nil {
		result.Error = fmt.Errorf("wait for origin render slot: %w", err)
		result.RenderDuration = time.Since(start)
		return result
	}
	defer releaseOrigin()

	renderCtx, renderCancel := context.WithTimeout(ctx, p.opts.PageTimeout)
	defer renderCancel()

	page, err := p.Acquire()
	if err != nil {
		result.Error = fmt.Errorf("acquire page: %w", err)
		result.RenderDuration = time.Since(start)
		return result
	}
	defer p.Release(page)

	page = page.Context(renderCtx)

	// Block heavy resources to speed up rendering
	if p.opts.BlockResources {
		router := page.HijackRequests()
		router.MustAdd("*", func(h *rod.Hijack) {
			resType := h.Request.Type()
			switch resType {
			case proto.NetworkResourceTypeImage,
				proto.NetworkResourceTypeFont,
				proto.NetworkResourceTypeMedia:
				h.Response.Fail(proto.NetworkErrorReasonBlockedByClient)
			default:
				h.ContinueRequest(&proto.FetchContinueRequest{})
			}
		})
		go router.Run()
		defer router.Stop()
	}

	// Collect JS console errors
	var jsErrors []string
	var jsErrorsMu sync.Mutex
	stopEvents := runEventConsumer(renderCtx, page, func(e *proto.RuntimeExceptionThrown) bool {
		if e.ExceptionDetails != nil && e.ExceptionDetails.Text != "" {
			jsErrorsMu.Lock()
			jsErrors = append(jsErrors, e.ExceptionDetails.Text)
			jsErrorsMu.Unlock()
		}
		return false // keep listening
	})
	defer stopEvents()

	// Navigate
	err = page.Navigate(url)
	if err != nil {
		result.Error = fmt.Errorf("navigate: %w", err)
		result.RenderDuration = time.Since(start)
		return result
	}

	// Wait for page load
	err = page.WaitLoad()
	if err != nil {
		result.Error = fmt.Errorf("wait load: %w", err)
		result.RenderDuration = time.Since(start)
		return result
	}

	// Give deferred SPA route work a minimum window, then require network and
	// DOM stability. Stability alone can return before a pending JS timer fires.
	time.Sleep(500 * time.Millisecond)
	_ = page.WaitStable(300 * time.Millisecond)

	// Extract rendered HTML
	html, err := page.HTML()
	if err != nil {
		result.Error = fmt.Errorf("extract html: %w", err)
		result.RenderDuration = time.Since(start)
		return result
	}

	result.RenderedHTML = html
	result.RenderDuration = time.Since(start)
	stopEvents()

	jsErrorsMu.Lock()
	result.JSErrors = jsErrors
	jsErrorsMu.Unlock()

	if strings.TrimSpace(html) == "" {
		result.Error = fmt.Errorf("empty rendered HTML")
	}

	return result
}

// RenderWithCWV is like Render but also measures Core Web Vitals (lab data).
// It does NOT block images (LCP depends on them) and uses the Chrome DevTools
// Protocol PerformanceTimeline events plus the Navigation Timing API:
// for reliable measurement:
//   - TTFB: from PerformanceNavigationTiming.responseStart
//   - LCP:  from PerformanceTimeline "largest-contentful-paint" events
//   - CLS:  from PerformanceTimeline "layout-shift" events
func (p *Pool) RenderWithCWV(ctx context.Context, url string) *RenderResult {
	start := time.Now()
	result := &RenderResult{}

	releaseOrigin, err := p.acquireOrigin(ctx, url)
	if err != nil {
		result.Error = fmt.Errorf("wait for origin render slot: %w", err)
		result.RenderDuration = time.Since(start)
		return result
	}
	defer releaseOrigin()

	renderCtx, renderCancel := context.WithTimeout(ctx, p.opts.PageTimeout)
	defer renderCancel()

	page, err := p.Acquire()
	if err != nil {
		result.Error = fmt.Errorf("acquire page: %w", err)
		result.RenderDuration = time.Since(start)
		return result
	}
	defer p.Release(page)

	page = page.Context(renderCtx)

	// Block fonts and media but NOT images (LCP needs images)
	if p.opts.BlockResources {
		router := page.HijackRequests()
		router.MustAdd("*", func(h *rod.Hijack) {
			resType := h.Request.Type()
			switch resType {
			case proto.NetworkResourceTypeFont,
				proto.NetworkResourceTypeMedia:
				h.Response.Fail(proto.NetworkErrorReasonBlockedByClient)
			default:
				h.ContinueRequest(&proto.FetchContinueRequest{})
			}
		})
		go router.Run()
		defer router.Stop()
	}

	// Enable PerformanceTimeline for LCP and CLS events (via CDP, not JS)
	var cwvErrors []string
	if err := (proto.PerformanceTimelineEnable{
		EventTypes: []string{"largest-contentful-paint", "layout-shift"},
	}).Call(page); err != nil {
		cwvErrors = append(cwvErrors, fmt.Sprintf("enable performance timeline: %v", err))
	}

	// Collect JS console errors + CWV timeline events
	var jsErrors []string
	var jsErrorsMu sync.Mutex
	var lcpMs float64
	var clsTotal float64
	var cwvMu sync.Mutex

	stopEvents := runEventConsumer(renderCtx, page,
		func(e *proto.RuntimeExceptionThrown) bool {
			if e.ExceptionDetails != nil && e.ExceptionDetails.Text != "" {
				jsErrorsMu.Lock()
				jsErrors = append(jsErrors, e.ExceptionDetails.Text)
				jsErrorsMu.Unlock()
			}
			return false
		},
		func(e *proto.PerformanceTimelineTimelineEventAdded) bool {
			ev := e.Event
			if ev == nil {
				return false
			}
			cwvMu.Lock()
			defer cwvMu.Unlock()
			if ev.LcpDetails != nil {
				// LCP: take the latest (largest) entry; Chrome may emit multiple.
				// Use RenderTime if available (more accurate), else LoadTime.
				ms := float64(ev.LcpDetails.RenderTime)
				if ms == 0 {
					ms = float64(ev.LcpDetails.LoadTime)
				}
				if ms > lcpMs {
					lcpMs = ms
				}
			}
			if ev.LayoutShiftDetails != nil && !ev.LayoutShiftDetails.HadRecentInput {
				clsTotal += ev.LayoutShiftDetails.Value
			}
			return false
		},
	)
	defer stopEvents()

	// Navigate
	err = page.Navigate(url)
	if err != nil {
		result.Error = fmt.Errorf("navigate: %w", err)
		result.RenderDuration = time.Since(start)
		return result
	}

	// Wait for page load
	err = page.WaitLoad()
	if err != nil {
		result.Error = fmt.Errorf("wait load: %w", err)
		result.RenderDuration = time.Since(start)
		return result
	}

	// Give deferred SPA route work a minimum window, then wait for stability.
	time.Sleep(500 * time.Millisecond)
	_ = page.WaitStable(300 * time.Millisecond)
	stopEvents()

	// TTFB: navigation timing API is reliable — use a single JS call.
	// (Unlike LCP/CLS, the Navigation Timing API is synchronous and complete.)
	var ttfbMs float64
	ttfbObj, evalErr := page.Eval(`() => {
		const navigation = performance.getEntriesByType('navigation')[0];
		return navigation ? navigation.responseStart : 0;
	}`)
	if evalErr == nil && ttfbObj != nil {
		ttfbMs = ttfbObj.Value.Num()
	} else if evalErr != nil {
		cwvErrors = append(cwvErrors, fmt.Sprintf("read TTFB: %v", evalErr))
	} else {
		cwvErrors = append(cwvErrors, "read TTFB: empty result")
	}

	// Collect LCP/CLS from CDP timeline events.
	cwvMu.Lock()
	finalLCP := lcpMs
	finalCLS := clsTotal
	cwvMu.Unlock()

	// CDP LcpDetails.RenderTime/LoadTime are TimeSinceEpoch (seconds since Unix epoch).
	// Convert to ms relative to navigation start (performance.timeOrigin).
	if finalLCP > 1e9 {
		originObj, originErr := page.Eval(`() => performance.timeOrigin`)
		if originErr == nil && originObj != nil {
			navOriginMs := originObj.Value.Num()
			if navOriginMs > 0 {
				finalLCP = (finalLCP * 1000) - navOriginMs
			} else {
				finalLCP = 0
				cwvErrors = append(cwvErrors, "read LCP: invalid navigation time origin")
			}
		} else if originErr != nil {
			finalLCP = 0
			cwvErrors = append(cwvErrors, fmt.Sprintf("read LCP time origin: %v", originErr))
		} else {
			finalLCP = 0
			cwvErrors = append(cwvErrors, "read LCP time origin: empty result")
		}
	}

	result.CWVLCP = finalLCP
	result.CWVCLS = finalCLS
	result.CWVTTFB = ttfbMs
	result.CWVMeasured = validCWVMeasurement(finalLCP, finalCLS, ttfbMs)
	if !result.CWVMeasured {
		if finalLCP <= 0 {
			cwvErrors = append(cwvErrors, "LCP was not observed")
		}
		if ttfbMs <= 0 {
			cwvErrors = append(cwvErrors, "TTFB was not observed")
		}
		if finalCLS < 0 || math.IsNaN(finalCLS) || math.IsInf(finalCLS, 0) {
			cwvErrors = append(cwvErrors, "CLS was invalid")
		}
	}
	result.CWVError = strings.Join(cwvErrors, "; ")

	// Extract rendered HTML
	html, err := page.HTML()
	if err != nil {
		result.Error = fmt.Errorf("extract html: %w", err)
		result.RenderDuration = time.Since(start)
		return result
	}

	result.RenderedHTML = html
	result.RenderDuration = time.Since(start)

	jsErrorsMu.Lock()
	result.JSErrors = jsErrors
	jsErrorsMu.Unlock()

	if strings.TrimSpace(html) == "" {
		result.Error = fmt.Errorf("empty rendered HTML")
	}

	return result
}

func validCWVMeasurement(lcpMs, cls, ttfbMs float64) bool {
	return lcpMs > 0 && ttfbMs > 0 && cls >= 0 &&
		!math.IsNaN(lcpMs) && !math.IsInf(lcpMs, 0) &&
		!math.IsNaN(cls) && !math.IsInf(cls, 0) &&
		!math.IsNaN(ttfbMs) && !math.IsInf(ttfbMs, 0)
}

func runEventConsumer(ctx context.Context, page *rod.Page, callbacks ...interface{}) func() {
	eventCtx, cancel := context.WithCancel(ctx)
	wait := page.Context(eventCtx).EachEvent(callbacks...)
	done := make(chan struct{})
	go func() {
		defer close(done)
		wait()
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}
