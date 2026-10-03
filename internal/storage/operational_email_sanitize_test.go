package storage

import (
	"strings"
	"testing"
)

func TestSanitizeOperationalEmailURLKeepsPathAndUsefulQuery(t *testing.T) {
	got, ok := SanitizeOperationalEmailURL("https://user:password@example.test/guides/features?lang=de&access_token=secret&category=seo#fragment")
	if !ok {
		t.Fatal("expected valid public URL")
	}
	if got != "https://example.test/guides/features?lang=de&access_token=%5BREDACTED%5D&category=seo" {
		t.Fatalf("sanitized URL = %q", got)
	}
}

func TestSanitizeOperationalEmailURLPreservesBalancedPunctuation(t *testing.T) {
	raw := "https://example.test/features/(matching-parentheses)?lang=en&filter=category!"
	got, ok := SanitizeOperationalEmailURL(raw)
	if !ok || got != raw {
		t.Fatalf("sanitized URL = %q, %v; want observed URL unchanged", got, ok)
	}
}

func TestSanitizeOperationalEmailURLRedactsCredentialsInNestedURLs(t *testing.T) {
	private := "https://example.test/page?next=https%3A%2F%2Fuser%3Aprivate-password%40other.test%2F&lang=en"
	got, ok := SanitizeOperationalEmailURL(private)
	if !ok {
		t.Fatal("expected valid public URL")
	}
	if got != "https://example.test/page?next=%5BREDACTED%5D&lang=en" {
		t.Fatalf("sanitized nested URL = %q", got)
	}

	nestedToken := "https://example.test/page?next=https%3A%2F%2Fother.test%2Fpath%3Faccess_token%3Dprivate-token&lang=en"
	got, ok = SanitizeOperationalEmailURL(nestedToken)
	if !ok || got != "https://example.test/page?next=%5BREDACTED%5D&lang=en" {
		t.Fatalf("sanitized nested token URL = %q, %v", got, ok)
	}

	public := "https://example.test/page?next=https%3A%2F%2Fother.test%2Fpublic%3Flang%3Den&lang=en"
	got, ok = SanitizeOperationalEmailURL(public)
	if !ok || got != public {
		t.Fatalf("ordinary nested public URL changed: %q, %v", got, ok)
	}
}

func TestSanitizeOperationalEmailReasonRedactsEmbeddedSensitiveURL(t *testing.T) {
	got := SanitizeOperationalEmailReason(`request failed for "https://user:password@example.test/retry?lang=en&token=secret" (context deadline exceeded)`)
	for _, secret := range []string{"user:password", "token=secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("reason leaked %q: %s", secret, got)
		}
	}
	for _, useful := range []string{"/retry", "lang=en", "context deadline exceeded"} {
		if !strings.Contains(got, useful) {
			t.Fatalf("reason lost %q: %s", useful, got)
		}
	}
}

func TestSanitizeOperationalEmailReasonKeepsApostropheInQuotedURL(t *testing.T) {
	got := SanitizeOperationalEmailReason(`Get "https://example.test/page?lang=O'Reilly&code=private-value": request failed`)
	if strings.Contains(got, "private-value") {
		t.Fatalf("reason leaked sensitive query value: %s", got)
	}
	for _, useful := range []string{"https://example.test/page?lang=O'Reilly&code=%5BREDACTED%5D", "request failed"} {
		if !strings.Contains(got, useful) {
			t.Fatalf("reason lost %q: %s", useful, got)
		}
	}
}

func TestSanitizeOperationalEmailReasonRedactsCredentialsInNestedURL(t *testing.T) {
	got := SanitizeOperationalEmailReason(`Get "https://example.test/fetch?next=https%3A%2F%2Fuser%3Aprivate-password%40other.test%2F&lang=en": request failed`)
	if strings.Contains(got, "private-password") {
		t.Fatalf("reason leaked nested URL credentials: %s", got)
	}
	for _, useful := range []string{"next=%5BREDACTED%5D&lang=en", "request failed"} {
		if !strings.Contains(got, useful) {
			t.Fatalf("reason lost %q: %s", useful, got)
		}
	}
}
