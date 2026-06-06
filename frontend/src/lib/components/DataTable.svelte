<script>
  import { onMount } from 'svelte';
  import { t } from '../i18n/index.svelte.js';

  let {
    tableId = '',
    columns,
    filterKeys,
    filters,
    data,
    offset,
    pageSize,
    hasMore,
    onsetfilter,
    onapplyfilters,
    onclearfilters,
    onnextpage,
    onprevpage,
    hasActiveFilters,
    row,
    extraHeaderCols = 0,
    sortColumn = '',
    sortOrder = '',
    onsort,
  } = $props();

  let columnWidths = $state([]);

  function storageKey() {
    return tableId ? `crawlobserver:table-widths:${tableId}` : '';
  }

  function defaultWidth(col) {
    return col.defaultWidth || col.width || null;
  }

  function loadColumnWidths() {
    let stored = [];
    const key = storageKey();
    if (key && typeof localStorage !== 'undefined') {
      try {
        stored = JSON.parse(localStorage.getItem(key) || '[]');
      } catch {
        stored = [];
      }
    }
    columnWidths = columns.map((col, idx) => stored[idx] || defaultWidth(col));
  }

  function persistColumnWidths(next = columnWidths) {
    const key = storageKey();
    if (!key || typeof localStorage === 'undefined') return;
    localStorage.setItem(key, JSON.stringify(next));
  }

  function columnStyle(idx) {
    const width = columnWidths[idx] || defaultWidth(columns[idx]);
    return width ? `width:${width}px;min-width:${columns[idx].minWidth || 56}px;` : '';
  }

  function headerStyle(col, idx) {
    return `${columnStyle(idx)}${col.headerStyle || ''}`;
  }

  function startResize(e, idx) {
    if (columns[idx]?.resizable === false) return;
    e.preventDefault();
    e.stopPropagation();

    const th = e.currentTarget.closest('th');
    const startX = e.clientX;
    const startWidth = columnWidths[idx] || th?.offsetWidth || columns[idx]?.defaultWidth || 120;
    const minWidth = columns[idx]?.minWidth || 56;

    function onMove(moveEvent) {
      const nextWidth = Math.max(minWidth, Math.round(startWidth + moveEvent.clientX - startX));
      const next = [...columnWidths];
      next[idx] = nextWidth;
      columnWidths = next;
    }

    function onUp() {
      persistColumnWidths();
      window.removeEventListener('mousemove', onMove);
      window.removeEventListener('mouseup', onUp);
      document.body.classList.remove('is-column-resizing');
    }

    document.body.classList.add('is-column-resizing');
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
  }

  function resetColumnWidth(e, idx) {
    e.preventDefault();
    e.stopPropagation();
    const next = [...columnWidths];
    next[idx] = defaultWidth(columns[idx]);
    columnWidths = next;
    persistColumnWidths(next);
  }

  function handleSort(sortKey) {
    if (!sortKey || !onsort) return;
    if (sortColumn !== sortKey) {
      onsort(sortKey, 'asc');
    } else if (sortOrder === 'asc') {
      onsort(sortKey, 'desc');
    } else {
      onsort('', '');
    }
  }

  onMount(loadColumnWidths);
</script>

<div class="data-table-wrap">
<table class="data-table-resizable">
  <colgroup>
    {#each columns as col, idx}
      <col style={columnStyle(idx)} />
    {/each}
  </colgroup>
  <thead>
    <tr>
      {#each columns as col, idx}
        {#if col.sortKey && onsort}
          <th
            style={headerStyle(col, idx)}
            class="{col.class || ''} sortable"
            onclick={() => handleSort(col.sortKey)}
          >
            <span class="sort-header">
              {col.label}
              <span class="sort-indicator" class:sort-active={sortColumn === col.sortKey}>
                {#if sortColumn === col.sortKey && sortOrder === 'asc'}
                  <svg
                    viewBox="0 0 24 24"
                    width="14"
                    height="14"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="2"><path d="M12 19V5m-7 7l7-7 7 7" /></svg
                  >
                {:else if sortColumn === col.sortKey && sortOrder === 'desc'}
                  <svg
                    viewBox="0 0 24 24"
                    width="14"
                    height="14"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="2"><path d="M12 5v14m7-7l-7 7-7-7" /></svg
                  >
                {:else}
                  <svg
                    viewBox="0 0 24 24"
                    width="14"
                    height="14"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="2"
                    opacity="0.3"><path d="M12 5v14m7-7l-7 7-7-7" /></svg
                  >
                {/if}
              </span>
            </span>
            <button
              type="button"
              class="column-resize-handle"
              aria-label="Resize column"
              title="Drag to resize. Double-click to reset."
              onmousedown={(e) => startResize(e, idx)}
              ondblclick={(e) => resetColumnWidth(e, idx)}
            ></button>
          </th>
        {:else}
          <th style={headerStyle(col, idx)} class={col.class || ''}>
            {col.label}
            <button
              type="button"
              class="column-resize-handle"
              aria-label="Resize column"
              title="Drag to resize. Double-click to reset."
              onmousedown={(e) => startResize(e, idx)}
              ondblclick={(e) => resetColumnWidth(e, idx)}
            ></button>
          </th>
        {/if}
      {/each}
    </tr>
    <tr class="filter-row">
      {#each filterKeys as key, idx}
        {#if key}
          <td
            ><input
              class="filter-input"
              placeholder={key}
              value={filters[key] || ''}
              oninput={(e) => onsetfilter?.(key, e.target.value)}
              onkeydown={(e) => e.key === 'Enter' && onapplyfilters?.()}
            /></td
          >
        {:else}
          <td></td>
        {/if}
      {/each}
      {#if columns.length > filterKeys.length}
        {#each Array(columns.length - filterKeys.length) as _, idx}
          <td
            >{#if hasActiveFilters && idx === 0}<button
                class="btn-filter-clear"
                title={t('dataTable.clearFilters')}
                onclick={() => onclearfilters?.()}
                ><svg
                  viewBox="0 0 24 24"
                  width="14"
                  height="14"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="2"
                  ><line x1="18" y1="6" x2="6" y2="18" /><line x1="6" y1="6" x2="18" y2="18" /></svg
                ></button
              >{/if}</td
          >
        {/each}
      {/if}
    </tr>
  </thead>
  <tbody>
    {#each data as item}
      {@render row(item)}
    {/each}
  </tbody>
</table>
</div>

{#if data.length > 0}
  <div class="pagination">
    <button class="btn btn-sm" onclick={() => onprevpage?.()} disabled={offset === 0}
      >{t('common.previous')}</button
    >
    <span class="pagination-info">{offset + 1} - {offset + data.length}</span>
    <button class="btn btn-sm" onclick={() => onnextpage?.()} disabled={!hasMore}
      >{t('common.next')}</button
    >
  </div>
{/if}

<style>
  .data-table-wrap {
    overflow-x: auto;
    width: 100%;
  }

  .data-table-resizable {
    table-layout: fixed;
    min-width: 100%;
  }

  th {
    position: sticky;
  }

  th,
  td {
    overflow: hidden;
  }

  th.sortable {
    cursor: pointer;
    user-select: none;
  }
  th.sortable:hover {
    background: var(--hover-bg, rgba(255, 255, 255, 0.05));
  }
  .sort-header {
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }
  .sort-indicator {
    display: inline-flex;
    flex-shrink: 0;
  }
  .sort-indicator.sort-active svg {
    opacity: 1;
  }

  .column-resize-handle {
    position: absolute;
    top: 0;
    right: -4px;
    width: 8px;
    height: 100%;
    padding: 0;
    border: 0;
    background: transparent;
    cursor: col-resize;
    z-index: 3;
  }

  .column-resize-handle::after {
    content: '';
    position: absolute;
    top: 20%;
    right: 3px;
    width: 1px;
    height: 60%;
    background: transparent;
  }

  th:hover .column-resize-handle::after {
    background: var(--border);
  }

  :global(.is-column-resizing) {
    cursor: col-resize;
    user-select: none;
  }
</style>
