//go:build integration

package storage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func cleanupQualitySession(t *testing.T, s *Store, sessionID string) {
	t.Helper()
	ctx := context.Background()
	for _, table := range []string{
		"crawl_quality_findings",
		"crawl_quality_evaluations",
		"crawl_quality_evaluation_findings",
		"crawl_quality_promotion_events",
		"crawl_quality_action_events",
	} {
		if err := s.conn.Exec(ctx, "ALTER TABLE crawlobserver."+table+" DROP PARTITION ?", sessionID); err != nil {
			t.Logf("cleanup %s: %v", table, err)
		}
	}
	for _, table := range []string{"crawl_quality_results", "crawl_quality_current_pointers"} {
		if err := s.conn.Exec(ctx, "ALTER TABLE crawlobserver."+table+" DELETE WHERE session_id = ? SETTINGS mutations_sync = 1", sessionID); err != nil {
			t.Logf("cleanup %s: %v", table, err)
		}
	}
}

func insertLegacyQuality(t *testing.T, s *Store, result CrawlQualityResult, findings []CrawlQualityFinding) {
	t.Helper()
	ctx := context.Background()
	if err := s.conn.Exec(ctx, `
		INSERT INTO crawlobserver.crawl_quality_results (
			session_id, project_id, baseline_session_id, status, score, trusted,
			is_full_crawl, summary, metrics, evaluated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		result.SessionID, result.ProjectID, result.BaselineSessionID, result.Status, result.Score,
		result.Trusted, result.IsFullCrawl, result.Summary, `{"pagerank_zero_top_pages":20}`, result.EvaluatedAt,
	); err != nil {
		t.Fatalf("insert legacy result: %v", err)
	}
	for _, finding := range findings {
		if err := s.conn.Exec(ctx, `
			INSERT INTO crawlobserver.crawl_quality_findings (
				session_id, project_id, severity, finding_type, message, metric,
				current_value, baseline_value, threshold_value, blocking, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			finding.SessionID, finding.ProjectID, finding.Severity, finding.FindingType, finding.Message,
			finding.Metric, finding.CurrentValue, finding.BaselineValue, finding.ThresholdValue,
			finding.Blocking, finding.CreatedAt,
		); err != nil {
			t.Fatalf("insert legacy finding: %v", err)
		}
	}
}

func qualityFixture(sessionID string) CrawlQualityResult {
	return CrawlQualityResult{
		SessionID: sessionID, ProjectID: "quality-test", Status: "untrusted", Score: 65,
		Summary: "legacy stale PageRank gate", IsFullCrawl: true,
		EvaluatedAt: time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC),
	}
}

func TestQualityPromotionEventSequenceOrdersEqualTimestampsAcrossRetryAndRestart(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	sessionID := uuid.NewString()
	projectID := "promotion-sequence-test"
	cleanupQualitySession(t, s, sessionID)
	t.Cleanup(func() { cleanupQualitySession(t, s, sessionID) })
	sameTime := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	binding := CrawlQualityPromotionEvent{
		ProjectID: projectID, SessionID: sessionID, EvaluationRevision: uuid.NewString(),
		PageRankEvidenceRevision: uuid.NewString(), OccurredAt: sameTime,
	}

	started := binding
	started.Status = "started"
	changed, startedResult, err := s.RecordQualityPromotionEvent(ctx, started)
	if err != nil || !changed || startedResult.EventSequence != 1 {
		t.Fatalf("record started changed=%t result=%#v err=%v", changed, startedResult, err)
	}
	applied := binding
	applied.Status = "applied"
	changed, appliedResult, err := s.RecordQualityPromotionEvent(ctx, applied)
	if err != nil || !changed || appliedResult.EventSequence != 2 {
		t.Fatalf("record applied changed=%t result=%#v err=%v", changed, appliedResult, err)
	}
	changed, duplicate, err := s.RecordQualityPromotionEvent(ctx, applied)
	if err != nil || changed || duplicate.EventSequence != 2 {
		t.Fatalf("duplicate applied changed=%t result=%#v err=%v", changed, duplicate, err)
	}

	restarted := &Store{conn: s.conn}
	latest, err := restarted.LatestQualityPromotionEvent(ctx, projectID, sessionID)
	if err != nil {
		t.Fatalf("restart readback: %v", err)
	}
	if latest.Status != "applied" || latest.EventSequence != 2 {
		t.Fatalf("restart latest = %#v, want applied sequence 2", latest)
	}

	retry := binding
	retry.Status = "started"
	changed, retryResult, err := restarted.RecordQualityPromotionEvent(ctx, retry)
	if err != nil || !changed || retryResult.EventSequence != 3 {
		t.Fatalf("record retry changed=%t result=%#v err=%v", changed, retryResult, err)
	}
	latest, err = restarted.LatestQualityPromotionEvent(ctx, projectID, sessionID)
	if err != nil || latest.Status != "started" || latest.EventSequence != 3 {
		t.Fatalf("newer retry must remain visible, latest=%#v err=%v", latest, err)
	}
}

func TestQualityActionEventSequenceOrdersSameMillisecondAfterCompactionAndRestart(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	sessionID := uuid.NewString()
	cleanupQualitySession(t, s, sessionID)
	t.Cleanup(func() { cleanupQualitySession(t, s, sessionID) })
	actionID := uuid.NewString()
	sameMillisecond := time.Date(2026, 8, 8, 12, 0, 0, 123000000, time.UTC)

	started, err := s.RecordQualityActionEvent(ctx, CrawlQualityActionEvent{
		SessionID: sessionID, ActionID: actionID, Action: "re_evaluate", Source: "test",
		Reason: "same millisecond lifecycle", Status: "started", OccurredAt: sameMillisecond,
	})
	if err != nil {
		t.Fatalf("record started action event: %v", err)
	}
	appliedInput := *started
	appliedInput.Status = "applied"
	appliedInput.OccurredAt = sameMillisecond.Add(500 * time.Microsecond)
	applied, err := s.RecordQualityActionEvent(ctx, appliedInput)
	if err != nil {
		t.Fatalf("record applied action event: %v", err)
	}
	if started.EventSequence != 1 || applied.EventSequence != 2 || !applied.OccurredAt.After(started.OccurredAt) {
		t.Fatalf("action lifecycle order started=%#v applied=%#v", started, applied)
	}
	if err := s.conn.Exec(ctx, `OPTIMIZE TABLE crawlobserver.crawl_quality_action_events FINAL`); err != nil {
		t.Fatalf("compact quality action events: %v", err)
	}

	restarted := &Store{conn: s.conn}
	events, err := restarted.ListQualityActionEvents(ctx, sessionID, 10)
	if err != nil {
		t.Fatalf("restart action event readback: %v", err)
	}
	if len(events) != 2 || events[0].Status != "applied" || events[0].EventSequence != 2 ||
		events[1].Status != "started" || events[1].EventSequence != 1 {
		t.Fatalf("restart action event order = %#v", events)
	}
}

func TestQualityCurrentPointerSequenceSurvivesClockTieAndRestart(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	sessionID := uuid.NewString()
	cleanupQualitySession(t, s, sessionID)
	t.Cleanup(func() { cleanupQualitySession(t, s, sessionID) })
	firstRevision := uuid.NewString()
	secondRevision := uuid.NewString()
	future := time.Now().UTC().Add(time.Minute).Truncate(time.Millisecond)
	if err := s.conn.Exec(ctx, `
		INSERT INTO crawlobserver.crawl_quality_current_pointers
			(session_id, evaluation_revision, pointer_sequence, published_at)
		VALUES (?, toUUID(?), 1, ?)`, sessionID, firstRevision, future); err != nil {
		t.Fatalf("insert first pointer: %v", err)
	}
	if err := s.publishCrawlQualityCurrentPointer(ctx, sessionID, secondRevision); err != nil {
		t.Fatalf("publish second pointer: %v", err)
	}
	if err := s.conn.Exec(ctx, `OPTIMIZE TABLE crawlobserver.crawl_quality_current_pointers FINAL`); err != nil {
		t.Fatalf("compact quality pointers: %v", err)
	}

	restarted := &Store{conn: s.conn}
	pointer, err := restarted.getCrawlQualityCurrentPointer(ctx, sessionID)
	if err != nil {
		t.Fatalf("restart pointer readback: %v", err)
	}
	if pointer.EvaluationRevision != secondRevision || pointer.PointerSequence != 2 {
		t.Fatalf("pointer = %#v, want second revision sequence 2", pointer)
	}
	if !pointer.PublishedAt.After(future) {
		t.Fatalf("pointer version timestamp %s must advance beyond %s", pointer.PublishedAt, future)
	}
}

func TestProjectCurrentSnapshotSequenceSurvivesSameMillisecondCompactionAndRestart(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := "snapshot-sequence-" + uuid.NewString()
	t.Cleanup(func() {
		if err := s.conn.Exec(ctx, `ALTER TABLE crawlobserver.project_current_snapshots DELETE WHERE project_id = ? SETTINGS mutations_sync = 1`, projectID); err != nil {
			t.Logf("cleanup current snapshot: %v", err)
		}
	})
	sameMillisecond := time.Now().UTC().Add(time.Minute).Truncate(time.Millisecond)
	first := ProjectCurrentSnapshot{
		ProjectID: projectID, CurrentSessionID: uuid.NewString(), BaselineSessionID: uuid.NewString(),
		BaselineCreatedAt: sameMillisecond, UpdatedAt: sameMillisecond,
	}
	if err := s.upsertProjectCurrentSnapshot(ctx, &first); err != nil {
		t.Fatalf("insert first snapshot pointer: %v", err)
	}
	second := first
	second.CurrentSessionID = uuid.NewString()
	second.UpdatedAt = sameMillisecond.Add(500 * time.Microsecond)
	if err := s.upsertProjectCurrentSnapshot(ctx, &second); err != nil {
		t.Fatalf("insert second snapshot pointer: %v", err)
	}
	if err := s.conn.Exec(ctx, `OPTIMIZE TABLE crawlobserver.project_current_snapshots FINAL`); err != nil {
		t.Fatalf("compact current snapshot pointers: %v", err)
	}

	restarted := &Store{conn: s.conn}
	latest, err := restarted.GetProjectCurrentSnapshot(ctx, projectID)
	if err != nil {
		t.Fatalf("restart current snapshot readback: %v", err)
	}
	if latest.CurrentSessionID != second.CurrentSessionID || latest.SnapshotRevision != 2 {
		t.Fatalf("current snapshot = %#v, want second pointer revision 2", latest)
	}
	if !latest.UpdatedAt.After(sameMillisecond) {
		t.Fatalf("current snapshot version timestamp %s must advance beyond %s", latest.UpdatedAt, sameMillisecond)
	}
}

func TestLegacyQualityImportIsCanonicalAndPreservesHistory(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	sessionID := uuid.NewString()
	cleanupQualitySession(t, s, sessionID)
	t.Cleanup(func() { cleanupQualitySession(t, s, sessionID) })
	legacy := qualityFixture(sessionID)
	findings := []CrawlQualityFinding{
		{SessionID: sessionID, ProjectID: legacy.ProjectID, Severity: "warning", FindingType: "config", Message: "changed", Metric: "crawl_config_changed", CreatedAt: legacy.EvaluatedAt},
		{SessionID: sessionID, ProjectID: legacy.ProjectID, Severity: "error", FindingType: "pagerank", Message: "stale", Metric: "pagerank_zero_top_pages", CurrentValue: 20, Blocking: true, CreatedAt: legacy.EvaluatedAt},
	}
	insertLegacyQuality(t, s, legacy, findings)

	first, err := s.EnsureLegacyQualityImported(ctx, sessionID)
	if err != nil {
		t.Fatalf("EnsureLegacyQualityImported first: %v", err)
	}
	second, err := s.EnsureLegacyQualityImported(ctx, sessionID)
	if err != nil {
		t.Fatalf("EnsureLegacyQualityImported second: %v", err)
	}
	if first.Source != legacyQualityImportSource || first.EvaluationRevision == "" || first.EvaluationRevision != second.EvaluationRevision {
		t.Fatalf("legacy import was not deterministic: first=%#v second=%#v", first, second)
	}
	if len(first.Findings) != 2 || first.Findings[0].FindingIndex != 0 || first.Findings[1].FindingIndex != 1 {
		t.Fatalf("findings are not coherent/stably indexed: %#v", first.Findings)
	}

	repair := *first
	repair.Source = "admin_re_evaluate"
	repair.PageRankEvidenceRevision = uuid.NewString()
	repair.PageRankEvidenceStatus = PageRankEvidenceFinalized
	repair.PageRankPredicateVersion = PageRankEligiblePredicateVersion
	repair.PageRankEligible, repair.PageRankPositive, repair.PageRankZero = 51, 51, 0
	repair.Status, repair.Score, repair.Summary = "trusted", 100, "reconciled"
	repair.Findings = nil
	repair.EvaluationRevision = ""
	changed, current, err := s.PublishCrawlQualityEvaluation(ctx, repair, first.EvaluationRevision)
	if err != nil || !changed || current.EvaluationRevision == first.EvaluationRevision {
		t.Fatalf("publish repair changed=%t current=%#v err=%v", changed, current, err)
	}
	history, err := s.ListCrawlQualityHistory(ctx, sessionID)
	if err != nil {
		t.Fatalf("ListCrawlQualityHistory: %v", err)
	}
	if len(history) != 2 || history[0].EvaluationRevision != current.EvaluationRevision || history[1].Source != legacyQualityImportSource {
		t.Fatalf("immutable history not preserved: %#v", history)
	}
}

func TestQualityPointerRetryAndConcurrentExpectedConflict(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	sessionID := uuid.NewString()
	cleanupQualitySession(t, s, sessionID)
	t.Cleanup(func() { cleanupQualitySession(t, s, sessionID) })
	base := normalizeQualityEvaluation(CrawlQualityResult{SessionID: sessionID, ProjectID: "quality-test", Status: "warning", Score: 80, Summary: "base"})
	if err := s.insertCrawlQualityEvaluation(ctx, base); err != nil {
		t.Fatalf("insert pre-pointer evaluation: %v", err)
	}
	if err := s.verifyCrawlQualityEvaluation(ctx, base); err != nil {
		t.Fatalf("verify pre-pointer evaluation: %v", err)
	}
	changed, current, err := s.PublishCrawlQualityEvaluation(ctx, base, "")
	if err != nil || !changed || current.EvaluationRevision != base.EvaluationRevision {
		t.Fatalf("pointer retry changed=%t current=%#v err=%v", changed, current, err)
	}

	results := []CrawlQualityResult{
		{SessionID: sessionID, ProjectID: "quality-test", Status: "trusted", Score: 100, Summary: "first"},
		{SessionID: sessionID, ProjectID: "quality-test", Status: "trusted", Score: 99, Summary: "second"},
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(results))
	changedCount := 0
	var changedMu sync.Mutex
	for _, candidate := range results {
		candidate := candidate
		wg.Add(1)
		go func() {
			defer wg.Done()
			changed, _, err := s.PublishCrawlQualityEvaluation(ctx, candidate, base.EvaluationRevision)
			if changed {
				changedMu.Lock()
				changedCount++
				changedMu.Unlock()
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	conflicts := 0
	for err := range errs {
		if err == nil {
			continue
		}
		var conflict *QualityEvaluationConflictError
		if errors.As(err, &conflict) {
			conflicts++
			continue
		}
		t.Fatalf("unexpected concurrent publish error: %v", err)
	}
	if changedCount != 1 || conflicts != 1 {
		t.Fatalf("expected one publish and one typed conflict, changed=%d conflicts=%d", changedCount, conflicts)
	}
}

func TestQualityPublishRecoversPartialFindingsBeforePointer(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	sessionID := uuid.NewString()
	cleanupQualitySession(t, s, sessionID)
	t.Cleanup(func() { cleanupQualitySession(t, s, sessionID) })

	result := normalizeQualityEvaluation(CrawlQualityResult{
		SessionID: sessionID, ProjectID: "quality-test", Status: "warning", Score: 70, Summary: "partial recovery",
		Findings: []CrawlQualityFinding{
			{Severity: "error", FindingType: "pagerank", Metric: "pagerank_zero_top_pages", Message: "missing rank", Blocking: true},
			{Severity: "warning", FindingType: "config", Metric: "crawl_config_changed", Message: "configuration changed"},
		},
	})
	if err := s.insertCrawlQualityEvaluation(ctx, result); err != nil {
		t.Fatalf("inserting partial evaluation: %v", err)
	}
	partial := result
	partial.Findings = []CrawlQualityFinding{result.Findings[0]}
	if err := s.insertCrawlQualityFindings(ctx, partial); err != nil {
		t.Fatalf("inserting partial findings: %v", err)
	}

	changed, current, err := s.PublishCrawlQualityEvaluation(ctx, result, "")
	if err != nil || !changed {
		t.Fatalf("recovering partial evaluation changed=%t err=%v", changed, err)
	}
	if current == nil || current.EvaluationRevision != result.EvaluationRevision {
		t.Fatalf("unexpected current result after recovery: %#v", current)
	}
	findings, err := s.GetCrawlQualityFindingsForRevision(ctx, sessionID, result.EvaluationRevision)
	if err != nil {
		t.Fatalf("reading recovered findings: %v", err)
	}
	if len(findings) != len(result.Findings) {
		t.Fatalf("pointer must not publish partial findings: got %d want %d", len(findings), len(result.Findings))
	}
	pointer, err := s.getCrawlQualityCurrentPointer(ctx, sessionID)
	if err != nil || pointer.EvaluationRevision != result.EvaluationRevision {
		t.Fatalf("pointer not published after complete recovery: %#v err=%v", pointer, err)
	}
}
