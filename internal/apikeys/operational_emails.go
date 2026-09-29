package apikeys

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	OperationalEmailPending  = "pending"
	OperationalEmailSending  = "sending"
	OperationalEmailAccepted = "accepted"
	OperationalEmailFailed   = "failed"
	OperationalEmailSkipped  = "skipped"
	OperationalEmailUnknown  = "unknown"
)

type OperationalEmailCursor struct {
	FinishedAt  time.Time
	SessionID   string
	ActivatedAt time.Time
}

type OperationalEmailRecipient struct {
	UserID  string
	Address string
}

type OperationalEmailEvent struct {
	SessionID     string
	ProjectID     string
	ProjectName   string
	SessionStatus string
	FinishedAt    time.Time
	Type          string
	Summary       string
	ErrorCount    int
	Details       []string
	Recipients    []OperationalEmailRecipient
	SkipReason    string
}

type OperationalEmailReceipt struct {
	ID             int64      `json:"id"`
	SessionID      string     `json:"session_id"`
	ProjectID      string     `json:"project_id,omitempty"`
	ProjectName    string     `json:"project_name"`
	SessionStatus  string     `json:"session_status"`
	FinishedAt     time.Time  `json:"finished_at"`
	Type           string     `json:"event_type"`
	UserID         string     `json:"user_id,omitempty"`
	Recipient      string     `json:"recipient,omitempty"`
	Status         string     `json:"status"`
	Summary        string     `json:"summary"`
	ErrorCount     int        `json:"error_count,omitempty"`
	Details        []string   `json:"details,omitempty"`
	Reason         string     `json:"reason,omitempty"`
	Attempts       int        `json:"attempts"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	FirstAttempt   *time.Time `json:"first_attempt_at,omitempty"`
	NextAttempt    *time.Time `json:"next_attempt_at,omitempty"`
	IdempotencyKey string     `json:"-"`
}

// ListEmailNotificationUsers reads recipient eligibility fields without
// loading project assignments, which are irrelevant for global administrators.
func (s *Store) ListEmailNotificationUsers() ([]User, error) {
	rows, err := s.db.Query(`
		SELECT id, username, role, active, created_at, last_login_at, email, email_verified_at
		FROM users ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, *user)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if users == nil {
		users = []User{}
	}
	return users, nil
}

func (s *Store) InitializeOperationalEmailCursor(now time.Time) error {
	now = now.UTC()
	_, err := s.db.Exec(`
		INSERT OR IGNORE INTO operational_email_state
		(id, watermark_finished_at, watermark_session_id, activated_at)
		VALUES (1, ?, ?, ?)`, now, "\uffff", now)
	return err
}

func (s *Store) OperationalEmailCursor() (OperationalEmailCursor, error) {
	var cursor OperationalEmailCursor
	err := s.db.QueryRow(`
		SELECT watermark_finished_at, watermark_session_id, activated_at
		FROM operational_email_state WHERE id = 1
	`).Scan(&cursor.FinishedAt, &cursor.SessionID, &cursor.ActivatedAt)
	return cursor, err
}

func (s *Store) OperationalSessionIDsScannedSince(finishedAt time.Time) ([]string, error) {
	rows, err := s.db.Query(`SELECT session_id FROM operational_email_scans WHERE finished_at >= ? ORDER BY finished_at, session_id`, finishedAt.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessionIDs []string
	for rows.Next() {
		var sessionID string
		if err := rows.Scan(&sessionID); err != nil {
			return nil, err
		}
		sessionIDs = append(sessionIDs, sessionID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return sessionIDs, nil
}

func (s *Store) OperationalSessionScanned(sessionID string) (bool, error) {
	var exists int
	err := s.db.QueryRow(`SELECT 1 FROM operational_email_scans WHERE session_id = ?`, sessionID).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return exists == 1, err
}

func (s *Store) RecordOperationalSession(sessionID string, finishedAt time.Time, events []OperationalEmailEvent, now time.Time, advanceMain bool) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	result, err := tx.Exec(`INSERT OR IGNORE INTO operational_email_scans(session_id, finished_at, scanned_at) VALUES (?, ?, ?)`, sessionID, finishedAt.UTC(), now.UTC())
	if err != nil {
		return false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if inserted == 0 {
		if advanceMain {
			if err := advanceOperationalEmailWatermark(tx, finishedAt, sessionID); err != nil {
				return false, err
			}
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}

	for _, event := range events {
		details, err := json.Marshal(event.Details)
		if err != nil {
			return false, err
		}
		recipients := event.Recipients
		status := OperationalEmailPending
		reason := ""
		if len(recipients) == 0 {
			recipients = []OperationalEmailRecipient{{}}
			status = OperationalEmailSkipped
			reason = event.SkipReason
			if reason == "" {
				reason = "no_eligible_admin"
			}
		}
		for _, recipient := range recipients {
			key := operationalEmailIdempotencyKey(sessionID, event.Type, recipient.UserID)
			if _, err := tx.Exec(`
				INSERT OR IGNORE INTO operational_email_receipts
				(session_id, project_id, project_name, session_status, finished_at, event_type, user_id, recipient, status,
				 summary, error_count, details_json, reason, idempotency_key, created_at, updated_at, next_attempt_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				sessionID, event.ProjectID, event.ProjectName, event.SessionStatus, event.FinishedAt.UTC(), event.Type, recipient.UserID,
				recipient.Address, status, event.Summary, event.ErrorCount, string(details), reason,
				key, now.UTC(), now.UTC(), nullableAttemptAt(status, now.UTC())); err != nil {
				return false, err
			}
		}
	}

	if advanceMain {
		if err := advanceOperationalEmailWatermark(tx, finishedAt, sessionID); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func advanceOperationalEmailWatermark(tx *sql.Tx, finishedAt time.Time, sessionID string) error {
	_, err := tx.Exec(`
		UPDATE operational_email_state
		SET watermark_finished_at = ?, watermark_session_id = ?
		WHERE id = 1 AND (
			watermark_finished_at < ? OR
			(watermark_finished_at = ? AND watermark_session_id < ?)
		)`, finishedAt.UTC(), sessionID, finishedAt.UTC(), finishedAt.UTC(), sessionID)
	return err
}

func nullableAttemptAt(status string, now time.Time) interface{} {
	if status == OperationalEmailPending {
		return now
	}
	return nil
}

func operationalEmailIdempotencyKey(sessionID, eventType, userID string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{sessionID, eventType, userID}, "\x00")))
	return "crawlobserver-alert-" + hex.EncodeToString(sum[:])
}

func (s *Store) ClaimOperationalEmailReceipts(now time.Time, limit int, retryHorizon, lease time.Duration) ([]OperationalEmailReceipt, error) {
	if limit < 1 {
		return nil, nil
	}
	now = now.UTC()
	cutoff := now.Add(-retryHorizon)
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		UPDATE operational_email_receipts
		SET status = ?, reason = 'retry_horizon_elapsed', updated_at = ?, lease_until = NULL
		WHERE status IN (?, ?) AND first_attempt_at IS NOT NULL AND first_attempt_at <= ?`,
		OperationalEmailUnknown, now, OperationalEmailPending, OperationalEmailSending, cutoff); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`
		UPDATE operational_email_receipts
		SET status = ?, reason = 'attempts_exhausted_unknown', updated_at = ?, lease_until = NULL
		WHERE status = ? AND attempts >= 3 AND lease_until <= ?`,
		OperationalEmailUnknown, now, OperationalEmailSending, now); err != nil {
		return nil, err
	}
	rows, err := tx.Query(`
		SELECT id, session_id, project_id, project_name, session_status, finished_at, event_type, user_id, recipient, status,
		       summary, error_count, details_json, reason, idempotency_key, attempts,
		       created_at, updated_at, first_attempt_at, next_attempt_at
		FROM operational_email_receipts
		WHERE attempts < 3
		  AND (first_attempt_at IS NULL OR first_attempt_at > ?)
		  AND ((status = ? AND COALESCE(next_attempt_at, created_at) <= ?)
		       OR (status = ? AND lease_until <= ?))
		ORDER BY created_at, id
		LIMIT ?`, cutoff, OperationalEmailPending, now, OperationalEmailSending, now, limit)
	if err != nil {
		return nil, err
	}
	var receipts []OperationalEmailReceipt
	for rows.Next() {
		var receipt OperationalEmailReceipt
		var detailsJSON string
		var firstAttempt, nextAttempt sql.NullTime
		if err := rows.Scan(
			&receipt.ID, &receipt.SessionID, &receipt.ProjectID, &receipt.ProjectName, &receipt.SessionStatus, &receipt.FinishedAt, &receipt.Type,
			&receipt.UserID, &receipt.Recipient, &receipt.Status, &receipt.Summary, &receipt.ErrorCount,
			&detailsJSON, &receipt.Reason, &receipt.IdempotencyKey, &receipt.Attempts,
			&receipt.CreatedAt, &receipt.UpdatedAt, &firstAttempt, &nextAttempt,
		); err != nil {
			rows.Close()
			return nil, err
		}
		if err := json.Unmarshal([]byte(detailsJSON), &receipt.Details); err != nil {
			rows.Close()
			return nil, err
		}
		if firstAttempt.Valid {
			receipt.FirstAttempt = &firstAttempt.Time
		}
		if nextAttempt.Valid {
			receipt.NextAttempt = &nextAttempt.Time
		}
		receipts = append(receipts, receipt)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range receipts {
		result, err := tx.Exec(`
			UPDATE operational_email_receipts
			SET status = ?, attempts = attempts + 1,
			    first_attempt_at = COALESCE(first_attempt_at, ?),
			    lease_until = ?, updated_at = ?
			WHERE id = ? AND status IN (?, ?)`,
			OperationalEmailSending, now, now.Add(lease), now, receipts[i].ID,
			OperationalEmailPending, OperationalEmailSending)
		if err != nil {
			return nil, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if changed == 0 {
			continue
		}
		receipts[i].Status = OperationalEmailSending
		receipts[i].Attempts++
		receipts[i].UpdatedAt = now
		if receipts[i].FirstAttempt == nil {
			receipts[i].FirstAttempt = &now
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return receipts, nil
}

func (s *Store) FinishOperationalEmailReceipt(id int64, status, reason string, now time.Time, nextAttempt *time.Time) error {
	if status != OperationalEmailPending && status != OperationalEmailAccepted && status != OperationalEmailFailed && status != OperationalEmailSkipped && status != OperationalEmailUnknown {
		return fmt.Errorf("invalid operational email receipt status %q", status)
	}
	var next interface{}
	if nextAttempt != nil {
		next = nextAttempt.UTC()
	}
	_, err := s.db.Exec(`
		UPDATE operational_email_receipts
		SET status = ?, reason = ?, next_attempt_at = ?, lease_until = NULL, updated_at = ?
		WHERE id = ? AND status = ?`, status, reason, next, now.UTC(), id, OperationalEmailSending)
	return err
}

func (s *Store) ListOperationalEmailReceipts(limit, offset int) ([]OperationalEmailReceipt, int, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	var total int
	if err := s.db.QueryRow(`SELECT count(*) FROM operational_email_receipts`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(`
		SELECT id, session_id, project_id, project_name, session_status, finished_at, event_type, user_id, recipient, status,
		       summary, error_count, details_json, reason, idempotency_key, attempts,
		       created_at, updated_at, first_attempt_at, next_attempt_at
		FROM operational_email_receipts
		ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var receipts []OperationalEmailReceipt
	for rows.Next() {
		var receipt OperationalEmailReceipt
		var detailsJSON string
		var firstAttempt, nextAttempt sql.NullTime
		if err := rows.Scan(
			&receipt.ID, &receipt.SessionID, &receipt.ProjectID, &receipt.ProjectName, &receipt.SessionStatus, &receipt.FinishedAt, &receipt.Type,
			&receipt.UserID, &receipt.Recipient, &receipt.Status, &receipt.Summary, &receipt.ErrorCount,
			&detailsJSON, &receipt.Reason, &receipt.IdempotencyKey, &receipt.Attempts,
			&receipt.CreatedAt, &receipt.UpdatedAt, &firstAttempt, &nextAttempt,
		); err != nil {
			return nil, 0, err
		}
		if err := json.Unmarshal([]byte(detailsJSON), &receipt.Details); err != nil {
			return nil, 0, err
		}
		if firstAttempt.Valid {
			receipt.FirstAttempt = &firstAttempt.Time
		}
		if nextAttempt.Valid {
			receipt.NextAttempt = &nextAttempt.Time
		}
		if receipt.Status == OperationalEmailSending {
			receipt.Status = OperationalEmailPending
		}
		receipts = append(receipts, receipt)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if receipts == nil {
		receipts = []OperationalEmailReceipt{}
	}
	return receipts, total, nil
}
