package parser

import (
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/SEObserver/crawlobserver/internal/htmlutil"
)

func extractTitle(doc *goquery.Document) string {
	return strings.TrimSpace(doc.Find("title").First().Text())
}

func extractCanonical(doc *goquery.Document) string {
	var canonical string
	doc.Find("link").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		if htmlutil.AttrTokenContains(s, "rel", "canonical") {
			canonical, _ = htmlutil.Attr(s, "href")
			return false
		}
		return true
	})
	return strings.TrimSpace(canonical)
}

func extractMetaContent(doc *goquery.Document, name string) string {
	var content string
	doc.Find("meta").Each(func(_ int, s *goquery.Selection) {
		n, _ := htmlutil.Attr(s, "name")
		if strings.EqualFold(n, name) {
			content, _ = htmlutil.Attr(s, "content")
		}
	})
	return strings.TrimSpace(content)
}

func extractHeadings(doc *goquery.Document, tag string) []string {
	var headings []string
	doc.Find(tag).Each(func(_ int, s *goquery.Selection) {
		text := strings.TrimSpace(s.Text())
		if text != "" {
			headings = append(headings, text)
		}
	})
	return headings
}

// extractHeadingOutline keeps headings in the order they appear in the DOM.
// Per-level arrays are still extracted for table views and issue checks, while
// this outline is used where the page structure itself matters.
func extractHeadingOutline(doc *goquery.Document) []Heading {
	outline := make([]Heading, 0)
	doc.Find("h1, h2, h3, h4, h5, h6").Each(func(_ int, s *goquery.Selection) {
		if len(s.Nodes) == 0 {
			return
		}

		tag := strings.ToLower(s.Nodes[0].Data)
		if len(tag) != 2 || tag[0] != 'h' || tag[1] < '1' || tag[1] > '6' {
			return
		}

		text := strings.TrimSpace(s.Text())
		if text == "" {
			return
		}
		outline = append(outline, Heading{
			Level: uint8(tag[1] - '0'),
			Text:  text,
		})
	})
	return outline
}
