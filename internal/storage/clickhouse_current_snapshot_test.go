package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SEObserver/crawlobserver/internal/config"
)

func TestCompareSnapshotSourceUsesSessionIDForEqualStartedAt(t *testing.T) {
	at := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	if got := compareSnapshotSource(at, "00000000-0000-4000-8000-000000000001", at, "ffffffff-ffff-4fff-8fff-ffffffffffff"); got >= 0 {
		t.Fatalf("older equal-timestamp ID compare = %d, want < 0", got)
	}
	if got := compareSnapshotSource(at, "ffffffff-ffff-4fff-8fff-ffffffffffff", at, "00000000-0000-4000-8000-000000000001"); got <= 0 {
		t.Fatalf("newer equal-timestamp ID compare = %d, want > 0", got)
	}
}

func TestDeltaSitemapPublicationBindingRequiresExactCompleteSelection(t *testing.T) {
	snap := &ProjectCurrentSnapshot{CurrentSessionID: "current", SnapshotRevision: 12, ContentWatermarkSessionID: "watermark"}
	plan := &config.DeltaPlanConfig{
		SitemapRefresh: &config.DeltaSitemapRefresh{Mode: "fresh", FetchedAt: time.Now().UTC()},
		SitemapSelection: &config.DeltaSitemapSelection{
			SelectorRevision: "v1", RawObservationSessionID: "raw", RawObservedAt: time.Now().UTC(),
			PublishedSessionID: "current", PublishedSnapshotRevision: 12, PublishedContentWatermarkSessionID: "watermark",
			SelectionComplete: true,
		},
	}
	publish, err := deltaSitemapPublicationBindingMatches(plan, snap)
	if err != nil || !publish {
		t.Fatalf("exact complete binding publish=%t err=%v", publish, err)
	}
	plan.SitemapSelection.SelectionComplete = false
	publish, err = deltaSitemapPublicationBindingMatches(plan, snap)
	if err != nil || publish {
		t.Fatalf("incomplete selection publish=%t err=%v, want held publication", publish, err)
	}
	plan.SitemapSelection.SelectionComplete = true
	plan.SitemapSelection.PublishedSnapshotRevision++
	_, err = deltaSitemapPublicationBindingMatches(plan, snap)
	if !errors.Is(err, ErrCurrentSnapshotSourceSuperseded) {
		t.Fatalf("stale published binding err=%v, want superseded", err)
	}
}

func TestHistoricalSnapshotBindingFailsClosedForIncompleteFacts(t *testing.T) {
	_, _, err := (&Store{}).ValidateProjectCurrentSnapshotHistoricalBinding(context.Background(), ProjectCurrentSnapshot{})
	if !errors.Is(err, ErrCurrentSnapshotBindingConflict) {
		t.Fatalf("incomplete historical binding err=%v, want binding conflict", err)
	}
}

func TestCurrentSnapshotBindingMatchesOnlyCompletePublishedBinding(t *testing.T) {
	binding := CrawlQualityPromotionEvent{
		BaselineSessionID:  "baseline-session",
		EvaluationRevision: "eval", BaselineEvaluationRevision: "baseline", PageRankEvidenceRevision: "pagerank",
		EvaluatorRevision: "evaluator", RulesRevision: "rules",
	}
	snap := ProjectCurrentSnapshot{
		QualityBaselineSessionID:  "baseline-session",
		QualityEvaluationRevision: "eval", BaselineQualityEvaluationRevision: "baseline", PageRankEvidenceRevision: "pagerank",
		QualityEvaluatorRevision: "evaluator", QualityRulesRevision: "rules", QualityPromotionStatus: "applied",
	}
	if !currentSnapshotBindingMatches(snap, binding) {
		t.Fatal("complete applied binding should be recognized as finalized")
	}
	snap.PageRankEvidenceRevision = "older"
	if currentSnapshotBindingMatches(snap, binding) {
		t.Fatal("stale PageRank binding must require deterministic pointer finalization")
	}
	snap.PageRankEvidenceRevision = "pagerank"
	snap.QualityPromotionStatus = "started"
	if currentSnapshotBindingMatches(snap, binding) {
		t.Fatal("content-stage marker must not masquerade as applied pointer binding")
	}
}

func TestCurrentSnapshotNeedsFoldCleanupOnlyAfterPublishedFold(t *testing.T) {
	if !currentSnapshotNeedsFoldCleanup(ProjectCurrentSnapshot{DeltaCount: 0}) {
		t.Fatal("published folded pointer must resume post-pointer cleanup")
	}
	if currentSnapshotNeedsFoldCleanup(ProjectCurrentSnapshot{DeltaCount: 1}) {
		t.Fatal("active delta chain must not be cleaned as a completed fold")
	}
	if currentSnapshotNeedsFoldCleanup(ProjectCurrentSnapshot{LastDeltaSessionID: "delta"}) {
		t.Fatal("published non-fold delta must retain its chain")
	}
}
