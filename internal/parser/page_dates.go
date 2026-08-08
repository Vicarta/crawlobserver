package parser

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/SEObserver/crawlobserver/internal/htmlutil"
)

var pageDateLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05-0700",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

func extractPageDates(doc *goquery.Document, jsonLDBlocks []string) (*time.Time, *time.Time) {
	published := extractJSONLDDate(jsonLDBlocks, "datePublished")
	modified := extractJSONLDDate(jsonLDBlocks, "dateModified")

	if published == nil {
		published = extractMetaDate(doc, "article:published_time", "datePublished")
	}
	if modified == nil {
		modified = extractMetaDate(doc, "article:modified_time", "og:updated_time", "dateModified")
	}
	return published, modified
}

func extractJSONLDDate(blocks []string, key string) *time.Time {
	for _, block := range blocks {
		var value any
		if err := json.Unmarshal([]byte(block), &value); err != nil {
			continue
		}
		if date := findJSONLDDate(value, key); date != nil {
			return date
		}
	}
	return nil
}

func findJSONLDDate(value any, key string) *time.Time {
	switch typed := value.(type) {
	case map[string]any:
		for candidateKey, candidateValue := range typed {
			if strings.EqualFold(candidateKey, key) {
				if date := dateFromJSONValue(candidateValue); date != nil {
					return date
				}
			}
		}
		keys := make([]string, 0, len(typed))
		for candidateKey := range typed {
			keys = append(keys, candidateKey)
		}
		sort.Strings(keys)
		for _, candidateKey := range keys {
			if date := findJSONLDDate(typed[candidateKey], key); date != nil {
				return date
			}
		}
	case []any:
		for _, item := range typed {
			if date := findJSONLDDate(item, key); date != nil {
				return date
			}
		}
	}
	return nil
}

func dateFromJSONValue(value any) *time.Time {
	switch typed := value.(type) {
	case string:
		return parsePageDate(typed)
	case []any:
		for _, item := range typed {
			if date := dateFromJSONValue(item); date != nil {
				return date
			}
		}
	}
	return nil
}

func extractMetaDate(doc *goquery.Document, names ...string) *time.Time {
	for _, name := range names {
		var date *time.Time
		doc.Find("meta").EachWithBreak(func(_ int, selection *goquery.Selection) bool {
			for _, attrName := range []string{"property", "name", "itemprop"} {
				attrValue, _ := htmlutil.Attr(selection, attrName)
				if !strings.EqualFold(strings.TrimSpace(attrValue), name) {
					continue
				}
				content, _ := htmlutil.Attr(selection, "content")
				date = parsePageDate(content)
				if date != nil {
					return false
				}
			}
			return true
		})
		if date != nil {
			return date
		}
	}
	return nil
}

func parsePageDate(value string) *time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	for _, layout := range pageDateLayouts {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			utc := parsed.UTC()
			return &utc
		}
	}
	return nil
}
