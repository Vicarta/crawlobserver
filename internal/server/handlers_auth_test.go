package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/SEObserver/crawlobserver/internal/apikeys"
)

type recordingEmailSender struct {
	invitations  []InvitationEmail
	codeMessages chan LoginCodeEmail
	codeBlock    <-chan struct{}
	err          error
}

func (s *recordingEmailSender) SendInvitation(_ context.Context, message InvitationEmail) error {
	s.invitations = append(s.invitations, message)
	return s.err
}

func (s *recordingEmailSender) SendLoginCode(_ context.Context, message LoginCodeEmail) error {
	if s.codeBlock != nil {
		<-s.codeBlock
	}
	if s.codeMessages != nil {
		s.codeMessages <- message
	}
	return s.err
}

func verifiedPasswordlessUser(t *testing.T, store *apikeys.Store, email string) *apikeys.User {
	t.Helper()
	user, err := store.CreatePasswordlessUser(email, apikeys.RoleViewer, nil)
	if err != nil {
		t.Fatalf("create passwordless user: %v", err)
	}
	token, _, _, err := store.IssueInvitation(user.ID)
	if err != nil {
		t.Fatalf("issue invitation: %v", err)
	}
	user, err = store.AcceptInvitation(token)
	if err != nil {
		t.Fatalf("accept invitation: %v", err)
	}
	return user
}

func TestPasswordlessCodeRequestAndVerify(t *testing.T) {
	srv, handler, store := newTestServer(t)
	srv.cfg.Server.PublicURL = "https://crawlobserver.example"
	sender := &recordingEmailSender{codeMessages: make(chan LoginCodeEmail, 1)}
	srv.emailSender = sender
	verifiedPasswordlessUser(t, store, "person@example.com")

	requestBody := `{"email":"person@example.com"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/code/request", strings.NewReader(requestBody))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("request code status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var delivered LoginCodeEmail
	select {
	case delivered = <-sender.codeMessages:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for code delivery")
	}
	if delivered.To != "person@example.com" || delivered.IdempotencyKey == "" {
		t.Fatalf("unexpected code delivery: %#v", delivered)
	}

	verifyBody := `{"email":"person@example.com","code":"` + delivered.Code + `"}`
	req = httptest.NewRequest(http.MethodPost, "/api/auth/code/verify", strings.NewReader(verifyBody))
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("verify code status = %d, body=%s", rr.Code, rr.Body.String())
	}
	cookies := rr.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookie count = %d", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != apikeys.SessionCookieName || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("unexpected auth cookie: %#v", cookie)
	}
	if remaining := time.Until(cookie.Expires); remaining < 13*24*time.Hour || remaining > 14*24*time.Hour+time.Minute {
		t.Fatalf("cookie lifetime = %s", remaining)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/auth/code/verify", strings.NewReader(verifyBody))
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("replayed code status = %d, body=%s", rr.Code, rr.Body.String())
	}
}

func TestPasswordlessCodeRequestDoesNotEnumerateAccountsOrProviderFailure(t *testing.T) {
	srv, handler, store := newTestServer(t)
	sender := &recordingEmailSender{
		codeMessages: make(chan LoginCodeEmail, 2),
		err:          errors.New("fixture delivery failure"),
	}
	srv.emailSender = sender
	verifiedPasswordlessUser(t, store, "known@example.com")

	for _, email := range []string{"known@example.com", "unknown@example.com", "not-an-email"} {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/code/request", strings.NewReader(`{"email":"`+email+`"}`))
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusAccepted {
			t.Fatalf("request for %q status = %d, body=%s", email, rr.Code, rr.Body.String())
		}
		var payload map[string]string
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil || payload["status"] != "accepted" {
			t.Fatalf("request for %q payload = %s, err=%v", email, rr.Body.String(), err)
		}
	}
	select {
	case <-sender.codeMessages:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for known-account provider call")
	}
	select {
	case message := <-sender.codeMessages:
		t.Fatalf("unexpected provider call for ineligible account: %#v", message)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestPasswordlessCodeRequestDoesNotWaitForProvider(t *testing.T) {
	srv, handler, store := newTestServer(t)
	block := make(chan struct{})
	srv.emailSender = &recordingEmailSender{codeBlock: block}
	verifiedPasswordlessUser(t, store, "timing@example.com")

	done := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/code/request", strings.NewReader(`{"email":"timing@example.com"}`))
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		done <- rr.Code
	}()
	select {
	case status := <-done:
		if status != http.StatusAccepted {
			t.Fatalf("status = %d", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("code request waited for email provider")
	}
	close(block)
}

func TestInvitationInspectAcceptAndReplay(t *testing.T) {
	srv, handler, store := newTestServer(t)
	srv.cfg.Server.PublicURL = "https://crawlobserver.example"
	sender := &recordingEmailSender{}
	srv.emailSender = sender
	user, err := store.CreatePasswordlessUser("invitee@example.com", apikeys.RoleViewer, nil)
	if err != nil {
		t.Fatalf("create passwordless user: %v", err)
	}

	req := authRequest(httptest.NewRequest(http.MethodPost, "/api/users/"+user.ID+"/invite", nil))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("invite status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if len(sender.invitations) != 1 || sender.invitations[0].IdempotencyKey == "" {
		t.Fatalf("unexpected invitation delivery: %#v", sender.invitations)
	}
	invitationURL, err := url.Parse(sender.invitations[0].InvitationURL)
	if err != nil {
		t.Fatalf("parse invitation URL: %v", err)
	}
	token := invitationURL.Query().Get("invite")
	if token == "" || invitationURL.Scheme != "https" {
		t.Fatalf("invalid invitation URL: %s", invitationURL)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/auth/invitations/"+url.PathEscape(token), nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "invitee@example.com") {
		t.Fatalf("inspect status = %d, body=%s", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/auth/invitations/"+url.PathEscape(token)+"/accept", nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || len(rr.Result().Cookies()) != 1 {
		t.Fatalf("accept status = %d, body=%s", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/auth/invitations/"+url.PathEscape(token)+"/accept", nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("replayed invitation status = %d, body=%s", rr.Code, rr.Body.String())
	}
}

func TestLegacyInteractivePasswordRouteIsRemoved(t *testing.T) {
	_, handler, _ := newTestServer(t)
	req := authRequest(httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"admin","password":"secret"}`)))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("legacy login status = %d, body=%s", rr.Code, rr.Body.String())
	}
}

func TestSessionAndAPIKeyAuthStayEnabledWithoutConfiguredBasicAuth(t *testing.T) {
	srv, _, _ := newTestServer(t)
	srv.cfg.Server.Username = ""
	srv.cfg.Server.Password = ""
	handler, err := srv.buildHandler()
	if err != nil {
		t.Fatalf("rebuild handler: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("protected route status = %d, body=%s", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/auth/code/request", strings.NewReader(`{"email":"unknown@example.com"}`))
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("public passwordless route status = %d, body=%s", rr.Code, rr.Body.String())
	}
}

func TestInvitationErrorClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "invalid", err: apikeys.ErrInvalidInvitation, want: http.StatusNotFound},
		{name: "expired", err: apikeys.ErrExpiredInvitation, want: http.StatusGone},
		{name: "used", err: apikeys.ErrUsedInvitation, want: http.StatusConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			writeInvitationError(rr, tt.err)
			if rr.Code != tt.want {
				t.Fatalf("status = %d, want %d; body=%s", rr.Code, tt.want, rr.Body.String())
			}
		})
	}
}
