<script>
  import { onMount } from 'svelte';
  import { getPages, getPageIssues, getRedirectPages, buildApiPath, rescanPages } from '../api.js';
  import { statusBadge, fmt, fmtSize, fmtN, trunc, fetchAll, downloadCSV } from '../utils.js';
  import { PAGE_SIZE, TAB_FILTERS } from '../tabColumns.js';
  import { t } from '../i18n/index.svelte.js';
  import DataTable from './DataTable.svelte';
  import UrlActions from './UrlActions.svelte';
  import OverflowText from './OverflowText.svelte';
  import ExportDropdown from './ExportDropdown.svelte';
  import NearDuplicatesTab from './NearDuplicatesTab.svelte';
  import HreflangValidationTab from './HreflangValidationTab.svelte';
  import URLPatternsTab from './URLPatternsTab.svelte';

  let {
    sessionId,
    initialSubView = 'all',
    initialFilters = {},
    initialOffset = 0,
    onpushurl,
    onnavigate,
    onerror,
    onopenhtml,
  } = $props();

  const SUB_VIEWS = [
    { id: 'all', label: () => t('pages.all') },
    { id: 'titles', label: () => t('pages.titles') },
    { id: 'meta', label: () => t('pages.meta') },
    { id: 'headings', label: () => t('pages.headings') },
    { id: 'images', label: () => t('pages.images') },
    { id: 'issues', label: () => t('pages.issues') },
    { id: 'indexability', label: () => t('pages.indexability') },
    { id: 'response', label: () => t('pages.response') },
    { id: 'redirects', label: () => t('pages.redirects') },
    { id: 'duplicates', label: () => t('tabs.nearDuplicates'), sep: true },
    { id: 'hreflang', label: () => t('tabs.hreflang') },
    { id: 'patterns', label: () => t('urlPatterns.patterns') },
    { id: 'parameters', label: () => t('urlPatterns.parameters') },
    { id: 'directories', label: () => t('urlPatterns.directories') },
    { id: 'hosts', label: () => t('urlPatterns.hosts') },
  ];

  const DELEGATED_VIEWS = new Set([
    'duplicates',
    'hreflang',
    'patterns',
    'parameters',
    'directories',
    'hosts',
  ]);

  // Map sub-view id to TAB_FILTERS key
  function filterKey(sv) {
    return sv === 'all' ? 'overview' : sv;
  }

  let subView = $state(initialSubView);
  let pages = $state([]);
  let pagesOffset = $state(initialOffset);
  let hasMorePages = $state(false);
  let filters = $state({ ...initialFilters });
  let sortColumn = $state('');
  let sortOrder = $state('');
  let redirectPages = $state([]);
  let redirectPagesOffset = $state(0);
  let hasMoreRedirectPages = $state(false);
  let selectedURLs = $state({});
  let rescanning = $state(false);
  let selectedList = $derived(Object.keys(selectedURLs).filter((u) => selectedURLs[u]));

  function basePath() {
    return `/sessions/${sessionId}/pages`;
  }

  function pushFilters(sv, f, offset) {
    const path = `${basePath()}/${sv || subView}`;
    const params = new URLSearchParams();
    const activeFilters = f || filters;
    for (const [k, v] of Object.entries(activeFilters)) {
      if (v !== '' && v != null) params.set(k, v);
    }
    if ((offset || 0) > 0) params.set('offset', String(offset));
    const qs = params.toString();
    onpushurl?.(qs ? `${path}?${qs}` : path);
  }

  function effectiveFilters() {
    if (filters.content_type || filters.status_code) return filters;
    return { content_type: 'text/html', ...filters };
  }

  async function loadData() {
    try {
      if (subView === 'redirects') {
        const result = await getRedirectPages(
          sessionId,
          PAGE_SIZE,
          redirectPagesOffset,
          filters,
          sortColumn,
          sortOrder,
        );
        redirectPages = result || [];
        hasMoreRedirectPages = redirectPages.length === PAGE_SIZE;
      } else if (subView === 'issues') {
        const result = await getPageIssues(sessionId, PAGE_SIZE, pagesOffset, filters);
        pages = result || [];
        hasMorePages = pages.length === PAGE_SIZE;
      } else {
        const result = await getPages(
          sessionId,
          PAGE_SIZE,
          pagesOffset,
          effectiveFilters(),
          sortColumn,
          sortOrder,
        );
        pages = result || [];
        hasMorePages = pages.length === PAGE_SIZE;
      }
    } catch (e) {
      onerror?.(e.message);
    }
  }

  function switchSubView(sv) {
    subView = sv;
    filters = {};
    pagesOffset = 0;
    redirectPagesOffset = 0;
    sortColumn = '';
    sortOrder = '';
    selectedURLs = {};
    pushFilters(sv, {}, 0);
    if (!DELEGATED_VIEWS.has(sv)) {
      loadData();
    }
  }

  function handleSort(col, ord) {
    sortColumn = col;
    sortOrder = ord;
    pagesOffset = 0;
    loadData();
  }

  async function nextPage() {
    if (subView === 'redirects') {
      redirectPagesOffset += PAGE_SIZE;
      pushFilters(null, null, redirectPagesOffset);
    } else {
      pagesOffset += PAGE_SIZE;
      pushFilters(null, null, pagesOffset);
    }
    await loadData();
  }

  async function prevPage() {
    if (subView === 'redirects') {
      redirectPagesOffset = Math.max(0, redirectPagesOffset - PAGE_SIZE);
      pushFilters(null, null, redirectPagesOffset);
    } else {
      pagesOffset = Math.max(0, pagesOffset - PAGE_SIZE);
      pushFilters(null, null, pagesOffset);
    }
    await loadData();
  }

  function applyFilters() {
    pagesOffset = 0;
    pushFilters();
    loadData();
  }

  function clearFilters() {
    filters = {};
    pagesOffset = 0;
    selectedURLs = {};
    pushFilters(null, {}, 0);
    loadData();
  }

  function setFilter(key, val) {
    filters[key] = val;
    filters = { ...filters };
  }

  function hasActiveFilters() {
    return Object.values(filters).some((v) => v && v !== '');
  }

  const CSV_CONFIGS = {
    all: {
      headers: [
        'URL',
        'Status',
        'Title',
        'Words',
        'Internal Links Out',
        'External Links Out',
        'Size',
        'Time (ms)',
        'Depth',
        'PageRank',
      ],
      keys: [
        'URL',
        'StatusCode',
        'Title',
        'WordCount',
        'InternalLinksOut',
        'ExternalLinksOut',
        'BodySize',
        'FetchDurationMs',
        'Depth',
        'PageRank',
      ],
    },
    titles: {
      headers: ['URL', 'Title', 'Title Length', 'H1'],
      keys: ['URL', 'Title', 'TitleLength'],
      transform: (row) => ({ ...row, H1: row.H1?.[0] || '' }),
    },
    meta: {
      headers: ['URL', 'Meta Description', 'Meta Desc Length', 'Meta Keywords', 'OG Title'],
      keys: ['URL', 'MetaDescription', 'MetaDescLength', 'MetaKeywords', 'OGTitle'],
    },
    headings: {
      headers: ['URL', 'H1', 'H1 Count', 'H2', 'H2 Count'],
      keys: ['URL'],
      transform: (row) => ({
        ...row,
        H1_text: row.H1?.[0] || '',
        H1Count: row.H1?.length || 0,
        H2_text: row.H2?.[0] || '',
        H2Count: row.H2?.length || 0,
      }),
      customKeys: ['URL', 'H1_text', 'H1Count', 'H2_text', 'H2Count'],
    },
    images: {
      headers: ['URL', 'Images', 'Without Alt', 'Title', 'Words'],
      keys: ['URL', 'ImagesCount', 'ImagesNoAlt', 'Title', 'WordCount'],
    },
    issues: {
      headers: [
        'URL',
        'Severity',
        'Issue Type',
        'Detail',
        'Status',
        'Title',
        'Rendered Title',
        'Rendered H1',
        'Rendered Words',
        'Rendered Images',
      ],
      keys: [
        'url',
        'severity',
        'issue_type',
        'issue_detail',
        'status_code',
        'title',
        'rendered_title',
        'rendered_h1_text',
        'rendered_word_count',
        'rendered_images_count',
      ],
      transform: (row) => ({ ...row, rendered_h1_text: row.rendered_h1?.join(' | ') || '' }),
    },
    indexability: {
      headers: ['URL', 'Indexable', 'Reason', 'Meta Robots', 'Canonical', 'Canonical Is Self'],
      keys: ['URL', 'IsIndexable', 'IndexReason', 'MetaRobots', 'Canonical', 'CanonicalIsSelf'],
    },
    response: {
      headers: ['URL', 'Status', 'Content Type', 'Encoding', 'Size', 'Time (ms)', 'Final URL'],
      keys: [
        'URL',
        'StatusCode',
        'ContentType',
        'ContentEncoding',
        'BodySize',
        'FetchDurationMs',
        'FinalURL',
      ],
    },
    redirects: {
      headers: ['URL', 'Status', 'Final URL', 'Inbound Internal Links'],
      keys: ['url', 'status_code', 'final_url', 'inbound_internal_links'],
    },
  };

  let exporting = $state(false);

  async function handleExportCSV() {
    if (exporting) return;
    exporting = true;
    try {
      await exportCSV();
    } finally {
      exporting = false;
    }
  }

  async function exportCSV() {
    const cfg = CSV_CONFIGS[subView];
    if (!cfg) return;
    const fetcher =
      subView === 'redirects'
        ? (limit, offset) => getRedirectPages(sessionId, limit, offset, filters)
        : subView === 'issues'
          ? (limit, offset) => getPageIssues(sessionId, limit, offset, filters)
        : (limit, offset) => getPages(sessionId, limit, offset, effectiveFilters());
    const allData = await fetchAll(fetcher);
    const keys = cfg.customKeys || cfg.keys;
    let rows = allData;
    if (cfg.transform) rows = allData.map(cfg.transform);
    // For titles sub-view, H1 needs special handling
    if (subView === 'titles') {
      rows = allData.map((r) => ({ ...r, H1_first: r.H1?.[0] || '' }));
      downloadCSV(`pages-${subView}.csv`, cfg.headers, [...cfg.keys, 'H1_first'], rows);
      return;
    }
    downloadCSV(`pages-${subView}.csv`, cfg.headers, keys, rows);
  }

  function urlDetailHref(url) {
    return `/sessions/${sessionId}/url/${encodeURIComponent(url)}`;
  }

  function goToUrlDetail(e, url) {
    e.preventDefault();
    onnavigate?.(urlDetailHref(url));
  }

  function issueTypeLabel(type) {
    switch (type) {
      case 'soft_404':
        return t('issues.soft404');
      case 'generic_rendered_title':
        return t('issues.genericRenderedTitle');
      case 'generic_static_metadata':
        return t('issues.genericStaticMetadata');
      default:
        return type || '-';
    }
  }

  function issueSeverityLabel(severity) {
    return severity === 'error' ? t('issues.error') : t('issues.warning');
  }

  function toggleSelected(url, checked) {
    selectedURLs = { ...selectedURLs, [url]: checked };
  }

  function clearSelection() {
    selectedURLs = {};
  }

  async function handleRescanSelected() {
    if (rescanning || selectedList.length === 0) return;
    const ok = window.confirm(
      `Rescan ${selectedList.length} selected page(s)?\n\nThis will refetch the exact selected URLs and update their page data. Internal PageRank and depth will not be recomputed.`,
    );
    if (!ok) return;

    rescanning = true;
    try {
      await rescanPages(sessionId, selectedList);
      clearSelection();
      await loadData();
    } catch (e) {
      onerror?.(e.message);
    } finally {
      rescanning = false;
    }
  }

  let apiPath = $derived.by(() => {
    const endpoint =
      subView === 'redirects'
        ? `/sessions/${sessionId}/redirect-pages`
        : subView === 'issues'
          ? `/sessions/${sessionId}/page-issues`
        : `/sessions/${sessionId}/pages`;
    const activeF = subView === 'redirects' || subView === 'issues' ? filters : effectiveFilters();
    return buildApiPath(endpoint, {
      limit: PAGE_SIZE,
      offset: 0,
      ...activeF,
      sort: sortColumn,
      order: sortOrder,
    });
  });

  onMount(() => {
    loadData();
  });
</script>

<div class="pages-explorer">
  <div class="explorer-toolbar">
    <div class="pr-subview-bar">
      {#each SUB_VIEWS as sv}
        {#if sv.sep}<span class="pr-subview-sep"></span>{/if}
        <button
          class="pr-subview-btn"
          class:pr-subview-active={subView === sv.id}
          onclick={() => switchSubView(sv.id)}>{sv.label()}</button
        >
      {/each}
    </div>
    {#if !DELEGATED_VIEWS.has(subView)}
      <ExportDropdown onexportcsv={handleExportCSV} {exporting} {apiPath} />
    {/if}
  </div>

  {#if subView === 'all'}
    {#if selectedList.length > 0}
      <div class="bulk-action-bar">
        <span>{selectedList.length} selected</span>
        <button class="btn btn-sm btn-primary" onclick={handleRescanSelected} disabled={rescanning}>
          {rescanning ? 'Rescanning...' : 'Rescan selected'}
        </button>
        <button class="btn btn-sm" onclick={clearSelection} disabled={rescanning}>Clear</button>
      </div>
    {/if}
    <DataTable
      tableId="pages-all"
      columns={[
        { label: '', defaultWidth: 42, minWidth: 42, resizable: false, class: 'select-col' },
        { label: t('session.url'), sortKey: 'url', defaultWidth: 380, minWidth: 180 },
        { label: t('session.status'), sortKey: 'status_code', defaultWidth: 92, minWidth: 76 },
        { label: t('session.title'), sortKey: 'title', defaultWidth: 300, minWidth: 160 },
        { label: t('session.words'), sortKey: 'word_count', defaultWidth: 92, minWidth: 74 },
        { label: t('session.intOut'), sortKey: 'internal_links_out', defaultWidth: 108, minWidth: 84 },
        { label: t('session.extOut'), sortKey: 'external_links_out', defaultWidth: 108, minWidth: 84 },
        { label: t('common.size'), sortKey: 'body_size', defaultWidth: 92, minWidth: 74 },
        { label: t('session.time'), sortKey: 'fetch_duration_ms', defaultWidth: 92, minWidth: 74 },
        { label: t('session.depth'), sortKey: 'depth', defaultWidth: 80, minWidth: 64 },
        { label: t('session.pr'), sortKey: 'pagerank', defaultWidth: 82, minWidth: 64 },
        { label: '', defaultWidth: 52, minWidth: 44, resizable: false },
      ]}
      filterKeys={[null, ...TAB_FILTERS.overview]}
      {filters}
      data={pages}
      offset={pagesOffset}
      pageSize={PAGE_SIZE}
      hasMore={hasMorePages}
      hasActiveFilters={hasActiveFilters()}
      onsetfilter={setFilter}
      onapplyfilters={applyFilters}
      onclearfilters={clearFilters}
      onnextpage={nextPage}
      onprevpage={prevPage}
      {sortColumn}
      {sortOrder}
      onsort={handleSort}
    >
      {#snippet row(p)}
        <tr>
          <td class="cell-select">
            <input
              type="checkbox"
              checked={!!selectedURLs[p.URL]}
              aria-label="Select {p.URL}"
              onchange={(e) => toggleSelected(p.URL, e.target.checked)}
              onclick={(e) => e.stopPropagation()}
            />
          </td>
          <td class="cell-url"
            ><span class="cell-url-inner"
              ><OverflowText
                text={p.URL}
                href={urlDetailHref(p.URL)}
                onclick={(e) => goToUrlDetail(e, p.URL)}
              />
              ><UrlActions url={p.URL} /></span
            ></td
          >
          <td><span class="badge {statusBadge(p.StatusCode)}">{p.StatusCode}</span></td>
          <td class="cell-title"><OverflowText text={p.Title || '-'} /></td>
          <td>{fmtN(p.WordCount)}</td>
          <td>{fmtN(p.InternalLinksOut)}</td>
          <td>{fmtN(p.ExternalLinksOut)}</td>
          <td>{fmtSize(p.BodySize)}</td>
          <td>{fmt(p.FetchDurationMs)}</td>
          <td>{p.Depth}</td>
          <td class="text-accent font-medium">{p.PageRank > 0 ? p.PageRank.toFixed(1) : '-'}</td>
          <td
            >{#if p.BodySize > 0}<button
                class="btn-html"
                title={t('session.viewHtml')}
                onclick={() => onopenhtml?.(p.URL)}
                ><svg
                  viewBox="0 0 24 24"
                  width="16"
                  height="16"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="2"
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  ><polyline points="16 18 22 12 16 6" /><polyline points="8 6 2 12 8 18" /></svg
                ></button
              >{/if}</td
          >
        </tr>
      {/snippet}
    </DataTable>
  {:else if subView === 'titles'}
    <DataTable
      tableId="pages-titles"
      columns={[
        { label: t('session.url'), sortKey: 'url' },
        { label: t('session.title'), sortKey: 'title' },
        { label: t('session.length'), sortKey: 'title_length' },
        { label: t('session.h1') },
      ]}
      filterKeys={TAB_FILTERS.titles}
      {filters}
      data={pages}
      offset={pagesOffset}
      pageSize={PAGE_SIZE}
      hasMore={hasMorePages}
      hasActiveFilters={hasActiveFilters()}
      onsetfilter={setFilter}
      onapplyfilters={applyFilters}
      onclearfilters={clearFilters}
      onnextpage={nextPage}
      onprevpage={prevPage}
      {sortColumn}
      {sortOrder}
      onsort={handleSort}
    >
      {#snippet row(p)}
        <tr>
          <td class="cell-url"
            ><span class="cell-url-inner"
              ><a href={urlDetailHref(p.URL)} onclick={(e) => goToUrlDetail(e, p.URL)}>{p.URL}</a
              ><UrlActions url={p.URL} /></span
            ></td
          >
          <td class="cell-title" class:cell-warn={p.TitleLength === 0 || p.TitleLength > 60}
            >{p.Title || '-'}</td
          >
          <td class:cell-warn={p.TitleLength === 0 || p.TitleLength > 60}>{p.TitleLength}</td>
          <td class="cell-title">{p.H1?.[0] || '-'}</td>
        </tr>
      {/snippet}
    </DataTable>
  {:else if subView === 'meta'}
    <DataTable
      tableId="pages-meta"
      columns={[
        { label: t('session.url'), sortKey: 'url' },
        { label: t('session.metaDescription'), sortKey: 'meta_description' },
        { label: t('session.length'), sortKey: 'meta_desc_length' },
        { label: t('session.metaKeywords'), sortKey: 'meta_keywords' },
        { label: t('session.ogTitle'), sortKey: 'og_title' },
      ]}
      filterKeys={TAB_FILTERS.meta}
      {filters}
      data={pages}
      offset={pagesOffset}
      pageSize={PAGE_SIZE}
      hasMore={hasMorePages}
      hasActiveFilters={hasActiveFilters()}
      onsetfilter={setFilter}
      onapplyfilters={applyFilters}
      onclearfilters={clearFilters}
      onnextpage={nextPage}
      onprevpage={prevPage}
      {sortColumn}
      {sortOrder}
      onsort={handleSort}
    >
      {#snippet row(p)}
        <tr>
          <td class="cell-url"
            ><span class="cell-url-inner"
              ><a href={urlDetailHref(p.URL)} onclick={(e) => goToUrlDetail(e, p.URL)}>{p.URL}</a
              ><UrlActions url={p.URL} /></span
            ></td
          >
          <td class="cell-title" class:cell-warn={p.MetaDescLength === 0 || p.MetaDescLength > 160}
            >{trunc(p.MetaDescription, 80)}</td
          >
          <td class:cell-warn={p.MetaDescLength === 0 || p.MetaDescLength > 160}
            >{p.MetaDescLength}</td
          >
          <td class="cell-title">{trunc(p.MetaKeywords, 60)}</td>
          <td class="cell-title">{trunc(p.OGTitle, 60)}</td>
        </tr>
      {/snippet}
    </DataTable>
  {:else if subView === 'headings'}
    <DataTable
      tableId="pages-headings"
      columns={[
        { label: t('session.url'), sortKey: 'url' },
        { label: t('session.h1') },
        { label: t('session.h1Count') },
        { label: t('session.h2') },
        { label: t('session.h2Count') },
      ]}
      filterKeys={TAB_FILTERS.headings}
      {filters}
      data={pages}
      offset={pagesOffset}
      pageSize={PAGE_SIZE}
      hasMore={hasMorePages}
      hasActiveFilters={hasActiveFilters()}
      onsetfilter={setFilter}
      onapplyfilters={applyFilters}
      onclearfilters={clearFilters}
      onnextpage={nextPage}
      onprevpage={prevPage}
      {sortColumn}
      {sortOrder}
      onsort={handleSort}
    >
      {#snippet row(p)}
        <tr>
          <td class="cell-url"
            ><span class="cell-url-inner"
              ><a href={urlDetailHref(p.URL)} onclick={(e) => goToUrlDetail(e, p.URL)}>{p.URL}</a
              ><UrlActions url={p.URL} /></span
            ></td
          >
          <td class="cell-title" class:cell-warn={!p.H1?.length || p.H1.length > 1}
            >{p.H1?.[0] || '-'}</td
          >
          <td class:cell-warn={!p.H1?.length || p.H1.length > 1}>{p.H1?.length || 0}</td>
          <td class="cell-title">{p.H2?.[0] || '-'}</td>
          <td>{p.H2?.length || 0}</td>
        </tr>
      {/snippet}
    </DataTable>
  {:else if subView === 'images'}
    <DataTable
      tableId="pages-images"
      columns={[
        { label: t('session.url'), sortKey: 'url' },
        { label: t('session.images'), sortKey: 'images_count' },
        { label: t('session.withoutAlt'), sortKey: 'images_no_alt' },
        { label: t('session.title'), sortKey: 'title' },
        { label: t('session.words'), sortKey: 'word_count' },
      ]}
      filterKeys={TAB_FILTERS.images}
      {filters}
      data={pages}
      offset={pagesOffset}
      pageSize={PAGE_SIZE}
      hasMore={hasMorePages}
      hasActiveFilters={hasActiveFilters()}
      onsetfilter={setFilter}
      onapplyfilters={applyFilters}
      onclearfilters={clearFilters}
      onnextpage={nextPage}
      onprevpage={prevPage}
      {sortColumn}
      {sortOrder}
      onsort={handleSort}
    >
      {#snippet row(p)}
        <tr>
          <td class="cell-url"
            ><span class="cell-url-inner"
              ><a href={urlDetailHref(p.URL)} onclick={(e) => goToUrlDetail(e, p.URL)}>{p.URL}</a
              ><UrlActions url={p.URL} /></span
            ></td
          >
          <td>{p.ImagesCount}</td>
          <td class:cell-warn={p.ImagesNoAlt > 0}>{p.ImagesNoAlt}</td>
          <td class="cell-title">{trunc(p.Title, 50)}</td>
          <td>{fmtN(p.WordCount)}</td>
        </tr>
      {/snippet}
    </DataTable>
  {:else if subView === 'issues'}
    <DataTable
      tableId="pages-issues"
      columns={[
        { label: t('issues.severity'), defaultWidth: 100, minWidth: 86 },
        { label: t('issues.issueType'), defaultWidth: 210, minWidth: 150 },
        { label: t('session.url'), defaultWidth: 360, minWidth: 180 },
        { label: t('issues.detail'), defaultWidth: 300, minWidth: 180 },
        { label: t('session.status'), defaultWidth: 86, minWidth: 72 },
        { label: t('session.title'), defaultWidth: 260, minWidth: 150 },
        { label: t('issues.renderedTitle'), defaultWidth: 260, minWidth: 150 },
        { label: t('issues.renderedH1'), defaultWidth: 180, minWidth: 120 },
        { label: t('issues.renderedWords'), defaultWidth: 110, minWidth: 90 },
        { label: t('issues.renderedImages'), defaultWidth: 120, minWidth: 96 },
      ]}
      filterKeys={TAB_FILTERS.issues}
      {filters}
      data={pages}
      offset={pagesOffset}
      pageSize={PAGE_SIZE}
      hasMore={hasMorePages}
      hasActiveFilters={hasActiveFilters()}
      onsetfilter={setFilter}
      onapplyfilters={applyFilters}
      onclearfilters={clearFilters}
      onnextpage={nextPage}
      onprevpage={prevPage}
      {sortColumn}
      {sortOrder}
      onsort={handleSort}
    >
      {#snippet row(issue)}
        <tr>
          <td>
            <span
              class="badge"
              class:badge-error={issue.severity === 'error'}
              class:badge-warning={issue.severity === 'warning'}
              >{issueSeverityLabel(issue.severity)}</span
            >
          </td>
          <td><span class="issue-type">{issueTypeLabel(issue.issue_type)}</span></td>
          <td class="cell-url"
            ><span class="cell-url-inner"
              ><OverflowText
                text={issue.url}
                href={urlDetailHref(issue.url)}
                onclick={(e) => goToUrlDetail(e, issue.url)}
              />
              ><UrlActions url={issue.url} /></span
            ></td
          >
          <td class="cell-title"><OverflowText text={issue.issue_detail || '-'} /></td>
          <td><span class="badge {statusBadge(issue.status_code)}">{issue.status_code}</span></td>
          <td class="cell-title"><OverflowText text={issue.title || '-'} /></td>
          <td class="cell-title"><OverflowText text={issue.rendered_title || '-'} /></td>
          <td class="cell-title"><OverflowText text={issue.rendered_h1?.join(' | ') || '-'} /></td>
          <td>{fmtN(issue.rendered_word_count)}</td>
          <td>{fmtN(issue.rendered_images_count)}</td>
        </tr>
      {/snippet}
    </DataTable>
  {:else if subView === 'indexability'}
    <DataTable
      tableId="pages-indexability"
      columns={[
        { label: t('session.url'), sortKey: 'url' },
        { label: t('session.indexable'), sortKey: 'is_indexable' },
        { label: t('session.reason'), sortKey: 'index_reason' },
        { label: t('session.metaRobots'), sortKey: 'meta_robots' },
        { label: t('session.canonical'), sortKey: 'canonical' },
        { label: t('session.self'), sortKey: 'canonical_is_self' },
      ]}
      filterKeys={TAB_FILTERS.indexability}
      {filters}
      data={pages}
      offset={pagesOffset}
      pageSize={PAGE_SIZE}
      hasMore={hasMorePages}
      hasActiveFilters={hasActiveFilters()}
      onsetfilter={setFilter}
      onapplyfilters={applyFilters}
      onclearfilters={clearFilters}
      onnextpage={nextPage}
      onprevpage={prevPage}
      {sortColumn}
      {sortOrder}
      onsort={handleSort}
    >
      {#snippet row(p)}
        <tr>
          <td class="cell-url"
            ><span class="cell-url-inner"
              ><a href={urlDetailHref(p.URL)} onclick={(e) => goToUrlDetail(e, p.URL)}>{p.URL}</a
              ><UrlActions url={p.URL} /></span
            ></td
          >
          <td
            ><span
              class="badge"
              class:badge-success={p.IsIndexable}
              class:badge-error={!p.IsIndexable}
              >{p.IsIndexable ? t('common.yes') : t('common.no')}</span
            ></td
          >
          <td>{p.IndexReason || '-'}</td>
          <td>{p.MetaRobots || '-'}</td>
          <td class="cell-url">{trunc(p.Canonical, 60)}</td>
          <td>{p.CanonicalIsSelf ? t('common.yes') : '-'}</td>
        </tr>
      {/snippet}
    </DataTable>
  {:else if subView === 'response'}
    <DataTable
      tableId="pages-response"
      columns={[
        { label: t('session.url'), sortKey: 'url' },
        { label: t('session.status'), sortKey: 'status_code' },
        { label: t('session.contentType'), sortKey: 'content_type' },
        { label: t('session.encoding'), sortKey: 'content_encoding' },
        { label: t('common.size'), sortKey: 'body_size' },
        { label: t('session.time'), sortKey: 'fetch_duration_ms' },
        { label: t('session.redirects') },
      ]}
      filterKeys={TAB_FILTERS.response}
      {filters}
      data={pages}
      offset={pagesOffset}
      pageSize={PAGE_SIZE}
      hasMore={hasMorePages}
      hasActiveFilters={hasActiveFilters()}
      onsetfilter={setFilter}
      onapplyfilters={applyFilters}
      onclearfilters={clearFilters}
      onnextpage={nextPage}
      onprevpage={prevPage}
      {sortColumn}
      {sortOrder}
      onsort={handleSort}
    >
      {#snippet row(p)}
        <tr>
          <td class="cell-url"
            ><span class="cell-url-inner"
              ><a href={urlDetailHref(p.URL)} onclick={(e) => goToUrlDetail(e, p.URL)}>{p.URL}</a
              ><UrlActions url={p.URL} /></span
            ></td
          >
          <td><span class="badge {statusBadge(p.StatusCode)}">{p.StatusCode}</span></td>
          <td>{p.ContentType || '-'}</td>
          <td>{p.ContentEncoding || '-'}</td>
          <td>{fmtSize(p.BodySize)}</td>
          <td>{fmt(p.FetchDurationMs)}</td>
          <td>{p.FinalURL !== p.URL ? p.FinalURL : '-'}</td>
        </tr>
      {/snippet}
    </DataTable>
  {:else if subView === 'redirects'}
    <DataTable
      tableId="pages-redirects"
      columns={[
        { label: t('session.url'), sortKey: 'url' },
        { label: t('session.status'), sortKey: 'status_code' },
        { label: t('session.finalUrl'), sortKey: 'final_url' },
        { label: t('session.inboundLinks'), sortKey: 'inbound_internal_links' },
      ]}
      filterKeys={TAB_FILTERS.redirects}
      {filters}
      data={redirectPages}
      offset={redirectPagesOffset}
      pageSize={PAGE_SIZE}
      hasMore={hasMoreRedirectPages}
      hasActiveFilters={hasActiveFilters()}
      onsetfilter={setFilter}
      onapplyfilters={applyFilters}
      onclearfilters={clearFilters}
      onnextpage={nextPage}
      onprevpage={prevPage}
      {sortColumn}
      {sortOrder}
      onsort={handleSort}
    >
      {#snippet row(p)}
        <tr>
          <td class="cell-url"
            ><span class="cell-url-inner"
              ><a href={urlDetailHref(p.url)} onclick={(e) => goToUrlDetail(e, p.url)}>{p.url}</a
              ><UrlActions url={p.url} /></span
            ></td
          >
          <td><span class="badge {statusBadge(p.status_code)}">{p.status_code}</span></td>
          <td class="cell-url">{p.final_url || '-'}</td>
          <td class:cell-warn={p.inbound_internal_links > 0}>{p.inbound_internal_links}</td>
        </tr>
      {/snippet}
    </DataTable>
  {:else if subView === 'duplicates'}
    <NearDuplicatesTab
      {sessionId}
      onerror={(msg) => onerror?.(msg)}
      onnavigate={(url) => onnavigate?.(url)}
    />
  {:else if subView === 'hreflang'}
    <HreflangValidationTab
      {sessionId}
      onerror={(msg) => onerror?.(msg)}
      onnavigate={(url) => onnavigate?.(url)}
    />
  {:else if DELEGATED_VIEWS.has(subView) && subView !== 'duplicates' && subView !== 'hreflang'}
    {#key subView}
      <URLPatternsTab
        {sessionId}
        initialSubView={subView}
        onpushurl={(u) => onpushurl?.(u)}
        onerror={(msg) => onerror?.(msg)}
        embedded={true}
      />
    {/key}
  {/if}
</div>

<style>
  .pages-explorer {
    padding: 24px;
  }
  .pr-subview-sep {
    width: 1px;
    background: var(--border);
    align-self: stretch;
    margin: 4px 4px;
  }

  .bulk-action-bar {
    display: flex;
    align-items: center;
    gap: 10px;
    min-height: 44px;
    margin: 0 0 10px;
    padding: 8px 10px;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--bg-card);
    color: var(--text-muted);
    font-size: 13px;
  }

  .cell-select {
    text-align: center;
    padding-left: 10px;
    padding-right: 8px;
  }

  .cell-select input {
    width: 15px;
    height: 15px;
    accent-color: var(--accent);
  }
</style>
