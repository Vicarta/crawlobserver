<script>
  import { getCoreWebVitals } from '../../api.js';
  import { t } from '../../i18n/index.svelte.js';
  import { fmtN } from '../../utils.js';
  import Pagination from '../Pagination.svelte';

  let { sessionId, onnavigate, onerror } = $props();

  const PAGE_SIZE = 50;
  const RATING_FILTERS = ['', 'good', 'needs_improvement', 'poor'];

  let report = $state(null);
  let loading = $state(false);
  let error = $state('');
  let offset = $state(0);
  let rating = $state('');
  let sort = $state('overall');
  let order = $state('desc');
  let requestID = 0;

  const summary = $derived(report?.summary || null);
  const pages = $derived(report?.pages || []);
  const total = $derived(report?.total || 0);

  function ratingLabel(value) {
    if (value === 'good') return t('report.coreWebVitals.good');
    if (value === 'needs_improvement') return t('report.coreWebVitals.needsImprovement');
    if (value === 'poor') return t('report.coreWebVitals.poor');
    return value || t('report.coreWebVitals.all');
  }

  function ratingClass(value) {
    if (value === 'good') return 'badge-success';
    if (value === 'needs_improvement') return 'badge-warning';
    return 'badge-error';
  }

  function formatMilliseconds(value) {
    const numeric = Number(value);
    if (!Number.isFinite(numeric)) return t('common.none');
    return `${Math.round(numeric).toLocaleString()}ms`;
  }

  function formatCLS(value) {
    const numeric = Number(value);
    if (!Number.isFinite(numeric)) return t('common.none');
    return numeric.toFixed(3);
  }

  function sortLabel(column) {
    const labels = {
      url: t('report.coreWebVitals.url'),
      lcp: t('report.coreWebVitals.lcp'),
      cls: t('report.coreWebVitals.cls'),
      ttfb: t('report.coreWebVitals.ttfb'),
      overall: t('report.coreWebVitals.overall'),
    };
    return labels[column];
  }

  function sortDirection(column) {
    if (sort !== column) return 'none';
    return order === 'asc' ? 'ascending' : 'descending';
  }

  function sortIndicator(column) {
    if (sort !== column) return '';
    return order === 'asc' ? ' ▲' : ' ▼';
  }

  function changeSort(column) {
    if (sort === column) {
      order = order === 'desc' ? 'asc' : 'desc';
    } else {
      sort = column;
      order = column === 'url' ? 'asc' : 'desc';
    }
    offset = 0;
  }

  function setRating(nextRating) {
    if (rating === nextRating) return;
    rating = nextRating;
    offset = 0;
  }

  function urlDetailHref(url) {
    return `/sessions/${sessionId}/url/${encodeURIComponent(url)}`;
  }

  function goToURL(event, url) {
    event.preventDefault();
    onnavigate?.(urlDetailHref(url));
  }

  async function loadData(id, nextOffset, nextRating, nextSort, nextOrder) {
    const currentRequest = ++requestID;
    loading = true;
    error = '';
    try {
      const data = await getCoreWebVitals(
        id,
        PAGE_SIZE,
        nextOffset,
        nextRating,
        nextSort,
        nextOrder,
      );
      if (currentRequest === requestID) {
        report = data;
      }
    } catch (err) {
      if (currentRequest === requestID) {
        error = err.message || String(err);
        onerror?.(error);
      }
    } finally {
      if (currentRequest === requestID) {
        loading = false;
      }
    }
  }

  function retry() {
    void loadData(sessionId, offset, rating, sort, order);
  }

  $effect(() => {
    if (!sessionId) return;
    void loadData(sessionId, offset, rating, sort, order);
  });
</script>

<section class="cwv-report" aria-labelledby="cwv-report-title">
  <header class="cwv-header">
    <div>
      <div class="cwv-title-row">
        <h2 id="cwv-report-title">{t('report.coreWebVitals.title')}</h2>
        <span class="badge badge-info">{t('report.coreWebVitals.labData')}</span>
      </div>
      <p class="cwv-scope">{t('report.coreWebVitals.scope')}</p>
      <p class="cwv-thresholds">{t('report.coreWebVitals.thresholds')}</p>
    </div>
  </header>

  {#if loading && !report}
    <div class="cwv-state" aria-live="polite">{t('common.loading')}</div>
  {:else if error && !report}
    <div class="cwv-state cwv-state-error" role="alert">
      <p>{t('report.coreWebVitals.loadFailed', { error })}</p>
      <button class="btn btn-sm" onclick={retry}>{t('report.coreWebVitals.retry')}</button>
    </div>
  {:else if summary}
    <div class="cwv-summary-grid" aria-label={t('report.coreWebVitals.title')}>
      <div class="stat-card">
        <div class="stat-value">{fmtN(summary.eligible_pages || 0)}</div>
        <div class="stat-label">{t('report.coreWebVitals.eligible')}</div>
      </div>
      <div class="stat-card">
        <div class="stat-value">{fmtN(summary.measured_pages || 0)}</div>
        <div class="stat-label">{t('report.coreWebVitals.measured')}</div>
      </div>
      <div class="stat-card cwv-summary-good">
        <div class="stat-value text-success">{fmtN(summary.good || 0)}</div>
        <div class="stat-label">{t('report.coreWebVitals.good')}</div>
      </div>
      <div class="stat-card cwv-summary-needs-improvement">
        <div class="stat-value text-warning">{fmtN(summary.needs_improvement || 0)}</div>
        <div class="stat-label">{t('report.coreWebVitals.needsImprovement')}</div>
      </div>
      <div class="stat-card cwv-summary-poor">
        <div class="stat-value text-error">{fmtN(summary.poor || 0)}</div>
        <div class="stat-label">{t('report.coreWebVitals.poor')}</div>
      </div>
      <div class="stat-card">
        <div class="stat-value">{fmtN(summary.unmeasured_pages || 0)}</div>
        <div class="stat-label">{t('report.coreWebVitals.unmeasured')}</div>
      </div>
    </div>

    {#if summary.eligible_pages === 0}
      <div class="cwv-state">{t('report.coreWebVitals.noEligible')}</div>
    {:else if summary.measured_pages === 0}
      <div class="cwv-state">{t('report.coreWebVitals.noMeasurements')}</div>
    {:else}
      <fieldset class="cwv-filter" aria-busy={loading}>
        <legend>{t('report.coreWebVitals.ratingFilter')}</legend>
        <div class="cwv-filter-buttons">
          {#each RATING_FILTERS as filterRating}
            <button
              class="cwv-filter-button"
              class:active={rating === filterRating}
              aria-pressed={rating === filterRating}
              onclick={() => setRating(filterRating)}
            >
              {ratingLabel(filterRating)}
              {#if filterRating === 'good'}
                <span>{fmtN(summary.good || 0)}</span>
              {:else if filterRating === 'needs_improvement'}
                <span>{fmtN(summary.needs_improvement || 0)}</span>
              {:else if filterRating === 'poor'}
                <span>{fmtN(summary.poor || 0)}</span>
              {:else}
                <span>{fmtN(summary.measured_pages || 0)}</span>
              {/if}
            </button>
          {/each}
        </div>
      </fieldset>

      {#if error}
        <div class="cwv-inline-error" role="alert">
          <span>{t('report.coreWebVitals.loadFailed', { error })}</span>
          <button class="btn btn-sm" onclick={retry}>{t('report.coreWebVitals.retry')}</button>
        </div>
      {/if}

      {#if pages.length === 0 && !loading}
        <div class="cwv-state">{t('report.coreWebVitals.noRatingMatch')}</div>
      {:else}
        <div class="table-wrap cwv-table-wrap" aria-busy={loading}>
          <table class="cwv-table">
            <thead>
              <tr>
                {#each ['url', 'lcp', 'cls', 'ttfb', 'overall'] as column}
                  <th aria-sort={sortDirection(column)}>
                    <button
                      class="cwv-sort-button"
                      aria-label={t('report.coreWebVitals.sortBy', { column: sortLabel(column) })}
                      onclick={() => changeSort(column)}
                    >
                      {sortLabel(column)}{sortIndicator(column)}
                    </button>
                  </th>
                {/each}
              </tr>
            </thead>
            <tbody>
              {#each pages as page}
                <tr>
                  <td class="cwv-url-cell">
                    <a
                      href={urlDetailHref(page.url)}
                      aria-label={t('report.coreWebVitals.openUrl', { url: page.url })}
                      title={page.url}
                      onclick={(event) => goToURL(event, page.url)}>{page.url}</a
                    >
                  </td>
                  <td class="cwv-metric-cell">
                    <span class="cwv-metric-value">{formatMilliseconds(page.lcp_ms)}</span>
                    <span class="badge {ratingClass(page.lcp_rating)}"
                      >{ratingLabel(page.lcp_rating)}</span
                    >
                  </td>
                  <td class="cwv-metric-cell">
                    <span class="cwv-metric-value">{formatCLS(page.cls)}</span>
                    <span class="badge {ratingClass(page.cls_rating)}"
                      >{ratingLabel(page.cls_rating)}</span
                    >
                  </td>
                  <td class="cwv-metric-cell">
                    <span class="cwv-metric-value">{formatMilliseconds(page.ttfb_ms)}</span>
                    <span class="badge {ratingClass(page.ttfb_rating)}"
                      >{ratingLabel(page.ttfb_rating)}</span
                    >
                  </td>
                  <td>
                    <span class="badge {ratingClass(page.overall_rating)}"
                      >{ratingLabel(page.overall_rating)}</span
                    >
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
        <Pagination
          {offset}
          limit={PAGE_SIZE}
          {total}
          onchange={(nextOffset) => (offset = nextOffset)}
        />
      {/if}
    {/if}
  {/if}
</section>

<style>
  .cwv-report {
    padding: 24px;
  }
  .cwv-header {
    border-bottom: 1px solid var(--border-light);
    margin-bottom: 20px;
    padding-bottom: 16px;
  }
  .cwv-title-row {
    align-items: center;
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }
  .cwv-title-row h2 {
    font-size: 18px;
    margin: 0;
  }
  .cwv-scope,
  .cwv-thresholds {
    color: var(--text-muted);
    font-size: 13px;
    line-height: 1.5;
    margin: 6px 0 0;
  }
  .cwv-thresholds {
    font-variant-numeric: tabular-nums;
  }
  .cwv-summary-grid {
    display: grid;
    gap: 12px;
    grid-template-columns: repeat(6, minmax(130px, 1fr));
    margin-bottom: 20px;
  }
  .cwv-summary-grid .stat-card {
    min-width: 0;
    padding: 14px 16px;
  }
  .cwv-summary-grid .stat-value {
    font-size: 24px;
    font-variant-numeric: tabular-nums;
  }
  .cwv-filter {
    border: 0;
    margin: 0 0 16px;
    padding: 0;
  }
  .cwv-filter legend {
    color: var(--text-secondary);
    font-size: 13px;
    font-weight: 600;
    margin-bottom: 8px;
    padding: 0;
  }
  .cwv-filter-buttons {
    display: inline-flex;
    flex-wrap: wrap;
  }
  .cwv-filter-button {
    align-items: center;
    background: var(--bg-card);
    border: 1px solid var(--border);
    color: var(--text-secondary);
    cursor: pointer;
    display: inline-flex;
    font-size: 13px;
    gap: 6px;
    min-height: 32px;
    padding: 5px 10px;
  }
  .cwv-filter-button:first-child {
    border-radius: var(--radius-sm) 0 0 var(--radius-sm);
  }
  .cwv-filter-button + .cwv-filter-button {
    border-left: 0;
  }
  .cwv-filter-button:last-child {
    border-radius: 0 var(--radius-sm) var(--radius-sm) 0;
  }
  .cwv-filter-button:hover {
    background: var(--bg-hover);
  }
  .cwv-filter-button.active {
    background: var(--accent-light);
    border-color: var(--accent);
    color: var(--accent);
    font-weight: 600;
  }
  .cwv-filter-button span {
    color: var(--text-muted);
    font-variant-numeric: tabular-nums;
  }
  .cwv-filter-button.active span {
    color: inherit;
  }
  .cwv-table-wrap {
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
  }
  .cwv-table {
    min-width: 740px;
  }
  .cwv-table th {
    padding: 0;
  }
  .cwv-sort-button {
    background: transparent;
    border: 0;
    color: inherit;
    cursor: pointer;
    font: inherit;
    font-size: inherit;
    font-weight: inherit;
    letter-spacing: inherit;
    padding: 10px 14px;
    text-align: left;
    text-transform: inherit;
    white-space: nowrap;
  }
  .cwv-sort-button:hover,
  .cwv-sort-button:focus-visible {
    color: var(--accent);
    outline: none;
  }
  .cwv-url-cell {
    max-width: 420px;
  }
  .cwv-url-cell a {
    display: block;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .cwv-metric-cell {
    min-width: 142px;
    white-space: nowrap;
  }
  .cwv-metric-value {
    display: inline-block;
    font-variant-numeric: tabular-nums;
    margin-right: 6px;
  }
  .cwv-state {
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    color: var(--text-muted);
    font-size: 14px;
    margin-top: 12px;
    padding: 20px;
  }
  .cwv-state-error,
  .cwv-inline-error {
    border-color: var(--error);
    color: var(--error);
  }
  .cwv-state-error p {
    margin: 0 0 12px;
  }
  .cwv-inline-error {
    align-items: center;
    background: var(--error-bg);
    border: 1px solid var(--error);
    border-radius: var(--radius-sm);
    display: flex;
    font-size: 13px;
    gap: 12px;
    justify-content: space-between;
    margin: 0 0 16px;
    padding: 10px 12px;
  }
  @media (max-width: 1100px) {
    .cwv-summary-grid {
      grid-template-columns: repeat(3, minmax(140px, 1fr));
    }
  }
  @media (max-width: 680px) {
    .cwv-report {
      padding: 16px;
    }
    .cwv-summary-grid {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
    .cwv-filter-buttons {
      display: grid;
      grid-template-columns: repeat(2, minmax(0, 1fr));
      width: 100%;
    }
    .cwv-filter-button {
      border-radius: 0;
      justify-content: space-between;
    }
    .cwv-filter-button:first-child {
      border-radius: var(--radius-sm) 0 0 0;
    }
    .cwv-filter-button:nth-child(2) {
      border-radius: 0 var(--radius-sm) 0 0;
    }
    .cwv-filter-button + .cwv-filter-button {
      border-left: 1px solid var(--border);
    }
    .cwv-filter-button:nth-child(3) {
      border-top: 0;
      border-left: 1px solid var(--border);
      border-radius: 0 0 0 var(--radius-sm);
    }
    .cwv-filter-button:last-child {
      border-radius: 0 0 var(--radius-sm) 0;
      border-left: 0;
      border-top: 0;
    }
    .cwv-inline-error {
      align-items: flex-start;
      flex-direction: column;
    }
  }
</style>
