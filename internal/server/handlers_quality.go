package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SEObserver/crawlobserver/internal/apikeys"
	"github.com/SEObserver/crawlobserver/internal/applog"
	"github.com/SEObserver/crawlobserver/internal/config"
	"github.com/SEObserver/crawlobserver/internal/storage"
	"github.com/google/uuid"
)

const qualityEvaluatorRevision = "quality-evaluator-v2"

type qualityStorage interface {
	UpsertCrawlQualityResult(ctx context.Context, result storage.CrawlQualityResult) error
	PublishCrawlQualityEvaluation(ctx context.Context, result storage.CrawlQualityResult, expectedCurrentRevision string) (bool, *storage.CrawlQualityResult, error)
	EnsureLegacyQualityImported(ctx context.Context, sessionID string) (*storage.CrawlQualityResult, error)
	GetCrawlQualityResult(ctx context.Context, sessionID string) (*storage.CrawlQualityResult, error)
	ListCrawlQualityHistory(ctx context.Context, sessionID string) ([]storage.CrawlQualityResult, error)
	CrawlQualityResultsForSessions(ctx context.Context, sessionIDs []string) (map[string]storage.CrawlQualityResult, error)
	LatestTrustedFullCrawlSession(ctx context.Context, projectID, excludeSessionID string) (*storage.CrawlSession, error)
	CrawlQualityMetrics(ctx context.Context, sessionID string, topN int) (*storage.CrawlQualityMetrics, error)
	TopPageRankURLs(ctx context.Context, sessionID string, limit int) ([]string, error)
	CanaryPageCheck(ctx context.Context, sessionID, canaryURL string) (*storage.CanaryPageCheck, error)
	CountMatchedPagesForURLs(ctx context.Context, sessionID string, urls []string) (int, error)
	LatestPageRankEvidence(ctx context.Context, sessionID string) (*storage.PageRankEvidence, error)
	LatestFinalizedPageRankEvidence(ctx context.Context, sessionID string) (*storage.PageRankEvidence, error)
	AdoptObservedPageRankEvidence(ctx context.Context, sessionID string, opts storage.PageRankOptions) (*storage.PageRankEvidence, error)
	RecordQualityPromotionEvent(ctx context.Context, event storage.CrawlQualityPromotionEvent) (bool, *storage.CrawlQualityPromotionEvent, error)
	LatestQualityPromotionEvent(ctx context.Context, projectID, sessionID string) (*storage.CrawlQualityPromotionEvent, error)
	RecordQualityActionEvent(ctx context.Context, event storage.CrawlQualityActionEvent) (*storage.CrawlQualityActionEvent, error)
}

func (s *Server) qualityStore() (qualityStorage, bool) {
	qs, ok := s.store.(qualityStorage)
	return qs, ok
}

type currentSnapshotStorage interface {
	GetProjectCurrentSnapshot(ctx context.Context, projectID string) (*storage.ProjectCurrentSnapshot, error)
	ValidateProjectCurrentSnapshotBinding(ctx context.Context, snap storage.ProjectCurrentSnapshot) (*storage.CrawlQualityResult, *storage.PageRankEvidence, error)
	InitializeProjectCurrentSnapshot(ctx context.Context, projectID, baselineSessionID string, binding storage.CrawlQualityPromotionEvent) (*storage.ProjectCurrentSnapshot, error)
	PromoteDeltaToCurrentSnapshot(ctx context.Context, projectID, deltaSessionID, baselineSessionID string, maxDeltas, foldIntervalDays int, opts storage.PageRankOptions, binding storage.CrawlQualityPromotionEvent) (*storage.ProjectCurrentSnapshot, error)
}

type currentSnapshotCleanupStorage interface {
	DeleteProjectCurrentSnapshot(ctx context.Context, projectID string) error
}

func (s *Server) currentSnapshotStore() (currentSnapshotStorage, bool) {
	cs, ok := s.store.(currentSnapshotStorage)
	return cs, ok
}

func (s *Server) deleteProjectCurrentSnapshot(ctx context.Context, projectID string) error {
	cs, ok := s.store.(currentSnapshotCleanupStorage)
	if !ok {
		return nil
	}
	return cs.DeleteProjectCurrentSnapshot(ctx, projectID)
}

func (s *Server) handleProjectQualitySettings(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !requireProjectAccess(w, r, projectID) {
		return
	}
	settings, err := s.keyStore.GetProjectQualitySettings(projectID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, settings)
}

func (s *Server) handleUpdateProjectQualitySettings(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAccess(w, r) {
		return
	}
	projectID := r.PathValue("id")
	if !requireProjectAccess(w, r, projectID) {
		return
	}
	var body apikeys.ProjectQualitySettings
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	body.ProjectID = projectID
	settings, err := s.keyStore.SaveProjectQualitySettings(body)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, settings)
}

func (s *Server) handleProjectCanaries(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !requireProjectAccess(w, r, projectID) {
		return
	}
	canaries, err := s.keyStore.ListProjectCanaries(projectID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, canaries)
}

func (s *Server) handleCreateProjectCanary(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAccess(w, r) {
		return
	}
	projectID := r.PathValue("id")
	if !requireProjectAccess(w, r, projectID) {
		return
	}
	var body apikeys.ProjectCanary
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	body.ProjectID = projectID
	canary, err := s.keyStore.SaveProjectCanary(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, canary)
}

func (s *Server) handleUpdateProjectCanary(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAccess(w, r) {
		return
	}
	projectID := r.PathValue("id")
	if !requireProjectAccess(w, r, projectID) {
		return
	}
	var body apikeys.ProjectCanary
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	body.ID = r.PathValue("canaryId")
	body.ProjectID = projectID
	canary, err := s.keyStore.SaveProjectCanary(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, canary)
}

func (s *Server) handleDeleteProjectCanary(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAccess(w, r) {
		return
	}
	projectID := r.PathValue("id")
	if !requireProjectAccess(w, r, projectID) {
		return
	}
	if err := s.keyStore.DeleteProjectCanary(projectID, r.PathValue("canaryId")); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, map[string]any{"deleted": true})
}

func (s *Server) handleSessionQuality(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if !s.requireSessionAccess(w, r, sessionID) {
		return
	}
	qs, ok := s.qualityStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "quality storage unavailable")
		return
	}
	result, err := qs.GetCrawlQualityResult(r.Context(), sessionID)
	if err != nil {
		if isNotFoundErr(err) {
			writeError(w, http.StatusNotFound, "quality result not found")
			return
		}
		internalError(w, r, err)
		return
	}
	result = s.deriveCurrentQualityReadState(r.Context(), qs, result)
	if promotion, promotionErr := qs.LatestQualityPromotionEvent(r.Context(), result.ProjectID, sessionID); promotionErr == nil && promotion != nil && promotion.EvaluationRevision == result.EvaluationRevision {
		result.PromotionStatus = promotion.Status
	}
	writeJSON(w, result)
}

func deriveCurrentQualityReadState(ctx context.Context, qs qualityStorage, stored *storage.CrawlQualityResult) *storage.CrawlQualityResult {
	if stored == nil {
		return nil
	}
	result := *stored
	result.StaleReasons = append([]string(nil), stored.StaleReasons...)
	if result.EvaluatorRevision != qualityEvaluatorRevision {
		markQualityReadStale(&result, "evaluator_revision_changed")
	}
	if result.PageRankPredicateVersion != storage.PageRankEligiblePredicateVersion {
		markQualityReadStale(&result, "pagerank_predicate_version_changed")
	}
	evidence, err := qs.LatestPageRankEvidence(ctx, result.SessionID)
	switch {
	case err != nil:
		markQualityReadStale(&result, "pagerank_evidence_unavailable")
	case evidence.State != storage.PageRankEvidenceFinalized:
		markQualityReadStale(&result, "pagerank_evidence_not_finalized")
	case evidence.AttemptID != result.PageRankEvidenceRevision:
		markQualityReadStale(&result, "pagerank_evidence_revision_changed")
	case evidence.PredicateVersion != storage.PageRankEligiblePredicateVersion:
		markQualityReadStale(&result, "pagerank_predicate_version_changed")
	}
	return &result
}

func (s *Server) deriveCurrentQualityReadState(ctx context.Context, qs qualityStorage, stored *storage.CrawlQualityResult) *storage.CrawlQualityResult {
	result := deriveCurrentQualityReadState(ctx, qs, stored)
	if result == nil || s.keyStore == nil || result.ProjectID == "" {
		return result
	}
	rulesRevision, err := s.currentQualityRulesRevision(result.ProjectID)
	if err != nil {
		markQualityReadStale(result, "quality_rules_unavailable")
	} else if result.RulesRevision != rulesRevision {
		markQualityReadStale(result, "quality_rules_revision_changed")
	}
	return result
}

func markQualityReadStale(result *storage.CrawlQualityResult, reason string) {
	if result == nil {
		return
	}
	for _, existing := range result.StaleReasons {
		if existing == reason {
			return
		}
	}
	result.StaleReasons = append(result.StaleReasons, reason)
	result.Stale = true
	result.Trusted = false
	result.Status = "untrusted"
	result.Summary = "Crawl data is stale/untrusted because the published quality evaluation no longer matches current evidence."
}

func (s *Server) handleSessionQualityHistory(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if !s.requireSessionAccess(w, r, sessionID) {
		return
	}
	qs, ok := s.qualityStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "quality storage unavailable")
		return
	}
	history, err := qs.ListCrawlQualityHistory(r.Context(), sessionID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	if history == nil {
		history = []storage.CrawlQualityResult{}
	}
	if len(history) > 0 {
		if promotion, promotionErr := qs.LatestQualityPromotionEvent(r.Context(), history[0].ProjectID, sessionID); promotionErr == nil {
			for i := range history {
				if history[i].EvaluationRevision == promotion.EvaluationRevision {
					history[i].PromotionStatus = promotion.Status
				}
			}
		}
	}
	writeJSON(w, history)
}

func (s *Server) handleSessionPageRankEvidence(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if !s.requireSessionAccess(w, r, sessionID) {
		return
	}
	qs, ok := s.qualityStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "quality storage unavailable")
		return
	}
	evidence, err := qs.LatestPageRankEvidence(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, storage.ErrNoFinalizedPageRankEvidence) || isNotFoundErr(err) {
			writeError(w, http.StatusNotFound, "pagerank evidence not found")
			return
		}
		internalError(w, r, err)
		return
	}
	evidence.Failure = storage.SanitizePageRankEvidenceFailure(evidence.Failure)
	writeJSON(w, evidence)
}

func (s *Server) handleReevaluateSessionQuality(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAccess(w, r) {
		return
	}
	sessionID := r.PathValue("id")
	if !s.requireSessionAccess(w, r, sessionID) {
		return
	}
	var body storage.QualityReevaluateRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	body.Reason = strings.Join(strings.Fields(body.Reason), " ")
	if !body.Confirm {
		writeError(w, http.StatusBadRequest, "confirm must be true")
		return
	}
	if len(body.Reason) < 3 || len(body.Reason) > 500 {
		writeError(w, http.StatusBadRequest, "reason must contain 3 to 500 characters")
		return
	}
	sess, err := s.store.GetSession(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if !qualityEvaluableStatus(sess.Status) || s.manager.IsRunning(sessionID) || s.manager.IsQueued(sessionID) {
		writeError(w, http.StatusBadRequest, "quality can only be re-evaluated for a terminal, inactive session")
		return
	}
	qs, ok := s.qualityStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "quality storage unavailable")
		return
	}
	current, err := qs.EnsureLegacyQualityImported(r.Context(), sessionID)
	if err != nil && !isNotFoundErr(err) {
		internalError(w, r, err)
		return
	}
	currentRevision := ""
	if current != nil {
		currentRevision = current.EvaluationRevision
	}
	if body.ExpectedEvaluationRevision != "" && body.ExpectedEvaluationRevision != currentRevision {
		if auditErr := s.recordQualityAction(r.Context(), qs, storage.CrawlQualityActionEvent{
			SessionID: sessionID, Action: "quality_re_evaluate", Source: "admin_api", Actor: qualityAuditActor(r),
			Reason: body.Reason, ExpectedEvaluationRevision: body.ExpectedEvaluationRevision,
			PreviousEvaluationRevision: currentRevision, ExpectedPageRankEvidenceRevision: body.ExpectedPageRankEvidenceRevision,
			Status: "conflict", OccurredAt: time.Now().UTC(),
		}); auditErr != nil {
			internalError(w, r, auditErr)
			return
		}
		writeQualityEvaluationConflict(w, body.ExpectedEvaluationRevision, current)
		return
	}
	evidence, err := qs.LatestPageRankEvidence(r.Context(), sessionID)
	if err != nil && (errors.Is(err, storage.ErrNoFinalizedPageRankEvidence) || isNotFoundErr(err)) {
		opts, optionsErr := s.pageRankOptionsForSession(r.Context(), sessionID)
		if optionsErr != nil {
			internalError(w, r, optionsErr)
			return
		}
		evidence, err = qs.AdoptObservedPageRankEvidence(r.Context(), sessionID, opts)
	}
	if err != nil {
		if auditErr := s.recordQualityAction(r.Context(), qs, storage.CrawlQualityActionEvent{
			SessionID: sessionID, Action: "quality_re_evaluate", Source: "admin_api", Actor: qualityAuditActor(r),
			Reason: body.Reason, ExpectedEvaluationRevision: body.ExpectedEvaluationRevision,
			PreviousEvaluationRevision: currentRevision, ExpectedPageRankEvidenceRevision: body.ExpectedPageRankEvidenceRevision,
			Status: "rejected", OccurredAt: time.Now().UTC(),
		}); auditErr != nil {
			internalError(w, r, auditErr)
			return
		}
		writeError(w, http.StatusConflict, "finalized pagerank evidence is unavailable")
		return
	}
	if evidence.State != storage.PageRankEvidenceFinalized {
		if auditErr := s.recordQualityAction(r.Context(), qs, storage.CrawlQualityActionEvent{
			SessionID: sessionID, Action: "quality_re_evaluate", Source: "admin_api", Actor: qualityAuditActor(r),
			Reason: body.Reason, ExpectedEvaluationRevision: body.ExpectedEvaluationRevision,
			PreviousEvaluationRevision: currentRevision, ExpectedPageRankEvidenceRevision: body.ExpectedPageRankEvidenceRevision,
			PageRankEvidenceRevision: evidence.AttemptID, Status: "conflict", OccurredAt: time.Now().UTC(),
		}); auditErr != nil {
			internalError(w, r, auditErr)
			return
		}
		writeQualityEvidenceConflict(w, "pagerank_evidence_not_finalized", body.ExpectedPageRankEvidenceRevision, evidence)
		return
	}
	if body.ExpectedPageRankEvidenceRevision != "" && body.ExpectedPageRankEvidenceRevision != evidence.AttemptID {
		if auditErr := s.recordQualityAction(r.Context(), qs, storage.CrawlQualityActionEvent{
			SessionID: sessionID, Action: "quality_re_evaluate", Source: "admin_api", Actor: qualityAuditActor(r),
			Reason: body.Reason, ExpectedEvaluationRevision: body.ExpectedEvaluationRevision,
			PreviousEvaluationRevision: currentRevision, ExpectedPageRankEvidenceRevision: body.ExpectedPageRankEvidenceRevision,
			PageRankEvidenceRevision: evidence.AttemptID, Status: "conflict", OccurredAt: time.Now().UTC(),
		}); auditErr != nil {
			internalError(w, r, auditErr)
			return
		}
		writeQualityEvidenceConflict(w, "pagerank_evidence_revision_conflict", body.ExpectedPageRankEvidenceRevision, evidence)
		return
	}
	actionID := uuid.NewString()
	startedAction := storage.CrawlQualityActionEvent{
		SessionID: sessionID, ActionID: actionID, Action: "quality_re_evaluate", Source: "admin_api", Actor: qualityAuditActor(r),
		Reason: body.Reason, ExpectedEvaluationRevision: body.ExpectedEvaluationRevision,
		PreviousEvaluationRevision: currentRevision, ExpectedPageRankEvidenceRevision: body.ExpectedPageRankEvidenceRevision,
		PageRankEvidenceRevision: evidence.AttemptID, Status: "started", OccurredAt: time.Now().UTC(),
	}
	if auditErr := s.recordQualityAction(r.Context(), qs, startedAction); auditErr != nil {
		internalError(w, r, auditErr)
		return
	}
	result, changed, promotionChanged, promotion, err := s.evaluateAndPublishSessionQuality(r.Context(), *sess, evidence, "admin_re_evaluate", currentRevision, body.Reason)
	action := storage.CrawlQualityActionEvent{
		SessionID: sessionID, ActionID: actionID, Action: "quality_re_evaluate", Source: "admin_api", Actor: qualityAuditActor(r), Reason: body.Reason,
		ExpectedEvaluationRevision: body.ExpectedEvaluationRevision, PreviousEvaluationRevision: currentRevision,
		ExpectedPageRankEvidenceRevision: body.ExpectedPageRankEvidenceRevision, PageRankEvidenceRevision: evidence.AttemptID,
		Status: "applied", OccurredAt: time.Now().UTC(),
	}
	if result != nil {
		action.ResultEvaluationRevision = result.EvaluationRevision
	}
	if err != nil {
		action.Status = "failed"
	}
	action = sanitizeQualityActionEvent(action)
	if _, auditErr := qs.RecordQualityActionEvent(r.Context(), action); auditErr != nil {
		if err != nil {
			err = fmt.Errorf("quality re-evaluation failed: %v; terminal audit failed: %w", err, auditErr)
		} else {
			err = auditErr
		}
	}
	if err != nil {
		if isQualityConflict(err) {
			latest, _ := qs.GetCrawlQualityResult(r.Context(), sessionID)
			writeQualityEvaluationConflict(w, body.ExpectedEvaluationRevision, latest)
			return
		}
		internalError(w, r, err)
		return
	}
	writeJSON(w, storage.QualityReevaluateResponse{
		Changed: changed, PromotionChanged: promotionChanged, Result: result, Evidence: evidence, Promotion: promotion,
	})
}

func (s *Server) recordQualityAction(ctx context.Context, qs qualityStorage, event storage.CrawlQualityActionEvent) error {
	event = sanitizeQualityActionEvent(event)
	_, err := qs.RecordQualityActionEvent(ctx, event)
	return err
}

func qualityAuditActor(r *http.Request) string {
	auth := apikeys.FromContext(r.Context())
	if auth == nil {
		return "local"
	}
	switch auth.Method {
	case "session":
		if auth.Username != "" {
			return sanitizeQualityAuditActor("session:" + auth.Username)
		}
		return "session"
	case "apikey":
		return sanitizeQualityAuditActor("apikey:" + auth.KeyType)
	case "basic":
		return "basic"
	default:
		return "authenticated"
	}
}

func writeQualityEvaluationConflict(w http.ResponseWriter, expected string, current *storage.CrawlQualityResult) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": "evaluation_revision_conflict", "expected_revision": expected, "current_quality": current,
	})
}

func writeQualityEvidenceConflict(w http.ResponseWriter, code, expected string, evidence *storage.PageRankEvidence) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": code, "expected_revision": expected, "current_evidence": evidence,
	})
}

func isQualityConflict(err error) bool {
	var conflict *storage.QualityEvaluationConflictError
	return errors.As(err, &conflict)
}

func (s *Server) handleProjectCurrentSnapshot(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !requireProjectAccess(w, r, projectID) {
		return
	}
	cs, ok := s.currentSnapshotStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "current snapshot storage unavailable")
		return
	}
	qs, ok := s.qualityStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "quality storage unavailable")
		return
	}
	snap, err := cs.GetProjectCurrentSnapshot(r.Context(), projectID)
	if err != nil {
		if isNotFoundErr(err) {
			snap, err = s.initializeCurrentSnapshotFromTrustedBaseline(r.Context(), projectID, cs)
			if err != nil {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
			writeJSON(w, snap)
			return
		}
		internalError(w, r, err)
		return
	}
	if _, err := s.store.GetSession(r.Context(), snap.CurrentSessionID); err != nil {
		snap, err = s.initializeCurrentSnapshotFromTrustedBaseline(r.Context(), projectID, cs)
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, snap)
		return
	}
	if _, err := s.store.GetSession(r.Context(), snap.BaselineSessionID); err != nil {
		writeError(w, http.StatusConflict, "current snapshot baseline is missing; reconcile a trusted bound baseline")
		return
	}
	if snap.QualityEvaluationRevision == "" || snap.PageRankEvidenceRevision == "" || snap.QualityPromotionStatus != "applied" {
		writeCurrentSnapshotBindingConflict(w, "current_snapshot_binding_incomplete", snap, nil, nil)
		return
	}
	quality, evidence, err := cs.ValidateProjectCurrentSnapshotBinding(r.Context(), *snap)
	if err != nil {
		if errors.Is(err, storage.ErrCurrentSnapshotBindingConflict) {
			writeCurrentSnapshotBindingConflict(w, "current_snapshot_binding_stale", snap, quality, evidence)
			return
		}
		internalError(w, r, err)
		return
	}
	quality = s.deriveCurrentQualityReadState(r.Context(), qs, quality)
	if quality == nil || quality.Stale || !quality.Trusted {
		writeCurrentSnapshotBindingConflict(w, "current_snapshot_binding_stale", snap, quality, evidence)
		return
	}
	writeJSON(w, snap)
}

func writeCurrentSnapshotBindingConflict(w http.ResponseWriter, code string, snap *storage.ProjectCurrentSnapshot, quality *storage.CrawlQualityResult, evidence *storage.PageRankEvidence) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": code, "current_snapshot": snap, "current_quality": quality, "current_evidence": evidence,
	})
}

func (s *Server) initializeCurrentSnapshotFromTrustedBaseline(ctx context.Context, projectID string, cs currentSnapshotStorage) (*storage.ProjectCurrentSnapshot, error) {
	lock := qualityPromotionLock(projectID)
	lock.Lock()
	defer lock.Unlock()
	qs, ok := s.qualityStore()
	if !ok {
		return nil, fmt.Errorf("baseline_required: quality storage unavailable")
	}
	baseline, err := qs.LatestTrustedFullCrawlSession(ctx, projectID, "")
	if err != nil {
		return nil, fmt.Errorf("baseline_required: no trusted full-crawl baseline exists; run a full crawl before using current snapshot")
	}
	quality, err := qs.GetCrawlQualityResult(ctx, baseline.ID)
	quality = s.deriveCurrentQualityReadState(ctx, qs, quality)
	if err != nil || quality == nil || quality.Stale || !quality.Trusted || quality.EvaluationRevision == "" || quality.PageRankEvidenceRevision == "" {
		return nil, fmt.Errorf("baseline_required: trusted baseline has no bound quality and PageRank evidence")
	}
	binding := storage.CrawlQualityPromotionEvent{
		ProjectID: projectID, SessionID: baseline.ID, EvaluationRevision: quality.EvaluationRevision,
		PageRankEvidenceRevision: quality.PageRankEvidenceRevision, BaselineSessionID: baseline.ID,
		BaselineEvaluationRevision: quality.EvaluationRevision, EvaluatorRevision: quality.EvaluatorRevision,
		RulesRevision: quality.RulesRevision, Status: "started", Reason: "lazy current snapshot initialization",
	}
	if _, _, recordErr := recordQualityPromotion(ctx, qs, binding); recordErr != nil {
		return nil, fmt.Errorf("recording current snapshot initialization start: %w", recordErr)
	}
	snap, err := cs.InitializeProjectCurrentSnapshot(ctx, projectID, baseline.ID, binding)
	if err != nil {
		binding.Status = "failed"
		binding.Detail = sanitizeQualityAuditDetail(err.Error())
		binding.OccurredAt = time.Now().UTC()
		if _, _, recordErr := recordQualityPromotion(ctx, qs, binding); recordErr != nil {
			return nil, fmt.Errorf("current snapshot initialization failed: %v; recording failure: %w", err, recordErr)
		}
		return nil, err
	}
	binding.Status = "applied"
	binding.Detail = "current snapshot initialized from trusted bound baseline"
	binding.OccurredAt = time.Now().UTC()
	if _, _, recordErr := recordQualityPromotion(ctx, qs, binding); recordErr != nil {
		return nil, fmt.Errorf("recording current snapshot initialization result: %w", recordErr)
	}
	return snap, nil
}

func (s *Server) startQualityScheduler() {
	if s.keyStore == nil || s.store == nil {
		return
	}
	s.qualitySchedulerMu.Lock()
	if s.qualitySchedulerCancel != nil {
		s.qualitySchedulerMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.qualitySchedulerCancel = cancel
	s.qualitySchedulerMu.Unlock()
	go s.runQualityScheduler(ctx)
}

func (s *Server) stopQualityScheduler() {
	s.qualitySchedulerMu.Lock()
	defer s.qualitySchedulerMu.Unlock()
	if s.qualitySchedulerCancel != nil {
		s.qualitySchedulerCancel()
		s.qualitySchedulerCancel = nil
	}
}

func (s *Server) runQualityScheduler(ctx context.Context) {
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.evaluateMissingQuality(ctx, 20)
			timer.Reset(time.Minute)
		}
	}
}

func (s *Server) evaluateMissingQuality(ctx context.Context, limit int) {
	if limit <= 0 {
		return
	}
	qs, ok := s.qualityStore()
	if !ok {
		return
	}
	sessions, err := s.store.ListSessions(ctx)
	if err != nil {
		return
	}
	sort.SliceStable(sessions, func(i, j int) bool {
		if sessions[i].StartedAt.Equal(sessions[j].StartedAt) {
			return sessions[i].ID > sessions[j].ID
		}
		return sessions[i].StartedAt.After(sessions[j].StartedAt)
	})
	if len(sessions) == 0 {
		s.setQualitySchedulerCursor(0)
		return
	}
	start := s.qualitySchedulerStart(len(sessions))
	maxScanned := limit * 10
	if maxScanned < 20 {
		maxScanned = 20
	}
	if maxScanned > len(sessions) {
		maxScanned = len(sessions)
	}
	done := 0
	scanned := 0
	for scanned < maxScanned && done < limit && ctx.Err() == nil {
		sess := sessions[(start+scanned)%len(sessions)]
		scanned++
		if sess.ProjectID == nil || !qualityEvaluableStatus(sess.Status) {
			continue
		}
		previous, importErr := qs.EnsureLegacyQualityImported(ctx, sess.ID)
		if importErr != nil && !isNotFoundErr(importErr) {
			applog.Warnf("server", "quality legacy import failed for session %s: %v", sess.ID, importErr)
			continue
		}
		evidence, evidenceErr := qs.LatestPageRankEvidence(ctx, sess.ID)
		if evidenceErr != nil && (errors.Is(evidenceErr, storage.ErrNoFinalizedPageRankEvidence) || isNotFoundErr(evidenceErr)) {
			opts, optsErr := s.pageRankOptionsForSession(ctx, sess.ID)
			if optsErr == nil {
				evidence, evidenceErr = qs.AdoptObservedPageRankEvidence(ctx, sess.ID, opts)
			}
		}
		if evidenceErr != nil {
			applog.Warnf("server", "quality pagerank evidence unavailable for session %s: %v", sess.ID, evidenceErr)
			continue
		}
		expectedRevision := ""
		if previous != nil {
			expectedRevision = previous.EvaluationRevision
		}
		_, changed, promotionChanged, _, err := s.evaluateAndPublishSessionQuality(ctx, sess, evidence, "scheduler", expectedRevision, "scheduler reconciliation")
		if err != nil {
			applog.Warnf("server", "quality evaluation failed for session %s: %v", sess.ID, err)
		}
		if changed || promotionChanged {
			done++
		}
	}
	s.setQualitySchedulerCursor((start + scanned) % len(sessions))
}

func (s *Server) qualitySchedulerStart(total int) int {
	if total <= 0 {
		return 0
	}
	s.qualitySchedulerMu.Lock()
	defer s.qualitySchedulerMu.Unlock()
	if s.qualitySchedulerCursorInitialized {
		return s.qualitySchedulerCursor % total
	}
	now := time.Now().UTC()
	if s.qualitySchedulerNow != nil {
		now = s.qualitySchedulerNow().UTC()
	}
	// A time-derived initial offset prevents a process restart from repeatedly
	// pinning bounded scans to the newest sessions. Subsequent passes use the
	// in-memory cursor, while independent restarts still advance each minute.
	minute := now.Unix() / int64(time.Minute/time.Second)
	if minute < 0 {
		minute = -minute
	}
	return int(minute % int64(total))
}

func (s *Server) setQualitySchedulerCursor(cursor int) {
	s.qualitySchedulerMu.Lock()
	s.qualitySchedulerCursor = cursor
	s.qualitySchedulerCursorInitialized = true
	s.qualitySchedulerMu.Unlock()
}

func qualityEvaluableStatus(status string) bool {
	return status == "completed" || status == "completed_with_errors"
}

func (s *Server) evaluateSessionQuality(ctx context.Context, sess storage.CrawlSession) (*storage.CrawlQualityResult, error) {
	qs, ok := s.qualityStore()
	if !ok {
		return nil, fmt.Errorf("quality storage unavailable")
	}
	evidence, err := qs.LatestPageRankEvidence(ctx, sess.ID)
	if err != nil && (errors.Is(err, storage.ErrNoFinalizedPageRankEvidence) || isNotFoundErr(err)) {
		opts, optsErr := s.pageRankOptionsForSession(ctx, sess.ID)
		if optsErr != nil {
			return nil, optsErr
		}
		evidence, err = qs.AdoptObservedPageRankEvidence(ctx, sess.ID, opts)
	}
	if err != nil {
		return nil, err
	}
	previous, _ := qs.EnsureLegacyQualityImported(ctx, sess.ID)
	expected := ""
	if previous != nil {
		expected = previous.EvaluationRevision
	}
	result, _, _, _, err := s.evaluateAndPublishSessionQuality(ctx, sess, evidence, "scheduler", expected, "scheduler reconciliation")
	return result, err
}

func (s *Server) evaluateSessionQualityResult(ctx context.Context, sess storage.CrawlSession, evidence *storage.PageRankEvidence, source string) (*storage.CrawlQualityResult, error) {
	qs, ok := s.qualityStore()
	if !ok {
		return nil, fmt.Errorf("quality storage unavailable")
	}
	if sess.ProjectID == nil || *sess.ProjectID == "" {
		return nil, fmt.Errorf("session has no project")
	}
	projectID := *sess.ProjectID
	settings, err := s.keyStore.GetProjectQualitySettings(projectID)
	if err != nil {
		return nil, err
	}
	isFull := isFullCrawlSession(sess)
	now := time.Now().UTC()
	result := storage.CrawlQualityResult{
		SessionID:   sess.ID,
		ProjectID:   projectID,
		Status:      "trusted",
		Score:       100,
		Trusted:     true,
		IsFullCrawl: isFull,
		Summary:     "Crawl data is trusted.",
		EvaluatedAt: now,
		Metrics:     map[string]interface{}{},
		Source:      source, EvaluatorRevision: qualityEvaluatorRevision,
	}
	applyPageRankEvidence(&result, evidence)
	if evidence == nil || evidence.State != storage.PageRankEvidenceFinalized || evidence.PredicateVersion != storage.PageRankEligiblePredicateVersion {
		result.Status = "untrusted"
		result.Score = 0
		result.Trusted = false
		result.Stale = true
		staleReason := "pagerank_evidence_not_finalized"
		if evidence != nil && evidence.State == storage.PageRankEvidenceFinalized {
			staleReason = "pagerank_predicate_version_changed"
		}
		result.StaleReasons = []string{staleReason}
		result.Summary = "Crawl data is untrusted because current finalized PageRank evidence is unavailable."
		result.Findings = append(result.Findings, qualityFinding(sess.ID, projectID, "error", staleReason, result.Summary, "pagerank_evidence", 0, 1, 1, true, now))
		return s.finalizeQualityRevision(ctx, qs, sess, *settings, &result)
	}
	if !settings.Enabled {
		result.Status = "warning"
		result.Score = 100
		result.Trusted = true
		result.Summary = "Quality gate is disabled for this project."
		result.Findings = append(result.Findings, qualityFinding(sess.ID, projectID, "info", "quality_disabled", "Quality gate is disabled for this project.", "", 0, 0, 0, false, now))
		return s.finalizeQualityRevision(ctx, qs, sess, *settings, &result)
	}
	if !isFull {
		if sess.Status == "completed_with_errors" {
			result.Status = "untrusted"
			result.Score = 50
			result.Trusted = false
			result.Summary = "Daily Delta completed with storage errors and was not promoted to current snapshot."
			result.Findings = append(result.Findings, qualityFinding(sess.ID, projectID, "error", "partial_crawl_errors", result.Summary, "", 0, 0, 0, true, now))
			return s.finalizeQualityRevision(ctx, qs, sess, *settings, &result)
		}
		result.IsFullCrawl = false
		result.Metrics["pages_crawled"] = sess.PagesCrawled
		result.Metrics["planned_pages"] = crawlConfigDeltaPlannedPages(sess.Config)
		if plan := crawlConfigDeltaPlan(sess.Config); plan != nil {
			addDeltaPlanMetrics(result.Metrics, plan)
		}
		current, err := qs.CrawlQualityMetrics(ctx, sess.ID, settings.PageRankTopN)
		if err != nil {
			result.Findings = append(result.Findings, qualityFinding(sess.ID, projectID, "error", "delta_metrics_unavailable", "Daily Delta quality metrics could not be loaded.", "delta_metrics", 0, 1, 1, true, now))
		} else {
			result.Metrics["status_5xx"] = current.Status5xx
			result.Metrics["status_404"] = current.Status404
		}
		result.Findings = append(result.Findings, s.evaluateDeltaPromotionGate(ctx, qs, sess, projectID, *settings, current, now)...)
		score := 90
		blocking := false
		for _, finding := range result.Findings {
			switch finding.Severity {
			case "error":
				score -= 30
			case "warning":
				score -= 10
			}
			if finding.Blocking {
				blocking = true
			}
		}
		if score < 0 {
			score = 0
		}
		result.Score = uint8(score)
		if blocking || score < settings.UntrustedScoreBelow {
			result.Status = "untrusted"
			result.Trusted = false
			result.Summary = "Daily Delta was not promoted: delta quality gate failed."
		} else {
			result.Status = "warning"
			result.Trusted = true
			result.Summary = "Daily Delta passed promotion gates and is eligible for current snapshot promotion, but not as a full-crawl baseline."
		}
		result.Findings = append(result.Findings, qualityFinding(sess.ID, projectID, "warning", "partial_crawl", "Daily Delta is not eligible as a full-crawl baseline.", "", 0, 0, 0, false, now))
		return s.finalizeQualityRevision(ctx, qs, sess, *settings, &result)
	}

	current, err := qs.CrawlQualityMetrics(ctx, sess.ID, settings.PageRankTopN)
	if err != nil {
		return nil, err
	}
	result.Metrics["html_pages"] = current.HTMLPages
	result.Metrics["internal_links"] = current.InternalLinks
	result.Metrics["status_404"] = current.Status404
	result.Metrics["status_5xx"] = current.Status5xx
	result.Metrics["noindex"] = current.Noindex
	result.Metrics["redirects"] = current.Redirects
	result.Metrics["canonical_mismatch"] = current.CanonicalMismatch
	result.Metrics["pagerank_zero_top_pages"] = current.PageRankZeroTopPages
	if evidence != nil && evidence.State == storage.PageRankEvidenceFinalized {
		result.Metrics["pagerank_eligible_pages"] = evidence.EligiblePageCount
		result.Metrics["pagerank_positive_pages"] = evidence.PositivePageCount
		result.Metrics["pagerank_zero_pages"] = evidence.ZeroPageCount
	}

	canaries, _ := s.keyStore.ListProjectCanaries(projectID)
	result.Findings = append(result.Findings, s.evaluateCanaries(ctx, qs, sess.ID, projectID, canaries, now)...)

	baseline, err := qs.LatestTrustedFullCrawlSession(ctx, projectID, sess.ID)
	if err != nil {
		if !isNotFoundErr(err) {
			return nil, err
		}
		result.Findings = append(result.Findings, qualityFinding(sess.ID, projectID, "info", "no_trusted_baseline", "No trusted full-crawl baseline exists yet; this session can become the initial baseline if canaries pass.", "", 0, 0, 0, false, now))
	} else {
		result.BaselineSessionID = baseline.ID
		if baselineQuality, qualityErr := qs.GetCrawlQualityResult(ctx, baseline.ID); qualityErr == nil {
			result.BaselineEvaluationRevision = baselineQuality.EvaluationRevision
		}
		baselineMetrics, err := qs.CrawlQualityMetrics(ctx, baseline.ID, settings.PageRankTopN)
		if err != nil {
			return nil, err
		}
		result.Findings = append(result.Findings, compareQualityMetrics(sess.ID, projectID, current, baselineMetrics, *settings, now)...)
		result.Findings = append(result.Findings, s.evaluatePageRankOverlap(ctx, qs, sess.ID, projectID, baseline.ID, *settings, now)...)
		if crawlConfigSignature(sess.Config) != crawlConfigSignature(baseline.Config) {
			result.Findings = append(result.Findings, qualityFinding(sess.ID, projectID, "warning", "crawl_config_changed", "Crawl configuration differs from the trusted baseline; compare coverage with caution.", "", 0, 0, 0, false, now))
		}
	}

	score := 100
	blocking := false
	for _, finding := range result.Findings {
		switch finding.Severity {
		case "error":
			score -= 25
		case "warning":
			score -= 10
		}
		if finding.Blocking {
			blocking = true
		}
	}
	if score < 0 {
		score = 0
	}
	result.Score = uint8(score)
	switch {
	case blocking || score < settings.UntrustedScoreBelow:
		result.Status = "untrusted"
		result.Trusted = false
		result.Summary = "Crawl Observer data stale/untrusted: blocking data-quality findings were detected."
	case score < settings.MinTrustedScore:
		result.Status = "warning"
		result.Trusted = true
		result.Summary = "Crawl data is usable with warnings."
	default:
		result.Status = "trusted"
		result.Trusted = true
		result.Summary = "Crawl data is trusted."
	}
	return s.finalizeQualityRevision(ctx, qs, sess, *settings, &result)
}

func applyPageRankEvidence(result *storage.CrawlQualityResult, evidence *storage.PageRankEvidence) {
	if result == nil || evidence == nil {
		return
	}
	result.PageRankEvidenceRevision = evidence.AttemptID
	result.PageRankEvidenceSource = evidence.Source
	result.PageRankEvidenceStatus = evidence.State
	result.PageRankPredicateVersion = evidence.PredicateVersion
	result.PageRankEligible = evidence.EligiblePageCount
	result.PageRankPositive = evidence.PositivePageCount
	result.PageRankZero = evidence.ZeroPageCount
}

func (s *Server) finalizeQualityRevision(ctx context.Context, qs qualityStorage, sess storage.CrawlSession, settings apikeys.ProjectQualitySettings, result *storage.CrawlQualityResult) (*storage.CrawlQualityResult, error) {
	canaries, err := s.keyStore.ListProjectCanaries(result.ProjectID)
	if err != nil {
		return nil, err
	}
	result.RulesRevision, err = canonicalQualityRulesRevision(settings, canaries)
	if err != nil {
		return nil, err
	}
	revisionPayload := strings.Join([]string{
		qualityEvaluatorRevision, sess.ID, result.PageRankEvidenceRevision,
		result.RulesRevision, result.BaselineEvaluationRevision,
	}, "\x00")
	result.EvaluationRevision = uuid.NewSHA1(uuid.NameSpaceOID, []byte(revisionPayload)).String()
	for i := range result.Findings {
		result.Findings[i].EvaluationRevision = result.EvaluationRevision
		result.Findings[i].FindingIndex = uint32(i)
	}
	return result, nil
}

func (s *Server) currentQualityRulesRevision(projectID string) (string, error) {
	settings, err := s.keyStore.GetProjectQualitySettings(projectID)
	if err != nil {
		return "", err
	}
	canaries, err := s.keyStore.ListProjectCanaries(projectID)
	if err != nil {
		return "", err
	}
	return canonicalQualityRulesRevision(*settings, canaries)
}

func canonicalQualityRulesRevision(settings apikeys.ProjectQualitySettings, canaries []apikeys.ProjectCanary) (string, error) {
	settings.UpdatedAt = time.Time{}
	canaries = append([]apikeys.ProjectCanary(nil), canaries...)
	sort.Slice(canaries, func(i, j int) bool { return canaries[i].ID < canaries[j].ID })
	for i := range canaries {
		canaries[i].CreatedAt = time.Time{}
		canaries[i].UpdatedAt = time.Time{}
	}
	rulesPayload, err := json.Marshal(struct {
		Settings apikeys.ProjectQualitySettings `json:"settings"`
		Canaries []apikeys.ProjectCanary        `json:"canaries"`
	}{settings, canaries})
	if err != nil {
		return "", err
	}
	return uuid.NewSHA1(uuid.NameSpaceOID, rulesPayload).String(), nil
}

func (s *Server) evaluateAndPublishSessionQuality(ctx context.Context, sess storage.CrawlSession, evidence *storage.PageRankEvidence, source, expectedRevision, reason string) (*storage.CrawlQualityResult, bool, bool, *storage.CrawlQualityPromotionEvent, error) {
	qs, ok := s.qualityStore()
	if !ok {
		return nil, false, false, nil, fmt.Errorf("quality storage unavailable")
	}
	if sess.ProjectID == nil || *sess.ProjectID == "" {
		return nil, false, false, nil, fmt.Errorf("session has no project")
	}
	lock := qualityPromotionLock(*sess.ProjectID)
	lock.Lock()
	defer lock.Unlock()
	result, err := s.evaluateSessionQualityResult(ctx, sess, evidence, source)
	if err != nil {
		return nil, false, false, nil, err
	}
	changed, current, err := qs.PublishCrawlQualityEvaluation(ctx, *result, expectedRevision)
	if err != nil {
		return nil, false, false, nil, err
	}
	promotionChanged, promotion, promotionErr := s.reconcileCurrentSnapshotPromotion(ctx, qs, sess, current, evidence, reason)
	if promotionErr != nil {
		return current, changed, promotionChanged, promotion, promotionErr
	}
	if current != nil && promotion != nil && promotion.EvaluationRevision == current.EvaluationRevision {
		current.PromotionStatus = promotion.Status
	}
	return current, changed, promotionChanged, promotion, nil
}

func (s *Server) reconcileCurrentSnapshotPromotion(ctx context.Context, qs qualityStorage, sess storage.CrawlSession, result *storage.CrawlQualityResult, evidence *storage.PageRankEvidence, reason string) (bool, *storage.CrawlQualityPromotionEvent, error) {
	if result == nil || sess.ProjectID == nil || *sess.ProjectID == "" {
		return false, nil, nil
	}
	projectID := *sess.ProjectID
	event := storage.CrawlQualityPromotionEvent{
		ProjectID: projectID, SessionID: sess.ID, EvaluationRevision: result.EvaluationRevision,
		PromotionID:              uuid.NewString(),
		PageRankEvidenceRevision: result.PageRankEvidenceRevision, BaselineSessionID: result.BaselineSessionID,
		BaselineEvaluationRevision: result.BaselineEvaluationRevision,
		EvaluatorRevision:          result.EvaluatorRevision, RulesRevision: result.RulesRevision,
		Reason: reason, OccurredAt: time.Now().UTC(),
	}
	if result.IsFullCrawl {
		event.BaselineSessionID = sess.ID
		event.BaselineEvaluationRevision = result.EvaluationRevision
	}
	if !result.Trusted || evidence == nil || evidence.State != storage.PageRankEvidenceFinalized || evidence.PredicateVersion != storage.PageRankEligiblePredicateVersion {
		event.Status = "rejected"
		event.Detail = "quality evaluation is not trusted or PageRank evidence is not finalized"
		return recordQualityPromotion(ctx, qs, event)
	}
	cs, ok := s.currentSnapshotStore()
	if !ok {
		return false, nil, nil
	}
	current, err := qs.GetCrawlQualityResult(ctx, sess.ID)
	if err != nil || current.EvaluationRevision != result.EvaluationRevision {
		event.Status = "conflict"
		event.Detail = "quality current pointer changed before promotion"
		return recordQualityPromotion(ctx, qs, event)
	}
	latestEvidence, err := qs.LatestPageRankEvidence(ctx, sess.ID)
	if err != nil || latestEvidence.State != storage.PageRankEvidenceFinalized || latestEvidence.AttemptID != evidence.AttemptID || latestEvidence.PredicateVersion != storage.PageRankEligiblePredicateVersion {
		event.Status = "conflict"
		event.Detail = "PageRank evidence changed before promotion"
		return recordQualityPromotion(ctx, qs, event)
	}
	if latest, latestErr := qs.LatestQualityPromotionEvent(ctx, projectID, sess.ID); latestErr == nil && latest != nil &&
		latest.Status == "applied" && latest.EvaluationRevision == result.EvaluationRevision && latest.PageRankEvidenceRevision == evidence.AttemptID {
		return false, latest, nil
	}
	event.Status = "started"
	event.OccurredAt = time.Now().UTC()
	if _, _, recordErr := recordQualityPromotion(ctx, qs, event); recordErr != nil {
		return false, nil, fmt.Errorf("recording quality promotion start: %w", recordErr)
	}

	if result.IsFullCrawl {
		_, err = cs.InitializeProjectCurrentSnapshot(ctx, projectID, sess.ID, event)
	} else {
		baselineID := result.BaselineSessionID
		if baselineID == "" {
			if snap, snapErr := cs.GetProjectCurrentSnapshot(ctx, projectID); snapErr == nil {
				baselineID = snap.BaselineSessionID
			}
		}
		settings, settingsErr := s.keyStore.GetProjectDeltaSettings(projectID)
		if settingsErr != nil {
			err = settingsErr
		} else if baselineID == "" {
			err = fmt.Errorf("trusted baseline is unavailable")
		} else {
			opts := storage.PageRankOptions{IncludeFooterLinks: settings.IncludeFooterLinksInPageRank, FooterSelectors: append([]string(nil), settings.FooterSelectorPatterns...)}
			_, err = cs.PromoteDeltaToCurrentSnapshot(ctx, projectID, sess.ID, baselineID, settings.CurrentSnapshotMaxDeltas, settings.CurrentSnapshotBaselineIntervalDays, opts, event)
		}
	}
	if err != nil {
		event.Status = "failed"
		event.OccurredAt = time.Now().UTC()
		event.Detail = sanitizeQualityAuditDetail(err.Error())
		changed, failed, recordErr := recordQualityPromotion(ctx, qs, event)
		if recordErr != nil {
			return false, failed, recordErr
		}
		applog.Warnf("server", "quality promotion failed for session %s: %v", sess.ID, err)
		return changed, failed, nil
	}
	current, qualityErr := qs.GetCrawlQualityResult(ctx, sess.ID)
	latestEvidence, evidenceErr := qs.LatestPageRankEvidence(ctx, sess.ID)
	if qualityErr != nil || evidenceErr != nil || current.EvaluationRevision != result.EvaluationRevision || latestEvidence.AttemptID != evidence.AttemptID || latestEvidence.State != storage.PageRankEvidenceFinalized || latestEvidence.PredicateVersion != storage.PageRankEligiblePredicateVersion {
		event.Status = "conflict"
		event.OccurredAt = time.Now().UTC()
		event.Detail = "quality or PageRank evidence changed during promotion"
		return recordQualityPromotion(ctx, qs, event)
	}
	event.Status = "applied"
	event.OccurredAt = time.Now().UTC()
	event.Detail = "current snapshot promotion applied"
	return recordQualityPromotion(ctx, qs, event)
}

func recordQualityPromotion(ctx context.Context, qs qualityStorage, event storage.CrawlQualityPromotionEvent) (bool, *storage.CrawlQualityPromotionEvent, error) {
	return qs.RecordQualityPromotionEvent(ctx, sanitizeQualityPromotionEvent(event))
}

var qualityPromotionLocks sync.Map

func qualityPromotionLock(projectID string) *sync.Mutex {
	lock, _ := qualityPromotionLocks.LoadOrStore(projectID, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func sanitizeQualityAuditDetail(value string) string {
	value = qualityBearerSecretPattern.ReplaceAllString(value, "$1[REDACTED]")
	value = qualityAssignedSecretPattern.ReplaceAllString(value, "$1=[REDACTED]")
	value = qualityTokenLikePattern.ReplaceAllString(value, "[REDACTED]")
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 500 {
		return value[:500]
	}
	return value
}

var (
	qualityBearerSecretPattern        = regexp.MustCompile(`(?i)\b(bearer\s+)[A-Za-z0-9._~+/=-]{8,}`)
	qualityAssignedSecretPattern      = regexp.MustCompile(`(?i)\b(api[_-]?key|token|secret|password|authorization)\b\s*[:=]\s*[^\s,;]+`)
	qualityActorAssignedSecretPattern = regexp.MustCompile(`(?i)\b(token|secret|password|authorization)\b\s*[:=]\s*[^\s,;]+`)
	qualityTokenLikePattern           = regexp.MustCompile(`\b(?:sk|co|cobs)[-_][A-Za-z0-9_-]{12,}\b`)
)

func sanitizeQualityActionEvent(event storage.CrawlQualityActionEvent) storage.CrawlQualityActionEvent {
	event.Actor = sanitizeQualityAuditActor(event.Actor)
	event.Reason = sanitizeQualityAuditDetail(event.Reason)
	return event
}

func sanitizeQualityAuditActor(value string) string {
	value = qualityBearerSecretPattern.ReplaceAllString(value, "$1[REDACTED]")
	value = qualityActorAssignedSecretPattern.ReplaceAllString(value, "$1=[REDACTED]")
	value = qualityTokenLikePattern.ReplaceAllString(value, "[REDACTED]")
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 500 {
		return value[:500]
	}
	return value
}

func sanitizeQualityPromotionEvent(event storage.CrawlQualityPromotionEvent) storage.CrawlQualityPromotionEvent {
	event.Reason = sanitizeQualityAuditDetail(event.Reason)
	event.Detail = sanitizeQualityAuditDetail(event.Detail)
	return event
}

func isFullCrawlSession(sess storage.CrawlSession) bool {
	label := strings.ToLower(strings.TrimSpace(sess.Label))
	return !strings.Contains(label, "daily delta")
}

func (s *Server) evaluateDeltaPromotionGate(ctx context.Context, qs qualityStorage, sess storage.CrawlSession, projectID string, settings apikeys.ProjectQualitySettings, current *storage.CrawlQualityMetrics, now time.Time) []storage.CrawlQualityFinding {
	var findings []storage.CrawlQualityFinding
	pages := float64(sess.PagesCrawled)
	plan := crawlConfigDeltaPlan(sess.Config)
	if plan != nil {
		findings = append(findings, s.evaluateDeltaPlanGate(ctx, qs, sess.ID, projectID, plan, settings, now)...)
	}
	if settings.DeltaMinCrawledPages > 0 && int(sess.PagesCrawled) < settings.DeltaMinCrawledPages {
		findings = append(findings, qualityFinding(sess.ID, projectID, "error", "delta_too_few_pages", "Daily Delta crawled too few pages to safely promote into current snapshot.", "pages_crawled", pages, 0, float64(settings.DeltaMinCrawledPages), true, now))
	}
	planned := crawlConfigDeltaPlannedPages(sess.Config)
	if planned > 0 && settings.DeltaMinCrawledPercent > 0 {
		percent := pages / float64(planned) * 100
		if percent < settings.DeltaMinCrawledPercent {
			findings = append(findings, qualityFinding(sess.ID, projectID, "error", "delta_crawled_ratio_low", "Daily Delta crawled too small a share of the planned run to safely promote.", "delta_crawled_percent", percent, float64(planned), settings.DeltaMinCrawledPercent, true, now))
		}
	}
	if current != nil && settings.DeltaStatus5xxPercent > 0 {
		status5xx := float64(current.Status5xx)
		minPages := float64(settings.DeltaStatus5xxMinPages)
		if status5xx >= minPages && sess.PagesCrawled > 0 {
			percent := status5xx / float64(sess.PagesCrawled) * 100
			if percent >= settings.DeltaStatus5xxPercent {
				findings = append(findings, qualityFinding(sess.ID, projectID, "error", "delta_status_5xx_high", "Daily Delta returned too many 5xx pages to safely update the current snapshot.", "delta_status_5xx_percent", percent, status5xx, settings.DeltaStatus5xxPercent, true, now))
			}
		}
	}
	if settings.DeltaRequireCanaries {
		canaries, err := s.keyStore.ListProjectCanaries(projectID)
		if err != nil {
			findings = append(findings, qualityFinding(sess.ID, projectID, "error", "delta_canaries_unavailable", "Daily Delta requires canaries but canary configuration could not be loaded.", "canary", 0, 1, 1, true, now))
		} else {
			findings = append(findings, s.evaluateCanaries(ctx, qs, sess.ID, projectID, canaries, now)...)
		}
	}
	return findings
}

func (s *Server) evaluateDeltaPlanGate(ctx context.Context, qs qualityStorage, sessionID, projectID string, plan *config.DeltaPlanConfig, settings apikeys.ProjectQualitySettings, now time.Time) []storage.CrawlQualityFinding {
	var findings []storage.CrawlQualityFinding
	launched := float64(plan.LaunchedCandidates)
	total := float64(plan.TotalCandidates)
	if settings.DeltaMinLaunchedCandidates > 0 && plan.LaunchedCandidates < settings.DeltaMinLaunchedCandidates {
		findings = append(findings, qualityFinding(sessionID, projectID, "error", "delta_launched_candidates_low", "Daily Delta launched too few planned candidates to safely update the current snapshot.", "launched_candidates", launched, total, float64(settings.DeltaMinLaunchedCandidates), true, now))
	}
	if plan.TotalCandidates > 0 && settings.DeltaMinLaunchedPercent > 0 {
		percent := launched / total * 100
		if percent < settings.DeltaMinLaunchedPercent {
			findings = append(findings, qualityFinding(sessionID, projectID, "error", "delta_launched_candidate_ratio_low", "Daily Delta launched too small a share of candidate URLs to safely update the current snapshot.", "launched_candidate_percent", percent, total, settings.DeltaMinLaunchedPercent, true, now))
		}
	}
	if plan.SitemapRefresh != nil {
		findings = append(findings, evaluateDeltaSitemapRefreshGate(sessionID, projectID, plan, settings, now)...)
	} else if sitemapCandidates, ok := plan.SourceCounts["sitemap"]; ok && plan.BaselineSitemapURLCount > 0 {
		current := float64(sitemapCandidates)
		baseline := float64(plan.BaselineSitemapURLCount)
		if settings.DeltaMinSitemapCandidates > 0 && sitemapCandidates < settings.DeltaMinSitemapCandidates {
			findings = append(findings, qualityFinding(sessionID, projectID, "error", "delta_sitemap_candidates_low", "Daily Delta sitemap candidate set is unexpectedly small.", "sitemap_candidates", current, baseline, float64(settings.DeltaMinSitemapCandidates), true, now))
		}
		if settings.DeltaMinSitemapPercent > 0 {
			percent := current / baseline * 100
			if percent < settings.DeltaMinSitemapPercent {
				findings = append(findings, qualityFinding(sessionID, projectID, "error", "delta_sitemap_candidate_ratio_low", "Daily Delta sitemap candidate set dropped sharply compared with the baseline sitemap universe.", "sitemap_candidate_percent", percent, baseline, settings.DeltaMinSitemapPercent, true, now))
			}
		}
	}
	if settings.DeltaCandidateCoveragePercent > 0 && len(plan.LaunchedURLs) > 0 {
		launchedURLs := uniqueStrings(plan.LaunchedURLs)
		if len(launchedURLs) == 0 {
			return findings
		}
		matched, err := qs.CountMatchedPagesForURLs(ctx, sessionID, launchedURLs)
		if err != nil {
			findings = append(findings, qualityFinding(sessionID, projectID, "error", "delta_candidate_coverage_unavailable", "Daily Delta planned candidate coverage could not be verified.", "candidate_coverage_percent", 0, float64(len(launchedURLs)), settings.DeltaCandidateCoveragePercent, true, now))
		} else {
			percent := float64(matched) / float64(len(launchedURLs)) * 100
			if percent < settings.DeltaCandidateCoveragePercent {
				findings = append(findings, qualityFinding(sessionID, projectID, "error", "delta_candidate_coverage_low", "Daily Delta did not produce results for enough launched candidate URLs.", "candidate_coverage_percent", percent, float64(len(launchedURLs)), settings.DeltaCandidateCoveragePercent, true, now))
			}
		}
	}
	return findings
}

func crawlConfigMaxPages(configJSON string) int {
	crawler := crawlConfigCrawler(configJSON)
	switch v := crawler["MaxPages"].(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return 0
	}
}

func crawlConfigDeltaPlannedPages(configJSON string) int {
	if plan := crawlConfigDeltaPlan(configJSON); plan != nil && plan.LaunchedCandidates > 0 {
		return plan.LaunchedCandidates
	}
	crawler := crawlConfigCrawler(configJSON)
	switch v := crawler["DeltaPlannedPages"].(type) {
	case float64:
		if v > 0 {
			return int(v)
		}
	case int:
		if v > 0 {
			return v
		}
	}
	return crawlConfigMaxPages(configJSON)
}

func crawlConfigDeltaPlan(configJSON string) *config.DeltaPlanConfig {
	crawler := crawlConfigCrawler(configJSON)
	if crawler == nil {
		return nil
	}
	raw, ok := crawler["DeltaPlan"]
	if !ok {
		raw, ok = crawler["delta_plan"]
	}
	if !ok || raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var plan config.DeltaPlanConfig
	if err := json.Unmarshal(b, &plan); err != nil {
		return nil
	}
	if plan.SourceCounts == nil {
		plan.SourceCounts = map[string]int{}
	}
	return &plan
}

func addDeltaPlanMetrics(metrics map[string]interface{}, plan *config.DeltaPlanConfig) {
	metrics["candidate_total"] = plan.TotalCandidates
	metrics["launched_candidates"] = plan.LaunchedCandidates
	metrics["deferred_candidates"] = plan.DeferredCandidates
	metrics["launch_limit"] = plan.LaunchLimit
	metrics["baseline_sitemap_urls"] = plan.BaselineSitemapURLCount
	for source, count := range plan.SourceCounts {
		metrics["source_"+source] = count
	}
	if refresh := plan.SitemapRefresh; refresh != nil {
		metrics["sitemap_refresh_mode"] = refresh.Mode
		metrics["sitemap_refresh_fresh_urls"] = refresh.FreshURLCount
		metrics["sitemap_refresh_snapshot_urls"] = refresh.SnapshotURLCount
		metrics["sitemap_refresh_added"] = refresh.AddedCount
		metrics["sitemap_refresh_removed"] = refresh.RemovedCount
		metrics["sitemap_refresh_invalid"] = refresh.InvalidEntryCount
	}
}

func evaluateDeltaSitemapRefreshGate(sessionID, projectID string, plan *config.DeltaPlanConfig, settings apikeys.ProjectQualitySettings, now time.Time) []storage.CrawlQualityFinding {
	refresh := plan.SitemapRefresh
	if refresh == nil {
		return nil
	}
	mode := strings.TrimSpace(refresh.Mode)
	switch mode {
	case deltaSitemapRefreshFresh:
		current := float64(refresh.FreshURLCount)
		baseline := float64(refresh.SnapshotURLCount)
		if settings.DeltaMinSitemapCandidates > 0 && refresh.FreshURLCount < settings.DeltaMinSitemapCandidates {
			return []storage.CrawlQualityFinding{qualityFinding(sessionID, projectID, "error", "delta_sitemap_candidates_low", "Daily Delta fresh sitemap candidate set is unexpectedly small.", "sitemap_candidates", current, baseline, float64(settings.DeltaMinSitemapCandidates), true, now)}
		}
		if baseline > 0 && settings.DeltaMinSitemapPercent > 0 {
			percent := current / baseline * 100
			if percent < settings.DeltaMinSitemapPercent {
				return []storage.CrawlQualityFinding{qualityFinding(sessionID, projectID, "error", "delta_sitemap_candidate_ratio_low", "Daily Delta fresh sitemap candidate set dropped sharply compared with the published sitemap universe.", "sitemap_candidate_percent", percent, baseline, settings.DeltaMinSitemapPercent, true, now)}
			}
		}
		return nil
	case deltaSitemapRefreshSkipped:
		return []storage.CrawlQualityFinding{qualityFinding(sessionID, projectID, "warning", "delta_sitemap_refresh_skipped", "Daily Delta skipped the sitemap source because the sitemap refresh was incomplete or unavailable.", "sitemap_refresh", 0, float64(refresh.SnapshotURLCount), 0, false, now)}
	case deltaSitemapRefreshSnapshotFallback:
		return []storage.CrawlQualityFinding{qualityFinding(sessionID, projectID, "warning", "delta_sitemap_snapshot_fallback", "Daily Delta used the explicit snapshot sitemap fallback; this source is not fresh.", "sitemap_refresh", float64(refresh.SnapshotURLCount), float64(refresh.SnapshotURLCount), 0, false, now)}
	default:
		return []storage.CrawlQualityFinding{qualityFinding(sessionID, projectID, "error", "delta_sitemap_refresh_invalid", "Daily Delta sitemap refresh metadata is malformed and cannot be trusted.", "sitemap_refresh", 0, 0, 0, true, now)}
	}
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func crawlConfigCrawler(configJSON string) map[string]any {
	var raw map[string]any
	if err := json.Unmarshal([]byte(configJSON), &raw); err != nil {
		return nil
	}
	crawler, _ := raw["Crawler"].(map[string]any)
	return crawler
}

func compareQualityMetrics(sessionID, projectID string, current, baseline *storage.CrawlQualityMetrics, settings apikeys.ProjectQualitySettings, now time.Time) []storage.CrawlQualityFinding {
	var findings []storage.CrawlQualityFinding
	findings = append(findings, dropFinding(sessionID, projectID, "coverage_drop", "HTML page coverage dropped sharply.", "html_pages", float64(current.HTMLPages), float64(baseline.HTMLPages), settings.CoverageDropPercent, float64(settings.CoverageMinPagesDelta), true, now)...)
	findings = append(findings, growthFinding(sessionID, projectID, "coverage_growth", "HTML page coverage grew sharply.", "html_pages", float64(current.HTMLPages), float64(baseline.HTMLPages), settings.CoverageGrowthPercent, float64(settings.CoverageMinPagesDelta), false, now)...)
	findings = append(findings, dropFinding(sessionID, projectID, "internal_links_drop", "Internal link count dropped sharply.", "internal_links", float64(current.InternalLinks), float64(baseline.InternalLinks), settings.InternalLinksDropPercent, float64(settings.InternalLinksMinDelta), true, now)...)
	findings = append(findings, growthFinding(sessionID, projectID, "status_404_growth", "404 pages increased sharply.", "status_404", float64(current.Status404), float64(baseline.Status404), settings.Status404Percent, float64(settings.Status404MinDelta), false, now)...)
	findings = append(findings, growthFinding(sessionID, projectID, "status_5xx_growth", "5xx pages increased sharply.", "status_5xx", float64(current.Status5xx), float64(baseline.Status5xx), settings.Status5xxPercent, float64(settings.Status5xxMinDelta), true, now)...)
	findings = append(findings, growthFinding(sessionID, projectID, "noindex_growth", "Noindex pages increased sharply.", "noindex", float64(current.Noindex), float64(baseline.Noindex), settings.NoindexPercent, float64(settings.NoindexMinDelta), false, now)...)
	findings = append(findings, growthFinding(sessionID, projectID, "redirect_growth", "Redirect pages increased sharply.", "redirects", float64(current.Redirects), float64(baseline.Redirects), settings.RedirectPercent, float64(settings.RedirectMinDelta), false, now)...)
	findings = append(findings, growthFinding(sessionID, projectID, "canonical_mismatch_growth", "Canonical mismatch pages increased sharply.", "canonical_mismatch", float64(current.CanonicalMismatch), float64(baseline.CanonicalMismatch), settings.CanonicalMismatchPercent, float64(settings.CanonicalMismatchMinDelta), false, now)...)
	if int(current.PageRankZeroTopPages) > settings.PageRankZeroTopPagesMax {
		findings = append(findings, qualityFinding(sessionID, projectID, "error", "pagerank_zero_top_pages", "Too many top PageRank pages have zero PageRank.", "pagerank_zero_top_pages", float64(current.PageRankZeroTopPages), 0, float64(settings.PageRankZeroTopPagesMax), true, now))
	}
	return findings
}

func dropFinding(sessionID, projectID, typ, msg, metric string, current, baseline, percent, minDelta float64, blocking bool, now time.Time) []storage.CrawlQualityFinding {
	if baseline <= 0 {
		return nil
	}
	delta := baseline - current
	if delta < minDelta {
		return nil
	}
	dropPercent := delta / baseline * 100
	if dropPercent >= percent {
		return []storage.CrawlQualityFinding{qualityFinding(sessionID, projectID, "error", typ, msg, metric, current, baseline, percent, blocking, now)}
	}
	return nil
}

func growthFinding(sessionID, projectID, typ, msg, metric string, current, baseline, percent, minDelta float64, blocking bool, now time.Time) []storage.CrawlQualityFinding {
	delta := current - baseline
	if delta < minDelta {
		return nil
	}
	base := math.Max(baseline, 1)
	growthPercent := delta / base * 100
	if growthPercent >= percent {
		severity := "warning"
		if blocking {
			severity = "error"
		}
		return []storage.CrawlQualityFinding{qualityFinding(sessionID, projectID, severity, typ, msg, metric, current, baseline, percent, blocking, now)}
	}
	return nil
}

func (s *Server) evaluatePageRankOverlap(ctx context.Context, qs qualityStorage, sessionID, projectID, baselineID string, settings apikeys.ProjectQualitySettings, now time.Time) []storage.CrawlQualityFinding {
	current, err := qs.TopPageRankURLs(ctx, sessionID, settings.PageRankTopN)
	if err != nil || len(current) == 0 {
		return nil
	}
	baseline, err := qs.TopPageRankURLs(ctx, baselineID, settings.PageRankTopN)
	if err != nil || len(baseline) == 0 {
		return nil
	}
	set := map[string]struct{}{}
	for _, u := range baseline {
		set[u] = struct{}{}
	}
	overlap := 0
	for _, u := range current {
		if _, ok := set[u]; ok {
			overlap++
		}
	}
	overlapPercent := float64(overlap) / float64(min(len(current), len(baseline))) * 100
	if overlapPercent < settings.PageRankTopOverlapMinPercent {
		return []storage.CrawlQualityFinding{qualityFinding(sessionID, projectID, "warning", "pagerank_top_overlap_low", "Top PageRank pages changed sharply compared with the trusted baseline.", "pagerank_top_overlap_percent", overlapPercent, 100, settings.PageRankTopOverlapMinPercent, false, now)}
	}
	return nil
}

func (s *Server) evaluateCanaries(ctx context.Context, qs qualityStorage, sessionID, projectID string, canaries []apikeys.ProjectCanary, now time.Time) []storage.CrawlQualityFinding {
	var findings []storage.CrawlQualityFinding
	for _, canary := range canaries {
		if !canary.Active {
			continue
		}
		check, err := qs.CanaryPageCheck(ctx, sessionID, canary.URL)
		if err != nil || check == nil || !check.Found {
			findings = append(findings, qualityFinding(sessionID, projectID, "error", "canary_missing", "Canary page is missing from this crawl: "+canary.URL, "canary", 0, 1, 1, true, now))
			continue
		}
		if canary.ExpectedStatus > 0 && int(check.StatusCode) != canary.ExpectedStatus {
			findings = append(findings, qualityFinding(sessionID, projectID, "error", "canary_status_mismatch", "Canary page returned unexpected status: "+canary.URL, "status_code", float64(check.StatusCode), float64(canary.ExpectedStatus), float64(canary.ExpectedStatus), true, now))
		}
		if canary.ExpectedFinalURL != "" && check.FinalURL != canary.ExpectedFinalURL {
			findings = append(findings, qualityFinding(sessionID, projectID, "error", "canary_final_url_mismatch", "Canary final URL changed: "+canary.URL, "final_url", 0, 0, 0, true, now))
		}
		if canary.ExpectedCanonical != "" && check.Canonical != canary.ExpectedCanonical {
			findings = append(findings, qualityFinding(sessionID, projectID, "error", "canary_canonical_mismatch", "Canary canonical changed: "+canary.URL, "canonical", 0, 0, 0, true, now))
		}
		if canary.TitleContains != "" && !strings.Contains(strings.ToLower(check.Title), strings.ToLower(canary.TitleContains)) {
			findings = append(findings, qualityFinding(sessionID, projectID, "warning", "canary_title_changed", "Canary title does not contain expected text: "+canary.URL, "title", 0, 0, 0, false, now))
		}
		if int(check.InternalLinksOut) < canary.MinInternalLinks {
			findings = append(findings, qualityFinding(sessionID, projectID, "error", "canary_internal_links_low", "Canary has too few outgoing internal links: "+canary.URL, "internal_links_out", float64(check.InternalLinksOut), 0, float64(canary.MinInternalLinks), true, now))
		}
		if canary.ExpectIndexable && !check.IsIndexable {
			findings = append(findings, qualityFinding(sessionID, projectID, "error", "canary_not_indexable", "Canary is not indexable: "+canary.URL, "is_indexable", 0, 1, 1, true, now))
		}
	}
	return findings
}

func qualityFinding(sessionID, projectID, severity, typ, message, metric string, current, baseline, threshold float64, blocking bool, now time.Time) storage.CrawlQualityFinding {
	return storage.CrawlQualityFinding{
		SessionID:      sessionID,
		ProjectID:      projectID,
		Severity:       severity,
		FindingType:    typ,
		Message:        message,
		Metric:         metric,
		CurrentValue:   current,
		BaselineValue:  baseline,
		ThresholdValue: threshold,
		Blocking:       blocking,
		CreatedAt:      now,
	}
}

func crawlConfigSignature(configJSON string) string {
	var raw map[string]any
	if err := json.Unmarshal([]byte(configJSON), &raw); err != nil {
		return ""
	}
	crawler, _ := raw["Crawler"].(map[string]any)
	keys := []string{"CrawlScope", "MaxPages", "MaxDepth", "StoreHTML", "UserAgent", "CrawlSitemapOnly", "FetchSitemaps", "IgnoreRobots"}
	var b strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&b, "%s=%v;", key, crawler[key])
	}
	return b.String()
}

func isNotFoundErr(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || strings.Contains(strings.ToLower(err.Error()), "no rows")
}
