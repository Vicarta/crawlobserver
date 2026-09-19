package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/SEObserver/crawlobserver/internal/config"
)

const (
	resendEmailsEndpoint    = "https://api.resend.com/emails"
	resendRequestTimeout    = 10 * time.Second
	resendUserAgent         = "CrawlObserver/1.0 (+auth-email)"
	resendMaxResponseBytes  = 64 * 1024
	resendMaxIdempotencyKey = 256
)

var (
	// ErrEmailDelivery identifies a delivery failure whose details are safe to
	// return or log without exposing provider response content.
	ErrEmailDelivery = errors.New("email delivery failed")
	// ErrEmailDeliveryUnavailable means Resend is not configured. The disabled
	// sender never creates an outbound request.
	ErrEmailDeliveryUnavailable = fmt.Errorf("%w: unavailable", ErrEmailDelivery)
)

// EmailSender is the passwordless-auth delivery seam. Tests and callers can
// substitute it with a recording fake without contacting Resend.
type EmailSender interface {
	SendInvitation(context.Context, InvitationEmail) error
	SendLoginCode(context.Context, LoginCodeEmail) error
}

// InvitationEmail is the complete invitation delivery request. IdempotencyKey
// must be an opaque, stable invitation identifier rather than its secret token.
type InvitationEmail struct {
	To             string
	InvitationURL  string
	IdempotencyKey string
}

// LoginCodeEmail is the complete login-code delivery request. IdempotencyKey
// must be an opaque, stable challenge identifier rather than the login code.
type LoginCodeEmail struct {
	To             string
	Code           string
	IdempotencyKey string
}

type resendEmailSender struct {
	apiKey   string
	from     string
	endpoint string
	client   *http.Client
}

type disabledEmailSender struct{}

type resendEmailRequest struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	Text    string   `json:"text"`
	HTML    string   `json:"html"`
}

type resendEmailResponse struct {
	ID string `json:"id"`
}

// NewResendEmailSender builds the production email sender. Missing Resend
// configuration deliberately produces a disabled sender so server startup and
// local tests never attempt network delivery.
func NewResendEmailSender(cfg config.ResendConfig) EmailSender {
	return newResendEmailSender(cfg, nil, resendEmailsEndpoint)
}

func newResendEmailSender(cfg config.ResendConfig, client *http.Client, endpoint string) EmailSender {
	apiKey := strings.TrimSpace(cfg.APIKey)
	from := strings.TrimSpace(cfg.From)
	if apiKey == "" || from == "" {
		return disabledEmailSender{}
	}
	if client == nil {
		client = &http.Client{Timeout: resendRequestTimeout}
	}
	return &resendEmailSender{
		apiKey:   apiKey,
		from:     from,
		endpoint: endpoint,
		client:   client,
	}
}

func (disabledEmailSender) SendInvitation(context.Context, InvitationEmail) error {
	return ErrEmailDeliveryUnavailable
}

func (disabledEmailSender) SendLoginCode(context.Context, LoginCodeEmail) error {
	return ErrEmailDeliveryUnavailable
}

func (s *resendEmailSender) SendInvitation(ctx context.Context, message InvitationEmail) error {
	invitationURL := strings.TrimSpace(message.InvitationURL)
	if invitationURL == "" {
		return emailDeliveryError("invalid invitation message")
	}

	return s.send(ctx, message.To, message.IdempotencyKey, resendEmailRequest{
		From:    s.from,
		Subject: "You're invited to CrawlObserver",
		Text: "You're invited to CrawlObserver.\n\n" +
			"Open this link to accept your invitation:\n" + invitationURL + "\n\n" +
			"This invitation expires in 7 days.",
		HTML: "<p>You're invited to CrawlObserver.</p><p><a href=\"" + html.EscapeString(invitationURL) +
			"\">Accept your invitation</a></p><p>This invitation expires in 7 days.</p>",
	})
}

func (s *resendEmailSender) SendLoginCode(ctx context.Context, message LoginCodeEmail) error {
	code := strings.TrimSpace(message.Code)
	if code == "" {
		return emailDeliveryError("invalid login code message")
	}

	return s.send(ctx, message.To, message.IdempotencyKey, resendEmailRequest{
		From:    s.from,
		Subject: "Your CrawlObserver login code",
		Text: "Your CrawlObserver login code is: " + code + "\n\n" +
			"This code expires in 15 minutes.",
		HTML: "<p>Your CrawlObserver login code is:</p><p><strong>" + html.EscapeString(code) +
			"</strong></p><p>This code expires in 15 minutes.</p>",
	})
}

func (s *resendEmailSender) send(ctx context.Context, recipient, idempotencyKey string, payload resendEmailRequest) error {
	recipient = strings.TrimSpace(recipient)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if recipient == "" || idempotencyKey == "" || len(idempotencyKey) > resendMaxIdempotencyKey {
		return emailDeliveryError("invalid email message")
	}
	payload.To = []string{recipient}

	body, err := json.Marshal(payload)
	if err != nil {
		return emailDeliveryError("request encoding failed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return emailDeliveryError("request creation failed")
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", resendUserAgent)
	req.Header.Set("Idempotency-Key", idempotencyKey)

	resp, err := s.client.Do(req)
	if err != nil {
		return emailDeliveryError("request failed")
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, resendMaxResponseBytes))
	if err != nil {
		return emailDeliveryError("response read failed")
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return emailDeliveryError(fmt.Sprintf("service returned HTTP %d", resp.StatusCode))
	}

	var response resendEmailResponse
	if err := json.Unmarshal(responseBody, &response); err != nil || strings.TrimSpace(response.ID) == "" {
		return emailDeliveryError("invalid success response")
	}
	return nil
}

func emailDeliveryError(reason string) error {
	return fmt.Errorf("%w: %s", ErrEmailDelivery, reason)
}
