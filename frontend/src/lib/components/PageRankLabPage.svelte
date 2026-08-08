<script>
  import { onMount } from 'svelte';
  import {
    getProjects,
    getProjectCurrentSnapshot,
    getInterlinkingSimulation,
    simulateProjectCurrentSnapshotInterlinking,
  } from '../api.js';
  import { fmtN } from '../utils.js';
  import DataTable from './DataTable.svelte';

  let { onerror } = $props();

  const PAGE_SIZE_OPTIONS = ['25', '50', '100', '250', '500', '1000', 'all'];
  const PAGE_SIZE_ALL_CHUNK = 1000;
  const MAX_FOCUSED_IMPACT_ITEMS = 8;
  const COMPACT_FOCUSED_IMPACT_THRESHOLD = 12;

  let projects = $state([]);
  let projectId = $state('');
  let snapshot = $state(null);
  let loading = $state(false);
  let running = $state(false);
  let addLinksText = $state('');
  let removeLinksText = $state('');
  let message = $state('');
  let simId = $state('');
  let simSessionId = $state('');
  let simulation = $state(null);
  let results = $state([]);
  let total = $state(0);
  let resultOffset = $state(0);
  let resultSort = $state('pagerank_diff');
  let resultOrder = $state('desc');
  let resultFilters = $state({});
  let resultPageSize = $state('100');
  let focusedImpacts = $state([]);
  let focusedImpactsOmitted = $state(0);
  let lastAddLinks = $state([]);
  let lastRemoveLinks = $state([]);
  let pollTimer = null;

  const resultColumns = [
    { label: 'URL', sortKey: 'url', defaultWidth: 620, minWidth: 240 },
    { label: 'Before', sortKey: 'pagerank_before', defaultWidth: 120, minWidth: 90 },
    { label: 'After', sortKey: 'pagerank_after', defaultWidth: 120, minWidth: 90 },
    { label: 'Diff', sortKey: 'pagerank_diff', defaultWidth: 110, minWidth: 84 },
  ];

  let resultHasMore = $derived(resultOffset + results.length < total);
  let resultLimit = $derived(resultPageSize === 'all' ? PAGE_SIZE_ALL_CHUNK : Number(resultPageSize));
  let hasActiveResultFilters = $derived(
    Object.values(resultFilters).some((value) => value !== '' && value != null),
  );

  function projectName(id) {
    return projects.find((p) => p.id === id)?.name || 'Project';
  }

  function parseLinkText(text) {
    return text.split(/\n+/).flatMap((rawLine) => {
      const line = rawLine.trim();
      if (!line) return [];

      if (line.includes('->')) {
        const arrowIndex = line.indexOf('->');
        const source = line.slice(0, arrowIndex).trim();
        const targets = line
          .slice(arrowIndex + 2)
          .split(/[\t,]/)
          .map((target) => target.trim())
          .filter(Boolean);
        return targets.map((target) => ({ source, target })).filter((link) => link.source);
      }

      const parts = line
        .split(/[\t,]/)
        .map((part) => part.trim())
        .filter(Boolean);
      if (parts.length < 2) return [];
      const [source, ...targets] = parts;
      return targets.map((target) => ({ source, target })).filter((link) => link.source);
    });
  }

  function addLinks() {
    return parseLinkText(addLinksText);
  }

  function removeLinks() {
    return parseLinkText(removeLinksText);
  }

  function resetResultState() {
    simulation = null;
    results = [];
    total = 0;
    resultOffset = 0;
    resultSort = 'pagerank_diff';
    resultOrder = 'desc';
    resultFilters = {};
    focusedImpacts = [];
    focusedImpactsOmitted = 0;
  }

  async function loadProjects() {
    loading = true;
    try {
      projects = await getProjects();
      if (!projectId && projects.length > 0) {
        projectId = projects[0].id;
      }
      if (projectId) await loadSnapshot();
    } catch (e) {
      onerror?.(e.message);
    } finally {
      loading = false;
    }
  }

  async function loadSnapshot() {
    if (!projectId) return;
    snapshot = null;
    resetResultState();
    message = '';
    try {
      snapshot = await getProjectCurrentSnapshot(projectId);
    } catch (e) {
      message = e.message;
    }
  }

  async function runSimulation() {
    const linksToAdd = addLinks();
    const linksToRemove = removeLinks();
    if (!projectId || (linksToAdd.length === 0 && linksToRemove.length === 0)) {
      message = 'Add at least one link pair to add or remove.';
      return;
    }
    running = true;
    message = '';
    resetResultState();
    lastAddLinks = linksToAdd;
    lastRemoveLinks = linksToRemove;
    try {
      const res = await simulateProjectCurrentSnapshotInterlinking(
        projectId,
        linksToAdd,
        linksToRemove,
      );
      simId = res?.simulation_id || '';
      simSessionId = res?.current_session_id || '';
      message = `Simulation started for ${projectName(projectId)} current snapshot.`;
      pollSimulation();
    } catch (e) {
      message = e.message;
      onerror?.(e.message);
      running = false;
    }
  }

  async function pollSimulation(attempt = 0) {
    if (!simId || !simSessionId) return;
    try {
      await loadResults(0);
      await loadFocusedImpacts();
      running = false;
      message = 'Simulation complete.';
      return;
    } catch (e) {
      if (attempt >= 20) {
        running = false;
        onerror?.(e.message);
        return;
      }
      pollTimer = setTimeout(() => pollSimulation(attempt + 1), 1500);
    }
  }

  async function loadResults(offset = resultOffset) {
    if (!simId || !simSessionId) return;
    if (resultPageSize === 'all') {
      await loadAllResults();
      return;
    }
    const res = await getInterlinkingSimulation(
      simSessionId,
      simId,
      resultLimit,
      offset,
      resultSort,
      resultOrder,
      resultFilters,
      { htmlOnly: true },
    );
    simulation = res?.simulation || simulation;
    results = res?.results || [];
    total = res?.total || 0;
    resultOffset = offset;
  }

  async function loadAllResults() {
    let offset = 0;
    let allResults = [];
    let expectedTotal = 0;
    while (true) {
      const res = await getInterlinkingSimulation(
        simSessionId,
        simId,
        PAGE_SIZE_ALL_CHUNK,
        offset,
        resultSort,
        resultOrder,
        resultFilters,
        { htmlOnly: true },
      );
      simulation = res?.simulation || simulation;
      const chunk = res?.results || [];
      expectedTotal = res?.total || 0;
      allResults = [...allResults, ...chunk];
      if (allResults.length >= expectedTotal || chunk.length === 0) break;
      offset += PAGE_SIZE_ALL_CHUNK;
    }
    results = allResults;
    total = expectedTotal;
    resultOffset = 0;
  }

  async function loadFocusedImpacts() {
    if (!simId || !simSessionId) return;

    const allFocusItems = [
      ...unique(lastAddLinks.map((link) => link.target)).map((url) => ({
        kind: 'Target page receiving added link',
        url,
      })),
      ...unique(lastRemoveLinks.map((link) => link.source)).map((url) => ({
        kind: 'Source page losing existing link',
        url,
      })),
      ...unique(lastRemoveLinks.map((link) => link.target)).map((url) => ({
        kind: 'Target page losing inbound link',
        url,
      })),
    ];

    let focusItems = allFocusItems.slice(0, MAX_FOCUSED_IMPACT_ITEMS);
    if (allFocusItems.length > COMPACT_FOCUSED_IMPACT_THRESHOLD) {
      focusItems = primaryFocusedImpactItems().slice(0, 1);
    }
    focusedImpactsOmitted = Math.max(0, allFocusItems.length - focusItems.length);

    focusedImpacts = focusItems.map((item) => ({ ...item, row: null, loading: true }));

    const resolved = [];
    for (const item of focusItems) {
      resolved.push(await loadFocusedImpact(item));
    }
    focusedImpacts = resolved;
  }

  function unique(items) {
    return [...new Set(items.filter(Boolean))];
  }

  function primaryFocusedImpactItems() {
    const removeSources = unique(lastRemoveLinks.map((link) => link.source)).map((url) => ({
      kind: 'Source page losing existing link',
      url,
    }));
    if (removeSources.length > 0) return removeSources;

    const addTargets = unique(lastAddLinks.map((link) => link.target)).map((url) => ({
      kind: 'Target page receiving added link',
      url,
    }));
    if (addTargets.length > 0) return addTargets;

    return unique(lastRemoveLinks.map((link) => link.target)).map((url) => ({
      kind: 'Target page losing inbound link',
      url,
    }));
  }

  async function loadFocusedImpact(item) {
    for (const candidate of urlVariants(item.url)) {
      try {
        const res = await getInterlinkingSimulation(
          simSessionId,
          simId,
          1,
          0,
          'url',
          'asc',
          { url_exact: candidate },
        );
        const row = res?.results?.[0] || null;
        if (row) {
          return { ...item, resolvedUrl: candidate, row, loading: false };
        }
      } catch (e) {
        return { ...item, row: null, loading: false, error: e.message };
      }
    }
    return { ...item, row: null, loading: false };
  }

  function urlVariants(rawUrl) {
    const trimmed = (rawUrl || '').trim();
    if (!trimmed) return [];
    const variants = [trimmed];
    try {
      const url = new URL(trimmed);
      if (url.hostname.startsWith('www.')) {
        url.hostname = url.hostname.slice(4);
      } else {
        url.hostname = `www.${url.hostname}`;
      }
      const alternate = url.toString();
      if (!variants.includes(alternate)) variants.push(alternate);
    } catch {
      // Keep the original value for non-standard input.
    }
    return variants;
  }

  function formatDiff(value) {
    if (value == null) return '-';
    return `${value > 0 ? '+' : ''}${value.toFixed(3)}`;
  }

  function handleResultSort(col, order) {
    resultSort = col;
    resultOrder = order;
    resultOffset = 0;
    loadResults(0).catch((e) => onerror?.(e.message));
  }

  function setResultFilter(key, value) {
    resultFilters = { ...resultFilters, [key]: value };
  }

  function applyResultFilters() {
    resultOffset = 0;
    loadResults(0).catch((e) => onerror?.(e.message));
  }

  function clearResultFilters() {
    resultFilters = {};
    resultOffset = 0;
    loadResults(0).catch((e) => onerror?.(e.message));
  }

  function handlePageSizeChange() {
    resultOffset = 0;
    loadResults(0).catch((e) => onerror?.(e.message));
  }

  onMount(() => {
    loadProjects();
    return () => {
      if (pollTimer) clearTimeout(pollTimer);
    };
  });
</script>

<div class="breadcrumb">
  <span class="breadcrumb-active">PageRank Lab</span>
</div>

<div class="prlab-shell">
  <section class="prlab-intro">
    <div>
      <p class="eyebrow">Simulation</p>
      <h2>Internal PageRank Lab</h2>
      <p>
        Test how adding or removing links would change the current project graph before editing the
        website.
      </p>
    </div>
    <div class="prlab-project-card">
      <label>
        <span>Project</span>
        <select
          bind:value={projectId}
          onchange={loadSnapshot}
          disabled={loading || projects.length === 0}
        >
          {#each projects as project}
            <option value={project.id}>{project.name}</option>
          {/each}
        </select>
      </label>
      <div class="snapshot-meta">
        <span>Current snapshot</span>
        <strong>{snapshot?.current_session_id || '-'}</strong>
      </div>
    </div>
  </section>

  <section class="prlab-workspace">
    <div class="prlab-editor">
      <div class="section-heading">
        <h3>Link changes</h3>
        <button class="btn btn-primary" onclick={runSimulation} disabled={running || !projectId}>
          {running ? 'Running...' : 'Run simulation'}
        </button>
      </div>
      <div class="change-grid">
        <label>
          <span>Links to add</span>
          <textarea
            bind:value={addLinksText}
            spellcheck="false"
            placeholder="https://example.com/source -> https://example.com/target&#10;https://example.com/blog, https://example.com/service"
          ></textarea>
        </label>
        <label>
          <span>Links to remove</span>
          <textarea
            bind:value={removeLinksText}
            spellcheck="false"
            placeholder="https://example.com/source -> https://example.com/current-target&#10;https://example.com/page, https://example.com/old-link"
          ></textarea>
        </label>
      </div>
      <p class="field-hint">
        One pair per line. Use either <code>source -> target</code>, comma, or tab separation.
        Removed links must already exist in the current snapshot.
      </p>
      {#if message}<div class="prlab-message">{message}</div>{/if}
    </div>

    <aside class="prlab-summary">
      <div>
        <span class="summary-label">Added links</span>
        <strong>{fmtN(addLinks().length)}</strong>
      </div>
      <div>
        <span class="summary-label">Removed links</span>
        <strong>{fmtN(removeLinks().length)}</strong>
      </div>
      <div>
        <span class="summary-label">Improved pages</span>
        <strong>{fmtN(simulation?.pages_improved || 0)}</strong>
      </div>
      <div>
        <span class="summary-label">Declined pages</span>
        <strong>{fmtN(simulation?.pages_declined || 0)}</strong>
      </div>
      <div>
        <span class="summary-label">Max diff</span>
        <strong>{simulation ? simulation.max_diff.toFixed(3) : '-'}</strong>
      </div>
    </aside>
  </section>

  {#if focusedImpacts.length > 0}
    <section class="prlab-focus">
      <div class="section-heading">
        <h3>Directly evaluated pages</h3>
        {#if focusedImpactsOmitted > 0}
          <span>Showing the primary page. {fmtN(focusedImpactsOmitted)} more direct pages are in the table below.</span>
        {:else}
          <span>Shown before the wider affected-page set</span>
        {/if}
      </div>
      <div class="focus-grid">
        {#each focusedImpacts as item}
          <article class="focus-card">
            <div class="focus-kind">{item.kind}</div>
            <div class="focus-url" title={item.resolvedUrl || item.url}>{item.resolvedUrl || item.url}</div>
            {#if item.loading}
              <div class="focus-warning">Loading impact...</div>
            {:else if item.row}
              <div class="focus-metrics">
                <span>
                  <small>Before</small>
                  <strong>{item.row.pagerank_before.toFixed(3)}</strong>
                </span>
                <span>
                  <small>After</small>
                  <strong>{item.row.pagerank_after.toFixed(3)}</strong>
                </span>
                <span>
                  <small>Diff</small>
                  <strong
                    class:positive={item.row.pagerank_diff > 0}
                    class:negative={item.row.pagerank_diff < 0}
                    >{formatDiff(item.row.pagerank_diff)}</strong
                  >
                </span>
              </div>
            {:else if item.error}
              <div class="focus-warning">{item.error}</div>
            {:else}
              <div class="focus-warning">This URL is not present in the current PageRank graph.</div>
            {/if}
          </article>
        {/each}
      </div>
    </section>
  {/if}

  {#if simulation}
    <section class="prlab-results">
      <div class="section-heading">
        <div>
          <h3>Largest PageRank changes</h3>
          <span>{fmtN(total)} affected pages</span>
        </div>
        <div class="results-actions">
          <label class="rows-select-label">
            <span>Rows</span>
            <select bind:value={resultPageSize} onchange={handlePageSizeChange}>
              {#each PAGE_SIZE_OPTIONS as option}
                <option value={option}>{option === 'all' ? 'All' : option}</option>
              {/each}
            </select>
          </label>
          {#if hasActiveResultFilters}
            <button class="btn btn-sm" onclick={clearResultFilters}>Clear search</button>
          {/if}
        </div>
      </div>

      <DataTable
        tableId={`pagerank-lab-results-${projectId}`}
        columns={resultColumns}
        filterKeys={['url', '', '', '']}
        filters={resultFilters}
        data={results}
        offset={resultOffset}
        pageSize={resultLimit}
        hasMore={resultHasMore}
        hasActiveFilters={hasActiveResultFilters}
        onsetfilter={setResultFilter}
        onapplyfilters={applyResultFilters}
        onclearfilters={clearResultFilters}
        onnextpage={() => loadResults(resultOffset + resultLimit)}
        onprevpage={() => loadResults(Math.max(0, resultOffset - resultLimit))}
        sortColumn={resultSort}
        sortOrder={resultOrder}
        onsort={handleResultSort}
      >
        {#snippet row(row)}
          <tr>
            <td class="cell-url" title={row.url}>{row.url}</td>
            <td>{row.pagerank_before.toFixed(3)}</td>
            <td>{row.pagerank_after.toFixed(3)}</td>
            <td class:positive={row.pagerank_diff > 0} class:negative={row.pagerank_diff < 0}>
              {formatDiff(row.pagerank_diff)}
            </td>
          </tr>
        {/snippet}
      </DataTable>
    </section>
  {/if}
</div>

<style>
  .prlab-shell {
    display: grid;
    gap: 18px;
  }

  .prlab-intro,
  .prlab-workspace,
  .prlab-focus,
  .prlab-results {
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--bg-card);
  }

  .prlab-intro {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(320px, 0.36fr);
    gap: 24px;
    padding: 24px;
  }

  .prlab-intro h2 {
    margin: 4px 0 8px;
    font-size: 24px;
  }

  .prlab-intro p,
  .field-hint {
    color: var(--text-muted);
    font-size: 14px;
  }

  .eyebrow {
    color: var(--accent);
    font-size: 12px;
    font-weight: 700;
    letter-spacing: 0.04em;
    text-transform: uppercase;
  }

  .prlab-project-card {
    display: grid;
    gap: 14px;
    align-content: start;
    padding: 16px;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--bg-input);
  }

  label {
    display: grid;
    gap: 7px;
    color: var(--text-secondary);
    font-size: 13px;
    font-weight: 700;
  }

  select,
  textarea {
    width: 100%;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--bg-card);
    color: var(--text);
    font: inherit;
  }

  select {
    height: 40px;
    padding: 0 10px;
  }

  textarea {
    min-height: 210px;
    padding: 12px;
    font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
    font-size: 13px;
    resize: vertical;
  }

  .snapshot-meta {
    display: grid;
    gap: 4px;
    color: var(--text-muted);
    font-size: 12px;
  }

  .snapshot-meta strong {
    color: var(--text);
    font-size: 13px;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .prlab-workspace {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 240px;
    gap: 0;
  }

  .prlab-editor {
    display: grid;
    gap: 12px;
    padding: 20px;
  }

  .change-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 14px;
  }

  .section-heading {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
  }

  .section-heading h3 {
    font-size: 17px;
  }

  .section-heading span {
    color: var(--text-muted);
    font-size: 13px;
  }

  .results-actions {
    display: flex;
    align-items: end;
    justify-content: flex-end;
    gap: 10px;
  }

  .rows-select-label {
    grid-template-columns: auto 120px;
    align-items: center;
    gap: 8px;
    font-weight: 600;
  }

  .rows-select-label span {
    font-size: 12px;
  }

  .rows-select-label select {
    height: 34px;
  }

  .prlab-message {
    padding: 10px 12px;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--bg-input);
    color: var(--text-secondary);
    font-size: 13px;
  }

  .prlab-summary {
    display: grid;
    align-content: start;
    gap: 0;
    padding: 20px;
    border-left: 1px solid var(--border);
  }

  .prlab-summary > div {
    display: flex;
    justify-content: space-between;
    gap: 12px;
    padding: 12px 0;
    border-bottom: 1px solid var(--border);
  }

  .summary-label {
    color: var(--text-muted);
    font-size: 13px;
  }

  .prlab-focus,
  .prlab-results {
    padding: 20px;
  }

  .focus-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
    gap: 12px;
    margin-top: 14px;
  }

  .focus-card {
    display: grid;
    gap: 10px;
    padding: 14px;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--bg-input);
  }

  .focus-kind {
    color: var(--accent);
    font-size: 12px;
    font-weight: 700;
  }

  .focus-url {
    color: var(--text);
    font-size: 13px;
    font-weight: 700;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .focus-metrics {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 10px;
  }

  .focus-metrics span {
    display: grid;
    gap: 3px;
  }

  .focus-metrics small {
    color: var(--text-muted);
    font-size: 11px;
  }

  .focus-warning {
    color: var(--warning);
    font-size: 13px;
  }

  .positive {
    color: var(--success);
    font-weight: 700;
  }

  .negative {
    color: var(--error);
    font-weight: 700;
  }

  :global(.prlab-results .data-table-wrap) {
    margin-top: 14px;
  }

  :global(.prlab-results td) {
    white-space: nowrap;
  }

  :global(.prlab-results .cell-url) {
    color: var(--link);
    overflow: hidden;
    text-overflow: ellipsis;
  }

  @media (max-width: 980px) {
    .prlab-intro,
    .prlab-workspace {
      grid-template-columns: 1fr;
    }

    .change-grid {
      grid-template-columns: 1fr;
    }

    .prlab-summary {
      border-left: 0;
      border-top: 1px solid var(--border);
    }
  }
</style>
