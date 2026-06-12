package parser

import (
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/SEObserver/crawlobserver/internal/htmlutil"
	"github.com/SEObserver/crawlobserver/internal/normalizer"
)

// PageResource represents a CSS, JS, font, icon, or image resource referenced by a page.
type PageResource struct {
	URL          string
	ResourceType string // "css", "js", "font", "icon", "image"
	IsInternal   bool
}

// ExtractResources extracts external resource references from the document.
func ExtractResources(doc *goquery.Document, baseURL *url.URL) []PageResource {
	seen := make(map[string]bool)
	var resources []PageResource

	add := func(href, resType string) {
		href = strings.TrimSpace(href)
		lower := strings.ToLower(href)
		if href == "" || strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "blob:") || strings.HasPrefix(lower, "javascript:") {
			return
		}
		resolved, err := normalizer.Resolve(baseURL.String(), href)
		if err != nil {
			return
		}
		key := resolved + "|" + resType
		if seen[key] {
			return
		}
		seen[key] = true
		resources = append(resources, PageResource{
			URL:          resolved,
			ResourceType: resType,
			IsInternal:   isInternal(baseURL, resolved),
		})
	}
	addSrcset := func(srcset string, resType string) {
		for _, candidate := range strings.Split(srcset, ",") {
			fields := strings.Fields(strings.TrimSpace(candidate))
			if len(fields) == 0 {
				continue
			}
			add(fields[0], resType)
		}
	}

	// <link> tags
	doc.Find("link").Each(func(_ int, s *goquery.Selection) {
		href, _ := htmlutil.Attr(s, "href")
		rel, _ := htmlutil.Attr(s, "rel")
		rel = strings.ToLower(strings.TrimSpace(rel))

		switch rel {
		case "stylesheet":
			add(href, "css")
		case "icon", "shortcut icon", "apple-touch-icon":
			add(href, "icon")
		case "preload":
			as, _ := htmlutil.Attr(s, "as")
			switch strings.ToLower(strings.TrimSpace(as)) {
			case "style":
				add(href, "css")
			case "script":
				add(href, "js")
			case "font":
				add(href, "font")
			case "image":
				add(href, "image")
			}
		}
	})

	// <script src> (external only)
	doc.Find("script").Each(func(_ int, s *goquery.Selection) {
		src, _ := htmlutil.Attr(s, "src")
		add(src, "js")
	})

	doc.Find("img").Each(func(_ int, s *goquery.Selection) {
		for _, attr := range []string{"src", "data-src", "data-original", "data-lazy-src"} {
			src, _ := htmlutil.Attr(s, attr)
			add(src, "image")
		}
		srcset, _ := htmlutil.Attr(s, "srcset")
		addSrcset(srcset, "image")
	})

	doc.Find("source").Each(func(_ int, s *goquery.Selection) {
		srcset, _ := htmlutil.Attr(s, "srcset")
		addSrcset(srcset, "image")
	})

	return resources
}
