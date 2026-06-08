<script>
  import { onMount } from 'svelte';
  import {
    addProjectDeltaManualURLs,
    getProjectDeltaPreview,
    getProjectDeltaSettings,
    runProjectDelta,
    updateProjectDeltaSettings,
  } from '../api.js';

  let { projectId, isAdmin, onerror, onselectsession } = $props();

  let settings = $state(null);
  let preview = $state(null);
  let loading = $state(false);
  let saving = $state(false);
  let running = $state(false);
  let manualURLs = $state('');
  let message = $state('');

  async function load() {
    loading = true;
    try {
      settings = await getProjectDeltaSettings(projectId);
      await loadPreview();
    } catch (e) {
      onerror?.(e.message);
    } finally {
      loading = false;
    }
  }

  async function loadPreview() {
    try {
      preview = await getProjectDeltaPreview(projectId);
    } catch (e) {
      preview = null;
      if (!e.message.includes('no baseline session')) onerror?.(e.message);
    }
  }

  async function saveSettings() {
    if (!isAdmin || !settings) return;
    saving = true;
    message = '';
    try {
      settings = await updateProjectDeltaSettings(projectId, settings);
      await loadPreview();
      message = 'Settings saved';
    } catch (e) {
      onerror?.(e.message);
    } finally {
      saving = false;
    }
  }

  async function queueManualURLs() {
    if (!isAdmin) return;
    const urls = manualURLs
      .split(/\n+/)
      .map((u) => u.trim())
      .filter(Boolean);
    if (urls.length === 0) return;
    try {
      const res = await addProjectDeltaManualURLs(projectId, urls);
      manualURLs = '';
      await loadPreview();
      message = `Queued ${res.added || 0} URLs`;
    } catch (e) {
      onerror?.(e.message);
    }
  }

  async function runNow() {
    if (!isAdmin) return;
    running = true;
    message = '';
    try {
      const res = await runProjectDelta(projectId);
      await loadPreview();
      message = `Delta crawl started`;
      if (res?.session_id) {
        onselectsession?.({ ID: res.session_id, ProjectID: projectId });
      }
    } catch (e) {
      onerror?.(e.message);
    } finally {
      running = false;
    }
  }

  function updateListField(key, value) {
    settings[key] = value
      .split(/\n+/)
      .map((v) => v.trim())
      .filter(Boolean);
  }

  function listValue(value) {
    return (value || []).join('\n');
  }

  onMount(load);
</script>

{#if loading || !settings}
  <div class="delta-empty">Loading...</div>
{:else}
  <div class="delta-layout">
    <section class="delta-main">
      <div class="delta-header">
        <div>
          <h3>Daily Delta Crawl</h3>
          <span class="text-muted text-sm">Baseline: {preview?.baseline_session_id || '-'}</span>
        </div>
        {#if isAdmin}
          <div class="delta-actions">
            <button class="btn btn-sm" onclick={loadPreview}>Preview</button>
            <button class="btn btn-primary btn-sm" onclick={runNow} disabled={running || !preview?.will_launch}>
              {running ? 'Starting...' : 'Run now'}
            </button>
            <button class="btn btn-sm" onclick={saveSettings} disabled={saving}>
              {saving ? 'Saving...' : 'Save'}
            </button>
          </div>
        {/if}
      </div>

      {#if message}
        <div class="delta-message">{message}</div>
      {/if}

      <div class="delta-summary">
        <div><strong>{preview?.total_candidates || 0}</strong><span>Candidates</span></div>
        <div><strong>{preview?.will_launch || 0}</strong><span>Will crawl</span></div>
        <div><strong>{preview?.deferred || 0}</strong><span>Deferred</span></div>
        <div><strong>{preview?.launch_limit || 0}</strong><span>Run limit</span></div>
      </div>

      <div class="settings-grid">
        <label class="check-row">
          <input type="checkbox" bind:checked={settings.enabled} disabled={!isAdmin} />
          <span>Enabled</span>
        </label>
        <label>
          <span>Schedule time</span>
          <input type="time" bind:value={settings.schedule_time} disabled={!isAdmin} />
        </label>
        <label>
          <span>Timezone</span>
          <input type="text" bind:value={settings.timezone} disabled={!isAdmin} />
        </label>
        <label>
          <span>Stale after days</span>
          <input type="number" min="1" bind:value={settings.stale_after_days} disabled={!isAdmin} />
        </label>
      </div>

      <h4>Candidate Sources</h4>
      <div class="checks-grid">
        <label><input type="checkbox" bind:checked={settings.source_sitemap} disabled={!isAdmin} /> Sitemap</label>
        <label><input type="checkbox" bind:checked={settings.source_gsc} disabled={!isAdmin} /> GSC</label>
        <label><input type="checkbox" bind:checked={settings.source_problem_pages} disabled={!isAdmin} /> Problem pages</label>
        <label><input type="checkbox" bind:checked={settings.source_stale_pages} disabled={!isAdmin} /> Stale pages</label>
        <label><input type="checkbox" bind:checked={settings.source_manual_queue} disabled={!isAdmin} /> Manual queue</label>
      </div>

      <h4>Limits</h4>
      <div class="settings-grid">
        <label><span>Max candidates</span><input type="number" min="1" bind:value={settings.max_candidates_per_run} disabled={!isAdmin} /></label>
        <label><span>Max changed pages</span><input type="number" min="1" bind:value={settings.max_changed_pages_per_run} disabled={!isAdmin} /></label>
        <label><span>Max new pages</span><input type="number" min="1" bind:value={settings.max_new_pages_per_run} disabled={!isAdmin} /></label>
        <label><span>Max discovered pages</span><input type="number" min="0" bind:value={settings.max_discovered_pages_per_run} disabled={!isAdmin} /></label>
        <label><span>Discovery depth</span><input type="number" min="0" bind:value={settings.max_discovery_depth} disabled={!isAdmin} /></label>
        <label><span>Max runtime minutes</span><input type="number" min="1" bind:value={settings.max_runtime_minutes} disabled={!isAdmin} /></label>
        <label><span>On limit reached</span><select bind:value={settings.on_limit_reached} disabled={!isAdmin}><option value="defer">defer</option><option value="stop">stop</option></select></label>
      </div>

      <h4>Fetch</h4>
      <div class="settings-grid">
        <label><span>Requests per second</span><input type="number" min="0.1" step="0.1" bind:value={settings.rate_limit_requests_per_second} disabled={!isAdmin} /></label>
        <label><span>Retry count</span><input type="number" min="0" bind:value={settings.retry_count} disabled={!isAdmin} /></label>
        <label><span>Retry backoff seconds</span><input type="number" min="1" bind:value={settings.retry_backoff_seconds} disabled={!isAdmin} /></label>
        <label><span>JS rendering</span><select bind:value={settings.enable_js_rendering_for_delta} disabled={!isAdmin}><option value="inherit">inherit</option><option value="off">off</option><option value="auto">auto</option><option value="always">always</option></select></label>
      </div>
      <div class="checks-grid">
        <label><input type="checkbox" bind:checked={settings.respect_robots_txt} disabled={!isAdmin} /> Respect robots.txt</label>
        <label><input type="checkbox" bind:checked={settings.use_conditional_requests} disabled={!isAdmin} /> Conditional requests</label>
        <label><input type="checkbox" bind:checked={settings.fallback_to_get_when_head_fails} disabled={!isAdmin} /> Fallback to GET</label>
        <label><input type="checkbox" bind:checked={settings.recompute_pagerank_when_graph_changed} disabled={!isAdmin} /> Recompute PageRank on graph changes</label>
      </div>

      <h4>URL Normalization</h4>
      <div class="settings-grid">
        <label><span>Canonical host policy</span><select bind:value={settings.canonical_host_policy} disabled={!isAdmin}><option value="project">project</option><option value="follow_redirect">follow redirect</option><option value="none">none</option></select></label>
        <label><span>Keep history days</span><input type="number" min="1" bind:value={settings.keep_delta_history_days} disabled={!isAdmin} /></label>
      </div>
      <div class="checks-grid">
        <label><input type="checkbox" bind:checked={settings.normalize_trailing_slash} disabled={!isAdmin} /> Normalize trailing slash</label>
        <label><input type="checkbox" bind:checked={settings.strip_fragments} disabled={!isAdmin} /> Strip fragments</label>
        <label><input type="checkbox" bind:checked={settings.strip_tracking_params} disabled={!isAdmin} /> Strip tracking params</label>
      </div>
      <div class="textarea-grid">
        <label><span>Allowed query params</span><textarea value={listValue(settings.allowed_query_params)} oninput={(e) => updateListField('allowed_query_params', e.target.value)} disabled={!isAdmin}></textarea></label>
        <label><span>Blocked URL patterns</span><textarea value={listValue(settings.blocked_url_patterns)} oninput={(e) => updateListField('blocked_url_patterns', e.target.value)} disabled={!isAdmin}></textarea></label>
        <label><span>Allowed URL patterns</span><textarea value={listValue(settings.allowed_url_patterns)} oninput={(e) => updateListField('allowed_url_patterns', e.target.value)} disabled={!isAdmin}></textarea></label>
      </div>

      <h4>Safety</h4>
      <div class="checks-grid">
        <label><input type="checkbox" bind:checked={settings.require_confirmation_on_scope_change} disabled={!isAdmin} /> Confirm scope changes</label>
        <label><input type="checkbox" bind:checked={settings.require_confirmation_on_full_recrawl} disabled={!isAdmin} /> Confirm full recrawl</label>
        <label><input type="checkbox" bind:checked={settings.never_delete_previous_snapshot_before_success} disabled={!isAdmin} /> Preserve previous snapshot</label>
        <label><input type="checkbox" bind:checked={settings.pause_delta_when_full_crawl_running} disabled={!isAdmin} /> Pause when crawl is running</label>
      </div>
    </section>

    <aside class="delta-side">
      <h4>Sources</h4>
      {#each Object.entries(preview?.by_source || {}) as [source, count]}
        <div class="source-row"><span>{source.replaceAll('_', ' ')}</span><strong>{count}</strong></div>
      {/each}

      <h4>Sample URLs</h4>
      <div class="sample-list">
        {#each preview?.sample_urls || [] as u}
          <div title={u}>{u}</div>
        {/each}
      </div>

      {#if isAdmin}
        <h4>Manual Queue</h4>
        <textarea class="manual-box" bind:value={manualURLs} placeholder="https://example.com/page"></textarea>
        <button class="btn btn-sm btn-primary" onclick={queueManualURLs}>Queue URLs</button>
      {/if}
    </aside>
  </div>
{/if}

<style>
  .delta-layout {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 280px;
    gap: 24px;
  }

  .delta-header,
  .delta-actions,
  .delta-summary,
  .source-row {
    display: flex;
    align-items: center;
  }

  .delta-header {
    justify-content: space-between;
    gap: 16px;
    margin-bottom: 16px;
  }

  h3,
  h4 {
    margin: 0 0 12px;
  }

  h4 {
    margin-top: 22px;
    color: var(--text);
    font-size: 13px;
  }

  .delta-actions {
    gap: 8px;
  }

  .delta-message {
    margin-bottom: 12px;
    color: var(--success);
    font-size: 13px;
  }

  .delta-summary {
    gap: 12px;
    margin-bottom: 20px;
  }

  .delta-summary > div {
    min-width: 112px;
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 10px 12px;
  }

  .delta-summary strong {
    display: block;
    color: var(--text);
    font-size: 22px;
  }

  .delta-summary span,
  label span {
    color: var(--text-muted);
    font-size: 12px;
  }

  .settings-grid,
  .textarea-grid {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 12px;
  }

  .checks-grid {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 10px 16px;
  }

  label {
    display: flex;
    flex-direction: column;
    gap: 6px;
    font-size: 13px;
  }

  .checks-grid label,
  .check-row {
    flex-direction: row;
    align-items: center;
  }

  input,
  select,
  textarea {
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--surface);
    color: var(--text);
    min-height: 34px;
    padding: 6px 8px;
  }

  textarea {
    min-height: 76px;
    resize: vertical;
  }

  .delta-side {
    border-left: 1px solid var(--border);
    padding-left: 20px;
    min-width: 0;
  }

  .source-row {
    justify-content: space-between;
    border-bottom: 1px solid var(--border);
    padding: 7px 0;
    text-transform: capitalize;
    font-size: 13px;
  }

  .sample-list {
    display: grid;
    gap: 6px;
    max-height: 260px;
    overflow: auto;
    font-size: 12px;
    color: var(--text-muted);
  }

  .sample-list div {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .manual-box {
    width: 100%;
    min-height: 120px;
    margin-bottom: 8px;
  }

  .delta-empty {
    color: var(--text-muted);
    padding: 24px;
  }

  @media (max-width: 980px) {
    .delta-layout,
    .settings-grid,
    .textarea-grid,
    .checks-grid {
      grid-template-columns: 1fr;
    }

    .delta-side {
      border-left: 0;
      border-top: 1px solid var(--border);
      padding: 16px 0 0;
    }
  }
</style>
