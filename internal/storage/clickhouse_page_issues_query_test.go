package storage

import (
	"strings"
	"testing"
)

func TestPageIssuesQueryUsesStaticMetadataProvenance(t *testing.T) {
	required := []string{
		"SELECT static_title, static_meta_description",
		"GROUP BY static_title, static_meta_description",
		"WHERE (static_title, static_meta_description) IN",
	}
	for _, fragment := range required {
		if !strings.Contains(pageIssuesQuery, fragment) {
			t.Fatalf("page issues query is missing static provenance fragment %q", fragment)
		}
	}

	if strings.Contains(pageIssuesQuery, "GROUP BY title, meta_description") {
		t.Fatal("page issues query still derives static metadata issues from effective fields")
	}
}

func TestPageIssuesQueryDetectsSharedRenderedMetadataShell(t *testing.T) {
	required := []string{
		"shared_rendered_metadata_shells AS",
		"GROUP BY host, rendered_title, rendered_meta_description",
		"HAVING countDistinct(url) >= 3",
		"uniqExact(arrayStringConcat(rendered_h1",
		"static_title = rendered_title",
		"static_meta_description = rendered_meta_description",
		"js_render_error = ''",
		"countIf(js_changed_h1) >= 2",
		"countIf(js_changed_content) >= 2",
		"'shared_rendered_metadata_shell' AS issue_type",
		"AND url NOT IN (SELECT url FROM shared_shell_urls)",
	}
	for _, fragment := range required {
		if !strings.Contains(pageIssuesQuery, fragment) {
			t.Fatalf("page issues query is missing shared-shell fragment %q", fragment)
		}
	}

	if strings.Contains(pageIssuesQuery, "base AS (\n\t\tSELECT\n\t\t\ta.url") {
		t.Fatal("page issues base CTE references alias a before it is declared")
	}
}
