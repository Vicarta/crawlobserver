package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	Crawler       CrawlerConfig       `mapstructure:"crawler"`
	ClickHouse    ClickHouseConfig    `mapstructure:"clickhouse"`
	Storage       StorageConfig       `mapstructure:"storage"`
	Resources     ResourcesConfig     `mapstructure:"resources"`
	Server        ServerConfig        `mapstructure:"server"`
	Theme         ThemeConfig         `mapstructure:"theme"`
	GSC           GSCConfig           `mapstructure:"gsc"`
	Interlinking  InterlinkingConfig  `mapstructure:"interlinking"`
	Backup        BackupConfig        `mapstructure:"backup"`
	Retention     RetentionConfig     `mapstructure:"retention"`
	Telemetry     TelemetryConfig     `mapstructure:"telemetry"`
	Announcements AnnouncementsConfig `mapstructure:"announcements"`
	SetupComplete bool                `mapstructure:"setup_complete"`
}

// AnnouncementsConfig controls the optional in-app announcement banner.
// When enabled, the backend periodically fetches a JSON feed from FeedURL and
// exposes the latest message to the frontend. A user can opt out at any time.
type AnnouncementsConfig struct {
	Enabled      bool          `mapstructure:"enabled"`
	FeedURL      string        `mapstructure:"feed_url"`
	PollInterval time.Duration `mapstructure:"poll_interval"`
}

type TelemetryConfig struct {
	Enabled          bool   `mapstructure:"enabled"`
	InstanceID       string `mapstructure:"instance_id"`
	AskedAt          string `mapstructure:"asked_at"`          // ISO timestamp when user was asked about telemetry
	SessionRecording bool   `mapstructure:"session_recording"` // WARNING: records full browser sessions — all page content, URLs, and clicks are sent to PostHog
}

type CrawlerConfig struct {
	Workers               int              `mapstructure:"workers"`
	Delay                 time.Duration    `mapstructure:"delay"`
	MaxPages              int              `mapstructure:"max_pages"`
	DeltaPlannedPages     int              `mapstructure:"delta_planned_pages"`
	DiscoveryBudget       *int             `mapstructure:"discovery_budget"`
	DeltaPlan             *DeltaPlanConfig `mapstructure:"delta_plan"`
	MaxDepth              int              `mapstructure:"max_depth"`
	Timeout               time.Duration    `mapstructure:"timeout"`
	UserAgent             string           `mapstructure:"user_agent"`
	MaxBodySize           int64            `mapstructure:"max_body_size"`
	RespectRobots         bool             `mapstructure:"respect_robots"`
	StoreHTML             bool             `mapstructure:"store_html"`
	CrawlScope            string           `mapstructure:"crawl_scope"`             // "host" (default), "domain" (eTLD+1), or "subdirectory"
	AllowPrivateIPs       bool             `mapstructure:"allow_private_ips"`       // allow crawling private/reserved IPs (default: false)
	TLSProfile            string           `mapstructure:"tls_profile"`             // "", "chrome", "firefox", "edge"
	SourceIP              string           `mapstructure:"source_ip"`               // local IP to bind outgoing connections
	ForceIPv4             bool             `mapstructure:"force_ipv4"`              // force IPv4-only DNS and connections
	MaxConcurrentSessions int              `mapstructure:"max_concurrent_sessions"` // 0 = 20
	MaxFrontierSize       int              `mapstructure:"max_frontier_size"`       // 0 = 5_000_000
	MaxWorkers            int              `mapstructure:"max_workers"`             // 0 = 100
	ExcludePatterns       []string         `mapstructure:"exclude_patterns"`        // URL substrings to exclude from crawl (links still recorded)
	CheckExternalLinks    *bool            `mapstructure:"check_external_links"`
	ExternalLinkWorkers   int              `mapstructure:"external_link_workers"`
	CrawlSitemapOnly      bool             `mapstructure:"crawl_sitemap_only"`
	FetchSitemaps         *bool            `mapstructure:"fetch_sitemaps"`
	CheckPageResources    *bool            `mapstructure:"check_page_resources"`
	ResourceWorkers       int              `mapstructure:"resource_workers"`
	FollowJSLinks         bool             `mapstructure:"follow_js_links"`
	SitemapURLs           []string         `json:"sitemap_urls,omitempty" mapstructure:"sitemap_urls"`
	MeasureCWV            bool             `mapstructure:"measure_cwv"`
	ExtractorSetID        string           `mapstructure:"extractor_set_id"`
	Retry                 RetryConfig      `mapstructure:"retry"`
	JSRender              JSRenderConfig   `mapstructure:"js_render"`
	Cloudflare            CloudflareConfig `mapstructure:"cloudflare"`
}

type DeltaPlanConfig struct {
	BaselineSessionID                   string                 `json:"baseline_session_id" mapstructure:"baseline_session_id"`
	ConditionalRequestBaselineSessionID string                 `json:"conditional_request_baseline_session_id,omitempty" mapstructure:"conditional_request_baseline_session_id"`
	UseConditionalRequests              bool                   `json:"use_conditional_requests" mapstructure:"use_conditional_requests"`
	BaselineSourceSessionID             string                 `json:"baseline_source_session_id" mapstructure:"baseline_source_session_id"`
	BaselineEvaluationRevision          string                 `json:"baseline_evaluation_revision" mapstructure:"baseline_evaluation_revision"`
	BaselineSourceEvaluationRevision    string                 `json:"baseline_source_evaluation_revision" mapstructure:"baseline_source_evaluation_revision"`
	BaselineSnapshotRevision            uint64                 `json:"baseline_snapshot_revision" mapstructure:"baseline_snapshot_revision"`
	BaselineContentWatermarkSessionID   string                 `json:"baseline_content_watermark_session_id" mapstructure:"baseline_content_watermark_session_id"`
	TotalCandidates                     int                    `json:"total_candidates" mapstructure:"total_candidates"`
	LaunchedCandidates                  int                    `json:"launched_candidates" mapstructure:"launched_candidates"`
	DeferredCandidates                  int                    `json:"deferred_candidates" mapstructure:"deferred_candidates"`
	LaunchLimit                         int                    `json:"launch_limit" mapstructure:"launch_limit"`
	SourceCounts                        map[string]int         `json:"source_counts" mapstructure:"source_counts"`
	BaselineSitemapURLCount             int                    `json:"baseline_sitemap_url_count" mapstructure:"baseline_sitemap_url_count"`
	LaunchedURLs                        []string               `json:"launched_urls,omitempty" mapstructure:"launched_urls"`
	CandidateSources                    map[string][]string    `json:"candidate_sources,omitempty" mapstructure:"candidate_sources"`
	SitemapRefresh                      *DeltaSitemapRefresh   `json:"sitemap_refresh,omitempty" mapstructure:"sitemap_refresh"`
	SitemapSelection                    *DeltaSitemapSelection `json:"sitemap_selection,omitempty" mapstructure:"sitemap_selection"`
}

// DeltaSitemapSelection records the immutable bounded changed-only selection
// inputs and result used by a launched Delta. A nil value explicitly denotes a
// legacy session created before selector provenance was persisted.
type DeltaSitemapSelection struct {
	SelectorRevision                   string            `json:"selector_revision" mapstructure:"selector_revision"`
	RawObservationSessionID            string            `json:"raw_observation_session_id,omitempty" mapstructure:"raw_observation_session_id"`
	RawObservedAt                      time.Time         `json:"raw_observed_at,omitempty" mapstructure:"raw_observed_at"`
	PublishedSessionID                 string            `json:"published_session_id,omitempty" mapstructure:"published_session_id"`
	PublishedSnapshotRevision          uint64            `json:"published_snapshot_revision" mapstructure:"published_snapshot_revision"`
	PublishedContentWatermarkSessionID string            `json:"published_content_watermark_session_id,omitempty" mapstructure:"published_content_watermark_session_id"`
	RotationEpoch                      time.Time         `json:"rotation_epoch,omitempty" mapstructure:"rotation_epoch"`
	EventTotal                         int               `json:"event_total" mapstructure:"event_total"`
	EventSelected                      int               `json:"event_selected" mapstructure:"event_selected"`
	EventDeferred                      int               `json:"event_deferred" mapstructure:"event_deferred"`
	PublishedDifferenceTotal           int               `json:"published_difference_total,omitempty" mapstructure:"published_difference_total"`
	ActionableTotal                    int               `json:"actionable_total,omitempty" mapstructure:"actionable_total"`
	StableAcknowledgedTotal            int               `json:"stable_acknowledged_total,omitempty" mapstructure:"stable_acknowledged_total"`
	SelectedTotal                      int               `json:"selected_total,omitempty" mapstructure:"selected_total"`
	CanarySelected                     int               `json:"canary_selected" mapstructure:"canary_selected"`
	SelectionComplete                  bool              `json:"selection_complete" mapstructure:"selection_complete"`
	PublicationHeld                    bool              `json:"publication_held,omitempty" mapstructure:"publication_held"`
	StabilityOlderSessionID            string            `json:"stability_older_session_id,omitempty" mapstructure:"stability_older_session_id"`
	StabilityNewerSessionID            string            `json:"stability_newer_session_id,omitempty" mapstructure:"stability_newer_session_id"`
	StabilityProofDigest               string            `json:"stability_proof_digest,omitempty" mapstructure:"stability_proof_digest"`
	StabilityLegacyCompletePair        bool              `json:"stability_legacy_complete_pair,omitempty" mapstructure:"stability_legacy_complete_pair"`
	SourceByURL                        map[string]string `json:"source_by_url,omitempty" mapstructure:"source_by_url"`
}

// DeltaSitemapRefresh records the sitemap provenance used to build a Delta
// plan. A nil SitemapRefresh keeps sessions created before sitemap refresh
// support backward compatible.
type DeltaSitemapRefresh struct {
	Mode                string                    `json:"mode" mapstructure:"mode"`
	FetchedAt           time.Time                 `json:"fetched_at" mapstructure:"fetched_at"`
	DeclaredSitemapURLs []string                  `json:"declared_sitemap_urls,omitempty" mapstructure:"declared_sitemap_urls"`
	FetchedSitemapURLs  []string                  `json:"fetched_sitemap_urls,omitempty" mapstructure:"fetched_sitemap_urls"`
	FreshURLCount       int                       `json:"fresh_url_count" mapstructure:"fresh_url_count"`
	RawURLRowCount      int                       `json:"raw_url_row_count,omitempty" mapstructure:"raw_url_row_count"`
	SnapshotURLCount    int                       `json:"snapshot_url_count" mapstructure:"snapshot_url_count"`
	AddedCount          int                       `json:"added_count" mapstructure:"added_count"`
	RemovedCount        int                       `json:"removed_count" mapstructure:"removed_count"`
	InvalidEntryCount   int                       `json:"invalid_entry_count" mapstructure:"invalid_entry_count"`
	Warnings            []string                  `json:"warnings,omitempty" mapstructure:"warnings"`
	RawEvidence         []DeltaSitemapEvidenceRef `json:"raw_evidence,omitempty" mapstructure:"raw_evidence"`
}

// DeltaSitemapEvidenceLimit bounds plan metadata. Full raw sitemap evidence
// remains in sitemap rows; this list only keeps representative references.
const DeltaSitemapEvidenceLimit = 100

// DeltaSitemapEvidenceRef points from plan metadata to raw sitemap evidence.
type DeltaSitemapEvidenceRef struct {
	SitemapURL string `json:"sitemap_url" mapstructure:"sitemap_url"`
	RawLoc     string `json:"raw_loc,omitempty" mapstructure:"raw_loc"`
}

type JSRenderConfig struct {
	Mode           string        `mapstructure:"mode"`            // "off" (default), "auto", "always"
	MaxPages       int           `mapstructure:"max_pages"`       // concurrent Chrome pages (default: 4)
	PageTimeout    time.Duration `mapstructure:"page_timeout"`    // per-page timeout (default: 15s)
	BlockResources bool          `mapstructure:"block_resources"` // block images/fonts (default: true)
}

type CloudflareConfig struct {
	Enabled      bool          `mapstructure:"enabled"`
	Resolver     string        `mapstructure:"resolver"` // "none" (default) or "api"
	APIURL       string        `mapstructure:"api_url"`  // external solver API endpoint
	APIKey       string        `mapstructure:"api_key"`  // Bearer token for the API
	SolveTimeout time.Duration `mapstructure:"solve_timeout"`
	MaxHoldURLs  int           `mapstructure:"max_hold_urls"`
}

type RetryConfig struct {
	MaxRetries          int           `mapstructure:"max_retries"`
	BaseDelay           time.Duration `mapstructure:"base_delay"`
	MaxDelay            time.Duration `mapstructure:"max_delay"`
	MaxConsecutiveFails int           `mapstructure:"max_consecutive_fails"`
	MaxGlobalErrorRate  float64       `mapstructure:"max_global_error_rate"`
}

type ClickHouseConfig struct {
	Host       string `mapstructure:"host"`
	Port       int    `mapstructure:"port"`
	HTTPPort   int    `mapstructure:"http_port"` // HTTP interface port for backups, 0 = port - 1000
	Database   string `mapstructure:"database"`
	Username   string `mapstructure:"username"`
	Password   string `mapstructure:"password"`
	Mode       string `mapstructure:"mode"`        // "managed" | "external" | "" (auto-detect)
	BinaryPath string `mapstructure:"binary_path"` // path to clickhouse binary, "" = auto-detect
	DataDir    string `mapstructure:"data_dir"`    // data directory, "" = platform default
}

// EffectiveHTTPPort returns the HTTP port, deriving it from the native port if not set.
// Convention: native 9000 → HTTP 8123, native 19000 → HTTP 18123.
// The offset between native and HTTP is always 877 (9000 - 8123).
func (c ClickHouseConfig) EffectiveHTTPPort() int {
	if c.HTTPPort > 0 {
		return c.HTTPPort
	}
	return c.Port - 877 // 9000→8123, 19000→18123
}

// DSN returns a redacted connection string safe for logging.
func (c ClickHouseConfig) DSN() string {
	pw := "***"
	if c.Password == "" {
		pw = ""
	}
	return fmt.Sprintf("clickhouse://%s:%s@%s:%d/%s",
		c.Username, pw, c.Host, c.Port, c.Database)
}

type StorageConfig struct {
	BatchSize     int           `mapstructure:"batch_size"`
	FlushInterval time.Duration `mapstructure:"flush_interval"`
}

type ResourcesConfig struct {
	MaxMemoryMB int `mapstructure:"max_memory_mb"` // soft limit, 0 = auto (75% of system RAM)
	MaxCPU      int `mapstructure:"max_cpu"`       // GOMAXPROCS, 0 = all available
}

type ServerConfig struct {
	Host              string          `mapstructure:"host"`
	Port              int             `mapstructure:"port"`
	PublicURL         string          `mapstructure:"public_url"`
	Username          string          `mapstructure:"username"`
	Password          string          `mapstructure:"password"`
	SQLitePath        string          `mapstructure:"sqlite_path"`
	RateLimit         RateLimitConfig `mapstructure:"rate_limit"`
	PasswordGenerated bool            `mapstructure:"-"` // transient, not persisted
	WeakPassword      bool            `mapstructure:"-"` // transient, not persisted
}

type RateLimitConfig struct {
	Enabled            bool    `mapstructure:"enabled"`
	RequestsPerSecond  float64 `mapstructure:"requests_per_second"`
	Burst              int     `mapstructure:"burst"`
	AuthRequestsPerMin int     `mapstructure:"auth_requests_per_minute"`
}

type ThemeConfig struct {
	AppName     string `mapstructure:"app_name" json:"app_name"`
	LogoURL     string `mapstructure:"logo_url" json:"logo_url"`
	AccentColor string `mapstructure:"accent_color" json:"accent_color"`
	Mode        string `mapstructure:"mode" json:"mode"` // "light" or "dark"
}

type InterlinkingConfig struct {
	SimilarityThreshold float64          `mapstructure:"similarity_threshold"`
	MaxOpportunities    int              `mapstructure:"max_opportunities"`
	Embeddings          EmbeddingsConfig `mapstructure:"embeddings"`
}

type EmbeddingsConfig struct {
	Provider  string `mapstructure:"provider"`
	APIKey    string `mapstructure:"api_key"`
	Model     string `mapstructure:"model"`
	BatchSize int    `mapstructure:"batch_size"`
}

type GSCConfig struct {
	ClientID     string `mapstructure:"client_id"`
	ClientSecret string `mapstructure:"client_secret"`
	RedirectURI  string `mapstructure:"redirect_uri"`
}

type BackupConfig struct {
	Enabled  bool   `mapstructure:"enabled"`
	Interval string `mapstructure:"interval"` // duration string: "12h", "24h"
	Time     string `mapstructure:"time"`     // optional daily wall-clock time: "HH:MM"
	Timezone string `mapstructure:"timezone"` // optional IANA timezone for Time
	Dir      string `mapstructure:"dir"`      // backup directory, "" = <dataDir>/backups
	Retain   int    `mapstructure:"retain"`   // number of backups to keep
}

type RetentionConfig struct {
	SessionsPerProject int    `mapstructure:"sessions_per_project"` // 0 = disabled
	Interval           string `mapstructure:"interval"`             // duration string; default 15m when sessions_per_project > 0
}

func SetDefaults() {
	viper.SetDefault("crawler.workers", 10)
	viper.SetDefault("crawler.delay", "1s")
	viper.SetDefault("crawler.max_pages", 0)
	viper.SetDefault("crawler.max_depth", 0)
	viper.SetDefault("crawler.timeout", "30s")
	viper.SetDefault("crawler.user_agent", "CrawlObserver/1.0")
	viper.SetDefault("crawler.max_body_size", 10*1024*1024) // 10MB
	viper.SetDefault("crawler.respect_robots", true)
	viper.SetDefault("crawler.store_html", false)
	viper.SetDefault("crawler.crawl_scope", "host")
	viper.SetDefault("crawler.allow_private_ips", false)
	viper.SetDefault("crawler.max_concurrent_sessions", 20)
	viper.SetDefault("crawler.max_frontier_size", 5000000)
	viper.SetDefault("crawler.max_workers", 100)
	viper.SetDefault("crawler.retry.max_retries", 3)
	viper.SetDefault("crawler.retry.base_delay", "2s")
	viper.SetDefault("crawler.retry.max_delay", "60s")
	viper.SetDefault("crawler.retry.max_consecutive_fails", 10)
	viper.SetDefault("crawler.retry.max_global_error_rate", 0.8)
	viper.SetDefault("crawler.js_render.mode", "off")
	viper.SetDefault("crawler.js_render.max_pages", 4)
	viper.SetDefault("crawler.js_render.page_timeout", "15s")
	viper.SetDefault("crawler.js_render.block_resources", true)
	viper.SetDefault("crawler.cloudflare.enabled", true)
	viper.SetDefault("crawler.cloudflare.resolver", "none")
	viper.SetDefault("crawler.cloudflare.api_url", "")
	viper.SetDefault("crawler.cloudflare.api_key", "")
	viper.SetDefault("crawler.cloudflare.solve_timeout", "30s")
	viper.SetDefault("crawler.cloudflare.max_hold_urls", 1000)

	viper.SetDefault("clickhouse.host", "localhost")
	viper.SetDefault("clickhouse.port", 19000)
	viper.SetDefault("clickhouse.database", "crawlobserver")
	viper.SetDefault("clickhouse.username", "default")
	viper.SetDefault("clickhouse.password", "")
	viper.SetDefault("clickhouse.http_port", 0)
	viper.SetDefault("clickhouse.mode", "")
	viper.SetDefault("clickhouse.binary_path", "")
	viper.SetDefault("clickhouse.data_dir", "")

	viper.SetDefault("storage.batch_size", 1000)
	viper.SetDefault("storage.flush_interval", "5s")

	viper.SetDefault("resources.max_memory_mb", 0) // auto
	viper.SetDefault("resources.max_cpu", 0)       // all available

	viper.SetDefault("server.host", "127.0.0.1")
	viper.SetDefault("server.port", 8899)
	viper.SetDefault("server.public_url", "")
	viper.SetDefault("server.username", "admin")
	viper.SetDefault("server.password", "")
	viper.SetDefault("server.sqlite_path", "crawlobserver.db")
	viper.SetDefault("server.rate_limit.enabled", false)
	viper.SetDefault("server.rate_limit.requests_per_second", 10)
	viper.SetDefault("server.rate_limit.burst", 20)
	viper.SetDefault("server.rate_limit.auth_requests_per_minute", 20)

	viper.SetDefault("theme.app_name", "CrawlObserver")
	viper.SetDefault("theme.logo_url", "")
	viper.SetDefault("theme.accent_color", "#7c3aed")
	viper.SetDefault("theme.mode", "light")

	viper.SetDefault("interlinking.similarity_threshold", 0.3)
	viper.SetDefault("interlinking.max_opportunities", 1000)
	viper.SetDefault("interlinking.embeddings.provider", "")
	viper.SetDefault("interlinking.embeddings.api_key", "")
	viper.SetDefault("interlinking.embeddings.model", "text-embedding-3-small")
	viper.SetDefault("interlinking.embeddings.batch_size", 100)

	viper.SetDefault("gsc.client_id", "")
	viper.SetDefault("gsc.client_secret", "")
	viper.SetDefault("gsc.redirect_uri", "http://127.0.0.1:8899/api/gsc/callback")

	viper.SetDefault("backup.enabled", true)
	viper.SetDefault("backup.interval", "24h")
	viper.SetDefault("backup.time", "")
	viper.SetDefault("backup.timezone", "")
	viper.SetDefault("backup.dir", "")
	viper.SetDefault("backup.retain", 2)

	viper.SetDefault("retention.sessions_per_project", 0)
	viper.SetDefault("retention.interval", "15m")

	viper.SetDefault("telemetry.enabled", false)
	viper.SetDefault("telemetry.instance_id", "")
	viper.SetDefault("telemetry.asked_at", "")
	viper.SetDefault("telemetry.session_recording", false)

	viper.SetDefault("announcements.enabled", true)
	viper.SetDefault("announcements.feed_url", "https://crawlobserver.com/announcements/feed.json")
	viper.SetDefault("announcements.poll_interval", "10m")

	viper.SetDefault("setup_complete", false)

	bindEnvironment()
}

func bindEnvironment() {
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	// Keep the deployed Docker path simple: these short names can live in
	// deploy/.env and be passed into the app container by Compose.
	_ = viper.BindEnv("gsc.client_id", "GSC_CLIENT_ID", "CRAWLOBSERVER_GSC_CLIENT_ID")
	_ = viper.BindEnv("gsc.client_secret", "GSC_CLIENT_SECRET", "CRAWLOBSERVER_GSC_CLIENT_SECRET")
	_ = viper.BindEnv("gsc.redirect_uri", "GSC_REDIRECT_URI", "CRAWLOBSERVER_GSC_REDIRECT_URI")
}

func Load() (*Config, error) {
	SetDefaults()

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshalling config: %w", err)
	}

	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	// Generate random password if username is set but password is empty, persist it
	if cfg.Server.Username != "" && cfg.Server.Password == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return nil, fmt.Errorf("generating random password: %w", err)
		}
		cfg.Server.Password = hex.EncodeToString(b)
		cfg.Server.PasswordGenerated = true
		viper.Set("server.password", cfg.Server.Password)
		_ = WriteConfig()
	}

	// Resolve relative SQLite path to a stable location so that all modes
	// (serve, crawl, gui) use the same database regardless of the working directory.
	if !filepath.IsAbs(cfg.Server.SQLitePath) {
		origName := cfg.Server.SQLitePath
		dataDir, err := DefaultDataDir()
		if err == nil {
			_ = os.MkdirAll(dataDir, 0755)
			cfg.Server.SQLitePath = filepath.Join(dataDir, origName)

			// Migrate legacy SQLite from old locations (pre-v1.1 stored it in cwd or next to config).
			migrateLegacySQLite(cfg.Server.SQLitePath, origName)
		}
	}

	// Flag weak password when exposed on all interfaces
	if cfg.Server.Host == "0.0.0.0" && isWeakPassword(cfg.Server.Password) {
		cfg.Server.WeakPassword = true
	}

	// Existing user upgrade: if config file existed BEFORE this Load() call
	// with real content but no setup_complete key, auto-set setup_complete to true
	// so they skip the full onboarding (they'll still get the telemetry opt-in).
	// This check must run BEFORE instance_id generation, which creates the file on fresh installs.
	if !cfg.SetupComplete && viper.ConfigFileUsed() != "" {
		if info, err := os.Stat(viper.ConfigFileUsed()); err == nil && info.Size() > 0 {
			if !viper.IsSet("setup_complete") {
				cfg.SetupComplete = true
				viper.Set("setup_complete", true)
				_ = WriteConfig()
			}
		}
	}

	// Generate instance_id if not set
	if cfg.Telemetry.InstanceID == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return nil, fmt.Errorf("generating instance_id: %w", err)
		}
		// Format as UUID v4
		b[6] = (b[6] & 0x0f) | 0x40
		b[8] = (b[8] & 0x3f) | 0x80
		cfg.Telemetry.InstanceID = fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
		viper.Set("telemetry.instance_id", cfg.Telemetry.InstanceID)
		_ = WriteConfig()
	}

	return &cfg, nil
}

// WriteConfig writes the current viper config to disk, creating it if needed.
func WriteConfig() error {
	if err := viper.WriteConfig(); err != nil {
		return viper.SafeWriteConfig()
	}
	return nil
}

// isWeakPassword checks if a password is too simple for internet-exposed deployments.
func isWeakPassword(password string) bool {
	if len(password) < 8 {
		return true
	}
	weak := []string{
		"password", "12345678", "123456789", "1234567890",
		"crawlobserver", "admin123", "changeme",
		"qwerty123", "letmein", "welcome1",
	}
	lower := strings.ToLower(password)
	for _, w := range weak {
		if lower == w {
			return true
		}
	}
	return false
}

// DefaultDataDir returns the platform-specific application data directory.
// macOS: ~/Library/Application Support/CrawlObserver
// Linux: ~/.local/share/crawlobserver
// Windows: %APPDATA%/CrawlObserver
func DefaultDataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "CrawlObserver"), nil
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "CrawlObserver"), nil
		}
		return filepath.Join(home, "AppData", "Roaming", "CrawlObserver"), nil
	default:
		return filepath.Join(home, ".local", "share", "crawlobserver"), nil
	}
}

// WriterStateDir resolves the directory shared by state-mutating commands
// without creating files or changing configuration. Callers use it to take the
// process writer lock before Load performs first-run persistence or legacy
// SQLite recovery.
func WriterStateDir() (string, error) {
	sqlitePath := viper.GetString("server.sqlite_path")
	if sqlitePath == "" {
		sqlitePath = "crawlobserver.db"
	}
	if filepath.IsAbs(sqlitePath) {
		return filepath.Dir(sqlitePath), nil
	}
	dataDir, err := DefaultDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Dir(filepath.Join(dataDir, sqlitePath)), nil
}

func validate(cfg *Config) error {
	if cfg.Crawler.Workers < 1 {
		return fmt.Errorf("crawler.workers must be >= 1")
	}
	if cfg.Crawler.Delay < 0 {
		return fmt.Errorf("crawler.delay must be >= 0")
	}
	if cfg.Crawler.Timeout <= 0 {
		return fmt.Errorf("crawler.timeout must be > 0")
	}
	if cfg.Crawler.MaxBodySize <= 0 {
		return fmt.Errorf("crawler.max_body_size must be > 0")
	}
	if cfg.Crawler.UserAgent == "" {
		return fmt.Errorf("crawler.user_agent must not be empty")
	}
	// Skip host/port validation when managed mode (ports assigned dynamically)
	if cfg.ClickHouse.Mode != "managed" {
		if cfg.ClickHouse.Host == "" {
			return fmt.Errorf("clickhouse.host must not be empty")
		}
		if cfg.ClickHouse.Port <= 0 || cfg.ClickHouse.Port > 65535 {
			return fmt.Errorf("clickhouse.port must be 1-65535")
		}
	}
	if cfg.Crawler.MaxConcurrentSessions < 0 {
		return fmt.Errorf("crawler.max_concurrent_sessions must be >= 0")
	}
	if cfg.Crawler.MaxFrontierSize < 0 {
		return fmt.Errorf("crawler.max_frontier_size must be >= 0")
	}
	if cfg.Crawler.MaxWorkers < 0 {
		return fmt.Errorf("crawler.max_workers must be >= 0")
	}
	if cfg.Crawler.Retry.MaxRetries < 0 {
		return fmt.Errorf("crawler.retry.max_retries must be >= 0")
	}
	if cfg.Crawler.Retry.MaxRetries > 0 {
		if cfg.Crawler.Retry.BaseDelay <= 0 {
			return fmt.Errorf("crawler.retry.base_delay must be > 0 when retries enabled")
		}
		if cfg.Crawler.Retry.MaxDelay < cfg.Crawler.Retry.BaseDelay {
			return fmt.Errorf("crawler.retry.max_delay must be >= base_delay")
		}
	}
	switch cfg.Crawler.Cloudflare.Resolver {
	case "", "none", "api":
	default:
		return fmt.Errorf("crawler.cloudflare.resolver must be \"none\" or \"api\"")
	}
	if cfg.Crawler.Cloudflare.Resolver == "api" && cfg.Crawler.Cloudflare.APIURL == "" {
		return fmt.Errorf("crawler.cloudflare.api_url is required when resolver is \"api\"")
	}
	if cfg.Storage.BatchSize < 1 {
		return fmt.Errorf("storage.batch_size must be >= 1")
	}
	if cfg.Storage.FlushInterval <= 0 {
		return fmt.Errorf("storage.flush_interval must be > 0")
	}
	if cfg.Backup.Enabled {
		interval, err := time.ParseDuration(cfg.Backup.Interval)
		if err != nil || interval < time.Hour {
			return fmt.Errorf("backup.interval must be a valid duration >= 1h")
		}
		if cfg.Backup.Time != "" {
			parsed, err := time.Parse("15:04", cfg.Backup.Time)
			if err != nil || parsed.Format("15:04") != cfg.Backup.Time {
				return fmt.Errorf("backup.time must be a valid 24-hour HH:MM value")
			}
		}
		if cfg.Backup.Timezone != "" {
			if _, err := time.LoadLocation(cfg.Backup.Timezone); err != nil {
				return fmt.Errorf("backup.timezone must be a valid IANA timezone: %w", err)
			}
		}
		if cfg.Backup.Retain < 1 {
			return fmt.Errorf("backup.retain must be >= 1")
		}
	}
	return nil
}
