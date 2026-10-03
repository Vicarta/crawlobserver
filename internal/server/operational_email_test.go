package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SEObserver/crawlobserver/internal/apikeys"
	"github.com/SEObserver/crawlobserver/internal/applog"
	"github.com/SEObserver/crawlobserver/internal/config"
	"github.com/SEObserver/crawlobserver/internal/storage"
)

type recordingOperationalSender struct {
	messages []OperationalAlertEmail
	err      error
}

type operationalEvidenceStore struct {
	*mockStore
	evidenceBySession map[string]*storage.PageRankEvidence
}

func (s *operationalEvidenceStore) LatestPageRankEvidence(_ context.Context, sessionID string) (*storage.PageRankEvidence, error) {
	if evidence := s.evidenceBySession[sessionID]; evidence != nil {
		return evidence, nil
	}
	return nil, storage.ErrNoFinalizedPageRankEvidence
}

func (s *recordingOperationalSender) SendOperationalAlert(_ context.Context, message OperationalAlertEmail) error {
	s.messages = append(s.messages, message)
	return s.err
}

func (s *recordingOperationalSender) SendInvitation(context.Context, InvitationEmail) error {
	return nil
}

func (s *recordingOperationalSender) SendLoginCode(context.Context, LoginCodeEmail) error {
	return nil
}

func verifiedUserWithRole(t *testing.T, store *apikeys.Store, email, role string, projectIDs []string) *apikeys.User {
	t.Helper()
	user, err := store.CreatePasswordlessUser(email, role, projectIDs)
	if err != nil {
		t.Fatalf("create passwordless %s: %v", role, err)
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

func operationalProject(t *testing.T, store *apikeys.Store, name string) *apikeys.Project {
	t.Helper()
	project, err := store.CreateProject(name)
	if err != nil {
		t.Fatalf("create project %q: %v", name, err)
	}
	return project
}

func TestOperationalEmailWorkerSendsDurableAlertsOnceAcrossProjects(t *testing.T) {
	srv, _, keyStore := newTestServer(t)
	projectA := operationalProject(t, keyStore, "DiskInternals")
	projectB := operationalProject(t, keyStore, "Astrogen")
	admin := verifiedUserWithRole(t, keyStore, "admin@example.test", apikeys.RoleAdmin, []string{projectA.ID})
	verifiedUserWithRole(t, keyStore, "viewer@example.test", apikeys.RoleViewer, []string{projectA.ID})
	srv.cfg.Resend.APIKey = "test-key"
	srv.cfg.Resend.From = "alerts@example.test"
	srv.cfg.Server.PublicURL = "https://crawlobserver.example"
	sender := &recordingOperationalSender{}
	srv.emailSender = sender

	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	if err := keyStore.InitializeOperationalEmailCursor(now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	sessionA := storage.CrawlSession{ID: "delta-a", Status: "completed_with_errors", FinishedAt: now.Add(-2 * time.Minute), ProjectID: &projectA.ID}
	sessionB := storage.CrawlSession{ID: "delta-b", Status: "completed_with_errors", FinishedAt: now.Add(-time.Minute), ProjectID: &projectB.ID}
	ms := srv.store.(*mockStore)
	ms.terminalSessions = []storage.CrawlSession{sessionA, sessionB}
	ms.pageErrorsBySession = map[string][]storage.PageErrorObservation{
		sessionA.ID: {{URL: "https://www.diskinternals.com/guides/new?token=do-not-email", StatusCode: 404}},
		sessionB.ID: {{URL: "https://astrogen.example/pricing", StatusCode: 0, FetchError: true}},
	}

	if err := srv.runOperationalEmailTick(context.Background(), now); err != nil {
		t.Fatalf("first worker pass: %v", err)
	}
	if len(sender.messages) != 4 {
		t.Fatalf("provider calls = %d; want two alerts for each project, got %#v", len(sender.messages), sender.messages)
	}
	for _, message := range sender.messages {
		if message.To != admin.Email || message.IdempotencyKey == "" {
			t.Fatalf("message recipient/key = %q / %q", message.To, message.IdempotencyKey)
		}
		if strings.Contains(message.Text, "do-not-email") {
			t.Fatalf("query secret leaked in email: %s", message.Text)
		}
		if !strings.Contains(message.Text, "Terminal status: completed_with_errors") || !strings.Contains(message.Text, "/sessions/") {
			t.Fatalf("missing status/session link: %s", message.Text)
		}
	}
	if !containsEmailText(sender.messages, "DiskInternals") || !containsEmailText(sender.messages, "Astrogen") {
		t.Fatalf("multi-project email lacks project names: %#v", sender.messages)
	}
	if err := srv.runOperationalEmailTick(context.Background(), now.Add(time.Second)); err != nil {
		t.Fatalf("replayed worker pass: %v", err)
	}
	if len(sender.messages) != 4 {
		t.Fatalf("session replay sent duplicates: %d provider calls", len(sender.messages))
	}
	receipts, total, err := keyStore.ListOperationalEmailReceipts(20, 0)
	if err != nil || total != 4 || len(receipts) != 4 {
		t.Fatalf("receipt history total=%d rows=%d err=%v", total, len(receipts), err)
	}
	for _, receipt := range receipts {
		if receipt.UserID != admin.ID || receipt.Status != apikeys.OperationalEmailAccepted {
			t.Fatalf("receipt was not accepted for global admin: %#v", receipt)
		}
	}
}

func TestOperationalEmailPendingReceiptSendsWhenSessionScanFails(t *testing.T) {
	srv, _, keyStore := newTestServer(t)
	admin := verifiedUserWithRole(t, keyStore, "admin@example.test", apikeys.RoleAdmin, nil)
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	if err := keyStore.InitializeOperationalEmailCursor(now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	event := apikeys.OperationalEmailEvent{
		SessionID: "pending-before-scan", ProjectID: "project", ProjectName: "Project",
		SessionStatus: "failed", FinishedAt: now.Add(-time.Minute), Type: "crawl_failure", Summary: "Crawl failed",
		Recipients: []apikeys.OperationalEmailRecipient{{UserID: admin.ID, Address: admin.Email}},
	}
	if _, err := keyStore.RecordOperationalSession(event.SessionID, event.FinishedAt, []apikeys.OperationalEmailEvent{event}, now, false); err != nil {
		t.Fatal(err)
	}
	sender := &recordingOperationalSender{}
	srv.emailSender = sender
	scanErr := errors.New("ClickHouse unavailable")
	srv.store.(*mockStore).err = scanErr
	if err := srv.runOperationalEmailTick(context.Background(), now); !errors.Is(err, scanErr) {
		t.Fatalf("worker error = %v; want scan error %v", err, scanErr)
	}
	if len(sender.messages) != 1 || sender.messages[0].To != admin.Email {
		t.Fatalf("pending receipt was not sent despite scan failure: %#v", sender.messages)
	}
	receipts, _, err := keyStore.ListOperationalEmailReceipts(10, 0)
	if err != nil || len(receipts) != 1 || receipts[0].Status != apikeys.OperationalEmailAccepted {
		t.Fatalf("receipt after independent send = %#v, err=%v", receipts, err)
	}
}

func TestOperationalEmailOverlapFindsLateVisibleSessionAfterWatermarkAdvance(t *testing.T) {
	srv, _, keyStore := newTestServer(t)
	project := operationalProject(t, keyStore, "Late visibility")
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	if err := keyStore.InitializeOperationalEmailCursor(now.Add(-10 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	newer := storage.CrawlSession{ID: "newer", Status: "completed", FinishedAt: now.Add(-2 * time.Minute), ProjectID: &project.ID}
	ms := srv.store.(*mockStore)
	ms.terminalSessions = []storage.CrawlSession{newer}
	if err := srv.runOperationalEmailTick(context.Background(), now); err != nil {
		t.Fatalf("first tick: %v", err)
	}

	late := storage.CrawlSession{ID: "late-visible", Status: "completed", FinishedAt: now.Add(-3 * time.Minute), ProjectID: &project.ID}
	ms.terminalSessions = append(ms.terminalSessions, late)
	if err := srv.runOperationalEmailTick(context.Background(), now.Add(20*time.Second)); err != nil {
		t.Fatalf("tick after late visibility: %v", err)
	}
	for _, id := range []string{newer.ID, late.ID} {
		scanned, err := keyStore.OperationalSessionScanned(id)
		if err != nil || !scanned {
			t.Fatalf("session %q scan marker = %v, %v; want true, nil", id, scanned, err)
		}
	}
	if got := len(ms.pageErrorsBySession); got != 0 {
		t.Fatalf("fixture unexpectedly included page errors: %d sessions", got)
	}
}

func TestOperationalEmailOverlapPaginatesPastFiftyLateSessions(t *testing.T) {
	srv, _, keyStore := newTestServer(t)
	project := operationalProject(t, keyStore, "Overlap pagination")
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	if err := keyStore.InitializeOperationalEmailCursor(now.Add(-20 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	ms := srv.store.(*mockStore)
	oldSessions := make([]storage.CrawlSession, 0, 150)
	oldBase := now.Add(-15 * time.Minute)
	for i := 0; i < 150; i++ {
		oldSessions = append(oldSessions, storage.CrawlSession{
			ID: fmt.Sprintf("older-%03d", i), Status: "completed", FinishedAt: oldBase.Add(time.Duration(i) * time.Second), ProjectID: &project.ID,
		})
	}
	ms.terminalSessions = oldSessions
	if err := srv.runOperationalEmailTick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	newer := storage.CrawlSession{ID: "newer-overlap", Status: "completed", FinishedAt: now.Add(-time.Minute), ProjectID: &project.ID}
	ms.terminalSessions = append(ms.terminalSessions, newer)
	lateSessions := make([]storage.CrawlSession, 0, 55)
	for i := 0; i < 55; i++ {
		lateSessions = append(lateSessions, storage.CrawlSession{
			ID: fmt.Sprintf("late-%03d", i), Status: "completed", FinishedAt: now.Add(-3 * time.Minute), ProjectID: &project.ID,
		})
	}
	ms.terminalSessions = append(ms.terminalSessions, lateSessions...)
	if err := srv.runOperationalEmailTick(context.Background(), now.Add(20*time.Second)); err != nil {
		t.Fatalf("first overlap page: %v", err)
	}
	lateBehindPriorPage := storage.CrawlSession{ID: "late-behind-prior-page", Status: "completed", FinishedAt: now.Add(-4 * time.Minute), ProjectID: &project.ID}
	ms.terminalSessions = append(ms.terminalSessions, lateBehindPriorPage)
	for _, tick := range []time.Time{now.Add(40 * time.Second), now.Add(time.Minute), now.Add(80 * time.Second)} {
		if err := srv.runOperationalEmailTick(context.Background(), tick); err != nil {
			t.Fatalf("overlap pagination tick at %s: %v", tick, err)
		}
	}
	cursor, err := keyStore.OperationalEmailCursor()
	if err != nil {
		t.Fatal(err)
	}
	if cursor.SessionID != newer.ID {
		t.Fatalf("main cursor = %q; want eventual main-query advance to %q", cursor.SessionID, newer.ID)
	}
	for _, session := range append(append(oldSessions, lateSessions...), lateBehindPriorPage, newer) {
		scanned, err := keyStore.OperationalSessionScanned(session.ID)
		if err != nil || !scanned {
			t.Fatalf("session %q scan marker = %v, %v; want true, nil", session.ID, scanned, err)
		}
		if calls := ms.pageErrorCalls[session.ID]; calls != 1 {
			t.Fatalf("session %q page-error comparisons = %d; want once", session.ID, calls)
		}
	}
}

func TestOperationalEmailProjectlessErrorsOnlySendExecutionFailure(t *testing.T) {
	srv, _, keyStore := newTestServer(t)
	verifiedUserWithRole(t, keyStore, "admin@example.test", apikeys.RoleAdmin, nil)
	sender := &recordingOperationalSender{}
	srv.emailSender = sender
	now := time.Now().UTC()
	if err := keyStore.InitializeOperationalEmailCursor(now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	session := storage.CrawlSession{ID: "projectless", Status: "completed_with_errors", FinishedAt: now.Add(-time.Minute)}
	srv.store.(*mockStore).terminalSessions = []storage.CrawlSession{session}
	srv.store.(*mockStore).pageErrorsBySession = map[string][]storage.PageErrorObservation{
		session.ID: {{URL: "https://example.test/broken", StatusCode: 404}},
	}
	if err := srv.runOperationalEmailTick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 1 || !strings.Contains(sender.messages[0].Text, "completed_with_errors") {
		t.Fatalf("projectless crawl failure notifications = %#v; want only execution failure", sender.messages)
	}
	receipts, total, err := keyStore.ListOperationalEmailReceipts(10, 0)
	if err != nil || total != 1 || len(receipts) != 1 || receipts[0].Type != "crawl_failure" {
		t.Fatalf("projectless receipts = %#v, total=%d, err=%v; want execution only", receipts, total, err)
	}
}

func TestOperationalEmailFailureIncludesLegacyExecutionCauseAndObservedPageURL(t *testing.T) {
	srv, _, keyStore := newTestServer(t)
	project := operationalProject(t, keyStore, "DiskInternals")
	verifiedUserWithRole(t, keyStore, "admin@example.test", apikeys.RoleAdmin, nil)
	srv.cfg.Resend.APIKey = "test-key"
	srv.cfg.Resend.From = "alerts@example.test"
	srv.cfg.Server.PublicURL = "https://crawlobserver.example"
	sender := &recordingOperationalSender{}
	srv.emailSender = sender

	sessionID := "0ac4a42d-ea3d-4e0e-87f8-4e2aea553be5"
	finishedAt := time.Date(2026, 10, 3, 12, 6, 53, 0, time.UTC)
	session := storage.CrawlSession{ID: sessionID, Status: "completed_with_errors", FinishedAt: finishedAt, ProjectID: &project.ID}
	ms := srv.store.(*mockStore)
	ms.terminalSessions = []storage.CrawlSession{session}
	pageURL := "https://de.diskinternals.com/guides/features/"
	ms.pageErrorsBySession = map[string][]storage.PageErrorObservation{
		sessionID: {{URL: pageURL, StatusCode: 404}},
	}
	ms.operationalErrorLogs = []applog.LogRow{{
		Timestamp: finishedAt.Add(time.Second), Level: "error", Component: "crawler",
		Message: "Crawl " + sessionID + " failed: computing PageRank: reading pagerank graph pages: context deadline exceeded",
	}}
	if err := keyStore.InitializeOperationalEmailCursor(finishedAt.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := srv.runOperationalEmailTick(context.Background(), finishedAt.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 2 {
		t.Fatalf("sent %d operational messages, want crawl failure and new page error", len(sender.messages))
	}
	var failureMessage, pageErrorMessage *OperationalAlertEmail
	for i := range sender.messages {
		message := &sender.messages[i]
		if strings.Contains(message.Subject, "crawl failure") {
			failureMessage = message
		} else {
			pageErrorMessage = message
		}
		writeOperationalEmailFixturePreview(t, message)
	}
	if failureMessage == nil || pageErrorMessage == nil {
		t.Fatalf("messages did not contain both event types: %#v", sender.messages)
	}
	for name, message := range map[string]*OperationalAlertEmail{"crawl failure": failureMessage, "new page error": pageErrorMessage} {
		if !strings.Contains(message.Text, "HTTP 404") || !strings.Contains(message.Text, pageURL) {
			t.Errorf("%s text omitted observed page error or full URL: %s", name, message.Text)
		}
		if !strings.Contains(message.HTML, "href=\""+pageURL+"\"") || !strings.Contains(message.HTML, ">"+pageURL+"</a>") {
			t.Errorf("%s HTML omitted clickable full page URL: %s", name, message.HTML)
		}
	}
	if !strings.Contains(failureMessage.Text, "computing PageRank: reading pagerank graph pages: context deadline exceeded") {
		t.Fatalf("legacy exact-session crawler cause missing from failure email: %s", failureMessage.Text)
	}
	if !strings.Contains(failureMessage.HTML, "context deadline exceeded") {
		t.Fatalf("legacy execution cause missing from failure HTML: %s", failureMessage.HTML)
	}
}

func TestOperationalEmailLegacyLogOutranksPageRankEvidenceOutsideCurrentRun(t *testing.T) {
	for _, test := range []struct {
		name         string
		evidenceTime func(startedAt, finishedAt time.Time) time.Time
	}{
		{name: "before run", evidenceTime: func(startedAt, _ time.Time) time.Time { return startedAt.Add(-time.Second) }},
		{name: "after terminal", evidenceTime: func(_, finishedAt time.Time) time.Time { return finishedAt.Add(time.Second) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv, _, keyStore := newTestServer(t)
			project := operationalProject(t, keyStore, "Terminal cause")
			verifiedUserWithRole(t, keyStore, "admin@example.test", apikeys.RoleAdmin, nil)
			sender := &recordingOperationalSender{}
			srv.emailSender = sender
			now := time.Date(2026, 10, 3, 13, 0, 0, 0, time.UTC)
			finishedAt := now.Add(-time.Minute)
			startedAt := finishedAt.Add(-time.Hour)
			session := storage.CrawlSession{ID: "terminal-log-wins", Status: "completed_with_errors", StartedAt: startedAt, FinishedAt: finishedAt, ProjectID: &project.ID}
			ms := srv.store.(*mockStore)
			ms.terminalSessions = []storage.CrawlSession{session}
			srv.store = &operationalEvidenceStore{
				mockStore: ms,
				evidenceBySession: map[string]*storage.PageRankEvidence{
					session.ID: {State: storage.PageRankEvidenceFailed, OccurredAt: test.evidenceTime(startedAt, finishedAt), Failure: "unrelated PageRank attempt failure"},
				},
			}
			ms.operationalErrorLogs = []applog.LogRow{{
				Timestamp: finishedAt.Add(time.Second), Level: "error", Component: "crawler",
				Message: "Crawl " + session.ID + " failed: computing PageRank: terminal crawl failure",
			}}
			if err := keyStore.InitializeOperationalEmailCursor(now.Add(-time.Hour)); err != nil {
				t.Fatal(err)
			}
			if err := srv.runOperationalEmailTick(context.Background(), now); err != nil {
				t.Fatal(err)
			}
			if len(sender.messages) != 1 || !strings.Contains(sender.messages[0].Text, "terminal crawl failure") || strings.Contains(sender.messages[0].Text, "unrelated PageRank attempt failure") {
				t.Fatalf("terminal log was not preferred over unrelated PageRank evidence: %#v", sender.messages)
			}
		})
	}
}

func TestOperationalEmailDoesNotInferCauseFromUnmatchedLogsOrPageRankEvidence(t *testing.T) {
	for _, evidenceState := range []string{storage.PageRankEvidenceFailed, storage.PageRankEvidenceStarted} {
		t.Run(evidenceState, func(t *testing.T) {
			srv, _, keyStore := newTestServer(t)
			project := operationalProject(t, keyStore, "Evidence boundary")
			verifiedUserWithRole(t, keyStore, "admin@example.test", apikeys.RoleAdmin, nil)
			sender := &recordingOperationalSender{}
			srv.emailSender = sender
			finishedAt := time.Date(2026, 10, 3, 13, 0, 0, 0, time.UTC)
			sessionID := "evidence-boundary"
			session := storage.CrawlSession{ID: sessionID, Status: "completed_with_errors", StartedAt: finishedAt.Add(-30 * time.Second), FinishedAt: finishedAt, ProjectID: &project.ID}
			ms := srv.store.(*mockStore)
			ms.terminalSessions = []storage.CrawlSession{session}
			pageURL := "https://example.test/observed-404"
			ms.pageErrorsBySession = map[string][]storage.PageErrorObservation{sessionID: {{URL: pageURL, StatusCode: 404}}}
			srv.store = &operationalEvidenceStore{
				mockStore: ms,
				evidenceBySession: map[string]*storage.PageRankEvidence{
					sessionID: {State: evidenceState, OccurredAt: finishedAt.Add(-10 * time.Second), Failure: "in-window unrelated PageRank attempt failure"},
				},
			}
			ms.operationalErrorLogs = []applog.LogRow{
				{Timestamp: finishedAt.Add(-45 * time.Second), Level: "error", Component: "crawler", Message: "Crawl " + sessionID + " failed: previous run PageRank failure"},
				{Timestamp: finishedAt.Add(time.Second), Level: "error", Component: "crawler", Message: "Crawl " + sessionID + " failed - wrong prefix"},
				{Timestamp: finishedAt.Add(time.Second), Level: "error", Component: "crawler", Message: "Crawl another-session failed: referenced " + sessionID},
				{Timestamp: finishedAt.Add(10 * time.Minute), Level: "error", Component: "crawler", Message: "Crawl " + sessionID + " failed: outside terminal window"},
			}
			if err := keyStore.InitializeOperationalEmailCursor(finishedAt.Add(-time.Hour)); err != nil {
				t.Fatal(err)
			}
			if err := srv.runOperationalEmailTick(context.Background(), finishedAt.Add(30*time.Second)); err != nil {
				t.Fatal(err)
			}
			if len(sender.messages) != 2 {
				t.Fatalf("messages = %#v; want execution and separate page-error events", sender.messages)
			}
			var failureMessage *OperationalAlertEmail
			for i := range sender.messages {
				if strings.Contains(sender.messages[i].Subject, "crawl failure") {
					failureMessage = &sender.messages[i]
				}
			}
			if failureMessage == nil || !strings.Contains(failureMessage.Text, "retained evidence does not identify its execution cause") {
				t.Fatalf("unmatched logs/PageRank evidence were attributed as the cause: %#v", sender.messages)
			}
			if !strings.Contains(failureMessage.Text, "HTTP 404") || !strings.Contains(failureMessage.Text, pageURL) {
				t.Fatalf("separate observed 404 missing from failure email: %s", failureMessage.Text)
			}
			for _, unrelated := range []string{"wrong prefix", "outside terminal window", "in-window unrelated PageRank attempt failure", "previous run PageRank failure"} {
				if strings.Contains(failureMessage.Text, unrelated) {
					t.Fatalf("unrelated detail %q was attributed to the session: %s", unrelated, failureMessage.Text)
				}
			}
		})
	}
}

func TestOperationalEmailLargePageErrorDetailsKeepURLAndOmissionCount(t *testing.T) {
	srv, _, keyStore := newTestServer(t)
	project := operationalProject(t, keyStore, "Long URLs")
	verifiedUserWithRole(t, keyStore, "admin@example.test", apikeys.RoleAdmin, nil)
	sender := &recordingOperationalSender{}
	srv.emailSender = sender
	finishedAt := time.Now().UTC().Add(-time.Minute)
	if err := keyStore.InitializeOperationalEmailCursor(finishedAt.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	longURL := "https://example.test/features/" + strings.Repeat("balanced-(segment)-", 30) + "end?lang=en&filter=category!"
	nestedURL := "https://example.test/page?next=https%3A%2F%2Fuser%3Aprivate-password%40other.test%2F&lang=en"
	nestedReason := `Get "https://example.test/fetch?next=https%3A%2F%2Fuser%3Aprivate-password%40other.test%2F&lang=en": request failed`
	session := storage.CrawlSession{ID: "long-path-51-errors", Status: "completed_with_errors", FinishedAt: finishedAt, ProjectID: &project.ID}
	pageErrors := make([]storage.PageErrorObservation, 51)
	pageErrors[0] = storage.PageErrorObservation{URL: longURL, StatusCode: 404}
	pageErrors[1] = storage.PageErrorObservation{URL: nestedURL, StatusCode: 503, FetchReason: nestedReason}
	for i := 2; i < len(pageErrors); i++ {
		pageErrors[i] = storage.PageErrorObservation{URL: fmt.Sprintf("https://example.test/error-%02d", i), StatusCode: 404}
	}
	ms := srv.store.(*mockStore)
	ms.terminalSessions = []storage.CrawlSession{session}
	ms.pageErrorsBySession = map[string][]storage.PageErrorObservation{session.ID: pageErrors}
	if err := srv.runOperationalEmailTick(context.Background(), finishedAt.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 2 {
		t.Fatalf("messages = %#v; want execution and new-page events", sender.messages)
	}
	wantOmission := "Showing 50 of 51 page errors; 1 omitted."
	wantHref := `href="` + strings.ReplaceAll(longURL, "&", "&amp;") + `" style="overflow-wrap:anywhere;word-break:break-all"`
	wantNestedURL := "https://example.test/page?next=%5BREDACTED%5D&lang=en"
	wantFetchReason := `Get "https://example.test/fetch?next=%5BREDACTED%5D&lang=en": request failed`
	for i := range sender.messages {
		message := &sender.messages[i]
		if !strings.Contains(message.Text, longURL) || !strings.Contains(message.Text, wantOmission) {
			t.Errorf("%s text lost the long URL or omission count", message.Subject)
		}
		if !strings.Contains(message.HTML, wantHref) || !strings.Contains(message.HTML, ">"+html.EscapeString(longURL)+"</a>") {
			t.Errorf("%s HTML did not preserve and wrap the long observed URL: %s", message.Subject, message.HTML)
		}
		if strings.Contains(message.Text, "private-password") || strings.Contains(message.HTML, "private-password") {
			t.Errorf("%s leaked nested URL credentials: text=%s html=%s", message.Subject, message.Text, message.HTML)
		}
		if !strings.Contains(message.Text, wantNestedURL) || !strings.Contains(message.Text, wantFetchReason) {
			t.Errorf("%s did not retain safe page URL and fetch context: %s", message.Subject, message.Text)
		}
		writeOperationalEmailFixturePreview(t, message, "longpath_51errors")
	}
}

func TestOperationalEmailUsesPersistedFinalizationCauseWithoutPageErrors(t *testing.T) {
	srv, _, keyStore := newTestServer(t)
	project := operationalProject(t, keyStore, "PageRank failure")
	verifiedUserWithRole(t, keyStore, "admin@example.test", apikeys.RoleAdmin, nil)
	sender := &recordingOperationalSender{}
	srv.emailSender = sender
	now := time.Now().UTC()
	if err := keyStore.InitializeOperationalEmailCursor(now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	session := storage.CrawlSession{
		ID: "durable-finalization-cause", Status: "completed_with_errors", FinishedAt: now.Add(-time.Minute), ProjectID: &project.ID,
		Config: config.WithSessionFinalizationMetadata("{}", config.SessionFinalizationMetadata{
			PageRankFailure: "reading pagerank graph pages: context deadline exceeded",
		}),
	}
	srv.store.(*mockStore).terminalSessions = []storage.CrawlSession{session}
	if err := srv.runOperationalEmailTick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("messages = %#v; want one execution alert and no page-error alert", sender.messages)
	}
	message := sender.messages[0]
	if !strings.Contains(message.Text, "PageRank finalization failed: reading pagerank graph pages: context deadline exceeded") {
		t.Fatalf("persisted finalization cause missing: %s", message.Text)
	}
	if strings.Contains(message.Text, "HTTP ") || strings.Contains(message.Text, "page errors:") {
		t.Fatalf("email invented page errors when none were observed: %s", message.Text)
	}
}

func TestOperationalEmailExplainsBufferLossAndMissingHistoricalCause(t *testing.T) {
	t.Run("buffer loss", func(t *testing.T) {
		srv, _, keyStore := newTestServer(t)
		project := operationalProject(t, keyStore, "Buffer loss")
		verifiedUserWithRole(t, keyStore, "admin@example.test", apikeys.RoleAdmin, nil)
		sender := &recordingOperationalSender{}
		srv.emailSender = sender
		now := time.Now().UTC()
		if err := keyStore.InitializeOperationalEmailCursor(now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		session := storage.CrawlSession{
			ID: "buffer-loss", Status: "completed_with_errors", FinishedAt: now.Add(-time.Minute), ProjectID: &project.ID,
			Config: config.WithSessionFinalizationMetadata("{}", config.SessionFinalizationMetadata{LostPages: 2, LostLinks: 1, BufferFailure: "ClickHouse insert failed"}),
		}
		srv.store.(*mockStore).terminalSessions = []storage.CrawlSession{session}
		if err := srv.runOperationalEmailTick(context.Background(), now); err != nil {
			t.Fatal(err)
		}
		if len(sender.messages) != 1 || !strings.Contains(sender.messages[0].Text, "lost 2 page row(s) and 1 link row(s)") || !strings.Contains(sender.messages[0].Text, "ClickHouse insert failed") {
			t.Fatalf("buffer-loss cause missing from email: %#v", sender.messages)
		}
	})

	t.Run("missing historical cause", func(t *testing.T) {
		srv, _, keyStore := newTestServer(t)
		verifiedUserWithRole(t, keyStore, "admin@example.test", apikeys.RoleAdmin, nil)
		sender := &recordingOperationalSender{}
		srv.emailSender = sender
		now := time.Now().UTC()
		if err := keyStore.InitializeOperationalEmailCursor(now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		session := storage.CrawlSession{ID: "missing-historical-cause", Status: "crashed", FinishedAt: now.Add(-time.Minute)}
		srv.store.(*mockStore).terminalSessions = []storage.CrawlSession{session}
		if err := srv.runOperationalEmailTick(context.Background(), now); err != nil {
			t.Fatal(err)
		}
		if len(sender.messages) != 1 || !strings.Contains(sender.messages[0].Text, "retained evidence does not identify its execution cause") {
			t.Fatalf("missing-cause email did not accurately explain the evidence gap: %#v", sender.messages)
		}
		if strings.Contains(sender.messages[0].Text, "No page errors") || strings.Contains(sender.messages[0].Text, "HTTP 0") {
			t.Fatalf("email claimed a page-level outcome without evidence: %s", sender.messages[0].Text)
		}
	})
}

func TestOperationalEmailActivationSkipsHistoryAndSurvivesRestart(t *testing.T) {
	srv, _, memoryStore := newTestServer(t)
	if err := memoryStore.Close(); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "operational-email.db")
	keyStore, err := apikeys.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	srv.keyStore = keyStore
	verifiedUserWithRole(t, keyStore, "admin@example.test", apikeys.RoleAdmin, nil)
	srv.cfg.Resend.APIKey = "test-key"
	srv.cfg.Resend.From = "alerts@example.test"
	srv.cfg.Server.PublicURL = "https://crawlobserver.example"
	sender := &recordingOperationalSender{}
	srv.emailSender = sender
	activation := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	if err := keyStore.InitializeOperationalEmailCursor(activation); err != nil {
		t.Fatal(err)
	}
	before := storage.CrawlSession{ID: "before-activation", Status: "failed", FinishedAt: activation.Add(-time.Minute)}
	after := storage.CrawlSession{ID: "after-activation", Status: "failed", FinishedAt: activation.Add(time.Minute)}
	srv.store.(*mockStore).terminalSessions = []storage.CrawlSession{before, after}
	if err := srv.runOperationalEmailTick(context.Background(), activation.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	receipts, total, err := keyStore.ListOperationalEmailReceipts(10, 0)
	if err != nil || total != 1 || len(receipts) != 1 || receipts[0].SessionID != after.ID {
		t.Fatalf("first-activation receipts = %#v, total=%d, err=%v; want only post-activation session", receipts, total, err)
	}
	if err := keyStore.Close(); err != nil {
		t.Fatal(err)
	}
	keyStore, err = apikeys.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	srv.keyStore = keyStore
	defer keyStore.Close()
	if err := srv.runOperationalEmailTick(context.Background(), activation.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	receipts, total, err = keyStore.ListOperationalEmailReceipts(10, 0)
	if err != nil || total != 1 || len(receipts) != 1 || receipts[0].SessionID != after.ID || len(sender.messages) != 1 {
		t.Fatalf("post-restart receipts=%#v total=%d sends=%d err=%v; want one stable post-activation receipt", receipts, total, len(sender.messages), err)
	}
}

func TestSafeEmailPageURLKeepsPathAndUsefulQuery(t *testing.T) {
	got := safeEmailPageURL("https://user:password@example.test/reset/token?key=secret&lang=en#fragment")
	want := "https://example.test/reset/token?key=%5BREDACTED%5D&lang=en"
	if got != want {
		t.Fatalf("safeEmailPageURL() = %q; want %q", got, want)
	}
}

func writeOperationalEmailFixturePreview(t *testing.T, message *OperationalAlertEmail, fixtureNames ...string) {
	t.Helper()
	dir := strings.TrimSpace(os.Getenv("CRAWLOBSERVER_EMAIL_PREVIEW_DIR"))
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("creating operational email preview directory: %v", err)
	}
	name := "new_page_errors"
	if strings.Contains(message.Subject, "crawl failure") {
		name = "crawl_failure"
	}
	if len(fixtureNames) > 0 && fixtureNames[0] != "" {
		name = fixtureNames[0] + "_" + name
	}
	for extension, body := range map[string]string{"txt": message.Text, "html": message.HTML} {
		path := filepath.Join(dir, name+"."+extension)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("writing operational email preview %s: %v", path, err)
		}
	}
}

func containsEmailText(messages []OperationalAlertEmail, fragment string) bool {
	for _, message := range messages {
		if strings.Contains(message.Text, fragment) {
			return true
		}
	}
	return false
}

func TestOperationalEmailRequiresVerifiedActiveAdminAndExcludesManualStop(t *testing.T) {
	srv, _, keyStore := newTestServer(t)
	project := operationalProject(t, keyStore, "Eligibility")
	admin := verifiedUserWithRole(t, keyStore, "admin@example.test", apikeys.RoleAdmin, nil)
	inactive := verifiedUserWithRole(t, keyStore, "inactive@example.test", apikeys.RoleAdmin, nil)
	if _, err := keyStore.UpdateUser(inactive.ID, inactive.Username, inactive.Role, false, nil, nil); err != nil {
		t.Fatal(err)
	}
	unverified, err := keyStore.CreatePasswordlessUser("unverified@example.test", apikeys.RoleAdmin, nil)
	if err != nil {
		t.Fatal(err)
	}
	viewer := verifiedUserWithRole(t, keyStore, "viewer@example.test", apikeys.RoleViewer, []string{project.ID})
	_ = unverified
	_ = viewer
	sender := &recordingOperationalSender{}
	srv.emailSender = sender
	now := time.Now().UTC()
	if err := keyStore.InitializeOperationalEmailCursor(now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	manual := storage.CrawlSession{
		ID: "manual-stop", Status: "stopped", FinishedAt: now.Add(-3 * time.Minute), ProjectID: &project.ID,
		Config: config.WithSessionStopMetadata("{}", config.SessionStopMetadata{Reason: "manual"}),
	}
	unexpected := storage.CrawlSession{
		ID: "unexpected-stop", Status: "stopped", FinishedAt: now.Add(-2 * time.Minute), ProjectID: &project.ID,
		Config: config.WithSessionStopMetadata("{}", config.SessionStopMetadata{Reason: "crawler_error", Message: "sensitive details"}),
	}
	failed := storage.CrawlSession{ID: "failed", Status: "failed", FinishedAt: now.Add(-time.Minute), ProjectID: &project.ID}
	ms := srv.store.(*mockStore)
	ms.terminalSessions = []storage.CrawlSession{manual, unexpected, failed}
	if err := srv.runOperationalEmailTick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 2 {
		t.Fatalf("provider calls = %d, want unexpected stop and failed crawl only", len(sender.messages))
	}
	for _, message := range sender.messages {
		if message.To != admin.Email {
			t.Fatalf("ineligible recipient received mail: %s", message.To)
		}
		if strings.Contains(message.Text, "sensitive details") {
			t.Fatalf("raw stop reason leaked: %s", message.Text)
		}
	}
	receipts, _, err := keyStore.ListOperationalEmailReceipts(20, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, receipt := range receipts {
		if receipt.SessionID == manual.ID {
			t.Fatalf("manual stop created a failure receipt: %#v", receipt)
		}
		if receipt.UserID != admin.ID || receipt.Status != apikeys.OperationalEmailAccepted {
			t.Fatalf("unexpected recipient/status: %#v", receipt)
		}
	}
}

func TestOperationalEmailRetryRechecksAdminEligibility(t *testing.T) {
	srv, _, keyStore := newTestServer(t)
	admin := verifiedUserWithRole(t, keyStore, "admin@example.test", apikeys.RoleAdmin, nil)
	sender := &recordingOperationalSender{err: errors.New("provider secret detail")}
	srv.emailSender = sender
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	if err := keyStore.InitializeOperationalEmailCursor(now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	session := storage.CrawlSession{ID: "retry", Status: "failed", FinishedAt: now.Add(-time.Minute)}
	srv.store.(*mockStore).terminalSessions = []storage.CrawlSession{session}
	if err := srv.runOperationalEmailTick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("first provider calls = %d", len(sender.messages))
	}
	if _, err := keyStore.UpdateUser(admin.ID, admin.Username, apikeys.RoleViewer, true, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := srv.runOperationalEmailTick(context.Background(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("retry sent to changed-role user: %d calls", len(sender.messages))
	}
	receipts, _, err := keyStore.ListOperationalEmailReceipts(10, 0)
	if err != nil || len(receipts) != 1 {
		t.Fatalf("history = %#v, err=%v", receipts, err)
	}
	if receipts[0].Status != apikeys.OperationalEmailSkipped || receipts[0].Reason != "recipient_no_longer_eligible" || receipts[0].Attempts != 2 {
		t.Fatalf("changed-role retry result = %#v", receipts[0])
	}
}

func TestOperationalEmailWithoutResendIsVisibleAndHistoryIsAdminOnly(t *testing.T) {
	srv, handler, keyStore := newTestServer(t)
	project := operationalProject(t, keyStore, "No Resend")
	viewer := verifiedUserWithRole(t, keyStore, "viewer@example.test", apikeys.RoleViewer, []string{project.ID})
	admin := verifiedUserWithRole(t, keyStore, "admin@example.test", apikeys.RoleAdmin, nil)
	now := time.Now().UTC()
	if err := keyStore.InitializeOperationalEmailCursor(now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	srv.store.(*mockStore).terminalSessions = []storage.CrawlSession{{ID: "missing-resend", Status: "failed", FinishedAt: now.Add(-time.Minute), ProjectID: &project.ID}}
	if err := srv.runOperationalEmailTick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	receipts, total, err := keyStore.ListOperationalEmailReceipts(10, 0)
	if err != nil || total != 1 || len(receipts) != 1 {
		t.Fatalf("missing provider receipt = %#v, total=%d err=%v", receipts, total, err)
	}
	if receipts[0].Status != apikeys.OperationalEmailSkipped || receipts[0].Reason != "resend_not_configured" {
		t.Fatalf("missing provider state = %#v", receipts[0])
	}

	viewerToken, _, err := keyStore.CreateUserSession(viewer.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/admin/operational-emails", nil)
	request.AddCookie(&http.Cookie{Name: apikeys.SessionCookieName, Value: viewerToken})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("viewer history status = %d, body=%s", response.Code, response.Body.String())
	}

	_ = admin
	request = authRequest(httptest.NewRequest(http.MethodGet, "/api/admin/operational-emails?limit=10", nil))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("admin history status = %d, body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Receipts []apikeys.OperationalEmailReceipt `json:"receipts"`
		Total    int                               `json:"total"`
		Status   map[string]interface{}            `json:"status"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Total != 1 || len(payload.Receipts) != 1 || payload.Receipts[0].ProjectName != "No Resend" {
		t.Fatalf("admin history payload = %#v", payload)
	}
	if payload.Status["eligible_admin_count"] != float64(1) || payload.Status["worker_running"] != false {
		t.Fatalf("history empty-state status = %#v", payload.Status)
	}
}
