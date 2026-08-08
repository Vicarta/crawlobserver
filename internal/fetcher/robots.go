package fetcher

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/temoto/robotstxt"
)

// RobotsCacheEntry holds the raw robots.txt data for a host.
type RobotsCacheEntry struct {
	Content    string
	StatusCode int
	FetchedAt  time.Time
	parsed     *robotstxt.RobotsData
}

// RobotsCache caches robots.txt data per host.
type RobotsCache struct {
	mu        sync.RWMutex
	cache     map[string]*RobotsCacheEntry
	client    *http.Client
	userAgent string
}

// NewRobotsCache creates a new RobotsCache.
func NewRobotsCache(userAgent string, timeout time.Duration, dialOpts DialOptions, tlsProfile TLSProfile) *RobotsCache {
	dialFn := SafeDialContextWithOpts(dialOpts)
	transport := &http.Transport{
		DialContext: dialFn,
	}
	var rt http.RoundTripper = transport
	if tlsProfile != "" {
		rt = utlsTransport(tlsProfile, dialFn, transport)
	}
	return &RobotsCache{
		cache:     make(map[string]*RobotsCacheEntry),
		userAgent: userAgent,
		client: &http.Client{
			Timeout:   timeout,
			Transport: rt,
		},
	}
}

// IsAllowed checks if the given URL is allowed by robots.txt.
func (rc *RobotsCache) IsAllowed(targetURL string) bool {
	u, err := url.Parse(targetURL)
	if err != nil {
		return true // allow on parse error
	}

	host := u.Scheme + "://" + u.Host
	entry := rc.get(host)
	if entry == nil {
		entry = rc.fetch(host)
	}

	group := entry.parsed.FindGroup(rc.userAgent)
	return group.Test(u.Path)
}

// CrawlDelay returns the crawl-delay specified in robots.txt for the given URL's host.
// Returns 0 if no crawl-delay is specified.
func (rc *RobotsCache) CrawlDelay(targetURL string) time.Duration {
	u, err := url.Parse(targetURL)
	if err != nil {
		return 0
	}

	host := u.Scheme + "://" + u.Host
	entry := rc.get(host)
	if entry == nil {
		entry = rc.fetch(host)
	}

	group := entry.parsed.FindGroup(rc.userAgent)
	return group.CrawlDelay
}

// Entries returns a copy of all cached robots.txt entries.
func (rc *RobotsCache) Entries() map[string]*RobotsCacheEntry {
	rc.mu.RLock()
	defer rc.mu.RUnlock()

	result := make(map[string]*RobotsCacheEntry, len(rc.cache))
	for k, v := range rc.cache {
		result[k] = v
	}
	return result
}

func (rc *RobotsCache) get(host string) *RobotsCacheEntry {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	return rc.cache[host]
}

// DeclaredSitemapURLs returns only sitemap roots explicitly declared by
// robots.txt. It does not add conventional fallback paths.
func (rc *RobotsCache) DeclaredSitemapURLs() []string {
	rc.mu.RLock()
	defer rc.mu.RUnlock()

	seen := make(map[string]bool)
	var urls []string

	for _, host := range sortedRobotsHosts(rc.cache) {
		entry := rc.cache[host]
		if entry.parsed != nil {
			for _, s := range entry.parsed.Sitemaps {
				if !seen[s] {
					seen[s] = true
					urls = append(urls, s)
				}
			}
		}
	}
	return urls
}

// SitemapFallbackURLs returns conventional fallback paths for cached hosts.
// It intentionally excludes robots.txt declarations so strict sitemap refresh
// callers can decide when fallbacks are appropriate.
func (rc *RobotsCache) SitemapFallbackURLs() []string {
	rc.mu.RLock()
	defer rc.mu.RUnlock()

	seen := make(map[string]bool)
	var urls []string
	for _, host := range sortedRobotsHosts(rc.cache) {
		for _, path := range []string{"/sitemap.xml", "/sitemap_index.xml"} {
			u := host + path
			if !seen[u] {
				seen[u] = true
				urls = append(urls, u)
			}
		}
	}
	return urls
}

// SitemapURLs collects sitemap URLs from all cached robots.txt entries, plus
// common fallback paths (/sitemap.xml, /sitemap_index.xml). It preserves the
// broad discovery behavior used by full crawls.
func (rc *RobotsCache) SitemapURLs() []string {
	declared := rc.DeclaredSitemapURLs()
	fallbacks := rc.SitemapFallbackURLs()
	seen := make(map[string]bool, len(declared)+len(fallbacks))
	urls := make([]string, 0, len(declared)+len(fallbacks))
	for _, sitemapURL := range append(declared, fallbacks...) {
		if !seen[sitemapURL] {
			seen[sitemapURL] = true
			urls = append(urls, sitemapURL)
		}
	}
	return urls
}

func sortedRobotsHosts(cache map[string]*RobotsCacheEntry) []string {
	hosts := make([]string, 0, len(cache))
	for host := range cache {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return hosts
}

func (rc *RobotsCache) fetch(host string) *RobotsCacheEntry {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	// Double-check after acquiring write lock
	if entry, ok := rc.cache[host]; ok {
		return entry
	}

	now := time.Now()
	entry := &RobotsCacheEntry{
		FetchedAt: now,
		parsed:    &robotstxt.RobotsData{},
	}

	robotsURL := fmt.Sprintf("%s/robots.txt", host)
	req, err := http.NewRequest("GET", robotsURL, nil)
	if err != nil {
		rc.cache[host] = entry
		return entry
	}
	req.Header.Set("User-Agent", rc.userAgent)

	resp, err := rc.client.Do(req)
	if err != nil {
		rc.cache[host] = entry
		return entry
	}
	defer resp.Body.Close()

	entry.StatusCode = resp.StatusCode

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024)) // 512KB limit
	if err != nil || resp.StatusCode != 200 {
		rc.cache[host] = entry
		return entry
	}

	entry.Content = string(body)

	parsed, err := robotstxt.FromBytes(body)
	if err != nil {
		log.Printf("[robots] %s: malformed robots.txt: %v", host, err)
		parsed = &robotstxt.RobotsData{}
	}
	entry.parsed = parsed

	rc.cache[host] = entry
	return entry
}
