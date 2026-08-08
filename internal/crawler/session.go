package crawler

import (
	"time"

	"github.com/SEObserver/crawlobserver/internal/config"
	"github.com/SEObserver/crawlobserver/internal/storage"
	"github.com/google/uuid"
)

// Session represents a single crawl session lifecycle.
type Session struct {
	ID        string
	StartedAt time.Time
	SeedURLs  []string
	Config    *config.Config
	Status    string
	Pages     uint64
	ProjectID *string
	Label     string
	Stop      config.SessionStopMetadata
}

// NewSession creates a new crawl session.
func NewSession(seeds []string, cfg *config.Config) *Session {
	return &Session{
		ID:        uuid.New().String(),
		StartedAt: time.Now(),
		SeedURLs:  seeds,
		Config:    cfg,
		Status:    "running",
	}
}

// ToStorageRow converts a Session to a storage model.
func (s *Session) ToStorageRow() *storage.CrawlSession {
	configJSON, _ := config.SessionConfigJSON(s.Config)
	if s.Stop.Reason != "" || s.Stop.Message != "" {
		configJSON = config.WithSessionStopMetadata(configJSON, s.Stop)
	}
	return &storage.CrawlSession{
		ID:           s.ID,
		StartedAt:    s.StartedAt,
		Status:       s.Status,
		SeedURLs:     s.SeedURLs,
		Config:       configJSON,
		PagesCrawled: s.Pages,
		UserAgent:    s.Config.Crawler.UserAgent,
		ProjectID:    s.ProjectID,
		Label:        s.Label,
	}
}
