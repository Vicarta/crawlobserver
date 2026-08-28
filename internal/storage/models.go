package storage

import (
	"time"
)

// CrawlSession represents a crawl session.
type CrawlSession struct {
	ID           string
	StartedAt    time.Time
	FinishedAt   time.Time
	Status       string // running, completed, failed, stopped
	SeedURLs     []string
	Config       string // JSON
	PagesCrawled uint64
	UserAgent    string
	ProjectID    *string
	Label        string
}

// EffectiveOrigin is response-only operational origin evidence for a session.
// An unavailable or ambiguous result always has an empty Origin.
type EffectiveOrigin struct {
	Origin string
	State  string
}

const (
	EffectiveOriginProven      = "proven"
	EffectiveOriginUnavailable = "unavailable"
	EffectiveOriginAmbiguous   = "ambiguous"
)

// PageRow represents a crawled page for storage.
type PageRow struct {
	CrawlSessionID   string
	URL              string
	FinalURL         string
	StatusCode       uint16
	ContentType      string
	PageType         string
	Title            string
	TitleLength      uint16
	Canonical        string
	CanonicalIsSelf  bool
	IsIndexable      bool
	IndexReason      string // why not indexable
	MetaRobots       string
	MetaDescription  string
	MetaDescLength   uint16
	MetaKeywords     string
	H1               []string
	H2               []string
	H3               []string
	H4               []string
	H5               []string
	H6               []string
	WordCount        uint32
	InternalLinksIn  uint32
	InternalLinksOut uint32
	ExternalLinksOut uint32
	ImagesCount      uint16
	ImagesNoAlt      uint16
	Hreflang         []HreflangRow
	Lang             string
	OGTitle          string
	OGDescription    string
	OGImage          string
	SchemaTypes      []string
	HeadingOutline   []HeadingRow
	PageCreatedAt    *time.Time
	PageModifiedAt   *time.Time
	Headers          map[string]string
	RedirectChain    []RedirectHopRow
	BodySize         uint64
	FetchDurationMs  uint64
	ContentEncoding  string
	XRobotsTag       string
	Error            string
	Depth            uint16
	FoundOn          string
	SitemapSourceURL string
	SitemapRawLoc    string
	DiscoverySource  string
	DiscoveryDetail  string
	CandidateSources []string
	IsInSitemap      bool
	ProblemOrigin    string
	PageRank         float64
	ContentHash      uint64
	BodyHTML         string
	BodyTruncated    bool
	CrawledAt        time.Time

	// JS Rendering
	JSRendered         bool
	JSRenderDurationMs uint64
	JSRenderError      string

	// Rendered data
	RenderedTitle           string
	RenderedMetaDescription string
	RenderedH1              []string
	RenderedWordCount       uint32
	RenderedLinksCount      uint32
	RenderedImagesCount     uint16
	RenderedCanonical       string
	RenderedMetaRobots      string
	RenderedSchemaTypes     []string
	RenderedBodyHTML        string

	// Static response data retained when rendered DOM becomes authoritative.
	StaticTitle           string
	StaticMetaDescription string
	StaticH1              []string
	StaticWordCount       uint32
	StaticCanonical       string
	StaticMetaRobots      string
	StaticLinksCount      uint32
	StaticImagesCount     uint16
	StaticContentHash     uint64
	StaticBodyHTML        string

	// Diff flags (static vs rendered)
	JSChangedTitle       bool
	JSChangedDescription bool
	JSChangedH1          bool
	JSChangedCanonical   bool
	JSChangedContent     bool  // word count changed >20%
	JSAddedLinks         int32 // delta links
	JSAddedImages        int32 // delta images
	JSAddedSchema        bool  // new schema types appeared

	// Structured data validation summary
	SchemaValidCount   uint16
	SchemaErrorCount   uint16
	SchemaWarningCount uint16

	// Core Web Vitals
	CWVMeasured bool
	CWVLCP      float64 // Largest Contentful Paint (ms)
	CWVCLS      float64 // Cumulative Layout Shift
	CWVTTFB     float64 // Time to First Byte (ms)
}

type Orphan404CleanupCandidate struct {
	URL             string    `json:"url"`
	Title           string    `json:"title"`
	StatusCode      uint16    `json:"status_code"`
	CrawledAt       time.Time `json:"crawled_at"`
	InternalLinksIn uint32    `json:"internal_links_in"`
	IsInSitemap     bool      `json:"is_in_sitemap"`
}

// PageIssue is a generic SEO/technical issue derived from stored crawl signals.
type PageIssue struct {
	URL                     string   `json:"url"`
	Severity                string   `json:"severity"`   // "error" or "warning"
	IssueType               string   `json:"issue_type"` // "soft_404", "generic_rendered_title", ...
	IssueDetail             string   `json:"issue_detail"`
	StatusCode              uint16   `json:"status_code"`
	Title                   string   `json:"title"`
	StaticTitle             string   `json:"static_title"`
	StaticMetaDescription   string   `json:"static_meta_description"`
	RenderedTitle           string   `json:"rendered_title"`
	RenderedMetaDescription string   `json:"rendered_meta_description"`
	RenderedH1              []string `json:"rendered_h1"`
	WordCount               uint32   `json:"word_count"`
	RenderedWordCount       uint32   `json:"rendered_word_count"`
	ImagesCount             uint16   `json:"images_count"`
	RenderedImagesCount     uint16   `json:"rendered_images_count"`
}

// CoreWebVitalsSummary aggregates lab measurements for eligible HTML pages.
type CoreWebVitalsSummary struct {
	EligiblePages    uint64 `json:"eligible_pages"`
	MeasuredPages    uint64 `json:"measured_pages"`
	Good             uint64 `json:"good"`
	NeedsImprovement uint64 `json:"needs_improvement"`
	Poor             uint64 `json:"poor"`
	UnmeasuredPages  uint64 `json:"unmeasured_pages"`
}

// CoreWebVitalsPage is a page-level Core Web Vitals lab result.
type CoreWebVitalsPage struct {
	URL           string  `json:"url"`
	LCPMs         float64 `json:"lcp_ms"`
	CLS           float64 `json:"cls"`
	TTFBMs        float64 `json:"ttfb_ms"`
	LCPRating     string  `json:"lcp_rating"`
	CLSRating     string  `json:"cls_rating"`
	TTFBRating    string  `json:"ttfb_rating"`
	OverallRating string  `json:"overall_rating"`
}

// CoreWebVitalsReport contains a summary and one filtered page of lab results.
type CoreWebVitalsReport struct {
	Summary CoreWebVitalsSummary `json:"summary"`
	Pages   []CoreWebVitalsPage  `json:"pages"`
	Total   uint64               `json:"total"`
}

// CrawlQualityResult is the trust gate outcome for a crawl session.
type CrawlQualityResult struct {
	SessionID         string `json:"session_id"`
	ProjectID         string `json:"project_id"`
	BaselineSessionID string `json:"baseline_session_id"`
	// EvaluationRevision identifies this immutable quality evaluation. It is a
	// deterministic UUID derived from the evaluated inputs, not a wall-clock ID.
	EvaluationRevision         string                 `json:"evaluation_revision"`
	Source                     string                 `json:"source"`
	EvaluatorRevision          string                 `json:"evaluator_revision"`
	RulesRevision              string                 `json:"rules_revision"`
	BaselineEvaluationRevision string                 `json:"baseline_evaluation_revision,omitempty"`
	PageRankEvidenceRevision   string                 `json:"pagerank_evidence_revision,omitempty"`
	PageRankEvidenceSource     string                 `json:"pagerank_evidence_source,omitempty"`
	PageRankEvidenceStatus     string                 `json:"pagerank_evidence_status,omitempty"`
	PageRankPredicateVersion   string                 `json:"pagerank_predicate_version,omitempty"`
	PageRankEligible           uint64                 `json:"pagerank_eligible_pages"`
	PageRankPositive           uint64                 `json:"pagerank_positive_pages"`
	PageRankZero               uint64                 `json:"pagerank_zero_pages"`
	Stale                      bool                   `json:"stale"`
	StaleReasons               []string               `json:"stale_reasons,omitempty"`
	FindingCount               uint32                 `json:"finding_count"`
	PromotionStatus            string                 `json:"promotion_status,omitempty"`
	Status                     string                 `json:"status"` // trusted, warning, untrusted
	Score                      uint8                  `json:"score"`
	Trusted                    bool                   `json:"trusted"`
	IsFullCrawl                bool                   `json:"is_full_crawl"`
	Summary                    string                 `json:"summary"`
	Metrics                    map[string]interface{} `json:"metrics,omitempty"`
	EvaluatedAt                time.Time              `json:"evaluated_at"`
	Findings                   []CrawlQualityFinding  `json:"findings,omitempty"`
}

// CrawlQualityFinding is a single data-quality signal for a crawl session.
type CrawlQualityFinding struct {
	SessionID          string    `json:"session_id"`
	EvaluationRevision string    `json:"evaluation_revision,omitempty"`
	FindingIndex       uint32    `json:"finding_index,omitempty"`
	ProjectID          string    `json:"project_id"`
	Severity           string    `json:"severity"` // info, warning, error
	FindingType        string    `json:"finding_type"`
	Message            string    `json:"message"`
	Metric             string    `json:"metric"`
	CurrentValue       float64   `json:"current_value"`
	BaselineValue      float64   `json:"baseline_value"`
	ThresholdValue     float64   `json:"threshold_value"`
	Blocking           bool      `json:"blocking"`
	CreatedAt          time.Time `json:"created_at"`
}

// CrawlQualityCurrentPointer is the separately-published current revision for
// a session. A pointer is written only after the evaluation and every finding
// have been inserted and read back.
type CrawlQualityCurrentPointer struct {
	SessionID          string    `json:"session_id"`
	EvaluationRevision string    `json:"evaluation_revision"`
	PointerSequence    uint64    `json:"pointer_sequence"`
	PublishedAt        time.Time `json:"published_at"`
}

// CrawlQualityPromotionEvent records an append-only decision to promote (or
// reject) a quality evaluation into a project current snapshot.
type CrawlQualityPromotionEvent struct {
	ProjectID                  string    `json:"project_id"`
	SessionID                  string    `json:"session_id"`
	PromotionID                string    `json:"promotion_id"`
	EventSequence              uint64    `json:"event_sequence"`
	EvaluationRevision         string    `json:"evaluation_revision"`
	PageRankEvidenceRevision   string    `json:"pagerank_evidence_revision"`
	BaselineSessionID          string    `json:"baseline_session_id,omitempty"`
	BaselineEvaluationRevision string    `json:"baseline_evaluation_revision,omitempty"`
	EvaluatorRevision          string    `json:"evaluator_revision,omitempty"`
	RulesRevision              string    `json:"rules_revision,omitempty"`
	Status                     string    `json:"status"`
	Reason                     string    `json:"reason,omitempty"`
	Detail                     string    `json:"detail,omitempty"`
	OccurredAt                 time.Time `json:"occurred_at"`
}

// CrawlQualityActionEvent is the durable audit/readback record for an
// operator- or scheduler-triggered evaluation. It exists independently of
// snapshot promotion, so an untrusted no-promotion repair remains traceable.
type CrawlQualityActionEvent struct {
	SessionID                        string    `json:"session_id"`
	ActionID                         string    `json:"action_id"`
	EventSequence                    uint64    `json:"event_sequence"`
	Action                           string    `json:"action"`
	Source                           string    `json:"source"`
	Actor                            string    `json:"actor,omitempty"`
	Reason                           string    `json:"reason"`
	ExpectedEvaluationRevision       string    `json:"expected_evaluation_revision,omitempty"`
	PreviousEvaluationRevision       string    `json:"previous_evaluation_revision,omitempty"`
	ResultEvaluationRevision         string    `json:"result_evaluation_revision,omitempty"`
	ExpectedPageRankEvidenceRevision string    `json:"expected_pagerank_evidence_revision,omitempty"`
	PageRankEvidenceRevision         string    `json:"pagerank_evidence_revision,omitempty"`
	Status                           string    `json:"status"`
	OccurredAt                       time.Time `json:"occurred_at"`
}

// QualityReevaluateRequest is the narrow admin repair contract. The handler
// validates confirmation, audit reason, and expected revisions before it asks
// storage to publish a replacement evaluation.
type QualityReevaluateRequest struct {
	Confirm                          bool   `json:"confirm"`
	Reason                           string `json:"reason"`
	ExpectedEvaluationRevision       string `json:"expected_evaluation_revision,omitempty"`
	ExpectedPageRankEvidenceRevision string `json:"expected_pagerank_evidence_revision,omitempty"`
}

// QualityReevaluateResponse is returned by the idempotent repair endpoint.
type QualityReevaluateResponse struct {
	Changed          bool                        `json:"changed"`
	PromotionChanged bool                        `json:"promotion_changed"`
	Result           *CrawlQualityResult         `json:"result,omitempty"`
	Evidence         *PageRankEvidence           `json:"evidence,omitempty"`
	Promotion        *CrawlQualityPromotionEvent `json:"promotion,omitempty"`
}

// ProjectCurrentSnapshot describes the materialized full-site current state for a project.
type ProjectCurrentSnapshot struct {
	ProjectID        string `json:"project_id"`
	SnapshotRevision uint64 `json:"snapshot_revision"`
	// SourceSessionID is the trusted full crawl that supplies baseline lineage.
	SourceSessionID                   string    `json:"source_session_id"`
	SourceStartedAt                   time.Time `json:"source_started_at"`
	ContentWatermarkSessionID         string    `json:"content_watermark_session_id"`
	ContentWatermarkStartedAt         time.Time `json:"content_watermark_started_at"`
	CurrentSessionID                  string    `json:"current_session_id"`
	BaselineSessionID                 string    `json:"baseline_session_id"`
	QualityBaselineSessionID          string    `json:"quality_baseline_session_id,omitempty"`
	QualityEvaluationRevision         string    `json:"quality_evaluation_revision,omitempty"`
	BaselineQualityEvaluationRevision string    `json:"baseline_quality_evaluation_revision,omitempty"`
	PageRankEvidenceRevision          string    `json:"pagerank_evidence_revision,omitempty"`
	QualityEvaluatorRevision          string    `json:"quality_evaluator_revision,omitempty"`
	QualityRulesRevision              string    `json:"quality_rules_revision,omitempty"`
	QualityPromotionStatus            string    `json:"quality_promotion_status,omitempty"`
	BaselineCreatedAt                 time.Time `json:"baseline_created_at"`
	LastDeltaSessionID                string    `json:"last_delta_session_id"`
	DeltaCount                        uint32    `json:"delta_count"`
	UpdatedAt                         time.Time `json:"updated_at"`
}

// CrawlQualityMetrics is the aggregate data used by the quality evaluator.
type CrawlQualityMetrics struct {
	HTMLPages            uint64
	InternalLinks        uint64
	Status404            uint64
	Status5xx            uint64
	Noindex              uint64
	Redirects            uint64
	CanonicalMismatch    uint64
	PageRankZeroTopPages uint64
}

// CanaryPageCheck is the crawled page state used to validate project canaries.
type CanaryPageCheck struct {
	Found            bool
	URL              string
	FinalURL         string
	StatusCode       uint16
	Title            string
	Canonical        string
	IsIndexable      bool
	InternalLinksOut uint32
	PageRank         float64
}

// RedirectHopRow represents a redirect hop for storage.
type RedirectHopRow struct {
	URL        string
	StatusCode uint16
}

// HreflangRow represents a hreflang entry.
type HreflangRow struct {
	Lang string
	URL  string
}

// HeadingRow is a heading and its HTML level, kept in document order.
type HeadingRow struct {
	Level uint8
	Text  string
}

// RobotsRow represents a robots.txt entry for storage.
type RobotsRow struct {
	CrawlSessionID string
	Host           string
	StatusCode     uint16
	Content        string
	FetchedAt      time.Time
}

// SitemapRow represents a discovered sitemap for storage.
type SitemapRow struct {
	CrawlSessionID string
	URL            string
	Type           string // "index" | "urlset"
	URLCount       uint32
	ParentURL      string // empty if top-level
	StatusCode     uint16
	FetchedAt      time.Time
}

// SitemapURLRow represents a URL entry within a sitemap.
type SitemapURLRow struct {
	CrawlSessionID string
	SitemapURL     string
	Loc            string
	LastMod        string
	ChangeFreq     string
	Priority       string
}

// LinkRow represents a link for storage.
type LinkRow struct {
	CrawlSessionID string
	SourceURL      string
	TargetURL      string
	AnchorText     string
	Rel            string
	IsInternal     bool
	Tag            string
	LinkLocation   string
	CrawledAt      time.Time
}

// PageDiscoveryReferrer is retained link evidence that explains how a page was reached.
type PageDiscoveryReferrer struct {
	SourceURL    string    `json:"source_url"`
	TargetURL    string    `json:"target_url"`
	AnchorText   string    `json:"anchor_text"`
	Rel          string    `json:"rel"`
	Tag          string    `json:"tag"`
	LinkLocation string    `json:"link_location"`
	ViaRedirect  bool      `json:"via_redirect"`
	RedirectURL  string    `json:"redirect_url,omitempty"`
	CrawledAt    time.Time `json:"crawled_at"`
}

// PageDiscoveryEvidence describes why a URL is present in a crawl session.
type PageDiscoveryEvidence struct {
	Availability           string                  `json:"availability"`
	PrimarySource          string                  `json:"primary_source"`
	Detail                 string                  `json:"detail"`
	FoundOn                string                  `json:"found_on,omitempty"`
	IsSeed                 bool                    `json:"is_seed"`
	IsInSitemap            bool                    `json:"is_in_sitemap"`
	SitemapSourceURL       string                  `json:"sitemap_source_url,omitempty"`
	SitemapRawLoc          string                  `json:"sitemap_raw_loc,omitempty"`
	CandidateSources       []string                `json:"candidate_sources"`
	Referrers              []PageDiscoveryReferrer `json:"referrers"`
	ReferrersCount         uint64                  `json:"referrers_count"`
	DirectReferrersCount   uint64                  `json:"direct_referrers_count"`
	RedirectReferrersCount uint64                  `json:"redirect_referrers_count"`
}

// PageRankOptions controls link graph construction for PageRank recomputation.
type PageRankOptions struct {
	IncludeFooterLinks  bool
	FooterSelectors     []string
	RefreshLinkLocation bool
}

const (
	PageRankEvidenceStarted   = "started"
	PageRankEvidenceFinalized = "finalized"
	PageRankEvidenceFailed    = "failed"

	PageRankEvidenceComputed         = "computed"
	PageRankEvidenceObservedExisting = "observed_existing"
	PageRankAlgorithmVersion         = "pagerank-v1"
)

// PageRankEvidence is an append-only lifecycle event for one PageRank attempt.
// AttemptID is the PageRank revision stamped onto computed page rows.
type PageRankEvidence struct {
	SessionID            string    `json:"session_id"`
	AttemptID            string    `json:"attempt_id"`
	EventSequence        uint64    `json:"event_sequence"`
	PredecessorAttemptID string    `json:"predecessor_attempt_id,omitempty"`
	State                string    `json:"state"`
	Source               string    `json:"source"`
	AlgorithmVersion     string    `json:"algorithm_version"`
	PredicateVersion     string    `json:"predicate_version"`
	OptionsSignature     string    `json:"options_signature"`
	GraphFingerprint     string    `json:"graph_fingerprint"`
	RankFingerprint      string    `json:"rank_fingerprint"`
	GraphPageCount       uint64    `json:"graph_page_count"`
	EligiblePageCount    uint64    `json:"eligible_page_count"`
	PositivePageCount    uint64    `json:"positive_page_count"`
	ZeroPageCount        uint64    `json:"zero_page_count"`
	QueryIdentity        string    `json:"query_identity"`
	OccurredAt           time.Time `json:"occurred_at"`
	Failure              string    `json:"failure,omitempty"`
}

// PageRankPopulation is the reconciled population for a PageRank revision.
type PageRankPopulation struct {
	Eligible uint64 `json:"eligible"`
	Positive uint64 `json:"positive"`
	Zero     uint64 `json:"zero"`
}

// CompareStatsResult holds side-by-side stats for two sessions.
type CompareStatsResult struct {
	SessionA string        `json:"session_a"`
	SessionB string        `json:"session_b"`
	StatsA   *SessionStats `json:"stats_a"`
	StatsB   *SessionStats `json:"stats_b"`
}

// PageDiffRow represents a single page difference between two crawls.
type PageDiffRow struct {
	URL              string  `json:"url"`
	DiffType         string  `json:"diff_type"`
	StatusCodeA      uint16  `json:"status_code_a"`
	TitleA           string  `json:"title_a"`
	CanonicalA       string  `json:"canonical_a"`
	IsIndexableA     bool    `json:"is_indexable_a"`
	WordCountA       uint32  `json:"word_count_a"`
	DepthA           uint16  `json:"depth_a"`
	PageRankA        float64 `json:"pagerank_a"`
	MetaDescriptionA string  `json:"meta_description_a"`
	H1A              string  `json:"h1_a"`
	StatusCodeB      uint16  `json:"status_code_b"`
	TitleB           string  `json:"title_b"`
	CanonicalB       string  `json:"canonical_b"`
	IsIndexableB     bool    `json:"is_indexable_b"`
	WordCountB       uint32  `json:"word_count_b"`
	DepthB           uint16  `json:"depth_b"`
	PageRankB        float64 `json:"pagerank_b"`
	MetaDescriptionB string  `json:"meta_description_b"`
	H1B              string  `json:"h1_b"`
}

// PageDiffResult wraps paginated page diff results.
type PageDiffResult struct {
	Pages        []PageDiffRow `json:"pages"`
	TotalAdded   uint64        `json:"total_added"`
	TotalRemoved uint64        `json:"total_removed"`
	TotalChanged uint64        `json:"total_changed"`
}

// LinkDiffRow represents a single internal link difference.
type LinkDiffRow struct {
	SourceURL  string `json:"source_url"`
	TargetURL  string `json:"target_url"`
	AnchorText string `json:"anchor_text"`
	DiffType   string `json:"diff_type"`
}

// LinkDiffResult wraps paginated link diff results.
type LinkDiffResult struct {
	Links        []LinkDiffRow `json:"links"`
	TotalAdded   uint64        `json:"total_added"`
	TotalRemoved uint64        `json:"total_removed"`
}

// ExternalLinkCheck represents a single external URL check result.
type ExternalLinkCheck struct {
	CrawlSessionID string    `json:"crawl_session_id"`
	URL            string    `json:"url"`
	StatusCode     uint16    `json:"status_code"`
	Error          string    `json:"error"`
	ContentType    string    `json:"content_type"`
	RedirectURL    string    `json:"redirect_url"`
	ResponseTimeMs uint32    `json:"response_time_ms"`
	CheckedAt      time.Time `json:"checked_at"`
	NSExists       bool      `json:"ns_exists"`
	NSError        string    `json:"ns_error"`
}

// ExternalLinkCheckWithSource extends ExternalLinkCheck with the internal source page info.
type ExternalLinkCheckWithSource struct {
	ExternalLinkCheck
	SourceURL      string  `json:"source_url"`
	SourcePageRank float64 `json:"source_pagerank"`
	SourceDepth    uint16  `json:"source_depth"`
}

// ExternalDomainCheck represents aggregated external check stats per domain.
type ExternalDomainCheck struct {
	Domain        string `json:"domain"`
	TotalURLs     uint64 `json:"total_urls"`
	OK            uint64 `json:"ok"`
	Redirects     uint64 `json:"redirects"`
	ClientErrors  uint64 `json:"client_errors"`
	ServerErrors  uint64 `json:"server_errors"`
	Unreachable   uint64 `json:"unreachable"`
	NSDead        uint64 `json:"ns_dead"`
	AvgResponseMs uint32 `json:"avg_response_ms"`
}

// ExpiredDomain represents a registrable domain where all checked URLs had DNS failures.
type ExpiredDomain struct {
	RegistrableDomain string                `json:"registrable_domain"`
	DeadURLsChecked   uint64                `json:"dead_urls_checked"`
	Sources           []ExpiredDomainSource `json:"sources"`
}

// ExpiredDomainSource represents a source page linking to an expired domain.
type ExpiredDomainSource struct {
	SourceURL string `json:"source_url"`
	TargetURL string `json:"target_url"`
}

// ExpiredDomainsResult wraps paginated expired domain results.
type ExpiredDomainsResult struct {
	Domains []ExpiredDomain `json:"domains"`
	Total   uint64          `json:"total"`
}

// --- Provider Data Models ---

type ProviderDomainMetricsRow struct {
	Provider        string    `json:"provider"`
	Domain          string    `json:"domain"`
	BacklinksTotal  int64     `json:"backlinks_total"`
	RefDomainsTotal int64     `json:"refdomains_total"`
	DomainRank      float64   `json:"domain_rank"`
	OrganicKeywords int64     `json:"organic_keywords"`
	OrganicTraffic  int64     `json:"organic_traffic"`
	OrganicCost     float64   `json:"organic_cost"`
	FetchedAt       time.Time `json:"fetched_at"`
}

type ProviderBacklinkRow struct {
	Provider       string    `json:"provider"`
	Domain         string    `json:"domain"`
	SourceURL      string    `json:"source_url"`
	TargetURL      string    `json:"target_url"`
	AnchorText     string    `json:"anchor_text"`
	SourceDomain   string    `json:"source_domain"`
	LinkType       string    `json:"link_type"`
	TrustFlow      float64   `json:"trust_flow"`
	CitationFlow   float64   `json:"citation_flow"`
	SourceTTFTopic string    `json:"source_ttf_topic"`
	Nofollow       bool      `json:"nofollow"`
	FirstSeen      time.Time `json:"first_seen"`
	LastSeen       time.Time `json:"last_seen"`
	FetchedAt      time.Time `json:"fetched_at"`
}

type ProviderRefDomainRow struct {
	Provider      string    `json:"provider"`
	Domain        string    `json:"domain"`
	RefDomain     string    `json:"ref_domain"`
	BacklinkCount int64     `json:"backlink_count"`
	DomainRank    float64   `json:"domain_rank"`
	FirstSeen     time.Time `json:"first_seen"`
	LastSeen      time.Time `json:"last_seen"`
	FetchedAt     time.Time `json:"fetched_at"`
}

type ProviderRankingRow struct {
	Provider     string    `json:"provider"`
	Domain       string    `json:"domain"`
	Keyword      string    `json:"keyword"`
	URL          string    `json:"url"`
	SearchBase   string    `json:"search_base"`
	Position     uint16    `json:"position"`
	SearchVolume int64     `json:"search_volume"`
	CPC          float64   `json:"cpc"`
	Traffic      float64   `json:"traffic"`
	TrafficPct   float64   `json:"traffic_pct"`
	FetchedAt    time.Time `json:"fetched_at"`
}

type ProviderVisibilityRow struct {
	Provider      string    `json:"provider"`
	Domain        string    `json:"domain"`
	SearchBase    string    `json:"search_base"`
	Date          time.Time `json:"date"`
	Visibility    float64   `json:"visibility"`
	KeywordsCount int64     `json:"keywords_count"`
	FetchedAt     time.Time `json:"fetched_at"`
}

// --- Unified Provider Data ---

type ProviderDataRow struct {
	Provider     string             `json:"provider"`
	DataType     string             `json:"data_type"`
	Domain       string             `json:"domain"`
	ItemURL      string             `json:"item_url"`
	TrustFlow    uint8              `json:"trust_flow"`
	CitationFlow uint8              `json:"citation_flow"`
	DomainRank   float64            `json:"domain_rank"`
	ExtBacklinks int64              `json:"ext_backlinks"`
	RefDomains   int64              `json:"ref_domains"`
	StrData      map[string]string  `json:"str_data"`
	NumData      map[string]float64 `json:"num_data"`
	FetchedAt    time.Time          `json:"fetched_at"`
}

// --- Provider Top Pages & API Calls ---

type TopicalTF struct {
	Topic string `json:"topic"`
	Value uint8  `json:"value"`
}

type ProviderTopPageRow struct {
	Provider         string      `json:"provider"`
	Domain           string      `json:"domain"`
	URL              string      `json:"url"`
	Title            string      `json:"title"`
	TrustFlow        uint8       `json:"trust_flow"`
	CitationFlow     uint8       `json:"citation_flow"`
	ExtBackLinks     int64       `json:"ext_backlinks"`
	RefDomains       int64       `json:"ref_domains"`
	TopicalTrustFlow []TopicalTF `json:"topical_trust_flow"`
	Language         string      `json:"language"`
	FetchedAt        time.Time   `json:"fetched_at"`
}

type ProviderAPICallRow struct {
	ProjectID    string    `json:"project_id"`
	Provider     string    `json:"provider"`
	Endpoint     string    `json:"endpoint"`
	Method       string    `json:"method"`
	StatusCode   uint16    `json:"status_code"`
	DurationMs   uint32    `json:"duration_ms"`
	RowsReturned uint32    `json:"rows_returned"`
	ResponseBody string    `json:"response_body"`
	Error        string    `json:"error"`
	CalledAt     time.Time `json:"called_at"`
}

// PageWithAuthority combines a crawled page with its Majestic authority data.
type PageWithAuthority struct {
	URL          string  `json:"url"`
	Title        string  `json:"title"`
	PageRank     float64 `json:"pagerank"`
	WordCount    uint32  `json:"word_count"`
	StatusCode   uint16  `json:"status_code"`
	Depth        uint16  `json:"depth"`
	TrustFlow    *uint8  `json:"trust_flow"`
	CitationFlow *uint8  `json:"citation_flow"`
	ExtBackLinks *int64  `json:"ext_backlinks"`
	RefDomains   *int64  `json:"ref_domains"`
}

// PageResourceCheck represents a single page resource check result.
type PageResourceCheck struct {
	CrawlSessionID string    `json:"crawl_session_id"`
	URL            string    `json:"url"`
	ResourceType   string    `json:"resource_type"`
	IsInternal     bool      `json:"is_internal"`
	StatusCode     uint16    `json:"status_code"`
	Error          string    `json:"error"`
	ContentType    string    `json:"content_type"`
	RedirectURL    string    `json:"redirect_url"`
	ResponseTimeMs uint32    `json:"response_time_ms"`
	CheckedAt      time.Time `json:"checked_at"`
	PageCount      uint64    `json:"page_count,omitempty"`
}

// PageResourceRef links a page to a resource it uses.
type PageResourceRef struct {
	CrawlSessionID string `json:"crawl_session_id"`
	PageURL        string `json:"page_url"`
	ResourceURL    string `json:"resource_url"`
	ResourceType   string `json:"resource_type"`
	IsInternal     bool   `json:"is_internal"`
}

// NearDuplicatePair represents two pages with near-identical content.
type NearDuplicatePair struct {
	URLa       string  `json:"url_a"`
	URLb       string  `json:"url_b"`
	TitleA     string  `json:"title_a"`
	TitleB     string  `json:"title_b"`
	CanonicalA string  `json:"canonical_a"`
	CanonicalB string  `json:"canonical_b"`
	WordCountA uint32  `json:"word_count_a"`
	WordCountB uint32  `json:"word_count_b"`
	Similarity float64 `json:"similarity"` // 0–1, 1 = exact duplicate
}

// NearDuplicatesResult wraps paginated near-duplicate results.
type NearDuplicatesResult struct {
	Pairs []NearDuplicatePair `json:"pairs"`
	Total uint64              `json:"total"`
}

// RedirectPageRow represents a redirect page with inbound internal link count.
type RedirectPageRow struct {
	URL                  string `json:"url"`
	StatusCode           uint16 `json:"status_code"`
	FinalURL             string `json:"final_url"`
	InboundInternalLinks uint64 `json:"inbound_internal_links"`
}

// WeightedPageRankPage represents a page with weighted PageRank combining internal PR and SEObserver data.
type WeightedPageRankPage struct {
	URL              string  `json:"url"`
	PageRank         float64 `json:"pagerank"`
	WeightedPR       float64 `json:"weighted_pr"`
	TrustFlow        *uint8  `json:"trust_flow"`
	CitationFlow     *uint8  `json:"citation_flow"`
	ExtBackLinks     *int64  `json:"ext_backlinks"`
	RefDomains       *int64  `json:"ref_domains"`
	Depth            uint16  `json:"depth"`
	InternalLinksOut uint32  `json:"internal_links_out"`
	StatusCode       uint16  `json:"status_code"`
	Title            string  `json:"title"`
	TTFTopic         *string `json:"ttf_topic"`
}

// WeightedPageRankResult wraps paginated weighted PageRank results.
type WeightedPageRankResult struct {
	Pages    []WeightedPageRankPage `json:"pages"`
	Total    uint64                 `json:"total"`
	Eligible uint64                 `json:"eligible_pages"`
	Positive uint64                 `json:"positive_pages"`
	Zero     uint64                 `json:"zero_pages"`
	Evidence *PageRankEvidence      `json:"evidence,omitempty"`
}

// InterlinkingOpportunity represents a pair of semantically similar pages without an existing internal link.
type InterlinkingOpportunity struct {
	CrawlSessionID   string  `json:"crawl_session_id"`
	SourceURL        string  `json:"source_url"`
	TargetURL        string  `json:"target_url"`
	Similarity       float64 `json:"similarity"`
	Method           string  `json:"method"`
	SourceTitle      string  `json:"source_title"`
	TargetTitle      string  `json:"target_title"`
	SourcePageRank   float64 `json:"source_pagerank"`
	TargetPageRank   float64 `json:"target_pagerank"`
	SourceWordCount  uint32  `json:"source_word_count"`
	TargetWordCount  uint32  `json:"target_word_count"`
	OpportunityScore float64 `json:"opportunity_score"`
	Category         string  `json:"category"` // "opportunity" | "cannibalization"
}

// SimulationMeta holds metadata for a PageRank simulation with virtual links.
type SimulationMeta struct {
	ID                string    `json:"id"`
	CrawlSessionID    string    `json:"crawl_session_id"`
	VirtualLinksCount uint32    `json:"virtual_links_count"`
	PagesImproved     uint32    `json:"pages_improved"`
	PagesDeclined     uint32    `json:"pages_declined"`
	AvgDiff           float64   `json:"avg_diff"`
	MaxDiff           float64   `json:"max_diff"`
	ComputedAt        time.Time `json:"computed_at"`
}

// SimulationResultRow holds per-page PageRank diff for a simulation.
type SimulationResultRow struct {
	URL            string  `json:"url"`
	PageRankBefore float64 `json:"pagerank_before"`
	PageRankAfter  float64 `json:"pagerank_after"`
	PageRankDiff   float64 `json:"pagerank_diff"`
}

// PageMetadata holds per-URL metadata for interlinking analysis.
type PageMetadata struct {
	Title         string
	Lang          string
	PageRank      float64
	WordCount     uint32
	Canonical     string
	CanonicalSelf bool
}

// PageRankGraph holds the link graph data needed for PageRank computation.
type PageRankGraph struct {
	N             uint32
	OutLinks      [][]uint32 // internal dofollow edges only
	TotalOutLinks []uint32   // all outlinks (internal+external, all rel)
	URLToID       map[string]uint32
	IDToURL       []string
}

// VirtualLink represents a proposed internal link to add to the graph.
type VirtualLink struct {
	SourceURL string `json:"source"`
	TargetURL string `json:"target"`
}

// HreflangIssue represents a single hreflang validation issue.
type HreflangIssue struct {
	IssueType  string `json:"issue_type"`
	SourceURL  string `json:"source_url"`
	SourceLang string `json:"source_lang"`
	TargetURL  string `json:"target_url"`
	TargetLang string `json:"target_lang"`
	Detail     string `json:"detail"`
}

// HreflangValidationResult wraps paginated hreflang validation results.
type HreflangValidationResult struct {
	Issues  []HreflangIssue   `json:"issues"`
	Total   uint64            `json:"total"`
	Summary map[string]uint64 `json:"summary"`
}

// ResourceTypeSummary holds aggregated stats for one resource type.
type ResourceTypeSummary struct {
	ResourceType string `json:"resource_type"`
	Total        uint64 `json:"total"`
	Internal     uint64 `json:"internal"`
	External     uint64 `json:"external"`
	OK           uint64 `json:"ok"`
	Errors       uint64 `json:"errors"`
}
