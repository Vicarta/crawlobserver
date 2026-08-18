package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SEObserver/crawlobserver/internal/apikeys"
	"github.com/SEObserver/crawlobserver/internal/crawler"
	"github.com/SEObserver/crawlobserver/internal/storage"
)

type projectRescanFixture struct {
	server    *Server
	handler   http.Handler
	keyStore  *apikeys.Store
	projectID string
	sessionID string
	rescanKey string
	store     *mockStore
	manager   *mockManager
}

type hookedProjectRescanManager struct {
	*mockManager
	afterRescan func()
}

func (m *hookedProjectRescanManager) RescanPages(sessionID string, urls []string) (int, error) {
	count, err := m.mockManager.RescanPages(sessionID, urls)
	if m.afterRescan != nil {
		m.afterRescan()
	}
	return count, err
}

func newProjectRescanFixture(t *testing.T) projectRescanFixture {
	t.Helper()
	srv, handler, keyStore := newTestServer(t)
	project, err := keyStore.CreateProject("project-bound-rescan")
	if err != nil {
		t.Fatal(err)
	}
	rescanKey, err := keyStore.CreateAPIKeyWithCapability("dashboard-rescan", "project", &project.ID, apikeys.CapabilityTargetedRescan)
	if err != nil {
		t.Fatal(err)
	}
	sessionID := "project-rescan-session"
	ms := srv.store.(*mockStore)
	ms.getSessionByID = map[string]*storage.CrawlSession{
		sessionID: {
			ID:        sessionID,
			ProjectID: &project.ID,
			Status:    "completed",
			SeedURLs:  []string{"https://example.com/"},
		},
	}
	mm := srv.manager.(*mockManager)
	mm.rescanCount = 2
	return projectRescanFixture{
		server: srv, handler: handler, keyStore: keyStore, projectID: project.ID,
		sessionID: sessionID, rescanKey: rescanKey.FullKey, store: ms, manager: mm,
	}
}

func (f projectRescanFixture) path() string {
	return "/api/projects/" + f.projectID + "/sessions/" + f.sessionID + "/rescan-pages"
}

func (f projectRescanFixture) request(t *testing.T, key, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, f.path(), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	return req
}

func (f projectRescanFixture) authorizedRequest(t *testing.T, key, body string) *http.Request {
	t.Helper()
	req := f.request(t, key, body)
	req.Header.Set("X-API-Key", f.rescanKey)
	return req
}

func TestProjectRescanRequiresAuthentication(t *testing.T) {
	f := newProjectRescanFixture(t)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, f.request(t, "unauthorized-request", `{"urls":["https://example.com/a"]}`))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", rec.Code, rec.Body.String())
	}
	if len(f.manager.rescanCalls) != 0 {
		t.Fatal("unauthorized request reached rescan manager")
	}
}

func TestProjectRescanBadRequestClassifications(t *testing.T) {
	tests := []struct {
		name string
		key  string
		body string
		code string
	}{
		{name: "missing idempotency key", body: `{"urls":["https://example.com/a"]}`, code: "invalid_idempotency_key"},
		{name: "malformed JSON", key: "bad-json", body: `{"urls":`, code: "invalid_request"},
		{name: "unknown field", key: "unknown-field", body: `{"urls":["https://example.com/a"],"extra":true}`, code: "invalid_request"},
		{name: "trailing JSON", key: "trailing-json", body: `{"urls":["https://example.com/a"]}{}`, code: "invalid_request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newProjectRescanFixture(t)
			req := f.authorizedRequest(t, tt.key, tt.body)
			rec := httptest.NewRecorder()
			f.handler.ServeHTTP(rec, req)
			var response projectRescanResponse
			decodeJSON(t, rec, &response)
			if rec.Code != http.StatusBadRequest || response.Status != "rejected" || response.ErrorCode != tt.code {
				t.Fatalf("response = %d %#v", rec.Code, response)
			}
			if len(f.manager.rescanCalls) != 0 {
				t.Fatal("bad request reached rescan manager")
			}
		})
	}
}

func TestProjectRescanRejectsInvalidScope(t *testing.T) {
	f := newProjectRescanFixture(t)
	req := f.authorizedRequest(t, "invalid-scope", `{"urls":["https://example.com/a"]}`)
	rec := httptest.NewRecorder()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("projectId", "")
		r.SetPathValue("sessionId", f.sessionID)
		f.server.handleProjectRescanPages(w, r)
	})
	apikeys.Authenticate(f.keyStore, "", "")(inner).ServeHTTP(rec, req)
	var response projectRescanResponse
	decodeJSON(t, rec, &response)
	if rec.Code != http.StatusBadRequest || response.Status != "rejected" || response.ErrorCode != "invalid_scope" {
		t.Fatalf("response = %d %#v", rec.Code, response)
	}
}

func TestProjectRescanNotFoundClassifications(t *testing.T) {
	t.Run("session", func(t *testing.T) {
		f := newProjectRescanFixture(t)
		path := "/api/projects/" + f.projectID + "/sessions/missing-session/rescan-pages"
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"urls":["https://example.com/a"]}`))
		req.Header.Set("X-API-Key", f.rescanKey)
		req.Header.Set("Idempotency-Key", "missing-session")
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, req)
		var response projectRescanResponse
		decodeJSON(t, rec, &response)
		if rec.Code != http.StatusNotFound || response.Status != "rejected" || response.ErrorCode != "session_not_found" {
			t.Fatalf("response = %d %#v", rec.Code, response)
		}
	})

	t.Run("project", func(t *testing.T) {
		f := newProjectRescanFixture(t)
		req := f.authorizedRequest(t, "missing-project", `{"urls":["https://example.com/a"]}`)
		rec := httptest.NewRecorder()
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := f.keyStore.DeleteProject(f.projectID); err != nil {
				t.Fatal(err)
			}
			r.SetPathValue("projectId", f.projectID)
			r.SetPathValue("sessionId", f.sessionID)
			f.server.handleProjectRescanPages(w, r)
		})
		apikeys.Authenticate(f.keyStore, "", "")(inner).ServeHTTP(rec, req)
		var response projectRescanResponse
		decodeJSON(t, rec, &response)
		if rec.Code != http.StatusNotFound || response.Status != "rejected" || response.ErrorCode != "project_not_found" {
			t.Fatalf("response = %d %#v", rec.Code, response)
		}
	})
}

func TestProjectRescanRejectsProjectReadOnlyKey(t *testing.T) {
	f := newProjectRescanFixture(t)
	key, err := f.keyStore.CreateAPIKey("evidence-only", "project", &f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	req := f.request(t, "read-only-request", `{"urls":["https://example.com/a"]}`)
	req.Header.Set("X-API-Key", key.FullKey)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)

	var response projectRescanResponse
	decodeJSON(t, rec, &response)
	if rec.Code != http.StatusForbidden || response.ErrorCode != "project_rescan_capability_required" {
		t.Fatalf("response = %d %#v", rec.Code, response)
	}
	if len(f.manager.rescanCalls) != 0 {
		t.Fatal("read-only request reached rescan manager")
	}
}

func TestCreateTargetedRescanKeyThroughAdminAPI(t *testing.T) {
	f := newProjectRescanFixture(t)
	body, err := json.Marshal(map[string]interface{}{
		"name": "dashboard-project-rescan", "type": "project", "project_id": f.projectID,
		"capability": apikeys.CapabilityTargetedRescan,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := authRequest(httptest.NewRequest(http.MethodPost, "/api/api-keys", strings.NewReader(string(body))))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var created apikeys.APIKeyCreateResult
	decodeJSON(t, rec, &created)
	if created.Type != "project" || created.ProjectID == nil || *created.ProjectID != f.projectID ||
		created.Capability != apikeys.CapabilityTargetedRescan || created.FullKey == "" {
		t.Fatalf("unexpected API key contract: %#v", created)
	}
}

func TestProjectRescanRejectsGeneralKey(t *testing.T) {
	f := newProjectRescanFixture(t)
	key, err := f.keyStore.CreateAPIKey("general-admin", "general", nil)
	if err != nil {
		t.Fatal(err)
	}
	req := f.request(t, "general-key-request", `{"urls":["https://example.com/a"]}`)
	req.Header.Set("X-API-Key", key.FullKey)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	var response projectRescanResponse
	decodeJSON(t, rec, &response)
	if rec.Code != http.StatusForbidden || response.ErrorCode != "project_rescan_capability_required" {
		t.Fatalf("response = %d %#v", rec.Code, response)
	}
	if len(f.manager.rescanCalls) != 0 {
		t.Fatal("general key reached project-bound rescan manager")
	}
}

func TestLegacySessionRescanKeepsGeneralAccessOnly(t *testing.T) {
	f := newProjectRescanFixture(t)
	general, err := f.keyStore.CreateAPIKey("legacy-general", "general", nil)
	if err != nil {
		t.Fatal(err)
	}
	request := func(apiKey string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+f.sessionID+"/rescan-pages", strings.NewReader(`{"urls":["https://example.com/a"]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-API-Key", apiKey)
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := request(general.FullKey); rec.Code != http.StatusOK {
		t.Fatalf("general legacy response = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := request(f.rescanKey); rec.Code != http.StatusForbidden {
		t.Fatalf("targeted capability legacy response = %d: %s", rec.Code, rec.Body.String())
	}
	if len(f.manager.rescanCalls) != 1 {
		t.Fatalf("legacy rescan calls = %d, want 1", len(f.manager.rescanCalls))
	}
}

func TestProjectRescanDedicatedKeySuccessAndIdempotentReplay(t *testing.T) {
	f := newProjectRescanFixture(t)
	request := func(body string) *httptest.ResponseRecorder {
		req := f.authorizedRequest(t, "publish-event-123", body)
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, req)
		return rec
	}

	first := request(`{"urls":["https://example.com/a#published","https://example.com/b"]}`)
	f.store.getSessionByID[f.sessionID].Status = "running"
	second := request(`{"urls":["https://example.com/b","https://example.com/a"]}`)
	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("statuses = %d/%d: %s / %s", first.Code, second.Code, first.Body.String(), second.Body.String())
	}
	if len(f.manager.rescanCalls) != 1 {
		t.Fatalf("rescan calls = %d, want 1", len(f.manager.rescanCalls))
	}
	if got := f.manager.rescanCalls[0].URLs; len(got) != 2 || got[0] != "https://example.com/a" {
		t.Fatalf("normalized URLs = %#v", got)
	}

	var firstResponse, secondResponse projectRescanResponse
	decodeJSON(t, first, &firstResponse)
	decodeJSON(t, second, &secondResponse)
	if firstResponse.RequestID == "" || firstResponse.RequestID != secondResponse.RequestID {
		t.Fatalf("request IDs = %q/%q", firstResponse.RequestID, secondResponse.RequestID)
	}
	if firstResponse.RequestDigest == "" || firstResponse.RequestDigest != secondResponse.RequestDigest || firstResponse.Status != apikeys.ProjectRescanStatusCompleted {
		t.Fatalf("responses are not stable: %#v / %#v", firstResponse, secondResponse)
	}
	if firstResponse.ProjectID != f.projectID || firstResponse.SessionID != f.sessionID || firstResponse.AcceptedCount != 2 || len(firstResponse.AcceptedURLs) != 2 || firstResponse.StartedAt == nil || firstResponse.CompletedAt == nil {
		t.Fatalf("incomplete typed response: %#v", firstResponse)
	}
}

func TestProjectRescanReturnsRunningDuplicateReceipt(t *testing.T) {
	f := newProjectRescanFixture(t)
	urls := []string{"https://example.com/a"}
	digest := projectRescanDigest(f.projectID, f.sessionID, urls)
	startedAt := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	record, err := f.keyStore.CreateProjectRescanRequest(f.projectID, "running-request", f.sessionID, digest, urls, startedAt)
	if err != nil {
		t.Fatal(err)
	}
	req := f.authorizedRequest(t, "running-request", `{"urls":["https://example.com/a"]}`)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)

	var response projectRescanResponse
	decodeJSON(t, rec, &response)
	if rec.Code != http.StatusConflict || response.Status != apikeys.ProjectRescanStatusRunning ||
		response.ErrorCode != "duplicate_in_progress" || response.RequestID != record.RequestID ||
		response.StartedAt == nil || !response.StartedAt.Equal(startedAt) || response.CompletedAt != nil {
		t.Fatalf("unexpected running duplicate receipt: %d %#v", rec.Code, response)
	}
	if len(response.AcceptedURLs) != 1 || response.AcceptedURLs[0] != urls[0] || response.RequestDigest != digest {
		t.Fatalf("incomplete running duplicate receipt: %#v", response)
	}
	if len(f.manager.rescanCalls) != 0 {
		t.Fatal("running duplicate replay reached rescan manager")
	}
}

func TestProjectRescanKeyCannotTargetAnotherProject(t *testing.T) {
	f := newProjectRescanFixture(t)
	other, err := f.keyStore.CreateProject("other-valid-project")
	if err != nil {
		t.Fatal(err)
	}
	otherSessionID := "other-project-session"
	f.store.getSessionByID[otherSessionID] = &storage.CrawlSession{
		ID: otherSessionID, ProjectID: &other.ID, Status: "completed", SeedURLs: []string{"https://other.example/"},
	}
	path := "/api/projects/" + other.ID + "/sessions/" + otherSessionID + "/rescan-pages"
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"urls":["https://other.example/a"]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "cross-project-attempt")
	req.Header.Set("X-API-Key", f.rescanKey)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)

	var response projectRescanResponse
	decodeJSON(t, rec, &response)
	if rec.Code != http.StatusForbidden || response.ErrorCode != "project_rescan_capability_required" {
		t.Fatalf("response = %d %#v", rec.Code, response)
	}
	if len(f.manager.rescanCalls) != 0 {
		t.Fatal("cross-project capability mismatch reached rescan manager")
	}
	if _, err := f.keyStore.GetProjectRescanRequest(other.ID, "cross-project-attempt"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-project capability mismatch wrote audit ledger: %v", err)
	}
}

func TestProjectRescanRejectsProjectSessionMismatchBeforeMutation(t *testing.T) {
	f := newProjectRescanFixture(t)
	other, err := f.keyStore.CreateProject("other-project")
	if err != nil {
		t.Fatal(err)
	}
	f.store.getSessionByID[f.sessionID].ProjectID = &other.ID
	req := f.authorizedRequest(t, "mismatch-request", `{"urls":["https://example.com/a"]}`)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)

	var response projectRescanResponse
	decodeJSON(t, rec, &response)
	if rec.Code != http.StatusConflict || response.ErrorCode != "project_session_mismatch" {
		t.Fatalf("response = %d %#v", rec.Code, response)
	}
	if len(f.manager.rescanCalls) != 0 {
		t.Fatal("mismatched request reached rescan manager")
	}
}

func TestProjectRescanRejectsInvalidAndCrossOriginURLs(t *testing.T) {
	tests := []struct {
		name string
		body string
		code string
	}{
		{name: "relative", body: `{"urls":["/not-absolute"]}`, code: "invalid_url"},
		{name: "cross origin", body: `{"urls":["https://attacker.example/a"]}`, code: "cross_origin_url"},
		{name: "credentials", body: `{"urls":["https://user:pass@example.com/a"]}`, code: "invalid_url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newProjectRescanFixture(t)
			req := f.authorizedRequest(t, "invalid-"+strings.ReplaceAll(tt.name, " ", "-"), tt.body)
			rec := httptest.NewRecorder()
			f.handler.ServeHTTP(rec, req)
			var response projectRescanResponse
			decodeJSON(t, rec, &response)
			if rec.Code != http.StatusUnprocessableEntity || response.ErrorCode != tt.code {
				t.Fatalf("response = %d %#v", rec.Code, response)
			}
			if len(f.manager.rescanCalls) != 0 {
				t.Fatal("invalid request reached rescan manager")
			}
		})
	}
}

func TestProjectRescanRejectsOversizedURLList(t *testing.T) {
	f := newProjectRescanFixture(t)
	urls := make([]string, maxProjectRescanURLs+1)
	for i := range urls {
		urls[i] = "https://example.com/repeated"
	}
	body, err := json.Marshal(projectRescanRequestBody{URLs: urls})
	if err != nil {
		t.Fatal(err)
	}
	req := f.authorizedRequest(t, "too-many-urls", string(body))
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	var response projectRescanResponse
	decodeJSON(t, rec, &response)
	if rec.Code != http.StatusUnprocessableEntity || response.ErrorCode != "too_many_urls" {
		t.Fatalf("response = %d %#v", rec.Code, response)
	}
	if len(f.manager.rescanCalls) != 0 {
		t.Fatal("oversized request reached manager")
	}
}

func TestProjectRescanRejectsNonRescannableSession(t *testing.T) {
	for _, status := range []string{"running", "stopped", "failed"} {
		t.Run(status, func(t *testing.T) {
			f := newProjectRescanFixture(t)
			f.store.getSessionByID[f.sessionID].Status = status
			req := f.authorizedRequest(t, "non-rescannable-"+status, `{"urls":["https://example.com/a"]}`)
			rec := httptest.NewRecorder()
			f.handler.ServeHTTP(rec, req)

			var response projectRescanResponse
			decodeJSON(t, rec, &response)
			if rec.Code != http.StatusConflict || response.ErrorCode != "session_not_rescannable" {
				t.Fatalf("response = %d %#v", rec.Code, response)
			}
			if len(f.manager.rescanCalls) != 0 {
				t.Fatal("non-rescannable request reached manager")
			}
		})
	}
}

func TestProjectRescanClassifiesControlPlaneFailure(t *testing.T) {
	f := newProjectRescanFixture(t)
	req := f.authorizedRequest(t, "control-plane-failure", `{"urls":["https://example.com/a"]}`)
	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectId}/sessions/{sessionId}/rescan-pages", func(w http.ResponseWriter, r *http.Request) {
		if err := f.keyStore.Close(); err != nil {
			t.Fatal(err)
		}
		f.server.handleProjectRescanPages(w, r)
	})
	apikeys.Authenticate(f.keyStore, "", "")(mux).ServeHTTP(rec, req)
	var response projectRescanResponse
	decodeJSON(t, rec, &response)
	if rec.Code != http.StatusInternalServerError || response.ErrorCode != "internal_error" {
		t.Fatalf("response = %d %#v", rec.Code, response)
	}
	if len(f.manager.rescanCalls) != 0 {
		t.Fatal("control-plane failure reached manager")
	}
}

func TestProjectRescanClassifiesAuditFinalizationFailure(t *testing.T) {
	f := newProjectRescanFixture(t)
	closeErr := error(nil)
	hooked := &hookedProjectRescanManager{
		mockManager: f.manager,
		afterRescan: func() {
			closeErr = f.keyStore.Close()
		},
	}
	f.server.manager = hooked
	req := f.authorizedRequest(t, "audit-finalize-failure", `{"urls":["https://example.com/a"]}`)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	var response projectRescanResponse
	decodeJSON(t, rec, &response)
	if rec.Code != http.StatusInternalServerError || response.Status != "rejected" || response.ErrorCode != "audit_finalize_failed" {
		t.Fatalf("response = %d %#v", rec.Code, response)
	}
	if len(f.manager.rescanCalls) != 1 {
		t.Fatalf("rescan calls = %d, want 1", len(f.manager.rescanCalls))
	}
}

func TestProjectRescanRejectsIdempotencyConflict(t *testing.T) {
	f := newProjectRescanFixture(t)
	first := f.authorizedRequest(t, "same-key", `{"urls":["https://example.com/a"]}`)
	firstRec := httptest.NewRecorder()
	f.manager.rescanCount = 1
	f.handler.ServeHTTP(firstRec, first)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("first response = %d %s", firstRec.Code, firstRec.Body.String())
	}

	conflict := f.authorizedRequest(t, "same-key", `{"urls":["https://example.com/b"]}`)
	conflictRec := httptest.NewRecorder()
	f.handler.ServeHTTP(conflictRec, conflict)
	var response projectRescanResponse
	decodeJSON(t, conflictRec, &response)
	if conflictRec.Code != http.StatusConflict || response.ErrorCode != "idempotency_conflict" {
		t.Fatalf("response = %d %#v", conflictRec.Code, response)
	}
	if len(f.manager.rescanCalls) != 1 {
		t.Fatalf("rescan calls = %d, want 1", len(f.manager.rescanCalls))
	}
}

func TestProjectRescanReturnsStablePageAndProviderFailures(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		statusCode int
		errorCode  string
	}{
		{name: "page missing", err: crawler.ErrRescanPageNotFound, statusCode: http.StatusUnprocessableEntity, errorCode: "url_not_in_session"},
		{name: "crawler busy", err: crawler.ErrRescanBusy, statusCode: http.StatusConflict, errorCode: "session_not_rescannable"},
		{name: "provider failure", err: errors.New("upstream failed with secret-token-value"), statusCode: http.StatusBadGateway, errorCode: "rescan_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newProjectRescanFixture(t)
			f.manager.rescanErr = tt.err
			f.manager.rescanCount = 0
			call := func() *httptest.ResponseRecorder {
				req := f.authorizedRequest(t, "failure-"+strings.ReplaceAll(tt.name, " ", "-"), `{"urls":["https://example.com/a"]}`)
				rec := httptest.NewRecorder()
				f.handler.ServeHTTP(rec, req)
				return rec
			}
			first := call()
			second := call()
			var firstResponse, secondResponse projectRescanResponse
			decodeJSON(t, first, &firstResponse)
			decodeJSON(t, second, &secondResponse)
			if first.Code != tt.statusCode || second.Code != tt.statusCode || firstResponse.ErrorCode != tt.errorCode {
				t.Fatalf("responses = %d %#v / %d %#v", first.Code, firstResponse, second.Code, secondResponse)
			}
			if firstResponse.Status != apikeys.ProjectRescanStatusFailed || secondResponse.Status != apikeys.ProjectRescanStatusFailed ||
				firstResponse.StartedAt == nil || firstResponse.CompletedAt == nil {
				t.Fatalf("failure receipt is incomplete: %#v / %#v", firstResponse, secondResponse)
			}
			if firstResponse.RequestID == "" || firstResponse.RequestID != secondResponse.RequestID || len(f.manager.rescanCalls) != 1 {
				t.Fatalf("failure replay was not stable: %#v / %#v, calls=%d", firstResponse, secondResponse, len(f.manager.rescanCalls))
			}
			if strings.Contains(first.Body.String(), "secret-token-value") || strings.Contains(second.Body.String(), "secret-token-value") {
				t.Fatal("provider error leaked into API response")
			}
		})
	}
}
