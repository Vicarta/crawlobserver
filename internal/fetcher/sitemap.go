package fetcher

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/SEObserver/crawlobserver/internal/applog"
)

const maxSitemapSize = 10 * 1024 * 1024 // 10MB
const maxTotalSitemaps = 50

// SitemapFailureKind classifies a failed sitemap request or traversal.
// The values are stable so callers can make refresh decisions without
// matching error text.
type SitemapFailureKind string

const (
	SitemapFailureRequest   SitemapFailureKind = "request"
	SitemapFailureNetwork   SitemapFailureKind = "network"
	SitemapFailureTimeout   SitemapFailureKind = "timeout"
	SitemapFailureContext   SitemapFailureKind = "context"
	SitemapFailureHTTP      SitemapFailureKind = "http"
	SitemapFailureRead      SitemapFailureKind = "read"
	SitemapFailureXML       SitemapFailureKind = "xml"
	SitemapFailureChild     SitemapFailureKind = "child"
	SitemapFailureTruncated SitemapFailureKind = "truncated"
	SitemapFailureNoRoots   SitemapFailureKind = "no_roots"
)

// SitemapFailure is structured failure evidence for a sitemap document or
// the overall traversal. Child failures retain their underlying document
// failure in Cause.
type SitemapFailure struct {
	Kind       SitemapFailureKind `json:"kind"`
	URL        string             `json:"url,omitempty"`
	StatusCode int                `json:"status_code,omitempty"`
	Message    string             `json:"message"`
	Cause      *SitemapFailure    `json:"cause,omitempty"`
}

// SitemapURL represents a single URL entry in a sitemap.
type SitemapURL struct {
	Loc        string `xml:"loc"`
	RawLoc     string `xml:"-"`
	LastMod    string `xml:"lastmod"`
	ChangeFreq string `xml:"changefreq"`
	Priority   string `xml:"priority"`
}

// SitemapChild preserves the raw index <loc> while exposing the transport URL
// used for the sequential child traversal.
type SitemapChild struct {
	URL    string
	RawLoc string
}

// SitemapEntry represents a fetched sitemap (index or urlset).
type SitemapEntry struct {
	URL           string
	ParentURL     string
	Type          string // "index" or "urlset"
	StatusCode    int
	URLs          []SitemapURL
	Sitemaps      []string // child sitemap URLs if index
	RawSitemaps   []string // raw <loc> evidence for index children
	ChildSitemaps []SitemapChild
	Complete      bool
	Failure       *SitemapFailure
}

// SitemapObservation is a bounded, sequential sitemap traversal. Complete is
// true for a valid traversal even when the discovered urlsets contain no URLs.
// A non-nil Failure means the observation must not be treated as fresh data.
type SitemapObservation struct {
	FetchedAt     time.Time
	Entries       []SitemapEntry
	AttemptedURLs []string
	URLCount      int
	ValidEmpty    bool
	Complete      bool
	Failure       *SitemapFailure
}

// XML structures for parsing

type xmlSitemapIndex struct {
	XMLName  xml.Name        `xml:"sitemapindex"`
	Sitemaps []xmlSitemapLoc `xml:"sitemap"`
}

type xmlSitemapLoc struct {
	Loc string `xml:"loc"`
}

type xmlURLSet struct {
	XMLName xml.Name      `xml:"urlset"`
	URLs    []xmlURLEntry `xml:"url"`
}

type xmlURLEntry struct {
	Loc        string `xml:"loc"`
	LastMod    string `xml:"lastmod"`
	ChangeFreq string `xml:"changefreq"`
	Priority   string `xml:"priority"`
}

// FetchSitemap fetches and parses a single sitemap URL.
//
// It preserves the legacy return shape for broad crawler discovery. New
// planning code should use ObserveSitemaps when it needs to distinguish a
// complete observation from a failed or truncated one.
func FetchSitemap(ctx context.Context, client *http.Client, sitemapURL, userAgent string) SitemapEntry {
	return fetchSitemap(ctx, client, sitemapURL, userAgent)
}

func fetchSitemap(ctx context.Context, client *http.Client, sitemapURL, userAgent string) SitemapEntry {
	entry := SitemapEntry{URL: sitemapURL}

	req, err := http.NewRequestWithContext(ctx, "GET", sitemapURL, nil)
	if err != nil {
		entry.Failure = &SitemapFailure{
			Kind:    SitemapFailureRequest,
			URL:     sitemapURL,
			Message: err.Error(),
		}
		return entry
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		entry.Failure = sitemapFailureForError(sitemapURL, err)
		return entry
	}
	defer resp.Body.Close()

	entry.StatusCode = resp.StatusCode
	if resp.StatusCode != 200 {
		entry.Failure = &SitemapFailure{
			Kind:       SitemapFailureHTTP,
			URL:        sitemapURL,
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("unexpected HTTP status %d", resp.StatusCode),
		}
		return entry
	}

	// Read one byte past the limit so a complete observation can distinguish a
	// bounded document from one that was cut off before XML parsing.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSitemapSize+1))
	if err != nil {
		entry.Failure = sitemapFailureForReadError(sitemapURL, err)
		return entry
	}
	if len(body) > maxSitemapSize {
		entry.Failure = &SitemapFailure{
			Kind:    SitemapFailureTruncated,
			URL:     sitemapURL,
			Message: fmt.Sprintf("sitemap exceeds %d byte limit", maxSitemapSize),
		}
		return entry
	}

	typeName, err := sitemapType(body)
	if err != nil {
		entry.Failure = &SitemapFailure{
			Kind:    SitemapFailureXML,
			URL:     sitemapURL,
			Message: err.Error(),
		}
		return entry
	}

	switch typeName {
	case "index":
		entry.Type = "index"
		var idx xmlSitemapIndex
		if err := xml.Unmarshal(body, &idx); err != nil {
			entry.Type = ""
			entry.Failure = &SitemapFailure{
				Kind:    SitemapFailureXML,
				URL:     sitemapURL,
				Message: err.Error(),
			}
			return entry
		}
		for _, s := range idx.Sitemaps {
			rawLoc := s.Loc
			entry.RawSitemaps = append(entry.RawSitemaps, rawLoc)

			// Only trim surrounding XML formatting for the request URL. RawLoc
			// retains the original value, including any literal path whitespace.
			childURL := strings.TrimSpace(rawLoc)
			entry.ChildSitemaps = append(entry.ChildSitemaps, SitemapChild{
				URL:    childURL,
				RawLoc: rawLoc,
			})
			if childURL != "" {
				entry.Sitemaps = append(entry.Sitemaps, childURL)
			}
		}
	case "urlset":
		entry.Type = "urlset"
		var urlset xmlURLSet
		if err := xml.Unmarshal(body, &urlset); err != nil {
			entry.Type = ""
			entry.Failure = &SitemapFailure{
				Kind:    SitemapFailureXML,
				URL:     sitemapURL,
				Message: err.Error(),
			}
			return entry
		}
		for _, u := range urlset.URLs {
			// Loc is intentionally raw evidence. URL normalization belongs to the
			// caller and must not rewrite literal path content here.
			entry.URLs = append(entry.URLs, SitemapURL{
				Loc:        u.Loc,
				RawLoc:     u.Loc,
				LastMod:    strings.TrimSpace(u.LastMod),
				ChangeFreq: strings.TrimSpace(u.ChangeFreq),
				Priority:   strings.TrimSpace(u.Priority),
			})
		}
	}

	entry.Complete = true
	return entry
}

func sitemapType(body []byte) (string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(body))
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return "", fmt.Errorf("empty sitemap XML document")
		}
		if err != nil {
			return "", err
		}

		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}

		switch start.Name.Local {
		case "sitemapindex":
			return "index", nil
		case "urlset":
			return "urlset", nil
		default:
			return "", fmt.Errorf("unsupported sitemap root element %q", start.Name.Local)
		}
	}
}

func sitemapFailureForError(sitemapURL string, err error) *SitemapFailure {
	failure := &SitemapFailure{
		Kind:    SitemapFailureNetwork,
		URL:     sitemapURL,
		Message: err.Error(),
	}

	switch {
	case errors.Is(err, context.Canceled):
		failure.Kind = SitemapFailureContext
	case errors.Is(err, context.DeadlineExceeded):
		failure.Kind = SitemapFailureTimeout
	default:
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			failure.Kind = SitemapFailureTimeout
		}
	}

	return failure
}

func sitemapFailureForReadError(sitemapURL string, err error) *SitemapFailure {
	failure := sitemapFailureForError(sitemapURL, err)
	if failure.Kind == SitemapFailureNetwork {
		failure.Kind = SitemapFailureRead
	}
	return failure
}

// ObserveSitemaps fetches a bounded sitemap tree sequentially. Unlike the
// compatibility discovery API below, it records enough status to reject a
// partial tree as a fresh sitemap observation.
func ObserveSitemaps(ctx context.Context, client *http.Client, userAgent string, sitemapURLs []string) SitemapObservation {
	observation := SitemapObservation{FetchedAt: time.Now().UTC()}
	type queuedSitemap struct {
		url       string
		parentURL string
	}

	seen := make(map[string]bool)
	queue := make([]queuedSitemap, 0, len(sitemapURLs))
	setFailure := func(failure *SitemapFailure) {
		if observation.Failure == nil {
			observation.Failure = failure
		}
	}
	enqueue := func(sitemapURL, parentURL string) {
		url := strings.TrimSpace(sitemapURL)
		if url == "" {
			setFailure(&SitemapFailure{
				Kind:    SitemapFailureChild,
				URL:     parentURL,
				Message: "sitemap index contains an empty child loc",
				Cause: &SitemapFailure{
					Kind:    SitemapFailureRequest,
					Message: "empty sitemap URL",
				},
			})
			return
		}
		if seen[url] {
			return
		}
		if len(observation.Entries)+len(queue) >= maxTotalSitemaps {
			setFailure(&SitemapFailure{
				Kind:    SitemapFailureTruncated,
				URL:     url,
				Message: fmt.Sprintf("sitemap traversal reached %d document limit", maxTotalSitemaps),
			})
			return
		}

		seen[url] = true
		queue = append(queue, queuedSitemap{url: url, parentURL: parentURL})
	}

	for _, sitemapURL := range sitemapURLs {
		enqueue(sitemapURL, "")
	}
	if len(queue) == 0 && observation.Failure == nil {
		observation.Failure = &SitemapFailure{
			Kind:    SitemapFailureNoRoots,
			Message: "no sitemap URLs provided",
		}
	}

	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			setFailure(sitemapFailureForError("", err))
			break
		}

		current := queue[0]
		queue = queue[1:]
		observation.AttemptedURLs = append(observation.AttemptedURLs, current.url)

		entry := fetchSitemap(ctx, client, current.url, userAgent)
		entry.ParentURL = current.parentURL
		observation.Entries = append(observation.Entries, entry)
		if !entry.Complete {
			if current.parentURL == "" {
				setFailure(entry.Failure)
			} else {
				setFailure(&SitemapFailure{
					Kind:    SitemapFailureChild,
					URL:     entry.URL,
					Message: "required sitemap index child could not be fetched or parsed",
					Cause:   entry.Failure,
				})
			}
			continue
		}

		observation.URLCount += len(entry.URLs)
		if entry.Type != "index" {
			continue
		}
		for _, child := range entry.ChildSitemaps {
			enqueue(child.URL, entry.URL)
		}
	}

	observation.Complete = observation.Failure == nil
	observation.ValidEmpty = observation.Complete && observation.URLCount == 0
	return observation
}

// DiscoverSitemaps fetches all given sitemap URLs, recursing into indexes.
// Returns at most maxTotalSitemaps entries.
func DiscoverSitemaps(ctx context.Context, client *http.Client, userAgent string, sitemapURLs []string) []SitemapEntry {
	var results []SitemapEntry
	seen := make(map[string]bool)

	var queue []string
	for _, u := range sitemapURLs {
		if !seen[u] {
			seen[u] = true
			queue = append(queue, u)
		}
	}

	for len(queue) > 0 && len(results) < maxTotalSitemaps {
		if ctx.Err() != nil {
			applog.Infof("fetcher", "Sitemap discovery cancelled")
			break
		}

		url := queue[0]
		queue = queue[1:]

		applog.Infof("fetcher", "Fetching sitemap: %s", url)
		entry := FetchSitemap(ctx, client, url, userAgent)
		results = append(results, entry)

		// If it's an index, enqueue children
		if entry.Type == "index" {
			for _, child := range entry.Sitemaps {
				if !seen[child] && len(results)+len(queue) < maxTotalSitemaps {
					seen[child] = true
					queue = append(queue, child)
				}
			}
		}
	}

	return results
}
