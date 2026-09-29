package apikeys

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestOperationalEmailReceiptsPersistAndDeduplicateSessionReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	if err := store.InitializeOperationalEmailCursor(now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	event := OperationalEmailEvent{
		SessionID:  "session-1",
		ProjectID:  "project-1",
		FinishedAt: now.Add(-time.Minute),
		Type:       "new_page_errors",
		Summary:    "2 new page error(s)",
		ErrorCount: 2,
		Details:    []string{"HTTP 404: https://example.test/a", "Fetch failed: https://example.test/b"},
		Recipients: []OperationalEmailRecipient{
			{UserID: "admin-a", Address: "a@example.test"},
			{UserID: "admin-b", Address: "b@example.test"},
		},
	}
	inserted, err := store.RecordOperationalSession(event.SessionID, event.FinishedAt, []OperationalEmailEvent{event}, now, true)
	if err != nil || !inserted {
		t.Fatalf("first RecordOperationalSession = %v, %v; want true, nil", inserted, err)
	}
	inserted, err = store.RecordOperationalSession(event.SessionID, event.FinishedAt, []OperationalEmailEvent{event}, now.Add(time.Second), true)
	if err != nil || inserted {
		t.Fatalf("replayed RecordOperationalSession = %v, %v; want false, nil", inserted, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	receipts, total, err := reopened.ListOperationalEmailReceipts(100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(receipts) != 2 {
		t.Fatalf("reopened receipts = %d, total %d; want 2", len(receipts), total)
	}
	if receipts[0].Status != OperationalEmailPending || receipts[0].IdempotencyKey == "" || len(receipts[0].Details) != 2 {
		t.Fatalf("persisted receipt = %#v", receipts[0])
	}
	cursor, err := reopened.OperationalEmailCursor()
	if err != nil {
		t.Fatal(err)
	}
	if cursor.SessionID != event.SessionID || cursor.FinishedAt != event.FinishedAt || cursor.ActivatedAt != now.Add(-time.Hour) {
		t.Fatalf("persisted cursor = %#v", cursor)
	}
	scanned, err := reopened.OperationalSessionIDsScannedSince(now.Add(-2 * time.Minute))
	if err != nil || len(scanned) != 1 || scanned[0] != event.SessionID {
		t.Fatalf("durable scan IDs since floor = %#v, %v; want session-1", scanned, err)
	}
}

func TestOperationalEmailClaimIsConcurrentAndRetriesStayWithinHorizon(t *testing.T) {
	store := newTestStore(t)
	start := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	if err := store.InitializeOperationalEmailCursor(start.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	event := OperationalEmailEvent{
		SessionID:  "session-2",
		FinishedAt: start.Add(-time.Minute),
		Type:       "crawl_failure",
		Summary:    "Crawl failed",
		Recipients: []OperationalEmailRecipient{{UserID: "admin-a", Address: "a@example.test"}},
	}
	if _, err := store.RecordOperationalSession(event.SessionID, event.FinishedAt, []OperationalEmailEvent{event}, start, true); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	results := make([][]OperationalEmailReceipt, 2)
	errs := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = store.ClaimOperationalEmailReceipts(start, 1, time.Hour, 2*time.Minute)
		}(i)
	}
	wg.Wait()
	claimed := 0
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("concurrent claim %d: %v", i, errs[i])
		}
		claimed += len(results[i])
	}
	if claimed != 1 {
		t.Fatalf("concurrent claims = %d; want exactly one", claimed)
	}
	first := results[0]
	if len(first) == 0 {
		first = results[1]
	}
	firstReceipt := first[0]
	if firstReceipt.Attempts != 1 || firstReceipt.Status != OperationalEmailSending {
		t.Fatalf("first claim = %#v", firstReceipt)
	}
	firstKey := firstReceipt.IdempotencyKey
	next := start.Add(time.Minute)
	if err := store.FinishOperationalEmailReceipt(firstReceipt.ID, OperationalEmailPending, "email_delivery_failed", start, &next); err != nil {
		t.Fatal(err)
	}
	if got, err := store.ClaimOperationalEmailReceipts(start.Add(30*time.Second), 1, time.Hour, 2*time.Minute); err != nil || len(got) != 0 {
		t.Fatalf("early retry = %#v, %v; want none", got, err)
	}
	second, err := store.ClaimOperationalEmailReceipts(next, 1, time.Hour, 2*time.Minute)
	if err != nil || len(second) != 1 {
		t.Fatalf("second claim = %#v, %v; want one", second, err)
	}
	if second[0].Attempts != 2 || second[0].IdempotencyKey != firstKey {
		t.Fatalf("retry changed attempt/key: %#v", second[0])
	}
	late := start.Add(61 * time.Minute)
	if got, err := store.ClaimOperationalEmailReceipts(late, 1, time.Hour, 2*time.Minute); err != nil || len(got) != 0 {
		t.Fatalf("late retry = %#v, %v; want no resend beyond provider idempotency horizon", got, err)
	}
	receipts, total, err := store.ListOperationalEmailReceipts(100, 0)
	if err != nil || total != 1 {
		t.Fatalf("receipt history = %d, %v", total, err)
	}
	if receipts[0].Status != OperationalEmailUnknown || receipts[0].Reason != "retry_horizon_elapsed" {
		t.Fatalf("late retry result = %#v; want unknown", receipts[0])
	}
}

func TestOperationalEmailWithoutRecipientsIsVisibleAsSkipped(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	if err := store.InitializeOperationalEmailCursor(now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	event := OperationalEmailEvent{
		SessionID:  "session-no-admin",
		FinishedAt: now.Add(-time.Minute),
		Type:       "crawl_failure",
		Summary:    "Crawl failed",
		SkipReason: "no_verified_admin_recipient",
	}
	if _, err := store.RecordOperationalSession(event.SessionID, event.FinishedAt, []OperationalEmailEvent{event}, now, true); err != nil {
		t.Fatal(err)
	}
	receipts, total, err := store.ListOperationalEmailReceipts(10, 0)
	if err != nil || total != 1 || len(receipts) != 1 {
		t.Fatalf("skipped receipt history = %#v, total %d, err %v", receipts, total, err)
	}
	if receipts[0].Status != OperationalEmailSkipped || receipts[0].Reason != "no_verified_admin_recipient" {
		t.Fatalf("no-recipient receipt = %#v", receipts[0])
	}
}
