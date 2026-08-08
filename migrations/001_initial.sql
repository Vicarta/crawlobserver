-- CrawlObserver initial schema
-- Run with: crawlobserver migrate

CREATE DATABASE IF NOT EXISTS crawlobserver;

CREATE TABLE IF NOT EXISTS crawlobserver.crawl_sessions (
    id UUID,
    started_at DateTime64(3),
    finished_at DateTime64(3),
    status String,
    seed_urls Array(String),
    config String,
    pages_crawled UInt64,
    user_agent String
) ENGINE = ReplacingMergeTree()
ORDER BY (id);

CREATE TABLE IF NOT EXISTS crawlobserver.pages (
    crawl_session_id UUID,
    url String,
    final_url String,
    status_code UInt16,
    content_type String,
    title String,
    canonical String,
    meta_robots String,
    meta_description String,
    h1 Array(String),
    h2 Array(String),
    h3 Array(String),
    h4 Array(String),
    h5 Array(String),
    h6 Array(String),
    page_created_at Nullable(DateTime64(3, 'UTC')),
    page_modified_at Nullable(DateTime64(3, 'UTC')),
    headers Map(String, String),
    redirect_chain Array(Tuple(url String, status_code UInt16)),
    body_size UInt64,
    fetch_duration_ms UInt64,
    error String,
    depth UInt16,
    found_on String,
    pagerank Float64 DEFAULT 0,
    pagerank_revision UUID DEFAULT toUUID('00000000-0000-0000-0000-000000000000'),
    body_html String CODEC(ZSTD(3)),
    crawled_at DateTime64(3)
) ENGINE = ReplacingMergeTree(crawled_at)
PARTITION BY toYYYYMM(crawled_at)
ORDER BY (crawl_session_id, url);

CREATE TABLE IF NOT EXISTS crawlobserver.links (
    crawl_session_id UUID,
    source_url String,
    target_url String,
    anchor_text String,
    rel String,
    is_internal Bool,
    tag String,
    link_location LowCardinality(String) DEFAULT 'body',
    crawled_at DateTime64(3)
) ENGINE = MergeTree()
ORDER BY (crawl_session_id, source_url, target_url);

CREATE TABLE IF NOT EXISTS crawlobserver.pagerank_evidence (
    session_id UUID,
    attempt_id UUID,
    event_sequence UInt64 DEFAULT 0,
    predecessor_attempt_id String,
    state LowCardinality(String),
    source LowCardinality(String),
    algorithm_version LowCardinality(String),
    predicate_version LowCardinality(String),
    options_signature String,
    graph_fingerprint String,
    rank_fingerprint String,
    graph_page_count UInt64,
    eligible_page_count UInt64,
    positive_page_count UInt64,
    zero_page_count UInt64,
    query_identity String,
    occurred_at DateTime64(3, 'UTC'),
    failure String
) ENGINE = ReplacingMergeTree(occurred_at)
PARTITION BY session_id
ORDER BY (session_id, attempt_id, state, event_sequence);

CREATE TABLE IF NOT EXISTS crawlobserver.project_current_snapshots (
    project_id String,
    snapshot_revision UInt64 DEFAULT 0,
    current_session_id UUID,
    baseline_session_id String,
    quality_baseline_session_id String DEFAULT '',
    baseline_created_at DateTime64(3),
    last_delta_session_id String,
    delta_count UInt32,
    quality_evaluation_revision String DEFAULT '',
    baseline_quality_evaluation_revision String DEFAULT '',
    pagerank_evidence_revision String DEFAULT '',
    quality_evaluator_revision String DEFAULT '',
    quality_rules_revision String DEFAULT '',
    quality_promotion_status LowCardinality(String) DEFAULT '',
    updated_at DateTime64(3)
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY (project_id);

CREATE TABLE IF NOT EXISTS crawlobserver.project_current_snapshot_deltas (
    project_id String,
    delta_session_id UUID,
    current_session_id UUID,
    applied_at DateTime64(3)
) ENGINE = ReplacingMergeTree(applied_at)
ORDER BY (project_id, delta_session_id);

-- Immutable quality evaluations and their separately published current pointer.
-- crawl_quality_results/findings remain legacy compatibility tables and are
-- imported lazily by the application; this schema is append-only.
CREATE TABLE IF NOT EXISTS crawlobserver.crawl_quality_evaluations (
    session_id UUID,
    evaluation_revision UUID,
    project_id String,
    baseline_session_id String,
    baseline_evaluation_revision String,
    source LowCardinality(String),
    evaluator_revision String,
    rules_revision String,
    pagerank_evidence_revision String,
    pagerank_evidence_source LowCardinality(String),
    pagerank_evidence_status LowCardinality(String),
    pagerank_predicate_version String,
    pagerank_eligible UInt64,
    pagerank_positive UInt64,
    pagerank_zero UInt64,
    stale Bool,
    stale_reasons Array(String),
    finding_count UInt32,
    promotion_status LowCardinality(String),
    status LowCardinality(String),
    score UInt8,
    trusted Bool,
    is_full_crawl Bool,
    summary String,
    metrics String CODEC(ZSTD(3)),
    evaluated_at DateTime64(3, 'UTC')
) ENGINE = MergeTree()
PARTITION BY session_id
ORDER BY (session_id, evaluation_revision);

CREATE TABLE IF NOT EXISTS crawlobserver.crawl_quality_evaluation_findings (
    session_id UUID,
    evaluation_revision UUID,
    finding_index UInt32,
    project_id String,
    severity LowCardinality(String),
    finding_type LowCardinality(String),
    message String,
    metric LowCardinality(String),
    current_value Float64,
    baseline_value Float64,
    threshold_value Float64,
    blocking Bool,
    created_at DateTime64(3, 'UTC')
) ENGINE = MergeTree()
PARTITION BY session_id
ORDER BY (session_id, evaluation_revision, finding_index);

CREATE TABLE IF NOT EXISTS crawlobserver.crawl_quality_current_pointers (
    session_id UUID,
    evaluation_revision UUID,
    pointer_sequence UInt64 DEFAULT 0,
    published_at DateTime64(3, 'UTC')
) ENGINE = ReplacingMergeTree(published_at)
ORDER BY (session_id);

CREATE TABLE IF NOT EXISTS crawlobserver.crawl_quality_promotion_events (
    project_id String,
    session_id UUID,
    promotion_id UUID,
    event_sequence UInt64 DEFAULT 0,
    evaluation_revision UUID,
    pagerank_evidence_revision String,
    baseline_session_id String,
    baseline_evaluation_revision String,
    evaluator_revision String,
    rules_revision String,
    status LowCardinality(String),
    reason String,
    detail String,
    occurred_at DateTime64(3, 'UTC')
) ENGINE = MergeTree()
PARTITION BY session_id
ORDER BY (session_id, evaluation_revision, occurred_at, promotion_id);

CREATE TABLE IF NOT EXISTS crawlobserver.crawl_quality_action_events (
    session_id UUID,
    action_id UUID,
    event_sequence UInt64 DEFAULT 0,
    action LowCardinality(String),
    source LowCardinality(String),
    actor String,
    reason String,
    expected_evaluation_revision String,
    previous_evaluation_revision String,
    result_evaluation_revision String,
    expected_pagerank_evidence_revision String,
    pagerank_evidence_revision String,
    status LowCardinality(String),
    occurred_at DateTime64(3, 'UTC')
) ENGINE = MergeTree()
PARTITION BY session_id
ORDER BY (session_id, occurred_at, action_id);
