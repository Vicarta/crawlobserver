package fetcher

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchWithContextValidators(t *testing.T) {
	tests := []struct {
		name       string
		validators RequestValidators
		status     int
		wantETag   string
		wantMod    string
	}{
		{name: "etag only", validators: RequestValidators{ETag: `W/"current"`}, status: http.StatusNotModified, wantETag: `W/"current"`},
		{name: "last modified only", validators: RequestValidators{LastModified: "Wed, 21 Oct 2015 07:28:00 GMT"}, status: http.StatusNotModified, wantMod: "Wed, 21 Oct 2015 07:28:00 GMT"},
		{name: "both exact", validators: RequestValidators{ETag: `"strong"`, LastModified: "Tue, 20 Oct 2015 07:28:00 GMT"}, status: http.StatusNotModified, wantETag: `"strong"`, wantMod: "Tue, 20 Oct 2015 07:28:00 GMT"},
		{name: "no validators stays unconditional", status: http.StatusOK},
		{name: "server may return 200", validators: RequestValidators{ETag: `"old"`}, status: http.StatusOK, wantETag: `"old"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("If-None-Match"); got != tt.wantETag {
					t.Errorf("If-None-Match = %q, want %q", got, tt.wantETag)
				}
				if got := r.Header.Get("If-Modified-Since"); got != tt.wantMod {
					t.Errorf("If-Modified-Since = %q, want %q", got, tt.wantMod)
				}
				w.Header().Set("ETag", `"response"`)
				w.WriteHeader(tt.status)
				if tt.status == http.StatusOK {
					_, _ = w.Write([]byte("<html><body>updated</body></html>"))
				}
			}))
			defer server.Close()

			f := New("TestBot/1.0", time.Second, 1024, DialOptions{AllowPrivateIPs: true}, "")
			got := f.FetchWithContextValidators(context.Background(), server.URL, 2, "https://example.test/source", tt.validators)
			if got.StatusCode != tt.status || got.NotModified != (tt.status == http.StatusNotModified) {
				t.Fatalf("result status/notModified = %d/%t", got.StatusCode, got.NotModified)
			}
		})
	}
}

func TestFetchWithContextValidatorsSurviveRedirectAndCancellation(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, server.URL+"/final", http.StatusFound)
			return
		}
		if got := r.Header.Get("If-None-Match"); got != `W/"retained"` {
			t.Errorf("redirected validator = %q", got)
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()

	f := New("TestBot/1.0", time.Second, 1024, DialOptions{AllowPrivateIPs: true}, "")
	got := f.FetchWithContextValidators(context.Background(), server.URL+"/start", 0, "", RequestValidators{ETag: `W/"retained"`})
	if got.StatusCode != http.StatusNotModified || len(got.RedirectChain) != 1 {
		t.Fatalf("redirected result = %#v", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got = f.FetchWithContextValidators(ctx, server.URL, 0, "", RequestValidators{ETag: `"cancel"`})
	if got.Error == "" {
		t.Fatal("cancelled request unexpectedly succeeded")
	}
}

func TestFetchBasic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); ua != "TestBot/1.0" {
			t.Errorf("unexpected User-Agent: %s", ua)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Custom", "test-value")
		fmt.Fprint(w, "<html><head><title>Test</title></head><body>Hello</body></html>")
	}))
	defer server.Close()

	f := New("TestBot/1.0", 10*time.Second, 10*1024*1024, DialOptions{AllowPrivateIPs: true}, "")
	result := f.Fetch(server.URL, 0, "")

	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.StatusCode != 200 {
		t.Errorf("expected status 200, got %d", result.StatusCode)
	}
	if !strings.Contains(result.ContentType, "text/html") {
		t.Errorf("expected text/html content type, got %s", result.ContentType)
	}
	if !result.IsHTML() {
		t.Error("expected IsHTML() to be true")
	}
	if result.Headers["X-Custom"] != "test-value" {
		t.Errorf("expected X-Custom header, got %s", result.Headers["X-Custom"])
	}
	if !strings.Contains(string(result.Body), "<title>Test</title>") {
		t.Error("expected body to contain title")
	}
	if result.Duration <= 0 {
		t.Error("expected positive duration")
	}
}

func TestFetchRedirectChain(t *testing.T) {
	mux := http.NewServeMux()
	var server *httptest.Server
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, server.URL+"/middle", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/middle", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, server.URL+"/end", http.StatusFound)
	})
	mux.HandleFunc("/end", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><body>Final</body></html>")
	})
	server = httptest.NewServer(mux)
	defer server.Close()

	f := New("TestBot/1.0", 10*time.Second, 10*1024*1024, DialOptions{AllowPrivateIPs: true}, "")
	result := f.Fetch(server.URL+"/start", 0, "")

	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.StatusCode != 200 {
		t.Errorf("expected status 200, got %d", result.StatusCode)
	}
	if !strings.HasSuffix(result.FinalURL, "/end") {
		t.Errorf("expected final URL to end with /end, got %s", result.FinalURL)
	}
	if len(result.RedirectChain) != 2 {
		t.Errorf("expected 2 redirect hops, got %d", len(result.RedirectChain))
	}
}

func TestFetchBodySizeLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		// Write 2KB of data
		fmt.Fprint(w, strings.Repeat("x", 2048))
	}))
	defer server.Close()

	// Limit to 1KB
	f := New("TestBot/1.0", 10*time.Second, 1024, DialOptions{AllowPrivateIPs: true}, "")
	result := f.Fetch(server.URL, 0, "")

	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.BodySize != 1024 {
		t.Errorf("expected body size 1024, got %d", result.BodySize)
	}
}

func TestFetchTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		fmt.Fprint(w, "slow")
	}))
	defer server.Close()

	f := New("TestBot/1.0", 100*time.Millisecond, 10*1024*1024, DialOptions{AllowPrivateIPs: true}, "")
	result := f.Fetch(server.URL, 0, "")

	if result.Error == "" {
		t.Error("expected timeout error")
	}
}

func TestFetch404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, "not found")
	}))
	defer server.Close()

	f := New("TestBot/1.0", 10*time.Second, 10*1024*1024, DialOptions{AllowPrivateIPs: true}, "")
	result := f.Fetch(server.URL+"/missing", 0, "")

	if result.StatusCode != 404 {
		t.Errorf("expected status 404, got %d", result.StatusCode)
	}
}
