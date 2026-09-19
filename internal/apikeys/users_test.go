package apikeys

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestUserEmailMigrationPreservesLegacyIdentityAndSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-users.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	created := time.Now().UTC().Add(-time.Hour)
	expires := time.Now().UTC().Add(time.Hour)
	if _, err := legacy.Exec(`
		CREATE TABLE users (
			id TEXT PRIMARY KEY,
			username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			role TEXT NOT NULL CHECK(role IN ('admin', 'viewer')),
			active INTEGER DEFAULT 1,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_login_at DATETIME
		);
		CREATE TABLE projects (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE user_projects (
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			PRIMARY KEY (user_id, project_id)
		);
		CREATE TABLE user_sessions (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			token_hash TEXT NOT NULL UNIQUE,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			expires_at DATETIME NOT NULL
		);`); err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO users (id, username, password_hash, role, active, created_at) VALUES ('legacy-user', 'legacy', 'legacy-hash', 'viewer', 1, ?)`, created); err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO projects (id, name, created_at) VALUES ('legacy-project', 'legacy project', ?)`, created); err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO user_projects (user_id, project_id) VALUES ('legacy-user', 'legacy-project')`); err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO user_sessions (id, user_id, token_hash, created_at, expires_at) VALUES ('legacy-session', 'legacy-user', ?, ?, ?)`, tokenHash("legacy-session-token"), created, expires); err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	user, err := store.GetUser("legacy-user")
	if err != nil {
		t.Fatal(err)
	}
	if user.Username != "legacy" || user.Role != RoleViewer || user.Email != "" || user.EmailVerifiedAt != nil || len(user.ProjectIDs) != 1 || user.ProjectIDs[0] != "legacy-project" {
		t.Fatalf("legacy user changed during migration: %+v", user)
	}
	var storedHash string
	if err := store.db.QueryRow(`SELECT token_hash FROM user_sessions WHERE id = 'legacy-session'`).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if storedHash != tokenHash("legacy-session-token") {
		t.Fatalf("legacy session changed during migration: hash=%q", storedHash)
	}
	sessionUser, err := store.ValidateUserSession("legacy-session-token")
	if err != nil || sessionUser.ID != user.ID {
		t.Fatalf("legacy session was not preserved: user=%+v err=%v", sessionUser, err)
	}

	updated, err := store.UpdatePasswordlessUser(user.ID, " Legacy@Example.COM ", user.Role, user.Active, user.ProjectIDs)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Email != "legacy@example.com" || updated.EmailVerifiedAt != nil || updated.Username != "legacy" || updated.Role != RoleViewer {
		t.Fatalf("legacy identity was not safely migrated: %+v", updated)
	}
	if _, _, _, err := store.IssueInvitation(user.ID); err != nil {
		t.Fatal(err)
	}
}

func TestUserEmailNormalizationAndUniqueIdentity(t *testing.T) {
	store := newTestStore(t)
	first, err := store.CreatePasswordlessUser(" First.User+tag@Example.COM ", RoleViewer, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Email != "first.user+tag@example.com" || first.EmailVerifiedAt != nil {
		t.Fatalf("unexpected normalized identity: %+v", first)
	}
	if _, err := store.CreatePasswordlessUser("first.user+tag@example.com", RoleViewer, nil); err == nil {
		t.Fatal("expected normalized duplicate email to fail")
	}
	if _, err := store.CreatePasswordlessUser("A Person <person@example.com>", RoleViewer, nil); err == nil {
		t.Fatal("expected display-name address to fail")
	}
	if _, err := store.CreatePasswordlessUser("not-an-email", RoleViewer, nil); err == nil {
		t.Fatal("expected invalid email to fail")
	}
}

func TestPasswordlessUserUpdateRejectsDuplicateEmail(t *testing.T) {
	store := newTestStore(t)
	first, err := store.CreatePasswordlessUser("first@example.com", RoleViewer, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreatePasswordlessUser("second@example.com", RoleViewer, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdatePasswordlessUser(first.ID, second.Email, RoleViewer, true, nil); err == nil {
		t.Fatal("expected duplicate-email update to fail")
	}
	unchanged, err := store.GetUser(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Email != first.Email {
		t.Fatalf("duplicate-email update changed identity: got %q, want %q", unchanged.Email, first.Email)
	}
}

func TestLegacyNoEmailUserCanUpdatePermissionsAndDisable(t *testing.T) {
	store := newTestStore(t)
	project, err := store.CreateProject("legacy-user-project")
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := store.CreateUser("legacy-no-email", "password123", RoleViewer, []string{project.ID})
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := store.CreateUserSession(legacy.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	updated, err := store.UpdatePasswordlessUser(legacy.ID, "", RoleAdmin, true, []string{project.ID})
	if err != nil {
		t.Fatalf("role update for legacy no-email user: %v", err)
	}
	if updated.Email != "" || updated.EmailVerifiedAt != nil || updated.Role != RoleAdmin || !updated.Active || len(updated.ProjectIDs) != 1 || updated.ProjectIDs[0] != project.ID {
		t.Fatalf("legacy no-email permission update changed identity or authorization: %+v", updated)
	}
	var persistedEmail sql.NullString
	if err := store.db.QueryRow(`SELECT email FROM users WHERE id = ?`, legacy.ID).Scan(&persistedEmail); err != nil {
		t.Fatal(err)
	}
	if persistedEmail.Valid {
		t.Fatalf("legacy empty email should remain NULL, got %q", persistedEmail.String)
	}
	if sessionUser, err := store.ValidateUserSession(token); err != nil || sessionUser.ID != legacy.ID || sessionUser.Role != RoleAdmin {
		t.Fatalf("legacy session did not survive permission update: user=%+v err=%v", sessionUser, err)
	}

	disabled, err := store.UpdatePasswordlessUser(legacy.ID, "", RoleAdmin, false, []string{project.ID})
	if err != nil {
		t.Fatalf("disable legacy no-email user: %v", err)
	}
	if disabled.Email != "" || disabled.EmailVerifiedAt != nil || disabled.Active {
		t.Fatalf("legacy no-email disable changed identity: %+v", disabled)
	}
}

func TestInvitationReplacementExpirationAndSingleUse(t *testing.T) {
	store := newTestStore(t)
	user, err := store.CreatePasswordlessUser("invitee@example.com", RoleViewer, nil)
	if err != nil {
		t.Fatal(err)
	}
	firstToken, first, recipient, err := store.IssueInvitation(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recipient.ID != user.ID || recipient.Email != user.Email || recipient.EmailVerifiedAt != nil {
		t.Fatalf("invitation recipient snapshot = %+v, want %q", recipient, user.Email)
	}
	if got := first.ExpiresAt.Sub(first.CreatedAt); got != InvitationTTL {
		t.Fatalf("invitation TTL = %s, want %s", got, InvitationTTL)
	}
	var storedHash string
	if err := store.db.QueryRow(`SELECT token_hash FROM user_invitations WHERE id = ?`, first.ID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if storedHash != tokenHash(firstToken) || strings.Contains(storedHash, firstToken) {
		t.Fatal("invitation token was not stored only as its hash")
	}

	secondToken, second, recipient, err := store.IssueInvitation(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recipient.Email != user.Email {
		t.Fatalf("replacement invitation recipient = %q, want %q", recipient.Email, user.Email)
	}
	if second.ID == first.ID || secondToken == firstToken {
		t.Fatal("replacement invitation was not unique")
	}
	if _, _, err := store.InspectInvitation(firstToken); !errors.Is(err, ErrInvalidInvitation) {
		t.Fatalf("replaced invitation inspection error = %v, want invalid", err)
	}
	if _, err := store.AcceptInvitation(firstToken); !errors.Is(err, ErrInvalidInvitation) {
		t.Fatalf("replaced invitation acceptance error = %v, want invalid", err)
	}

	inspected, invitationUser, err := store.InspectInvitation(secondToken)
	if err != nil {
		t.Fatal(err)
	}
	if inspected.ID != second.ID || invitationUser.ID != user.ID || invitationUser.Email != user.Email {
		t.Fatalf("unexpected inspection result: invitation=%+v user=%+v", inspected, invitationUser)
	}
	accepted, err := store.AcceptInvitation(secondToken)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.ID != user.ID || accepted.EmailVerifiedAt == nil {
		t.Fatalf("invitation did not verify the intended user: %+v", accepted)
	}
	if _, _, err := store.InspectInvitation(secondToken); !errors.Is(err, ErrUsedInvitation) {
		t.Fatalf("used invitation inspection error = %v, want used", err)
	}
	if _, err := store.AcceptInvitation(secondToken); !errors.Is(err, ErrUsedInvitation) {
		t.Fatalf("replayed invitation acceptance error = %v, want used", err)
	}

	expiredToken, expired, _, err := store.IssueInvitation(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE user_invitations SET expires_at = ? WHERE id = ?`, time.Now().UTC().Add(-time.Second), expired.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.InspectInvitation(expiredToken); !errors.Is(err, ErrExpiredInvitation) {
		t.Fatalf("expired invitation inspection error = %v, want expired", err)
	}
	if _, err := store.AcceptInvitation(expiredToken); !errors.Is(err, ErrExpiredInvitation) {
		t.Fatalf("expired invitation acceptance error = %v, want expired", err)
	}
}

func TestLoginCodeReplacementExpiryAttemptsAndSingleUse(t *testing.T) {
	store := newTestStore(t)
	user := createVerifiedPasswordlessUser(t, store, "code-user@example.com")

	firstCode, _, _, err := store.IssueLoginCode(user.Email)
	if err != nil {
		t.Fatal(err)
	}
	secondCode, challenge, _, err := store.IssueLoginCode(user.Email)
	if err != nil {
		t.Fatal(err)
	}
	if !validLoginCode(firstCode) || !validLoginCode(secondCode) || firstCode == secondCode {
		t.Fatalf("invalid or reused generated codes: %q and %q", firstCode, secondCode)
	}
	if challenge == nil || challenge.UserID != user.ID || challenge.ExpiresAt.Sub(challenge.CreatedAt) != LoginCodeTTL {
		t.Fatalf("unexpected challenge metadata: %+v", challenge)
	}
	var codeHash string
	if err := store.db.QueryRow(`SELECT code_hash FROM user_login_challenges WHERE user_id = ? AND invalidated_at IS NULL`, user.ID).Scan(&codeHash); err != nil {
		t.Fatal(err)
	}
	if codeHash == secondCode || bcrypt.CompareHashAndPassword([]byte(codeHash), []byte(secondCode)) != nil {
		t.Fatal("login code was not stored as a bcrypt hash")
	}
	if _, err := store.ConsumeLoginCode(user.Email, firstCode); !errors.Is(err, ErrInvalidLoginCode) {
		t.Fatalf("replaced login code error = %v, want invalid", err)
	}

	wrong := wrongCode(secondCode)
	for attempt := 1; attempt <= LoginCodeMaxAttempts; attempt++ {
		if _, err := store.ConsumeLoginCode(user.Email, wrong); !errors.Is(err, ErrInvalidLoginCode) {
			t.Fatalf("failed attempt %d error = %v, want invalid", attempt, err)
		}
	}
	if _, err := store.ConsumeLoginCode(user.Email, secondCode); !errors.Is(err, ErrInvalidLoginCode) {
		t.Fatalf("locked login code error = %v, want invalid", err)
	}

	thirdCode, _, _, err := store.IssueLoginCode(user.Email)
	if err != nil {
		t.Fatal(err)
	}
	loggedIn, err := store.ConsumeLoginCode(user.Email, thirdCode)
	if err != nil {
		t.Fatal(err)
	}
	if loggedIn.ID != user.ID || loggedIn.LastLoginAt == nil {
		t.Fatalf("successful login code did not update the user: %+v", loggedIn)
	}
	if _, err := store.ConsumeLoginCode(user.Email, thirdCode); !errors.Is(err, ErrInvalidLoginCode) {
		t.Fatalf("replayed login code error = %v, want invalid", err)
	}

	expiredCode, _, _, err := store.IssueLoginCode(user.Email)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE user_login_challenges SET expires_at = ? WHERE user_id = ? AND invalidated_at IS NULL AND consumed_at IS NULL`, time.Now().UTC().Add(-time.Second), user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeLoginCode(user.Email, expiredCode); !errors.Is(err, ErrInvalidLoginCode) {
		t.Fatalf("expired login code error = %v, want invalid", err)
	}
}

func TestLoginCodeUnknownAndUnverifiedUsersDoNotReceiveChallenge(t *testing.T) {
	store := newTestStore(t)
	if code, challenge, user, err := store.IssueLoginCode("unknown@example.com"); err != nil || code != "" || challenge != nil || user != nil {
		t.Fatalf("unknown email challenge = %q, %+v, %+v, %v; want no challenge", code, challenge, user, err)
	}
	unverified, err := store.CreatePasswordlessUser("unverified@example.com", RoleViewer, nil)
	if err != nil {
		t.Fatal(err)
	}
	if code, challenge, user, err := store.IssueLoginCode(unverified.Email); err != nil || code != "" || challenge != nil || user != nil {
		t.Fatalf("unverified email challenge = %q, %+v, %+v, %v; want no challenge", code, challenge, user, err)
	}
	if _, err := store.ConsumeLoginCode("unknown@example.com", "123456"); !errors.Is(err, ErrInvalidLoginCode) {
		t.Fatalf("unknown email consume error = %v, want invalid", err)
	}
}

func TestPasswordlessUserUpdatePreservesOrRevokesDeliveryCredentialsByChange(t *testing.T) {
	store := newTestStore(t)
	user := createVerifiedPasswordlessUser(t, store, "updates@example.com")
	token, _, _, err := store.IssueInvitation(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	code, _, _, err := store.IssueLoginCode(user.Email)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdatePasswordlessUser(user.ID, user.Email, RoleAdmin, true, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.InspectInvitation(token); err != nil {
		t.Fatalf("same-email role update revoked invitation: %v", err)
	}
	if _, err := store.ConsumeLoginCode(user.Email, code); err != nil {
		t.Fatalf("same-email role update invalidated code: %v", err)
	}

	token, _, _, err = store.IssueInvitation(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	code, _, _, err = store.IssueLoginCode(user.Email)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdatePasswordlessUser(user.ID, "new-address@example.com", RoleAdmin, true, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.InspectInvitation(token); !errors.Is(err, ErrInvalidInvitation) {
		t.Fatalf("email change invitation error = %v, want invalid", err)
	}
	if _, err := store.ConsumeLoginCode(user.Email, code); !errors.Is(err, ErrInvalidLoginCode) {
		t.Fatalf("email change code error = %v, want invalid", err)
	}
}

func TestPasswordlessSessionTTL(t *testing.T) {
	store := newTestStore(t)
	user, err := store.CreateUser("session-user", "password123", RoleViewer, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC()
	_, session, err := store.CreateUserSession(user.ID, PasswordlessSessionTTL)
	if err != nil {
		t.Fatal(err)
	}
	after := time.Now().UTC()
	if session.ExpiresAt.Before(before.Add(PasswordlessSessionTTL)) || session.ExpiresAt.After(after.Add(PasswordlessSessionTTL)) {
		t.Fatalf("session expiry = %s, want exactly 14 days from creation", session.ExpiresAt)
	}
}

func createVerifiedPasswordlessUser(t *testing.T, store *Store, email string) *User {
	t.Helper()
	user, err := store.CreatePasswordlessUser(email, RoleViewer, nil)
	if err != nil {
		t.Fatal(err)
	}
	token, _, _, err := store.IssueInvitation(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := store.AcceptInvitation(token)
	if err != nil {
		t.Fatal(err)
	}
	return verified
}

func wrongCode(code string) string {
	if code == "000000" {
		return "999999"
	}
	return "000000"
}
