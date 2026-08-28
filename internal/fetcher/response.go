package fetcher

import (
	"time"
)

// RedirectHop represents a single hop in a redirect chain.
type RedirectHop struct {
	URL        string
	StatusCode int
}

// RequestValidators contains the retained HTTP validators for an optional
// conditional GET. Values are copied exactly to the request headers.
type RequestValidators struct {
	ETag         string
	LastModified string
}

// FetchResult contains the result of fetching a URL.
type FetchResult struct {
	URL           string
	FinalURL      string
	StatusCode    int
	ContentType   string
	Headers       map[string]string
	Body          []byte
	BodySize      int64
	BodyTruncated bool
	RedirectChain []RedirectHop
	Duration      time.Duration
	Error         string
	Depth         int
	FoundOn       string
	Attempt       int // retry attempt number (0 = first try)
	NotModified   bool
}
