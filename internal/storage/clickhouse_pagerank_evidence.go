package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrNoFinalizedPageRankEvidence means no verified PageRank result exists.
	ErrNoFinalizedPageRankEvidence = errors.New("no finalized pagerank evidence")
	// ErrPageRankMutationActive prevents adoption from observing a moving target.
	ErrPageRankMutationActive = errors.New("pagerank mutation is active")
	// ErrPageRankReportUnavailable means current FINAL pages are not bound to
	// the newest finalized PageRank evidence revision.
	ErrPageRankReportUnavailable = errors.New("pagerank report unavailable")

	pageRankAttemptLocks sync.Map // map[string]*sync.Mutex, keyed by session ID
)

const pageRankEvidenceQueryIdentity = "pagerank-final-readback-v1"

var sensitiveFailurePatterns = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`(?i)\bbearer\s+[^\s,;&]+`), "Bearer [REDACTED]"},
	{regexp.MustCompile(`(?i)\b(x-api-key|api-key)\s+[^\s,;&]+`), "${1} [REDACTED]"},
	{regexp.MustCompile(`(?i)\b(api[_-]?key|access[_-]?token|refresh[_-]?token|token|password|passwd|client[_-]?secret|secret)\b["']?\s*[:=]\s*["']?[^\s,"';&}\]]+["']?`), "${1}=[REDACTED]"},
}

func pageRankAttemptLock(sessionID string) *sync.Mutex {
	lock, _ := pageRankAttemptLocks.LoadOrStore(sessionID, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

// PageRankOptionsSignature is stable across selector ordering and duplication.
func PageRankOptionsSignature(opts PageRankOptions) string {
	selectors := append([]string(nil), opts.FooterSelectors...)
	for i := range selectors {
		selectors[i] = strings.TrimSpace(selectors[i])
	}
	sort.Strings(selectors)
	selectors = uniqueNonEmptyStrings(selectors)
	payload := fmt.Sprintf("footer=%t;refresh_locations=%t;selectors=%s", opts.IncludeFooterLinks, opts.RefreshLinkLocation, strings.Join(selectors, ","))
	sum := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("sha256:%x", sum[:])
}

func uniqueNonEmptyStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := values[:0]
	for _, value := range values {
		if value == "" || (len(out) > 0 && value == out[len(out)-1]) {
			continue
		}
		out = append(out, value)
	}
	return out
}

func (s *Store) appendPageRankEvidence(ctx context.Context, evidence *PageRankEvidence) error {
	if evidence == nil {
		return errors.New("pagerank evidence is required")
	}
	sequence, latestOccurredAt, err := s.nextPageRankEvidenceSequence(ctx, evidence.SessionID)
	if err != nil {
		return err
	}
	evidence.EventSequence = sequence
	if evidence.OccurredAt.IsZero() {
		evidence.OccurredAt = time.Now().UTC()
	}
	evidence.OccurredAt = evidence.OccurredAt.UTC().Truncate(time.Second)
	latestOccurredAt = latestOccurredAt.UTC().Truncate(time.Second)
	if !evidence.OccurredAt.After(latestOccurredAt) {
		evidence.OccurredAt = latestOccurredAt.Add(time.Second)
	}
	return s.conn.Exec(ctx, `
		INSERT INTO crawlobserver.pagerank_evidence (
			session_id, attempt_id, event_sequence, predecessor_attempt_id, state, source,
			algorithm_version, predicate_version, options_signature,
			graph_fingerprint, rank_fingerprint, graph_page_count,
			eligible_page_count, positive_page_count, zero_page_count,
			query_identity, occurred_at, failure
		) VALUES (?, toUUID(?), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		evidence.SessionID, evidence.AttemptID, evidence.EventSequence, evidence.PredecessorAttemptID, evidence.State, evidence.Source,
		evidence.AlgorithmVersion, evidence.PredicateVersion, evidence.OptionsSignature,
		evidence.GraphFingerprint, evidence.RankFingerprint, evidence.GraphPageCount,
		evidence.EligiblePageCount, evidence.PositivePageCount, evidence.ZeroPageCount,
		evidence.QueryIdentity, evidence.OccurredAt, sanitizePageRankFailure(evidence.Failure),
	)
}

func (s *Store) nextPageRankEvidenceSequence(ctx context.Context, sessionID string) (uint64, time.Time, error) {
	var current uint64
	var latestOccurredAt time.Time
	if err := s.conn.QueryRow(ctx, `
		SELECT ifNull(max(event_sequence), 0), max(occurred_at)
		FROM crawlobserver.pagerank_evidence
		WHERE session_id = ?`, sessionID).Scan(&current, &latestOccurredAt); err != nil {
		return 0, time.Time{}, fmt.Errorf("reading pagerank evidence sequence: %w", err)
	}
	if current == ^uint64(0) {
		return 0, time.Time{}, errors.New("pagerank evidence sequence exhausted")
	}
	return current + 1, latestOccurredAt, nil
}

func sanitizePageRankFailure(message string) string {
	return SanitizePageRankEvidenceFailure(message)
}

// SanitizePageRankEvidenceFailure redacts credential-like values before an
// evidence failure is persisted or returned through the API.
func SanitizePageRankEvidenceFailure(message string) string {
	if len(message) > 8192 {
		message = message[:8192]
	}
	for _, sensitive := range sensitiveFailurePatterns {
		message = sensitive.pattern.ReplaceAllString(message, sensitive.replacement)
	}
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > 1024 {
		return message[:1024]
	}
	return message
}

// LatestPageRankEvidence returns the latest lifecycle event across all attempts.
// EventSequence is the causal order. Timestamp and state are only deterministic
// fallback ordering for pre-sequence rows migrated with the default value 0.
func (s *Store) LatestPageRankEvidence(ctx context.Context, sessionID string) (*PageRankEvidence, error) {
	evidence, err := s.readPageRankEvidence(ctx, `
		WHERE session_id = ?
		ORDER BY event_sequence DESC, occurred_at DESC,
			multiIf(state = 'failed', 3, state = 'started', 2, state = 'finalized', 1, 0) DESC,
			attempt_id DESC
		LIMIT 1`, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoFinalizedPageRankEvidence
	}
	return evidence, err
}

// LatestFinalizedPageRankEvidence returns the latest evidence only when the
// newest lifecycle event is finalized. A newer started or failed attempt makes
// the session non-finalized until a later finalization event is written.
func (s *Store) LatestFinalizedPageRankEvidence(ctx context.Context, sessionID string) (*PageRankEvidence, error) {
	evidence, err := s.LatestPageRankEvidence(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if evidence.State != PageRankEvidenceFinalized {
		return nil, ErrNoFinalizedPageRankEvidence
	}
	return evidence, nil
}

func (s *Store) verifiedPageRankReportEvidence(ctx context.Context, sessionID string) (*PageRankEvidence, pageRankSnapshot, error) {
	var empty pageRankSnapshot
	evidence, err := s.LatestPageRankEvidence(ctx, sessionID)
	if err != nil {
		return nil, empty, fmt.Errorf("%w: %v", ErrPageRankReportUnavailable, err)
	}
	if evidence.State != PageRankEvidenceFinalized {
		return nil, empty, fmt.Errorf("%w: newest evidence is %s", ErrPageRankReportUnavailable, evidence.State)
	}
	if evidence.PredicateVersion != PageRankEligiblePredicateVersion {
		return nil, empty, fmt.Errorf("%w: predicate version %q is not %q", ErrPageRankReportUnavailable, evidence.PredicateVersion, PageRankEligiblePredicateVersion)
	}
	snapshot, err := s.readPageRankSnapshot(ctx, sessionID)
	if err != nil {
		return nil, empty, err
	}
	if snapshot.Graph != evidence.GraphFingerprint || snapshot.Rank != evidence.RankFingerprint {
		return nil, empty, fmt.Errorf("%w: FINAL page fingerprint does not match evidence", ErrPageRankReportUnavailable)
	}
	if snapshot.GraphPageCount != evidence.GraphPageCount ||
		snapshot.Population.Eligible != evidence.EligiblePageCount ||
		snapshot.Population.Positive != evidence.PositivePageCount ||
		snapshot.Population.Zero != evidence.ZeroPageCount ||
		snapshot.Population.Eligible != snapshot.Population.Positive+snapshot.Population.Zero {
		return nil, empty, fmt.Errorf("%w: FINAL page population does not match evidence", ErrPageRankReportUnavailable)
	}
	switch evidence.Source {
	case PageRankEvidenceComputed:
		population, revised, err := s.PageRankPopulationForRevision(ctx, sessionID, evidence.AttemptID)
		if err != nil {
			return nil, empty, err
		}
		if population != snapshot.Population || revised != population.Eligible {
			return nil, empty, fmt.Errorf("%w: computed revision is only stamped on %d of %d eligible pages", ErrPageRankReportUnavailable, revised, population.Eligible)
		}
	case PageRankEvidenceObservedExisting:
		// Adopted legacy rows intentionally retain their original revision stamp;
		// the exact FINAL graph/rank fingerprint above is their binding.
	default:
		return nil, empty, fmt.Errorf("%w: unsupported evidence source %q", ErrPageRankReportUnavailable, evidence.Source)
	}
	return evidence, snapshot, nil
}

// GetPageRankEvidence returns the most recent lifecycle event for an attempt.
func (s *Store) GetPageRankEvidence(ctx context.Context, sessionID, attemptID string) (*PageRankEvidence, error) {
	evidence, err := s.readPageRankEvidence(ctx, `
		WHERE session_id = ? AND attempt_id = toUUID(?)
		ORDER BY event_sequence DESC, occurred_at DESC,
			multiIf(state = 'failed', 3, state = 'started', 2, state = 'finalized', 1, 0) DESC
		LIMIT 1`, sessionID, attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoFinalizedPageRankEvidence
	}
	return evidence, err
}

func (s *Store) readPageRankEvidence(ctx context.Context, suffix string, args ...interface{}) (*PageRankEvidence, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT toString(session_id), toString(attempt_id), event_sequence, predecessor_attempt_id,
			state, source, algorithm_version, predicate_version, options_signature,
			graph_fingerprint, rank_fingerprint, graph_page_count, eligible_page_count,
			positive_page_count, zero_page_count, query_identity, occurred_at, failure
		FROM crawlobserver.pagerank_evidence FINAL `+suffix, args...)
	var evidence PageRankEvidence
	err := row.Scan(
		&evidence.SessionID, &evidence.AttemptID, &evidence.EventSequence, &evidence.PredecessorAttemptID,
		&evidence.State, &evidence.Source, &evidence.AlgorithmVersion, &evidence.PredicateVersion,
		&evidence.OptionsSignature, &evidence.GraphFingerprint, &evidence.RankFingerprint,
		&evidence.GraphPageCount, &evidence.EligiblePageCount, &evidence.PositivePageCount,
		&evidence.ZeroPageCount, &evidence.QueryIdentity, &evidence.OccurredAt, &evidence.Failure,
	)
	if err != nil {
		return nil, err
	}
	evidence.Failure = sanitizePageRankFailure(evidence.Failure)
	return &evidence, nil
}

type pageRankSnapshot struct {
	GraphPageCount uint64
	Population     PageRankPopulation
	Graph          string
	Rank           string
}

func (s *Store) readPageRankSnapshot(ctx context.Context, sessionID string) (pageRankSnapshot, error) {
	var snapshot pageRankSnapshot
	graphHash := sha256.New()
	rows, err := s.conn.Query(ctx, `
		SELECT p.url, p.final_url, p.status_code, p.canonical, p.canonical_is_self, p.content_type
		FROM crawlobserver.pages AS p FINAL
		WHERE p.crawl_session_id = ? AND `+PageRankEligiblePredicate("p")+`
		ORDER BY url`, sessionID)
	if err != nil {
		return snapshot, fmt.Errorf("reading pagerank graph pages: %w", err)
	}
	for rows.Next() {
		var url, finalURL, canonical, contentType string
		var statusCode uint16
		var canonicalSelf bool
		if err := rows.Scan(&url, &finalURL, &statusCode, &canonical, &canonicalSelf, &contentType); err != nil {
			rows.Close()
			return snapshot, fmt.Errorf("scanning pagerank graph page: %w", err)
		}
		snapshot.GraphPageCount++
		fmt.Fprintf(graphHash, "p\x00%s\x00%s\x00%d\x00%s\x00%t\x00%s\n", url, finalURL, statusCode, canonical, canonicalSelf, contentType)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return snapshot, fmt.Errorf("iterating pagerank graph pages: %w", err)
	}
	rows.Close()

	rows, err = s.conn.Query(ctx, `
		SELECT source_url, target_url, anchor_text, rel, is_internal, tag, link_location
		FROM crawlobserver.links
		WHERE crawl_session_id = ?
			AND source_url IN (
				SELECT p.url FROM crawlobserver.pages AS p FINAL
				WHERE p.crawl_session_id = ? AND `+PageRankEligiblePredicate("p")+`
			)
		ORDER BY source_url, target_url, anchor_text, rel, is_internal, tag, link_location`, sessionID, sessionID)
	if err != nil {
		return snapshot, fmt.Errorf("reading pagerank graph links: %w", err)
	}
	for rows.Next() {
		var source, target, anchor, rel, tag, location string
		var internal bool
		if err := rows.Scan(&source, &target, &anchor, &rel, &internal, &tag, &location); err != nil {
			rows.Close()
			return snapshot, fmt.Errorf("scanning pagerank graph link: %w", err)
		}
		fmt.Fprintf(graphHash, "l\x00%s\x00%s\x00%s\x00%s\x00%t\x00%s\x00%s\n", source, target, anchor, rel, internal, tag, location)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return snapshot, fmt.Errorf("iterating pagerank graph links: %w", err)
	}
	rows.Close()
	snapshot.Graph = fmt.Sprintf("sha256:%x", graphHash.Sum(nil))

	rankHash := sha256.New()
	rows, err = s.conn.Query(ctx, `
		SELECT p.url, p.pagerank, toString(p.pagerank_revision)
		FROM crawlobserver.pages AS p FINAL
		WHERE p.crawl_session_id = ? AND `+PageRankEligiblePredicate("p")+`
		ORDER BY p.url`, sessionID)
	if err != nil {
		return snapshot, fmt.Errorf("reading pagerank population: %w", err)
	}
	for rows.Next() {
		var url, revision string
		var rank float64
		if err := rows.Scan(&url, &rank, &revision); err != nil {
			rows.Close()
			return snapshot, fmt.Errorf("scanning pagerank population: %w", err)
		}
		snapshot.Population.Eligible++
		if rank > 0 {
			snapshot.Population.Positive++
		} else {
			snapshot.Population.Zero++
		}
		fmt.Fprintf(rankHash, "%s\x00%.17g\x00%s\n", url, rank, revision)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return snapshot, fmt.Errorf("iterating pagerank population: %w", err)
	}
	rows.Close()
	snapshot.Rank = fmt.Sprintf("sha256:%x", rankHash.Sum(nil))
	return snapshot, nil
}

// PageRankPopulationForRevision returns FINAL population counts, optionally
// requiring every eligible page to carry revisionID.
func (s *Store) PageRankPopulationForRevision(ctx context.Context, sessionID, revisionID string) (PageRankPopulation, uint64, error) {
	var population PageRankPopulation
	query := `
		SELECT count(), countIf(p.pagerank > 0), countIf(p.pagerank = 0),
			countIf(p.pagerank_revision = toUUID(?))
		FROM crawlobserver.pages AS p FINAL
		WHERE p.crawl_session_id = ? AND ` + PageRankEligiblePredicate("p")
	var revised uint64
	if err := s.conn.QueryRow(ctx, query, revisionID, sessionID).Scan(&population.Eligible, &population.Positive, &population.Zero, &revised); err != nil {
		return population, 0, fmt.Errorf("reading pagerank revision population: %w", err)
	}
	return population, revised, nil
}

func (s *Store) pageRankMutationActive(ctx context.Context) (bool, error) {
	var count uint64
	err := s.conn.QueryRow(ctx, `
		SELECT count()
		FROM system.mutations
		WHERE database = 'crawlobserver' AND table = 'pages' AND is_done = 0`).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("checking active pages mutations: %w", err)
	}
	return count > 0, nil
}

func (s *Store) finalizeComputedPageRankEvidence(ctx context.Context, started PageRankEvidence, expectedGraphFingerprint string) (*PageRankEvidence, error) {
	snapshot, err := s.readPageRankSnapshot(ctx, started.SessionID)
	if err != nil {
		return nil, err
	}
	if snapshot.Graph != expectedGraphFingerprint {
		return nil, fmt.Errorf("pagerank graph changed during computation")
	}
	population, revised, err := s.PageRankPopulationForRevision(ctx, started.SessionID, started.AttemptID)
	if err != nil {
		return nil, err
	}
	if population.Eligible != revised {
		return nil, fmt.Errorf("pagerank revision verification failed: %d eligible rows, %d stamped", population.Eligible, revised)
	}
	if population.Eligible != population.Positive+population.Zero {
		return nil, fmt.Errorf("pagerank population does not reconcile")
	}
	if population.Zero != 0 {
		return nil, fmt.Errorf("pagerank verification failed: %d eligible rows have zero rank", population.Zero)
	}
	finalized := started
	finalized.State = PageRankEvidenceFinalized
	finalized.GraphFingerprint = snapshot.Graph
	finalized.RankFingerprint = snapshot.Rank
	finalized.GraphPageCount = snapshot.GraphPageCount
	finalized.EligiblePageCount = population.Eligible
	finalized.PositivePageCount = population.Positive
	finalized.ZeroPageCount = population.Zero
	finalized.OccurredAt = time.Now().UTC()
	if err := s.appendPageRankEvidence(ctx, &finalized); err != nil {
		return nil, err
	}
	return &finalized, nil
}

// AdoptObservedPageRankEvidence attaches deterministic evidence to an existing
// finalized PageRank state without updating pages or crawl sessions.
func (s *Store) AdoptObservedPageRankEvidence(ctx context.Context, sessionID string, opts PageRankOptions) (*PageRankEvidence, error) {
	if !isValidUUID(sessionID) {
		return nil, fmt.Errorf("invalid session ID: %s", sessionID)
	}
	lock := pageRankAttemptLock(sessionID)
	lock.Lock()
	defer lock.Unlock()

	if active, err := s.pageRankMutationActive(ctx); err != nil {
		return nil, err
	} else if active {
		return nil, ErrPageRankMutationActive
	}
	before, err := s.readPageRankSnapshot(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	optionsSignature := PageRankOptionsSignature(opts)
	attemptID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(strings.Join([]string{
		sessionID, PageRankAlgorithmVersion, PageRankEligiblePredicateVersion,
		optionsSignature, before.Graph, before.Rank,
	}, "\x00"))).String()
	if evidence, err := s.GetPageRankEvidence(ctx, sessionID, attemptID); err == nil && evidence.State == PageRankEvidenceFinalized {
		return evidence, nil
	} else if err != nil && !errors.Is(err, ErrNoFinalizedPageRankEvidence) {
		return nil, err
	}

	started := PageRankEvidence{
		SessionID: sessionID, AttemptID: attemptID, State: PageRankEvidenceStarted,
		Source: PageRankEvidenceObservedExisting, AlgorithmVersion: PageRankAlgorithmVersion,
		PredicateVersion: PageRankEligiblePredicateVersion, OptionsSignature: optionsSignature,
		GraphFingerprint: before.Graph, RankFingerprint: before.Rank, GraphPageCount: before.GraphPageCount,
		EligiblePageCount: before.Population.Eligible, PositivePageCount: before.Population.Positive,
		ZeroPageCount: before.Population.Zero, QueryIdentity: pageRankEvidenceQueryIdentity,
		OccurredAt: time.Now().UTC(),
	}
	if err := s.appendPageRankEvidence(ctx, &started); err != nil {
		return nil, fmt.Errorf("recording observed pagerank start: %w", err)
	}
	failed := func(cause error) (*PageRankEvidence, error) {
		failure := started
		failure.State = PageRankEvidenceFailed
		failure.OccurredAt = time.Now().UTC()
		failure.Failure = cause.Error()
		if err := s.appendPageRankEvidence(ctx, &failure); err != nil {
			return nil, fmt.Errorf("%v; recording observed pagerank failure: %w", cause, err)
		}
		return nil, cause
	}

	if active, err := s.pageRankMutationActive(ctx); err != nil {
		return failed(err)
	} else if active {
		return failed(ErrPageRankMutationActive)
	}
	after, err := s.readPageRankSnapshot(ctx, sessionID)
	if err != nil {
		return failed(err)
	}
	if before.Graph != after.Graph || before.Rank != after.Rank {
		return failed(fmt.Errorf("pagerank evidence changed during observation"))
	}
	if after.Population.Eligible != after.Population.Positive+after.Population.Zero {
		return failed(fmt.Errorf("pagerank population does not reconcile"))
	}
	if after.Population.Zero != 0 {
		return failed(fmt.Errorf("pagerank verification failed: %d eligible rows have zero rank", after.Population.Zero))
	}
	finalized := started
	finalized.State = PageRankEvidenceFinalized
	finalized.OccurredAt = time.Now().UTC()
	if err := s.appendPageRankEvidence(ctx, &finalized); err != nil {
		return failed(fmt.Errorf("recording observed pagerank finalization: %w", err))
	}
	return &finalized, nil
}
