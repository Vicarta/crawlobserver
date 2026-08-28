package apikeys

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/SEObserver/crawlobserver/internal/customtests"
	"github.com/SEObserver/crawlobserver/internal/extraction"
	"github.com/SEObserver/crawlobserver/internal/providers"
)

func TestBoundedDeltaSettingsAddsColumnsWithoutChangingCandidateMaximum(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`
		CREATE TABLE project_delta_settings (
			project_id TEXT PRIMARY KEY,
			max_candidates_per_run INTEGER NOT NULL DEFAULT 5000
		);
		INSERT INTO project_delta_settings(project_id, max_candidates_per_run)
		VALUES ('legacy-default', 5000), ('custom', 240);`); err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	legacy.Close()

	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	var historical, custom, changedLimit, canaryCount int
	if err := store.db.QueryRow(`SELECT max_candidates_per_run, sitemap_changed_limit, sitemap_canary_count FROM project_delta_settings WHERE project_id = 'legacy-default'`).Scan(&historical, &changedLimit, &canaryCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT max_candidates_per_run FROM project_delta_settings WHERE project_id = 'custom'`).Scan(&custom); err != nil {
		t.Fatal(err)
	}
	if historical != 5000 || custom != 240 || changedLimit != 0 || canaryCount != 50 {
		t.Fatalf("historical=%d custom=%d changed=%d canaries=%d; want 5000, 240, 0, 50", historical, custom, changedLimit, canaryCount)
	}
	store.Close()

	reopened, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.db.QueryRow(`SELECT max_candidates_per_run FROM project_delta_settings WHERE project_id = 'legacy-default'`).Scan(&historical); err != nil {
		t.Fatal(err)
	}
	if historical != 5000 {
		t.Fatalf("reopened historical maximum = %d; want 5000", historical)
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("newTestStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// --- Projects ---

func TestCreateProject(t *testing.T) {
	s := newTestStore(t)
	p, err := s.CreateProject("my-site")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if p.ID == "" || p.Name != "my-site" {
		t.Fatalf("unexpected project: %+v", p)
	}
}

func TestCreateProjectDuplicate(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreateProject("dup"); err != nil {
		t.Fatal(err)
	}
	_, err := s.CreateProject("dup")
	if err == nil {
		t.Fatal("expected UNIQUE constraint error")
	}
}

func TestListProjectsEmpty(t *testing.T) {
	s := newTestStore(t)
	list, err := s.ListProjects()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("expected 0 projects, got %d", len(list))
	}
}

func TestListProjectsOrdered(t *testing.T) {
	s := newTestStore(t)
	s.CreateProject("first")
	time.Sleep(10 * time.Millisecond)
	s.CreateProject("second")

	list, err := s.ListProjects()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2, got %d", len(list))
	}
	// DESC order: second first
	if list[0].Name != "second" || list[1].Name != "first" {
		t.Fatalf("wrong order: %v, %v", list[0].Name, list[1].Name)
	}
}

func TestListProjectsPaginated(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 5; i++ {
		s.CreateProject("p" + strings.Repeat("x", i))
	}
	list, total, err := s.ListProjectsPaginated(2, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 {
		t.Fatalf("expected total=5, got %d", total)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 results, got %d", len(list))
	}
}

func TestListProjectsPaginatedSearch(t *testing.T) {
	s := newTestStore(t)
	s.CreateProject("alpha-site")
	s.CreateProject("beta-site")
	s.CreateProject("gamma")

	list, total, err := s.ListProjectsPaginated(10, 0, "site")
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("expected total=2, got %d", total)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 results, got %d", len(list))
	}
}

func TestGetProject(t *testing.T) {
	s := newTestStore(t)
	created, _ := s.CreateProject("test")
	got, err := s.GetProject(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "test" {
		t.Fatalf("expected 'test', got %q", got.Name)
	}
}

func TestGetProjectNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetProject("nonexistent")
	if err == nil {
		t.Fatal("expected error for missing project")
	}
}

func TestRenameProject(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.CreateProject("old")
	if err := s.RenameProject(p.ID, "new"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetProject(p.ID)
	if got.Name != "new" {
		t.Fatalf("expected 'new', got %q", got.Name)
	}
}

func TestRenameProjectNotFound(t *testing.T) {
	s := newTestStore(t)
	err := s.RenameProject("nonexistent", "x")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDeleteProject(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.CreateProject("doomed")
	if err := s.DeleteProject(p.ID); err != nil {
		t.Fatal(err)
	}
	_, err := s.GetProject(p.ID)
	if err == nil {
		t.Fatal("expected project to be gone")
	}
}

func TestDeleteProjectCascade(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.CreateProject("proj")
	_, err := s.CreateAPIKey("key1", "project", &p.ID)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteProject(p.ID); err != nil {
		t.Fatal(err)
	}
	keys, _ := s.ListAPIKeys()
	if len(keys) != 0 {
		t.Fatalf("expected 0 keys after cascade, got %d", len(keys))
	}
}

func TestDeleteProjectNotFound(t *testing.T) {
	s := newTestStore(t)
	err := s.DeleteProject("ghost")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestProjectDeltaSettingsSaveAndManualQueue(t *testing.T) {
	s := newTestStore(t)
	p, err := s.CreateProject("delta")
	if err != nil {
		t.Fatal(err)
	}

	defaults, err := s.GetProjectDeltaSettings(p.ID)
	if err != nil {
		t.Fatalf("GetProjectDeltaSettings default: %v", err)
	}
	if defaults.ProjectID != p.ID || defaults.Enabled {
		t.Fatalf("unexpected defaults: %+v", defaults)
	}
	if !defaults.IncludeFooterLinksInPageRank {
		t.Fatal("expected footer links to be included in PageRank by default")
	}

	defaults.Enabled = true
	defaults.ScheduleTime = "04:15"
	defaults.IncludeFooterLinksInPageRank = false
	defaults.CurrentSnapshotMaxDeltas = 7
	defaults.CurrentSnapshotBaselineIntervalDays = 21
	defaults.BlockedURLPatterns = []string{"/search", "utm_"}
	saved, err := s.SaveProjectDeltaSettings(*defaults)
	if err != nil {
		t.Fatalf("SaveProjectDeltaSettings: %v", err)
	}
	if !saved.Enabled || saved.ScheduleTime != "04:15" || saved.IncludeFooterLinksInPageRank || saved.CurrentSnapshotMaxDeltas != 7 || saved.CurrentSnapshotBaselineIntervalDays != 21 || len(saved.BlockedURLPatterns) != 2 {
		t.Fatalf("unexpected saved settings: %+v", saved)
	}

	enabled, err := s.ListEnabledProjectDeltaSettings()
	if err != nil {
		t.Fatalf("ListEnabledProjectDeltaSettings: %v", err)
	}
	if len(enabled) != 1 || enabled[0].ProjectID != p.ID {
		t.Fatalf("unexpected enabled settings: %+v", enabled)
	}

	added, err := s.AddProjectDeltaManualURLs(p.ID, []string{"https://example.com/a", " ", "https://example.com/b"})
	if err != nil {
		t.Fatalf("AddProjectDeltaManualURLs: %v", err)
	}
	if added != 2 {
		t.Fatalf("expected 2 queued URLs, got %d", added)
	}
	urls, err := s.ListProjectDeltaManualURLs(p.ID, 10)
	if err != nil {
		t.Fatalf("ListProjectDeltaManualURLs: %v", err)
	}
	if len(urls) != 2 {
		t.Fatalf("expected 2 URLs, got %d", len(urls))
	}
	if err := s.MarkProjectDeltaManualURLsConsumed(p.ID, urls[:1], time.Now()); err != nil {
		t.Fatalf("MarkProjectDeltaManualURLsConsumed: %v", err)
	}
	remaining, err := s.ListProjectDeltaManualURLs(p.ID, 10)
	if err != nil {
		t.Fatalf("ListProjectDeltaManualURLs after consume: %v", err)
	}
	if len(remaining) != 1 {
		t.Fatalf("expected 1 remaining URL, got %d", len(remaining))
	}
}

func TestProjectDeltaSettingsSitemapRefreshFailureMode(t *testing.T) {
	s := newTestStore(t)
	p, err := s.CreateProject("sitemap-refresh-mode")
	if err != nil {
		t.Fatal(err)
	}

	settings, err := s.GetProjectDeltaSettings(p.ID)
	if err != nil {
		t.Fatalf("GetProjectDeltaSettings default: %v", err)
	}
	if settings.SitemapRefreshFailureMode != SitemapRefreshFailureModeSkip {
		t.Fatalf("default sitemap refresh failure mode = %q, want %q", settings.SitemapRefreshFailureMode, SitemapRefreshFailureModeSkip)
	}

	settings.SitemapRefreshFailureMode = SitemapRefreshFailureModeSnapshotFallback
	saved, err := s.SaveProjectDeltaSettings(*settings)
	if err != nil {
		t.Fatalf("SaveProjectDeltaSettings snapshot fallback: %v", err)
	}
	if saved.SitemapRefreshFailureMode != SitemapRefreshFailureModeSnapshotFallback {
		t.Fatalf("saved sitemap refresh failure mode = %q, want %q", saved.SitemapRefreshFailureMode, SitemapRefreshFailureModeSnapshotFallback)
	}

	if _, err := s.db.Exec(`UPDATE project_delta_settings SET sitemap_refresh_failure_mode = ? WHERE project_id = ?`, "unknown_mode", p.ID); err != nil {
		t.Fatalf("setting invalid persisted mode: %v", err)
	}
	sanitized, err := s.GetProjectDeltaSettings(p.ID)
	if err != nil {
		t.Fatalf("GetProjectDeltaSettings sanitized: %v", err)
	}
	if sanitized.SitemapRefreshFailureMode != SitemapRefreshFailureModeSkip {
		t.Fatalf("sanitized sitemap refresh failure mode = %q, want %q", sanitized.SitemapRefreshFailureMode, SitemapRefreshFailureModeSkip)
	}

	settings.SitemapRefreshFailureMode = "historical_fallback"
	if _, err := s.SaveProjectDeltaSettings(*settings); err == nil || !strings.Contains(err.Error(), "invalid sitemap_refresh_failure_mode") {
		t.Fatalf("SaveProjectDeltaSettings invalid mode error = %v, want actionable validation error", err)
	}
}

func TestProjectDeltaSettingsSitemapRefreshFailureModeMigrationIsIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "apikeys.db")

	first, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("first NewStore: %v", err)
	}
	p, err := first.CreateProject("sitemap-refresh-migration")
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	settings := DefaultProjectDeltaSettings(p.ID)
	settings.SitemapRefreshFailureMode = SitemapRefreshFailureModeSnapshotFallback
	if _, err := first.SaveProjectDeltaSettings(settings); err != nil {
		first.Close()
		t.Fatalf("first SaveProjectDeltaSettings: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("closing first store: %v", err)
	}

	second, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("second NewStore: %v", err)
	}
	defer second.Close()

	got, err := second.GetProjectDeltaSettings(p.ID)
	if err != nil {
		t.Fatalf("GetProjectDeltaSettings after rerun: %v", err)
	}
	if got.SitemapRefreshFailureMode != SitemapRefreshFailureModeSnapshotFallback {
		t.Fatalf("migrated sitemap refresh failure mode = %q, want %q", got.SitemapRefreshFailureMode, SitemapRefreshFailureModeSnapshotFallback)
	}
}

func TestProjectDeltaSettingsSitemapSelectionDefaultsAndBounds(t *testing.T) {
	s := newTestStore(t)
	p, err := s.CreateProject("selection-defaults")
	if err != nil {
		t.Fatal(err)
	}

	defaults, err := s.GetProjectDeltaSettings(p.ID)
	if err != nil {
		t.Fatalf("GetProjectDeltaSettings() error = %v", err)
	}
	if defaults.MaxCandidatesPerRun != 5000 || defaults.SitemapChangedLimit != 0 || defaults.SitemapCanaryCount != 50 {
		t.Fatalf("selection defaults = %+v, want max=5000 changed=all canaries=50", defaults)
	}

	defaults.MaxCandidatesPerRun = 511
	defaults.SitemapChangedLimit = 0
	defaults.SitemapCanaryCount = 0
	saved, err := s.SaveProjectDeltaSettings(*defaults)
	if err != nil {
		t.Fatalf("SaveProjectDeltaSettings() error = %v", err)
	}
	if saved.MaxCandidatesPerRun != 511 || saved.SitemapChangedLimit != 0 || saved.SitemapCanaryCount != 0 {
		t.Fatalf("saved explicit bounds = %+v, want max=511 changed=0 canaries=0", saved)
	}

	saved.MaxCandidatesPerRun = -1
	saved.SitemapChangedLimit = -1
	saved.SitemapCanaryCount = -1
	sanitized, err := s.SaveProjectDeltaSettings(*saved)
	if err != nil {
		t.Fatalf("SaveProjectDeltaSettings negative bounds error = %v", err)
	}
	if sanitized.MaxCandidatesPerRun != 0 || sanitized.SitemapChangedLimit != 0 || sanitized.SitemapCanaryCount != 0 {
		t.Fatalf("negative selection bounds were not sanitized to zero: %+v", sanitized)
	}
}

func TestProjectDeltaSettingsLegacySelectionJSONDefaultsPreserveExplicitZero(t *testing.T) {
	var legacy ProjectDeltaSettings
	if err := json.Unmarshal([]byte(`{"project_id":"legacy"}`), &legacy); err != nil {
		t.Fatalf("Unmarshal legacy settings error = %v", err)
	}
	if legacy.SitemapChangedLimit != 0 || legacy.SitemapCanaryCount != 50 {
		t.Fatalf("legacy selection settings = %+v, want defaults", legacy)
	}

	var explicitZero ProjectDeltaSettings
	if err := json.Unmarshal([]byte(`{"project_id":"zero","sitemap_changed_limit":0,"sitemap_canary_count":0}`), &explicitZero); err != nil {
		t.Fatalf("Unmarshal zero settings error = %v", err)
	}
	if explicitZero.SitemapChangedLimit != 0 || explicitZero.SitemapCanaryCount != 0 {
		t.Fatalf("explicit zero selection settings = %+v, want zero values", explicitZero)
	}
}

func TestProjectQualitySettingsAndCanaries(t *testing.T) {
	s := newTestStore(t)
	p, err := s.CreateProject("quality")
	if err != nil {
		t.Fatal(err)
	}

	defaults, err := s.GetProjectQualitySettings(p.ID)
	if err != nil {
		t.Fatalf("GetProjectQualitySettings default: %v", err)
	}
	if defaults.ProjectID != p.ID || !defaults.Enabled || defaults.MinTrustedScore != 85 {
		t.Fatalf("unexpected quality defaults: %+v", defaults)
	}

	defaults.MinTrustedScore = 90
	defaults.UntrustedScoreBelow = 70
	defaults.CoverageDropPercent = 20
	defaults.InternalLinksMinDelta = 250
	defaults.PageRankTopN = 15
	defaults.CanaryMinInternalLinksDefault = 3
	saved, err := s.SaveProjectQualitySettings(*defaults)
	if err != nil {
		t.Fatalf("SaveProjectQualitySettings: %v", err)
	}
	if saved.MinTrustedScore != 90 || saved.UntrustedScoreBelow != 70 || saved.PageRankTopN != 15 {
		t.Fatalf("unexpected saved quality settings: %+v", saved)
	}

	canaries, err := s.ListProjectCanaries(p.ID)
	if err != nil {
		t.Fatalf("ListProjectCanaries empty: %v", err)
	}
	if len(canaries) != 0 {
		t.Fatalf("expected no canaries, got %d", len(canaries))
	}

	created, err := s.SaveProjectCanary(ProjectCanary{
		ProjectID:        p.ID,
		URL:              " https://example.com/ ",
		ExpectedStatus:   200,
		TitleContains:    "Example",
		MinInternalLinks: 5,
		ExpectIndexable:  true,
		Active:           true,
	})
	if err != nil {
		t.Fatalf("SaveProjectCanary create: %v", err)
	}
	if created.ID == "" || created.URL != "https://example.com/" || !created.Active {
		t.Fatalf("unexpected created canary: %+v", created)
	}

	created.MinInternalLinks = 7
	updated, err := s.SaveProjectCanary(*created)
	if err != nil {
		t.Fatalf("SaveProjectCanary update: %v", err)
	}
	if updated.MinInternalLinks != 7 {
		t.Fatalf("expected updated min links, got %+v", updated)
	}

	canaries, err = s.ListProjectCanaries(p.ID)
	if err != nil {
		t.Fatalf("ListProjectCanaries: %v", err)
	}
	if len(canaries) != 1 || canaries[0].ID != created.ID {
		t.Fatalf("unexpected canaries: %+v", canaries)
	}

	if err := s.DeleteProjectCanary(p.ID, created.ID); err != nil {
		t.Fatalf("DeleteProjectCanary: %v", err)
	}
	canaries, err = s.ListProjectCanaries(p.ID)
	if err != nil {
		t.Fatalf("ListProjectCanaries after delete: %v", err)
	}
	if len(canaries) != 0 {
		t.Fatalf("expected canary deletion, got %+v", canaries)
	}
}

// --- API Keys ---

func TestCreateAPIKeyGeneral(t *testing.T) {
	s := newTestStore(t)
	res, err := s.CreateAPIKey("admin key", "general", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.FullKey, "sk_") {
		t.Fatalf("key should start with sk_, got %q", res.FullKey)
	}
	if len(res.FullKey) != 67 { // sk_ + 64 hex
		t.Fatalf("expected 67 chars, got %d", len(res.FullKey))
	}
	if !strings.HasSuffix(res.KeyPrefix, "...") {
		t.Fatalf("prefix should end with ..., got %q", res.KeyPrefix)
	}
	if res.Type != "general" || res.ProjectID != nil {
		t.Fatalf("unexpected type/project: %s / %v", res.Type, res.ProjectID)
	}
}

func TestCreateAPIKeyProject(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.CreateProject("proj")
	res, err := s.CreateAPIKey("proj key", "project", &p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Type != "project" || res.ProjectID == nil || *res.ProjectID != p.ID {
		t.Fatalf("unexpected: type=%s pid=%v", res.Type, res.ProjectID)
	}
}

func TestCreateAPIKeyTargetedRescanCapability(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.CreateProject("rescan-project")
	res, err := s.CreateAPIKeyWithCapability("rescan adapter", "project", &p.ID, CapabilityTargetedRescan)
	if err != nil {
		t.Fatal(err)
	}
	if res.Type != "project" || res.ProjectID == nil || *res.ProjectID != p.ID || res.Capability != CapabilityTargetedRescan {
		t.Fatalf("unexpected capability key: %#v", res.APIKey)
	}
	lookup := s.ValidateKey(res.FullKey)
	if lookup == nil || lookup.Type != "project" || lookup.ProjectID == nil || *lookup.ProjectID != p.ID || lookup.Capability != CapabilityTargetedRescan {
		t.Fatalf("unexpected capability lookup: %#v", lookup)
	}
	keys, err := s.ListAPIKeys()
	if err != nil || len(keys) != 1 || keys[0].Capability != CapabilityTargetedRescan {
		t.Fatalf("unexpected capability listing: %#v, err=%v", keys, err)
	}
}

func TestCreateAPIKeyRejectsInvalidCapabilities(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.CreateProject("capability-validation")
	for _, tc := range []struct {
		name       string
		keyType    string
		projectID  *string
		capability string
	}{
		{name: "unknown project capability", keyType: "project", projectID: &p.ID, capability: "unknown"},
		{name: "general mutation capability", keyType: "general", capability: CapabilityTargetedRescan},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.CreateAPIKeyWithCapability("bad", tc.keyType, tc.projectID, tc.capability); err == nil {
				t.Fatal("expected capability validation error")
			}
		})
	}
}

func TestAPIKeyCapabilityMigrationFromExistingDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "apikeys.db")
	first, err := NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	project, err := first.CreateProject("capability-migration")
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	if _, err := first.db.Exec("DROP TABLE api_keys"); err != nil {
		first.Close()
		t.Fatal(err)
	}
	if _, err := first.db.Exec(`
		CREATE TABLE api_keys (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			key_hash TEXT NOT NULL UNIQUE,
			key_prefix TEXT NOT NULL,
			type TEXT NOT NULL CHECK(type IN ('general', 'project')),
			project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_used_at DATETIME,
			active INTEGER DEFAULT 1
		)`); err != nil {
		first.Close()
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("reopening existing database: %v", err)
	}
	defer second.Close()
	created, err := second.CreateAPIKeyWithCapability("migrated rescan", "project", &project.ID, CapabilityTargetedRescan)
	if err != nil {
		t.Fatalf("creating capability key after migration: %v", err)
	}
	if lookup := second.ValidateKey(created.FullKey); lookup == nil || lookup.Capability != CapabilityTargetedRescan {
		t.Fatalf("capability unavailable after migration: %#v", lookup)
	}
}

func TestCreateAPIKeyInvalidType(t *testing.T) {
	s := newTestStore(t)
	_, err := s.CreateAPIKey("bad", "invalid", nil)
	if err == nil {
		t.Fatal("expected error for invalid type")
	}
}

func TestCreateAPIKeyProjectWithoutID(t *testing.T) {
	s := newTestStore(t)
	_, err := s.CreateAPIKey("bad", "project", nil)
	if err == nil {
		t.Fatal("expected error when project type lacks project_id")
	}
}

func TestListAPIKeysEmpty(t *testing.T) {
	s := newTestStore(t)
	keys, err := s.ListAPIKeys()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Fatalf("expected 0, got %d", len(keys))
	}
}

func TestListAPIKeysMultiple(t *testing.T) {
	s := newTestStore(t)
	s.CreateAPIKey("k1", "general", nil)
	s.CreateAPIKey("k2", "general", nil)
	keys, err := s.ListAPIKeys()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2, got %d", len(keys))
	}
}

func TestDeleteAPIKey(t *testing.T) {
	s := newTestStore(t)
	res, _ := s.CreateAPIKey("temp", "general", nil)
	if err := s.DeleteAPIKey(res.ID); err != nil {
		t.Fatal(err)
	}
	keys, _ := s.ListAPIKeys()
	if len(keys) != 0 {
		t.Fatalf("expected 0, got %d", len(keys))
	}
}

func TestDeleteAPIKeyNotFound(t *testing.T) {
	s := newTestStore(t)
	err := s.DeleteAPIKey("ghost")
	if err == nil {
		t.Fatal("expected error")
	}
}

// --- Validation ---

func TestValidateKeyGeneral(t *testing.T) {
	s := newTestStore(t)
	res, _ := s.CreateAPIKey("k", "general", nil)
	lookup := s.ValidateKey(res.FullKey)
	if lookup == nil {
		t.Fatal("expected non-nil result")
	}
	if lookup.Type != "general" || lookup.ProjectID != nil {
		t.Fatalf("unexpected: type=%s pid=%v", lookup.Type, lookup.ProjectID)
	}
}

func TestValidateKeyProject(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.CreateProject("proj")
	res, _ := s.CreateAPIKey("k", "project", &p.ID)
	lookup := s.ValidateKey(res.FullKey)
	if lookup == nil {
		t.Fatal("expected non-nil result")
	}
	if lookup.ProjectID == nil || *lookup.ProjectID != p.ID {
		t.Fatal("expected project ID in lookup")
	}
}

func TestValidateKeyInvalid(t *testing.T) {
	s := newTestStore(t)
	if s.ValidateKey("sk_invalid") != nil {
		t.Fatal("expected nil for invalid key")
	}
}

func TestValidateKeyAfterDelete(t *testing.T) {
	s := newTestStore(t)
	res, _ := s.CreateAPIKey("k", "general", nil)
	s.DeleteAPIKey(res.ID)
	if s.ValidateKey(res.FullKey) != nil {
		t.Fatal("expected nil after delete")
	}
}

// --- GSC Connections ---

func TestSaveGSCConnectionInsert(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.CreateProject("proj")
	conn := &GSCConnection{
		ProjectID:    p.ID,
		PropertyURL:  "sc-domain:example.com",
		AccessToken:  "at",
		RefreshToken: "rt",
		TokenExpiry:  time.Now().Add(time.Hour),
	}
	if err := s.SaveGSCConnection(conn); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetGSCConnection(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PropertyURL != "sc-domain:example.com" {
		t.Fatalf("unexpected property: %s", got.PropertyURL)
	}
}

func TestSaveGSCConnectionUpsert(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.CreateProject("proj")
	conn := &GSCConnection{
		ProjectID: p.ID, PropertyURL: "old",
		AccessToken: "a", RefreshToken: "r", TokenExpiry: time.Now(),
	}
	s.SaveGSCConnection(conn)

	conn2 := &GSCConnection{
		ProjectID: p.ID, PropertyURL: "new",
		AccessToken: "a2", RefreshToken: "r2", TokenExpiry: time.Now(),
	}
	if err := s.SaveGSCConnection(conn2); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetGSCConnection(p.ID)
	if got.PropertyURL != "new" {
		t.Fatalf("expected 'new', got %q", got.PropertyURL)
	}
}

func TestSaveGSCFetchCheckpointUpsert(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.CreateProject("proj")
	cp := &GSCFetchCheckpoint{
		ProjectID:     p.ID,
		PropertyURL:   "https://example.com/",
		StartDate:     "2025-01-01",
		EndDate:       "2025-01-31",
		NextStartDate: "2025-01-08",
		RowsFetched:   100,
	}
	if err := s.SaveGSCFetchCheckpoint(cp); err != nil {
		t.Fatal(err)
	}

	cp.NextStartDate = "2025-02-01"
	cp.RowsFetched = 250
	cp.Completed = true
	if err := s.SaveGSCFetchCheckpoint(cp); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetGSCFetchCheckpoint(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.NextStartDate != "2025-02-01" || got.RowsFetched != 250 || !got.Completed {
		t.Fatalf("unexpected checkpoint: %+v", got)
	}
}

func TestDeleteGSCFetchCheckpoint(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.CreateProject("proj")
	if err := s.SaveGSCFetchCheckpoint(&GSCFetchCheckpoint{
		ProjectID:     p.ID,
		PropertyURL:   "https://example.com/",
		StartDate:     "2025-01-01",
		EndDate:       "2025-01-31",
		NextStartDate: "2025-01-08",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteGSCFetchCheckpoint(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetGSCFetchCheckpoint(p.ID); err == nil {
		t.Fatal("expected missing checkpoint after delete")
	}
}

func TestGetGSCConnectionNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetGSCConnection("nonexistent")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDeleteGSCConnection(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.CreateProject("proj")
	s.SaveGSCConnection(&GSCConnection{
		ProjectID: p.ID, PropertyURL: "x",
		AccessToken: "a", RefreshToken: "r", TokenExpiry: time.Now(),
	})
	if err := s.DeleteGSCConnection(p.ID); err != nil {
		t.Fatal(err)
	}
	_, err := s.GetGSCConnection(p.ID)
	if err == nil {
		t.Fatal("expected not found after delete")
	}
}

func TestDeleteGSCConnectionNotFound(t *testing.T) {
	s := newTestStore(t)
	err := s.DeleteGSCConnection("ghost")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestListGSCConnectionsEmpty(t *testing.T) {
	s := newTestStore(t)
	list, err := s.ListGSCConnections()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("expected 0, got %d", len(list))
	}
}

func TestListGSCConnectionsMultiple(t *testing.T) {
	s := newTestStore(t)
	p1, _ := s.CreateProject("p1")
	p2, _ := s.CreateProject("p2")
	s.SaveGSCConnection(&GSCConnection{
		ProjectID: p1.ID, PropertyURL: "a",
		AccessToken: "a", RefreshToken: "r", TokenExpiry: time.Now(),
	})
	s.SaveGSCConnection(&GSCConnection{
		ProjectID: p2.ID, PropertyURL: "b",
		AccessToken: "a", RefreshToken: "r", TokenExpiry: time.Now(),
	})
	list, err := s.ListGSCConnections()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2, got %d", len(list))
	}
}

// --- Rulesets ---

func TestCreateRulesetWithRules(t *testing.T) {
	s := newTestStore(t)
	rules := []customtests.TestRule{
		{Type: "contains", Name: "r1", Value: "val1"},
		{Type: "regex", Name: "r2", Value: "val2"},
	}
	rs, err := s.CreateRuleset("test-rs", rules)
	if err != nil {
		t.Fatal(err)
	}
	if rs.ID == "" || rs.Name != "test-rs" {
		t.Fatalf("unexpected: %+v", rs)
	}
	if len(rs.Rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(rs.Rules))
	}
	for i, r := range rs.Rules {
		if r.ID == "" {
			t.Fatalf("rule %d missing ID", i)
		}
		if r.RulesetID != rs.ID {
			t.Fatalf("rule %d wrong RulesetID", i)
		}
		if r.SortOrder != i {
			t.Fatalf("rule %d wrong SortOrder: %d", i, r.SortOrder)
		}
	}
}

func TestCreateRulesetNoRules(t *testing.T) {
	s := newTestStore(t)
	rs, err := s.CreateRuleset("empty", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Rules) != 0 {
		t.Fatalf("expected 0 rules, got %d", len(rs.Rules))
	}
}

func TestGetRulesetNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetRuleset("nonexistent")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUpdateRuleset(t *testing.T) {
	s := newTestStore(t)
	rs, _ := s.CreateRuleset("orig", []customtests.TestRule{
		{Type: "contains", Name: "old", Value: "v"},
	})
	newRules := []customtests.TestRule{
		{Type: "regex", Name: "new1", Value: "a"},
		{Type: "regex", Name: "new2", Value: "b"},
	}
	if err := s.UpdateRuleset(rs.ID, "renamed", newRules); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetRuleset(rs.ID)
	if got.Name != "renamed" {
		t.Fatalf("expected 'renamed', got %q", got.Name)
	}
	if len(got.Rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(got.Rules))
	}
}

func TestUpdateRulesetNotFound(t *testing.T) {
	s := newTestStore(t)
	err := s.UpdateRuleset("ghost", "x", nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDeleteRuleset(t *testing.T) {
	s := newTestStore(t)
	rs, _ := s.CreateRuleset("del", nil)
	if err := s.DeleteRuleset(rs.ID); err != nil {
		t.Fatal(err)
	}
	_, err := s.GetRuleset(rs.ID)
	if err == nil {
		t.Fatal("expected not found after delete")
	}
}

func TestDeleteRulesetNotFound(t *testing.T) {
	s := newTestStore(t)
	err := s.DeleteRuleset("ghost")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestListRulesetsEmpty(t *testing.T) {
	s := newTestStore(t)
	list, err := s.ListRulesets()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("expected 0, got %d", len(list))
	}
}

// --- Provider Connections ---

func TestSaveProviderConnectionInsert(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.CreateProject("proj")
	conn := &providers.ProviderConnection{
		ProjectID: p.ID,
		Provider:  "seobserver",
		Domain:    "example.com",
		APIKey:    "secret",
	}
	if err := s.SaveProviderConnection(conn); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetProviderConnection(p.ID, "seobserver")
	if err != nil {
		t.Fatal(err)
	}
	if got.Domain != "example.com" {
		t.Fatalf("unexpected domain: %s", got.Domain)
	}
}

func TestSaveProviderConnectionUpsert(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.CreateProject("proj")
	s.SaveProviderConnection(&providers.ProviderConnection{
		ProjectID: p.ID, Provider: "seobserver", Domain: "old.com", APIKey: "k",
	})
	s.SaveProviderConnection(&providers.ProviderConnection{
		ProjectID: p.ID, Provider: "seobserver", Domain: "new.com", APIKey: "k2",
	})
	got, _ := s.GetProviderConnection(p.ID, "seobserver")
	if got.Domain != "new.com" {
		t.Fatalf("expected 'new.com', got %q", got.Domain)
	}
}

func TestGetProviderConnectionNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetProviderConnection("x", "y")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDeleteProviderConnection(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.CreateProject("proj")
	s.SaveProviderConnection(&providers.ProviderConnection{
		ProjectID: p.ID, Provider: "seobserver", Domain: "d", APIKey: "k",
	})
	if err := s.DeleteProviderConnection(p.ID, "seobserver"); err != nil {
		t.Fatal(err)
	}
	_, err := s.GetProviderConnection(p.ID, "seobserver")
	if err == nil {
		t.Fatal("expected not found after delete")
	}
}

func TestDeleteProviderConnectionNotFound(t *testing.T) {
	s := newTestStore(t)
	err := s.DeleteProviderConnection("x", "y")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestListProviderConnectionsEmpty(t *testing.T) {
	s := newTestStore(t)
	list, err := s.ListProviderConnections("proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("expected 0, got %d", len(list))
	}
}

func TestListProviderConnectionsFiltered(t *testing.T) {
	s := newTestStore(t)
	p1, _ := s.CreateProject("p1")
	p2, _ := s.CreateProject("p2")
	s.SaveProviderConnection(&providers.ProviderConnection{
		ProjectID: p1.ID, Provider: "a", Domain: "d", APIKey: "k",
	})
	s.SaveProviderConnection(&providers.ProviderConnection{
		ProjectID: p2.ID, Provider: "b", Domain: "d", APIKey: "k",
	})
	list, _ := s.ListProviderConnections(p1.ID)
	if len(list) != 1 {
		t.Fatalf("expected 1, got %d", len(list))
	}
	if list[0].ProjectID != p1.ID {
		t.Fatal("wrong project in results")
	}
}

// --- Extractor Sets ---

func TestCreateExtractorSetWithExtractors(t *testing.T) {
	s := newTestStore(t)
	extractors := []extraction.Extractor{
		{Type: extraction.CSSExtractText, Name: "title", Selector: "h1"},
		{Type: extraction.CSSExtractAttr, Name: "canonical", Selector: "link[rel=canonical]", Attribute: "href"},
	}
	es, err := s.CreateExtractorSet("my-set", extractors)
	if err != nil {
		t.Fatalf("CreateExtractorSet: %v", err)
	}
	if es.ID == "" || es.Name != "my-set" {
		t.Fatalf("unexpected set: %+v", es)
	}
	if len(es.Extractors) != 2 {
		t.Fatalf("expected 2 extractors, got %d", len(es.Extractors))
	}
	for i, e := range es.Extractors {
		if e.ID == "" {
			t.Fatalf("extractor %d missing ID", i)
		}
		if e.SetID != es.ID {
			t.Fatalf("extractor %d wrong SetID: got %q, want %q", i, e.SetID, es.ID)
		}
		if e.SortOrder != i {
			t.Fatalf("extractor %d wrong SortOrder: got %d, want %d", i, e.SortOrder, i)
		}
	}
}

func TestCreateExtractorSetNoExtractors(t *testing.T) {
	s := newTestStore(t)
	es, err := s.CreateExtractorSet("empty-set", nil)
	if err != nil {
		t.Fatalf("CreateExtractorSet: %v", err)
	}
	if len(es.Extractors) != 0 {
		t.Fatalf("expected 0 extractors, got %d", len(es.Extractors))
	}
}

func TestListExtractorSetsEmpty(t *testing.T) {
	s := newTestStore(t)
	list, err := s.ListExtractorSets()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("expected 0, got %d", len(list))
	}
}

func TestListExtractorSetsAfterCreate(t *testing.T) {
	s := newTestStore(t)
	s.CreateExtractorSet("set-a", nil)
	s.CreateExtractorSet("set-b", []extraction.Extractor{
		{Type: extraction.CSSExtractText, Name: "x", Selector: "p"},
	})
	list, err := s.ListExtractorSets()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2, got %d", len(list))
	}
}

func TestGetExtractorSetExisting(t *testing.T) {
	s := newTestStore(t)
	extractors := []extraction.Extractor{
		{Type: extraction.RegexExtract, Name: "price", Selector: `\$[\d.]+`},
	}
	created, _ := s.CreateExtractorSet("get-test", extractors)
	got, err := s.GetExtractorSet(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "get-test" {
		t.Fatalf("expected 'get-test', got %q", got.Name)
	}
	if len(got.Extractors) != 1 {
		t.Fatalf("expected 1 extractor, got %d", len(got.Extractors))
	}
	if got.Extractors[0].Name != "price" {
		t.Fatalf("expected extractor name 'price', got %q", got.Extractors[0].Name)
	}
}

func TestGetExtractorSetNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetExtractorSet("nonexistent")
	if err == nil {
		t.Fatal("expected error for missing extractor set")
	}
}

func TestUpdateExtractorSet(t *testing.T) {
	s := newTestStore(t)
	es, _ := s.CreateExtractorSet("orig", []extraction.Extractor{
		{Type: extraction.CSSExtractText, Name: "old", Selector: "h1"},
	})
	newExtractors := []extraction.Extractor{
		{Type: extraction.CSSExtractAttr, Name: "new1", Selector: "a", Attribute: "href"},
		{Type: extraction.RegexExtract, Name: "new2", Selector: `\d+`},
	}
	if err := s.UpdateExtractorSet(es.ID, "renamed", newExtractors); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetExtractorSet(es.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "renamed" {
		t.Fatalf("expected 'renamed', got %q", got.Name)
	}
	if len(got.Extractors) != 2 {
		t.Fatalf("expected 2 extractors, got %d", len(got.Extractors))
	}
	if got.Extractors[0].Name != "new1" || got.Extractors[1].Name != "new2" {
		t.Fatalf("unexpected extractor names: %q, %q", got.Extractors[0].Name, got.Extractors[1].Name)
	}
}

func TestUpdateExtractorSetNotFound(t *testing.T) {
	s := newTestStore(t)
	err := s.UpdateExtractorSet("ghost", "x", nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDeleteExtractorSet(t *testing.T) {
	s := newTestStore(t)
	es, _ := s.CreateExtractorSet("doomed", nil)
	if err := s.DeleteExtractorSet(es.ID); err != nil {
		t.Fatal(err)
	}
	_, err := s.GetExtractorSet(es.ID)
	if err == nil {
		t.Fatal("expected not found after delete")
	}
}

func TestDeleteExtractorSetNotFound(t *testing.T) {
	s := newTestStore(t)
	err := s.DeleteExtractorSet("ghost")
	if err == nil {
		t.Fatal("expected error")
	}
}

// --- Database file permissions ---

func TestNewStoreFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permissions not enforced on Windows")
	}

	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer s.Close()

	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("expected permissions 0600, got %04o", perm)
	}
}

func TestNewStoreFixesLoosePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permissions not enforced on Windows")
	}

	dbPath := filepath.Join(t.TempDir(), "loose.db")

	// Create a file with overly permissive mode
	f, err := os.OpenFile(dbPath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	f.Close()

	s, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer s.Close()

	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("expected permissions tightened to 0600, got %04o", perm)
	}
}
