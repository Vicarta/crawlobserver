package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SEObserver/crawlobserver/internal/apikeys"
)

type projectRescanVerificationFixture struct {
	ContractVersion string `json:"contract_version"`
	Request         struct {
		ProjectID string `json:"project_id"`
		URL       string `json:"url"`
	} `json:"request"`
	MutationAuth projectRescanVerificationAuth `json:"mutation_auth"`
	ReadAuth     projectRescanVerificationAuth `json:"read_auth"`
	Receipt      struct {
		ProjectID    string   `json:"project_id"`
		SessionID    string   `json:"session_id"`
		RequestID    string   `json:"request_id"`
		Status       string   `json:"status"`
		AcceptedURLs []string `json:"accepted_urls"`
		StartedAt    string   `json:"started_at"`
		CompletedAt  string   `json:"completed_at"`
	} `json:"receipt"`
	Scenarios []projectRescanVerificationScenario `json:"scenarios"`
}

type projectRescanVerificationAuth struct {
	Header     string `json:"header"`
	KeyType    string `json:"key_type"`
	Capability string `json:"capability"`
}

type projectRescanVerificationScenario struct {
	Name                 string          `json:"name"`
	SnapshotHTTPStatus   int             `json:"snapshot_http_status"`
	Snapshot             json.RawMessage `json:"snapshot"`
	PageDetailHTTPStatus int             `json:"page_detail_http_status"`
	PageDetail           json.RawMessage `json:"page_detail"`
	Verified             bool            `json:"verified"`
	Reason               string          `json:"reason"`
}

func TestProjectRescanVerificationContractFixture(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "fixtures", "project-rescan-verification-v1.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture projectRescanVerificationFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.ContractVersion != "project-rescan-verification/v1" {
		t.Fatalf("contract version = %q", fixture.ContractVersion)
	}
	if fixture.MutationAuth.Header != "X-API-Key" || fixture.MutationAuth.KeyType != "project" || fixture.MutationAuth.Capability != apikeys.CapabilityTargetedRescan {
		t.Fatalf("unexpected mutation auth contract: %#v", fixture.MutationAuth)
	}
	if fixture.ReadAuth.Header != "X-API-Key" || fixture.ReadAuth.KeyType != "project" || fixture.ReadAuth.Capability != "" {
		t.Fatalf("unexpected evidence auth contract: %#v", fixture.ReadAuth)
	}

	reasons := make(map[string]bool)
	for _, scenario := range fixture.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			verified, reason := evaluateProjectRescanVerificationFixture(fixture, scenario)
			if verified != scenario.Verified || reason != scenario.Reason {
				t.Fatalf("got verified=%v reason=%q, want verified=%v reason=%q", verified, reason, scenario.Verified, scenario.Reason)
			}
			reasons[reason] = true
		})
	}
	for _, required := range []string{
		"verified", "receipt_is_not_verification", "project_mismatch", "session_mismatch",
		"untrusted_current_snapshot", "malformed_current_snapshot", "stale_page", "page_mismatch", "malformed_page_detail",
	} {
		if !reasons[required] {
			t.Fatalf("fixture is missing required outcome %q", required)
		}
	}
}

func evaluateProjectRescanVerificationFixture(fixture projectRescanVerificationFixture, scenario projectRescanVerificationScenario) (bool, string) {
	startedAt, err := time.Parse(time.RFC3339, fixture.Receipt.StartedAt)
	if err != nil || fixture.Receipt.Status != "completed" || fixture.Receipt.ProjectID != fixture.Request.ProjectID ||
		fixture.Receipt.RequestID == "" || !containsExactString(fixture.Receipt.AcceptedURLs, fixture.Request.URL) {
		return false, "malformed_receipt"
	}
	if scenario.SnapshotHTTPStatus == 0 {
		return false, "receipt_is_not_verification"
	}
	if scenario.SnapshotHTTPStatus != 200 {
		return false, "untrusted_current_snapshot"
	}
	var snapshot struct {
		ProjectID              string `json:"project_id"`
		CurrentSessionID       string `json:"current_session_id"`
		QualityPromotionStatus string `json:"quality_promotion_status"`
	}
	if err := json.Unmarshal(scenario.Snapshot, &snapshot); err != nil || snapshot.CurrentSessionID == "" || snapshot.QualityPromotionStatus == "" {
		return false, "malformed_current_snapshot"
	}
	if snapshot.ProjectID != fixture.Request.ProjectID {
		return false, "project_mismatch"
	}
	if snapshot.QualityPromotionStatus != "applied" {
		return false, "untrusted_current_snapshot"
	}
	if scenario.PageDetailHTTPStatus != 200 {
		return false, "malformed_page_detail"
	}
	var detail struct {
		RequestSessionID string `json:"request_session_id"`
		RequestedURL     string `json:"requested_url"`
		Body             struct {
			Page *struct {
				CrawlSessionID string `json:"CrawlSessionID"`
				URL            string `json:"URL"`
				CrawledAt      string `json:"CrawledAt"`
			} `json:"page"`
		} `json:"body"`
	}
	if err := json.Unmarshal(scenario.PageDetail, &detail); err != nil || detail.Body.Page == nil || detail.Body.Page.CrawledAt == "" {
		return false, "malformed_page_detail"
	}
	if detail.RequestSessionID != snapshot.CurrentSessionID || detail.Body.Page.CrawlSessionID != snapshot.CurrentSessionID {
		return false, "session_mismatch"
	}
	if detail.RequestedURL != fixture.Request.URL || detail.Body.Page.URL != fixture.Request.URL {
		return false, "page_mismatch"
	}
	crawledAt, err := time.Parse(time.RFC3339, detail.Body.Page.CrawledAt)
	if err != nil {
		return false, "malformed_page_detail"
	}
	if crawledAt.Before(startedAt) {
		return false, "stale_page"
	}
	return true, "verified"
}

func containsExactString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
