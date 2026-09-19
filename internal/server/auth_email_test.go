package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/SEObserver/crawlobserver/internal/config"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}

func resendTestResponse(statusCode int, body string) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func TestResendEmailSenderSendsInvitation(t *testing.T) {
	const (
		apiKey        = "re_test_secret"
		idempotency   = "invitation-123"
		invitationURL = "https://crawlobserver.example.com/invitations/accept?token=test-token"
	)

	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/emails" {
			t.Fatalf("request = %s %s, want POST /emails", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+apiKey {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != resendUserAgent {
			t.Errorf("User-Agent = %q", got)
		}
		if got := r.Header.Get("Idempotency-Key"); got != idempotency {
			t.Errorf("Idempotency-Key = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}

		var payload resendEmailRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload.From != "CrawlObserver <auth@example.com>" {
			t.Errorf("From = %q", payload.From)
		}
		if len(payload.To) != 1 || payload.To[0] != "user@example.com" {
			t.Errorf("To = %#v", payload.To)
		}
		if payload.Subject != "You're invited to CrawlObserver" {
			t.Errorf("Subject = %q", payload.Subject)
		}
		if !strings.Contains(payload.Text, invitationURL) || !strings.Contains(payload.HTML, invitationURL) {
			t.Errorf("invitation URL missing from payload: %#v", payload)
		}
		if !strings.Contains(payload.Text, "7 days") || !strings.Contains(payload.HTML, "7 days") {
			t.Errorf("invitation expiry missing from payload: %#v", payload)
		}
		return resendTestResponse(http.StatusOK, `{"id":"email-123"}`), nil
	})}

	sender := newResendEmailSender(config.ResendConfig{
		APIKey: apiKey,
		From:   "CrawlObserver <auth@example.com>",
	}, client, resendEmailsEndpoint)
	if err := sender.SendInvitation(context.Background(), InvitationEmail{
		To:             "user@example.com",
		InvitationURL:  invitationURL,
		IdempotencyKey: idempotency,
	}); err != nil {
		t.Fatalf("SendInvitation() error = %v", err)
	}
}

func TestResendEmailSenderSendsLoginCode(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var payload resendEmailRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload.Subject != "Your CrawlObserver login code" {
			t.Errorf("Subject = %q", payload.Subject)
		}
		if !strings.Contains(payload.Text, "123456") || !strings.Contains(payload.HTML, "123456") {
			t.Errorf("login code missing from payload: %#v", payload)
		}
		if !strings.Contains(payload.Text, "15 minutes") || !strings.Contains(payload.HTML, "15 minutes") {
			t.Errorf("login code expiry missing from payload: %#v", payload)
		}
		return resendTestResponse(http.StatusOK, `{"id":"email-456"}`), nil
	})}

	sender := newResendEmailSender(config.ResendConfig{APIKey: "re_test", From: "auth@example.com"}, client, resendEmailsEndpoint)
	if err := sender.SendLoginCode(context.Background(), LoginCodeEmail{
		To:             "user@example.com",
		Code:           "123456",
		IdempotencyKey: "challenge-456",
	}); err != nil {
		t.Fatalf("SendLoginCode() error = %v", err)
	}
}

func TestResendEmailSenderRedactsProviderFailure(t *testing.T) {
	const (
		apiKey          = "re_test_secret"
		providerMessage = "provider-secret-response"
		loginCode       = "123456"
	)
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return resendTestResponse(http.StatusBadRequest, `{"message":"`+providerMessage+`"}`), nil
	})}

	sender := newResendEmailSender(config.ResendConfig{APIKey: apiKey, From: "auth@example.com"}, client, resendEmailsEndpoint)
	err := sender.SendLoginCode(context.Background(), LoginCodeEmail{
		To:             "user@example.com",
		Code:           loginCode,
		IdempotencyKey: "challenge-789",
	})
	if !errors.Is(err, ErrEmailDelivery) {
		t.Fatalf("SendLoginCode() error = %v, want ErrEmailDelivery", err)
	}
	for _, secret := range []string{apiKey, providerMessage, loginCode, "user@example.com"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error exposed %q: %v", secret, err)
		}
	}
	if !strings.Contains(err.Error(), "HTTP 400") {
		t.Errorf("error = %v, want HTTP status", err)
	}
}

func TestResendEmailSenderRejectsMalformedSuccess(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return resendTestResponse(http.StatusOK, `{"message":"not an email id"}`), nil
	})}

	sender := newResendEmailSender(config.ResendConfig{APIKey: "re_test", From: "auth@example.com"}, client, resendEmailsEndpoint)
	err := sender.SendLoginCode(context.Background(), LoginCodeEmail{
		To:             "user@example.com",
		Code:           "123456",
		IdempotencyKey: "challenge-789",
	})
	if !errors.Is(err, ErrEmailDelivery) || !strings.Contains(err.Error(), "invalid success response") {
		t.Fatalf("SendLoginCode() error = %v, want malformed response failure", err)
	}
}

func TestResendEmailSenderTimeoutIsRedacted(t *testing.T) {
	client := &http.Client{Timeout: 10 * time.Millisecond, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}

	sender := newResendEmailSender(
		config.ResendConfig{APIKey: "re_timeout_secret", From: "auth@example.com"},
		client,
		resendEmailsEndpoint,
	)
	err := sender.SendLoginCode(context.Background(), LoginCodeEmail{
		To:             "user@example.com",
		Code:           "123456",
		IdempotencyKey: "challenge-timeout",
	})
	if !errors.Is(err, ErrEmailDelivery) || err.Error() != "email delivery failed: request failed" {
		t.Fatalf("SendLoginCode() error = %v, want redacted timeout error", err)
	}
}

func TestResendEmailSenderWithoutConfigurationDoesNotSend(t *testing.T) {
	sender := NewResendEmailSender(config.ResendConfig{})
	err := sender.SendInvitation(context.Background(), InvitationEmail{
		To:             "user@example.com",
		InvitationURL:  "https://crawlobserver.example.com/invitations/accept?token=test-token",
		IdempotencyKey: "invitation-disabled",
	})
	if !errors.Is(err, ErrEmailDeliveryUnavailable) {
		t.Fatalf("SendInvitation() error = %v, want ErrEmailDeliveryUnavailable", err)
	}
}
