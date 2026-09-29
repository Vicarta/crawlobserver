package server

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/mail"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/SEObserver/crawlobserver/internal/apikeys"
	"github.com/SEObserver/crawlobserver/internal/applog"
	"github.com/SEObserver/crawlobserver/internal/config"
	"github.com/SEObserver/crawlobserver/internal/storage"
)

const (
	operationalEmailPollInterval = 20 * time.Second
	operationalEmailScanLimit    = 50
	operationalEmailSendLimit    = 20
	operationalEmailRetryHorizon = time.Hour
	operationalEmailLease        = 2 * time.Minute
	operationalEmailOverlap      = 5 * time.Minute
)

func (s *Server) startOperationalEmailWorker() {
	if s.store == nil || s.keyStore == nil {
		return
	}
	s.operationalEmailMu.Lock()
	if s.operationalEmailCancel != nil {
		s.operationalEmailMu.Unlock()
		return
	}
	now := time.Now().UTC()
	if s.operationalEmailNow != nil {
		now = s.operationalEmailNow().UTC()
	}
	if err := s.keyStore.InitializeOperationalEmailCursor(now); err != nil {
		s.operationalEmailMu.Unlock()
		applog.Errorf("server", "Operational email worker cursor initialization failed: %v", err)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.operationalEmailCancel = cancel
	s.operationalEmailWG.Add(1)
	s.operationalEmailMu.Unlock()

	go func() {
		defer s.operationalEmailWG.Done()
		ticker := time.NewTicker(operationalEmailPollInterval)
		defer ticker.Stop()
		for {
			now := time.Now().UTC()
			if s.operationalEmailNow != nil {
				now = s.operationalEmailNow().UTC()
			}
			if err := s.runOperationalEmailTick(ctx, now); err != nil && ctx.Err() == nil {
				applog.Warnf("server", "Operational email worker pass failed: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	applog.Info("server", "Operational email notifications enabled")
}

func (s *Server) stopOperationalEmailWorker() {
	s.operationalEmailMu.Lock()
	cancel := s.operationalEmailCancel
	s.operationalEmailCancel = nil
	s.operationalEmailMu.Unlock()
	if cancel != nil {
		cancel()
		s.operationalEmailWG.Wait()
	}
}

func (s *Server) runOperationalEmailTick(ctx context.Context, now time.Time) (tickErr error) {
	if s.store == nil || s.keyStore == nil {
		return nil
	}
	defer func() {
		tickErr = errors.Join(tickErr, s.sendPendingOperationalEmails(ctx, now))
	}()
	cursor, err := s.keyStore.OperationalEmailCursor()
	if err != nil {
		return err
	}
	sessions, err := s.store.TerminalSessionsAfter(ctx, cursor.FinishedAt, cursor.SessionID, operationalEmailScanLimit)
	if err != nil {
		return err
	}
	sweepFloor := now.Add(-operationalEmailOverlap)
	sweepFloorID := ""
	if sweepFloor.Before(cursor.ActivatedAt) || sweepFloor.Equal(cursor.ActivatedAt) {
		sweepFloor = cursor.ActivatedAt
		sweepFloorID = "\uffff"
	}
	scannedInWindow, err := s.keyStore.OperationalSessionIDsScannedSince(sweepFloor)
	if err != nil {
		return err
	}
	overlapSessions, err := s.store.TerminalSessionsAfterExcluding(ctx, sweepFloor, sweepFloorID, scannedInWindow, operationalEmailScanLimit)
	if err != nil {
		return err
	}
	byID := make(map[string]storage.CrawlSession, len(sessions)+len(overlapSessions))
	mainSessionIDs := make(map[string]bool, len(sessions))
	for _, session := range sessions {
		mainSessionIDs[session.ID] = true
	}
	for _, session := range append(sessions, overlapSessions...) {
		byID[session.ID] = session
	}
	sessions = sessions[:0]
	for _, session := range byID {
		sessions = append(sessions, session)
	}
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].FinishedAt.Equal(sessions[j].FinishedAt) {
			return sessions[i].ID < sessions[j].ID
		}
		return sessions[i].FinishedAt.Before(sessions[j].FinishedAt)
	})
	var admins []apikeys.User
	if len(sessions) > 0 {
		admins, err = s.eligibleOperationalEmailAdmins()
		if err != nil {
			return err
		}
	}
	scannedIDs := make(map[string]struct{}, len(scannedInWindow))
	for _, sessionID := range scannedInWindow {
		scannedIDs[sessionID] = struct{}{}
	}
	for _, session := range sessions {
		_, scanned := scannedIDs[session.ID]
		if !scanned {
			scanned, err = s.keyStore.OperationalSessionScanned(session.ID)
			if err != nil {
				return err
			}
		}
		if scanned {
			if mainSessionIDs[session.ID] {
				if _, err := s.keyStore.RecordOperationalSession(session.ID, session.FinishedAt, nil, now, true); err != nil {
					return err
				}
			}
			continue
		}
		events, err := s.operationalEmailEvents(ctx, session, admins)
		if err != nil {
			return err
		}
		if _, err := s.keyStore.RecordOperationalSession(session.ID, session.FinishedAt, events, now, mainSessionIDs[session.ID]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) operationalEmailEvents(ctx context.Context, session storage.CrawlSession, admins []apikeys.User) ([]apikeys.OperationalEmailEvent, error) {
	projectID := ""
	projectName := "Unassigned project"
	if session.ProjectID != nil {
		projectID = *session.ProjectID
		projectName = "Project " + projectID
		if project, err := s.keyStore.GetProject(projectID); err == nil && strings.TrimSpace(project.Name) != "" {
			projectName = project.Name
		}
	}
	var events []apikeys.OperationalEmailEvent
	if reason := crawlFailureReason(session); reason != "" {
		statusLabel := "Crawl failed"
		if session.Status == "completed_with_errors" {
			statusLabel = "Crawl completed with errors"
		} else if session.Status == "stopped" {
			statusLabel = "Crawl stopped unexpectedly"
		}
		events = append(events, operationalEmailEvent(session, projectID, projectName, "crawl_failure", statusLabel, 0, nil, admins))
	}
	if session.Status != "completed" && session.Status != "completed_with_errors" {
		return events, nil
	}
	if session.ProjectID == nil || strings.TrimSpace(*session.ProjectID) == "" {
		return events, nil
	}
	newErrors, err := s.store.NewPageErrorsForSession(ctx, session)
	if err != nil {
		return nil, err
	}
	if len(newErrors) == 0 {
		return events, nil
	}
	details := make([]string, 0, min(len(newErrors), 10))
	for _, pageError := range newErrors[:min(len(newErrors), 10)] {
		details = append(details, fmt.Sprintf("%s: %s", pageErrorDescription(pageError.StatusCode, pageError.FetchError), safeEmailPageURL(pageError.URL)))
	}
	if len(newErrors) > len(details) {
		details = append(details, fmt.Sprintf("and %d more", len(newErrors)-len(details)))
	}
	summary := fmt.Sprintf("%d new page error(s)", len(newErrors))
	events = append(events, operationalEmailEvent(session, projectID, projectName, "new_page_errors", summary, len(newErrors), details, admins))
	return events, nil
}

func operationalEmailEvent(session storage.CrawlSession, projectID, projectName, eventType, summary string, errorCount int, details []string, admins []apikeys.User) apikeys.OperationalEmailEvent {
	event := apikeys.OperationalEmailEvent{
		SessionID:     session.ID,
		ProjectID:     projectID,
		ProjectName:   projectName,
		SessionStatus: session.Status,
		FinishedAt:    session.FinishedAt,
		Type:          eventType,
		Summary:       summary,
		ErrorCount:    errorCount,
		Details:       details,
	}
	for _, admin := range admins {
		event.Recipients = append(event.Recipients, apikeys.OperationalEmailRecipient{UserID: admin.ID, Address: admin.Email})
	}
	if len(event.Recipients) == 0 {
		event.SkipReason = "no_verified_admin_recipient"
	}
	return event
}

func crawlFailureReason(session storage.CrawlSession) string {
	switch session.Status {
	case "failed", "crashed", "completed_with_errors":
		return session.Status
	case "stopped":
		meta, ok := config.SessionStopMetadataFromJSON(session.Config)
		if ok && meta.Reason != "" && !strings.EqualFold(meta.Reason, "manual") {
			return meta.Reason
		}
	}
	return ""
}

func pageErrorDescription(statusCode uint16, fetchError bool) string {
	if statusCode == 0 || fetchError {
		return "Fetch failed"
	}
	return fmt.Sprintf("HTTP %d", statusCode)
}

func safeEmailPageURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return "[URL omitted]"
	}
	return strings.ToLower(u.Scheme) + "://" + u.Host
}

func (s *Server) eligibleOperationalEmailAdmins() ([]apikeys.User, error) {
	if s.keyStore == nil {
		return nil, nil
	}
	users, err := s.keyStore.ListEmailNotificationUsers()
	if err != nil {
		return nil, err
	}
	admins := make([]apikeys.User, 0, len(users))
	for _, user := range users {
		if !eligibleOperationalEmailAdmin(user) {
			continue
		}
		user.Email = strings.ToLower(strings.TrimSpace(user.Email))
		admins = append(admins, user)
	}
	sort.Slice(admins, func(i, j int) bool { return admins[i].ID < admins[j].ID })
	return admins, nil
}

func eligibleOperationalEmailAdmin(user apikeys.User) bool {
	if !user.Active || user.Role != apikeys.RoleAdmin || user.EmailVerifiedAt == nil {
		return false
	}
	email := strings.TrimSpace(user.Email)
	parsed, err := mail.ParseAddress(email)
	return err == nil && parsed.Address == email && email != ""
}

func (s *Server) sendPendingOperationalEmails(ctx context.Context, now time.Time) error {
	if s.keyStore == nil {
		return nil
	}
	receipts, err := s.keyStore.ClaimOperationalEmailReceipts(
		now,
		operationalEmailSendLimit,
		operationalEmailRetryHorizon,
		operationalEmailLease,
	)
	if err != nil {
		return err
	}
	admins, err := s.eligibleOperationalEmailAdmins()
	if err != nil && len(receipts) > 0 {
		return err
	}
	eligibleByID := make(map[string]apikeys.User, len(admins))
	for _, admin := range admins {
		eligibleByID[admin.ID] = admin
	}
	sender, canSend := s.emailSender.(OperationalEmailSender)
	for _, receipt := range receipts {
		if admin, ok := eligibleByID[receipt.UserID]; !ok || strings.ToLower(strings.TrimSpace(admin.Email)) != strings.ToLower(strings.TrimSpace(receipt.Recipient)) {
			if err := s.keyStore.FinishOperationalEmailReceipt(receipt.ID, apikeys.OperationalEmailSkipped, "recipient_no_longer_eligible", now, nil); err != nil {
				return err
			}
			continue
		}
		if !canSend {
			if err := s.keyStore.FinishOperationalEmailReceipt(receipt.ID, apikeys.OperationalEmailSkipped, "resend_not_configured", now, nil); err != nil {
				return err
			}
			continue
		}
		message := s.operationalAlertMessage(receipt)
		message.To = receipt.Recipient
		message.IdempotencyKey = receipt.IdempotencyKey
		sendCtx, cancel := context.WithTimeout(ctx, resendRequestTimeout)
		sendErr := sender.SendOperationalAlert(sendCtx, message)
		cancel()
		if sendErr == nil {
			if err := s.keyStore.FinishOperationalEmailReceipt(receipt.ID, apikeys.OperationalEmailAccepted, "provider_accepted", now, nil); err != nil {
				return err
			}
			continue
		}
		if errors.Is(sendErr, ErrEmailDeliveryUnavailable) {
			if err := s.keyStore.FinishOperationalEmailReceipt(receipt.ID, apikeys.OperationalEmailSkipped, "resend_not_configured", now, nil); err != nil {
				return err
			}
			continue
		}
		status := apikeys.OperationalEmailPending
		var nextAttempt *time.Time
		if receipt.Attempts >= 3 {
			status = apikeys.OperationalEmailFailed
		} else {
			next := now.Add(operationalEmailRetryDelay(receipt.Attempts))
			nextAttempt = &next
		}
		if err := s.keyStore.FinishOperationalEmailReceipt(receipt.ID, status, "email_delivery_failed", now, nextAttempt); err != nil {
			return err
		}
	}
	return nil
}

func operationalEmailRetryDelay(attempt int) time.Duration {
	if attempt <= 1 {
		return time.Minute
	}
	return 5 * time.Minute
}

func (s *Server) operationalAlertMessage(receipt apikeys.OperationalEmailReceipt) OperationalAlertEmail {
	link := "/sessions/" + url.PathEscape(receipt.SessionID) + "/pages"
	if s.cfg != nil {
		base := strings.TrimRight(strings.TrimSpace(s.cfg.Server.PublicURL), "/")
		if base != "" {
			link = base + link
		}
	}
	projectLabel := receipt.ProjectName
	if projectLabel == "" {
		projectLabel = "Project " + receipt.ProjectID
	}
	subject := fmt.Sprintf("CrawlObserver: %s crawl failure (%s)", projectLabel, receipt.SessionStatus)
	text := fmt.Sprintf("%s\nProject: %s\nSession: %s\nTerminal status: %s\nOpen session: %s\n", receipt.Summary, projectLabel, receipt.SessionID, receipt.SessionStatus, link)
	if receipt.Type == "new_page_errors" {
		subject = fmt.Sprintf("CrawlObserver: %s - %s", projectLabel, receipt.Summary)
		text = fmt.Sprintf("%s\nProject: %s\nSession: %s\nTerminal status: %s\nOpen session: %s\n\n%s\n", receipt.Summary, projectLabel, receipt.SessionID, receipt.SessionStatus, link, strings.Join(receipt.Details, "\n"))
	}
	var htmlDetails strings.Builder
	for _, detail := range receipt.Details {
		htmlDetails.WriteString("<li>")
		htmlDetails.WriteString(html.EscapeString(detail))
		htmlDetails.WriteString("</li>")
	}
	htmlBody := "<p>" + html.EscapeString(receipt.Summary) + "</p><p>Project: " + html.EscapeString(projectLabel) + "</p><p>Session: " + html.EscapeString(receipt.SessionID) + "</p><p>Terminal status: " + html.EscapeString(receipt.SessionStatus) + "</p>"
	if receipt.Type == "new_page_errors" && htmlDetails.Len() > 0 {
		htmlBody += "<ul>" + htmlDetails.String() + "</ul>"
	}
	htmlBody += "<p><a href=\"" + html.EscapeString(link) + "\">Open session</a></p>"
	return OperationalAlertEmail{Subject: subject, Text: text, HTML: htmlBody}
}

func (s *Server) operationalEmailStatus() map[string]interface{} {
	status := map[string]interface{}{
		"worker_running":        false,
		"resend_configured":     s.cfg != nil && strings.TrimSpace(s.cfg.Resend.APIKey) != "" && strings.TrimSpace(s.cfg.Resend.From) != "",
		"eligible_admin_count":  0,
		"eligibility_status":    "unavailable",
		"activated_at":          nil,
		"retry_horizon_minutes": int(operationalEmailRetryHorizon.Minutes()),
	}
	s.operationalEmailMu.Lock()
	status["worker_running"] = s.operationalEmailCancel != nil
	s.operationalEmailMu.Unlock()
	if admins, err := s.eligibleOperationalEmailAdmins(); err == nil {
		status["eligible_admin_count"] = len(admins)
		status["eligibility_status"] = "available"
	}
	if s.keyStore != nil {
		if cursor, err := s.keyStore.OperationalEmailCursor(); err == nil {
			status["activated_at"] = cursor.ActivatedAt
		}
	}
	return status
}
