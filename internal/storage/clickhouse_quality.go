package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	defaultQualityEvaluatorRevision = "quality-evaluator-v1"
	defaultQualityRulesRevision     = "quality-rules-v1"
	legacyQualityImportSource       = "legacy_import"
)

var (
	qualityEvaluationLocks     sync.Map // map[string]*sync.Mutex, keyed by session ID
	qualityPromotionEventLocks sync.Map // map[string]*sync.Mutex, keyed by project/session
	qualityActionEventLocks    sync.Map // map[string]*sync.Mutex, keyed by session/action
)

// QualityEvaluationConflictError is returned when a caller's optimistic
// concurrency expectation no longer matches the durable current pointer.
type QualityEvaluationConflictError struct {
	SessionID        string
	ExpectedRevision string
	CurrentRevision  string
}

func (e *QualityEvaluationConflictError) Error() string {
	return fmt.Sprintf("quality evaluation revision conflict for session %s: expected %s, current %s", e.SessionID, e.ExpectedRevision, e.CurrentRevision)
}

func qualityEvaluationLock(sessionID string) *sync.Mutex {
	lock, _ := qualityEvaluationLocks.LoadOrStore(sessionID, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func qualityPromotionEventLock(projectID, sessionID string) *sync.Mutex {
	lock, _ := qualityPromotionEventLocks.LoadOrStore(projectID+"\x00"+sessionID, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func qualityActionEventLock(sessionID, actionID string) *sync.Mutex {
	lock, _ := qualityActionEventLocks.LoadOrStore(sessionID+"\x00"+actionID, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

// UpsertCrawlQualityResult remains for callers compiled against the original
// storage interface. New writes use immutable revisions and never delete the
// legacy findings partition.
func (s *Store) UpsertCrawlQualityResult(ctx context.Context, result CrawlQualityResult) error {
	_, _, err := s.PublishCrawlQualityEvaluation(ctx, result, "")
	return err
}

// PublishCrawlQualityEvaluation stores an immutable evaluation plus its
// immutable findings, verifies both through FINAL, and only then publishes the
// separate current pointer. expectedCurrentRevision is optional; when present
// it implements optimistic concurrency for scheduler and repair callers.
func (s *Store) PublishCrawlQualityEvaluation(ctx context.Context, result CrawlQualityResult, expectedCurrentRevision string) (bool, *CrawlQualityResult, error) {
	if !isValidUUID(result.SessionID) {
		return false, nil, fmt.Errorf("invalid quality session ID: %s", result.SessionID)
	}
	lock := qualityEvaluationLock(result.SessionID)
	lock.Lock()
	defer lock.Unlock()

	result = normalizeQualityEvaluation(result)
	current, err := s.getCrawlQualityCurrentPointer(ctx, result.SessionID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, nil, err
	}
	if expectedCurrentRevision != "" && (current == nil || current.EvaluationRevision != expectedCurrentRevision) {
		actual := ""
		if current != nil {
			actual = current.EvaluationRevision
		}
		return false, nil, &QualityEvaluationConflictError{SessionID: result.SessionID, ExpectedRevision: expectedCurrentRevision, CurrentRevision: actual}
	}

	// Same immutable revision is idempotent. It can still repair a missing or
	// outdated current pointer after a prior crash before pointer publication.
	existing, err := s.getCrawlQualityEvaluation(ctx, result.SessionID, result.EvaluationRevision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, nil, err
	}
	if existing, err = s.ensureCrawlQualityEvaluationFacts(ctx, result); err != nil {
		return false, nil, err
	}

	if current != nil && current.EvaluationRevision == result.EvaluationRevision {
		return false, existing, nil
	}
	if err := s.publishCrawlQualityCurrentPointer(ctx, result.SessionID, result.EvaluationRevision); err != nil {
		return false, nil, err
	}
	published, err := s.getCrawlQualityCurrentPointer(ctx, result.SessionID)
	if err != nil {
		return false, nil, fmt.Errorf("reading quality pointer after publication: %w", err)
	}
	if published.EvaluationRevision != result.EvaluationRevision {
		return false, nil, &QualityEvaluationConflictError{SessionID: result.SessionID, ExpectedRevision: result.EvaluationRevision, CurrentRevision: published.EvaluationRevision}
	}
	return true, existing, nil
}

func normalizeQualityEvaluation(result CrawlQualityResult) CrawlQualityResult {
	if result.EvaluatedAt.IsZero() {
		result.EvaluatedAt = time.Now().UTC()
	}
	if result.Source == "" {
		result.Source = "runtime_evaluation"
	}
	if result.EvaluatorRevision == "" {
		result.EvaluatorRevision = defaultQualityEvaluatorRevision
	}
	if result.RulesRevision == "" {
		result.RulesRevision = defaultQualityRulesRevision
	}
	if result.PromotionStatus == "" {
		result.PromotionStatus = "not_attempted"
	}
	result.StaleReasons = uniqueSortedStrings(result.StaleReasons)
	result.Findings = normalizedQualityFindings(result)
	result.FindingCount = uint32(len(result.Findings))
	if result.EvaluationRevision == "" {
		result.EvaluationRevision = deterministicQualityEvaluationRevision(result)
	}
	for i := range result.Findings {
		result.Findings[i].EvaluationRevision = result.EvaluationRevision
	}
	return result
}

func normalizedQualityFindings(result CrawlQualityResult) []CrawlQualityFinding {
	findings := append([]CrawlQualityFinding(nil), result.Findings...)
	for i := range findings {
		findings[i].SessionID = result.SessionID
		findings[i].ProjectID = result.ProjectID
		if findings[i].CreatedAt.IsZero() {
			findings[i].CreatedAt = result.EvaluatedAt
		}
	}
	sort.SliceStable(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		return qualityFindingSortKey(a) < qualityFindingSortKey(b)
	})
	for i := range findings {
		findings[i].FindingIndex = uint32(i)
		findings[i].EvaluationRevision = result.EvaluationRevision
	}
	return findings
}

func qualityFindingSortKey(f CrawlQualityFinding) string {
	return strings.Join([]string{
		fmt.Sprintf("%t", f.Blocking), f.Severity, f.FindingType, f.Metric, f.Message,
		fmt.Sprintf("%.12g", f.CurrentValue), fmt.Sprintf("%.12g", f.BaselineValue), fmt.Sprintf("%.12g", f.ThresholdValue),
	}, "\x00")
}

func uniqueSortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	values = append([]string(nil), values...)
	for i := range values {
		values[i] = strings.TrimSpace(values[i])
	}
	sort.Strings(values)
	return uniqueNonEmptyStrings(values)
}

// deterministicQualityEvaluationRevision is the canonical SHA-256 identity of
// result and sorted findings, converted to a stable UUID for ClickHouse. It is
// used for legacy import and idempotent repeated evaluation alike.
func deterministicQualityEvaluationRevision(result CrawlQualityResult) string {
	type canonicalFinding struct {
		Severity, FindingType, Message, Metric string
		Current, Baseline, Threshold           float64
		Blocking                               bool
	}
	type canonicalResult struct {
		SessionID, ProjectID, BaselineSessionID, BaselineEvaluationRevision string
		Source, EvaluatorRevision, RulesRevision                            string
		PageRankEvidenceRevision, PageRankEvidenceSource                    string
		PageRankEvidenceStatus, PageRankPredicateVersion                    string
		PageRankEligible, PageRankPositive, PageRankZero                    uint64
		Stale                                                               bool
		FindingCount                                                        uint32
		StaleReasons, MetricsJSON                                           string
		PromotionStatus, Status, Summary                                    string
		Score                                                               uint8
		Trusted, IsFullCrawl                                                bool
		Findings                                                            []canonicalFinding
	}
	metrics, _ := json.Marshal(result.Metrics)
	payload := canonicalResult{
		SessionID: result.SessionID, ProjectID: result.ProjectID, BaselineSessionID: result.BaselineSessionID,
		BaselineEvaluationRevision: result.BaselineEvaluationRevision, Source: result.Source,
		EvaluatorRevision: result.EvaluatorRevision, RulesRevision: result.RulesRevision,
		PageRankEvidenceRevision: result.PageRankEvidenceRevision, PageRankEvidenceSource: result.PageRankEvidenceSource,
		PageRankEvidenceStatus: result.PageRankEvidenceStatus, PageRankPredicateVersion: result.PageRankPredicateVersion,
		PageRankEligible: result.PageRankEligible, PageRankPositive: result.PageRankPositive, PageRankZero: result.PageRankZero,
		Stale: result.Stale, FindingCount: result.FindingCount, StaleReasons: strings.Join(uniqueSortedStrings(result.StaleReasons), "\x00"),
		MetricsJSON: string(metrics), PromotionStatus: result.PromotionStatus, Status: result.Status,
		Summary: result.Summary, Score: result.Score, Trusted: result.Trusted, IsFullCrawl: result.IsFullCrawl,
	}
	for _, f := range result.Findings {
		payload.Findings = append(payload.Findings, canonicalFinding{f.Severity, f.FindingType, f.Message, f.Metric, f.CurrentValue, f.BaselineValue, f.ThresholdValue, f.Blocking})
	}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return uuid.NewSHA1(uuid.NameSpaceURL, sum[:]).String()
}

func (s *Store) insertCrawlQualityEvaluation(ctx context.Context, result CrawlQualityResult) error {
	metricsJSON, err := json.Marshal(result.Metrics)
	if err != nil {
		return fmt.Errorf("encoding quality metrics: %w", err)
	}
	if err := s.conn.Exec(ctx, `
		INSERT INTO crawlobserver.crawl_quality_evaluations (
			session_id, evaluation_revision, project_id, baseline_session_id, baseline_evaluation_revision,
			source, evaluator_revision, rules_revision, pagerank_evidence_revision,
			pagerank_evidence_source, pagerank_evidence_status, pagerank_predicate_version,
			pagerank_eligible, pagerank_positive, pagerank_zero, stale, stale_reasons, finding_count,
			promotion_status, status, score, trusted, is_full_crawl, summary, metrics, evaluated_at
		) VALUES (?, toUUID(?), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		result.SessionID, result.EvaluationRevision, result.ProjectID, result.BaselineSessionID, result.BaselineEvaluationRevision,
		result.Source, result.EvaluatorRevision, result.RulesRevision, result.PageRankEvidenceRevision,
		result.PageRankEvidenceSource, result.PageRankEvidenceStatus, result.PageRankPredicateVersion,
		result.PageRankEligible, result.PageRankPositive, result.PageRankZero, result.Stale, result.StaleReasons, result.FindingCount,
		result.PromotionStatus, result.Status, result.Score, result.Trusted, result.IsFullCrawl, result.Summary, string(metricsJSON), result.EvaluatedAt,
	); err != nil {
		return fmt.Errorf("inserting quality evaluation: %w", err)
	}
	return nil
}

func (s *Store) insertCrawlQualityFindings(ctx context.Context, result CrawlQualityResult) error {
	for _, finding := range result.Findings {
		if err := s.conn.Exec(ctx, `
			INSERT INTO crawlobserver.crawl_quality_evaluation_findings (
				session_id, evaluation_revision, finding_index, project_id, severity, finding_type,
				message, metric, current_value, baseline_value, threshold_value, blocking, created_at
			) VALUES (?, toUUID(?), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			finding.SessionID, result.EvaluationRevision, finding.FindingIndex, finding.ProjectID, finding.Severity,
			finding.FindingType, finding.Message, finding.Metric, finding.CurrentValue, finding.BaselineValue,
			finding.ThresholdValue, finding.Blocking, finding.CreatedAt,
		); err != nil {
			return fmt.Errorf("inserting quality finding %d: %w", finding.FindingIndex, err)
		}
	}
	return nil
}

// ensureCrawlQualityEvaluationFacts is the crash-recovery barrier before a
// pointer can be published. A prior process may have inserted the evaluation
// row and crashed during finding insertion; an existing evaluation row is not
// sufficient evidence of a complete immutable generation.
func (s *Store) ensureCrawlQualityEvaluationFacts(ctx context.Context, result CrawlQualityResult) (*CrawlQualityResult, error) {
	existing, err := s.getCrawlQualityEvaluation(ctx, result.SessionID, result.EvaluationRevision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if existing == nil {
		if err := s.insertCrawlQualityEvaluation(ctx, result); err != nil {
			return nil, err
		}
	} else if existing.FindingCount != uint32(len(result.Findings)) {
		return nil, fmt.Errorf("quality evaluation %s finding count %d does not match expected %d", result.EvaluationRevision, existing.FindingCount, len(result.Findings))
	}

	present, err := s.qualityFindingIndexes(ctx, result.SessionID, result.EvaluationRevision)
	if err != nil {
		return nil, err
	}
	if len(present) > len(result.Findings) {
		return nil, fmt.Errorf("quality evaluation %s has %d findings; expected %d", result.EvaluationRevision, len(present), len(result.Findings))
	}
	missing := make([]CrawlQualityFinding, 0, len(result.Findings))
	for index, finding := range result.Findings {
		if _, ok := present[uint32(index)]; !ok {
			missing = append(missing, finding)
		}
	}
	for index := range present {
		if int(index) >= len(result.Findings) {
			return nil, fmt.Errorf("quality evaluation %s has unexpected finding index %d", result.EvaluationRevision, index)
		}
	}
	if len(missing) > 0 {
		partial := result
		partial.Findings = missing
		if err := s.insertCrawlQualityFindings(ctx, partial); err != nil {
			return nil, err
		}
	}
	if err := s.verifyCrawlQualityEvaluation(ctx, result); err != nil {
		return nil, err
	}
	return s.getCrawlQualityEvaluation(ctx, result.SessionID, result.EvaluationRevision)
}

func (s *Store) qualityFindingIndexes(ctx context.Context, sessionID, evaluationRevision string) (map[uint32]struct{}, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT finding_index
		FROM crawlobserver.crawl_quality_evaluation_findings
		WHERE session_id = ? AND evaluation_revision = toUUID(?)
		ORDER BY finding_index`, sessionID, evaluationRevision)
	if err != nil {
		return nil, fmt.Errorf("reading quality finding indexes: %w", err)
	}
	defer rows.Close()
	indexes := make(map[uint32]struct{})
	for rows.Next() {
		var index uint32
		if err := rows.Scan(&index); err != nil {
			return nil, err
		}
		if _, exists := indexes[index]; exists {
			return nil, fmt.Errorf("quality evaluation %s has duplicate finding index %d", evaluationRevision, index)
		}
		indexes[index] = struct{}{}
	}
	return indexes, rows.Err()
}

func (s *Store) verifyCrawlQualityEvaluation(ctx context.Context, result CrawlQualityResult) error {
	var evaluations, findings uint64
	var findingCount uint32
	if err := s.conn.QueryRow(ctx, `
		SELECT count() FROM crawlobserver.crawl_quality_evaluations
		WHERE session_id = ? AND evaluation_revision = toUUID(?)`, result.SessionID, result.EvaluationRevision).Scan(&evaluations); err != nil {
		return fmt.Errorf("reading quality evaluation: %w", err)
	}
	if err := s.conn.QueryRow(ctx, `
		SELECT finding_count FROM crawlobserver.crawl_quality_evaluations
		WHERE session_id = ? AND evaluation_revision = toUUID(?)`, result.SessionID, result.EvaluationRevision).Scan(&findingCount); err != nil {
		return fmt.Errorf("reading quality evaluation finding count: %w", err)
	}
	if err := s.conn.QueryRow(ctx, `
		SELECT count() FROM crawlobserver.crawl_quality_evaluation_findings
		WHERE session_id = ? AND evaluation_revision = toUUID(?)`, result.SessionID, result.EvaluationRevision).Scan(&findings); err != nil {
		return fmt.Errorf("reading quality findings: %w", err)
	}
	if evaluations != 1 || findingCount != result.FindingCount || findings != uint64(result.FindingCount) {
		return fmt.Errorf("quality evaluation readback mismatch: evaluations=%d finding_count=%d findings=%d want=%d", evaluations, findingCount, findings, result.FindingCount)
	}
	return nil
}

func (s *Store) publishCrawlQualityCurrentPointer(ctx context.Context, sessionID, revision string) error {
	pointerSequence := uint64(1)
	publishedAt := time.Now().UTC().Truncate(time.Second)
	current, err := s.getCrawlQualityCurrentPointer(ctx, sessionID)
	if err == nil {
		if current.PointerSequence == ^uint64(0) {
			return errors.New("quality current pointer sequence exhausted")
		}
		pointerSequence = current.PointerSequence + 1
		currentPublishedAt := current.PublishedAt.UTC().Truncate(time.Second)
		if !publishedAt.After(currentPublishedAt) {
			publishedAt = currentPublishedAt.Add(time.Second)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("reading quality pointer sequence: %w", err)
	}
	return s.conn.Exec(ctx, `
		INSERT INTO crawlobserver.crawl_quality_current_pointers (session_id, evaluation_revision, pointer_sequence, published_at)
		VALUES (?, toUUID(?), ?, ?)`, sessionID, revision, pointerSequence, publishedAt)
}

func (s *Store) getCrawlQualityCurrentPointer(ctx context.Context, sessionID string) (*CrawlQualityCurrentPointer, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT toString(session_id), toString(evaluation_revision), pointer_sequence, published_at
		FROM crawlobserver.crawl_quality_current_pointers
		WHERE session_id = ?
		ORDER BY pointer_sequence DESC, published_at DESC, evaluation_revision DESC
		LIMIT 1`, sessionID)
	var pointer CrawlQualityCurrentPointer
	if err := row.Scan(&pointer.SessionID, &pointer.EvaluationRevision, &pointer.PointerSequence, &pointer.PublishedAt); err != nil {
		return nil, err
	}
	return &pointer, nil
}

// EnsureLegacyQualityImported makes legacy quality data observable through the
// immutable revision model. It reads a canonical result and complete, sorted
// legacy findings, does not mutate legacy rows, and is idempotent by revision.
func (s *Store) EnsureLegacyQualityImported(ctx context.Context, sessionID string) (*CrawlQualityResult, error) {
	if !isValidUUID(sessionID) {
		return nil, fmt.Errorf("invalid quality session ID: %s", sessionID)
	}
	lock := qualityEvaluationLock(sessionID)
	lock.Lock()
	defer lock.Unlock()

	if pointer, err := s.getCrawlQualityCurrentPointer(ctx, sessionID); err == nil {
		return s.getCrawlQualityEvaluationWithFindings(ctx, sessionID, pointer.EvaluationRevision)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	legacy, err := s.getLegacyCrawlQualityResult(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	findings, err := s.getLegacyCrawlQualityFindings(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	legacy.Source = legacyQualityImportSource
	legacy.Findings = findings
	legacy = normalizeQualityEvaluation(legacy)

	// The import shares the publish recovery barrier but stays in this lock so
	// concurrent legacy reads cannot point at a partial findings generation.
	if _, err := s.ensureCrawlQualityEvaluationFacts(ctx, legacy); err != nil {
		return nil, err
	}
	if err := s.publishCrawlQualityCurrentPointer(ctx, sessionID, legacy.EvaluationRevision); err != nil {
		return nil, err
	}
	return s.getCrawlQualityEvaluationWithFindings(ctx, sessionID, legacy.EvaluationRevision)
}

// GetCrawlQualityResult resolves the separately published current pointer. A
// pre-Phase-25.1 session is imported lazily only when no pointer exists.
func (s *Store) GetCrawlQualityResult(ctx context.Context, sessionID string) (*CrawlQualityResult, error) {
	pointer, err := s.getCrawlQualityCurrentPointer(ctx, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return s.EnsureLegacyQualityImported(ctx, sessionID)
	}
	if err != nil {
		return nil, err
	}
	return s.getCrawlQualityEvaluationWithFindings(ctx, sessionID, pointer.EvaluationRevision)
}

// ListCrawlQualityHistory returns immutable generations with their matching
// findings. The current pointer is deliberately not used to discard history.
func (s *Store) ListCrawlQualityHistory(ctx context.Context, sessionID string) ([]CrawlQualityResult, error) {
	if _, err := s.getCrawlQualityCurrentPointer(ctx, sessionID); errors.Is(err, sql.ErrNoRows) {
		if _, importErr := s.EnsureLegacyQualityImported(ctx, sessionID); importErr != nil {
			return nil, importErr
		}
	} else if err != nil {
		return nil, err
	}
	rows, err := s.conn.Query(ctx, `
		SELECT toString(session_id), toString(evaluation_revision), project_id, baseline_session_id,
			baseline_evaluation_revision, source, evaluator_revision, rules_revision,
			pagerank_evidence_revision, pagerank_evidence_source, pagerank_evidence_status,
			pagerank_predicate_version, pagerank_eligible, pagerank_positive, pagerank_zero,
			stale, stale_reasons, finding_count, promotion_status, status, score, trusted, is_full_crawl,
			summary, metrics, evaluated_at
		FROM crawlobserver.crawl_quality_evaluations
		WHERE session_id = ?
		ORDER BY evaluated_at DESC, evaluation_revision DESC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var history []CrawlQualityResult
	for rows.Next() {
		result, err := scanQualityEvaluation(rows)
		if err != nil {
			return nil, err
		}
		result.Findings, err = s.GetCrawlQualityFindingsForRevision(ctx, sessionID, result.EvaluationRevision)
		if err != nil {
			return nil, err
		}
		history = append(history, *result)
	}
	if history == nil {
		history = []CrawlQualityResult{}
	}
	return history, rows.Err()
}

func (s *Store) getCrawlQualityEvaluation(ctx context.Context, sessionID, evaluationRevision string) (*CrawlQualityResult, error) {
	row := s.conn.QueryRow(ctx, qualityEvaluationSelect+`
		WHERE session_id = ? AND evaluation_revision = toUUID(?)`, sessionID, evaluationRevision)
	return scanQualityEvaluation(row)
}

func (s *Store) getCrawlQualityEvaluationWithFindings(ctx context.Context, sessionID, evaluationRevision string) (*CrawlQualityResult, error) {
	result, err := s.getCrawlQualityEvaluation(ctx, sessionID, evaluationRevision)
	if err != nil {
		return nil, err
	}
	result.Findings, err = s.GetCrawlQualityFindingsForRevision(ctx, sessionID, evaluationRevision)
	if err != nil {
		return nil, err
	}
	if uint32(len(result.Findings)) != result.FindingCount {
		return nil, fmt.Errorf("quality evaluation %s is incomplete: findings=%d expected=%d", evaluationRevision, len(result.Findings), result.FindingCount)
	}
	return result, nil
}

const qualityEvaluationSelect = `
	SELECT toString(session_id), toString(evaluation_revision), project_id, baseline_session_id,
		baseline_evaluation_revision, source, evaluator_revision, rules_revision,
		pagerank_evidence_revision, pagerank_evidence_source, pagerank_evidence_status,
		pagerank_predicate_version, pagerank_eligible, pagerank_positive, pagerank_zero,
		stale, stale_reasons, finding_count, promotion_status, status, score, trusted, is_full_crawl,
		summary, metrics, evaluated_at
	FROM crawlobserver.crawl_quality_evaluations`

// GetCrawlQualityFindings returns the findings attached to the current
// immutable evaluation rather than a mix of historical generations.
func (s *Store) GetCrawlQualityFindings(ctx context.Context, sessionID string) ([]CrawlQualityFinding, error) {
	result, err := s.GetCrawlQualityResult(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return result.Findings, nil
}

func (s *Store) GetCrawlQualityFindingsForRevision(ctx context.Context, sessionID, evaluationRevision string) ([]CrawlQualityFinding, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT toString(session_id), toString(evaluation_revision), finding_index, project_id,
			severity, finding_type, message, metric, current_value, baseline_value,
			threshold_value, blocking, created_at
		FROM crawlobserver.crawl_quality_evaluation_findings
		WHERE session_id = ? AND evaluation_revision = toUUID(?)
		ORDER BY finding_index ASC`, sessionID, evaluationRevision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	findings := []CrawlQualityFinding{}
	for rows.Next() {
		var finding CrawlQualityFinding
		if err := rows.Scan(&finding.SessionID, &finding.EvaluationRevision, &finding.FindingIndex,
			&finding.ProjectID, &finding.Severity, &finding.FindingType, &finding.Message,
			&finding.Metric, &finding.CurrentValue, &finding.BaselineValue, &finding.ThresholdValue,
			&finding.Blocking, &finding.CreatedAt); err != nil {
			return nil, err
		}
		findings = append(findings, finding)
	}
	return findings, rows.Err()
}

func (s *Store) getLegacyCrawlQualityResult(ctx context.Context, sessionID string) (CrawlQualityResult, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT session_id, project_id, baseline_session_id, status, score, trusted,
			is_full_crawl, summary, metrics, evaluated_at
		FROM crawlobserver.crawl_quality_results FINAL
		WHERE session_id = ?`, sessionID)
	result, err := scanLegacyQualityResult(row)
	if err != nil {
		return CrawlQualityResult{}, err
	}
	return *result, nil
}

func (s *Store) getLegacyCrawlQualityFindings(ctx context.Context, sessionID string) ([]CrawlQualityFinding, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT session_id, project_id, severity, finding_type, message, metric,
			current_value, baseline_value, threshold_value, blocking, created_at
		FROM crawlobserver.crawl_quality_findings
		WHERE session_id = ?
		ORDER BY blocking DESC, severity DESC, finding_type ASC, metric ASC, message ASC, created_at ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	findings := []CrawlQualityFinding{}
	for rows.Next() {
		var finding CrawlQualityFinding
		if err := rows.Scan(&finding.SessionID, &finding.ProjectID, &finding.Severity, &finding.FindingType,
			&finding.Message, &finding.Metric, &finding.CurrentValue, &finding.BaselineValue,
			&finding.ThresholdValue, &finding.Blocking, &finding.CreatedAt); err != nil {
			return nil, err
		}
		findings = append(findings, finding)
	}
	return findings, rows.Err()
}

// CrawlQualityResultsForSessions returns the pointer-selected results. Legacy
// sessions are lazily imported so dashboard/current snapshot consumers do not
// silently lose their historical trusted baselines.
func (s *Store) CrawlQualityResultsForSessions(ctx context.Context, sessionIDs []string) (map[string]CrawlQualityResult, error) {
	result := make(map[string]CrawlQualityResult, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		quality, err := s.GetCrawlQualityResult(ctx, sessionID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		result[sessionID] = *quality
	}
	return result, nil
}

func (s *Store) LatestTrustedFullCrawlSession(ctx context.Context, projectID, excludeSessionID string) (*CrawlSession, error) {
	session, err := s.latestTrustedFullCrawlSessionFromPointers(ctx, projectID, excludeSessionID)
	if err == nil || !errors.Is(err, sql.ErrNoRows) {
		return session, err
	}

	// The first current-snapshot request after the format migration can precede
	// the scheduler. Import matching legacy trusted results before retrying so a
	// valid historical baseline is not hidden merely because it lacks a pointer.
	rows, legacyErr := s.conn.Query(ctx, `
		SELECT toString(session_id)
		FROM crawlobserver.crawl_quality_results FINAL
		WHERE project_id = ? AND trusted = true AND is_full_crawl = true AND session_id != ?`, projectID, excludeSessionID)
	if legacyErr != nil {
		return nil, legacyErr
	}
	var legacySessionIDs []string
	for rows.Next() {
		var sessionID string
		if scanErr := rows.Scan(&sessionID); scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		legacySessionIDs = append(legacySessionIDs, sessionID)
	}
	if legacyErr = rows.Err(); legacyErr != nil {
		rows.Close()
		return nil, legacyErr
	}
	rows.Close()
	for _, sessionID := range legacySessionIDs {
		if _, importErr := s.EnsureLegacyQualityImported(ctx, sessionID); importErr != nil {
			return nil, importErr
		}
	}
	return s.latestTrustedFullCrawlSessionFromPointers(ctx, projectID, excludeSessionID)
}

func (s *Store) latestTrustedFullCrawlSessionFromPointers(ctx context.Context, projectID, excludeSessionID string) (*CrawlSession, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT cs.id, cs.started_at, cs.finished_at, cs.status, cs.seed_urls, cs.config,
			cs.pages_crawled, cs.user_agent, cs.project_id, cs.label
		FROM crawlobserver.crawl_sessions AS cs FINAL
		INNER JOIN crawlobserver.crawl_quality_current_pointers AS pointer FINAL ON pointer.session_id = cs.id
		INNER JOIN crawlobserver.crawl_quality_evaluations AS qr
			ON qr.session_id = pointer.session_id AND qr.evaluation_revision = pointer.evaluation_revision
		WHERE cs.project_id = ? AND qr.project_id = ? AND qr.trusted = true
			AND qr.is_full_crawl = true AND cs.id != ?
		ORDER BY cs.started_at DESC
		LIMIT 1`, projectID, projectID, excludeSessionID)
	var session CrawlSession
	if err := row.Scan(&session.ID, &session.StartedAt, &session.FinishedAt, &session.Status,
		&session.SeedURLs, &session.Config, &session.PagesCrawled, &session.UserAgent, &session.ProjectID, &session.Label); err != nil {
		return nil, err
	}
	return &session, nil
}

// RecordQualityPromotionEvent appends an auditable promotion decision. The
// same decision is idempotent so retry recovery cannot duplicate an event.
func (s *Store) RecordQualityPromotionEvent(ctx context.Context, event CrawlQualityPromotionEvent) (bool, *CrawlQualityPromotionEvent, error) {
	if !isValidUUID(event.SessionID) || !isValidUUID(event.EvaluationRevision) {
		return false, nil, fmt.Errorf("valid session and evaluation revisions are required")
	}
	lock := qualityPromotionEventLock(event.ProjectID, event.SessionID)
	lock.Lock()
	defer lock.Unlock()

	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	if event.PromotionID == "" {
		event.PromotionID = uuid.NewString()
	}
	if !isValidUUID(event.PromotionID) {
		return false, nil, fmt.Errorf("invalid quality promotion ID: %s", event.PromotionID)
	}
	if existing, err := s.LatestQualityPromotionEvent(ctx, event.ProjectID, event.SessionID); err == nil &&
		existing.EvaluationRevision == event.EvaluationRevision && existing.PageRankEvidenceRevision == event.PageRankEvidenceRevision &&
		existing.Status == event.Status {
		return false, existing, nil
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, nil, err
	}
	sequence, err := s.nextQualityPromotionEventSequence(ctx, event.ProjectID, event.SessionID)
	if err != nil {
		return false, nil, err
	}
	event.EventSequence = sequence
	if err := s.conn.Exec(ctx, `
		INSERT INTO crawlobserver.crawl_quality_promotion_events (
			project_id, session_id, promotion_id, event_sequence, evaluation_revision, pagerank_evidence_revision,
			baseline_session_id, baseline_evaluation_revision, evaluator_revision, rules_revision,
			status, reason, detail, occurred_at
		) VALUES (?, ?, toUUID(?), ?, toUUID(?), ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ProjectID, event.SessionID, event.PromotionID, event.EventSequence, event.EvaluationRevision, event.PageRankEvidenceRevision,
		event.BaselineSessionID, event.BaselineEvaluationRevision, event.EvaluatorRevision, event.RulesRevision, event.Status,
		sanitizeQualityAuditText(event.Reason), sanitizeQualityAuditText(event.Detail), event.OccurredAt,
	); err != nil {
		return false, nil, fmt.Errorf("recording quality promotion event: %w", err)
	}
	return true, &event, nil
}

func (s *Store) nextQualityPromotionEventSequence(ctx context.Context, projectID, sessionID string) (uint64, error) {
	var current uint64
	if err := s.conn.QueryRow(ctx, `
		SELECT ifNull(max(event_sequence), 0)
		FROM crawlobserver.crawl_quality_promotion_events
		WHERE project_id = ? AND session_id = ?`, projectID, sessionID).Scan(&current); err != nil {
		return 0, fmt.Errorf("reading quality promotion event sequence: %w", err)
	}
	if current == ^uint64(0) {
		return 0, errors.New("quality promotion event sequence exhausted")
	}
	return current + 1, nil
}

func (s *Store) LatestQualityPromotionEvent(ctx context.Context, projectID, sessionID string) (*CrawlQualityPromotionEvent, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT project_id, toString(session_id), toString(promotion_id), event_sequence, toString(evaluation_revision), pagerank_evidence_revision,
			baseline_session_id, baseline_evaluation_revision, evaluator_revision, rules_revision, status, reason, detail, occurred_at
		FROM crawlobserver.crawl_quality_promotion_events
		WHERE project_id = ? AND session_id = ?
		ORDER BY event_sequence DESC, occurred_at DESC, promotion_id DESC
		LIMIT 1`, projectID, sessionID)
	var event CrawlQualityPromotionEvent
	if err := row.Scan(&event.ProjectID, &event.SessionID, &event.PromotionID, &event.EventSequence, &event.EvaluationRevision, &event.PageRankEvidenceRevision,
		&event.BaselineSessionID, &event.BaselineEvaluationRevision, &event.EvaluatorRevision, &event.RulesRevision,
		&event.Status, &event.Reason, &event.Detail, &event.OccurredAt); err != nil {
		return nil, err
	}
	return &event, nil
}

// RecordQualityActionEvent appends a sanitized audit event and reads it back
// before returning. It is intentionally independent from promotion so a
// re-evaluation that stays untrusted is still durably observable.
func (s *Store) RecordQualityActionEvent(ctx context.Context, event CrawlQualityActionEvent) (*CrawlQualityActionEvent, error) {
	if !isValidUUID(event.SessionID) {
		return nil, fmt.Errorf("invalid quality action session ID: %s", event.SessionID)
	}
	if event.ActionID == "" {
		event.ActionID = uuid.NewString()
	}
	if !isValidUUID(event.ActionID) {
		return nil, fmt.Errorf("invalid quality action ID: %s", event.ActionID)
	}
	lock := qualityActionEventLock(event.SessionID, event.ActionID)
	lock.Lock()
	defer lock.Unlock()

	sequence, latestOccurredAt, err := s.nextQualityActionEventSequence(ctx, event.SessionID, event.ActionID)
	if err != nil {
		return nil, err
	}
	event.EventSequence = sequence
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	event.OccurredAt = event.OccurredAt.UTC().Truncate(time.Second)
	latestOccurredAt = latestOccurredAt.UTC().Truncate(time.Second)
	if !event.OccurredAt.After(latestOccurredAt) {
		event.OccurredAt = latestOccurredAt.Add(time.Second)
	}
	if err := s.conn.Exec(ctx, `
		INSERT INTO crawlobserver.crawl_quality_action_events (
			session_id, action_id, event_sequence, action, source, actor, reason,
			expected_evaluation_revision, previous_evaluation_revision, result_evaluation_revision,
			expected_pagerank_evidence_revision, pagerank_evidence_revision, status, occurred_at
		) VALUES (?, toUUID(?), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.SessionID, event.ActionID, event.EventSequence, sanitizeQualityAuditText(event.Action), sanitizeQualityAuditText(event.Source),
		sanitizeQualityAuditText(event.Actor), sanitizeQualityAuditText(event.Reason),
		event.ExpectedEvaluationRevision, event.PreviousEvaluationRevision, event.ResultEvaluationRevision,
		event.ExpectedPageRankEvidenceRevision, event.PageRankEvidenceRevision,
		sanitizeQualityAuditText(event.Status), event.OccurredAt,
	); err != nil {
		return nil, fmt.Errorf("recording quality action event: %w", err)
	}
	row := s.conn.QueryRow(ctx, `
		SELECT toString(session_id), toString(action_id), event_sequence, action, source, actor, reason,
			expected_evaluation_revision, previous_evaluation_revision, result_evaluation_revision,
			expected_pagerank_evidence_revision, pagerank_evidence_revision, status, occurred_at
		FROM crawlobserver.crawl_quality_action_events
		WHERE session_id = ? AND action_id = toUUID(?)
		ORDER BY event_sequence DESC, occurred_at DESC, if(status = 'requested', 0, 1) DESC, status DESC
		LIMIT 1`, event.SessionID, event.ActionID)
	if err := row.Scan(&event.SessionID, &event.ActionID, &event.EventSequence, &event.Action, &event.Source, &event.Actor,
		&event.Reason, &event.ExpectedEvaluationRevision, &event.PreviousEvaluationRevision,
		&event.ResultEvaluationRevision, &event.ExpectedPageRankEvidenceRevision,
		&event.PageRankEvidenceRevision, &event.Status, &event.OccurredAt); err != nil {
		return nil, fmt.Errorf("reading quality action event: %w", err)
	}
	return &event, nil
}

func (s *Store) nextQualityActionEventSequence(ctx context.Context, sessionID, actionID string) (uint64, time.Time, error) {
	var current uint64
	var latestOccurredAt time.Time
	if err := s.conn.QueryRow(ctx, `
		SELECT ifNull(max(event_sequence), 0), max(occurred_at)
		FROM crawlobserver.crawl_quality_action_events
		WHERE session_id = ? AND action_id = toUUID(?)`, sessionID, actionID).Scan(&current, &latestOccurredAt); err != nil {
		return 0, time.Time{}, fmt.Errorf("reading quality action event sequence: %w", err)
	}
	if current == ^uint64(0) {
		return 0, time.Time{}, errors.New("quality action event sequence exhausted")
	}
	return current + 1, latestOccurredAt, nil
}

func (s *Store) ListQualityActionEvents(ctx context.Context, sessionID string, limit int) ([]CrawlQualityActionEvent, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := s.conn.Query(ctx, `
		SELECT toString(session_id), toString(action_id), event_sequence, action, source, actor, reason,
			expected_evaluation_revision, previous_evaluation_revision, result_evaluation_revision,
			expected_pagerank_evidence_revision, pagerank_evidence_revision, status, occurred_at
		FROM crawlobserver.crawl_quality_action_events
		WHERE session_id = ?
		ORDER BY occurred_at DESC, event_sequence DESC, action_id DESC LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []CrawlQualityActionEvent{}
	for rows.Next() {
		var event CrawlQualityActionEvent
		if err := rows.Scan(&event.SessionID, &event.ActionID, &event.EventSequence, &event.Action, &event.Source, &event.Actor,
			&event.Reason, &event.ExpectedEvaluationRevision, &event.PreviousEvaluationRevision,
			&event.ResultEvaluationRevision, &event.ExpectedPageRankEvidenceRevision,
			&event.PageRankEvidenceRevision, &event.Status, &event.OccurredAt); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func sanitizeQualityAuditText(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 1024 {
		return value[:1024]
	}
	return value
}

func (s *Store) CrawlQualityMetrics(ctx context.Context, sessionID string, topN int) (*CrawlQualityMetrics, error) {
	if topN <= 0 {
		topN = 20
	}
	htmlPredicate := "(" + PageTypeSQLExpression + " = 'html' OR p.content_type ILIKE '%html%')"
	row := s.conn.QueryRow(ctx, `
		SELECT countIf(`+htmlPredicate+`), countIf(p.status_code = 404), countIf(p.status_code >= 500),
			countIf(p.status_code >= 300 AND p.status_code < 400),
			countIf(`+htmlPredicate+` AND p.is_indexable = false),
			countIf(`+htmlPredicate+` AND p.canonical != '' AND p.canonical_is_self = false),
			sum(p.internal_links_out)
		FROM crawlobserver.pages AS p FINAL WHERE p.crawl_session_id = ?`, sessionID)
	var metrics CrawlQualityMetrics
	if err := row.Scan(&metrics.HTMLPages, &metrics.Status404, &metrics.Status5xx, &metrics.Redirects, &metrics.Noindex, &metrics.CanonicalMismatch, &metrics.InternalLinks); err != nil {
		return nil, err
	}
	row = s.conn.QueryRow(ctx, `
		SELECT countIf(pagerank = 0)
		FROM (
			SELECT p.pagerank
			FROM crawlobserver.pages AS p FINAL
			WHERE p.crawl_session_id = ? AND `+PageRankEligiblePredicate("p")+`
			ORDER BY p.pagerank DESC, p.url ASC
			LIMIT ?
		)`, sessionID, topN)
	if err := row.Scan(&metrics.PageRankZeroTopPages); err != nil {
		return nil, err
	}
	return &metrics, nil
}

func (s *Store) TopPageRankURLs(ctx context.Context, sessionID string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.conn.Query(ctx, `
		SELECT p.url FROM crawlobserver.pages AS p FINAL
		WHERE p.crawl_session_id = ? AND `+PageRankEligiblePredicate("p")+`
		ORDER BY p.pagerank DESC, p.url ASC LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	urls := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		urls = append(urls, value)
	}
	return urls, rows.Err()
}

func (s *Store) CanaryPageCheck(ctx context.Context, sessionID, canaryURL string) (*CanaryPageCheck, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT url, final_url, status_code, title, canonical, is_indexable, internal_links_out, pagerank
		FROM crawlobserver.pages FINAL
		WHERE crawl_session_id = ? AND (url = ? OR final_url = ?)
		ORDER BY crawled_at DESC LIMIT 1`, sessionID, canaryURL, canaryURL)
	var check CanaryPageCheck
	if err := row.Scan(&check.URL, &check.FinalURL, &check.StatusCode, &check.Title, &check.Canonical,
		&check.IsIndexable, &check.InternalLinksOut, &check.PageRank); err != nil {
		return &CanaryPageCheck{Found: false}, nil
	}
	check.Found = true
	return &check, nil
}

func (s *Store) CountMatchedPagesForURLs(ctx context.Context, sessionID string, urls []string) (int, error) {
	if len(urls) == 0 {
		return 0, nil
	}
	row := s.conn.QueryRow(ctx, `
		SELECT count() FROM (SELECT arrayJoin(?) AS candidate_url) AS candidates
		WHERE candidate_url IN (
			SELECT url FROM crawlobserver.pages FINAL WHERE crawl_session_id = ?
			UNION DISTINCT
			SELECT final_url FROM crawlobserver.pages FINAL WHERE crawl_session_id = ? AND final_url != ''
		)`, urls, sessionID, sessionID)
	var count uint64
	if err := row.Scan(&count); err != nil {
		return 0, fmt.Errorf("counting matched planned candidate urls: %w", err)
	}
	return int(count), nil
}

type qualityScanner interface {
	Scan(dest ...interface{}) error
}

func scanQualityEvaluation(row qualityScanner) (*CrawlQualityResult, error) {
	var result CrawlQualityResult
	var metricsJSON string
	if err := row.Scan(&result.SessionID, &result.EvaluationRevision, &result.ProjectID, &result.BaselineSessionID,
		&result.BaselineEvaluationRevision, &result.Source, &result.EvaluatorRevision, &result.RulesRevision,
		&result.PageRankEvidenceRevision, &result.PageRankEvidenceSource, &result.PageRankEvidenceStatus,
		&result.PageRankPredicateVersion, &result.PageRankEligible, &result.PageRankPositive, &result.PageRankZero,
		&result.Stale, &result.StaleReasons, &result.FindingCount, &result.PromotionStatus, &result.Status, &result.Score,
		&result.Trusted, &result.IsFullCrawl, &result.Summary, &metricsJSON, &result.EvaluatedAt); err != nil {
		return nil, err
	}
	decodeQualityMetrics(&result, metricsJSON)
	return &result, nil
}

func scanLegacyQualityResult(row qualityScanner) (*CrawlQualityResult, error) {
	var result CrawlQualityResult
	var metricsJSON string
	if err := row.Scan(&result.SessionID, &result.ProjectID, &result.BaselineSessionID, &result.Status, &result.Score,
		&result.Trusted, &result.IsFullCrawl, &result.Summary, &metricsJSON, &result.EvaluatedAt); err != nil {
		return nil, err
	}
	decodeQualityMetrics(&result, metricsJSON)
	return &result, nil
}

func decodeQualityMetrics(result *CrawlQualityResult, raw string) {
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &result.Metrics)
	}
	if result.Metrics == nil {
		result.Metrics = map[string]interface{}{}
	}
}
