package apikeys

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/SEObserver/crawlobserver/internal/applog"
	"github.com/SEObserver/crawlobserver/internal/customtests"
	"github.com/SEObserver/crawlobserver/internal/extraction"
	"github.com/SEObserver/crawlobserver/internal/providers"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type ProjectDeltaSettings struct {
	ProjectID                                string      `json:"project_id"`
	Enabled                                  bool        `json:"enabled"`
	ScheduleTime                             string      `json:"schedule_time"`
	Timezone                                 string      `json:"timezone"`
	SourceSitemap                            bool        `json:"source_sitemap"`
	SourceGSC                                bool        `json:"source_gsc"`
	SourceProblemPages                       bool        `json:"source_problem_pages"`
	SourceStalePages                         bool        `json:"source_stale_pages"`
	SourceManualQueue                        bool        `json:"source_manual_queue"`
	StaleAfterDays                           int         `json:"stale_after_days"`
	MaxCandidatesPerRun                      int         `json:"max_candidates_per_run"`
	MaxChangedPagesPerRun                    int         `json:"max_changed_pages_per_run"`
	MaxNewPagesPerRun                        int         `json:"max_new_pages_per_run"`
	MaxDiscoveredPagesPerRun                 int         `json:"max_discovered_pages_per_run"`
	MaxDiscoveryDepth                        int         `json:"max_discovery_depth"`
	RespectRobotsTxt                         bool        `json:"respect_robots_txt"`
	UseConditionalRequests                   bool        `json:"use_conditional_requests"`
	FallbackToGetWhenHeadFails               bool        `json:"fallback_to_get_when_head_fails"`
	EnableJSRenderingForDelta                string      `json:"enable_js_rendering_for_delta"`
	RateLimitRequestsPerSecond               float64     `json:"rate_limit_requests_per_second"`
	RetryCount                               int         `json:"retry_count"`
	RetryBackoffSeconds                      int         `json:"retry_backoff_seconds"`
	RecomputePageRankWhenGraphChanged        bool        `json:"recompute_pagerank_when_graph_changed"`
	KeepDeltaHistoryDays                     int         `json:"keep_delta_history_days"`
	CanonicalHostPolicy                      string      `json:"canonical_host_policy"`
	NormalizeTrailingSlash                   bool        `json:"normalize_trailing_slash"`
	StripFragments                           bool        `json:"strip_fragments"`
	StripTrackingParams                      bool        `json:"strip_tracking_params"`
	AllowedQueryParams                       StringSlice `json:"allowed_query_params"`
	BlockedURLPatterns                       StringSlice `json:"blocked_url_patterns"`
	AllowedURLPatterns                       StringSlice `json:"allowed_url_patterns"`
	RequireConfirmationOnScopeChange         bool        `json:"require_confirmation_on_scope_change"`
	RequireConfirmationOnFullRecrawl         bool        `json:"require_confirmation_on_full_recrawl"`
	NeverDeletePreviousSnapshotBeforeSuccess bool        `json:"never_delete_previous_snapshot_before_success"`
	PauseDeltaWhenFullCrawlRunning           bool        `json:"pause_delta_when_full_crawl_running"`
	MaxRuntimeMinutes                        int         `json:"max_runtime_minutes"`
	OnLimitReached                           string      `json:"on_limit_reached"`
	LastRunAt                                *time.Time  `json:"last_run_at"`
	LastSessionID                            string      `json:"last_session_id"`
	UpdatedAt                                time.Time   `json:"updated_at"`
}

type StringSlice []string

func (s StringSlice) Value() (driver.Value, error) {
	if s == nil {
		return "[]", nil
	}
	b, err := json.Marshal([]string(s))
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func (s *StringSlice) Scan(value interface{}) error {
	if value == nil {
		*s = []string{}
		return nil
	}
	var raw string
	switch v := value.(type) {
	case string:
		raw = v
	case []byte:
		raw = string(v)
	default:
		return fmt.Errorf("unsupported StringSlice scan type %T", value)
	}
	if raw == "" {
		*s = []string{}
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return err
	}
	*s = out
	return nil
}

type APIKey struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	KeyPrefix  string     `json:"key_prefix"`
	Type       string     `json:"type"` // "general" | "project"
	ProjectID  *string    `json:"project_id"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	Active     bool       `json:"active"`
}

type APIKeyCreateResult struct {
	APIKey
	FullKey string `json:"key"`
}

type KeyLookupResult struct {
	ID        string
	Type      string
	ProjectID *string
}

type Store struct {
	db *sql.DB
}

func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite: %w", err)
	}
	if dbPath == ":memory:" {
		db.SetMaxOpenConns(1)
	}

	// Enable WAL mode and foreign keys
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("setting pragma: %w", err)
		}
	}

	// Create tables
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS projects (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating projects table: %w", err)
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS api_keys (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			key_hash TEXT NOT NULL UNIQUE,
			key_prefix TEXT NOT NULL,
			type TEXT NOT NULL CHECK(type IN ('general', 'project')),
			project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_used_at DATETIME,
			active INTEGER DEFAULT 1
		)
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating api_keys table: %w", err)
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			role TEXT NOT NULL CHECK(role IN ('admin', 'viewer')),
			active INTEGER DEFAULT 1,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_login_at DATETIME
		)
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating users table: %w", err)
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS user_projects (
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			PRIMARY KEY (user_id, project_id)
		)
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating user_projects table: %w", err)
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS user_sessions (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			token_hash TEXT NOT NULL UNIQUE,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			expires_at DATETIME NOT NULL
		)
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating user_sessions table: %w", err)
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS gsc_connections (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL UNIQUE REFERENCES projects(id) ON DELETE CASCADE,
			property_url TEXT NOT NULL,
			access_token TEXT NOT NULL,
			refresh_token TEXT NOT NULL,
			token_expiry DATETIME NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating gsc_connections table: %w", err)
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS gsc_fetch_checkpoints (
			project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
			property_url TEXT NOT NULL,
			start_date TEXT NOT NULL,
			end_date TEXT NOT NULL,
			next_start_date TEXT NOT NULL,
			rows_fetched INTEGER NOT NULL DEFAULT 0,
			completed INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating gsc_fetch_checkpoints table: %w", err)
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS project_delta_settings (
			project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
			enabled INTEGER NOT NULL DEFAULT 0,
			schedule_time TEXT NOT NULL DEFAULT '03:00',
			timezone TEXT NOT NULL DEFAULT 'UTC',
			source_sitemap INTEGER NOT NULL DEFAULT 1,
			source_gsc INTEGER NOT NULL DEFAULT 1,
			source_problem_pages INTEGER NOT NULL DEFAULT 1,
			source_stale_pages INTEGER NOT NULL DEFAULT 1,
			source_manual_queue INTEGER NOT NULL DEFAULT 1,
			stale_after_days INTEGER NOT NULL DEFAULT 30,
			max_candidates_per_run INTEGER NOT NULL DEFAULT 5000,
			max_changed_pages_per_run INTEGER NOT NULL DEFAULT 1000,
			max_new_pages_per_run INTEGER NOT NULL DEFAULT 1000,
			max_discovered_pages_per_run INTEGER NOT NULL DEFAULT 500,
			max_discovery_depth INTEGER NOT NULL DEFAULT 1,
			respect_robots_txt INTEGER NOT NULL DEFAULT 1,
			use_conditional_requests INTEGER NOT NULL DEFAULT 1,
			fallback_to_get_when_head_fails INTEGER NOT NULL DEFAULT 1,
			enable_js_rendering_for_delta TEXT NOT NULL DEFAULT 'inherit',
			rate_limit_requests_per_second REAL NOT NULL DEFAULT 1.0,
			retry_count INTEGER NOT NULL DEFAULT 2,
			retry_backoff_seconds INTEGER NOT NULL DEFAULT 10,
			recompute_pagerank_when_graph_changed INTEGER NOT NULL DEFAULT 1,
			keep_delta_history_days INTEGER NOT NULL DEFAULT 90,
			canonical_host_policy TEXT NOT NULL DEFAULT 'project',
			normalize_trailing_slash INTEGER NOT NULL DEFAULT 1,
			strip_fragments INTEGER NOT NULL DEFAULT 1,
			strip_tracking_params INTEGER NOT NULL DEFAULT 1,
			allowed_query_params TEXT NOT NULL DEFAULT '[]',
			blocked_url_patterns TEXT NOT NULL DEFAULT '[]',
			allowed_url_patterns TEXT NOT NULL DEFAULT '[]',
			require_confirmation_on_scope_change INTEGER NOT NULL DEFAULT 1,
			require_confirmation_on_full_recrawl INTEGER NOT NULL DEFAULT 1,
			never_delete_previous_snapshot_before_success INTEGER NOT NULL DEFAULT 1,
			pause_delta_when_full_crawl_running INTEGER NOT NULL DEFAULT 1,
			max_runtime_minutes INTEGER NOT NULL DEFAULT 120,
			on_limit_reached TEXT NOT NULL DEFAULT 'defer',
			last_run_at DATETIME,
			last_session_id TEXT NOT NULL DEFAULT '',
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating project_delta_settings table: %w", err)
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS project_delta_manual_queue (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			url TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			consumed_at DATETIME,
			UNIQUE(project_id, url, consumed_at)
		)
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating project_delta_manual_queue table: %w", err)
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS project_quality_settings (
			project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
			enabled INTEGER NOT NULL DEFAULT 1,
			min_trusted_score INTEGER NOT NULL DEFAULT 85,
			untrusted_score_below INTEGER NOT NULL DEFAULT 60,
			coverage_drop_percent REAL NOT NULL DEFAULT 30,
			coverage_growth_percent REAL NOT NULL DEFAULT 50,
			coverage_min_pages_delta INTEGER NOT NULL DEFAULT 50,
			internal_links_drop_percent REAL NOT NULL DEFAULT 25,
			internal_links_min_delta INTEGER NOT NULL DEFAULT 500,
			status_404_percent REAL NOT NULL DEFAULT 10,
			status_404_min_delta INTEGER NOT NULL DEFAULT 25,
			noindex_percent REAL NOT NULL DEFAULT 10,
			noindex_min_delta INTEGER NOT NULL DEFAULT 25,
			redirect_percent REAL NOT NULL DEFAULT 15,
			redirect_min_delta INTEGER NOT NULL DEFAULT 25,
			canonical_mismatch_percent REAL NOT NULL DEFAULT 10,
			canonical_mismatch_min_delta INTEGER NOT NULL DEFAULT 25,
			pagerank_top_n INTEGER NOT NULL DEFAULT 20,
			pagerank_top_overlap_min_percent REAL NOT NULL DEFAULT 50,
			pagerank_zero_top_pages_max INTEGER NOT NULL DEFAULT 2,
			canary_min_internal_links_default INTEGER NOT NULL DEFAULT 1,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating project_quality_settings table: %w", err)
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS project_canaries (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			url TEXT NOT NULL,
			expected_status INTEGER NOT NULL DEFAULT 200,
			expected_final_url TEXT NOT NULL DEFAULT '',
			expected_canonical TEXT NOT NULL DEFAULT '',
			title_contains TEXT NOT NULL DEFAULT '',
			min_internal_links INTEGER NOT NULL DEFAULT 1,
			expect_indexable INTEGER NOT NULL DEFAULT 1,
			active INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating project_canaries table: %w", err)
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS rulesets (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating rulesets table: %w", err)
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS rules (
			id TEXT PRIMARY KEY,
			ruleset_id TEXT NOT NULL REFERENCES rulesets(id) ON DELETE CASCADE,
			type TEXT NOT NULL,
			name TEXT NOT NULL,
			value TEXT NOT NULL,
			extra TEXT DEFAULT '',
			sort_order INTEGER DEFAULT 0
		)
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating rules table: %w", err)
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS provider_connections (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			provider TEXT NOT NULL,
			domain TEXT NOT NULL,
			api_key TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(project_id, provider),
			FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
		)
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating provider_connections table: %w", err)
	}

	// Migrate: add limit columns to provider_connections
	for _, col := range []string{
		"ALTER TABLE provider_connections ADD COLUMN limit_backlinks INTEGER NOT NULL DEFAULT 1000",
		"ALTER TABLE provider_connections ADD COLUMN limit_refdomains INTEGER NOT NULL DEFAULT 1000",
		"ALTER TABLE provider_connections ADD COLUMN limit_rankings INTEGER NOT NULL DEFAULT 1000",
		"ALTER TABLE provider_connections ADD COLUMN limit_top_pages INTEGER NOT NULL DEFAULT 1000",
	} {
		db.Exec(col) // ignore duplicate column errors
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS extractor_sets (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating extractor_sets table: %w", err)
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS extractors (
			id TEXT PRIMARY KEY,
			set_id TEXT NOT NULL REFERENCES extractor_sets(id) ON DELETE CASCADE,
			type TEXT NOT NULL,
			name TEXT NOT NULL,
			selector TEXT NOT NULL DEFAULT '',
			attribute TEXT NOT NULL DEFAULT '',
			url_pattern TEXT NOT NULL DEFAULT '',
			sort_order INTEGER DEFAULT 0
		)
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating extractors table: %w", err)
	}

	// Restrict file permissions to owner-only (skip for in-memory DBs and Windows)
	if dbPath != ":memory:" && runtime.GOOS != "windows" {
		if err := os.Chmod(dbPath, 0600); err != nil {
			db.Close()
			return nil, fmt.Errorf("setting database permissions: %w", err)
		}
	}

	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// --- Projects ---

func (s *Store) ListProjects() ([]Project, error) {
	rows, err := s.db.Query(`SELECT id, name, created_at FROM projects ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.CreatedAt); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	if projects == nil {
		projects = []Project{}
	}
	return projects, nil
}

func (s *Store) ListProjectsPaginated(limit, offset int, search string) ([]Project, int, error) {
	where := ""
	var args []interface{}
	if search != "" {
		where = " WHERE name LIKE ?"
		args = append(args, "%"+search+"%")
	}

	var total int
	countArgs := make([]interface{}, len(args))
	copy(countArgs, args)
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM projects`+where, countArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := `SELECT id, name, created_at FROM projects` + where + ` ORDER BY created_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.CreatedAt); err != nil {
			return nil, 0, err
		}
		projects = append(projects, p)
	}
	if projects == nil {
		projects = []Project{}
	}
	return projects, total, nil
}

func (s *Store) CreateProject(name string) (*Project, error) {
	p := &Project{
		ID:        uuid.New().String(),
		Name:      name,
		CreatedAt: time.Now().UTC(),
	}
	_, err := s.db.Exec(`INSERT INTO projects (id, name, created_at) VALUES (?, ?, ?)`,
		p.ID, p.Name, p.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("creating project: %w", err)
	}
	return p, nil
}

func (s *Store) GetProject(id string) (*Project, error) {
	var p Project
	err := s.db.QueryRow(`SELECT id, name, created_at FROM projects WHERE id = ?`, id).
		Scan(&p.ID, &p.Name, &p.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) RenameProject(id, name string) error {
	res, err := s.db.Exec(`UPDATE projects SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("project not found")
	}
	return nil
}

func (s *Store) DeleteProject(id string) error {
	res, err := s.db.Exec(`DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("project not found")
	}
	return nil
}

// --- Project Delta Crawl Settings ---

func DefaultProjectDeltaSettings(projectID string) ProjectDeltaSettings {
	return ProjectDeltaSettings{
		ProjectID:                                projectID,
		Enabled:                                  false,
		ScheduleTime:                             "03:00",
		Timezone:                                 "UTC",
		SourceSitemap:                            true,
		SourceGSC:                                true,
		SourceProblemPages:                       true,
		SourceStalePages:                         true,
		SourceManualQueue:                        true,
		StaleAfterDays:                           30,
		MaxCandidatesPerRun:                      5000,
		MaxChangedPagesPerRun:                    1000,
		MaxNewPagesPerRun:                        1000,
		MaxDiscoveredPagesPerRun:                 500,
		MaxDiscoveryDepth:                        1,
		RespectRobotsTxt:                         true,
		UseConditionalRequests:                   true,
		FallbackToGetWhenHeadFails:               true,
		EnableJSRenderingForDelta:                "inherit",
		RateLimitRequestsPerSecond:               1,
		RetryCount:                               2,
		RetryBackoffSeconds:                      10,
		RecomputePageRankWhenGraphChanged:        true,
		KeepDeltaHistoryDays:                     90,
		CanonicalHostPolicy:                      "project",
		NormalizeTrailingSlash:                   true,
		StripFragments:                           true,
		StripTrackingParams:                      true,
		AllowedQueryParams:                       []string{},
		BlockedURLPatterns:                       []string{},
		AllowedURLPatterns:                       []string{},
		RequireConfirmationOnScopeChange:         true,
		RequireConfirmationOnFullRecrawl:         true,
		NeverDeletePreviousSnapshotBeforeSuccess: true,
		PauseDeltaWhenFullCrawlRunning:           true,
		MaxRuntimeMinutes:                        120,
		OnLimitReached:                           "defer",
		UpdatedAt:                                time.Now().UTC(),
	}
}

func sanitizeDeltaSettings(in ProjectDeltaSettings) ProjectDeltaSettings {
	out := in
	if out.ScheduleTime == "" {
		out.ScheduleTime = "03:00"
	}
	if out.Timezone == "" {
		out.Timezone = "UTC"
	}
	if out.StaleAfterDays <= 0 {
		out.StaleAfterDays = 30
	}
	if out.MaxCandidatesPerRun <= 0 {
		out.MaxCandidatesPerRun = 5000
	}
	if out.MaxChangedPagesPerRun <= 0 {
		out.MaxChangedPagesPerRun = 1000
	}
	if out.MaxNewPagesPerRun <= 0 {
		out.MaxNewPagesPerRun = 1000
	}
	if out.MaxDiscoveredPagesPerRun < 0 {
		out.MaxDiscoveredPagesPerRun = 0
	}
	if out.MaxDiscoveryDepth < 0 {
		out.MaxDiscoveryDepth = 0
	}
	if out.EnableJSRenderingForDelta == "" {
		out.EnableJSRenderingForDelta = "inherit"
	}
	if out.RateLimitRequestsPerSecond <= 0 {
		out.RateLimitRequestsPerSecond = 1
	}
	if out.RetryCount < 0 {
		out.RetryCount = 0
	}
	if out.RetryBackoffSeconds <= 0 {
		out.RetryBackoffSeconds = 10
	}
	if out.KeepDeltaHistoryDays <= 0 {
		out.KeepDeltaHistoryDays = 90
	}
	if out.CanonicalHostPolicy == "" {
		out.CanonicalHostPolicy = "project"
	}
	if out.MaxRuntimeMinutes <= 0 {
		out.MaxRuntimeMinutes = 120
	}
	if out.OnLimitReached == "" {
		out.OnLimitReached = "defer"
	}
	if out.AllowedQueryParams == nil {
		out.AllowedQueryParams = []string{}
	}
	if out.BlockedURLPatterns == nil {
		out.BlockedURLPatterns = []string{}
	}
	if out.AllowedURLPatterns == nil {
		out.AllowedURLPatterns = []string{}
	}
	return out
}

func (s *Store) GetProjectDeltaSettings(projectID string) (*ProjectDeltaSettings, error) {
	defaults := DefaultProjectDeltaSettings(projectID)
	row := s.db.QueryRow(`
		SELECT project_id, enabled, schedule_time, timezone,
			source_sitemap, source_gsc, source_problem_pages, source_stale_pages, source_manual_queue,
			stale_after_days, max_candidates_per_run, max_changed_pages_per_run, max_new_pages_per_run,
			max_discovered_pages_per_run, max_discovery_depth, respect_robots_txt, use_conditional_requests,
			fallback_to_get_when_head_fails, enable_js_rendering_for_delta, rate_limit_requests_per_second,
			retry_count, retry_backoff_seconds, recompute_pagerank_when_graph_changed, keep_delta_history_days,
			canonical_host_policy, normalize_trailing_slash, strip_fragments, strip_tracking_params,
			allowed_query_params, blocked_url_patterns, allowed_url_patterns,
			require_confirmation_on_scope_change, require_confirmation_on_full_recrawl,
			never_delete_previous_snapshot_before_success, pause_delta_when_full_crawl_running,
			max_runtime_minutes, on_limit_reached, last_run_at, last_session_id, updated_at
		FROM project_delta_settings WHERE project_id = ?`, projectID)

	var st ProjectDeltaSettings
	var enabled, sourceSitemap, sourceGSC, sourceProblemPages, sourceStalePages, sourceManualQueue int
	var respectRobots, useConditional, fallbackGet, recomputePR int
	var normalizeSlash, stripFragments, stripTracking int
	var requireScopeConfirm, requireRecrawlConfirm, neverDelete, pauseWhenFull int
	if err := row.Scan(
		&st.ProjectID, &enabled, &st.ScheduleTime, &st.Timezone,
		&sourceSitemap, &sourceGSC, &sourceProblemPages, &sourceStalePages, &sourceManualQueue,
		&st.StaleAfterDays, &st.MaxCandidatesPerRun, &st.MaxChangedPagesPerRun, &st.MaxNewPagesPerRun,
		&st.MaxDiscoveredPagesPerRun, &st.MaxDiscoveryDepth, &respectRobots, &useConditional,
		&fallbackGet, &st.EnableJSRenderingForDelta, &st.RateLimitRequestsPerSecond,
		&st.RetryCount, &st.RetryBackoffSeconds, &recomputePR, &st.KeepDeltaHistoryDays,
		&st.CanonicalHostPolicy, &normalizeSlash, &stripFragments, &stripTracking,
		&st.AllowedQueryParams, &st.BlockedURLPatterns, &st.AllowedURLPatterns,
		&requireScopeConfirm, &requireRecrawlConfirm, &neverDelete, &pauseWhenFull,
		&st.MaxRuntimeMinutes, &st.OnLimitReached, &st.LastRunAt, &st.LastSessionID, &st.UpdatedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return &defaults, nil
		}
		return nil, err
	}
	st.Enabled = enabled != 0
	st.SourceSitemap = sourceSitemap != 0
	st.SourceGSC = sourceGSC != 0
	st.SourceProblemPages = sourceProblemPages != 0
	st.SourceStalePages = sourceStalePages != 0
	st.SourceManualQueue = sourceManualQueue != 0
	st.RespectRobotsTxt = respectRobots != 0
	st.UseConditionalRequests = useConditional != 0
	st.FallbackToGetWhenHeadFails = fallbackGet != 0
	st.RecomputePageRankWhenGraphChanged = recomputePR != 0
	st.NormalizeTrailingSlash = normalizeSlash != 0
	st.StripFragments = stripFragments != 0
	st.StripTrackingParams = stripTracking != 0
	st.RequireConfirmationOnScopeChange = requireScopeConfirm != 0
	st.RequireConfirmationOnFullRecrawl = requireRecrawlConfirm != 0
	st.NeverDeletePreviousSnapshotBeforeSuccess = neverDelete != 0
	st.PauseDeltaWhenFullCrawlRunning = pauseWhenFull != 0
	return &st, nil
}

func (s *Store) SaveProjectDeltaSettings(settings ProjectDeltaSettings) (*ProjectDeltaSettings, error) {
	if settings.ProjectID == "" {
		return nil, fmt.Errorf("project_id is required")
	}
	settings = sanitizeDeltaSettings(settings)
	now := time.Now().UTC()
	_, err := s.db.Exec(`
		INSERT INTO project_delta_settings (
			project_id, enabled, schedule_time, timezone,
			source_sitemap, source_gsc, source_problem_pages, source_stale_pages, source_manual_queue,
			stale_after_days, max_candidates_per_run, max_changed_pages_per_run, max_new_pages_per_run,
			max_discovered_pages_per_run, max_discovery_depth, respect_robots_txt, use_conditional_requests,
			fallback_to_get_when_head_fails, enable_js_rendering_for_delta, rate_limit_requests_per_second,
			retry_count, retry_backoff_seconds, recompute_pagerank_when_graph_changed, keep_delta_history_days,
			canonical_host_policy, normalize_trailing_slash, strip_fragments, strip_tracking_params,
			allowed_query_params, blocked_url_patterns, allowed_url_patterns,
			require_confirmation_on_scope_change, require_confirmation_on_full_recrawl,
			never_delete_previous_snapshot_before_success, pause_delta_when_full_crawl_running,
			max_runtime_minutes, on_limit_reached, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET
			enabled = excluded.enabled,
			schedule_time = excluded.schedule_time,
			timezone = excluded.timezone,
			source_sitemap = excluded.source_sitemap,
			source_gsc = excluded.source_gsc,
			source_problem_pages = excluded.source_problem_pages,
			source_stale_pages = excluded.source_stale_pages,
			source_manual_queue = excluded.source_manual_queue,
			stale_after_days = excluded.stale_after_days,
			max_candidates_per_run = excluded.max_candidates_per_run,
			max_changed_pages_per_run = excluded.max_changed_pages_per_run,
			max_new_pages_per_run = excluded.max_new_pages_per_run,
			max_discovered_pages_per_run = excluded.max_discovered_pages_per_run,
			max_discovery_depth = excluded.max_discovery_depth,
			respect_robots_txt = excluded.respect_robots_txt,
			use_conditional_requests = excluded.use_conditional_requests,
			fallback_to_get_when_head_fails = excluded.fallback_to_get_when_head_fails,
			enable_js_rendering_for_delta = excluded.enable_js_rendering_for_delta,
			rate_limit_requests_per_second = excluded.rate_limit_requests_per_second,
			retry_count = excluded.retry_count,
			retry_backoff_seconds = excluded.retry_backoff_seconds,
			recompute_pagerank_when_graph_changed = excluded.recompute_pagerank_when_graph_changed,
			keep_delta_history_days = excluded.keep_delta_history_days,
			canonical_host_policy = excluded.canonical_host_policy,
			normalize_trailing_slash = excluded.normalize_trailing_slash,
			strip_fragments = excluded.strip_fragments,
			strip_tracking_params = excluded.strip_tracking_params,
			allowed_query_params = excluded.allowed_query_params,
			blocked_url_patterns = excluded.blocked_url_patterns,
			allowed_url_patterns = excluded.allowed_url_patterns,
			require_confirmation_on_scope_change = excluded.require_confirmation_on_scope_change,
			require_confirmation_on_full_recrawl = excluded.require_confirmation_on_full_recrawl,
			never_delete_previous_snapshot_before_success = excluded.never_delete_previous_snapshot_before_success,
			pause_delta_when_full_crawl_running = excluded.pause_delta_when_full_crawl_running,
			max_runtime_minutes = excluded.max_runtime_minutes,
			on_limit_reached = excluded.on_limit_reached,
			updated_at = excluded.updated_at`,
		settings.ProjectID, deltaBoolInt(settings.Enabled), settings.ScheduleTime, settings.Timezone,
		deltaBoolInt(settings.SourceSitemap), deltaBoolInt(settings.SourceGSC), deltaBoolInt(settings.SourceProblemPages), deltaBoolInt(settings.SourceStalePages), deltaBoolInt(settings.SourceManualQueue),
		settings.StaleAfterDays, settings.MaxCandidatesPerRun, settings.MaxChangedPagesPerRun, settings.MaxNewPagesPerRun,
		settings.MaxDiscoveredPagesPerRun, settings.MaxDiscoveryDepth, deltaBoolInt(settings.RespectRobotsTxt), deltaBoolInt(settings.UseConditionalRequests),
		deltaBoolInt(settings.FallbackToGetWhenHeadFails), settings.EnableJSRenderingForDelta, settings.RateLimitRequestsPerSecond,
		settings.RetryCount, settings.RetryBackoffSeconds, deltaBoolInt(settings.RecomputePageRankWhenGraphChanged), settings.KeepDeltaHistoryDays,
		settings.CanonicalHostPolicy, deltaBoolInt(settings.NormalizeTrailingSlash), deltaBoolInt(settings.StripFragments), deltaBoolInt(settings.StripTrackingParams),
		settings.AllowedQueryParams, settings.BlockedURLPatterns, settings.AllowedURLPatterns,
		deltaBoolInt(settings.RequireConfirmationOnScopeChange), deltaBoolInt(settings.RequireConfirmationOnFullRecrawl),
		deltaBoolInt(settings.NeverDeletePreviousSnapshotBeforeSuccess), deltaBoolInt(settings.PauseDeltaWhenFullCrawlRunning),
		settings.MaxRuntimeMinutes, settings.OnLimitReached, now,
	)
	if err != nil {
		return nil, err
	}
	return s.GetProjectDeltaSettings(settings.ProjectID)
}

func deltaBoolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (s *Store) ListEnabledProjectDeltaSettings() ([]ProjectDeltaSettings, error) {
	rows, err := s.db.Query(`SELECT project_id FROM project_delta_settings WHERE enabled = 1 ORDER BY project_id`)
	if err != nil {
		return nil, err
	}
	var projectIDs []string
	for rows.Next() {
		var projectID string
		if err := rows.Scan(&projectID); err != nil {
			rows.Close()
			return nil, err
		}
		projectIDs = append(projectIDs, projectID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	var result []ProjectDeltaSettings
	for _, projectID := range projectIDs {
		st, err := s.GetProjectDeltaSettings(projectID)
		if err != nil {
			return nil, err
		}
		result = append(result, *st)
	}
	if result == nil {
		result = []ProjectDeltaSettings{}
	}
	return result, nil
}

func (s *Store) MarkProjectDeltaRun(projectID, sessionID string, when time.Time) error {
	_, err := s.db.Exec(`
		INSERT INTO project_delta_settings (project_id, last_run_at, last_session_id, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET
			last_run_at = excluded.last_run_at,
			last_session_id = excluded.last_session_id,
			updated_at = excluded.updated_at`,
		projectID, when.UTC(), sessionID, when.UTC())
	return err
}

func (s *Store) AddProjectDeltaManualURLs(projectID string, urls []string) (int, error) {
	count := 0
	for _, raw := range urls {
		u := strings.TrimSpace(raw)
		if u == "" {
			continue
		}
		res, err := s.db.Exec(`
			INSERT OR IGNORE INTO project_delta_manual_queue (id, project_id, url, created_at)
			VALUES (?, ?, ?, ?)`,
			uuid.New().String(), projectID, u, time.Now().UTC())
		if err != nil {
			return count, err
		}
		inserted, err := res.RowsAffected()
		if err != nil {
			return count, err
		}
		count += int(inserted)
	}
	return count, nil
}

func (s *Store) ListProjectDeltaManualURLs(projectID string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := s.db.Query(`
		SELECT url FROM project_delta_manual_queue
		WHERE project_id = ? AND consumed_at IS NULL
		ORDER BY created_at ASC
		LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var urls []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		urls = append(urls, u)
	}
	if urls == nil {
		urls = []string{}
	}
	return urls, rows.Err()
}

func (s *Store) MarkProjectDeltaManualURLsConsumed(projectID string, urls []string, when time.Time) error {
	for _, raw := range urls {
		u := strings.TrimSpace(raw)
		if u == "" {
			continue
		}
		if _, err := s.db.Exec(`
			UPDATE project_delta_manual_queue
			SET consumed_at = ?
			WHERE project_id = ? AND url = ? AND consumed_at IS NULL`,
			when.UTC(), projectID, u); err != nil {
			return err
		}
	}
	return nil
}

// --- API Keys ---

func (s *Store) ListAPIKeys() ([]APIKey, error) {
	rows, err := s.db.Query(`
		SELECT id, name, key_prefix, type, project_id, created_at, last_used_at, active
		FROM api_keys ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []APIKey
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.Name, &k.KeyPrefix, &k.Type, &k.ProjectID,
			&k.CreatedAt, &k.LastUsedAt, &k.Active); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	if keys == nil {
		keys = []APIKey{}
	}
	return keys, nil
}

func (s *Store) CreateAPIKey(name, keyType string, projectID *string) (*APIKeyCreateResult, error) {
	if keyType != "general" && keyType != "project" {
		return nil, fmt.Errorf("invalid key type: %s", keyType)
	}
	if keyType == "project" && (projectID == nil || *projectID == "") {
		return nil, fmt.Errorf("project_id required for project keys")
	}

	// Generate random key: 32 bytes -> hex -> prefix with sk_
	rawBytes := make([]byte, 32)
	if _, err := rand.Read(rawBytes); err != nil {
		return nil, fmt.Errorf("generating key: %w", err)
	}
	fullKey := "sk_" + hex.EncodeToString(rawBytes)

	// Hash for storage
	hash := sha256.Sum256([]byte(fullKey))
	keyHash := hex.EncodeToString(hash[:])

	// Display prefix: sk_ + first 8 hex chars
	keyPrefix := fullKey[:11] + "..."

	k := APIKey{
		ID:        uuid.New().String(),
		Name:      name,
		KeyPrefix: keyPrefix,
		Type:      keyType,
		ProjectID: projectID,
		CreatedAt: time.Now().UTC(),
		Active:    true,
	}

	_, err := s.db.Exec(`
		INSERT INTO api_keys (id, name, key_hash, key_prefix, type, project_id, created_at, active)
		VALUES (?, ?, ?, ?, ?, ?, ?, 1)`,
		k.ID, k.Name, keyHash, k.KeyPrefix, k.Type, k.ProjectID, k.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("inserting api key: %w", err)
	}

	return &APIKeyCreateResult{APIKey: k, FullKey: fullKey}, nil
}

func (s *Store) DeleteAPIKey(id string) error {
	res, err := s.db.Exec(`DELETE FROM api_keys WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("api key not found")
	}
	return nil
}

// --- GSC Connections ---

type GSCConnection struct {
	ID           string    `json:"id"`
	ProjectID    string    `json:"project_id"`
	PropertyURL  string    `json:"property_url"`
	AccessToken  string    `json:"-"`
	RefreshToken string    `json:"-"`
	TokenExpiry  time.Time `json:"token_expiry"`
	CreatedAt    time.Time `json:"created_at"`
}

type GSCFetchCheckpoint struct {
	ProjectID     string    `json:"project_id"`
	PropertyURL   string    `json:"property_url"`
	StartDate     string    `json:"start_date"`
	EndDate       string    `json:"end_date"`
	NextStartDate string    `json:"next_start_date"`
	RowsFetched   int       `json:"rows_fetched"`
	Completed     bool      `json:"completed"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (s *Store) SaveGSCConnection(conn *GSCConnection) error {
	if conn.ID == "" {
		conn.ID = uuid.New().String()
	}
	_, err := s.db.Exec(`
		INSERT INTO gsc_connections (id, project_id, property_url, access_token, refresh_token, token_expiry, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET
			property_url = excluded.property_url,
			access_token = excluded.access_token,
			refresh_token = excluded.refresh_token,
			token_expiry = excluded.token_expiry`,
		conn.ID, conn.ProjectID, conn.PropertyURL, conn.AccessToken, conn.RefreshToken, conn.TokenExpiry, time.Now().UTC())
	return err
}

func (s *Store) GetGSCConnection(projectID string) (*GSCConnection, error) {
	var c GSCConnection
	err := s.db.QueryRow(`
		SELECT id, project_id, property_url, access_token, refresh_token, token_expiry, created_at
		FROM gsc_connections WHERE project_id = ?`, projectID).
		Scan(&c.ID, &c.ProjectID, &c.PropertyURL, &c.AccessToken, &c.RefreshToken, &c.TokenExpiry, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) SaveGSCFetchCheckpoint(cp *GSCFetchCheckpoint) error {
	now := time.Now().UTC()
	completed := 0
	if cp.Completed {
		completed = 1
	}
	_, err := s.db.Exec(`
		INSERT INTO gsc_fetch_checkpoints (
			project_id, property_url, start_date, end_date, next_start_date,
			rows_fetched, completed, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET
			property_url = excluded.property_url,
			start_date = excluded.start_date,
			end_date = excluded.end_date,
			next_start_date = excluded.next_start_date,
			rows_fetched = excluded.rows_fetched,
			completed = excluded.completed,
			updated_at = excluded.updated_at`,
		cp.ProjectID, cp.PropertyURL, cp.StartDate, cp.EndDate, cp.NextStartDate,
		cp.RowsFetched, completed, now, now)
	return err
}

func (s *Store) GetGSCFetchCheckpoint(projectID string) (*GSCFetchCheckpoint, error) {
	var cp GSCFetchCheckpoint
	var completed int
	err := s.db.QueryRow(`
		SELECT project_id, property_url, start_date, end_date, next_start_date,
			rows_fetched, completed, created_at, updated_at
		FROM gsc_fetch_checkpoints WHERE project_id = ?`, projectID).
		Scan(&cp.ProjectID, &cp.PropertyURL, &cp.StartDate, &cp.EndDate, &cp.NextStartDate,
			&cp.RowsFetched, &completed, &cp.CreatedAt, &cp.UpdatedAt)
	if err != nil {
		return nil, err
	}
	cp.Completed = completed != 0
	return &cp, nil
}

func (s *Store) DeleteGSCFetchCheckpoint(projectID string) error {
	_, err := s.db.Exec(`DELETE FROM gsc_fetch_checkpoints WHERE project_id = ?`, projectID)
	return err
}

func (s *Store) DeleteGSCConnection(projectID string) error {
	res, err := s.db.Exec(`DELETE FROM gsc_connections WHERE project_id = ?`, projectID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("gsc connection not found")
	}
	return nil
}

func (s *Store) ListGSCConnections() ([]GSCConnection, error) {
	rows, err := s.db.Query(`SELECT id, project_id, property_url, access_token, refresh_token, token_expiry, created_at FROM gsc_connections ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var conns []GSCConnection
	for rows.Next() {
		var c GSCConnection
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.PropertyURL, &c.AccessToken, &c.RefreshToken, &c.TokenExpiry, &c.CreatedAt); err != nil {
			return nil, err
		}
		conns = append(conns, c)
	}
	if conns == nil {
		conns = []GSCConnection{}
	}
	return conns, nil
}

func (s *Store) ValidateKey(rawKey string) *KeyLookupResult {
	hash := sha256.Sum256([]byte(rawKey))
	keyHash := hex.EncodeToString(hash[:])

	var result KeyLookupResult
	err := s.db.QueryRow(`
		SELECT id, type, project_id FROM api_keys
		WHERE key_hash = ? AND active = 1`,
		keyHash).Scan(&result.ID, &result.Type, &result.ProjectID)
	if err != nil {
		return nil
	}

	// Update last_used_at
	if _, err := s.db.Exec(`UPDATE api_keys SET last_used_at = ? WHERE id = ?`, time.Now().UTC(), result.ID); err != nil {
		applog.Warnf("apikeys", "failed to update last_used_at: %v", err)
	}

	return &result
}

// --- Rulesets ---

func (s *Store) ListRulesets() ([]customtests.Ruleset, error) {
	rows, err := s.db.Query(`
		SELECT rs.id, rs.name, rs.created_at, rs.updated_at, COUNT(r.id) AS rule_count
		FROM rulesets rs
		LEFT JOIN rules r ON r.ruleset_id = rs.id
		GROUP BY rs.id
		ORDER BY rs.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rulesets []customtests.Ruleset
	for rows.Next() {
		var rs customtests.Ruleset
		if err := rows.Scan(&rs.ID, &rs.Name, &rs.CreatedAt, &rs.UpdatedAt, &rs.RuleCount); err != nil {
			return nil, err
		}
		rulesets = append(rulesets, rs)
	}
	if rulesets == nil {
		rulesets = []customtests.Ruleset{}
	}
	return rulesets, nil
}

func (s *Store) GetRuleset(id string) (*customtests.Ruleset, error) {
	var rs customtests.Ruleset
	err := s.db.QueryRow(`SELECT id, name, created_at, updated_at FROM rulesets WHERE id = ?`, id).
		Scan(&rs.ID, &rs.Name, &rs.CreatedAt, &rs.UpdatedAt)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.Query(`SELECT id, ruleset_id, type, name, value, extra, sort_order FROM rules WHERE ruleset_id = ? ORDER BY sort_order`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var r customtests.TestRule
		if err := rows.Scan(&r.ID, &r.RulesetID, &r.Type, &r.Name, &r.Value, &r.Extra, &r.SortOrder); err != nil {
			return nil, err
		}
		rs.Rules = append(rs.Rules, r)
	}
	if rs.Rules == nil {
		rs.Rules = []customtests.TestRule{}
	}
	return &rs, nil
}

func (s *Store) CreateRuleset(name string, rules []customtests.TestRule) (*customtests.Ruleset, error) {
	now := time.Now().UTC()
	rs := &customtests.Ruleset{
		ID:        uuid.New().String(),
		Name:      name,
		CreatedAt: now,
		UpdatedAt: now,
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT INTO rulesets (id, name, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		rs.ID, rs.Name, rs.CreatedAt, rs.UpdatedAt); err != nil {
		return nil, fmt.Errorf("inserting ruleset: %w", err)
	}

	for i, r := range rules {
		r.ID = uuid.New().String()
		r.RulesetID = rs.ID
		r.SortOrder = i
		if _, err := tx.Exec(`INSERT INTO rules (id, ruleset_id, type, name, value, extra, sort_order) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.RulesetID, r.Type, r.Name, r.Value, r.Extra, r.SortOrder); err != nil {
			return nil, fmt.Errorf("inserting rule: %w", err)
		}
		rs.Rules = append(rs.Rules, r)
	}
	if rs.Rules == nil {
		rs.Rules = []customtests.TestRule{}
	}

	return rs, tx.Commit()
}

func (s *Store) UpdateRuleset(id, name string, rules []customtests.TestRule) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`UPDATE rulesets SET name = ?, updated_at = ? WHERE id = ?`, name, time.Now().UTC(), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("ruleset not found")
	}

	if _, err := tx.Exec(`DELETE FROM rules WHERE ruleset_id = ?`, id); err != nil {
		return err
	}

	for i, r := range rules {
		rID := uuid.New().String()
		if _, err := tx.Exec(`INSERT INTO rules (id, ruleset_id, type, name, value, extra, sort_order) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			rID, id, r.Type, r.Name, r.Value, r.Extra, i); err != nil {
			return fmt.Errorf("inserting rule: %w", err)
		}
	}

	return tx.Commit()
}

func (s *Store) DeleteRuleset(id string) error {
	res, err := s.db.Exec(`DELETE FROM rulesets WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("ruleset not found")
	}
	return nil
}

// --- Extractor Sets ---

func (s *Store) ListExtractorSets() ([]extraction.ExtractorSet, error) {
	rows, err := s.db.Query(`
		SELECT es.id, es.name, es.created_at, es.updated_at, COUNT(e.id) AS extractor_count
		FROM extractor_sets es
		LEFT JOIN extractors e ON e.set_id = es.id
		GROUP BY es.id
		ORDER BY es.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sets []extraction.ExtractorSet
	for rows.Next() {
		var es extraction.ExtractorSet
		var count int
		if err := rows.Scan(&es.ID, &es.Name, &es.CreatedAt, &es.UpdatedAt, &count); err != nil {
			return nil, err
		}
		es.ExtractorCount = count
		sets = append(sets, es)
	}
	if sets == nil {
		sets = []extraction.ExtractorSet{}
	}
	return sets, nil
}

func (s *Store) GetExtractorSet(id string) (*extraction.ExtractorSet, error) {
	var es extraction.ExtractorSet
	err := s.db.QueryRow(`SELECT id, name, created_at, updated_at FROM extractor_sets WHERE id = ?`, id).
		Scan(&es.ID, &es.Name, &es.CreatedAt, &es.UpdatedAt)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.Query(`SELECT id, set_id, type, name, selector, attribute, url_pattern, sort_order FROM extractors WHERE set_id = ? ORDER BY sort_order`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var e extraction.Extractor
		if err := rows.Scan(&e.ID, &e.SetID, &e.Type, &e.Name, &e.Selector, &e.Attribute, &e.URLPattern, &e.SortOrder); err != nil {
			return nil, err
		}
		es.Extractors = append(es.Extractors, e)
	}
	if es.Extractors == nil {
		es.Extractors = []extraction.Extractor{}
	}
	return &es, nil
}

func (s *Store) CreateExtractorSet(name string, extractors []extraction.Extractor) (*extraction.ExtractorSet, error) {
	now := time.Now().UTC()
	es := &extraction.ExtractorSet{
		ID:        uuid.New().String(),
		Name:      name,
		CreatedAt: now,
		UpdatedAt: now,
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT INTO extractor_sets (id, name, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		es.ID, es.Name, es.CreatedAt, es.UpdatedAt); err != nil {
		return nil, fmt.Errorf("inserting extractor set: %w", err)
	}

	for i, e := range extractors {
		e.ID = uuid.New().String()
		e.SetID = es.ID
		e.SortOrder = i
		if _, err := tx.Exec(`INSERT INTO extractors (id, set_id, type, name, selector, attribute, url_pattern, sort_order) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			e.ID, e.SetID, e.Type, e.Name, e.Selector, e.Attribute, e.URLPattern, e.SortOrder); err != nil {
			return nil, fmt.Errorf("inserting extractor: %w", err)
		}
		es.Extractors = append(es.Extractors, e)
	}
	if es.Extractors == nil {
		es.Extractors = []extraction.Extractor{}
	}

	return es, tx.Commit()
}

func (s *Store) UpdateExtractorSet(id, name string, extractors []extraction.Extractor) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`UPDATE extractor_sets SET name = ?, updated_at = ? WHERE id = ?`, name, time.Now().UTC(), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("extractor set not found")
	}

	if _, err := tx.Exec(`DELETE FROM extractors WHERE set_id = ?`, id); err != nil {
		return err
	}

	for i, e := range extractors {
		eID := uuid.New().String()
		if _, err := tx.Exec(`INSERT INTO extractors (id, set_id, type, name, selector, attribute, url_pattern, sort_order) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			eID, id, e.Type, e.Name, e.Selector, e.Attribute, e.URLPattern, i); err != nil {
			return fmt.Errorf("inserting extractor: %w", err)
		}
	}

	return tx.Commit()
}

func (s *Store) DeleteExtractorSet(id string) error {
	res, err := s.db.Exec(`DELETE FROM extractor_sets WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("extractor set not found")
	}
	return nil
}

// --- Provider Connections ---

func (s *Store) SaveProviderConnection(conn *providers.ProviderConnection) error {
	if conn.ID == "" {
		conn.ID = uuid.New().String()
	}
	_, err := s.db.Exec(`
		INSERT INTO provider_connections (id, project_id, provider, domain, api_key, limit_backlinks, limit_refdomains, limit_rankings, limit_top_pages, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, provider) DO UPDATE SET
			domain = excluded.domain,
			api_key = excluded.api_key,
			limit_backlinks = excluded.limit_backlinks,
			limit_refdomains = excluded.limit_refdomains,
			limit_rankings = excluded.limit_rankings,
			limit_top_pages = excluded.limit_top_pages`,
		conn.ID, conn.ProjectID, conn.Provider, conn.Domain, conn.APIKey,
		conn.LimitBacklinks, conn.LimitRefdomains, conn.LimitRankings, conn.LimitTopPages,
		time.Now().UTC())
	return err
}

func (s *Store) GetProviderConnection(projectID, provider string) (*providers.ProviderConnection, error) {
	var c providers.ProviderConnection
	err := s.db.QueryRow(`
		SELECT id, project_id, provider, domain, api_key, limit_backlinks, limit_refdomains, limit_rankings, limit_top_pages, created_at
		FROM provider_connections WHERE project_id = ? AND provider = ?`, projectID, provider).
		Scan(&c.ID, &c.ProjectID, &c.Provider, &c.Domain, &c.APIKey,
			&c.LimitBacklinks, &c.LimitRefdomains, &c.LimitRankings, &c.LimitTopPages, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) DeleteProviderConnection(projectID, provider string) error {
	res, err := s.db.Exec(`DELETE FROM provider_connections WHERE project_id = ? AND provider = ?`, projectID, provider)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("provider connection not found")
	}
	return nil
}

func (s *Store) ListProviderConnections(projectID string) ([]providers.ProviderConnection, error) {
	rows, err := s.db.Query(`SELECT id, project_id, provider, domain, api_key, limit_backlinks, limit_refdomains, limit_rankings, limit_top_pages, created_at FROM provider_connections WHERE project_id = ? ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var conns []providers.ProviderConnection
	for rows.Next() {
		var c providers.ProviderConnection
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.Provider, &c.Domain, &c.APIKey,
			&c.LimitBacklinks, &c.LimitRefdomains, &c.LimitRankings, &c.LimitTopPages, &c.CreatedAt); err != nil {
			return nil, err
		}
		conns = append(conns, c)
	}
	if conns == nil {
		conns = []providers.ProviderConnection{}
	}
	return conns, nil
}
