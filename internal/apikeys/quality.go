package apikeys

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type ProjectQualitySettings struct {
	ProjectID                     string    `json:"project_id"`
	Enabled                       bool      `json:"enabled"`
	MinTrustedScore               int       `json:"min_trusted_score"`
	UntrustedScoreBelow           int       `json:"untrusted_score_below"`
	CoverageDropPercent           float64   `json:"coverage_drop_percent"`
	CoverageGrowthPercent         float64   `json:"coverage_growth_percent"`
	CoverageMinPagesDelta         int       `json:"coverage_min_pages_delta"`
	InternalLinksDropPercent      float64   `json:"internal_links_drop_percent"`
	InternalLinksMinDelta         int       `json:"internal_links_min_delta"`
	Status404Percent              float64   `json:"status_404_percent"`
	Status404MinDelta             int       `json:"status_404_min_delta"`
	Status5xxPercent              float64   `json:"status_5xx_percent"`
	Status5xxMinDelta             int       `json:"status_5xx_min_delta"`
	NoindexPercent                float64   `json:"noindex_percent"`
	NoindexMinDelta               int       `json:"noindex_min_delta"`
	RedirectPercent               float64   `json:"redirect_percent"`
	RedirectMinDelta              int       `json:"redirect_min_delta"`
	CanonicalMismatchPercent      float64   `json:"canonical_mismatch_percent"`
	CanonicalMismatchMinDelta     int       `json:"canonical_mismatch_min_delta"`
	PageRankTopN                  int       `json:"pagerank_top_n"`
	PageRankTopOverlapMinPercent  float64   `json:"pagerank_top_overlap_min_percent"`
	PageRankZeroTopPagesMax       int       `json:"pagerank_zero_top_pages_max"`
	CanaryMinInternalLinksDefault int       `json:"canary_min_internal_links_default"`
	DeltaMinCrawledPages          int       `json:"delta_min_crawled_pages"`
	DeltaMinCrawledPercent        float64   `json:"delta_min_crawled_percent"`
	DeltaMinLaunchedCandidates    int       `json:"delta_min_launched_candidates"`
	DeltaMinLaunchedPercent       float64   `json:"delta_min_launched_percent"`
	DeltaMinSitemapCandidates     int       `json:"delta_min_sitemap_candidates"`
	DeltaMinSitemapPercent        float64   `json:"delta_min_sitemap_percent"`
	DeltaCandidateCoveragePercent float64   `json:"delta_candidate_coverage_percent"`
	DeltaStatus5xxPercent         float64   `json:"delta_status_5xx_percent"`
	DeltaStatus5xxMinPages        int       `json:"delta_status_5xx_min_pages"`
	DeltaRequireCanaries          bool      `json:"delta_require_canaries"`
	UpdatedAt                     time.Time `json:"updated_at"`
}

type ProjectCanary struct {
	ID                string    `json:"id"`
	ProjectID         string    `json:"project_id"`
	URL               string    `json:"url"`
	ExpectedStatus    int       `json:"expected_status"`
	ExpectedFinalURL  string    `json:"expected_final_url"`
	ExpectedCanonical string    `json:"expected_canonical"`
	TitleContains     string    `json:"title_contains"`
	MinInternalLinks  int       `json:"min_internal_links"`
	ExpectIndexable   bool      `json:"expect_indexable"`
	Active            bool      `json:"active"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func DefaultProjectQualitySettings(projectID string) ProjectQualitySettings {
	return ProjectQualitySettings{
		ProjectID:                     projectID,
		Enabled:                       true,
		MinTrustedScore:               85,
		UntrustedScoreBelow:           60,
		CoverageDropPercent:           30,
		CoverageGrowthPercent:         50,
		CoverageMinPagesDelta:         50,
		InternalLinksDropPercent:      25,
		InternalLinksMinDelta:         500,
		Status404Percent:              10,
		Status404MinDelta:             25,
		Status5xxPercent:              5,
		Status5xxMinDelta:             5,
		NoindexPercent:                10,
		NoindexMinDelta:               25,
		RedirectPercent:               15,
		RedirectMinDelta:              25,
		CanonicalMismatchPercent:      10,
		CanonicalMismatchMinDelta:     25,
		PageRankTopN:                  20,
		PageRankTopOverlapMinPercent:  50,
		PageRankZeroTopPagesMax:       2,
		CanaryMinInternalLinksDefault: 1,
		DeltaMinCrawledPages:          5,
		DeltaMinCrawledPercent:        50,
		DeltaMinLaunchedCandidates:    0,
		DeltaMinLaunchedPercent:       0,
		DeltaMinSitemapCandidates:     1,
		DeltaMinSitemapPercent:        30,
		DeltaCandidateCoveragePercent: 100,
		DeltaStatus5xxPercent:         5,
		DeltaStatus5xxMinPages:        5,
		DeltaRequireCanaries:          false,
		UpdatedAt:                     time.Now().UTC(),
	}
}

func sanitizeQualitySettings(in ProjectQualitySettings) ProjectQualitySettings {
	out := in
	if out.MinTrustedScore <= 0 || out.MinTrustedScore > 100 {
		out.MinTrustedScore = 85
	}
	if out.UntrustedScoreBelow <= 0 || out.UntrustedScoreBelow > 100 {
		out.UntrustedScoreBelow = 60
	}
	if out.UntrustedScoreBelow > out.MinTrustedScore {
		out.UntrustedScoreBelow = out.MinTrustedScore
	}
	out.CoverageDropPercent = clampPercent(out.CoverageDropPercent, 30)
	out.CoverageGrowthPercent = clampPercent(out.CoverageGrowthPercent, 50)
	out.InternalLinksDropPercent = clampPercent(out.InternalLinksDropPercent, 25)
	out.Status404Percent = clampPercent(out.Status404Percent, 10)
	out.Status5xxPercent = clampPercent(out.Status5xxPercent, 5)
	out.NoindexPercent = clampPercent(out.NoindexPercent, 10)
	out.RedirectPercent = clampPercent(out.RedirectPercent, 15)
	out.CanonicalMismatchPercent = clampPercent(out.CanonicalMismatchPercent, 10)
	out.PageRankTopOverlapMinPercent = clampPercent(out.PageRankTopOverlapMinPercent, 50)
	if out.CoverageMinPagesDelta < 0 {
		out.CoverageMinPagesDelta = 0
	}
	if out.InternalLinksMinDelta < 0 {
		out.InternalLinksMinDelta = 0
	}
	if out.Status404MinDelta < 0 {
		out.Status404MinDelta = 0
	}
	if out.Status5xxMinDelta < 0 {
		out.Status5xxMinDelta = 0
	}
	if out.NoindexMinDelta < 0 {
		out.NoindexMinDelta = 0
	}
	if out.RedirectMinDelta < 0 {
		out.RedirectMinDelta = 0
	}
	if out.CanonicalMismatchMinDelta < 0 {
		out.CanonicalMismatchMinDelta = 0
	}
	if out.PageRankTopN <= 0 {
		out.PageRankTopN = 20
	}
	if out.PageRankZeroTopPagesMax < 0 {
		out.PageRankZeroTopPagesMax = 0
	}
	if out.CanaryMinInternalLinksDefault < 0 {
		out.CanaryMinInternalLinksDefault = 0
	}
	if out.DeltaMinCrawledPages < 0 {
		out.DeltaMinCrawledPages = 0
	}
	out.DeltaMinCrawledPercent = clampPercent(out.DeltaMinCrawledPercent, 50)
	if out.DeltaMinLaunchedCandidates < 0 {
		out.DeltaMinLaunchedCandidates = 0
	}
	out.DeltaMinLaunchedPercent = clampPercentAllowZero(out.DeltaMinLaunchedPercent)
	if out.DeltaMinSitemapCandidates < 0 {
		out.DeltaMinSitemapCandidates = 0
	}
	out.DeltaMinSitemapPercent = clampPercentAllowZero(out.DeltaMinSitemapPercent)
	out.DeltaCandidateCoveragePercent = clampPercent(out.DeltaCandidateCoveragePercent, 100)
	out.DeltaStatus5xxPercent = clampPercent(out.DeltaStatus5xxPercent, 5)
	if out.DeltaStatus5xxMinPages < 0 {
		out.DeltaStatus5xxMinPages = 0
	}
	return out
}

func clampPercent(v, fallback float64) float64 {
	if v <= 0 || v > 100 {
		return fallback
	}
	return v
}

func clampPercentAllowZero(v float64) float64 {
	if v < 0 || v > 100 {
		return 0
	}
	return v
}

func (s *Store) GetProjectQualitySettings(projectID string) (*ProjectQualitySettings, error) {
	defaults := DefaultProjectQualitySettings(projectID)
	row := s.db.QueryRow(`
		SELECT project_id, enabled, min_trusted_score, untrusted_score_below,
			coverage_drop_percent, coverage_growth_percent, coverage_min_pages_delta,
			internal_links_drop_percent, internal_links_min_delta,
			status_404_percent, status_404_min_delta, status_5xx_percent, status_5xx_min_delta,
			noindex_percent, noindex_min_delta,
			redirect_percent, redirect_min_delta, canonical_mismatch_percent, canonical_mismatch_min_delta,
			pagerank_top_n, pagerank_top_overlap_min_percent, pagerank_zero_top_pages_max,
			canary_min_internal_links_default,
			delta_min_crawled_pages, delta_min_crawled_percent,
			delta_min_launched_candidates, delta_min_launched_percent,
			delta_min_sitemap_candidates, delta_min_sitemap_percent,
			delta_candidate_coverage_percent, delta_status_5xx_percent, delta_status_5xx_min_pages,
			delta_require_canaries,
			updated_at
		FROM project_quality_settings WHERE project_id = ?`, projectID)

	var st ProjectQualitySettings
	var enabled, deltaRequireCanaries int
	if err := row.Scan(
		&st.ProjectID, &enabled, &st.MinTrustedScore, &st.UntrustedScoreBelow,
		&st.CoverageDropPercent, &st.CoverageGrowthPercent, &st.CoverageMinPagesDelta,
		&st.InternalLinksDropPercent, &st.InternalLinksMinDelta,
		&st.Status404Percent, &st.Status404MinDelta, &st.Status5xxPercent, &st.Status5xxMinDelta,
		&st.NoindexPercent, &st.NoindexMinDelta,
		&st.RedirectPercent, &st.RedirectMinDelta, &st.CanonicalMismatchPercent, &st.CanonicalMismatchMinDelta,
		&st.PageRankTopN, &st.PageRankTopOverlapMinPercent, &st.PageRankZeroTopPagesMax,
		&st.CanaryMinInternalLinksDefault,
		&st.DeltaMinCrawledPages, &st.DeltaMinCrawledPercent,
		&st.DeltaMinLaunchedCandidates, &st.DeltaMinLaunchedPercent,
		&st.DeltaMinSitemapCandidates, &st.DeltaMinSitemapPercent,
		&st.DeltaCandidateCoveragePercent, &st.DeltaStatus5xxPercent, &st.DeltaStatus5xxMinPages,
		&deltaRequireCanaries,
		&st.UpdatedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return &defaults, nil
		}
		return nil, err
	}
	st.Enabled = enabled != 0
	st.DeltaRequireCanaries = deltaRequireCanaries != 0
	return &st, nil
}

func (s *Store) SaveProjectQualitySettings(settings ProjectQualitySettings) (*ProjectQualitySettings, error) {
	if settings.ProjectID == "" {
		return nil, fmt.Errorf("project_id is required")
	}
	settings = sanitizeQualitySettings(settings)
	now := time.Now().UTC()
	_, err := s.db.Exec(`
		INSERT INTO project_quality_settings (
			project_id, enabled, min_trusted_score, untrusted_score_below,
			coverage_drop_percent, coverage_growth_percent, coverage_min_pages_delta,
			internal_links_drop_percent, internal_links_min_delta,
			status_404_percent, status_404_min_delta, status_5xx_percent, status_5xx_min_delta,
			noindex_percent, noindex_min_delta,
			redirect_percent, redirect_min_delta, canonical_mismatch_percent, canonical_mismatch_min_delta,
			pagerank_top_n, pagerank_top_overlap_min_percent, pagerank_zero_top_pages_max,
			canary_min_internal_links_default,
			delta_min_crawled_pages, delta_min_crawled_percent,
			delta_min_launched_candidates, delta_min_launched_percent,
			delta_min_sitemap_candidates, delta_min_sitemap_percent,
			delta_candidate_coverage_percent, delta_status_5xx_percent, delta_status_5xx_min_pages,
			delta_require_canaries,
			updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET
			enabled = excluded.enabled,
			min_trusted_score = excluded.min_trusted_score,
			untrusted_score_below = excluded.untrusted_score_below,
			coverage_drop_percent = excluded.coverage_drop_percent,
			coverage_growth_percent = excluded.coverage_growth_percent,
			coverage_min_pages_delta = excluded.coverage_min_pages_delta,
			internal_links_drop_percent = excluded.internal_links_drop_percent,
			internal_links_min_delta = excluded.internal_links_min_delta,
			status_404_percent = excluded.status_404_percent,
			status_404_min_delta = excluded.status_404_min_delta,
			status_5xx_percent = excluded.status_5xx_percent,
			status_5xx_min_delta = excluded.status_5xx_min_delta,
			noindex_percent = excluded.noindex_percent,
			noindex_min_delta = excluded.noindex_min_delta,
			redirect_percent = excluded.redirect_percent,
			redirect_min_delta = excluded.redirect_min_delta,
			canonical_mismatch_percent = excluded.canonical_mismatch_percent,
			canonical_mismatch_min_delta = excluded.canonical_mismatch_min_delta,
			pagerank_top_n = excluded.pagerank_top_n,
			pagerank_top_overlap_min_percent = excluded.pagerank_top_overlap_min_percent,
			pagerank_zero_top_pages_max = excluded.pagerank_zero_top_pages_max,
			canary_min_internal_links_default = excluded.canary_min_internal_links_default,
			delta_min_crawled_pages = excluded.delta_min_crawled_pages,
			delta_min_crawled_percent = excluded.delta_min_crawled_percent,
			delta_min_launched_candidates = excluded.delta_min_launched_candidates,
			delta_min_launched_percent = excluded.delta_min_launched_percent,
			delta_min_sitemap_candidates = excluded.delta_min_sitemap_candidates,
			delta_min_sitemap_percent = excluded.delta_min_sitemap_percent,
			delta_candidate_coverage_percent = excluded.delta_candidate_coverage_percent,
			delta_status_5xx_percent = excluded.delta_status_5xx_percent,
			delta_status_5xx_min_pages = excluded.delta_status_5xx_min_pages,
			delta_require_canaries = excluded.delta_require_canaries,
			updated_at = excluded.updated_at`,
		settings.ProjectID, boolInt(settings.Enabled), settings.MinTrustedScore, settings.UntrustedScoreBelow,
		settings.CoverageDropPercent, settings.CoverageGrowthPercent, settings.CoverageMinPagesDelta,
		settings.InternalLinksDropPercent, settings.InternalLinksMinDelta,
		settings.Status404Percent, settings.Status404MinDelta, settings.Status5xxPercent, settings.Status5xxMinDelta,
		settings.NoindexPercent, settings.NoindexMinDelta,
		settings.RedirectPercent, settings.RedirectMinDelta, settings.CanonicalMismatchPercent, settings.CanonicalMismatchMinDelta,
		settings.PageRankTopN, settings.PageRankTopOverlapMinPercent, settings.PageRankZeroTopPagesMax,
		settings.CanaryMinInternalLinksDefault,
		settings.DeltaMinCrawledPages, settings.DeltaMinCrawledPercent,
		settings.DeltaMinLaunchedCandidates, settings.DeltaMinLaunchedPercent,
		settings.DeltaMinSitemapCandidates, settings.DeltaMinSitemapPercent,
		settings.DeltaCandidateCoveragePercent, settings.DeltaStatus5xxPercent, settings.DeltaStatus5xxMinPages,
		boolInt(settings.DeltaRequireCanaries),
		now,
	)
	if err != nil {
		return nil, err
	}
	return s.GetProjectQualitySettings(settings.ProjectID)
}

func (s *Store) ListProjectCanaries(projectID string) ([]ProjectCanary, error) {
	rows, err := s.db.Query(`
		SELECT id, project_id, url, expected_status, expected_final_url, expected_canonical,
			title_contains, min_internal_links, expect_indexable, active, created_at, updated_at
		FROM project_canaries WHERE project_id = ? ORDER BY created_at ASC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var canaries []ProjectCanary
	for rows.Next() {
		c, err := scanCanary(rows)
		if err != nil {
			return nil, err
		}
		canaries = append(canaries, *c)
	}
	if canaries == nil {
		canaries = []ProjectCanary{}
	}
	return canaries, rows.Err()
}

func (s *Store) SaveProjectCanary(canary ProjectCanary) (*ProjectCanary, error) {
	if canary.ProjectID == "" {
		return nil, fmt.Errorf("project_id is required")
	}
	canary.URL = strings.TrimSpace(canary.URL)
	if canary.URL == "" {
		return nil, fmt.Errorf("url is required")
	}
	if canary.ID == "" {
		canary.ID = uuid.New().String()
	}
	if canary.ExpectedStatus <= 0 {
		canary.ExpectedStatus = 200
	}
	if canary.MinInternalLinks < 0 {
		canary.MinInternalLinks = 0
	}
	now := time.Now().UTC()
	_, err := s.db.Exec(`
		INSERT INTO project_canaries (
			id, project_id, url, expected_status, expected_final_url, expected_canonical,
			title_contains, min_internal_links, expect_indexable, active, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			url = excluded.url,
			expected_status = excluded.expected_status,
			expected_final_url = excluded.expected_final_url,
			expected_canonical = excluded.expected_canonical,
			title_contains = excluded.title_contains,
			min_internal_links = excluded.min_internal_links,
			expect_indexable = excluded.expect_indexable,
			active = excluded.active,
			updated_at = excluded.updated_at`,
		canary.ID, canary.ProjectID, canary.URL, canary.ExpectedStatus, canary.ExpectedFinalURL, canary.ExpectedCanonical,
		canary.TitleContains, canary.MinInternalLinks, boolInt(canary.ExpectIndexable), boolInt(canary.Active), now, now,
	)
	if err != nil {
		return nil, err
	}
	return s.GetProjectCanary(canary.ProjectID, canary.ID)
}

func (s *Store) GetProjectCanary(projectID, id string) (*ProjectCanary, error) {
	row := s.db.QueryRow(`
		SELECT id, project_id, url, expected_status, expected_final_url, expected_canonical,
			title_contains, min_internal_links, expect_indexable, active, created_at, updated_at
		FROM project_canaries WHERE project_id = ? AND id = ?`, projectID, id)
	return scanCanary(row)
}

func (s *Store) DeleteProjectCanary(projectID, id string) error {
	res, err := s.db.Exec(`DELETE FROM project_canaries WHERE project_id = ? AND id = ?`, projectID, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("canary not found")
	}
	return nil
}

type canaryScanner interface {
	Scan(dest ...interface{}) error
}

func scanCanary(row canaryScanner) (*ProjectCanary, error) {
	var c ProjectCanary
	var expectIndexable, active int
	if err := row.Scan(
		&c.ID, &c.ProjectID, &c.URL, &c.ExpectedStatus, &c.ExpectedFinalURL, &c.ExpectedCanonical,
		&c.TitleContains, &c.MinInternalLinks, &expectIndexable, &active, &c.CreatedAt, &c.UpdatedAt,
	); err != nil {
		return nil, err
	}
	c.ExpectIndexable = expectIndexable != 0
	c.Active = active != 0
	return &c, nil
}
