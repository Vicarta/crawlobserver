package apikeys

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const (
	ProjectRescanStatusRunning   = "running"
	ProjectRescanStatusCompleted = "completed"
	ProjectRescanStatusFailed    = "failed"
)

// ProjectRescanRequest is the durable idempotency and audit record for a
// project-bound targeted rescan request.
type ProjectRescanRequest struct {
	ProjectID      string
	IdempotencyKey string
	RequestID      string
	SessionID      string
	RequestDigest  string
	URLs           []string
	Status         string
	AcceptedCount  int
	ErrorCode      string
	ErrorMessage   string
	StartedAt      time.Time
	CompletedAt    *time.Time
}

func (s *Store) GetProjectRescanRequest(projectID, idempotencyKey string) (*ProjectRescanRequest, error) {
	var record ProjectRescanRequest
	var urlsJSON string
	var completedAt sql.NullTime
	err := s.db.QueryRow(`
		SELECT project_id, idempotency_key, request_id, session_id, request_digest,
		       urls_json, status, accepted_count, error_code, error_message,
		       started_at, completed_at
		FROM project_rescan_requests
		WHERE project_id = ? AND idempotency_key = ?
	`, projectID, idempotencyKey).Scan(
		&record.ProjectID, &record.IdempotencyKey, &record.RequestID,
		&record.SessionID, &record.RequestDigest, &urlsJSON, &record.Status,
		&record.AcceptedCount, &record.ErrorCode, &record.ErrorMessage,
		&record.StartedAt, &completedAt,
	)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(urlsJSON), &record.URLs); err != nil {
		return nil, fmt.Errorf("decoding project rescan URLs: %w", err)
	}
	if completedAt.Valid {
		record.CompletedAt = &completedAt.Time
	}
	return &record, nil
}

func (s *Store) CreateProjectRescanRequest(projectID, idempotencyKey, sessionID, requestDigest string, urls []string, startedAt time.Time) (*ProjectRescanRequest, error) {
	urlsJSON, err := json.Marshal(urls)
	if err != nil {
		return nil, fmt.Errorf("encoding project rescan URLs: %w", err)
	}
	record := &ProjectRescanRequest{
		ProjectID:      projectID,
		IdempotencyKey: idempotencyKey,
		RequestID:      uuid.NewString(),
		SessionID:      sessionID,
		RequestDigest:  requestDigest,
		URLs:           append([]string(nil), urls...),
		Status:         ProjectRescanStatusRunning,
		StartedAt:      startedAt.UTC(),
	}
	_, err = s.db.Exec(`
		INSERT INTO project_rescan_requests (
			project_id, idempotency_key, request_id, session_id, request_digest,
			urls_json, status, accepted_count, started_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?)
	`, record.ProjectID, record.IdempotencyKey, record.RequestID, record.SessionID,
		record.RequestDigest, string(urlsJSON), record.Status, record.StartedAt)
	if err != nil {
		return nil, fmt.Errorf("creating project rescan request: %w", err)
	}
	return record, nil
}

func (s *Store) FinishProjectRescanRequest(projectID, idempotencyKey, status string, acceptedCount int, errorCode, errorMessage string, completedAt time.Time) (*ProjectRescanRequest, error) {
	if status != ProjectRescanStatusCompleted && status != ProjectRescanStatusFailed {
		return nil, fmt.Errorf("invalid project rescan terminal status %q", status)
	}
	result, err := s.db.Exec(`
		UPDATE project_rescan_requests
		SET status = ?, accepted_count = ?, error_code = ?, error_message = ?, completed_at = ?
		WHERE project_id = ? AND idempotency_key = ? AND status = 'running'
	`, status, acceptedCount, errorCode, errorMessage, completedAt.UTC(), projectID, idempotencyKey)
	if err != nil {
		return nil, fmt.Errorf("finishing project rescan request: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return nil, fmt.Errorf("project rescan request is not running")
	}
	return s.GetProjectRescanRequest(projectID, idempotencyKey)
}
