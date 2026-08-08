//go:build integration

package storage

import (
	"context"
	"database/sql"
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
		for _, table := range []string{"project_current_snapshots", "project_current_snapshot_promotions_v2"} {
			if err := s.conn.Exec(ctx, `ALTER TABLE crawlobserver.`+table+` DELETE WHERE project_id = ? SETTINGS mutations_sync = 1`, projectID); err != nil {
				t.Logf("cleanup %s: %v", table, err)
			}
		}
	})
	sameMillisecond := time.Now().UTC().Add(time.Minute).Truncate(time.Millisecond)
	first := ProjectCurrentSnapshot{
		ProjectID: projectID, SourceSessionID: "11111111-1111-4111-8111-111111111111", SourceStartedAt: sameMillisecond,
		ContentWatermarkSessionID: "11111111-1111-4111-8111-111111111111", ContentWatermarkStartedAt: sameMillisecond,
		CurrentSessionID: "11111111-1111-4111-8111-111111111112", BaselineSessionID: uuid.NewString(),
		BaselineCreatedAt: sameMillisecond, UpdatedAt: sameMillisecond,
	}
	if err := s.upsertProjectCurrentSnapshot(ctx, &first); err != nil {
		t.Fatalf("insert first snapshot pointer: %v", err)
	}
	second := first
	second.CurrentSessionID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	second.ContentWatermarkSessionID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	second.UpdatedAt = sameMillisecond.Add(500 * time.Microsecond)
	if err := s.upsertProjectCurrentSnapshot(ctx, &second); err != nil {
		t.Fatalf("insert second snapshot pointer: %v", err)
	}
	// A late historical promotion receives a newer write timestamp/revision, but
	// its lower source tuple must remain unreadable after FINAL compaction.
	lateHistorical := first
	lateHistorical.CurrentSessionID = "00000000-0000-4000-8000-000000000001"
	lateHistorical.ContentWatermarkSessionID = "00000000-0000-4000-8000-000000000001"
	lateHistorical.UpdatedAt = sameMillisecond.Add(time.Second)
	if err := s.upsertProjectCurrentSnapshot(ctx, &lateHistorical); err != nil {
		t.Fatalf("insert late historical snapshot pointer: %v", err)
	}
	if err := s.conn.Exec(ctx, `OPTIMIZE TABLE crawlobserver.project_current_snapshot_promotions_v2 FINAL`); err != nil {
		t.Fatalf("compact current snapshot promotion journal: %v", err)
	}

	restarted := &Store{conn: s.conn}
	latest, err := restarted.GetProjectCurrentSnapshot(ctx, projectID)
	if err != nil {
		t.Fatalf("restart current snapshot readback: %v", err)
	}
	if latest.CurrentSessionID != second.CurrentSessionID || latest.SnapshotRevision != 2 {
		t.Fatalf("current snapshot = %#v, want second pointer revision 2", latest)
	}
	revision, err := restarted.GetProjectCurrentSnapshotRevision(ctx, projectID, second.SnapshotRevision)
	if err != nil || revision.CurrentSessionID != second.CurrentSessionID || revision.ContentWatermarkSessionID != second.ContentWatermarkSessionID {
		t.Fatalf("restart journal revision = %#v err=%v, want second pointer", revision, err)
	}
	if !latest.UpdatedAt.After(sameMillisecond) {
		t.Fatalf("current snapshot version timestamp %s must advance beyond %s", latest.UpdatedAt, sameMillisecond)
	}
}

func TestProjectCurrentSnapshotPromotionsV2MigratesOldJournalRevisions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := "snapshot-v2-upgrade-" + uuid.NewString()
	sourceID, watermarkID, currentID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Now().UTC().Truncate(time.Millisecond)
	t.Cleanup(func() {
		for _, table := range []string{"project_current_snapshot_promotions", "project_current_snapshot_promotions_v2"} {
			_ = s.conn.Exec(ctx, `ALTER TABLE crawlobserver.`+table+` DELETE WHERE project_id = ? SETTINGS mutations_sync = 1`, projectID)
		}
		_ = s.conn.Exec(ctx, `ALTER TABLE crawlobserver.crawl_sessions DELETE WHERE id = ? SETTINGS mutations_sync = 1`, currentID)
	})
	if err := s.InsertSession(ctx, &CrawlSession{ID: currentID, StartedAt: now, FinishedAt: now, Status: "completed", ProjectID: &projectID, Label: CurrentSnapshotLabel}); err != nil {
		t.Fatalf("insert synthetic current session: %v", err)
	}
	for _, revision := range []uint64{7, 8} {
		if err := s.conn.Exec(ctx, `
			INSERT INTO crawlobserver.project_current_snapshot_promotions (
				project_id, source_session_id, source_started_at, content_watermark_session_id, content_watermark_started_at,
				snapshot_revision, current_session_id, baseline_session_id, baseline_created_at, last_delta_session_id, delta_count, updated_at
			) VALUES (?, toUUID(?), ?, toUUID(?), ?, ?, toUUID(?), ?, ?, '', 0, ?)`,
			projectID, sourceID, now.Add(-time.Hour), watermarkID, now, revision, currentID, sourceID, now, now.Add(time.Duration(revision)*time.Second)); err != nil {
			t.Fatalf("insert old journal revision %d: %v", revision, err)
		}
	}
	if err := MigrateProjectCurrentSnapshotPromotionsV2(ctx, s.conn); err != nil {
		t.Fatalf("migrate old journal to v2: %v", err)
	}
	if err := s.conn.Exec(ctx, `OPTIMIZE TABLE crawlobserver.project_current_snapshot_promotions_v2 FINAL`); err != nil {
		t.Fatalf("compact v2 journal: %v", err)
	}
	restarted := &Store{conn: s.conn}
	for _, revision := range []uint64{7, 8} {
		snap, err := restarted.GetProjectCurrentSnapshotRevision(ctx, projectID, revision)
		if err != nil || snap.SnapshotRevision != revision {
			t.Fatalf("v2 journal revision %d = %#v err=%v", revision, snap, err)
		}
	}
	canonical, err := restarted.GetProjectCurrentSnapshot(ctx, projectID)
	if err != nil || canonical.SnapshotRevision != 8 {
		t.Fatalf("v2 canonical snapshot = %#v err=%v, want revision 8", canonical, err)
	}
	if err := restarted.DeleteProjectCurrentSnapshot(ctx, projectID); err != nil {
		t.Fatalf("delete migrated current snapshot: %v", err)
	}
	if err := MigrateProjectCurrentSnapshotPromotionsV2(ctx, s.conn); err != nil {
		t.Fatalf("rerun migration after delete: %v", err)
	}
	var remaining uint64
	if err := s.conn.QueryRow(ctx, `SELECT count() FROM crawlobserver.project_current_snapshot_promotions_v2 WHERE project_id = ?`, projectID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("deleted snapshot resurrected in v2 count=%d err=%v", remaining, err)
	}
	if err := s.conn.QueryRow(ctx, `SELECT count() FROM crawlobserver.project_current_snapshot_promotions WHERE project_id = ?`, projectID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("delete retained legacy journal rows count=%d err=%v", remaining, err)
	}
	if _, err := s.GetSession(ctx, currentID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted snapshot synthetic session remains referenced or present: %v", err)
	}
}

func TestCurrentSnapshotJournalRejectsOutOfOrderDeltaWatermark(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := "snapshot-delta-order-" + uuid.NewString()
	fullID, newerDeltaID, olderDeltaID, currentID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Now().UTC().Truncate(time.Millisecond)
	t.Cleanup(func() {
		for _, table := range []string{"project_current_snapshot_deltas", "project_current_snapshot_promotions_v2"} {
			_ = s.conn.Exec(ctx, `ALTER TABLE crawlobserver.`+table+` DELETE WHERE project_id = ? SETTINGS mutations_sync = 1`, projectID)
		}
		_ = s.conn.Exec(ctx, `ALTER TABLE crawlobserver.crawl_sessions DELETE WHERE id IN (?, ?, ?, ?) SETTINGS mutations_sync = 1`, fullID, newerDeltaID, olderDeltaID, currentID)
	})
	for id, started := range map[string]time.Time{fullID: now.Add(-3 * time.Hour), olderDeltaID: now.Add(-time.Hour), newerDeltaID: now, currentID: now.Add(-3 * time.Hour)} {
		if err := s.InsertSession(ctx, &CrawlSession{ID: id, StartedAt: started, FinishedAt: started, Status: "completed", ProjectID: &projectID}); err != nil {
			t.Fatal(err)
		}
	}
	snap := ProjectCurrentSnapshot{ProjectID: projectID, SourceSessionID: fullID, SourceStartedAt: now.Add(-3 * time.Hour), ContentWatermarkSessionID: newerDeltaID, ContentWatermarkStartedAt: now, CurrentSessionID: currentID, BaselineSessionID: fullID, BaselineCreatedAt: now, UpdatedAt: now}
	if err := s.upsertProjectCurrentSnapshot(ctx, &snap); err != nil {
		t.Fatal(err)
	}
	allowed, _, err := s.CanPromoteCurrentSnapshotSource(ctx, projectID, olderDeltaID)
	if err != nil || allowed {
		t.Fatalf("older delta allowed=%t err=%v", allowed, err)
	}
	if count, err := s.countProjectCurrentSnapshotDeltas(ctx, projectID); err != nil || count != 0 {
		t.Fatalf("out-of-order delta mutated marker count=%d err=%v", count, err)
	}
	got, err := s.GetProjectCurrentSnapshot(ctx, projectID)
	if err != nil || got.ContentWatermarkSessionID != newerDeltaID {
		t.Fatalf("out-of-order delta changed watermark=%#v err=%v", got, err)
	}
}

func TestCurrentSnapshotJournalEqualWatermarkUpdatesBindingWithoutContentMutation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := "snapshot-equal-watermark-" + uuid.NewString()
	fullID, deltaID, currentID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Now().UTC().Truncate(time.Millisecond)
	t.Cleanup(func() {
		_ = s.conn.Exec(ctx, `ALTER TABLE crawlobserver.project_current_snapshot_promotions_v2 DELETE WHERE project_id = ? SETTINGS mutations_sync = 1`, projectID)
	})
	base := ProjectCurrentSnapshot{ProjectID: projectID, SourceSessionID: fullID, SourceStartedAt: now.Add(-time.Hour), ContentWatermarkSessionID: deltaID, ContentWatermarkStartedAt: now, CurrentSessionID: currentID, BaselineSessionID: fullID, QualityEvaluationRevision: uuid.NewString(), BaselineCreatedAt: now, UpdatedAt: now}
	if err := s.upsertProjectCurrentSnapshot(ctx, &base); err != nil {
		t.Fatal(err)
	}
	updated := base
	updated.QualityEvaluationRevision = uuid.NewString()
	updated.UpdatedAt = now.Add(time.Second)
	if err := s.upsertProjectCurrentSnapshot(ctx, &updated); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetProjectCurrentSnapshot(ctx, projectID)
	if err != nil || got.QualityEvaluationRevision != updated.QualityEvaluationRevision || got.ContentWatermarkSessionID != deltaID {
		t.Fatalf("equal watermark binding readback=%#v err=%v", got, err)
	}
}

func TestLegacyCurrentSnapshotMigrationAmbiguousProvenanceFailsClosed(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID, currentID := "snapshot-legacy-"+uuid.NewString(), uuid.NewString()
	t.Cleanup(func() {
		for _, table := range []string{"project_current_snapshots", "project_current_snapshot_promotions_v2"} {
			_ = s.conn.Exec(ctx, `ALTER TABLE crawlobserver.`+table+` DELETE WHERE project_id = ? SETTINGS mutations_sync = 1`, projectID)
		}
	})
	if err := s.conn.Exec(ctx, `INSERT INTO crawlobserver.project_current_snapshots (project_id, current_session_id, baseline_session_id, baseline_created_at, last_delta_session_id, delta_count, updated_at) VALUES (?, toUUID(?), '', ?, '', 0, ?)`, projectID, currentID, time.Now().UTC(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetProjectCurrentSnapshot(ctx, projectID); !errors.Is(err, ErrCurrentSnapshotBindingConflict) {
		t.Fatalf("legacy ambiguous provenance err=%v", err)
	}
	var count uint64
	if err := s.conn.QueryRow(ctx, `SELECT count() FROM crawlobserver.project_current_snapshot_promotions_v2 WHERE project_id = ?`, projectID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("legacy migration wrote journal count=%d err=%v", count, err)
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

func TestLatestTrustedFullCrawlSessionUsesStrictHistoricalOrder(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := "quality-historical-" + uuid.NewString()
	olderAtSameTimeID := "10000000-0000-4000-8000-000000000001"
	evaluatedID := "20000000-0000-4000-8000-000000000001"
	newerAtSameTimeID := "30000000-0000-4000-8000-000000000001"
	futureID := "40000000-0000-4000-8000-000000000001"
	sessionIDs := []string{olderAtSameTimeID, evaluatedID, newerAtSameTimeID, futureID}
	for _, sessionID := range sessionIDs {
		cleanupQualitySession(t, s, sessionID)
	}
	t.Cleanup(func() {
		for _, sessionID := range sessionIDs {
			cleanupQualitySession(t, s, sessionID)
			if err := s.conn.Exec(ctx, `ALTER TABLE crawlobserver.crawl_sessions DELETE WHERE id = ? SETTINGS mutations_sync = 1`, sessionID); err != nil {
				t.Logf("cleanup historical session %s: %v", sessionID, err)
			}
		}
	})

	startedAt := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	for _, session := range []CrawlSession{
		{ID: olderAtSameTimeID, StartedAt: startedAt, FinishedAt: startedAt.Add(time.Minute), Status: "completed", ProjectID: &projectID},
		{ID: evaluatedID, StartedAt: startedAt, FinishedAt: startedAt.Add(time.Minute), Status: "completed", ProjectID: &projectID},
		{ID: newerAtSameTimeID, StartedAt: startedAt, FinishedAt: startedAt.Add(time.Minute), Status: "completed", ProjectID: &projectID},
		{ID: futureID, StartedAt: startedAt.Add(time.Hour), FinishedAt: startedAt.Add(time.Hour + time.Minute), Status: "completed", ProjectID: &projectID},
	} {
		session := session
		if err := s.InsertSession(ctx, &session); err != nil {
			t.Fatalf("insert session %s: %v", session.ID, err)
		}
		if _, _, err := s.PublishCrawlQualityEvaluation(ctx, CrawlQualityResult{
			SessionID: session.ID, ProjectID: projectID, EvaluationRevision: uuid.NewString(),
			Trusted: true, IsFullCrawl: true, Status: "trusted", Score: 100, EvaluatedAt: startedAt,
		}, ""); err != nil {
			t.Fatalf("publish trusted full quality %s: %v", session.ID, err)
		}
	}

	baseline, err := s.LatestTrustedFullCrawlSession(ctx, projectID, evaluatedID)
	if err != nil {
		t.Fatalf("select historical baseline: %v", err)
	}
	if baseline.ID != olderAtSameTimeID {
		t.Fatalf("historical baseline = %s, want %s; selected future/cyclic baseline", baseline.ID, olderAtSameTimeID)
	}

	latest, err := s.LatestTrustedFullCrawlSession(ctx, projectID, "")
	if err != nil {
		t.Fatalf("select unrestricted latest trusted baseline: %v", err)
	}
	if latest.ID != futureID {
		t.Fatalf("unrestricted baseline = %s, want %s", latest.ID, futureID)
	}
}

func TestLatestTrustedFullCrawlSessionLegacyFallbackRespectsHistoricalCutoff(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := "quality-legacy-historical-" + uuid.NewString()
	historicalID := "50000000-0000-4000-8000-000000000001"
	evaluatedID := "60000000-0000-4000-8000-000000000001"
	futureID := "70000000-0000-4000-8000-000000000001"
	sessionIDs := []string{historicalID, evaluatedID, futureID}
	for _, sessionID := range sessionIDs {
		cleanupQualitySession(t, s, sessionID)
	}
	t.Cleanup(func() {
		for _, sessionID := range sessionIDs {
			cleanupQualitySession(t, s, sessionID)
			if err := s.conn.Exec(ctx, `ALTER TABLE crawlobserver.crawl_sessions DELETE WHERE id = ? SETTINGS mutations_sync = 1`, sessionID); err != nil {
				t.Logf("cleanup legacy historical session %s: %v", sessionID, err)
			}
		}
	})

	startedAt := time.Date(2026, 8, 8, 14, 0, 0, 0, time.UTC)
	for _, session := range []CrawlSession{
		{ID: historicalID, StartedAt: startedAt.Add(-time.Hour), FinishedAt: startedAt.Add(-59 * time.Minute), Status: "completed", ProjectID: &projectID},
		{ID: evaluatedID, StartedAt: startedAt, FinishedAt: startedAt.Add(time.Minute), Status: "completed", ProjectID: &projectID},
		{ID: futureID, StartedAt: startedAt.Add(time.Hour), FinishedAt: startedAt.Add(time.Hour + time.Minute), Status: "completed", ProjectID: &projectID},
	} {
		session := session
		if err := s.InsertSession(ctx, &session); err != nil {
			t.Fatalf("insert legacy session %s: %v", session.ID, err)
		}
		insertLegacyQuality(t, s, CrawlQualityResult{
			SessionID: session.ID, ProjectID: projectID, Trusted: true, IsFullCrawl: true,
			Status: "trusted", Score: 100, EvaluatedAt: startedAt,
		}, nil)
	}

	baseline, err := s.LatestTrustedFullCrawlSession(ctx, projectID, evaluatedID)
	if err != nil {
		t.Fatalf("select legacy historical baseline: %v", err)
	}
	if baseline.ID != historicalID {
		t.Fatalf("legacy historical baseline = %s, want %s", baseline.ID, historicalID)
	}
	if _, err := s.getCrawlQualityCurrentPointer(ctx, futureID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("future legacy quality was imported despite cutoff: %v", err)
	}
}
