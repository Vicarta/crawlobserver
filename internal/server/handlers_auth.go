package server

import (
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/SEObserver/crawlobserver/internal/apikeys"
)

const authSessionTTL = 7 * 24 * time.Hour

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if s.keyStore == nil {
		writeError(w, http.StatusServiceUnavailable, "local users are not available")
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	user, err := s.keyStore.AuthenticateUser(req.Username, req.Password)
	if err != nil {
		user, err = s.authenticateConfigAdmin(req.Username, req.Password)
	}
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	token, session, err := s.keyStore.CreateUserSession(user.ID, authSessionTTL)
	if err != nil {
		internalError(w, r, err)
		return
	}
	setAuthCookie(w, token, session.ExpiresAt)
	writeJSON(w, authUserPayload(user, "session"))
}

func (s *Server) authenticateConfigAdmin(username, password string) (*apikeys.User, error) {
	if s.cfg.Server.Username == "" || s.cfg.Server.Password == "" {
		return nil, sql.ErrNoRows
	}
	if subtle.ConstantTimeCompare([]byte(username), []byte(s.cfg.Server.Username)) != 1 ||
		subtle.ConstantTimeCompare([]byte(password), []byte(s.cfg.Server.Password)) != 1 {
		return nil, sql.ErrNoRows
	}

	user, err := s.keyStore.GetUserByUsername(username)
	if err == nil {
		if user.Role != apikeys.RoleAdmin || !user.Active {
			active := true
			pass := password
			return s.keyStore.UpdateUser(user.ID, username, apikeys.RoleAdmin, active, &pass, nil)
		}
		return user, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return s.keyStore.CreateUser(username, password, apikeys.RoleAdmin, nil)
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if s.keyStore != nil {
		if cookie, err := r.Cookie(apikeys.SessionCookieName); err == nil {
			_ = s.keyStore.DeleteUserSession(cookie.Value)
		}
	}
	clearAuthCookie(w)
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
	writeJSON(w, users)
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
		Username   string   `json:"username"`
		Password   string   `json:"password"`
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
	user, err := s.keyStore.CreateUser(req.Username, req.Password, req.Role, req.ProjectIDs)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, user)
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
		Username   string   `json:"username"`
		Password   string   `json:"password"`
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
	if req.Username == "" {
		req.Username = existing.Username
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
	var password *string
	if req.Password != "" {
		password = &req.Password
	}
	user, err := s.keyStore.UpdateUser(existing.ID, req.Username, req.Role, active, password, req.ProjectIDs)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, user)
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
	return map[string]interface{}{
		"id":          user.ID,
		"method":      method,
		"username":    user.Username,
		"role":        user.Role,
		"project_ids": user.ProjectIDs,
		"active":      user.Active,
	}
}

func setAuthCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     apikeys.SessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearAuthCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     apikeys.SessionCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}
