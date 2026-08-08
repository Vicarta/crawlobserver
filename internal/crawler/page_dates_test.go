package crawler

import "testing"

func TestLastModifiedFromHeaders(t *testing.T) {
	got := lastModifiedFromHeaders(map[string]string{
		"last-modified": "Wed, 09 Jul 2025 10:11:12 GMT",
	})
	if got == nil || got.Format("2006-01-02T15:04:05Z") != "2025-07-09T10:11:12Z" {
		t.Fatalf("lastModifiedFromHeaders() = %v", got)
	}

	if got := lastModifiedFromHeaders(map[string]string{"Last-Modified": "invalid"}); got != nil {
		t.Fatalf("lastModifiedFromHeaders() invalid = %v, want nil", got)
	}
}
