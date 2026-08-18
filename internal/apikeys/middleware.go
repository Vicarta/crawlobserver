package apikeys

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
)

const SessionCookieName = "crawlobserver_session"

type contextKey struct{}

type AuthInfo struct {
	Method     string  // "basic" | "apikey" | "session"
	KeyType    string  // "general" | "project" (only for apikey)
	ProjectID  *string // non-nil only for project keys
	Capability string  // optional narrowly scoped project mutation capability
	UserID     string
	Username   string
	Role       string
	ProjectIDs []string
}

func (a *AuthInfo) CanTargetedRescan(projectID string) bool {
	return a != nil && a.Method == "apikey" && a.KeyType == "project" &&
		a.Capability == CapabilityTargetedRescan && a.ProjectID != nil && *a.ProjectID == projectID
}

func (a *AuthInfo) IsReadOnly() bool {
	return (a.Method == "apikey" && a.KeyType == "project") ||
		(a.Method == "session" && a.Role != RoleAdmin)
}

func (a *AuthInfo) IsAdmin() bool {
	return a == nil ||
		a.Method == "basic" ||
		(a.Method == "apikey" && a.KeyType == "general") ||
		(a.Method == "session" && a.Role == RoleAdmin)
}

func (a *AuthInfo) AllowedProjectIDs() []string {
	if a == nil || a.IsAdmin() {
		return nil
	}
	if a.ProjectID != nil {
		return []string{*a.ProjectID}
	}
	return a.ProjectIDs
}

func (a *AuthInfo) HasProjectAccess(projectID string) bool {
	if a == nil || a.IsAdmin() {
		return true
	}
	for _, allowed := range a.AllowedProjectIDs() {
		if allowed == projectID {
			return true
		}
	}
	return false
}

func FromContext(ctx context.Context) *AuthInfo {
	if v, ok := ctx.Value(contextKey{}).(*AuthInfo); ok {
		return v
	}
	return nil
}

func Authenticate(keyStore *Store, basicUser, basicPass string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Try API key first
			if apiKey := r.Header.Get("X-API-Key"); apiKey != "" {
				result := keyStore.ValidateKey(apiKey)
				if result == nil {
					http.Error(w, `{"error":"invalid api key"}`, http.StatusUnauthorized)
					return
				}
				info := &AuthInfo{
					Method:     "apikey",
					KeyType:    result.Type,
					ProjectID:  result.ProjectID,
					Capability: result.Capability,
				}
				ctx := context.WithValue(r.Context(), contextKey{}, info)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			if cookie, err := r.Cookie(SessionCookieName); err == nil && cookie.Value != "" {
				user, err := keyStore.ValidateUserSession(cookie.Value)
				if err == nil {
					info := &AuthInfo{
						Method:     "session",
						UserID:     user.ID,
						Username:   user.Username,
						Role:       user.Role,
						ProjectIDs: user.ProjectIDs,
					}
					ctx := context.WithValue(r.Context(), contextKey{}, info)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}

			// Fall back to basic auth
			if basicUser != "" && basicPass != "" {
				user, pass, ok := r.BasicAuth()
				if ok &&
					subtle.ConstantTimeCompare([]byte(user), []byte(basicUser)) == 1 &&
					subtle.ConstantTimeCompare([]byte(pass), []byte(basicPass)) == 1 {
					info := &AuthInfo{Method: "basic"}
					ctx := context.WithValue(r.Context(), contextKey{}, info)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}

			if !strings.HasPrefix(r.URL.Path, "/api/") {
				w.Header().Set("WWW-Authenticate", `Basic realm="CrawlObserver"`)
			}
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		})
	}
}
