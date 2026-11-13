package parser

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseLinkPosition(t *testing.T) {
	body := []byte(`<!doctype html><html><body>
		<nav><ul><li><a href="/one">One</a></li><li><a href="/two">Two</a></li></ul></nav>
		<div role="main"><a href="/main">Main</a></div>
		<a href="mailto:skip@example.com">Skipped</a>
		<a href="/plain">Plain</a>
	</body></html>`)
	data, err := ParseWithOptions(body, "https://example.com/", ParseOptions{LinkPosition: true})
	if err != nil {
		t.Fatalf("ParseWithOptions() error = %v", err)
	}
	if len(data.Links) != 4 {
		t.Fatalf("links = %d, want 4 successfully extracted links", len(data.Links))
	}

	want := map[string]struct {
		landmark string
		xpath    string
		depth    uint16
		index    uint32
	}{
		"One":   {"nav", "/html/body/nav/ul/li[1]/a", 5, 0},
		"Two":   {"nav", "/html/body/nav/ul/li[2]/a", 5, 1},
		"Main":  {"main", "/html/body/div/a", 3, 2},
		"Plain": {"", "/html/body/a[2]", 2, 3},
	}
	for _, link := range data.Links {
		expected, ok := want[link.AnchorText]
		if !ok {
			continue
		}
		if link.Landmark != expected.landmark || link.XPath != expected.xpath || link.Depth != expected.depth || link.DocumentIndex != expected.index {
			t.Errorf("position for %q = {%q %q %d %d}, want {%q %q %d %d}",
				link.AnchorText, link.Landmark, link.XPath, link.Depth, link.DocumentIndex,
				expected.landmark, expected.xpath, expected.depth, expected.index)
		}
		if link.BlockSignature == 0 {
			t.Errorf("BlockSignature for %q is zero", link.AnchorText)
		}
	}
	if data.Links[0].BlockSignature != data.Links[1].BlockSignature {
		t.Errorf("same menu block signatures differ: %d vs %d", data.Links[0].BlockSignature, data.Links[1].BlockSignature)
	}
	if data.Links[0].BlockSignature == data.Links[2].BlockSignature {
		t.Error("different blocks should have different signatures")
	}
}

func TestParseLinkPositionDisabledByDefault(t *testing.T) {
	data, err := Parse([]byte(`<html><body><nav><a href="/next">Next</a></nav></body></html>`), "https://example.com/")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(data.Links) != 1 || data.Links[0].TargetURL != "https://example.com/next" {
		t.Fatalf("link extraction changed with position disabled: %+v", data.Links)
	}
	link := data.Links[0]
	if link.Landmark != "" || link.XPath != "" || link.Depth != 0 || link.DocumentIndex != 0 || link.BlockSignature != 0 {
		t.Fatalf("position metadata unexpectedly enabled by default: %+v", link)
	}
}

func TestParseLinkPositionLimitLeavesLinksAvailable(t *testing.T) {
	var markup strings.Builder
	markup.WriteString("<html><body>")
	for i := 0; i <= DefaultMaxLinkPositions; i++ {
		fmt.Fprintf(&markup, `<a href="/link-%d">Link %d</a>`, i, i)
	}
	markup.WriteString("</body></html>")

	data, err := ParseWithOptions([]byte(markup.String()), "https://example.com/", ParseOptions{LinkPosition: true})
	if err != nil {
		t.Fatalf("ParseWithOptions() error = %v", err)
	}
	if len(data.Links) != DefaultMaxLinkPositions+1 {
		t.Fatalf("links = %d, want all %d links", len(data.Links), DefaultMaxLinkPositions+1)
	}
	if got := data.Links[DefaultMaxLinkPositions-1].XPath; got == "" {
		t.Error("link at the position limit has no XPath")
	}
	last := data.Links[DefaultMaxLinkPositions]
	if last.TargetURL != "https://example.com/link-1000" {
		t.Errorf("last link target = %q, want the final extracted link", last.TargetURL)
	}
	if last.Landmark != "" || last.XPath != "" || last.Depth != 0 || last.DocumentIndex != 0 || last.BlockSignature != 0 {
		t.Errorf("position metadata was calculated past the limit: %+v", last)
	}
}

func TestParseLinkPositionBoundsKeepLinks(t *testing.T) {
	longTag := "custom" + strings.Repeat("x", maxLinkXPathBytes)
	deepMarkup := strings.Builder{}
	deepMarkup.WriteString("<html><body>")
	for i := 0; i < maxLinkPositionAncestors; i++ {
		deepMarkup.WriteString("<div>")
	}
	deepMarkup.WriteString(`<a href="/deep">Deep</a>`)
	for i := 0; i < maxLinkPositionAncestors; i++ {
		deepMarkup.WriteString("</div>")
	}
	deepMarkup.WriteString("</body></html>")
	largeClass := strings.Repeat("x ", maxPositionAttrBytes/2+1)

	tests := []struct {
		name   string
		markup string
	}{
		{
			name:   "oversized custom ancestor",
			markup: `<html><body><` + longTag + `><a href="/long">Long</a></` + longTag + `></body></html>`,
		},
		{name: "excessive ancestry", markup: deepMarkup.String()},
		{
			name:   "oversized block class",
			markup: `<html><body><div class="` + largeClass + `"><a href="/class">Class</a></div></body></html>`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := ParseWithOptions([]byte(tt.markup), "https://example.com/", ParseOptions{LinkPosition: true})
			if err != nil {
				t.Fatalf("ParseWithOptions() error = %v", err)
			}
			if len(data.Links) != 1 || data.Links[0].TargetURL == "" {
				t.Fatalf("link extraction changed for oversized position data: %+v", data.Links)
			}
			link := data.Links[0]
			if link.Landmark != "" || link.XPath != "" || link.Depth != 0 || link.DocumentIndex != 0 || link.BlockSignature != 0 {
				t.Errorf("oversized position metadata was retained: %+v", link)
			}
		})
	}
}

func TestValidXPathNameRejectsUnsupportedNames(t *testing.T) {
	for _, name := range []string{"svg:path", "bad name", "1custom", "na\u00efve"} {
		if validXPathName(name) {
			t.Errorf("validXPathName(%q) = true, want false", name)
		}
	}
	for _, name := range []string{"html", "custom-element", "_component2"} {
		if !validXPathName(name) {
			t.Errorf("validXPathName(%q) = false, want true", name)
		}
	}
}
