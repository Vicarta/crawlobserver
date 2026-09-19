package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SEObserver/crawlobserver/internal/apikeys"
	"github.com/SEObserver/crawlobserver/internal/applog"
)

var authCodeRequestResponse = map[string]string{
	"status":  "accepted",
	"message": "If the email is registered, a login code will be sent.",
}

func (s *Server) handleAuthCodeRequest(w http.ResponseWriter, r *http.Request) {
	if s.keyStore == nil {
		writeError(w, http.StatusServiceUnavailable, "local users are not available")
		return
	}

	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	code, challenge, user, err := s.keyStore.IssueLoginCode(req.Email)
	if err != nil {
		internalError(w, r, err)
		return
	}
	if user != nil && challenge != nil {
		s.deliverLoginCode(user.Email, code, challenge.ID)
	}
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, authCodeRequestResponse)
}

func (s *Server) deliverLoginCode(email, code, challengeID string) {
	if s.emailSender == nil {
		applog.Warn("auth", "passwordless login email delivery unavailable")
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), resendRequestTimeout)
		defer cancel()
		if err := s.emailSender.SendLoginCode(ctx, LoginCodeEmail{
			To:             email,
			Code:           code,
			IdempotencyKey: challengeID,
		}); err != nil {
			applog.Warnf("auth", "passwordless login email delivery failed: %v", err)
		}
	}()
}

func (s *Server) handleAuthCodeVerify(w http.ResponseWriter, r *http.Request) {
	if s.keyStore == nil {
		writeError(w, http.StatusServiceUnavailable, "local users are not available")
		return
	}
	var req struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	user, err := s.keyStore.ConsumeLoginCode(req.Email, req.Code)
	if err != nil {
		if errors.Is(err, apikeys.ErrInvalidLoginCode) {
			writeError(w, http.StatusUnauthorized, "invalid or expired login code")
			return
		}
		internalError(w, r, err)
		return
	}
	s.writePasswordlessSession(w, r, user)
}

func (s *Server) handleInspectInvitation(w http.ResponseWriter, r *http.Request) {
	if s.keyStore == nil {
		writeError(w, http.StatusServiceUnavailable, "local users are not available")
		return
	}
	invitation, user, err := s.keyStore.InspectInvitation(r.PathValue("token"))
	if err != nil {
		writeInvitationError(w, err)
		return
	}
	writeJSON(w, map[string]interface{}{
		"email":      user.Email,
		"expires_at": invitation.ExpiresAt,
	})
}

func (s *Server) handleAcceptInvitation(w http.ResponseWriter, r *http.Request) {
	if s.keyStore == nil {
		writeError(w, http.StatusServiceUnavailable, "local users are not available")
		return
	}
	user, err := s.keyStore.AcceptInvitation(r.PathValue("token"))
	if err != nil {
		writeInvitationError(w, err)
		return
	}
	s.writePasswordlessSession(w, r, user)
}

func (s *Server) writePasswordlessSession(w http.ResponseWriter, r *http.Request, user *apikeys.User) {
	token, session, err := s.keyStore.CreateUserSession(user.ID, apikeys.PasswordlessSessionTTL)
	if err != nil {
		if strings.HasPrefix(r.URL.Path, "/api/auth/invitations/") {
			applog.Errorf("auth", "passwordless session creation failed: %v", err)
			writeError(w, http.StatusInternalServerError, "internal server error")
		} else {
			internalError(w, r, err)
		}
		return
	}
	s.setAuthCookie(w, r, token, session.ExpiresAt)
	writeJSON(w, authUserPayload(user, "session"))
}

func writeInvitationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, apikeys.ErrExpiredInvitation):
		writeError(w, http.StatusGone, "invitation expired")
	case errors.Is(err, apikeys.ErrUsedInvitation):
		writeError(w, http.StatusConflict, "invitation already accepted")
	case errors.Is(err, apikeys.ErrInvalidInvitation), errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "invitation not found")
	default:
		applog.Errorf("auth", "invitation storage failure: %v", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if s.keyStore != nil {
		if cookie, err := r.Cookie(apikeys.SessionCookieName); err == nil {
			_ = s.keyStore.DeleteUserSession(cookie.Value)
		}
	}
	s.clearAuthCookie(w, r)
	writeJSON(w, map[string]string{"status": "logged_out"})
}

func (s *Server) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	auth := apikeys.FromContext(r.Context())
	if auth == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if auth.Method == "session" && s.keyStore != nil && auth.UserID != "" {
		user, err := s.keyStore.GetUser(auth.UserID)
		if err == nil {
			writeJSON(w, authUserPayload(user, auth.Method))
			return
		}
	}
	role := apikeys.RoleAdmin
	projectIDs := []string{}
	if !auth.IsAdmin() {
		role = apikeys.RoleViewer
		projectIDs = auth.AllowedProjectIDs()
	}
	writeJSON(w, map[string]interface{}{
		"method":      auth.Method,
		"username":    auth.Username,
		"role":        role,
		"project_ids": projectIDs,
	})
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAccess(w, r) {
		return
	}
	if s.keyStore == nil {
		writeError(w, http.StatusServiceUnavailable, "local users are not available")
		return
	}
	users, err := s.keyStore.ListUsers()
	if err != nil {
		internalError(w, r, err)
		return
	}
	payload := make([]map[string]interface{}, 0, len(users))
	for i := range users {
		payload = append(payload, userManagementPayload(&users[i]))
	}
	writeJSON(w, payload)
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAccess(w, r) {
		return
	}
	if s.keyStore == nil {
		writeError(w, http.StatusServiceUnavailable, "local users are not available")
		return
	}
	var req struct {
		Email      string   `json:"email"`
		Role       string   `json:"role"`
		ProjectIDs []string `json:"project_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Role == "" {
		req.Role = apikeys.RoleViewer
	}
	user, err := s.keyStore.CreatePasswordlessUser(req.Email, req.Role, req.ProjectIDs)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, userManagementPayload(user))
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAccess(w, r) {
		return
	}
	if s.keyStore == nil {
		writeError(w, http.StatusServiceUnavailable, "local users are not available")
		return
	}
	var req struct {
		Email      string   `json:"email"`
		Role       string   `json:"role"`
		Active     *bool    `json:"active"`
		ProjectIDs []string `json:"project_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	existing, err := s.keyStore.GetUser(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if req.Email == "" {
		req.Email = existing.Email
	}
	if req.Role == "" {
		req.Role = existing.Role
	}
	active := existing.Active
	if req.Active != nil {
		active = *req.Active
	}
	if existing.Role == apikeys.RoleAdmin && req.Role != apikeys.RoleAdmin {
		admins, err := s.keyStore.CountUsersByRole(apikeys.RoleAdmin)
		if err != nil {
			internalError(w, r, err)
			return
		}
		if admins <= 1 {
			writeError(w, http.StatusBadRequest, "cannot change the role of the last administrator")
			return
		}
	}
	user, err := s.keyStore.UpdatePasswordlessUser(existing.ID, req.Email, req.Role, active, req.ProjectIDs)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, userManagementPayload(user))
}

func (s *Server) handleInviteUser(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAccess(w, r) {
		return
	}
	if s.keyStore == nil {
		writeError(w, http.StatusServiceUnavailable, "local users are not available")
		return
	}
	user, err := s.keyStore.GetUser(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if strings.TrimSpace(user.Email) == "" {
		writeError(w, http.StatusBadRequest, "user email is required")
		return
	}
	token, invitation, recipient, err := s.keyStore.IssueInvitation(user.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.emailSender == nil {
		writeError(w, http.StatusServiceUnavailable, "email delivery is not configured")
		return
	}
	invitationURL := s.publicBaseURL(r) + "/?invite=" + url.QueryEscape(token)
	if err := s.emailSender.SendInvitation(r.Context(), InvitationEmail{
		To:             recipient.Email,
		InvitationURL:  invitationURL,
		IdempotencyKey: invitation.ID,
	}); err != nil {
		applog.Warnf("auth", "invitation email delivery failed for user %s: %v", user.ID, err)
		writeError(w, http.StatusServiceUnavailable, "email delivery unavailable")
		return
	}
	writeJSON(w, map[string]interface{}{
		"status":     "sent",
		"user_id":    user.ID,
		"expires_at": invitation.ExpiresAt,
	})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAccess(w, r) {
		return
	}
	if s.keyStore == nil {
		writeError(w, http.StatusServiceUnavailable, "local users are not available")
		return
	}
	user, err := s.keyStore.GetUser(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if user.Role == apikeys.RoleAdmin {
		admins, err := s.keyStore.CountUsersByRole(apikeys.RoleAdmin)
		if err != nil {
			internalError(w, r, err)
			return
		}
		if admins <= 1 {
			writeError(w, http.StatusBadRequest, "cannot delete the last administrator")
			return
		}
	}
	if err := s.keyStore.DeleteUser(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	writeJSON(w, map[string]string{"status": "deleted"})
}

func authUserPayload(user *apikeys.User, method string) map[string]interface{} {
	payload := userManagementPayload(user)
	payload["method"] = method
	return payload
}

func userManagementPayload(user *apikeys.User) map[string]interface{} {
	return map[string]interface{}{
		"id":                user.ID,
		"username":          user.Username,
		"email":             user.Email,
		"email_verified":    user.EmailVerifiedAt != nil,
		"email_verified_at": user.EmailVerifiedAt,
		"role":              user.Role,
		"project_ids":       user.ProjectIDs,
		"active":            user.Active,
		"created_at":        user.CreatedAt,
		"last_login_at":     user.LastLoginAt,
	}
}

func (s *Server) setAuthCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     apikeys.SessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		Secure:   s.requestUsesHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearAuthCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     apikeys.SessionCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.requestUsesHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) requestUsesHTTPS(r *http.Request) bool {
	publicURL := ""
	if s.cfg != nil {
		publicURL = strings.ToLower(strings.TrimSpace(s.cfg.Server.PublicURL))
	}
	return strings.HasPrefix(publicURL, "https://") || r.TLS != nil ||
		strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
