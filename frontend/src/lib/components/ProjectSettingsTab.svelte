<script>
  import { onDestroy, onMount } from 'svelte';
  import {
    cleanupProjectOrphan404s,
    getProjectOrphan404CleanupPreview,
    getProjectDeltaSettings,
    getProjectPageRankRecomputeStatus,
    updateProjectDeltaSettings,
  } from '../api.js';

  let { projectId, isAdmin, onerror } = $props();

  let settings = $state(null);
  let loading = $state(false);
  let saving = $state(false);
  let message = $state('');
  let messageTone = $state('muted');
  let cleanupPreview = $state(null);
  let loadingCleanupPreview = $state(false);
  let runningCleanup = $state(false);
  let loadedPageRankSettings = $state('');
  let recomputePollTimer = null;

  function pageRankSettingsSignature(value) {
    return JSON.stringify({
      include_footer_links_in_pagerank: !!value?.include_footer_links_in_pagerank,
      footer_selector_patterns: value?.footer_selector_patterns || [],
    });
  }

  async function load() {
    loading = true;
    try {
      settings = await getProjectDeltaSettings(projectId);
      loadedPageRankSettings = pageRankSettingsSignature(settings);
      pollPageRankRecomputeStatus(false);
    } catch (e) {
      onerror?.(e.message);
    } finally {
      loading = false;
    }
  }

  function listValue(value) {
    return (value || []).join('\n');
  }

  function updateListField(key, value) {
    settings[key] = value
      .split(/\n+/)
      .map((v) => v.trim())
      .filter(Boolean);
  }

  async function save() {
    if (!isAdmin || !settings) return;
    saving = true;
    message = '';
    messageTone = 'muted';
    try {
      const pageRankChanged = loadedPageRankSettings !== pageRankSettingsSignature(settings);
      settings = await updateProjectDeltaSettings(projectId, settings);
      loadedPageRankSettings = pageRankSettingsSignature(settings);
      if (pageRankChanged) {
        message = 'Project settings saved. Internal PageRank recalculation started...';
        messageTone = 'running';
        pollPageRankRecomputeStatus(true);
      } else {
        message = 'Project crawl settings saved';
        messageTone = 'success';
      }
    } catch (e) {
      onerror?.(e.message);
    } finally {
      saving = false;
    }
  }

  async function previewOrphan404Cleanup() {
    if (!isAdmin) return;
    loadingCleanupPreview = true;
    try {
      cleanupPreview = await getProjectOrphan404CleanupPreview(projectId);
      message = `Found ${cleanupPreview?.count || 0} orphan 404 cleanup candidate(s).`;
      messageTone = cleanupPreview?.count > 0 ? 'running' : 'success';
    } catch (e) {
      onerror?.(e.message);
    } finally {
      loadingCleanupPreview = false;
    }
  }

  async function runOrphan404Cleanup() {
    if (!isAdmin || runningCleanup || !cleanupPreview?.count) return;
    const ok = window.confirm(
      `Delete ${cleanupPreview.count} orphan 404 page(s) from the current snapshot?\n\nOnly URLs with status 404, zero internal inlinks, no sitemap match, and older than ${cleanupPreview.older_than_days} day(s) are eligible. Raw crawl sessions are not deleted.`,
    );
    if (!ok) return;

    runningCleanup = true;
    try {
      const result = await cleanupProjectOrphan404s(projectId);
      cleanupPreview = null;
      message = `Deleted ${result.deleted || 0} orphan 404 page(s). Internal PageRank recalculation ${result.pagerank_recalculation_started ? 'started' : 'was not needed'}.`;
      messageTone = 'success';
      if (result.pagerank_recalculation_started) pollPageRankRecomputeStatus(true);
    } catch (e) {
      onerror?.(e.message);
    } finally {
      runningCleanup = false;
    }
  }

  function clearRecomputePolling() {
    if (recomputePollTimer) {
      clearTimeout(recomputePollTimer);
      recomputePollTimer = null;
    }
  }

  function formatStatusTime(value) {
    if (!value) return '';
    try {
      return new Date(value).toLocaleTimeString([], {
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
      });
    } catch {
      return '';
    }
  }

  async function pollPageRankRecomputeStatus(forceMessage = false) {
    clearRecomputePolling();
    try {
      const status = await getProjectPageRankRecomputeStatus(projectId);
      if (status?.status === 'running') {
        const startedAt = formatStatusTime(status.started_at);
        message = `Internal PageRank recalculation is running${startedAt ? ` since ${startedAt}` : ''}...`;
        messageTone = 'running';
        recomputePollTimer = setTimeout(() => pollPageRankRecomputeStatus(true), 1500);
      } else if (status?.status === 'completed' && forceMessage) {
        const finishedAt = formatStatusTime(status.finished_at);
        message = `Internal PageRank recalculation completed${finishedAt ? ` at ${finishedAt}` : ''}.`;
        messageTone = 'success';
      } else if (status?.status === 'failed' && forceMessage) {
        message = `Internal PageRank recalculation failed${status.error ? `: ${status.error}` : '.'}`;
        messageTone = 'error';
      }
    } catch (e) {
      if (forceMessage) {
        message = `Could not check Internal PageRank recalculation status: ${e.message}`;
        messageTone = 'error';
      }
    }
  }

  onMount(load);
  onDestroy(clearRecomputePolling);
</script>

{#if loading}
  <div class="empty-state"><p>Loading project settings...</p></div>
{:else if settings}
  <div class="project-settings-shell">
    <section class="project-settings-panel">
      <div class="settings-copy">
        <p class="eyebrow">Crawl Rules</p>
        <h3>URL exclusions</h3>
        <p>
          Excluded patterns are matched as URL substrings. They apply to Daily Delta and to new full
          crawls for this project. Existing crawl data is not rewritten.
        </p>
      </div>
      <label class="settings-field wide">
        <span>Exclude URL patterns</span>
        <textarea
          class="settings-textarea"
          placeholder="/cdn-cgi/&#10;/private/&#10;?preview="
          value={listValue(settings.blocked_url_patterns)}
          oninput={(e) => updateListField('blocked_url_patterns', e.target.value)}
          disabled={!isAdmin}
        ></textarea>
      </label>
    </section>

    <section class="project-settings-panel">
      <div class="settings-copy">
        <p class="eyebrow">Cleanup</p>
        <h3>Orphan 404 retention</h3>
        <p>
          Removes stale 404 URLs from the current snapshot only when they have no internal inlinks
          and are not present in the sitemap. Historical crawl sessions are preserved.
        </p>
      </div>
      <div class="settings-stack">
        <label class="settings-field">
          <span>Orphan 404 cleanup after days</span>
          <input
            type="number"
            min="1"
            bind:value={settings.orphan_404_cleanup_days}
            disabled={!isAdmin}
          />
        </label>
        <div class="cleanup-actions">
          <button
            class="btn btn-sm"
            onclick={previewOrphan404Cleanup}
            disabled={!isAdmin || loadingCleanupPreview || runningCleanup}
          >
            {loadingCleanupPreview ? 'Checking...' : 'Preview cleanup'}
          </button>
          <button
            class="btn btn-sm btn-danger"
            onclick={runOrphan404Cleanup}
            disabled={!isAdmin || runningCleanup || !cleanupPreview?.count}
          >
            {runningCleanup ? 'Cleaning...' : 'Delete orphan 404s'}
          </button>
        </div>
        {#if cleanupPreview}
          <div class="cleanup-preview">
            <strong>{cleanupPreview.count}</strong> eligible URL(s) older than {cleanupPreview.older_than_days} day(s).
            {#if cleanupPreview.candidates?.length}
              <ul>
                {#each cleanupPreview.candidates.slice(0, 5) as item}
                  <li title={item.url}>{item.url}</li>
                {/each}
              </ul>
            {/if}
          </div>
        {/if}
      </div>
    </section>

    <section class="project-settings-panel">
      <div class="settings-copy">
        <p class="eyebrow">Daily Delta</p>
        <h3>Sitemap refresh failure</h3>
        <p>
          Daily Delta normally uses the sitemap fetched immediately before the run. This setting
          controls only what happens when that refresh is incomplete or unavailable.
        </p>
      </div>
      <div class="settings-stack">
        <label class="settings-field">
          <span>When sitemap refresh fails</span>
          <select bind:value={settings.sitemap_refresh_failure_mode} disabled={!isAdmin}>
            <option value="skip">Skip sitemap candidates</option>
            <option value="snapshot_fallback">Use snapshot fallback (not fresh)</option>
          </select>
        </label>
        <p class="settings-hint">
          Snapshot fallback uses the previous published sitemap only. It is marked as non-fresh and
          never replaces current sitemap membership.
        </p>
      </div>
    </section>

    <section class="project-settings-panel">
      <div class="settings-copy">
        <p class="eyebrow">Internal PageRank</p>
        <h3>Graph options</h3>
        <p>
          These options are used when PageRank is calculated for new crawls, current snapshots, and
          manual recalculation.
        </p>
      </div>
      <div class="settings-stack">
        <label class="toggle-row">
          <input
            type="checkbox"
            bind:checked={settings.include_footer_links_in_pagerank}
            disabled={!isAdmin}
          />
          <span>Include footer links in internal PageRank</span>
        </label>
        <label class="settings-field wide">
          <span>Footer selectors</span>
          <textarea
            class="settings-textarea compact"
            placeholder="footer&#10;.site-footer&#10;[data-section=&quot;footer&quot;]"
            value={listValue(settings.footer_selector_patterns)}
            oninput={(e) => updateListField('footer_selector_patterns', e.target.value)}
            disabled={!isAdmin}
          ></textarea>
        </label>
      </div>
    </section>

    <div class="settings-actions">
      {#if message}<span class="settings-message" class:running={messageTone === 'running'} class:success={messageTone === 'success'} class:error={messageTone === 'error'}>{message}</span>{/if}
      <button class="btn btn-primary" onclick={save} disabled={!isAdmin || saving}>
        {saving ? 'Saving...' : 'Save project settings'}
      </button>
    </div>
  </div>
{/if}

<style>
  .project-settings-shell {
    display: grid;
    gap: 16px;
  }

  .project-settings-panel {
    display: grid;
    grid-template-columns: minmax(220px, 0.35fr) minmax(360px, 1fr);
    gap: 24px;
    padding: 20px;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--bg-card);
  }

  .settings-copy h3 {
    margin: 4px 0 8px;
    font-size: 18px;
  }

  .settings-copy p {
    color: var(--text-muted);
    font-size: 14px;
    max-width: 420px;
  }

  .eyebrow {
    color: var(--accent);
    font-size: 12px;
    font-weight: 700;
    letter-spacing: 0.04em;
    text-transform: uppercase;
  }

  .settings-field {
    display: grid;
    gap: 8px;
    color: var(--text-secondary);
    font-size: 13px;
    font-weight: 600;
  }

  .settings-textarea {
    min-height: 144px;
    padding: 12px;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--bg-input);
    color: var(--text);
    font: inherit;
    font-weight: 500;
    line-height: 1.5;
    resize: vertical;
  }

  .settings-textarea.compact {
    min-height: 104px;
  }

  .settings-stack {
    display: grid;
    gap: 16px;
  }

  .settings-hint {
    margin: 0;
    color: var(--text-muted);
    font-size: 13px;
    line-height: 1.5;
  }

  .cleanup-actions {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }

  .cleanup-preview {
    padding: 12px;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--bg-secondary);
    color: var(--text-secondary);
    font-size: 13px;
  }

  .cleanup-preview ul {
    display: grid;
    gap: 4px;
    margin: 8px 0 0;
    padding: 0;
    list-style: none;
  }

  .cleanup-preview li {
    overflow: hidden;
    color: var(--text-muted);
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .toggle-row {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 12px;
    border: 1px solid var(--border);
    border-radius: 8px;
    color: var(--text-secondary);
    font-weight: 600;
  }

  .settings-actions {
    display: flex;
    justify-content: flex-end;
    align-items: center;
    gap: 12px;
  }

  .settings-message {
    color: var(--text-muted);
    font-size: 13px;
    font-weight: 600;
  }

  .settings-message.running {
    color: var(--accent);
  }

  .settings-message.success {
    color: var(--success);
  }

  .settings-message.error {
    color: var(--danger);
  }

  @media (max-width: 900px) {
    .project-settings-panel {
      grid-template-columns: 1fr;
    }
  }
</style>
