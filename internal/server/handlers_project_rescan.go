package server

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/SEObserver/crawlobserver/internal/apikeys"
	"github.com/SEObserver/crawlobserver/internal/crawler"
)

const (
	maxProjectRescanURLs      = 200
	maxProjectRescanURLLength = 4096
	maxProjectRescanBodyBytes = 1 << 20
	maxProjectRescanKeyLength = 200
)

type projectRescanRequestBody struct {
	URLs []string `json:"urls"`
}

type projectRescanResponse struct {
	ProjectID      string     `json:"project_id,omitempty"`
	SessionID      string     `json:"session_id,omitempty"`
	RequestID      string     `json:"request_id,omitempty"`
	IdempotencyKey string     `json:"idempotency_key,omitempty"`
	Status         string     `json:"status,omitempty"`
	AcceptedCount  int        `json:"accepted_url_count,omitempty"`
	AcceptedURLs   []string   `json:"accepted_urls,omitempty"`
	RequestDigest  string     `json:"request_digest,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	ErrorCode      string     `json:"error_code,omitempty"`
	Error          string     `json:"error,omitempty"`
}

type projectRescanInputError struct {
	code    string
	message string
}

func (e *projectRescanInputError) Error() string { return e.message }

func (s *Server) handleProjectRescanPages(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.PathValue("projectId"))
	sessionID := strings.TrimSpace(r.PathValue("sessionId"))
	if projectID == "" || sessionID == "" {
		writeProjectRescanError(w, http.StatusBadRequest, projectID, sessionID, "invalid_scope", "project_id and session_id are required")
		return
	}
	if !projectRescanCapabilityAllowed(r, projectID) {
		writeProjectRescanError(w, http.StatusForbidden, projectID, sessionID, "project_rescan_capability_required", "a targeted_rescan API key bound to this project is required")
		return
	}
	if s.keyStore == nil {
		writeProjectRescanError(w, http.StatusInternalServerError, projectID, sessionID, "internal_error", "rescan request storage is unavailable")
		return
	}

	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" || len(idempotencyKey) > maxProjectRescanKeyLength {
		writeProjectRescanError(w, http.StatusBadRequest, projectID, sessionID, "invalid_idempotency_key", fmt.Sprintf("Idempotency-Key is required and must be at most %d characters", maxProjectRescanKeyLength))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxProjectRescanBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var body projectRescanRequestBody
	if err := decoder.Decode(&body); err != nil {
		writeProjectRescanError(w, http.StatusBadRequest, projectID, sessionID, "invalid_request", "request body must contain a valid urls array")
		return
	}
	if err := rejectTrailingJSON(decoder); err != nil {
		writeProjectRescanError(w, http.StatusBadRequest, projectID, sessionID, "invalid_request", "request body must contain one JSON object")
		return
	}

	if _, err := s.keyStore.GetProject(projectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProjectRescanError(w, http.StatusNotFound, projectID, sessionID, "project_not_found", "project not found")
			return
		}
		writeProjectRescanError(w, http.StatusInternalServerError, projectID, sessionID, "internal_error", "project lookup failed")
		return
	}

	session, err := s.store.GetSession(r.Context(), sessionID)
	if err != nil {
		if isNotFoundErr(err) || strings.Contains(strings.ToLower(err.Error()), "not found") {
			writeProjectRescanError(w, http.StatusNotFound, projectID, sessionID, "session_not_found", "session not found")
			return
		}
		writeProjectRescanError(w, http.StatusInternalServerError, projectID, sessionID, "internal_error", "session lookup failed")
		return
	}
	if session.ProjectID == nil || *session.ProjectID != projectID {
		writeProjectRescanError(w, http.StatusConflict, projectID, sessionID, "project_session_mismatch", "session does not belong to the supplied project")
		return
	}
	normalizedURLs, err := validateProjectRescanURLs(body.URLs, session.SeedURLs)
	if err != nil {
		var inputErr *projectRescanInputError
		if errors.As(err, &inputErr) {
			statusCode := http.StatusUnprocessableEntity
			if inputErr.code == "session_not_rescannable" {
				statusCode = http.StatusConflict
			}
			writeProjectRescanError(w, statusCode, projectID, sessionID, inputErr.code, inputErr.message)
			return
		}
		writeProjectRescanError(w, http.StatusUnprocessableEntity, projectID, sessionID, "invalid_url", "one or more URLs are invalid")
		return
	}
	digest := projectRescanDigest(projectID, sessionID, normalizedURLs)

	// The process-wide writer lock guarantees one server writer. This mutex also
	// serializes idempotency claim, mutation, and terminal ledger publication.
	s.projectRescanMu.Lock()
	defer s.projectRescanMu.Unlock()

	if existing, err := s.keyStore.GetProjectRescanRequest(projectID, idempotencyKey); err == nil {
		if existing.RequestDigest != digest {
			writeProjectRescanError(w, http.StatusConflict, projectID, sessionID, "idempotency_conflict", "Idempotency-Key was already used for a different targeted rescan request")
			return
		}
		writeStoredProjectRescanResponse(w, existing)
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		writeProjectRescanError(w, http.StatusInternalServerError, projectID, sessionID, "internal_error", "idempotency lookup failed")
		return
	}
	if !projectRescanSessionStatusAllowed(session.Status) || s.manager.IsRunning(sessionID) || s.manager.IsQueued(sessionID) {
		writeProjectRescanError(w, http.StatusConflict, projectID, sessionID, "session_not_rescannable", "session is not in a terminal rescannable state")
		return
	}

	startedAt := time.Now().UTC()
	if _, err := s.keyStore.CreateProjectRescanRequest(projectID, idempotencyKey, sessionID, digest, normalizedURLs, startedAt); err != nil {
		writeProjectRescanError(w, http.StatusInternalServerError, projectID, sessionID, "internal_error", "idempotency claim failed")
		return
	}

	count, rescanErr := s.manager.RescanPages(sessionID, normalizedURLs)
	completedAt := time.Now().UTC()
	if rescanErr != nil {
		errorCode, message, statusCode := classifyProjectRescanFailure(rescanErr)
		finished, finishErr := s.keyStore.FinishProjectRescanRequest(projectID, idempotencyKey, apikeys.ProjectRescanStatusFailed, 0, errorCode, message, completedAt)
		if finishErr != nil {
			writeProjectRescanError(w, http.StatusInternalServerError, projectID, sessionID, "internal_error", "rescan failed and audit finalization failed")
			return
		}
		writeStoredProjectRescanResponseWithStatus(w, finished, statusCode)
		return
	}

	finished, err := s.keyStore.FinishProjectRescanRequest(projectID, idempotencyKey, apikeys.ProjectRescanStatusCompleted, count, "", "", completedAt)
	if err != nil {
		// The rescan already ran. Leave the durable request in running state so an
		// identical retry fails closed instead of running the mutation again.
		writeProjectRescanError(w, http.StatusInternalServerError, projectID, sessionID, "audit_finalize_failed", "rescan completed but its audit record could not be finalized")
		return
	}
	writeStoredProjectRescanResponse(w, finished)
}

func projectRescanCapabilityAllowed(r *http.Request, projectID string) bool {
	auth := apikeys.FromContext(r.Context())
	return auth.CanTargetedRescan(projectID)
}

func projectRescanSessionStatusAllowed(status string) bool {
	return status == "completed" || status == "completed_with_errors"
}

func validateProjectRescanURLs(rawURLs, seedURLs []string) ([]string, error) {
	if len(rawURLs) == 0 {
		return nil, &projectRescanInputError{code: "invalid_url", message: "at least one URL is required"}
	}
	if len(rawURLs) > maxProjectRescanURLs {
		return nil, &projectRescanInputError{code: "too_many_urls", message: fmt.Sprintf("at most %d URLs are allowed", maxProjectRescanURLs)}
	}

	allowedOrigins := make(map[string]struct{})
	for _, seed := range seedURLs {
		parsed, err := parseAbsoluteRescanURL(seed)
		if err == nil {
			allowedOrigins[rescanOrigin(parsed)] = struct{}{}
		}
	}
	if len(allowedOrigins) == 0 {
		return nil, &projectRescanInputError{code: "session_not_rescannable", message: "session has no valid canonical seed origin"}
	}

	seen := make(map[string]struct{}, len(rawURLs))
	normalized := make([]string, 0, len(rawURLs))
	for _, raw := range rawURLs {
		if len(raw) > maxProjectRescanURLLength {
			return nil, &projectRescanInputError{code: "invalid_url", message: "URL exceeds the maximum supported length"}
		}
		parsed, err := parseAbsoluteRescanURL(strings.TrimSpace(raw))
		if err != nil {
			return nil, &projectRescanInputError{code: "invalid_url", message: "every URL must be an absolute HTTP or HTTPS page URL"}
		}
		if _, ok := allowedOrigins[rescanOrigin(parsed)]; !ok {
			return nil, &projectRescanInputError{code: "cross_origin_url", message: "every URL must use an allowed session seed origin"}
		}
		parsed.Fragment = ""
		canonical := parsed.String()
		if _, ok := seen[canonical]; ok {
			continue
		}
		seen[canonical] = struct{}{}
		normalized = append(normalized, canonical)
	}
	if len(normalized) == 0 {
		return nil, &projectRescanInputError{code: "invalid_url", message: "at least one URL is required"}
	}
	return normalized, nil
}

func parseAbsoluteRescanURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil {
		return nil, fmt.Errorf("invalid absolute URL")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("unsupported URL scheme")
	}
	return parsed, nil
}

func rescanOrigin(parsed *url.URL) string {
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return strings.ToLower(parsed.Scheme + "://" + parsed.Hostname() + ":" + port)
}

func projectRescanDigest(projectID, sessionID string, urls []string) string {
	canonical := append([]string(nil), urls...)
	sort.Strings(canonical)
	hash := sha256.New()
	_, _ = io.WriteString(hash, projectID)
	hash.Write([]byte{0})
	_, _ = io.WriteString(hash, sessionID)
	for _, pageURL := range canonical {
		hash.Write([]byte{0})
		_, _ = io.WriteString(hash, pageURL)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func rejectTrailingJSON(decoder *json.Decoder) error {
	var extra interface{}
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("unexpected trailing JSON value")
}

func classifyProjectRescanFailure(err error) (string, string, int) {
	switch {
	case errors.Is(err, crawler.ErrRescanSessionUnavailable), errors.Is(err, crawler.ErrRescanBusy):
		return "session_not_rescannable", "session is not currently rescannable", http.StatusConflict
	case errors.Is(err, crawler.ErrRescanPageNotFound):
		return "url_not_in_session", "one or more URLs are not present in the target session", http.StatusUnprocessableEntity
	default:
		return "rescan_failed", "targeted rescan failed", http.StatusBadGateway
	}
}

func writeProjectRescanError(w http.ResponseWriter, status int, projectID, sessionID, code, message string) {
	writeProjectRescanJSON(w, status, projectRescanResponse{
		ProjectID: projectID,
		SessionID: sessionID,
		Status:    "rejected",
		ErrorCode: code,
		Error:     message,
	})
}

func writeStoredProjectRescanResponse(w http.ResponseWriter, record *apikeys.ProjectRescanRequest) {
	statusCode := http.StatusOK
	if record.Status == apikeys.ProjectRescanStatusRunning {
		statusCode = http.StatusConflict
	} else if record.Status == apikeys.ProjectRescanStatusFailed {
		statusCode = projectRescanFailureHTTPStatus(record.ErrorCode)
	}
	writeStoredProjectRescanResponseWithStatus(w, record, statusCode)
}

func writeStoredProjectRescanResponseWithStatus(w http.ResponseWriter, record *apikeys.ProjectRescanRequest, statusCode int) {
	startedAt := record.StartedAt.UTC()
	response := projectRescanResponse{
		ProjectID:      record.ProjectID,
		SessionID:      record.SessionID,
		RequestID:      record.RequestID,
		IdempotencyKey: record.IdempotencyKey,
		Status:         record.Status,
		AcceptedCount:  record.AcceptedCount,
		AcceptedURLs:   append([]string(nil), record.URLs...),
		RequestDigest:  record.RequestDigest,
		StartedAt:      &startedAt,
		CompletedAt:    record.CompletedAt,
		ErrorCode:      record.ErrorCode,
		Error:          record.ErrorMessage,
	}
	if record.Status == apikeys.ProjectRescanStatusRunning {
		response.ErrorCode = "duplicate_in_progress"
		response.Error = "an identical targeted rescan request is still in progress"
	}
	writeProjectRescanJSON(w, statusCode, response)
}

func projectRescanFailureHTTPStatus(errorCode string) int {
	switch errorCode {
	case "session_not_rescannable":
		return http.StatusConflict
	case "url_not_in_session":
		return http.StatusUnprocessableEntity
	default:
		return http.StatusBadGateway
	}
}

func writeProjectRescanJSON(w http.ResponseWriter, status int, response projectRescanResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}
