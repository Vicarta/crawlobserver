package crawler

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/SEObserver/crawlobserver/internal/config"
)

func TestSessionToStorageRowRedactsSensitiveConfig(t *testing.T) {
	cfg := &config.Config{
		Crawler: config.CrawlerConfig{
			Workers:   4,
			UserAgent: "SafeBot/1.0",
			Cloudflare: config.CloudflareConfig{
				APIKey: "cloudflare-api-key-secret",
			},
		},
		Server: config.ServerConfig{
			Username: "admin",
			Password: "app-password-secret",
		},
		ClickHouse: config.ClickHouseConfig{
			Password: "clickhouse-password-secret",
		},
		GSC: config.GSCConfig{
			ClientSecret: "gsc-client-secret",
		},
		Interlinking: config.InterlinkingConfig{
			Embeddings: config.EmbeddingsConfig{
				APIKey: "embedding-api-key-secret",
			},
		},
	}

	row := NewSession([]string{"https://example.com"}, cfg).ToStorageRow()

	for _, forbidden := range []string{
		"app-password-secret",
		"clickhouse-password-secret",
		"gsc-client-secret",
		"cloudflare-api-key-secret",
		"embedding-api-key-secret",
		"Password",
		"ClientSecret",
		"APIKey",
	} {
		if strings.Contains(row.Config, forbidden) {
			t.Fatalf("session config leaked %q: %s", forbidden, row.Config)
		}
	}

	var decoded struct {
		Crawler config.CrawlerConfig
	}
	if err := json.Unmarshal([]byte(row.Config), &decoded); err != nil {
		t.Fatalf("unmarshaling session config: %v", err)
	}
	if decoded.Crawler.UserAgent != "SafeBot/1.0" {
		t.Fatalf("Crawler.UserAgent = %q, want SafeBot/1.0", decoded.Crawler.UserAgent)
	}
	if decoded.Crawler.Workers != 4 {
		t.Fatalf("Crawler.Workers = %d, want 4", decoded.Crawler.Workers)
	}
}

func TestSessionToStorageRowIncludesTerminalFailureTime(t *testing.T) {
	finishedAt := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	session := NewSession([]string{"https://example.com"}, &config.Config{})
	session.Status = "failed"
	session.FinishedAt = finishedAt

	row := session.ToStorageRow()
	if row.Status != "failed" || !row.FinishedAt.Equal(finishedAt) {
		t.Fatalf("terminal row = status %q, finished_at %v; want failed at %v", row.Status, row.FinishedAt, finishedAt)
	}
}
