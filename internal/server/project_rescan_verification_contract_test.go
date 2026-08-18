package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SEObserver/crawlobserver/internal/apikeys"
	"github.com/SEObserver/crawlobserver/internal/config"
	"github.com/SEObserver/crawlobserver/internal/storage"
)

type projectRescanVerificationFixture struct {
	ContractVersion string `json:"contract_version"`
	Request         struct {
		ProjectID string `json:"project_id"`
		URL       string `json:"url"`
	} `json:"request"`
	MutationAuth projectRescanVerificationAuth `json:"mutation_auth"`
	ReadAuth     projectRescanVerificationAuth `json:"read_auth"`
	Receipt      struct {
		ProjectID      string   `json:"project_id"`
		SessionID      string   `json:"session_id"`
		RequestID      string   `json:"request_id"`
		IdempotencyKey string   `json:"idempotency_key"`
		Status         string   `json:"status"`
		AcceptedCount  int      `json:"accepted_url_count"`
		AcceptedURLs   []string `json:"accepted_urls"`
		RequestDigest  string   `json:"request_digest"`
		StartedAt      string   `json:"started_at"`
		CompletedAt    string   `json:"completed_at"`
	} `json:"receipt"`
	Scenarios []projectRescanVerificationScenario `json:"scenarios"`
}

type projectRescanVerificationAuth struct {
	Header     string `json:"header"`
	KeyType    string `json:"key_type"`
	Capability string `json:"capability"`
}

type projectRescanVerificationRead struct {
	Method     string          `json:"method"`
	Path       string          `json:"path"`
	HTTPStatus int             `json:"http_status"`
	Body       json.RawMessage `json:"body"`
}

type projectRescanVerificationScenario struct {
	Name                string                         `json:"name"`
	CurrentSnapshotRead *projectRescanVerificationRead `json:"current_snapshot_read"`
	ReturnedSessionRead *projectRescanVerificationRead `json:"returned_session_read"`
	PageDetailRead      *projectRescanVerificationRead `json:"page_detail_read"`
	Verified            bool                           `json:"verified"`
	Reason              string                         `json:"reason"`
}

func TestProjectRescanVerificationContractFixture(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "fixtures", "project-rescan-verification-v2.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture projectRescanVerificationFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.ContractVersion != "project-rescan-verification/v2" {
		t.Fatalf("contract version = %q", fixture.ContractVersion)
	}
	if fixture.MutationAuth.Header != "X-API-Key" || fixture.MutationAuth.KeyType != "project" || fixture.MutationAuth.Capability != apikeys.CapabilityTargetedRescan {
		t.Fatalf("unexpected mutation auth contract: %#v", fixture.MutationAuth)
	}
	if fixture.ReadAuth.Header != "X-API-Key" || fixture.ReadAuth.KeyType != "project" || fixture.ReadAuth.Capability != "" {
		t.Fatalf("unexpected evidence auth contract: %#v", fixture.ReadAuth)
	}

	reasons := make(map[string]bool)
	for _, scenario := range fixture.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			verified, reason := evaluateProjectRescanVerificationFixture(fixture, scenario)
			if verified != scenario.Verified || reason != scenario.Reason {
				t.Fatalf("got verified=%v reason=%q, want verified=%v reason=%q", verified, reason, scenario.Verified, scenario.Reason)
			}
			reasons[reason] = true
		})
	}
	for _, required := range []string{
		"verified", "receipt_is_not_verification", "project_mismatch", "receipt_session_not_current",
		"untrusted_current_snapshot", "malformed_current_snapshot", "returned_session_unavailable",
		"session_mismatch", "session_not_terminal", "malformed_returned_session", "stale_page",
		"page_mismatch", "malformed_page_detail",
	} {
		if !reasons[required] {
			t.Fatalf("fixture is missing required outcome %q", required)
		}
	}
}

func evaluateProjectRescanVerificationFixture(fixture projectRescanVerificationFixture, scenario projectRescanVerificationScenario) (bool, string) {
	startedAt, startErr := time.Parse(time.RFC3339, fixture.Receipt.StartedAt)
	completedAt, completedErr := time.Parse(time.RFC3339, fixture.Receipt.CompletedAt)
	if startErr != nil || completedErr != nil || completedAt.Before(startedAt) || fixture.Receipt.Status != apikeys.ProjectRescanStatusCompleted ||
		fixture.Receipt.ProjectID != fixture.Request.ProjectID || fixture.Receipt.SessionID == "" || fixture.Receipt.RequestID == "" ||
		fixture.Receipt.IdempotencyKey == "" || fixture.Receipt.RequestDigest == "" || fixture.Receipt.AcceptedCount != len(fixture.Receipt.AcceptedURLs) ||
		!containsExactString(fixture.Receipt.AcceptedURLs, fixture.Request.URL) {
		return false, "malformed_receipt"
	}
	if scenario.CurrentSnapshotRead == nil {
		return false, "receipt_is_not_verification"
	}
	if scenario.CurrentSnapshotRead.Method != http.MethodGet || scenario.CurrentSnapshotRead.Path != "/api/projects/"+fixture.Request.ProjectID+"/current-snapshot" {
		return false, "malformed_current_snapshot"
	}
	if scenario.CurrentSnapshotRead.HTTPStatus != http.StatusOK {
		return false, "untrusted_current_snapshot"
	}
	var snapshot struct {
		ProjectID              string `json:"project_id"`
		CurrentSessionID       string `json:"current_session_id"`
		QualityPromotionStatus string `json:"quality_promotion_status"`
	}
	if err := json.Unmarshal(scenario.CurrentSnapshotRead.Body, &snapshot); err != nil || snapshot.CurrentSessionID == "" || snapshot.QualityPromotionStatus == "" {
		return false, "malformed_current_snapshot"
	}
	if snapshot.ProjectID != fixture.Request.ProjectID {
		return false, "project_mismatch"
	}
	if snapshot.QualityPromotionStatus != "applied" {
		return false, "untrusted_current_snapshot"
	}
	if snapshot.CurrentSessionID != fixture.Receipt.SessionID {
		return false, "receipt_session_not_current"
	}

	if scenario.ReturnedSessionRead == nil || scenario.ReturnedSessionRead.HTTPStatus != http.StatusOK {
		return false, "returned_session_unavailable"
	}
	if scenario.ReturnedSessionRead.Method != http.MethodGet || scenario.ReturnedSessionRead.Path != "/api/sessions/"+fixture.Receipt.SessionID {
		return false, "session_mismatch"
	}
	var session struct {
		ID        string `json:"ID"`
		ProjectID string `json:"ProjectID"`
		Status    string `json:"Status"`
		IsRunning *bool  `json:"is_running"`
		IsQueued  *bool  `json:"is_queued"`
	}
	if err := json.Unmarshal(scenario.ReturnedSessionRead.Body, &session); err != nil || session.ID == "" || session.ProjectID == "" || session.Status == "" || session.IsRunning == nil || session.IsQueued == nil {
		return false, "malformed_returned_session"
	}
	if session.ID != fixture.Receipt.SessionID || session.ProjectID != fixture.Request.ProjectID {
		return false, "session_mismatch"
	}
	if (session.Status != "completed" && session.Status != "completed_with_errors") || *session.IsRunning || *session.IsQueued {
		return false, "session_not_terminal"
	}

	if scenario.PageDetailRead == nil || scenario.PageDetailRead.HTTPStatus != http.StatusOK {
		return false, "malformed_page_detail"
	}
	expectedPagePath := "/api/sessions/" + fixture.Receipt.SessionID + "/page-detail?url=" + url.QueryEscape(fixture.Request.URL)
	if scenario.PageDetailRead.Method != http.MethodGet || scenario.PageDetailRead.Path != expectedPagePath {
		return false, "page_mismatch"
	}
	var detail struct {
		Page *struct {
			CrawlSessionID string `json:"CrawlSessionID"`
			URL            string `json:"URL"`
			CrawledAt      string `json:"CrawledAt"`
		} `json:"page"`
	}
	if err := json.Unmarshal(scenario.PageDetailRead.Body, &detail); err != nil || detail.Page == nil || detail.Page.CrawledAt == "" {
		return false, "malformed_page_detail"
	}
	if detail.Page.CrawlSessionID != fixture.Receipt.SessionID {
		return false, "session_mismatch"
	}
	if detail.Page.URL != fixture.Request.URL {
		return false, "page_mismatch"
	}
	crawledAt, err := time.Parse(time.RFC3339, detail.Page.CrawledAt)
	if err != nil {
		return false, "malformed_page_detail"
	}
	if crawledAt.Before(startedAt) {
		return false, "stale_page"
	}
	return true, "verified"
}

func TestProjectRescanVerificationFlowUsesReceiptSessionAcrossRealReadHandlers(t *testing.T) {
	keyStore, err := apikeys.NewStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer keyStore.Close()
	project, err := keyStore.CreateProject("rescan-verification-flow")
	if err != nil {
		t.Fatal(err)
	}
	mutationKey, err := keyStore.CreateAPIKeyWithCapability("rescan", "project", &project.ID, apikeys.CapabilityTargetedRescan)
	if err != nil {
		t.Fatal(err)
	}
	evidenceKey, err := keyStore.CreateAPIKey("evidence", "project", &project.ID)
	if err != nil {
		t.Fatal(err)
	}

	sessionID := "session-current-snapshot"
	pageURL := "https://example.com/published-page/"
	page := &storage.PageRow{CrawlSessionID: sessionID, URL: pageURL}
	mockStorage := &mockStore{
		getSessionByID: map[string]*storage.CrawlSession{
			sessionID: {ID: sessionID, ProjectID: &project.ID, Status: "completed", SeedURLs: []string{"https://example.com/"}},
		},
		page: page,
	}
	evidence := &storage.PageRankEvidence{
		SessionID: sessionID, AttemptID: "pagerank-evidence-1", State: storage.PageRankEvidenceFinalized,
		PredicateVersion: storage.PageRankEligiblePredicateVersion,
	}
	quality := &storage.CrawlQualityResult{
		SessionID: sessionID, ProjectID: project.ID, EvaluationRevision: "quality-evaluation-1",
		EvaluatorRevision: qualityEvaluatorRevision, PageRankEvidenceRevision: evidence.AttemptID,
		PageRankPredicateVersion: storage.PageRankEligiblePredicateVersion, IsFullCrawl: true, Trusted: true, Status: "trusted",
	}
	snapshot := &storage.ProjectCurrentSnapshot{
		ProjectID: project.ID, CurrentSessionID: sessionID, BaselineSessionID: sessionID,
		QualityEvaluationRevision: quality.EvaluationRevision, PageRankEvidenceRevision: evidence.AttemptID,
		QualityPromotionStatus: "applied",
	}
	store := qualitySnapshotServerStore{
		mockStore:               mockStorage,
		qualityGateMock:         qualityGateMock{current: quality, evidence: evidence},
		currentSnapshotGateMock: currentSnapshotGateMock{snapshot: snapshot, quality: quality, evidence: evidence},
	}
	baseManager := newMockManager()
	baseManager.rescanCount = 1
	manager := &hookedProjectRescanManager{mockManager: baseManager, afterRescan: func() { page.CrawledAt = time.Now().UTC() }}
	cfg := &config.Config{Server: config.ServerConfig{Username: "admin", Password: "secret"}}
	srv := NewWithDeps(cfg, store, keyStore, manager)
	quality.RulesRevision, err = srv.currentQualityRulesRevision(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.QualityRulesRevision = quality.RulesRevision
	handler, err := srv.Handler()
	if err != nil {
		t.Fatal(err)
	}

	post := httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/sessions/"+sessionID+"/rescan-pages", strings.NewReader(`{"urls":["`+pageURL+`"]}`))
	post.Header.Set("X-API-Key", mutationKey.FullKey)
	post.Header.Set("Idempotency-Key", "published-change-1")
	post.Header.Set("Content-Type", "application/json")
	postRec := httptest.NewRecorder()
	handler.ServeHTTP(postRec, post)
	if postRec.Code != http.StatusOK {
		t.Fatalf("rescan receipt = %d: %s", postRec.Code, postRec.Body.String())
	}
	var receipt projectRescanResponse
	decodeJSON(t, postRec, &receipt)
	if receipt.Status != apikeys.ProjectRescanStatusCompleted || receipt.SessionID != sessionID || receipt.StartedAt == nil {
		t.Fatalf("unexpected receipt: %#v", receipt)
	}

	snapshotReq := httptest.NewRequest(http.MethodGet, "/api/projects/"+project.ID+"/current-snapshot", nil)
	snapshotReq.Header.Set("X-API-Key", evidenceKey.FullKey)
	snapshotRec := httptest.NewRecorder()
	handler.ServeHTTP(snapshotRec, snapshotReq)
	if snapshotRec.Code != http.StatusOK {
		t.Fatalf("current snapshot = %d: %s", snapshotRec.Code, snapshotRec.Body.String())
	}
	var current storage.ProjectCurrentSnapshot
	decodeJSON(t, snapshotRec, &current)
	if current.ProjectID != receipt.ProjectID || current.CurrentSessionID != receipt.SessionID || current.QualityPromotionStatus != "applied" {
		t.Fatalf("receipt is not the trusted current session: receipt=%#v snapshot=%#v", receipt, current)
	}

	sessionReq := httptest.NewRequest(http.MethodGet, "/api/sessions/"+receipt.SessionID, nil)
	sessionReq.Header.Set("X-API-Key", evidenceKey.FullKey)
	sessionRec := httptest.NewRecorder()
	handler.ServeHTTP(sessionRec, sessionReq)
	if sessionRec.Code != http.StatusOK {
		t.Fatalf("returned session = %d: %s", sessionRec.Code, sessionRec.Body.String())
	}
	var returnedSession struct {
		ID        string `json:"ID"`
		ProjectID string `json:"ProjectID"`
		Status    string `json:"Status"`
		IsRunning bool   `json:"is_running"`
		IsQueued  bool   `json:"is_queued"`
	}
	decodeJSON(t, sessionRec, &returnedSession)
	if returnedSession.ID != receipt.SessionID || returnedSession.ProjectID != receipt.ProjectID || returnedSession.Status != "completed" || returnedSession.IsRunning || returnedSession.IsQueued {
		t.Fatalf("unexpected returned session: %#v", returnedSession)
	}

	pageReq := httptest.NewRequest(http.MethodGet, "/api/sessions/"+returnedSession.ID+"/page-detail?url="+url.QueryEscape(pageURL), nil)
	pageReq.Header.Set("X-API-Key", evidenceKey.FullKey)
	pageRec := httptest.NewRecorder()
	handler.ServeHTTP(pageRec, pageReq)
	if pageRec.Code != http.StatusOK {
		t.Fatalf("page detail = %d: %s", pageRec.Code, pageRec.Body.String())
	}
	var detail struct {
		Page *storage.PageRow `json:"page"`
	}
	decodeJSON(t, pageRec, &detail)
	if detail.Page == nil || detail.Page.CrawlSessionID != receipt.SessionID || detail.Page.URL != pageURL || detail.Page.CrawledAt.Before(*receipt.StartedAt) {
		t.Fatalf("page detail does not prove the requested rescan: %#v", detail.Page)
	}
}

func containsExactString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
