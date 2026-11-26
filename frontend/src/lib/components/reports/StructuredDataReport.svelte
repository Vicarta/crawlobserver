<script>
  import { getStructuredDataReport } from '../../api.js';
  import { t } from '../../i18n/index.svelte.js';
  import { fmtN } from '../../utils.js';
  import Pagination from '../Pagination.svelte';

  let { sessionId, onnavigate, onerror } = $props();

  const PAGE_SIZE = 50;
  const STATUSES = ['', 'not_checked', 'no_markup', 'issues', 'clean'];
  const FORMATS = ['', 'json-ld', 'microdata'];

  let report = $state(null);
  let loading = $state(false);
  let error = $state('');
  let offset = $state(0);
  let status = $state('');
  let format = $state('');
  let typeDraft = $state('');
  let typeFilter = $state('');
  let requestID = 0;

  const summary = $derived(report?.summary || null);
  const pages = $derived(report?.pages || []);
  const total = $derived(report?.total || 0);

  function statusLabel(value) {
    return t(`report.structuredData.status.${value || 'all'}`);
  }

  function statusClass(value) {
    if (value === 'issues') return 'badge-error';
    if (value === 'clean') return 'badge-success';
    if (value === 'no_markup') return 'badge-info';
    return 'badge-warning';
  }

  function sourceLabel(value) {
    return t(`report.structuredData.source.${value}`) || value;
  }

  function formatLabel(value) {
    if (value === 'json-ld') return 'JSON-LD';
    return value || t('common.none');
  }

  function coverageLabel(value) {
    return t(`report.structuredData.coverage.${value || 'not_checked'}`);
  }

  function diagnosticLabel(item) {
    const location = [];
    if (item.source) location.push(sourceLabel(item.source));
    if (item.block_index) location.push(t('report.structuredData.block', { index: item.block_index }));
    if (item.byte_end > item.byte_start) {
      location.push(t('report.structuredData.bytes', { start: item.byte_start, end: item.byte_end }));
    } else if (item.code === 'json_ld_invalid_json') {
      location.push(t('report.structuredData.eofByte', { offset: item.byte_start }));
    }
    return `${item.code}${location.length ? ` (${location.join(', ')})` : ''}`;
  }

  function urlDetailHref(url) {
    return `/sessions/${sessionId}/url/${encodeURIComponent(url)}`;
  }

  function goToURL(event, url) {
    event.preventDefault();
    onnavigate?.(urlDetailHref(url));
  }

  async function loadData(id, nextOffset, nextStatus, nextFormat, nextType) {
    const currentRequest = ++requestID;
    loading = true;
    error = '';
    try {
      const data = await getStructuredDataReport(id, {
        limit: PAGE_SIZE,
        offset: nextOffset,
        status: nextStatus,
        format: nextFormat,
        type: nextType,
      });
      if (currentRequest === requestID) report = data;
    } catch (err) {
      if (currentRequest === requestID) {
        error = err.message || String(err);
        onerror?.(error);
      }
    } finally {
      if (currentRequest === requestID) loading = false;
    }
  }

  function retry() {
    void loadData(sessionId, offset, status, format, typeFilter);
  }

  function applyTypeFilter(event) {
    event.preventDefault();
    offset = 0;
    typeFilter = typeDraft.trim();
  }

  function changeStatus(value) {
    status = value;
    offset = 0;
  }

  function changeFormat(value) {
    format = value;
    offset = 0;
  }

  $effect(() => {
    if (!sessionId) return;
    void loadData(sessionId, offset, status, format, typeFilter);
  });
</script>

<section class="structured-data-report" aria-labelledby="structured-data-report-title">
  <header class="sd-report-header">
    <div>
      <h2 id="structured-data-report-title">{t('report.structuredData.title')}</h2>
      <p>{t('report.structuredData.scope')}</p>
    </div>
    {#if summary}
      <span class="sd-total">{fmtN(summary.total)} {t('report.structuredData.htmlPages')}</span>
    {/if}
  </header>

  {#if !report && loading}
    <div class="sd-state" aria-live="polite">{t('common.loading')}</div>
  {:else if !report && error}
    <div class="sd-state sd-state-error" role="alert">
      <p>{t('report.structuredData.loadFailed', { error })}</p>
      <button class="btn btn-sm" onclick={retry}>{t('report.structuredData.retry')}</button>
    </div>
  {:else if report}
    <div class="sd-summary" aria-label={t('report.structuredData.summary')}>
      {#each ['not_checked', 'no_markup', 'issues', 'clean'] as key}
        <div class="sd-summary-item">
          <span class="sd-summary-count">{fmtN(summary?.[key] || 0)}</span>
          <span>{statusLabel(key)}</span>
        </div>
      {/each}
    </div>

    <form class="sd-filters" onsubmit={applyTypeFilter}>
      <label>
        <span>{t('common.status')}</span>
        <select value={status} onchange={(event) => changeStatus(event.currentTarget.value)}>
          {#each STATUSES as value}
            <option value={value}>{statusLabel(value)}</option>
          {/each}
        </select>
      </label>
      <label>
        <span>{t('common.format')}</span>
        <select value={format} onchange={(event) => changeFormat(event.currentTarget.value)}>
          {#each FORMATS as value}
            <option value={value}>{value ? formatLabel(value) : t('report.structuredData.allFormats')}</option>
          {/each}
        </select>
      </label>
      <label class="sd-type-filter">
        <span>{t('report.structuredData.schemaType')}</span>
        <input bind:value={typeDraft} type="search" maxlength="256" />
      </label>
      <button class="btn btn-sm" type="submit">{t('common.search')}</button>
      {#if loading}
        <span class="sd-loading" aria-live="polite">{t('common.loading')}</span>
      {/if}
    </form>

    {#if error}
      <div class="sd-inline-error" role="alert">
        <span>{t('report.structuredData.loadFailed', { error })}</span>
        <button class="btn btn-sm" onclick={retry}>{t('report.structuredData.retry')}</button>
      </div>
    {/if}

    {#if summary?.total === 0}
      <div class="sd-state">{t('report.structuredData.noHtmlPages')}</div>
    {:else if total === 0}
      <div class="sd-state">{t('report.structuredData.noMatches')}</div>
    {:else}
      <div class="sd-table-wrap" aria-busy={loading}>
        <table class="sd-table">
          <thead>
            <tr>
              <th>{t('common.url')}</th>
              <th>{t('common.status')}</th>
              <th>{t('report.structuredData.origin')}</th>
              <th>{t('common.format')}</th>
              <th>{t('report.structuredData.schemaTypes')}</th>
              <th>{t('report.structuredData.diagnostics')}</th>
            </tr>
          </thead>
          <tbody>
            {#each pages as page (page.url)}
              <tr>
                <td class="sd-url">
                  <a href={urlDetailHref(page.url)} title={page.url} onclick={(event) => goToURL(event, page.url)}>
                    {page.url}
                  </a>
                </td>
                <td>
                  <span class="badge {statusClass(page.status)}">{statusLabel(page.status)}</span>
                  {#if page.truncated}
                    <small class="sd-incomplete">{t('report.structuredData.incomplete')}</small>
                  {/if}
                </td>
                <td>{page.sources?.map(sourceLabel).join(', ') || t('common.none')}</td>
                <td>{page.formats?.map(formatLabel).join(', ') || t('common.none')}</td>
                <td>
                  <span>{page.types?.join(', ') || t('common.none')}</span>
                  {#if page.json_ld_entity_count > 0}
                    <small>{t('report.structuredData.jsonLdEntityOccurrences', { count: page.json_ld_entity_count })}</small>
                  {/if}
                </td>
                <td>
                  {#if page.diagnostics?.length}
                    <ul class="sd-diagnostics">
                      {#each page.diagnostics as diagnostic}
                        <li>{diagnosticLabel(diagnostic)}</li>
                      {/each}
                    </ul>
                    {#if page.diagnostics_truncated}
                      <small>{t('report.structuredData.moreDiagnostics', { count: Math.max(0, page.diagnostic_count - page.diagnostics.length) })}</small>
                    {/if}
                  {:else}
                    <span>{t('common.none')}</span>
                  {/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      <Pagination {offset} limit={PAGE_SIZE} {total} onchange={(nextOffset) => (offset = nextOffset)} />
    {/if}
  {/if}
</section>

<style>
  .structured-data-report { box-sizing: border-box; min-width: 0; max-width: 100%; padding: 24px; }
  .sd-report-header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 20px;
    border-bottom: 1px solid var(--border-light);
    margin-bottom: 18px;
    padding-bottom: 14px;
  }
  .sd-report-header h2 { margin: 0; font-size: 18px; }
  .sd-report-header p { margin: 6px 0 0; color: var(--text-muted); font-size: 13px; }
  .sd-total { color: var(--text-muted); font-size: 13px; white-space: nowrap; }
  .sd-summary {
    display: grid;
    grid-template-columns: repeat(4, minmax(100px, 1fr));
    gap: 12px;
    border-bottom: 1px solid var(--border-light);
    padding: 0 0 18px;
  }
  .sd-summary-item { display: grid; gap: 3px; color: var(--text-muted); font-size: 12px; }
  .sd-summary-count { color: var(--text); font-size: 18px; font-weight: 650; }
  .sd-filters {
    display: flex;
    align-items: end;
    flex-wrap: wrap;
    gap: 12px;
    padding: 16px 0;
  }
  .sd-filters label { display: grid; gap: 5px; color: var(--text-muted); font-size: 12px; }
  .sd-filters select, .sd-filters input {
    min-width: 150px;
    height: 34px;
    border: 1px solid var(--border);
    border-radius: 4px;
    background: var(--bg-input);
    color: var(--text);
    padding: 0 8px;
  }
  .sd-filters .sd-type-filter input { min-width: 190px; }
  .sd-loading { color: var(--text-muted); font-size: 12px; padding-bottom: 8px; }
  .sd-state { color: var(--text-muted); padding: 28px 0; }
  .sd-state-error, .sd-inline-error { color: var(--error); }
  .sd-inline-error { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 8px 0; }
  .sd-table-wrap { min-width: 0; max-width: 100%; overflow-x: auto; overscroll-behavior-x: contain; border-top: 1px solid var(--border-light); }
  .sd-table { width: 100%; border-collapse: collapse; font-size: 13px; }
  .sd-table th, .sd-table td { padding: 10px 12px; border-bottom: 1px solid var(--border-light); text-align: left; vertical-align: top; }
  .sd-table th { position: sticky; top: 0; background: var(--bg-card); color: var(--text-muted); font-size: 11px; font-weight: 600; white-space: nowrap; }
  .sd-table td { max-width: 290px; }
  .sd-table .sd-url { min-width: 240px; max-width: 380px; overflow-wrap: anywhere; }
  .sd-table .sd-url a { color: var(--accent); }
  .sd-table small { display: block; color: var(--text-muted); margin-top: 4px; }
  .sd-diagnostics { display: grid; gap: 4px; margin: 0; padding-left: 16px; }
  @media (max-width: 720px) {
    .structured-data-report { min-width: 0; max-width: 100%; padding: 16px; }
    .sd-summary { grid-template-columns: repeat(2, minmax(100px, 1fr)); }
    .sd-report-header { flex-direction: column; gap: 8px; }
    .sd-table { min-width: 840px; }
  }
</style>
