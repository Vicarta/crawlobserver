package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/SEObserver/crawlobserver/internal/applog"
	"github.com/SEObserver/crawlobserver/internal/config"
	"github.com/google/uuid"
)

const (
	CurrentSnapshotLabel           = "Current Snapshot"
	CurrentBaselineSnapshotLabel   = "Current Baseline Snapshot"
	deltaSitemapSelectorRevisionV1 = "v1"
	deltaSitemapSelectorRevisionV2 = "v2"
)

var currentSnapshotPromotionLocks sync.Map

var ErrCurrentSnapshotBindingConflict = errors.New("current snapshot quality binding conflict")
var ErrCurrentSnapshotSourceSuperseded = errors.New("current snapshot source is superseded")

func currentSnapshotPromotionLock(projectID string) *sync.Mutex {
	lock, _ := currentSnapshotPromotionLocks.LoadOrStore(projectID, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

// CanPromoteCurrentSnapshotSource is a durable preflight for callers. The
// journal remains authoritative: concurrent writers may both insert, but the
// greatest persisted watermark is the only readable pointer after restart or
// FINAL compaction.
func (s *Store) CanPromoteCurrentSnapshotSource(ctx context.Context, projectID, candidateSessionID string) (bool, *ProjectCurrentSnapshot, error) {
	candidate, err := s.GetSession(ctx, candidateSessionID)
	if err != nil {
		return false, nil, err
	}
	if candidate.ProjectID == nil || *candidate.ProjectID != projectID {
		return false, nil, fmt.Errorf("candidate session does not belong to project")
	}
	current, err := s.GetProjectCurrentSnapshot(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	if compareSnapshotSource(candidate.StartedAt, candidate.ID, current.ContentWatermarkStartedAt, current.ContentWatermarkSessionID) < 0 {
		return false, current, nil
	}
	return true, current, nil
}

func compareSnapshotSource(leftAt time.Time, leftID string, rightAt time.Time, rightID string) int {
	if leftAt.Before(rightAt) {
		return -1
	}
	if leftAt.After(rightAt) {
		return 1
	}
	if leftID < rightID {
		return -1
	}
	if leftID > rightID {
		return 1
	}
	return 0
}

const snapshotPageColumns = `crawl_session_id, url, final_url, status_code, content_type,
	title, title_length, canonical, canonical_is_self, is_indexable, index_reason,
	meta_robots, meta_description, meta_desc_length, meta_keywords,
	h1, h2, h3, h4, h5, h6,
	word_count, internal_links_out, external_links_out,
	images_count, images_no_alt, hreflang,
	lang, og_title, og_description, og_image, schema_types,
	page_created_at, page_modified_at,
	headers, redirect_chain, body_size, fetch_duration_ms,
	content_encoding, x_robots_tag,
	error, depth, found_on, pagerank, pagerank_revision, content_hash, body_html, body_truncated, crawled_at,
	js_rendered, js_render_duration_ms, js_render_error,
	rendered_title, rendered_meta_description, rendered_h1,
	rendered_word_count, rendered_links_count, rendered_images_count,
	rendered_canonical, rendered_meta_robots, rendered_schema_types,
	rendered_body_html,
	static_title, static_meta_description, static_h1,
	static_word_count, static_canonical, static_meta_robots,
	static_links_count, static_images_count, static_content_hash, static_body_html,
	js_changed_title, js_changed_description, js_changed_h1,
	js_changed_canonical, js_changed_content,
	js_added_links, js_added_images, js_added_schema,
	schema_valid_count, schema_error_count, schema_warning_count,
	cwv_lcp_ms, cwv_cls, cwv_ttfb_ms, cwv_measured`

const snapshotPageSelectColumns = `url, final_url, status_code, content_type,
	title, title_length, canonical, canonical_is_self, is_indexable, index_reason,
	meta_robots, meta_description, meta_desc_length, meta_keywords,
	h1, h2, h3, h4, h5, h6,
	word_count, internal_links_out, external_links_out,
	images_count, images_no_alt, hreflang,
	lang, og_title, og_description, og_image, schema_types,
	page_created_at, page_modified_at,
	headers, redirect_chain, body_size, fetch_duration_ms,
	content_encoding, x_robots_tag,
	error, depth, found_on, pagerank, pagerank_revision, content_hash, body_html, body_truncated, crawled_at,
	js_rendered, js_render_duration_ms, js_render_error,
	rendered_title, rendered_meta_description, rendered_h1,
	rendered_word_count, rendered_links_count, rendered_images_count,
	rendered_canonical, rendered_meta_robots, rendered_schema_types,
	rendered_body_html,
	static_title, static_meta_description, static_h1,
	static_word_count, static_canonical, static_meta_robots,
	static_links_count, static_images_count, static_content_hash, static_body_html,
	js_changed_title, js_changed_description, js_changed_h1,
	js_changed_canonical, js_changed_content,
	js_added_links, js_added_images, js_added_schema,
	schema_valid_count, schema_error_count, schema_warning_count,
	cwv_lcp_ms, cwv_cls, cwv_ttfb_ms, cwv_measured`

func (s *Store) GetProjectCurrentSnapshot(ctx context.Context, projectID string) (*ProjectCurrentSnapshot, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT project_id, snapshot_revision, toString(source_session_id), source_started_at,
			toString(content_watermark_session_id), content_watermark_started_at,
			toString(current_session_id), baseline_session_id,
			quality_baseline_session_id,
			quality_evaluation_revision, baseline_quality_evaluation_revision,
			pagerank_evidence_revision, quality_evaluator_revision, quality_rules_revision,
			quality_promotion_status, baseline_created_at,
			last_delta_session_id, delta_count, updated_at
		FROM crawlobserver.project_current_snapshot_promotions_v2 FINAL
		WHERE project_id = ?
		ORDER BY content_watermark_started_at DESC, toString(content_watermark_session_id) DESC, snapshot_revision DESC, updated_at DESC
		LIMIT 1`, projectID)
	var snap ProjectCurrentSnapshot
	if err := row.Scan(
		&snap.ProjectID, &snap.SnapshotRevision, &snap.SourceSessionID, &snap.SourceStartedAt,
		&snap.ContentWatermarkSessionID, &snap.ContentWatermarkStartedAt,
		&snap.CurrentSessionID, &snap.BaselineSessionID,
		&snap.QualityBaselineSessionID,
		&snap.QualityEvaluationRevision, &snap.BaselineQualityEvaluationRevision,
		&snap.PageRankEvidenceRevision, &snap.QualityEvaluatorRevision, &snap.QualityRulesRevision,
		&snap.QualityPromotionStatus, &snap.BaselineCreatedAt,
		&snap.LastDeltaSessionID, &snap.DeltaCount, &snap.UpdatedAt,
	); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return s.migrateLegacyProjectCurrentSnapshot(ctx, projectID)
	}
	if !isValidUUID(snap.SourceSessionID) || snap.SourceStartedAt.IsZero() ||
		!isValidUUID(snap.ContentWatermarkSessionID) || snap.ContentWatermarkStartedAt.IsZero() {
		return nil, fmt.Errorf("%w: snapshot source lineage is incomplete", ErrCurrentSnapshotBindingConflict)
	}
	return &snap, nil
}

// GetProjectCurrentSnapshotRevision reads one immutable journal revision.
// Unlike the canonical getter it must not guess when a malformed concurrent
// write produced duplicate revision values.
func (s *Store) GetProjectCurrentSnapshotRevision(ctx context.Context, projectID string, snapshotRevision uint64) (*ProjectCurrentSnapshot, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT project_id, snapshot_revision, toString(source_session_id), source_started_at,
			toString(content_watermark_session_id), content_watermark_started_at,
			toString(current_session_id), baseline_session_id, quality_baseline_session_id,
			quality_evaluation_revision, baseline_quality_evaluation_revision,
			pagerank_evidence_revision, quality_evaluator_revision, quality_rules_revision,
			quality_promotion_status, baseline_created_at, last_delta_session_id, delta_count, updated_at
		FROM crawlobserver.project_current_snapshot_promotions_v2 FINAL
		WHERE project_id = ? AND snapshot_revision = ?
		LIMIT 2`, projectID, snapshotRevision)
	if err != nil {
		return nil, fmt.Errorf("querying current snapshot journal revision: %w", err)
	}
	defer rows.Close()
	var snapshots []ProjectCurrentSnapshot
	for rows.Next() {
		var snap ProjectCurrentSnapshot
		if err := rows.Scan(&snap.ProjectID, &snap.SnapshotRevision, &snap.SourceSessionID, &snap.SourceStartedAt,
			&snap.ContentWatermarkSessionID, &snap.ContentWatermarkStartedAt, &snap.CurrentSessionID, &snap.BaselineSessionID,
			&snap.QualityBaselineSessionID, &snap.QualityEvaluationRevision, &snap.BaselineQualityEvaluationRevision,
			&snap.PageRankEvidenceRevision, &snap.QualityEvaluatorRevision, &snap.QualityRulesRevision,
			&snap.QualityPromotionStatus, &snap.BaselineCreatedAt, &snap.LastDeltaSessionID, &snap.DeltaCount, &snap.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning current snapshot journal revision: %w", err)
		}
		snapshots = append(snapshots, snap)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating current snapshot journal revision: %w", err)
	}
	if len(snapshots) != 1 || !isValidUUID(snapshots[0].SourceSessionID) || !isValidUUID(snapshots[0].ContentWatermarkSessionID) {
		return nil, fmt.Errorf("%w: snapshot revision is missing or ambiguous", ErrCurrentSnapshotBindingConflict)
	}
	return &snapshots[0], nil
}

// migrateLegacyProjectCurrentSnapshot imports only legacy pointers whose raw
// source can be proved uniquely from their immutable quality evaluation. A
// guessed source would let an old pointer bypass the monotonic journal.
func (s *Store) migrateLegacyProjectCurrentSnapshot(ctx context.Context, projectID string) (*ProjectCurrentSnapshot, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT project_id, snapshot_revision, source_session_id, source_started_at,
			content_watermark_session_id, content_watermark_started_at,
			toString(current_session_id), baseline_session_id, quality_baseline_session_id,
			quality_evaluation_revision, baseline_quality_evaluation_revision,
			pagerank_evidence_revision, quality_evaluator_revision, quality_rules_revision,
			quality_promotion_status, baseline_created_at, last_delta_session_id, delta_count, updated_at
		FROM crawlobserver.project_current_snapshots FINAL
		WHERE project_id = ?
		ORDER BY snapshot_revision DESC, updated_at DESC LIMIT 1`, projectID)
	var snap ProjectCurrentSnapshot
	if err := row.Scan(&snap.ProjectID, &snap.SnapshotRevision, &snap.SourceSessionID, &snap.SourceStartedAt,
		&snap.ContentWatermarkSessionID, &snap.ContentWatermarkStartedAt, &snap.CurrentSessionID, &snap.BaselineSessionID,
		&snap.QualityBaselineSessionID, &snap.QualityEvaluationRevision, &snap.BaselineQualityEvaluationRevision,
		&snap.PageRankEvidenceRevision, &snap.QualityEvaluatorRevision, &snap.QualityRulesRevision,
		&snap.QualityPromotionStatus, &snap.BaselineCreatedAt, &snap.LastDeltaSessionID, &snap.DeltaCount, &snap.UpdatedAt); err != nil {
		return nil, err
	}
	if !isValidUUID(snap.QualityEvaluationRevision) || snap.QualityPromotionStatus != "applied" {
		return nil, fmt.Errorf("%w: legacy snapshot provenance is incomplete", ErrCurrentSnapshotBindingConflict)
	}
	qualitySourceID, err := s.currentSnapshotQualitySourceSessionID(ctx, snap)
	if err != nil {
		return nil, err
	}
	quality, err := s.GetCrawlQualityResult(ctx, qualitySourceID)
	if err != nil {
		return nil, fmt.Errorf("%w: legacy snapshot quality source cannot be proven", ErrCurrentSnapshotBindingConflict)
	}
	if !isValidUUID(snap.SourceSessionID) {
		if quality.IsFullCrawl {
			snap.SourceSessionID = qualitySourceID
		} else {
			snap.SourceSessionID = quality.BaselineSessionID
		}
	}
	source, err := s.GetSession(ctx, snap.SourceSessionID)
	if err != nil || source.ProjectID == nil || *source.ProjectID != projectID {
		return nil, fmt.Errorf("%w: legacy snapshot source cannot be proven", ErrCurrentSnapshotBindingConflict)
	}
	if snap.SourceStartedAt.IsZero() {
		snap.SourceStartedAt = source.StartedAt
	}
	if !isValidUUID(snap.ContentWatermarkSessionID) {
		watermark, watermarkErr := s.GetSession(ctx, qualitySourceID)
		if watermarkErr != nil || watermark.ProjectID == nil || *watermark.ProjectID != projectID {
			return nil, fmt.Errorf("%w: legacy snapshot content watermark cannot be proven", ErrCurrentSnapshotBindingConflict)
		}
		snap.ContentWatermarkSessionID = qualitySourceID
		snap.ContentWatermarkStartedAt = watermark.StartedAt
	}
	if err := s.upsertProjectCurrentSnapshot(ctx, &snap); err != nil {
		return nil, err
	}
	return s.GetProjectCurrentSnapshot(ctx, projectID)
}

// ValidateProjectCurrentSnapshotBinding resolves the immutable evaluation that
// produced the pointer and verifies it is still the current trusted evaluation
// backed by the newest finalized PageRank evidence.
func (s *Store) ValidateProjectCurrentSnapshotBinding(ctx context.Context, snap ProjectCurrentSnapshot) (*CrawlQualityResult, *PageRankEvidence, error) {
	if snap.ProjectID == "" || !isValidUUID(snap.QualityEvaluationRevision) || !isValidUUID(snap.PageRankEvidenceRevision) || snap.QualityPromotionStatus != "applied" {
		return nil, nil, fmt.Errorf("%w: pointer provenance is incomplete", ErrCurrentSnapshotBindingConflict)
	}
	sourceID, err := s.currentSnapshotQualitySourceSessionID(ctx, snap)
	if err != nil {
		return nil, nil, err
	}
	quality, err := s.GetCrawlQualityResult(ctx, sourceID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, fmt.Errorf("%w: current quality evaluation is missing", ErrCurrentSnapshotBindingConflict)
		}
		return nil, nil, err
	}
	evidence, err := s.LatestPageRankEvidence(ctx, sourceID)
	if err != nil {
		if errors.Is(err, ErrNoFinalizedPageRankEvidence) || errors.Is(err, sql.ErrNoRows) {
			return quality, nil, fmt.Errorf("%w: current PageRank evidence is missing", ErrCurrentSnapshotBindingConflict)
		}
		return quality, nil, err
	}
	expectedQualityBaselineSessionID := quality.BaselineSessionID
	expectedBaselineEvaluationRevision := quality.BaselineEvaluationRevision
	if quality.IsFullCrawl {
		expectedQualityBaselineSessionID = sourceID
		expectedBaselineEvaluationRevision = quality.EvaluationRevision
	}
	if !quality.Trusted || quality.Stale || quality.EvaluationRevision != snap.QualityEvaluationRevision ||
		quality.PageRankEvidenceRevision != snap.PageRankEvidenceRevision ||
		quality.PageRankPredicateVersion != PageRankEligiblePredicateVersion ||
		quality.EvaluatorRevision != snap.QualityEvaluatorRevision || quality.RulesRevision != snap.QualityRulesRevision ||
		snap.QualityBaselineSessionID != expectedQualityBaselineSessionID ||
		snap.BaselineQualityEvaluationRevision != expectedBaselineEvaluationRevision ||
		evidence.State != PageRankEvidenceFinalized || evidence.AttemptID != snap.PageRankEvidenceRevision ||
		evidence.PredicateVersion != PageRankEligiblePredicateVersion {
		return quality, evidence, fmt.Errorf("%w: persisted pointer no longer matches current quality and PageRank evidence", ErrCurrentSnapshotBindingConflict)
	}
	baselineQuality, baselineErr := s.GetCrawlQualityResult(ctx, snap.QualityBaselineSessionID)
	if baselineErr != nil || !baselineQuality.Trusted || baselineQuality.EvaluationRevision != snap.BaselineQualityEvaluationRevision ||
		baselineQuality.PageRankPredicateVersion != PageRankEligiblePredicateVersion {
		return quality, evidence, fmt.Errorf("%w: quality baseline evaluation is no longer current", ErrCurrentSnapshotBindingConflict)
	}
	baselineEvidence, baselineEvidenceErr := s.LatestPageRankEvidence(ctx, snap.QualityBaselineSessionID)
	if baselineEvidenceErr != nil || baselineEvidence.State != PageRankEvidenceFinalized ||
		baselineEvidence.AttemptID != baselineQuality.PageRankEvidenceRevision ||
		baselineEvidence.PredicateVersion != PageRankEligiblePredicateVersion {
		return quality, evidence, fmt.Errorf("%w: quality baseline PageRank evidence is no longer current", ErrCurrentSnapshotBindingConflict)
	}
	return quality, evidence, nil
}

// ValidateProjectCurrentSnapshotHistoricalBinding validates the immutable
// facts captured by a journal revision. It intentionally does not require the
// quality or PageRank current pointers to still select those revisions.
func (s *Store) ValidateProjectCurrentSnapshotHistoricalBinding(ctx context.Context, snap ProjectCurrentSnapshot) (*CrawlQualityResult, *PageRankEvidence, error) {
	fail := func(format string, args ...interface{}) (*CrawlQualityResult, *PageRankEvidence, error) {
		return nil, nil, fmt.Errorf("%w: "+format, append([]interface{}{ErrCurrentSnapshotBindingConflict}, args...)...)
	}
	if snap.ProjectID == "" || !isValidUUID(snap.SourceSessionID) || !isValidUUID(snap.ContentWatermarkSessionID) ||
		!isValidUUID(snap.QualityEvaluationRevision) || !isValidUUID(snap.PageRankEvidenceRevision) ||
		!isValidUUID(snap.BaselineQualityEvaluationRevision) || snap.QualityPromotionStatus != "applied" ||
		snap.QualityBaselineSessionID != snap.SourceSessionID {
		return fail("historical snapshot provenance is incomplete")
	}
	quality, err := s.GetCrawlQualityEvaluation(ctx, snap.ContentWatermarkSessionID, snap.QualityEvaluationRevision)
	if err != nil {
		return fail("historical quality evaluation is unavailable")
	}
	if quality.ProjectID != snap.ProjectID || !quality.Trusted || quality.Stale ||
		quality.PageRankEvidenceRevision != snap.PageRankEvidenceRevision || quality.EvaluatorRevision != snap.QualityEvaluatorRevision ||
		quality.RulesRevision != snap.QualityRulesRevision || quality.PageRankPredicateVersion != PageRankEligiblePredicateVersion {
		return fail("historical quality evaluation does not match snapshot")
	}
	if quality.IsFullCrawl {
		if snap.ContentWatermarkSessionID != snap.SourceSessionID || quality.EvaluationRevision != snap.BaselineQualityEvaluationRevision {
			return fail("historical full-crawl baseline does not match snapshot")
		}
	} else if quality.BaselineSessionID != snap.SourceSessionID || quality.BaselineEvaluationRevision != snap.BaselineQualityEvaluationRevision {
		return fail("historical delta baseline does not match snapshot")
	}
	evidence, err := s.GetPageRankEvidence(ctx, snap.ContentWatermarkSessionID, snap.PageRankEvidenceRevision)
	if err != nil || evidence.State != PageRankEvidenceFinalized || evidence.PredicateVersion != PageRankEligiblePredicateVersion {
		return fail("historical PageRank evidence is unavailable or incomplete")
	}
	baseline, err := s.GetCrawlQualityEvaluation(ctx, snap.SourceSessionID, snap.BaselineQualityEvaluationRevision)
	if err != nil || baseline.ProjectID != snap.ProjectID || !baseline.Trusted || baseline.Stale ||
		!baseline.IsFullCrawl || baseline.PageRankPredicateVersion != PageRankEligiblePredicateVersion {
		return fail("historical baseline evaluation is unavailable or incomplete")
	}
	baselineEvidence, err := s.GetPageRankEvidence(ctx, snap.SourceSessionID, baseline.PageRankEvidenceRevision)
	if err != nil || baselineEvidence.State != PageRankEvidenceFinalized || baselineEvidence.PredicateVersion != PageRankEligiblePredicateVersion {
		return fail("historical baseline PageRank evidence is unavailable or incomplete")
	}
	return quality, evidence, nil
}

func (s *Store) currentSnapshotQualitySourceSessionID(ctx context.Context, snap ProjectCurrentSnapshot) (string, error) {
	if isValidUUID(snap.ContentWatermarkSessionID) {
		return snap.ContentWatermarkSessionID, nil
	}
	rows, err := s.conn.Query(ctx, `
		SELECT toString(session_id)
		FROM crawlobserver.crawl_quality_evaluations
		WHERE project_id = ? AND evaluation_revision = toUUID(?)
		ORDER BY evaluated_at DESC, session_id DESC
		LIMIT 2`, snap.ProjectID, snap.QualityEvaluationRevision)
	if err != nil {
		return "", fmt.Errorf("resolving current snapshot quality source: %w", err)
	}
	defer rows.Close()
	var sourceIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		sourceIDs = append(sourceIDs, id)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(sourceIDs) != 1 {
		return "", fmt.Errorf("%w: quality source is missing or ambiguous", ErrCurrentSnapshotBindingConflict)
	}
	return sourceIDs[0], nil
}

func (s *Store) InitializeProjectCurrentSnapshot(ctx context.Context, projectID, baselineSessionID string, binding CrawlQualityPromotionEvent) (*ProjectCurrentSnapshot, error) {
	lock := currentSnapshotPromotionLock(projectID)
	lock.Lock()
	defer lock.Unlock()
	if projectID == "" {
		return nil, fmt.Errorf("project_id is required")
	}
	if !isValidUUID(baselineSessionID) {
		return nil, fmt.Errorf("invalid baseline session ID: %s", baselineSessionID)
	}
	baseline, err := s.GetSession(ctx, baselineSessionID)
	if err != nil {
		return nil, err
	}
	if baseline.ProjectID == nil || *baseline.ProjectID != projectID {
		return nil, fmt.Errorf("baseline session does not belong to project")
	}
	allowed, existing, err := s.CanPromoteCurrentSnapshotSource(ctx, projectID, baselineSessionID)
	if err != nil {
		if !errors.Is(err, ErrCurrentSnapshotBindingConflict) {
			return nil, err
		}
		var v2Count uint64
		if countErr := s.conn.QueryRow(ctx, `SELECT count() FROM crawlobserver.project_current_snapshot_promotions_v2 WHERE project_id = ?`, projectID).Scan(&v2Count); countErr != nil {
			return nil, countErr
		}
		if v2Count != 0 {
			return nil, err
		}
		// The only supported recovery is a validated full crawl replacing an
		// unprovable legacy pointer. Binding validation below proves the new
		// source before it can publish v2; legacy rows remain audit-only.
		allowed = true
	}
	if !allowed {
		return existing, ErrCurrentSnapshotSourceSuperseded
	}
	quality, err := s.GetCrawlQualityResult(ctx, baselineSessionID)
	if err != nil {
		return nil, fmt.Errorf("reading full snapshot recovery quality: %w", err)
	}
	if !quality.IsFullCrawl {
		return nil, fmt.Errorf("%w: current snapshot initialization requires a trusted full crawl", ErrCurrentSnapshotBindingConflict)
	}
	if binding.BaselineSessionID != baselineSessionID || binding.BaselineEvaluationRevision != quality.EvaluationRevision {
		return nil, fmt.Errorf("%w: full snapshot initialization requires self-baseline binding", ErrCurrentSnapshotBindingConflict)
	}
	if err := s.validateCurrentSnapshotBinding(ctx, projectID, baselineSessionID, binding); err != nil {
		return nil, err
	}
	if existing != nil && existing.SourceSessionID == baselineSessionID && existing.ContentWatermarkSessionID == baselineSessionID &&
		currentSnapshotBindingMatches(*existing, binding) {
		if currentSnapshotNeedsFoldCleanup(*existing) {
			if err := s.completeFoldedSnapshotCleanup(ctx, *existing); err != nil {
				return nil, err
			}
		}
		return existing, nil
	}

	var oldCurrentID string
	if existing, err := s.GetProjectCurrentSnapshot(ctx, projectID); err == nil {
		oldCurrentID = existing.CurrentSessionID
	}
	oldDeltaIDs, err := s.listProjectCurrentSnapshotDeltas(ctx, projectID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	currentID := uuid.New().String()
	if err := s.copySessionForSnapshot(ctx, baselineSessionID, currentID, CurrentSnapshotLabel, now); err != nil {
		return nil, err
	}
	baselineCreatedAt := baseline.FinishedAt
	if baselineCreatedAt.IsZero() {
		baselineCreatedAt = now
	}
	snap := ProjectCurrentSnapshot{
		ProjectID: projectID, SourceSessionID: baselineSessionID, SourceStartedAt: baseline.StartedAt,
		ContentWatermarkSessionID: baselineSessionID, ContentWatermarkStartedAt: baseline.StartedAt,
		CurrentSessionID: currentID, BaselineSessionID: baselineSessionID,
		QualityBaselineSessionID:          binding.BaselineSessionID,
		QualityEvaluationRevision:         binding.EvaluationRevision,
		BaselineQualityEvaluationRevision: binding.BaselineEvaluationRevision,
		PageRankEvidenceRevision:          binding.PageRankEvidenceRevision,
		QualityEvaluatorRevision:          binding.EvaluatorRevision, QualityRulesRevision: binding.RulesRevision,
		QualityPromotionStatus: "applied",
		BaselineCreatedAt:      baselineCreatedAt, DeltaCount: 0, UpdatedAt: now,
	}
	if err := s.validateCurrentSnapshotBinding(ctx, projectID, baselineSessionID, binding); err != nil {
		s.deleteSyntheticSnapshotSession(ctx, currentID)
		return nil, err
	}
	if err := s.upsertProjectCurrentSnapshot(ctx, &snap); err != nil {
		return nil, err
	}
	if err := s.verifyProjectCurrentSnapshotBinding(ctx, snap); err != nil {
		if canonical, readErr := s.GetProjectCurrentSnapshot(ctx, projectID); readErr == nil &&
			compareSnapshotSource(canonical.ContentWatermarkStartedAt, canonical.ContentWatermarkSessionID, baseline.StartedAt, baselineSessionID) > 0 {
			// A concurrent writer published a newer durable source after our
			// preflight. This synthetic copy was never canonical, so remove it
			// before returning a typed retry-safe outcome to the caller.
			if deleteErr := s.deleteSession(ctx, currentID, true); deleteErr != nil {
				return nil, fmt.Errorf("%w: deleting superseded synthetic snapshot: %v", ErrCurrentSnapshotSourceSuperseded, deleteErr)
			}
			return canonical, ErrCurrentSnapshotSourceSuperseded
		}
		return nil, err
	}
	if err := s.clearProjectCurrentSnapshotDeltas(ctx, projectID); err != nil {
		return nil, err
	}
	for _, id := range oldDeltaIDs {
		s.deleteDeltaSnapshotSession(ctx, id)
	}
	if oldCurrentID != "" && oldCurrentID != currentID {
		s.deleteSyntheticSnapshotSession(ctx, oldCurrentID)
	}
	return &snap, nil
}

func (s *Store) PromoteDeltaToCurrentSnapshot(ctx context.Context, projectID, deltaSessionID, baselineSessionID string, maxDeltas, foldIntervalDays int, opts PageRankOptions, binding CrawlQualityPromotionEvent) (*ProjectCurrentSnapshot, error) {
	lock := currentSnapshotPromotionLock(projectID)
	lock.Lock()
	defer lock.Unlock()
	if projectID == "" {
		return nil, fmt.Errorf("project_id is required")
	}
	if !isValidUUID(deltaSessionID) {
		return nil, fmt.Errorf("invalid delta session ID: %s", deltaSessionID)
	}
	if maxDeltas <= 0 {
		maxDeltas = 14
	}
	if foldIntervalDays <= 0 {
		foldIntervalDays = 30
	}

	snap, err := s.GetProjectCurrentSnapshot(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("current snapshot missing; reconcile the trusted full-crawl baseline first")
	}
	if _, err := s.GetSession(ctx, snap.CurrentSessionID); err != nil {
		return nil, fmt.Errorf("current snapshot session missing; reconcile the trusted full-crawl baseline first")
	} else if _, err := s.GetSession(ctx, snap.BaselineSessionID); err != nil {
		return nil, fmt.Errorf("current snapshot baseline missing; reconcile a trusted full-crawl baseline first")
	}
	delta, err := s.GetSession(ctx, deltaSessionID)
	if err != nil {
		return nil, err
	}
	if compareSnapshotSource(delta.StartedAt, delta.ID, snap.SourceStartedAt, snap.SourceSessionID) <= 0 ||
		compareSnapshotSource(delta.StartedAt, delta.ID, snap.ContentWatermarkStartedAt, snap.ContentWatermarkSessionID) < 0 {
		return snap, ErrCurrentSnapshotSourceSuperseded
	}
	// A delta can only extend the exact trusted full source that produced the
	// current materialization. Do not let a caller-provided or evaluation-bound
	// baseline silently drift away from the persisted full-crawl lineage.
	if baselineSessionID == "" || binding.BaselineSessionID == "" ||
		baselineSessionID != snap.SourceSessionID || binding.BaselineSessionID != snap.SourceSessionID {
		return snap, fmt.Errorf("%w: delta baseline does not match current snapshot source", ErrCurrentSnapshotBindingConflict)
	}
	if err := s.validateCurrentSnapshotBinding(ctx, projectID, deltaSessionID, binding); err != nil {
		return nil, err
	}
	if currentSnapshotBindingMatches(*snap, binding) {
		if currentSnapshotNeedsFoldCleanup(*snap) {
			if err := s.completeFoldedSnapshotCleanup(ctx, *snap); err != nil {
				return nil, err
			}
		}
		if err := s.cleanupSupersededDeltaPlanPredecessor(ctx, *snap); err != nil {
			return nil, err
		}
		return snap, nil
	}
	// This is deliberately before every materialized mutation. An idempotent
	// retry returns above before comparing against the snapshot revision it has
	// already advanced.
	publishSitemaps, err := s.deltaSitemapPublicationPreflight(ctx, snap, deltaSessionID)
	if err != nil {
		return snap, err
	}

	// A fold clears content markers, but an equal durable watermark still proves
	// the delta content is already present. Re-evaluation may update only the
	// immutable binding and must never overlay the pages a second time.
	alreadyApplied := compareSnapshotSource(delta.StartedAt, delta.ID, snap.ContentWatermarkStartedAt, snap.ContentWatermarkSessionID) == 0
	if !alreadyApplied {
		var err error
		alreadyApplied, err = s.isCurrentSnapshotDeltaApplied(ctx, projectID, deltaSessionID)
		if err != nil {
			return nil, err
		}
	}
	if !alreadyApplied {
		if err := s.overlayDeltaPages(ctx, snap.CurrentSessionID, deltaSessionID); err != nil {
			return nil, err
		}
		if err := s.overlayDeltaLinks(ctx, snap.CurrentSessionID, deltaSessionID); err != nil {
			return nil, err
		}
		if err := s.ComputePageRankWithOptions(ctx, snap.CurrentSessionID, opts); err != nil {
			return nil, err
		}
		if publishSitemaps {
			if err := s.replaceCurrentSnapshotSitemaps(ctx, snap.CurrentSessionID, deltaSessionID); err != nil {
				return nil, fmt.Errorf("replacing current snapshot sitemap membership: %w", err)
			}
			applog.Infof("storage", "promoted fresh sitemap membership from delta %s into current snapshot %s", deltaSessionID, snap.CurrentSessionID)
		}

		if err := s.conn.Exec(ctx, `
			INSERT INTO crawlobserver.project_current_snapshot_deltas
				(project_id, delta_session_id, current_session_id, applied_at)
			VALUES (?, toUUID(?), toUUID(?), ?)`,
			projectID, deltaSessionID, snap.CurrentSessionID, time.Now().UTC(),
		); err != nil {
			return nil, fmt.Errorf("recording current snapshot delta content stage: %w", err)
		}
	}
	return s.finalizeCurrentSnapshotDelta(ctx, *snap, deltaSessionID, maxDeltas, foldIntervalDays, binding)
}

// deltaSitemapPublicationPreflight accepts publication only for a new
// selector-aware Delta whose fresh observation was planned against this exact
// materialized Current Snapshot. An incomplete selection can still overlay the
// selected page evidence, but it must leave the published sitemap safety term
// unchanged so deferred events remain pending on the next plan.
func (s *Store) deltaSitemapPublicationPreflight(ctx context.Context, snap *ProjectCurrentSnapshot, deltaSessionID string) (bool, error) {
	if snap == nil {
		return false, ErrCurrentSnapshotSourceSuperseded
	}
	delta, err := s.GetSession(ctx, deltaSessionID)
	if err != nil {
		return false, err
	}
	var saved config.Config
	if err := json.Unmarshal([]byte(delta.Config), &saved); err != nil || saved.Crawler.DeltaPlan == nil {
		return false, nil
	}
	plan := saved.Crawler.DeltaPlan
	return deltaSitemapPublicationBindingMatches(plan, snap)
}

func deltaSitemapPublicationBindingMatches(plan *config.DeltaPlanConfig, snap *ProjectCurrentSnapshot) (bool, error) {
	if plan == nil || plan.SitemapRefresh == nil || plan.SitemapRefresh.Mode != "fresh" {
		return false, nil
	}
	selection := plan.SitemapSelection
	if selection == nil {
		// Legacy fresh Deltas retain their existing content-overlay behavior but
		// cannot advance the new published sitemap term.
		return false, nil
	}
	if snap == nil || plan.SitemapRefresh.FetchedAt.IsZero() || !isSupportedDeltaSitemapSelectorRevision(selection.SelectorRevision) || selection.RawObservationSessionID == "" || selection.RawObservedAt.IsZero() ||
		selection.PublishedSessionID != snap.CurrentSessionID ||
		selection.PublishedSnapshotRevision != snap.SnapshotRevision ||
		selection.PublishedContentWatermarkSessionID != snap.ContentWatermarkSessionID {
		return false, ErrCurrentSnapshotSourceSuperseded
	}
	// Raw stability is deliberately execution-only. It can prove that a
	// refetch is redundant, never that the published sitemap term may advance.
	if selection.PublicationHeld || selection.StableAcknowledgedTotal > 0 {
		return false, nil
	}
	return selection.SelectionComplete, nil
}

func isSupportedDeltaSitemapSelectorRevision(value string) bool {
	return value == deltaSitemapSelectorRevisionV1 || value == deltaSitemapSelectorRevisionV2
}

func (s *Store) finalizeCurrentSnapshotDelta(ctx context.Context, snap ProjectCurrentSnapshot, deltaSessionID string, maxDeltas, foldIntervalDays int, binding CrawlQualityPromotionEvent) (*ProjectCurrentSnapshot, error) {
	projectID := snap.ProjectID
	delta, err := s.GetSession(ctx, deltaSessionID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	lastDeltaID := deltaSessionID
	baselineID := snap.BaselineSessionID
	baselineCreatedAt := snap.BaselineCreatedAt
	folded := !baselineCreatedAt.IsZero() && !now.Before(baselineCreatedAt.AddDate(0, 0, foldIntervalDays))
	if folded {
		newBaselineID := uuid.New().String()
		if err := s.copySessionForSnapshot(ctx, snap.CurrentSessionID, newBaselineID, CurrentBaselineSnapshotLabel, now); err != nil {
			return nil, err
		}
		baselineID = newBaselineID
		baselineCreatedAt = now
		lastDeltaID = ""
	} else if err := s.trimProjectCurrentSnapshotDeltas(ctx, projectID, maxDeltas); err != nil {
		return nil, err
	}

	deltaCount, err := s.countProjectCurrentSnapshotDeltas(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if folded {
		deltaCount = 0
	}
	pages, err := s.CountPages(ctx, snap.CurrentSessionID)
	if err != nil {
		return nil, err
	}
	currentSession, err := s.GetSession(ctx, snap.CurrentSessionID)
	if err != nil {
		return nil, err
	}
	currentSession.PagesCrawled = pages
	currentSession.FinishedAt = now
	currentSession.Status = "completed"
	if err := s.InsertSession(ctx, currentSession); err != nil {
		return nil, err
	}

	updated := ProjectCurrentSnapshot{
		ProjectID:                         projectID,
		SourceSessionID:                   snap.SourceSessionID,
		SourceStartedAt:                   snap.SourceStartedAt,
		ContentWatermarkSessionID:         deltaSessionID,
		ContentWatermarkStartedAt:         delta.StartedAt,
		CurrentSessionID:                  snap.CurrentSessionID,
		BaselineSessionID:                 baselineID,
		QualityBaselineSessionID:          binding.BaselineSessionID,
		QualityEvaluationRevision:         binding.EvaluationRevision,
		BaselineQualityEvaluationRevision: binding.BaselineEvaluationRevision,
		PageRankEvidenceRevision:          binding.PageRankEvidenceRevision,
		QualityEvaluatorRevision:          binding.EvaluatorRevision,
		QualityRulesRevision:              binding.RulesRevision,
		QualityPromotionStatus:            "applied",
		BaselineCreatedAt:                 baselineCreatedAt,
		LastDeltaSessionID:                lastDeltaID,
		DeltaCount:                        deltaCount,
		UpdatedAt:                         now,
	}
	if err := s.validateCurrentSnapshotBinding(ctx, projectID, deltaSessionID, binding); err != nil {
		return nil, err
	}
	if err := s.upsertProjectCurrentSnapshot(ctx, &updated); err != nil {
		return nil, err
	}
	if err := s.verifyProjectCurrentSnapshotBinding(ctx, updated); err != nil {
		return nil, err
	}
	if err := s.cleanupSupersededDeltaPlanPredecessor(ctx, updated); err != nil {
		return nil, err
	}
	if folded {
		if err := s.completeFoldedSnapshotCleanup(ctx, updated); err != nil {
			return nil, err
		}
	}
	return &updated, nil
}

// Once D3 is durably canonical, its live plan references D2. D2's own plan
// may still name an already-pruned D1; reclaim that metadata only after the
// D3 pointer readback succeeds. A retry reaches the idempotent branch above.
func (s *Store) cleanupSupersededDeltaPlanPredecessor(ctx context.Context, snap ProjectCurrentSnapshot) error {
	current, err := s.GetSession(ctx, snap.ContentWatermarkSessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var currentCfg config.Config
	if err := json.Unmarshal([]byte(current.Config), &currentCfg); err != nil || currentCfg.Crawler.DeltaPlan == nil {
		return nil
	}
	predecessorID := currentCfg.Crawler.DeltaPlan.BaselineContentWatermarkSessionID
	if !isValidUUID(predecessorID) {
		return nil
	}
	predecessor, err := s.GetSession(ctx, predecessorID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var predecessorCfg config.Config
	if err := json.Unmarshal([]byte(predecessor.Config), &predecessorCfg); err != nil || predecessorCfg.Crawler.DeltaPlan == nil {
		return nil
	}
	priorID := predecessorCfg.Crawler.DeltaPlan.BaselineContentWatermarkSessionID
	if !isValidUUID(priorID) || priorID == predecessorID {
		return nil
	}
	return s.deleteDeltaSnapshotSessionChecked(ctx, priorID)
}

func currentSnapshotNeedsFoldCleanup(snap ProjectCurrentSnapshot) bool {
	return snap.DeltaCount == 0 && snap.LastDeltaSessionID == ""
}

func (s *Store) completeFoldedSnapshotCleanup(ctx context.Context, snap ProjectCurrentSnapshot) error {
	qualitySourceID, err := s.currentSnapshotQualitySourceSessionID(ctx, snap)
	if err != nil {
		return err
	}
	deltaIDs, err := s.listProjectCurrentSnapshotDeltas(ctx, snap.ProjectID)
	if err != nil {
		return err
	}
	for _, id := range deltaIDs {
		if id == qualitySourceID {
			continue
		}
		if err := s.deleteDeltaSnapshotSessionChecked(ctx, id); err != nil {
			return err
		}
	}
	obsolete, err := s.obsoleteSyntheticSnapshotSessions(ctx, snap)
	if err != nil {
		return err
	}
	for _, id := range obsolete {
		if err := s.deleteSyntheticSnapshotSessionChecked(ctx, id); err != nil {
			return err
		}
	}
	return s.clearProjectCurrentSnapshotDeltas(ctx, snap.ProjectID)
}

func (s *Store) obsoleteSyntheticSnapshotSessions(ctx context.Context, snap ProjectCurrentSnapshot) ([]string, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT toString(id)
		FROM crawlobserver.crawl_sessions FINAL
		WHERE project_id = ? AND label IN (?, ?)
		  AND id != toUUID(?)
		  AND (? = '' OR id != toUUID(?))
		ORDER BY started_at, id`, snap.ProjectID, CurrentSnapshotLabel, CurrentBaselineSnapshotLabel,
		snap.CurrentSessionID, snap.BaselineSessionID, snap.BaselineSessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func currentSnapshotBindingMatches(snap ProjectCurrentSnapshot, binding CrawlQualityPromotionEvent) bool {
	return snap.QualityPromotionStatus == "applied" &&
		snap.QualityBaselineSessionID == binding.BaselineSessionID &&
		snap.QualityEvaluationRevision == binding.EvaluationRevision &&
		snap.BaselineQualityEvaluationRevision == binding.BaselineEvaluationRevision &&
		snap.PageRankEvidenceRevision == binding.PageRankEvidenceRevision &&
		snap.QualityEvaluatorRevision == binding.EvaluatorRevision &&
		snap.QualityRulesRevision == binding.RulesRevision
}

func (s *Store) verifyProjectCurrentSnapshotBinding(ctx context.Context, expected ProjectCurrentSnapshot) error {
	actual, err := s.GetProjectCurrentSnapshot(ctx, expected.ProjectID)
	if err != nil {
		return fmt.Errorf("reading current snapshot after pointer publication: %w", err)
	}
	if actual.CurrentSessionID != expected.CurrentSessionID ||
		actual.SnapshotRevision != expected.SnapshotRevision ||
		actual.QualityBaselineSessionID != expected.QualityBaselineSessionID ||
		actual.QualityEvaluationRevision != expected.QualityEvaluationRevision ||
		actual.BaselineQualityEvaluationRevision != expected.BaselineQualityEvaluationRevision ||
		actual.PageRankEvidenceRevision != expected.PageRankEvidenceRevision ||
		actual.QualityEvaluatorRevision != expected.QualityEvaluatorRevision ||
		actual.QualityRulesRevision != expected.QualityRulesRevision ||
		actual.QualityPromotionStatus != "applied" {
		return fmt.Errorf("current snapshot pointer readback did not match the expected quality binding")
	}
	return nil
}

func (s *Store) validateCurrentSnapshotBinding(ctx context.Context, projectID, sessionID string, binding CrawlQualityPromotionEvent) error {
	if binding.ProjectID != projectID || binding.SessionID != sessionID || !isValidUUID(binding.EvaluationRevision) || !isValidUUID(binding.PageRankEvidenceRevision) {
		return fmt.Errorf("quality promotion binding does not match the snapshot source")
	}
	quality, err := s.GetCrawlQualityResult(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("reading bound quality evaluation: %w", err)
	}
	if !quality.Trusted || quality.EvaluationRevision != binding.EvaluationRevision ||
		quality.PageRankEvidenceRevision != binding.PageRankEvidenceRevision ||
		quality.PageRankPredicateVersion != PageRankEligiblePredicateVersion ||
		quality.EvaluatorRevision == "" || quality.EvaluatorRevision != binding.EvaluatorRevision ||
		quality.RulesRevision == "" || quality.RulesRevision != binding.RulesRevision {
		return fmt.Errorf("quality promotion binding is not the current trusted evaluation")
	}
	evidence, err := s.LatestPageRankEvidence(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("reading bound PageRank evidence: %w", err)
	}
	if evidence.State != PageRankEvidenceFinalized || evidence.AttemptID != binding.PageRankEvidenceRevision ||
		evidence.PredicateVersion != PageRankEligiblePredicateVersion {
		return fmt.Errorf("quality promotion binding references non-current PageRank evidence")
	}
	if binding.BaselineSessionID != "" && binding.BaselineEvaluationRevision != "" {
		baselineQuality, baselineErr := s.GetCrawlQualityResult(ctx, binding.BaselineSessionID)
		if baselineErr != nil || !baselineQuality.Trusted || baselineQuality.EvaluationRevision != binding.BaselineEvaluationRevision ||
			baselineQuality.PageRankPredicateVersion != PageRankEligiblePredicateVersion ||
			baselineQuality.ProjectID != projectID {
			return fmt.Errorf("quality promotion binding references a non-current baseline evaluation")
		}
		baselineEvidence, baselineEvidenceErr := s.LatestPageRankEvidence(ctx, binding.BaselineSessionID)
		if baselineEvidenceErr != nil || baselineEvidence.State != PageRankEvidenceFinalized ||
			baselineEvidence.AttemptID != baselineQuality.PageRankEvidenceRevision ||
			baselineEvidence.PredicateVersion != PageRankEligiblePredicateVersion {
			return fmt.Errorf("quality promotion binding references stale baseline PageRank evidence")
		}
	} else {
		return fmt.Errorf("quality promotion binding requires baseline session and evaluation revisions")
	}
	return nil
}

func (s *Store) isCurrentSnapshotDeltaApplied(ctx context.Context, projectID, deltaSessionID string) (bool, error) {
	var count uint64
	if err := s.conn.QueryRow(ctx, `
		SELECT count()
		FROM crawlobserver.project_current_snapshot_deltas FINAL
		WHERE project_id = ? AND delta_session_id = toUUID(?)`, projectID, deltaSessionID).Scan(&count); err != nil {
		return false, fmt.Errorf("checking current snapshot delta idempotency: %w", err)
	}
	return count > 0, nil
}

func (s *Store) deltaHasFreshSitemapObservation(ctx context.Context, deltaSessionID string) (bool, error) {
	session, err := s.GetSession(ctx, deltaSessionID)
	if err != nil {
		return false, fmt.Errorf("loading delta sitemap metadata: %w", err)
	}
	var cfg config.Config
	if err := json.Unmarshal([]byte(session.Config), &cfg); err != nil {
		return false, fmt.Errorf("decoding delta sitemap metadata: %w", err)
	}
	if cfg.Crawler.DeltaPlan == nil {
		return false, nil
	}
	return cfg.Crawler.DeltaPlan.SitemapRefresh != nil && cfg.Crawler.DeltaPlan.SitemapRefresh.Mode == "fresh", nil
}

func (s *Store) replaceCurrentSnapshotSitemaps(ctx context.Context, currentSessionID, deltaSessionID string) error {
	// ClickHouse mutations are not transactional. Preserve the published
	// membership under an isolated temporary session ID before replacing it so
	// a copy failure can restore the exact prior set without changing the
	// current snapshot pointer.
	backupSessionID := uuid.NewString()
	if err := s.copySnapshotSitemaps(ctx, currentSessionID, backupSessionID); err != nil {
		return fmt.Errorf("backing up current sitemaps: %w", err)
	}
	if err := s.copySnapshotSitemapURLs(ctx, currentSessionID, backupSessionID); err != nil {
		_ = s.clearSnapshotSitemaps(ctx, backupSessionID)
		return fmt.Errorf("backing up current sitemap URLs: %w", err)
	}
	defer func() { _ = s.clearSnapshotSitemaps(ctx, backupSessionID) }()

	if err := s.clearSnapshotSitemaps(ctx, currentSessionID); err != nil {
		return err
	}
	if err := s.copySnapshotSitemaps(ctx, deltaSessionID, currentSessionID); err != nil {
		return s.restoreCurrentSnapshotSitemaps(ctx, currentSessionID, backupSessionID, err)
	}
	if err := s.copySnapshotSitemapURLs(ctx, deltaSessionID, currentSessionID); err != nil {
		return s.restoreCurrentSnapshotSitemaps(ctx, currentSessionID, backupSessionID, err)
	}
	return nil
}

func (s *Store) restoreCurrentSnapshotSitemaps(ctx context.Context, currentSessionID, backupSessionID string, cause error) error {
	if err := s.clearSnapshotSitemaps(ctx, currentSessionID); err != nil {
		return fmt.Errorf("%v; rollback could not clear partial sitemap data: %w", cause, err)
	}
	if err := s.copySnapshotSitemaps(ctx, backupSessionID, currentSessionID); err != nil {
		return fmt.Errorf("%v; rollback could not restore sitemaps: %w", cause, err)
	}
	if err := s.copySnapshotSitemapURLs(ctx, backupSessionID, currentSessionID); err != nil {
		return fmt.Errorf("%v; rollback could not restore sitemap URLs: %w", cause, err)
	}
	return fmt.Errorf("%w (previous current sitemap membership restored)", cause)
}

func (s *Store) clearSnapshotSitemaps(ctx context.Context, sessionID string) error {
	if err := s.conn.Exec(ctx, `
		ALTER TABLE crawlobserver.sitemap_urls DELETE
		WHERE crawl_session_id = ?
		SETTINGS mutations_sync = 1`, sessionID); err != nil {
		return fmt.Errorf("clearing sitemap URLs: %w", err)
	}
	if err := s.conn.Exec(ctx, `
		ALTER TABLE crawlobserver.sitemaps DELETE
		WHERE crawl_session_id = ?
		SETTINGS mutations_sync = 1`, sessionID); err != nil {
		return fmt.Errorf("clearing sitemaps: %w", err)
	}
	return nil
}

func (s *Store) RepairProjectCurrentSnapshotBaseline(ctx context.Context, projectID string) (*ProjectCurrentSnapshot, error) {
	lock := currentSnapshotPromotionLock(projectID)
	lock.Lock()
	defer lock.Unlock()
	snap, err := s.GetProjectCurrentSnapshot(ctx, projectID)
	if err != nil {
		return nil, err
	}
	current, err := s.GetSession(ctx, snap.CurrentSessionID)
	if err != nil {
		return nil, fmt.Errorf("current snapshot session missing: %w", err)
	}
	if current.ProjectID == nil || *current.ProjectID != projectID {
		return nil, fmt.Errorf("current snapshot session does not belong to project")
	}
	if baseline, baselineErr := s.GetSession(ctx, snap.BaselineSessionID); baselineErr == nil &&
		strings.TrimSpace(baseline.Label) == CurrentBaselineSnapshotLabel && currentSnapshotNeedsFoldCleanup(*snap) {
		if err := s.completeFoldedSnapshotCleanup(ctx, *snap); err != nil {
			return nil, err
		}
		return snap, nil
	}
	now := time.Now().UTC()
	newBaselineID := uuid.New().String()
	if err := s.copySessionForSnapshot(ctx, snap.CurrentSessionID, newBaselineID, CurrentBaselineSnapshotLabel, now); err != nil {
		return nil, err
	}
	repaired := ProjectCurrentSnapshot{
		ProjectID:                         projectID,
		SourceSessionID:                   snap.SourceSessionID,
		SourceStartedAt:                   snap.SourceStartedAt,
		ContentWatermarkSessionID:         snap.ContentWatermarkSessionID,
		ContentWatermarkStartedAt:         snap.ContentWatermarkStartedAt,
		CurrentSessionID:                  snap.CurrentSessionID,
		BaselineSessionID:                 newBaselineID,
		QualityBaselineSessionID:          snap.QualityBaselineSessionID,
		QualityEvaluationRevision:         snap.QualityEvaluationRevision,
		BaselineQualityEvaluationRevision: snap.BaselineQualityEvaluationRevision,
		PageRankEvidenceRevision:          snap.PageRankEvidenceRevision,
		QualityEvaluatorRevision:          snap.QualityEvaluatorRevision,
		QualityRulesRevision:              snap.QualityRulesRevision,
		QualityPromotionStatus:            snap.QualityPromotionStatus,
		BaselineCreatedAt:                 now,
		DeltaCount:                        0,
		UpdatedAt:                         now,
	}
	if err := s.upsertProjectCurrentSnapshot(ctx, &repaired); err != nil {
		return nil, err
	}
	if err := s.verifyProjectCurrentSnapshotBinding(ctx, repaired); err != nil {
		return nil, err
	}
	if err := s.completeFoldedSnapshotCleanup(ctx, repaired); err != nil {
		return nil, err
	}
	return &repaired, nil
}

func (s *Store) upsertProjectCurrentSnapshot(ctx context.Context, snap *ProjectCurrentSnapshot) error {
	if snap == nil {
		return fmt.Errorf("current snapshot is required")
	}
	if !isValidUUID(snap.SourceSessionID) || !isValidUUID(snap.ContentWatermarkSessionID) ||
		snap.SourceStartedAt.IsZero() || snap.ContentWatermarkStartedAt.IsZero() {
		return fmt.Errorf("current snapshot source lineage is required")
	}
	var maxRevision uint64
	var maxUpdatedAt time.Time
	if err := s.conn.QueryRow(ctx, `
		SELECT max(snapshot_revision), max(updated_at)
		FROM crawlobserver.project_current_snapshot_promotions_v2
		WHERE project_id = ?`, snap.ProjectID).Scan(&maxRevision, &maxUpdatedAt); err != nil {
		return fmt.Errorf("reading current snapshot pointer revision: %w", err)
	}
	if snap.SnapshotRevision <= maxRevision {
		snap.SnapshotRevision = maxRevision + 1
	}
	if snap.UpdatedAt.IsZero() {
		snap.UpdatedAt = time.Now().UTC()
	}
	snap.UpdatedAt = snap.UpdatedAt.UTC().Truncate(time.Second)
	maxUpdatedAt = maxUpdatedAt.UTC().Truncate(time.Second)
	if !snap.UpdatedAt.After(maxUpdatedAt) {
		snap.UpdatedAt = maxUpdatedAt.Add(time.Second)
	}
	if snap.BaselineCreatedAt.IsZero() {
		snap.BaselineCreatedAt = snap.UpdatedAt
	}
	if !isValidUUID(snap.CurrentSessionID) {
		return fmt.Errorf("invalid current session ID: %s", snap.CurrentSessionID)
	}
	return s.conn.Exec(ctx, `
		INSERT INTO crawlobserver.project_current_snapshot_promotions_v2 (
			project_id, source_session_id, source_started_at, content_watermark_session_id, content_watermark_started_at,
			snapshot_revision, current_session_id, baseline_session_id,
			quality_baseline_session_id,
			quality_evaluation_revision, baseline_quality_evaluation_revision,
			pagerank_evidence_revision, quality_evaluator_revision, quality_rules_revision,
			quality_promotion_status, baseline_created_at,
			last_delta_session_id, delta_count, updated_at
		) VALUES (?, toUUID(?), ?, toUUID(?), ?, ?, toUUID(?), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		snap.ProjectID, snap.SourceSessionID, snap.SourceStartedAt, snap.ContentWatermarkSessionID, snap.ContentWatermarkStartedAt,
		snap.SnapshotRevision, snap.CurrentSessionID, snap.BaselineSessionID,
		snap.QualityBaselineSessionID,
		snap.QualityEvaluationRevision, snap.BaselineQualityEvaluationRevision,
		snap.PageRankEvidenceRevision, snap.QualityEvaluatorRevision, snap.QualityRulesRevision,
		snap.QualityPromotionStatus, snap.BaselineCreatedAt,
		snap.LastDeltaSessionID, snap.DeltaCount, snap.UpdatedAt,
	)
}

func (s *Store) copySessionForSnapshot(ctx context.Context, sourceSessionID, targetSessionID, label string, now time.Time) error {
	if !isValidUUID(sourceSessionID) || !isValidUUID(targetSessionID) {
		return fmt.Errorf("invalid snapshot session copy IDs")
	}
	source, err := s.GetSession(ctx, sourceSessionID)
	if err != nil {
		return err
	}
	projectID := source.ProjectID
	pages, err := s.CountPages(ctx, sourceSessionID)
	if err != nil {
		return err
	}
	target := &CrawlSession{
		ID:           targetSessionID,
		StartedAt:    source.StartedAt,
		FinishedAt:   now,
		Status:       "completed",
		SeedURLs:     append([]string(nil), source.SeedURLs...),
		Config:       source.Config,
		PagesCrawled: pages,
		UserAgent:    source.UserAgent,
		ProjectID:    projectID,
		Label:        label,
	}
	if err := s.InsertSession(ctx, target); err != nil {
		return err
	}
	if err := s.copySnapshotPages(ctx, sourceSessionID, targetSessionID); err != nil {
		return err
	}
	if err := s.copySnapshotLinks(ctx, sourceSessionID, targetSessionID); err != nil {
		return err
	}
	if err := s.copySnapshotSitemaps(ctx, sourceSessionID, targetSessionID); err != nil {
		return err
	}
	if err := s.copySnapshotSitemapURLs(ctx, sourceSessionID, targetSessionID); err != nil {
		return err
	}
	return nil
}

func (s *Store) overlayDeltaPages(ctx context.Context, currentSessionID, deltaSessionID string) error {
	return s.conn.Exec(ctx, fmt.Sprintf(`
		INSERT INTO crawlobserver.pages (%s)
		SELECT toUUID(?), %s
		FROM crawlobserver.pages FINAL
		WHERE crawl_session_id = ? AND status_code != 304`, snapshotPageColumns, snapshotPageSelectColumns),
		currentSessionID, deltaSessionID,
	)
}

func (s *Store) overlayDeltaLinks(ctx context.Context, currentSessionID, deltaSessionID string) error {
	if err := s.conn.Exec(ctx, `
		ALTER TABLE crawlobserver.links DELETE
		WHERE crawl_session_id = ?
		  AND source_url IN (
			SELECT url FROM crawlobserver.pages FINAL WHERE crawl_session_id = ? AND status_code != 304
		  )
		SETTINGS mutations_sync = 1`,
		currentSessionID, deltaSessionID,
	); err != nil {
		return fmt.Errorf("removing current links for delta pages: %w", err)
	}
	return s.conn.Exec(ctx, `
		INSERT INTO crawlobserver.links (
			crawl_session_id, source_url, target_url, anchor_text, rel, is_internal, tag, link_location, crawled_at
		)
		SELECT toUUID(?), source_url, target_url, anchor_text, rel, is_internal, tag, link_location, crawled_at
		FROM crawlobserver.links
		WHERE crawl_session_id = ?
		  AND source_url NOT IN (
				SELECT url FROM crawlobserver.pages FINAL WHERE crawl_session_id = ? AND status_code = 304
		  )`, currentSessionID, deltaSessionID, deltaSessionID)
}

func (s *Store) copySnapshotPages(ctx context.Context, sourceSessionID, targetSessionID string) error {
	return s.conn.Exec(ctx, fmt.Sprintf(`
		INSERT INTO crawlobserver.pages (%s)
		SELECT toUUID(?), %s
		FROM crawlobserver.pages FINAL
		WHERE crawl_session_id = ?`, snapshotPageColumns, snapshotPageSelectColumns),
		targetSessionID, sourceSessionID,
	)
}

func (s *Store) copySnapshotLinks(ctx context.Context, sourceSessionID, targetSessionID string) error {
	return s.conn.Exec(ctx, `
		INSERT INTO crawlobserver.links (
			crawl_session_id, source_url, target_url, anchor_text, rel, is_internal, tag, link_location, crawled_at
		)
		SELECT toUUID(?), source_url, target_url, anchor_text, rel, is_internal, tag, link_location, crawled_at
		FROM crawlobserver.links
		WHERE crawl_session_id = ?`,
		targetSessionID, sourceSessionID,
	)
}

func (s *Store) copySnapshotSitemaps(ctx context.Context, sourceSessionID, targetSessionID string) error {
	return s.conn.Exec(ctx, `
		INSERT INTO crawlobserver.sitemaps (
			crawl_session_id, url, type, url_count, parent_url, status_code, fetched_at
		)
		SELECT toUUID(?), url, type, url_count, parent_url, status_code, fetched_at
		FROM crawlobserver.sitemaps FINAL
		WHERE crawl_session_id = ?`,
		targetSessionID, sourceSessionID,
	)
}

func (s *Store) copySnapshotSitemapURLs(ctx context.Context, sourceSessionID, targetSessionID string) error {
	return s.conn.Exec(ctx, `
		INSERT INTO crawlobserver.sitemap_urls (
			crawl_session_id, sitemap_url, loc, lastmod, changefreq, priority
		)
		SELECT toUUID(?), sitemap_url, loc, lastmod, changefreq, priority
		FROM crawlobserver.sitemap_urls FINAL
		WHERE crawl_session_id = ?`,
		targetSessionID, sourceSessionID,
	)
}

func (s *Store) countProjectCurrentSnapshotDeltas(ctx context.Context, projectID string) (uint32, error) {
	var count uint32
	err := s.conn.QueryRow(ctx, `
		SELECT toUInt32(count())
		FROM crawlobserver.project_current_snapshot_deltas FINAL
		WHERE project_id = ?`, projectID).Scan(&count)
	return count, err
}

func (s *Store) listProjectCurrentSnapshotDeltas(ctx context.Context, projectID string) ([]string, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT toString(delta_session_id)
		FROM crawlobserver.project_current_snapshot_deltas FINAL
		WHERE project_id = ?
		ORDER BY applied_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

func (s *Store) trimProjectCurrentSnapshotDeltas(ctx context.Context, projectID string, maxDeltas int) error {
	ids, err := s.listProjectCurrentSnapshotDeltas(ctx, projectID)
	if err != nil {
		return err
	}
	if maxDeltas < 0 {
		maxDeltas = 0
	}
	if len(ids) <= maxDeltas {
		return nil
	}
	for _, id := range ids[maxDeltas:] {
		if err := s.conn.Exec(ctx, `
			ALTER TABLE crawlobserver.project_current_snapshot_deltas DELETE
			WHERE project_id = ? AND delta_session_id = toUUID(?)
			SETTINGS mutations_sync = 1`, projectID, id); err != nil {
			return err
		}
		s.deleteDeltaSnapshotSession(ctx, id)
	}
	return nil
}

func (s *Store) clearProjectCurrentSnapshotDeltas(ctx context.Context, projectID string) error {
	return s.conn.Exec(ctx, `
		ALTER TABLE crawlobserver.project_current_snapshot_deltas DELETE
		WHERE project_id = ?
		SETTINGS mutations_sync = 1`, projectID)
}

func (s *Store) DeleteProjectCurrentSnapshot(ctx context.Context, projectID string) error {
	if projectID == "" {
		return nil
	}
	snap, err := s.GetProjectCurrentSnapshot(ctx, projectID)
	if err == nil {
		s.deleteSyntheticSnapshotSession(ctx, snap.CurrentSessionID)
		s.deleteSyntheticSnapshotSession(ctx, snap.BaselineSessionID)
	}
	if err := s.clearProjectCurrentSnapshotDeltas(ctx, projectID); err != nil {
		return err
	}
	if err := s.conn.Exec(ctx, `
		ALTER TABLE crawlobserver.project_current_snapshots DELETE
		WHERE project_id = ?
		SETTINGS mutations_sync = 1`, projectID); err != nil {
		return fmt.Errorf("deleting project current snapshot metadata: %w", err)
	}
	if err := s.conn.Exec(ctx, `
		ALTER TABLE crawlobserver.project_current_snapshot_promotions_v2 DELETE
		WHERE project_id = ?
		SETTINGS mutations_sync = 1`, projectID); err != nil {
		return fmt.Errorf("deleting current snapshot promotion journal: %w", err)
	}
	// Keep the retained v1 table from re-seeding v2 on the next startup.
	if err := s.conn.Exec(ctx, `
		ALTER TABLE crawlobserver.project_current_snapshot_promotions DELETE
		WHERE project_id = ?
		SETTINGS mutations_sync = 1`, projectID); err != nil {
		return fmt.Errorf("deleting legacy current snapshot promotion journal: %w", err)
	}
	return nil
}

func (s *Store) deleteSyntheticSnapshotSession(ctx context.Context, sessionID string) {
	if err := s.deleteSyntheticSnapshotSessionChecked(ctx, sessionID); err != nil {
		applog.Warnf("storage", "deleting old synthetic snapshot session %s: %v", sessionID, err)
	}
}

func (s *Store) deleteSyntheticSnapshotSessionChecked(ctx context.Context, sessionID string) error {
	if !isValidUUID(sessionID) {
		return nil
	}
	sess, err := s.GetSession(ctx, sessionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	label := strings.TrimSpace(sess.Label)
	if label != CurrentSnapshotLabel && label != CurrentBaselineSnapshotLabel {
		return nil
	}
	return s.deleteSession(ctx, sessionID, true)
}

// deleteDeltaSnapshotMetadataOnly reclaims immutable facts for an already
// pruned raw delta after no canonical DeltaPlan names it as a predecessor.
func (s *Store) deleteDeltaSnapshotMetadataOnly(ctx context.Context, sessionID string) error {
	if !isValidUUID(sessionID) {
		return nil
	}
	for _, table := range []string{
		"crawl_quality_findings", "crawl_quality_evaluations", "crawl_quality_evaluation_findings",
		"crawl_quality_promotion_events", "crawl_quality_action_events", "pagerank_evidence",
	} {
		if err := s.conn.Exec(ctx, fmt.Sprintf("ALTER TABLE crawlobserver.%s DROP PARTITION ?", table), sessionID); err != nil {
			return fmt.Errorf("deleting expired delta metadata from %s: %w", table, err)
		}
	}
	if err := s.conn.Exec(ctx, `ALTER TABLE crawlobserver.crawl_quality_results DELETE WHERE session_id = ? SETTINGS mutations_sync = 1`, sessionID); err != nil {
		return err
	}
	if err := s.conn.Exec(ctx, `ALTER TABLE crawlobserver.crawl_quality_current_pointers DELETE WHERE session_id = ? SETTINGS mutations_sync = 1`, sessionID); err != nil {
		return err
	}
	return nil
}

func (s *Store) deleteDeltaSnapshotSession(ctx context.Context, sessionID string) {
	if err := s.deleteDeltaSnapshotSessionChecked(ctx, sessionID); err != nil {
		applog.Warnf("storage", "deleting retained snapshot delta session %s: %v", sessionID, err)
	}
}

func (s *Store) deleteDeltaSnapshotSessionChecked(ctx context.Context, sessionID string) error {
	if !isValidUUID(sessionID) {
		return nil
	}
	sess, err := s.GetSession(ctx, sessionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			protected, _, protectErr := s.isSessionSnapshotProtected(ctx, sessionID)
			if protectErr != nil {
				return protectErr
			}
			if protected {
				return nil
			}
			return s.deleteDeltaSnapshotMetadataOnly(ctx, sessionID)
		}
		return err
	}
	if !strings.Contains(strings.ToLower(strings.TrimSpace(sess.Label)), "daily delta") {
		applog.Warnf("storage", "not deleting retained snapshot delta %s: unexpected label %q", sessionID, sess.Label)
		return nil
	}
	if sess.ProjectID != nil {
		retain, err := s.currentSnapshotRetainsDeltaPlanPredecessor(ctx, *sess.ProjectID, sessionID)
		if err != nil {
			return err
		}
		if retain {
			return s.pruneDeltaSnapshotHeavyContent(ctx, sessionID)
		}
	}
	return s.deleteSession(ctx, sessionID, true)
}

// currentSnapshotRetainsDeltaPlanPredecessor identifies the exact predecessor
// named by the live materialization's DeltaPlan. It is retained only as
// immutable replay evidence, never as a scheduler-visible raw crawl.
func (s *Store) currentSnapshotRetainsDeltaPlanPredecessor(ctx context.Context, projectID, sessionID string) (bool, error) {
	snap, err := s.GetProjectCurrentSnapshot(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	current, err := s.GetSession(ctx, snap.ContentWatermarkSessionID)
	if err != nil {
		return false, err
	}
	var cfg config.Config
	if err := json.Unmarshal([]byte(current.Config), &cfg); err != nil {
		return false, fmt.Errorf("decoding live delta plan retention lineage: %w", err)
	}
	if cfg.Crawler.DeltaPlan == nil {
		return false, nil
	}
	return cfg.Crawler.DeltaPlan.BaselineContentWatermarkSessionID == sessionID, nil
}

func (s *Store) pruneDeltaSnapshotHeavyContent(ctx context.Context, sessionID string) error {
	for _, table := range []string{
		"pages", "links", "robots_txt", "sitemaps", "sitemap_urls", "external_link_checks",
		"page_resource_checks", "page_resource_refs", "extractions", "near_duplicate_pairs",
		"retry_attempts", "structured_data_items", "hreflang_issues", "interlinking_opportunities",
		"interlinking_simulation_results", "interlinking_simulations", "page_embeddings",
	} {
		if err := s.conn.Exec(ctx, fmt.Sprintf("ALTER TABLE crawlobserver.%s DROP PARTITION ?", table), sessionID); err != nil {
			return fmt.Errorf("pruning retained delta content from %s: %w", table, err)
		}
	}
	// Keep immutable quality/evidence and journal facts, but remove the crawl
	// session itself so the scheduler and UI cannot revive this predecessor as
	// a normal Daily Delta. Historical replay addresses its exact facts directly.
	if err := s.conn.Exec(ctx, `ALTER TABLE crawlobserver.crawl_sessions DELETE WHERE id = ? SETTINGS mutations_sync = 1`, sessionID); err != nil {
		return fmt.Errorf("removing retained delta raw session: %w", err)
	}
	return nil
}
