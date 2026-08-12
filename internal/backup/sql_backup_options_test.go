package backup

import "testing"

func TestExcludesTableData(t *testing.T) {
	excluded := []string{"gsc_analytics", "provider_data"}

	if !excludesTableData(excluded, "gsc_analytics") {
		t.Fatal("gsc_analytics should be excluded")
	}
	if excludesTableData(excluded, "pages") {
		t.Fatal("pages should remain in the full backup")
	}
	if excludesTableData(excluded, "GSC_ANALYTICS") {
		t.Fatal("table matching must remain exact")
	}
}
