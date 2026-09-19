package apikeys

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"

	InvitationTTL          = 7 * 24 * time.Hour
	LoginCodeTTL           = 15 * time.Minute
	PasswordlessSessionTTL = 14 * 24 * time.Hour
	LoginCodeMaxAttempts   = 5
)

var (
	ErrInvalidInvitation = errors.New("invalid invitation")
	ErrExpiredInvitation = fmt.Errorf("%w: expired", ErrInvalidInvitation)
	ErrUsedInvitation    = fmt.Errorf("%w: already used", ErrInvalidInvitation)
	ErrInvalidLoginCode  = errors.New("invalid or expired login code")
)

type User struct {
	ID              string     `json:"id"`
	Username        string     `json:"username"`
	Email           string     `json:"email"`
	EmailVerifiedAt *time.Time `json:"email_verified_at,omitempty"`
	Role            string     `json:"role"`
	ProjectIDs      []string   `json:"project_ids"`
	Active          bool       `json:"active"`
	CreatedAt       time.Time  `json:"created_at"`
	LastLoginAt     *time.Time `json:"last_login_at"`
}

// Invitation has no token or token hash. The raw token is returned separately
// only when it is issued so callers can send it to the intended recipient.
type Invitation struct {
	ID         string     `json:"id"`
	UserID     string     `json:"user_id"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	AcceptedAt *time.Time `json:"accepted_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

type LoginChallenge struct {
	ID            string     `json:"id"`
	UserID        string     `json:"user_id"`
	CreatedAt     time.Time  `json:"created_at"`
	ExpiresAt     time.Time  `json:"expires_at"`
	Attempts      int        `json:"attempts"`
	ConsumedAt    *time.Time `json:"consumed_at,omitempty"`
	InvalidatedAt *time.Time `json:"invalidated_at,omitempty"`
}

type AuthSession struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

func normalizeUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

func normalizeEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return "", fmt.Errorf("email is required")
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email {
		return "", fmt.Errorf("email must be a single mailbox address")
	}
	return email, nil
}

func validateRole(role string) error {
	if role != RoleAdmin && role != RoleViewer {
		return fmt.Errorf("invalid role: %s", role)
	}
	return nil
}

func hashPassword(password string) (string, error) {
	if len(password) < 8 {
		return "", fmt.Errorf("password must be at least 8 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func generateSessionToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func generateLoginCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// CreateUser remains for compatibility with callers that construct legacy
// records. Passwordless handlers must use CreatePasswordlessUser instead.
func (s *Store) CreateUser(username, password, role string, projectIDs []string) (*User, error) {
	username = normalizeUsername(username)
	if username == "" {
		return nil, fmt.Errorf("username is required")
	}
	if err := validateRole(role); err != nil {
		return nil, err
	}
	passwordHash, err := hashPassword(password)
	if err != nil {
		return nil, err
	}

	u := &User{
		ID:         uuid.New().String(),
		Username:   username,
		Role:       role,
		ProjectIDs: uniqueStrings(projectIDs),
		Active:     true,
		CreatedAt:  time.Now().UTC(),
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		INSERT INTO users (id, username, password_hash, role, active, created_at)
		VALUES (?, ?, ?, ?, 1, ?)`,
		u.ID, u.Username, passwordHash, u.Role, u.CreatedAt); err != nil {
		return nil, err
	}
	if err := replaceUserProjects(tx, u.ID, u.ProjectIDs); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Store) CreatePasswordlessUser(email, role string, projectIDs []string) (*User, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		return nil, err
	}
	if err := validateRole(role); err != nil {
		return nil, err
	}

	u := &User{
		ID:         uuid.New().String(),
		Username:   "passwordless-" + uuid.New().String(),
		Email:      email,
		Role:       role,
		ProjectIDs: uniqueStrings(projectIDs),
		Active:     true,
		CreatedAt:  time.Now().UTC(),
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// password_hash is a legacy NOT NULL column. An empty value keeps it inert
	// for passwordless identities without fabricating a usable credential.
	if _, err := tx.Exec(`
		INSERT INTO users (id, username, password_hash, email, role, active, created_at)
		VALUES (?, ?, '', ?, ?, 1, ?)`,
		u.ID, u.Username, u.Email, u.Role, u.CreatedAt); err != nil {
		return nil, err
	}
	if err := replaceUserProjects(tx, u.ID, u.ProjectIDs); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.db.Query(`
		SELECT id, username, role, active, created_at, last_login_at, email, email_verified_at
		FROM users ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		projects, err := s.UserProjectIDs(u.ID)
		if err != nil {
			return nil, err
		}
		u.ProjectIDs = projects
		users = append(users, *u)
	}
	if users == nil {
		users = []User{}
	}
	return users, rows.Err()
}

func (s *Store) CountUsersByRole(role string) (int, error) {
	if err := validateRole(role); err != nil {
		return 0, err
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role = ?`, role).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) GetUser(id string) (*User, error) {
	row := s.db.QueryRow(`
		SELECT id, username, role, active, created_at, last_login_at, email, email_verified_at
		FROM users WHERE id = ?`, id)
	u, err := scanUser(row)
	if err != nil {
		return nil, err
	}
	u.ProjectIDs, err = s.UserProjectIDs(u.ID)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Store) GetUserByUsername(username string) (*User, error) {
	row := s.db.QueryRow(`
		SELECT id, username, role, active, created_at, last_login_at, email, email_verified_at
		FROM users WHERE username = ?`, normalizeUsername(username))
	u, err := scanUser(row)
	if err != nil {
		return nil, err
	}
	u.ProjectIDs, err = s.UserProjectIDs(u.ID)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Store) GetUserByEmail(email string) (*User, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		return nil, err
	}
	row := s.db.QueryRow(`
		SELECT id, username, role, active, created_at, last_login_at, email, email_verified_at
		FROM users WHERE email = ?`, email)
	u, err := scanUser(row)
	if err != nil {
		return nil, err
	}
	u.ProjectIDs, err = s.UserProjectIDs(u.ID)
	if err != nil {
		return nil, err
	}
	return u, nil
}

// UpdateUser remains for legacy callers. It intentionally does not alter
// passwordless email identity fields.
func (s *Store) UpdateUser(id, username, role string, active bool, password *string, projectIDs []string) (*User, error) {
	username = normalizeUsername(username)
	if username == "" {
		return nil, fmt.Errorf("username is required")
	}
	if err := validateRole(role); err != nil {
		return nil, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if password != nil && *password != "" {
		passwordHash, err := hashPassword(*password)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`UPDATE users SET username = ?, password_hash = ?, role = ?, active = ? WHERE id = ?`,
			username, passwordHash, role, boolInt(active), id); err != nil {
			return nil, err
		}
	} else {
		if _, err := tx.Exec(`UPDATE users SET username = ?, role = ?, active = ? WHERE id = ?`,
			username, role, boolInt(active), id); err != nil {
			return nil, err
		}
	}
	if err := replaceUserProjects(tx, id, uniqueStrings(projectIDs)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetUser(id)
}

func (s *Store) UpdatePasswordlessUser(id, email, role string, active bool, projectIDs []string) (*User, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()

	if err := validateRole(role); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var currentEmail sql.NullString
	var currentActive int
	if err := tx.QueryRow(`SELECT email, active FROM users WHERE id = ?`, id).Scan(&currentEmail, &currentActive); err != nil {
		return nil, err
	}

	requestedEmail := strings.TrimSpace(email)
	var res sql.Result
	identityChanged := false
	if requestedEmail == "" {
		if currentEmail.Valid && currentEmail.String != "" {
			return nil, fmt.Errorf("email is required")
		}
		res, err = tx.Exec(`
			UPDATE users
			SET email = NULL, email_verified_at = NULL, role = ?, active = ?
			WHERE id = ?`, role, boolInt(active), id)
	} else {
		normalizedEmail, normalizeErr := normalizeEmail(requestedEmail)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		identityChanged = !currentEmail.Valid || currentEmail.String != normalizedEmail
		res, err = tx.Exec(`
			UPDATE users
			SET email = ?, email_verified_at = CASE WHEN email IS ? THEN email_verified_at ELSE NULL END,
				role = ?, active = ?
			WHERE id = ?`, normalizedEmail, normalizedEmail, role, boolInt(active), id)
	}
	if err != nil {
		return nil, err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, sql.ErrNoRows
	}
	disabled := currentActive != 0 && !active
	// Identity changes and deactivation invalidate delivery credentials. Role and
	// project changes leave a pending invitation or login challenge intact.
	if identityChanged || disabled {
		if _, err := tx.Exec(`UPDATE user_invitations SET revoked_at = ? WHERE user_id = ? AND accepted_at IS NULL AND revoked_at IS NULL`, now, id); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`UPDATE user_login_challenges SET invalidated_at = ? WHERE user_id = ? AND consumed_at IS NULL AND invalidated_at IS NULL`, now, id); err != nil {
			return nil, err
		}
	}
	if err := replaceUserProjects(tx, id, uniqueStrings(projectIDs)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetUser(id)
}

func (s *Store) DeleteUser(id string) error {
	res, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("user not found")
	}
	return nil
}

// AuthenticateUser is retained only for source compatibility with legacy
// callers. Passwordless HTTP handlers must never call it.
func (s *Store) AuthenticateUser(username, password string) (*User, error) {
	username = normalizeUsername(username)
	row := s.db.QueryRow(`
		SELECT id, username, password_hash, role, active, created_at, last_login_at, email, email_verified_at
		FROM users WHERE username = ? AND active = 1`, username)

	var passwordHash string
	var activeInt int
	var email sql.NullString
	u := &User{}
	if err := row.Scan(&u.ID, &u.Username, &passwordHash, &u.Role, &activeInt, &u.CreatedAt, &u.LastLoginAt, &email, &u.EmailVerifiedAt); err != nil {
		return nil, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)); err != nil {
		return nil, err
	}
	u.Active = activeInt != 0
	if email.Valid {
		u.Email = email.String
	}
	projects, err := s.UserProjectIDs(u.ID)
	if err != nil {
		return nil, err
	}
	u.ProjectIDs = projects
	now := time.Now().UTC()
	if _, err := s.db.Exec(`UPDATE users SET last_login_at = ? WHERE id = ?`, now, u.ID); err != nil {
		return nil, err
	}
	u.LastLoginAt = &now
	return u, nil
}

func (s *Store) IssueInvitation(userID string) (string, *Invitation, *User, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()

	token, err := generateSessionToken()
	if err != nil {
		return "", nil, nil, err
	}
	now := time.Now().UTC()
	invitation := &Invitation{
		ID:        uuid.New().String(),
		UserID:    userID,
		CreatedAt: now,
		ExpiresAt: now.Add(InvitationTTL),
	}

	tx, err := s.db.Begin()
	if err != nil {
		return "", nil, nil, err
	}
	defer tx.Rollback()
	recipient, err := scanUser(tx.QueryRow(`
		SELECT id, username, role, active, created_at, last_login_at, email, email_verified_at
		FROM users
		WHERE id = ? AND active = 1 AND email IS NOT NULL AND TRIM(email) <> ''`, userID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil, nil, fmt.Errorf("user must be active and have an email address")
		}
		return "", nil, nil, err
	}
	recipient.ProjectIDs, err = userProjectIDs(tx, recipient.ID)
	if err != nil {
		return "", nil, nil, err
	}
	if _, err := tx.Exec(`UPDATE user_invitations SET revoked_at = ? WHERE user_id = ? AND accepted_at IS NULL AND revoked_at IS NULL`, now, userID); err != nil {
		return "", nil, nil, err
	}
	if _, err := tx.Exec(`
		INSERT INTO user_invitations (id, user_id, token_hash, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?)`, invitation.ID, invitation.UserID, tokenHash(token), invitation.CreatedAt, invitation.ExpiresAt); err != nil {
		return "", nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return "", nil, nil, err
	}
	return token, invitation, recipient, nil
}

func (s *Store) InspectInvitation(token string) (*Invitation, *User, error) {
	if token == "" {
		return nil, nil, ErrInvalidInvitation
	}
	now := time.Now().UTC()
	row := s.db.QueryRow(`
		SELECT i.id, i.user_id, i.created_at, i.expires_at, i.accepted_at, i.revoked_at,
			u.id, u.username, u.role, u.active, u.created_at, u.last_login_at, u.email, u.email_verified_at
		FROM user_invitations i
		JOIN users u ON u.id = i.user_id
		WHERE i.token_hash = ?`, tokenHash(token))
	invitation, user, err := scanInvitationUser(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrInvalidInvitation
		}
		return nil, nil, err
	}
	if err := invitationStateError(invitation, now); err != nil {
		return nil, nil, err
	}
	if !user.Active || user.Email == "" {
		return nil, nil, ErrInvalidInvitation
	}
	user.ProjectIDs, err = s.UserProjectIDs(user.ID)
	if err != nil {
		return nil, nil, err
	}
	return invitation, user, nil
}

func (s *Store) AcceptInvitation(token string) (*User, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()

	if token == "" {
		return nil, ErrInvalidInvitation
	}
	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var invitationID, userID string
	var expiresAt time.Time
	var acceptedAt, revokedAt *time.Time
	var active int
	var email sql.NullString
	if err := tx.QueryRow(`
		SELECT i.id, i.user_id, i.expires_at, i.accepted_at, i.revoked_at, u.active, u.email
		FROM user_invitations i
		JOIN users u ON u.id = i.user_id
		WHERE i.token_hash = ?`, tokenHash(token)).Scan(&invitationID, &userID, &expiresAt, &acceptedAt, &revokedAt, &active, &email); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInvalidInvitation
		}
		return nil, err
	}
	if err := invitationStateError(&Invitation{ExpiresAt: expiresAt, AcceptedAt: acceptedAt, RevokedAt: revokedAt}, now); err != nil {
		return nil, err
	}
	if active == 0 || !email.Valid {
		return nil, ErrInvalidInvitation
	}
	res, err := tx.Exec(`
		UPDATE user_invitations SET accepted_at = ?
		WHERE id = ? AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > ?`, now, invitationID, now)
	if err != nil {
		return nil, err
	}
	accepted, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if accepted != 1 {
		return nil, ErrInvalidInvitation
	}
	res, err = tx.Exec(`
		UPDATE users SET email_verified_at = COALESCE(email_verified_at, ?)
		WHERE id = ? AND active = 1 AND email IS NOT NULL`, now, userID)
	if err != nil {
		return nil, err
	}
	verified, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if verified != 1 {
		return nil, ErrInvalidInvitation
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetUser(userID)
}

func invitationStateError(invitation *Invitation, now time.Time) error {
	if invitation.AcceptedAt != nil {
		return ErrUsedInvitation
	}
	if !invitation.ExpiresAt.After(now) {
		return ErrExpiredInvitation
	}
	if invitation.RevokedAt != nil {
		return ErrInvalidInvitation
	}
	return nil
}

// IssueLoginCode returns a code only for active, verified users. Nil challenge
// and user with a nil error cover unknown, inactive, unverified, and malformed
// email input so the HTTP layer can keep its request response uniform.
func (s *Store) IssueLoginCode(email string) (string, *LoginChallenge, *User, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()

	email, err := normalizeEmail(email)
	if err != nil {
		return "", nil, nil, nil
	}
	code, err := generateLoginCode()
	if err != nil {
		return "", nil, nil, err
	}
	codeHash, err := bcrypt.GenerateFromPassword([]byte(code), bcrypt.DefaultCost)
	if err != nil {
		return "", nil, nil, err
	}
	now := time.Now().UTC()
	challenge := &LoginChallenge{
		ID:        uuid.New().String(),
		CreatedAt: now,
		ExpiresAt: now.Add(LoginCodeTTL),
	}

	tx, err := s.db.Begin()
	if err != nil {
		return "", nil, nil, err
	}
	defer tx.Rollback()
	if err := tx.QueryRow(`
		SELECT id FROM users
		WHERE email = ? AND email_verified_at IS NOT NULL AND active = 1`, email).Scan(&challenge.UserID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil, nil, nil
		}
		return "", nil, nil, err
	}
	if _, err := tx.Exec(`
		UPDATE user_login_challenges SET invalidated_at = ?
		WHERE user_id = ? AND consumed_at IS NULL AND invalidated_at IS NULL`, now, challenge.UserID); err != nil {
		return "", nil, nil, err
	}
	if _, err := tx.Exec(`
		INSERT INTO user_login_challenges (id, user_id, code_hash, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?)`, challenge.ID, challenge.UserID, string(codeHash), challenge.CreatedAt, challenge.ExpiresAt); err != nil {
		return "", nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return "", nil, nil, err
	}
	user, err := s.GetUser(challenge.UserID)
	if err != nil {
		return "", nil, nil, err
	}
	return code, challenge, user, nil
}

func (s *Store) ConsumeLoginCode(email, code string) (*User, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()

	email, err := normalizeEmail(email)
	if err != nil || !validLoginCode(code) {
		return nil, ErrInvalidLoginCode
	}
	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var userID string
	if err := tx.QueryRow(`
		SELECT id FROM users
		WHERE email = ? AND email_verified_at IS NOT NULL AND active = 1`, email).Scan(&userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInvalidLoginCode
		}
		return nil, err
	}
	var challengeID, codeHash string
	var attempts int
	if err := tx.QueryRow(`
		SELECT id, code_hash, attempts FROM user_login_challenges
		WHERE user_id = ? AND consumed_at IS NULL AND invalidated_at IS NULL AND expires_at > ?
		ORDER BY created_at DESC LIMIT 1`, userID, now).Scan(&challengeID, &codeHash, &attempts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInvalidLoginCode
		}
		return nil, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(codeHash), []byte(code)); err != nil {
		attempts++
		if attempts >= LoginCodeMaxAttempts {
			_, err = tx.Exec(`
				UPDATE user_login_challenges SET attempts = ?, invalidated_at = ?
				WHERE id = ? AND consumed_at IS NULL AND invalidated_at IS NULL`, attempts, now, challengeID)
		} else {
			_, err = tx.Exec(`
				UPDATE user_login_challenges SET attempts = ?
				WHERE id = ? AND consumed_at IS NULL AND invalidated_at IS NULL`, attempts, challengeID)
		}
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, ErrInvalidLoginCode
	}
	res, err := tx.Exec(`
		UPDATE user_login_challenges SET consumed_at = ?
		WHERE id = ? AND consumed_at IS NULL AND invalidated_at IS NULL AND expires_at > ?`, now, challengeID, now)
	if err != nil {
		return nil, err
	}
	consumed, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if consumed != 1 {
		return nil, ErrInvalidLoginCode
	}
	if _, err := tx.Exec(`UPDATE users SET last_login_at = ? WHERE id = ?`, now, userID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetUser(userID)
}

func validLoginCode(code string) bool {
	if len(code) != 6 {
		return false
	}
	for i := 0; i < len(code); i++ {
		if code[i] < '0' || code[i] > '9' {
			return false
		}
	}
	return true
}

func (s *Store) CreateUserSession(userID string, ttl time.Duration) (string, *AuthSession, error) {
	if ttl <= 0 {
		return "", nil, fmt.Errorf("session ttl must be positive")
	}
	token, err := generateSessionToken()
	if err != nil {
		return "", nil, err
	}
	session := &AuthSession{
		ID:        uuid.New().String(),
		UserID:    userID,
		ExpiresAt: time.Now().UTC().Add(ttl),
	}
	if _, err := s.db.Exec(`
		INSERT INTO user_sessions (id, user_id, token_hash, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?)`,
		session.ID, session.UserID, tokenHash(token), time.Now().UTC(), session.ExpiresAt); err != nil {
		return "", nil, err
	}
	return token, session, nil
}

func (s *Store) ValidateUserSession(token string) (*User, error) {
	if token == "" {
		return nil, sql.ErrNoRows
	}
	now := time.Now().UTC()
	s.db.Exec(`DELETE FROM user_sessions WHERE expires_at <= ?`, now)

	row := s.db.QueryRow(`
		SELECT u.id, u.username, u.role, u.active, u.created_at, u.last_login_at, u.email, u.email_verified_at
		FROM user_sessions us
		JOIN users u ON u.id = us.user_id
		WHERE us.token_hash = ? AND us.expires_at > ? AND u.active = 1`, tokenHash(token), now)
	u, err := scanUser(row)
	if err != nil {
		return nil, err
	}
	u.ProjectIDs, err = s.UserProjectIDs(u.ID)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Store) DeleteUserSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM user_sessions WHERE token_hash = ?`, tokenHash(token))
	return err
}

func (s *Store) UserProjectIDs(userID string) ([]string, error) {
	return userProjectIDs(s.db, userID)
}

type userProjectIDQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

func userProjectIDs(queryer userProjectIDQuerier, userID string) ([]string, error) {
	rows, err := queryer.Query(`SELECT project_id FROM user_projects WHERE user_id = ? ORDER BY project_id`, userID)
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
	if ids == nil {
		ids = []string{}
	}
	return ids, rows.Err()
}

func replaceUserProjects(tx *sql.Tx, userID string, projectIDs []string) error {
	if _, err := tx.Exec(`DELETE FROM user_projects WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, projectID := range uniqueStrings(projectIDs) {
		if projectID == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO user_projects (user_id, project_id) VALUES (?, ?)`, userID, projectID); err != nil {
			return err
		}
	}
	return nil
}

type userScanner interface {
	Scan(dest ...interface{}) error
}

func scanUser(row userScanner) (*User, error) {
	var activeInt int
	var email sql.NullString
	u := &User{}
	if err := row.Scan(&u.ID, &u.Username, &u.Role, &activeInt, &u.CreatedAt, &u.LastLoginAt, &email, &u.EmailVerifiedAt); err != nil {
		return nil, err
	}
	u.Active = activeInt != 0
	if email.Valid {
		u.Email = email.String
	}
	if u.ProjectIDs == nil {
		u.ProjectIDs = []string{}
	}
	return u, nil
}

func scanInvitationUser(row userScanner) (*Invitation, *User, error) {
	invitation := &Invitation{}
	user := &User{}
	var activeInt int
	var email sql.NullString
	if err := row.Scan(
		&invitation.ID, &invitation.UserID, &invitation.CreatedAt, &invitation.ExpiresAt, &invitation.AcceptedAt, &invitation.RevokedAt,
		&user.ID, &user.Username, &user.Role, &activeInt, &user.CreatedAt, &user.LastLoginAt, &email, &user.EmailVerifiedAt,
	); err != nil {
		return nil, nil, err
	}
	user.Active = activeInt != 0
	if email.Valid {
		user.Email = email.String
	}
	user.ProjectIDs = []string{}
	return invitation, user, nil
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
