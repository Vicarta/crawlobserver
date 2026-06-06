package apikeys

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"
)

type User struct {
	ID          string     `json:"id"`
	Username    string     `json:"username"`
	Role        string     `json:"role"`
	ProjectIDs  []string   `json:"project_ids"`
	Active      bool       `json:"active"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at"`
}

type AuthSession struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

func normalizeUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
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

func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.db.Query(`
		SELECT id, username, role, active, created_at, last_login_at
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
		SELECT id, username, role, active, created_at, last_login_at
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
		SELECT id, username, role, active, created_at, last_login_at
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

func (s *Store) AuthenticateUser(username, password string) (*User, error) {
	username = normalizeUsername(username)
	row := s.db.QueryRow(`
		SELECT id, username, password_hash, role, active, created_at, last_login_at
		FROM users WHERE username = ? AND active = 1`, username)

	var passwordHash string
	var activeInt int
	u := &User{}
	if err := row.Scan(&u.ID, &u.Username, &passwordHash, &u.Role, &activeInt, &u.CreatedAt, &u.LastLoginAt); err != nil {
		return nil, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)); err != nil {
		return nil, err
	}
	u.Active = activeInt != 0
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
		SELECT u.id, u.username, u.role, u.active, u.created_at, u.last_login_at
		FROM user_sessions us
		JOIN users u ON u.id = us.user_id
		WHERE us.token_hash = ? AND us.expires_at > ? AND u.active = 1`,
		tokenHash(token), now)
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
	rows, err := s.db.Query(`SELECT project_id FROM user_projects WHERE user_id = ? ORDER BY project_id`, userID)
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
	u := &User{}
	if err := row.Scan(&u.ID, &u.Username, &u.Role, &activeInt, &u.CreatedAt, &u.LastLoginAt); err != nil {
		return nil, err
	}
	u.Active = activeInt != 0
	if u.ProjectIDs == nil {
		u.ProjectIDs = []string{}
	}
	return u, nil
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
