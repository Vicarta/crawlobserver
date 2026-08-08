<script>
  import {
    getPageRankTop,
    getPageRankTreemap,
    getPageRankDistribution,
    getPageRankWeightedTop,
    getSessionPageRankEvidence,
    computePageRank,
    buildApiPath,
  } from '../api.js';
  import { fetchAll, downloadCSV } from '../utils.js';
  import { t } from '../i18n/index.svelte.js';
  import PageRankTopView from './PageRankTopView.svelte';
  import PageRankTreemap from './PageRankTreemap.svelte';
  import PageRankDistribution from './PageRankDistribution.svelte';
  import PageRankTableView from './PageRankTableView.svelte';
  import PageRankWeightedView from './PageRankWeightedView.svelte';
  import PageRankTooltip from './PageRankTooltip.svelte';
  import ExportDropdown from './ExportDropdown.svelte';

  let {
    sessionId,
    projectId,
    initialSubView = 'top',
    onnavigate,
    onpushurl,
    onerror,
    onrefresh,
  } = $props();

  let prSubView = $state(initialSubView);
  let prLoading = $state(false);
  let prTopData = $state(null);
  let prTopLimit = $state(50);
  let prTopOffset = $state(0);
  let prDistData = $state(null);
  let prTreemapData = $state(null);
  let prTreemapDepth = $state(2);
  let prTreemapMinPages = $state(1);
  let prTableData = $state(null);
  let prTableOffset = $state(0);
  let prTableDir = $state('');
  let prWeightedData = $state(null);
  let prWeightedOffset = $state(0);
  let prWeightedLimit = $state(50);
  let prWeightedSort = $state('');
  let prWeightedOrder = $state('');
  let prWeightedDir = $state('');
  let prTooltip = $state(null);
  let hasData = $state(null); // null = unknown, true/false after first load
  let computingPR = $state(false);
  let prEvidence = $state(null);

  async function loadPageRankEvidence() {
    try {
      prEvidence = await getSessionPageRankEvidence(sessionId);
    } catch (e) {
      // Evidence can be absent for legacy sessions that have not been adopted yet.
      if (e.status !== 404) onerror?.(e.message);
      prEvidence = null;
    }
  }

  async function loadPRSubView(view) {
    prLoading = true;
    try {
      if (view === 'top') {
        prTopData = await getPageRankTop(sessionId, prTopLimit, prTopOffset);
        if (hasData === null) hasData = prTopData?.total > 0;
      } else if (view === 'directory')
        prTreemapData = await getPageRankTreemap(sessionId, prTreemapDepth, prTreemapMinPages);
      else if (view === 'distribution') prDistData = await getPageRankDistribution(sessionId, 20);
      else if (view === 'weighted' && projectId)
        prWeightedData = await getPageRankWeightedTop(
          sessionId,
          projectId,
          prWeightedLimit,
          prWeightedOffset,
          prWeightedDir,
          prWeightedSort,
          prWeightedOrder,
        );
      else if (view === 'table')
        prTableData = await getPageRankTop(sessionId, 50, prTableOffset, prTableDir);
    } catch (e) {
      onerror?.(e.message);
    } finally {
      prLoading = false;
    }
  }

  function switchPRSubView(view) {
    prSubView = view;
    if (view === 'top') prTopOffset = 0;
    if (view === 'table') prTableOffset = 0;
    if (view === 'weighted') {
      prWeightedOffset = 0;
      prWeightedSort = '';
      prWeightedOrder = '';
      prWeightedDir = '';
    }
    onpushurl?.(`/sessions/${sessionId}/pagerank/${view}`);
    loadPRSubView(view);
  }

  function drillToTable(dir) {
    prTableDir = dir;
    prTableOffset = 0;
    prSubView = 'table';
    onpushurl?.(`/sessions/${sessionId}/pagerank/table`);
    loadPRSubView('table');
  }

  function drillHistToTable() {
    prTableDir = '';
    prTableOffset = 0;
    prSubView = 'table';
    onpushurl?.(`/sessions/${sessionId}/pagerank/table`);
    loadPRSubView('table');
  }

  async function handleComputePageRank() {
    computingPR = true;
    try {
      await computePageRank(sessionId);
      hasData = true;
      await Promise.all([loadPRSubView(prSubView), loadPageRankEvidence()]);
    } catch (e) {
      onerror?.(e.message);
    } finally {
      computingPR = false;
    }
  }

  let exporting = $state(false);
  let canExportCSV = $derived(hasData && (prSubView === 'top' || prSubView === 'table'));

  async function handleExportCSV() {
    if (exporting) return;
    exporting = true;
    try {
      const dir = prSubView === 'table' ? prTableDir : '';
      const allData = await fetchAll(
        (limit, offset) =>
          getPageRankTop(sessionId, limit, offset, dir).then((r) => r?.pages || []),
        50,
      );
      downloadCSV(
        `pagerank-${prSubView}.csv`,
        [
          'URL',
          'PageRank',
          'Depth',
          'Internal Links',
          'External Links',
          'Word Count',
          'Status',
          'Title',
        ],
        [
          'url',
          'pagerank',
          'depth',
          'internal_links_out',
          'external_links_out',
          'word_count',
          'status_code',
          'title',
        ],
        allData,
      );
    } finally {
      exporting = false;
    }
  }

  let apiPath = $derived.by(() => {
    const dir = prSubView === 'table' ? prTableDir : '';
    return buildApiPath(`/sessions/${sessionId}/pagerank-top`, {
      limit: 50,
      offset: 0,
      directory: dir,
    });
  });

  loadPRSubView(prSubView);
  loadPageRankEvidence();
</script>

<div class="pr-container">
  {#if prEvidence}
    <section class="pr-evidence" aria-label={t('pagerank.evidence')}>
      <div class="pr-evidence-heading">
        <strong>{t('pagerank.evidence')}</strong>
        <span class="badge" class:badge-success={prEvidence.state === 'finalized'} class:badge-warning={prEvidence.state === 'started'} class:badge-error={prEvidence.state === 'failed'}>{prEvidence.state || 'unknown'}</span>
      </div>
      <div class="pr-evidence-grid">
        <div><span>{t('pagerank.evidenceRevision')}</span><code>{prEvidence.attempt_id || '-'}</code></div>
        <div><span>{t('pagerank.evidenceSource')}</span><strong>{prEvidence.source || '-'}</strong></div>
        <div><span>{t('pagerank.evidencePredicate')}</span><code>{prEvidence.predicate_version || '-'}</code></div>
        <div><span>{t('pagerank.evidenceEligible')}</span><strong>{prEvidence.eligible_page_count ?? '-'}</strong></div>
        <div><span>{t('pagerank.evidencePositive')}</span><strong>{prEvidence.positive_page_count ?? '-'}</strong></div>
        <div><span>{t('pagerank.evidenceZero')}</span><strong>{prEvidence.zero_page_count ?? '-'}</strong></div>
      </div>
    </section>
  {/if}

  <div class="pr-subview-header">
    <div class="pr-subview-bar">
      <button
        class="pr-subview-btn"
        class:pr-subview-active={prSubView === 'top'}
        onclick={() => switchPRSubView('top')}>{t('pagerank.topPages')}</button
      >
      <button
        class="pr-subview-btn"
        class:pr-subview-active={prSubView === 'directory'}
        onclick={() => switchPRSubView('directory')}>{t('pagerank.byDirectory')}</button
      >
      <button
        class="pr-subview-btn"
        class:pr-subview-active={prSubView === 'distribution'}
        onclick={() => switchPRSubView('distribution')}>{t('pagerank.distribution')}</button
      >
      <button
        class="pr-subview-btn"
        class:pr-subview-active={prSubView === 'table'}
        onclick={() => switchPRSubView('table')}>{t('pagerank.fullTable')}</button
      >
      <button
        class="pr-subview-btn pr-subview-premium"
        class:pr-subview-active={prSubView === 'weighted'}
        onclick={() => switchPRSubView('weighted')}>&#9733; {t('pagerank.weighted')}</button
      >
    </div>
    {#if canExportCSV}
      <ExportDropdown onexportcsv={handleExportCSV} {exporting} {apiPath} />
    {/if}
    {#if hasData}
      <button
        class="btn btn-sm pr-recalc-btn"
        onclick={handleComputePageRank}
        disabled={computingPR}
      >
        <svg
          viewBox="0 0 24 24"
          width="14"
          height="14"
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          stroke-linecap="round"
          stroke-linejoin="round"
          ><polyline points="23 4 23 10 17 10" /><path
            d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10"
          /></svg
        >
        {computingPR ? t('actionBar.computing') : 'Re-calculate internal PR'}
      </button>
    {/if}
  </div>

  {#if prLoading}
    <p class="loading-msg">{t('common.loading')}</p>
  {:else if hasData === false}
    <div class="pr-empty-state">
      <svg
        viewBox="0 0 24 24"
        width="48"
        height="48"
        fill="none"
        stroke="currentColor"
        stroke-width="1.5"
        stroke-linecap="round"
        stroke-linejoin="round"
        class="pr-empty-icon"
      >
        <circle cx="11" cy="11" r="2.5" fill="currentColor" stroke="none" /><circle
          cx="13"
          cy="4"
          r="1.5"
          fill="currentColor"
          stroke="none"
        /><circle cx="20" cy="10" r="1.5" fill="currentColor" stroke="none" /><circle
          cx="16"
          cy="17"
          r="1.5"
          fill="currentColor"
          stroke="none"
        /><circle cx="5" cy="17" r="1.5" fill="currentColor" stroke="none" /><path
          d="M11 11l2-7M11 11l9-1M11 11l5 6M11 11l-6 6M20 10l-4 7M20 10l-7-6"
        />
      </svg>
      <p class="pr-empty-text">{t('pagerank.noData')}</p>
      <button class="btn btn-primary" onclick={handleComputePageRank} disabled={computingPR}>
        <svg
          viewBox="0 0 24 24"
          width="16"
          height="16"
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          stroke-linecap="round"
          stroke-linejoin="round"
          ><circle cx="11" cy="11" r="2.5" fill="currentColor" stroke="none" /><circle
            cx="13"
            cy="4"
            r="1.5"
            fill="currentColor"
            stroke="none"
          /><circle cx="20" cy="10" r="1.5" fill="currentColor" stroke="none" /><circle
            cx="16"
            cy="17"
            r="1.5"
            fill="currentColor"
            stroke="none"
          /><circle cx="5" cy="17" r="1.5" fill="currentColor" stroke="none" /><path
            d="M11 11l2-7M11 11l9-1M11 11l5 6M11 11l-6 6M20 10l-4 7M20 10l-7-6"
          /></svg
        >
        {computingPR ? t('actionBar.computing') : 'Re-calculate internal PR'}
      </button>
    </div>
  {:else if prSubView === 'top'}
    <PageRankTopView
      data={prTopData}
      offset={prTopOffset}
      limit={prTopLimit}
      {onnavigate}
      ontooltip={(t) => (prTooltip = t)}
      onlimitchange={(l) => {
        prTopLimit = l;
        prTopOffset = 0;
        loadPRSubView('top');
      }}
      onpagechange={(o) => {
        prTopOffset = o;
        loadPRSubView('top');
      }}
    />
  {:else if prSubView === 'directory'}
    <PageRankTreemap
      data={prTreemapData}
      depth={prTreemapDepth}
      minPages={prTreemapMinPages}
      ontooltip={(t) => (prTooltip = t)}
      ondrill={drillToTable}
      ondepthchange={(d) => {
        prTreemapDepth = d;
        loadPRSubView('directory');
      }}
      onminpageschange={(m) => {
        prTreemapMinPages = m;
        loadPRSubView('directory');
      }}
    />
  {:else if prSubView === 'distribution'}
    <PageRankDistribution
      data={prDistData}
      ontooltip={(t) => (prTooltip = t)}
      ondrill={drillHistToTable}
    />
  {:else if prSubView === 'weighted'}
    {#if !projectId}
      <div class="pr-empty-state">
        <p class="pr-empty-text">{t('pagerank.weightedNeedProject')}</p>
      </div>
    {:else}
      <PageRankWeightedView
        data={prWeightedData}
        offset={prWeightedOffset}
        limit={prWeightedLimit}
        sortColumn={prWeightedSort}
        sortOrder={prWeightedOrder}
        dirFilter={prWeightedDir}
        {onnavigate}
        ontooltip={(t) => (prTooltip = t)}
        onsort={(col, ord) => {
          prWeightedSort = col;
          prWeightedOrder = ord;
          prWeightedOffset = 0;
          loadPRSubView('weighted');
        }}
        onfilterchange={(dir, apply) => {
          prWeightedDir = dir;
          if (apply) {
            prWeightedOffset = 0;
            loadPRSubView('weighted');
          }
        }}
        onlimitchange={(l) => {
          prWeightedLimit = l;
          prWeightedOffset = 0;
          loadPRSubView('weighted');
        }}
        onpagechange={(o) => {
          prWeightedOffset = o;
          loadPRSubView('weighted');
        }}
      />
    {/if}
  {:else if prSubView === 'table'}
    <PageRankTableView
      data={prTableData}
      offset={prTableOffset}
      dirFilter={prTableDir}
      {onnavigate}
      onfilterchange={(dir, apply) => {
        prTableDir = dir;
        if (apply) {
          prTableOffset = 0;
          loadPRSubView('table');
        }
      }}
      onpagechange={(o) => {
        prTableOffset = o;
        loadPRSubView('table');
      }}
    />
  {/if}
</div>

<PageRankTooltip tooltip={prTooltip} />

<style>
  .pr-empty-state {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 16px;
    padding: 64px 20px;
    text-align: center;
  }
  .pr-empty-icon {
    color: var(--text-muted);
    opacity: 0.4;
  }
  .pr-empty-text {
    color: var(--text-muted);
    font-size: 15px;
    margin: 0;
  }
  .pr-subview-header {
    display: flex;
    align-items: center;
    gap: 12px;
    margin-bottom: 24px;
  }
  .pr-subview-header :global(.pr-subview-bar) {
    margin-bottom: 0;
  }
  .pr-recalc-btn {
    margin-left: auto;
  }
  .pr-evidence {
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--bg-card);
    padding: 12px;
    margin-bottom: 16px;
  }
  .pr-evidence-heading {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    margin-bottom: 10px;
  }
  .pr-evidence-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(120px, 1fr));
    gap: 8px 12px;
  }
  .pr-evidence-grid span {
    display: block;
    color: var(--text-muted);
    font-size: 11px;
    margin-bottom: 2px;
  }
  .pr-evidence-grid strong,
  .pr-evidence-grid code {
    display: block;
    font-size: 12px;
    overflow-wrap: anywhere;
  }
</style>
