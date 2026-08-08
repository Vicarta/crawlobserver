package storage

import (
	"testing"
	"time"
)

func TestDeterministicQualityEvaluationRevisionCanonicalizesFindings(t *testing.T) {
	when := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	base := CrawlQualityResult{
		SessionID: "11111111-1111-1111-1111-111111111111", ProjectID: "project", Status: "warning", Score: 80,
		Metrics: map[string]interface{}{"pagerank_zero_top_pages": 20}, EvaluatedAt: when,
		Findings: []CrawlQualityFinding{
			{Severity: "warning", FindingType: "config", Message: "changed", Metric: "crawl_config_changed"},
			{Severity: "error", FindingType: "pagerank", Message: "zero", Metric: "pagerank_zero_top_pages", CurrentValue: 20, Blocking: true},
		},
	}
	reversed := base
	reversed.Findings = []CrawlQualityFinding{base.Findings[1], base.Findings[0]}
	first := normalizeQualityEvaluation(base)
	second := normalizeQualityEvaluation(reversed)
	if first.EvaluationRevision == "" || first.EvaluationRevision != second.EvaluationRevision {
		t.Fatalf("canonical revisions differ: %q != %q", first.EvaluationRevision, second.EvaluationRevision)
	}
	if first.FindingCount != 2 || len(first.Findings) != 2 || first.Findings[0].FindingIndex != 0 || first.Findings[1].FindingIndex != 1 {
		t.Fatalf("findings lack stable indexes: %#v", first.Findings)
	}
}

func TestQualityEvaluationZeroFindingsHasDurableCompletenessContract(t *testing.T) {
	result := normalizeQualityEvaluation(CrawlQualityResult{
		SessionID: "22222222-2222-2222-2222-222222222222", ProjectID: "project", Status: "trusted",
	})
	if result.FindingCount != 0 || len(result.Findings) != 0 {
		t.Fatalf("zero findings must be explicit, got count=%d findings=%#v", result.FindingCount, result.Findings)
	}
}

func TestQualityEvaluationConflictErrorIsTyped(t *testing.T) {
	err := &QualityEvaluationConflictError{SessionID: "session", ExpectedRevision: "before", CurrentRevision: "after"}
	if err.Error() == "" {
		t.Fatal("conflict error must provide readback context")
	}
}
