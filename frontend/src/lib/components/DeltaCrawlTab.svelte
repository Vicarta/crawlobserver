<script>
  import { onMount } from 'svelte';
  import {
    addProjectDeltaManualURLs,
    getProjectDeltaPreview,
    getProjectDeltaSettings,
    runProjectDelta,
    updateProjectDeltaSettings,
  } from '../api.js';

  let { projectId, isAdmin, onerror, onopensessions } = $props();

  let settings = $state(null);
  let preview = $state(null);
  let loading = $state(false);
  let saving = $state(false);
  let running = $state(false);
  let previewing = $state(false);
  let advancedOpen = $state(false);
  let manualURLs = $state('');
  let message = $state('');
  let startedSessionId = $state('');

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
    previewing = true;
    try {
      preview = await getProjectDeltaPreview(projectId);
    } catch (e) {
      preview = null;
      if (!e.message.includes('no baseline session')) onerror?.(e.message);
    } finally {
      previewing = false;
    }
  }

  async function saveSettings() {
    if (!isAdmin || !settings) return;
    saving = true;
    message = '';
    startedSessionId = '';
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
      startedSessionId = '';
    } catch (e) {
      onerror?.(e.message);
    }
  }

  async function runNow() {
    if (!isAdmin) return;
    running = true;
    message = '';
    startedSessionId = '';
    try {
      const res = await runProjectDelta(projectId);
      await loadPreview();
      startedSessionId = res?.session_id || '';
      message = 'Delta crawl started. It is now visible in Sessions.';
    } catch (e) {
      onerror?.(e.message);
    } finally {
      running = false;
    }
  }

  function openSessions() {
    onopensessions?.(startedSessionId);
  }

  async function copyPreviewURLs() {
    const urls = preview?.sample_urls || [];
    if (urls.length === 0 || !navigator?.clipboard) return;
    try {
      await navigator.clipboard.writeText(urls.join('\n'));
      message = `Copied ${urls.length} sample URLs`;
    } catch (e) {
      onerror?.(e.message);
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

  function formatInt(value) {
    return Number(value || 0).toLocaleString();
  }

  function formatOptionalInt(value) {
    return value == null ? '\u2014' : formatInt(value);
  }

  function sourceLabel(source) {
    return source.replaceAll('_', ' ').replace(/\b\w/g, (m) => m.toUpperCase());
  }

  function scheduleLabel() {
    if (!settings?.enabled) return 'Disabled';
    return `Daily after ${settings.schedule_time || '03:00'} ${settings.timezone || 'UTC'}`;
  }

  function statusTone() {
    if (!settings?.enabled) return 'muted';
    if (!preview?.will_launch) return 'warning';
    return 'active';
  }

  function formatRefreshTime(value) {
    if (!value) return 'Not fetched';
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? String(value) : date.toLocaleString();
  }

  function refreshModeLabel(mode) {
    if (mode === 'fresh') return 'Fresh sitemap';
    if (mode === 'snapshot_fallback') return 'Snapshot fallback';
    if (mode === 'skipped') return 'Sitemap skipped';
    return 'Sitemap unavailable';
  }

  function hasSitemapV2() {
    const selection = preview?.sitemap_selection;
    return (
      preview?.sitemap_published_differences != null ||
      preview?.sitemap_actionable != null ||
      preview?.sitemap_stable_acknowledged != null ||
      selection?.published_difference_total != null ||
      selection?.actionable_total != null ||
      selection?.stable_acknowledged_total != null ||
      selection?.stability_older_session_id ||
      selection?.stability_newer_session_id ||
      selection?.stability_proof_digest ||
      selection?.stability_legacy_complete_pair != null ||
      selection?.publication_held != null
    );
  }

  function selectionCount(topLevelKey, selectionKey, legacyKey) {
    const topLevel = preview?.[topLevelKey];
    if (topLevel != null) return topLevel;
    const selected = preview?.sitemap_selection?.[selectionKey];
    if (selected != null) return selected;
    return legacyKey ? preview?.[legacyKey] : null;
  }

  function proofPairLabel() {
    const selection = preview?.sitemap_selection;
    if (!selection?.stability_older_session_id && !selection?.stability_newer_session_id)
      return 'Not available';
    if (!selection.stability_older_session_id || !selection.stability_newer_session_id)
      return 'Incomplete proof pair';
    return `${selection.stability_older_session_id} -> ${selection.stability_newer_session_id}`;
  }

  function proofStatusLabel() {
    const selection = preview?.sitemap_selection;
    if (selection?.stability_legacy_complete_pair) return 'Legacy complete pair';
    if (selection?.stability_older_session_id && selection?.stability_newer_session_id)
      return 'Two-session proof';
    return 'Proof unavailable';
  }

  function showPublicationHold() {
    const selection = preview?.sitemap_selection;
    return (
      selection?.publication_held === true ||
      Number(selectionCount('sitemap_stable_acknowledged', 'stable_acknowledged_total')) > 0
    );
  }

  onMount(load);
</script>

{#if loading || !settings}
  <div class="delta-empty">Loading...</div>
{:else}
  <div class="delta-shell">
    <section class="delta-main">
      <div class="delta-topbar">
        <div class="delta-title-group">
          <div class="delta-eyebrow">Project automation</div>
          <h3>Daily Delta Crawl</h3>
          <p>
            Re-scan changed, problem, stale, manual, and newly discovered pages without deleting
            previous crawl data.
          </p>
        </div>

        {#if isAdmin}
          <div class="delta-actions">
            <button class="btn btn-sm" onclick={saveSettings} disabled={saving}>
              {saving ? 'Saving...' : 'Save changes'}
            </button>
            <button class="btn btn-sm" onclick={loadPreview} disabled={previewing}>
              {previewing ? 'Previewing...' : 'Preview'}
            </button>
            <button
              class="btn btn-primary btn-sm"
              onclick={runNow}
              disabled={running || !preview?.will_launch}
            >
              {running ? 'Starting...' : 'Run now'}
            </button>
          </div>
        {/if}
      </div>

      {#if message}
        <div class="delta-message">
          <span>{message}</span>
          {#if startedSessionId}
            <button class="btn btn-sm btn-ghost" onclick={openSessions}>View in Sessions</button>
          {/if}
        </div>
      {/if}

      <div class="status-strip {statusTone()}">
        <label class="toggle-row">
          <input type="checkbox" bind:checked={settings.enabled} disabled={!isAdmin} />
          <span class="toggle-control"></span>
          <span>
            <strong
              >{settings.enabled ? 'Daily delta is enabled' : 'Daily delta is disabled'}</strong
            >
            <em>{scheduleLabel()}</em>
          </span>
        </label>
        <div class="status-meta">
          <span>Baseline</span>
          <strong title={preview?.baseline_session_id || ''}
            >{preview?.baseline_session_id || 'No baseline session'}</strong
          >
        </div>
      </div>

      <div class="delta-summary">
        <div>
          <strong>{formatInt(preview?.total_candidates)}</strong>
          <span>Candidates</span>
        </div>
        <div class="summary-primary">
          <strong>{formatInt(preview?.will_launch)}</strong>
          <span>Will crawl now</span>
        </div>
        <div title="Candidates kept for later because the current limits are reached.">
          <strong>{formatInt(preview?.deferred)}</strong>
          <span>Deferred by limits</span>
        </div>
        <div>
          <strong>{formatInt(preview?.launch_limit)}</strong>
          <span>Total crawl budget</span>
        </div>
      </div>

      {#if hasSitemapV2()}
        <div class="delta-summary sitemap-selection-summary sitemap-v2-summary">
          <div>
            <strong
              >{formatOptionalInt(
                selectionCount('sitemap_published_differences', 'published_difference_total'),
              )}</strong
            >
            <span>Published differences</span>
          </div>
          <div class="summary-primary">
            <strong
              >{formatOptionalInt(
                selectionCount('sitemap_actionable', 'actionable_total', 'sitemap_events'),
              )}</strong
            >
            <span>Actionable refetches</span>
          </div>
          <div>
            <strong
              >{formatOptionalInt(
                selectionCount('sitemap_stable_acknowledged', 'stable_acknowledged_total'),
              )}</strong
            >
            <span>Raw-stable acknowledged</span>
          </div>
          <div>
            <strong
              >{formatOptionalInt(selectionCount('sitemap_canaries', 'canary_selected'))}</strong
            >
            <span>Canaries</span>
          </div>
          <div title="Actionable candidates kept for a later Delta plan.">
            <strong
              >{formatOptionalInt(selectionCount('sitemap_deferred', 'event_deferred'))}</strong
            >
            <span>Deferred</span>
          </div>
        </div>

        <div class="sitemap-provenance">
          <div class="section-heading compact">
            <h4>Sitemap decision provenance</h4>
            <span>{proofStatusLabel()}</span>
          </div>
          <div class="provenance-grid">
            <div>
              <span>Proof pair</span>
              <strong title={proofPairLabel()}>{proofPairLabel()}</strong>
            </div>
            <div>
              <span>Proof digest</span>
              <strong title={preview?.sitemap_selection?.stability_proof_digest || ''}
                >{preview?.sitemap_selection?.stability_proof_digest || 'Not available'}</strong
              >
            </div>
          </div>
          {#if showPublicationHold()}
            <div class="delta-note warning">
              Current Snapshot retained; raw stability is not publication evidence.
            </div>
          {/if}
        </div>
      {:else if preview?.sitemap_selection}
        <div class="delta-summary sitemap-selection-summary">
          <div>
            <strong>{formatInt(preview?.sitemap_events)}</strong>
            <span>Changed events</span>
          </div>
          <div>
            <strong>{formatInt(preview?.sitemap_pending_unpublished)}</strong>
            <span>Pending unpublished</span>
          </div>
          <div>
            <strong>{formatInt(preview?.sitemap_canaries)}</strong>
            <span>Canaries</span>
          </div>
          <div title="Changed sitemap events kept for a later Delta plan.">
            <strong>{formatInt(preview?.sitemap_deferred)}</strong>
            <span>Sitemap deferred</span>
          </div>
        </div>
      {/if}

      {#if preview?.held_publication_reason}
        <div class="delta-note warning">{preview.held_publication_reason}</div>
      {/if}

      {#if preview?.sitemap_refresh}
        {@const refresh = preview.sitemap_refresh}
        <div class:warning={refresh.mode !== 'fresh'} class="sitemap-refresh">
          <div class="section-heading compact">
            <h4>{refreshModeLabel(refresh.mode)}</h4>
            <span>Fetched {formatRefreshTime(refresh.fetched_at)}</span>
          </div>
          <div class="refresh-metrics">
            <span><strong>{formatInt(refresh.fresh_url_count)}</strong> fresh URLs</span>
            <span><strong>{formatInt(refresh.snapshot_url_count)}</strong> previous URLs</span>
            <span><strong>{formatInt(refresh.added_count)}</strong> added</span>
            <span><strong>{formatInt(refresh.removed_count)}</strong> removed</span>
            <span><strong>{formatInt(refresh.invalid_entry_count)}</strong> invalid</span>
          </div>
          {#if refresh.declared_sitemap_urls?.length}
            <p class="refresh-roots" title={refresh.declared_sitemap_urls.join('\n')}>
              {refresh.declared_sitemap_urls.join(', ')}
            </p>
          {/if}
          {#if refresh.warnings?.length}
            <p class="refresh-warning">{refresh.warnings.join(' ')}</p>
          {/if}
        </div>
      {/if}

      <div class="delta-section">
        <div class="section-heading">
          <h4>Schedule</h4>
          <span
            >{settings.enabled
              ? 'Runs once per day after the selected local time.'
              : 'Turn on to run automatically.'}</span
          >
        </div>
        <div class="settings-grid">
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
            <input
              type="number"
              min="1"
              bind:value={settings.stale_after_days}
              disabled={!isAdmin}
            />
          </label>
        </div>
        {#if settings.timezone === 'UTC'}
          <div class="delta-note">
            This is scheduled in UTC. For Kyiv local time, use <code>Europe/Kiev</code>.
          </div>
        {/if}
      </div>

      <div class="delta-section">
        <div class="section-heading">
          <h4>Candidate Sources</h4>
          <span>Choose where the daily candidate list comes from.</span>
        </div>
        <div class="source-grid">
          <label
            ><input
              type="checkbox"
              bind:checked={settings.source_sitemap}
              disabled={!isAdmin}
            /><span>Sitemap</span></label
          >
          <label
            ><input type="checkbox" bind:checked={settings.source_gsc} disabled={!isAdmin} /><span
              >Google Search Console</span
            ></label
          >
          <label
            ><input
              type="checkbox"
              bind:checked={settings.source_problem_pages}
              disabled={!isAdmin}
            /><span>Problem pages</span></label
          >
          <label
            ><input
              type="checkbox"
              bind:checked={settings.source_stale_pages}
              disabled={!isAdmin}
            /><span>Stale pages</span></label
          >
          <label
            ><input
              type="checkbox"
              bind:checked={settings.source_manual_queue}
              disabled={!isAdmin}
            /><span>Manual queue</span></label
          >
        </div>
      </div>

      <div class="delta-section">
        <div class="section-heading">
          <h4>Run Limits</h4>
          <span>Keep the daily job bounded to its selected URL plan.</span>
        </div>
        <div class="settings-grid">
          <label
            ><span>Max candidates</span><input
              type="number"
              min="0"
              bind:value={settings.max_candidates_per_run}
              disabled={!isAdmin}
            /></label
          >
          <label
            ><span>Changed sitemap cap (0 = all)</span><input
              type="number"
              min="0"
              bind:value={settings.sitemap_changed_limit}
              disabled={!isAdmin}
            /></label
          >
          <label
            ><span>Max rotating sitemap canaries (10% sample)</span><input
              type="number"
              min="0"
              bind:value={settings.sitemap_canary_count}
              disabled={!isAdmin}
            /></label
          >
          <label
            ><span>Max changed pages</span><input
              type="number"
              min="1"
              bind:value={settings.max_changed_pages_per_run}
              disabled={!isAdmin}
            /></label
          >
          <label
            ><span>Max new pages</span><input
              type="number"
              min="1"
              bind:value={settings.max_new_pages_per_run}
              disabled={!isAdmin}
            /></label
          >
          <label
            ><span>Max discovered pages</span><input
              type="number"
              min="0"
              bind:value={settings.max_discovered_pages_per_run}
              disabled={!isAdmin}
            /></label
          >
          <label
            ><span>Discovery depth</span><input
              type="number"
              min="0"
              bind:value={settings.max_discovery_depth}
              disabled={!isAdmin}
            /></label
          >
          <label
            ><span>Max runtime minutes</span><input
              type="number"
              min="1"
              bind:value={settings.max_runtime_minutes}
              disabled={!isAdmin}
            /></label
          >
        </div>
      </div>

      <div class="delta-advanced">
        <button class="advanced-toggle" onclick={() => (advancedOpen = !advancedOpen)}>
          <span>Advanced settings</span>
          <strong>{advancedOpen ? 'Hide' : 'Show'}</strong>
        </button>

        {#if advancedOpen}
          <div class="advanced-body">
            <div class="delta-section compact">
              <div class="section-heading">
                <h4>Fetch Behavior</h4>
              </div>
              <div class="settings-grid">
                <label
                  ><span>Requests per second</span><input
                    type="number"
                    min="0.1"
                    step="0.1"
                    bind:value={settings.rate_limit_requests_per_second}
                    disabled={!isAdmin}
                  /></label
                >
                <label
                  ><span>Retry count</span><input
                    type="number"
                    min="0"
                    bind:value={settings.retry_count}
                    disabled={!isAdmin}
                  /></label
                >
                <label
                  ><span>Retry backoff seconds</span><input
                    type="number"
                    min="1"
                    bind:value={settings.retry_backoff_seconds}
                    disabled={!isAdmin}
                  /></label
                >
                <label
                  ><span>JS rendering</span><select
                    bind:value={settings.enable_js_rendering_for_delta}
                    disabled={!isAdmin}
                    ><option value="inherit">inherit</option><option value="off">off</option><option
                      value="auto">auto</option
                    ><option value="always">always</option></select
                  ></label
                >
                <label
                  ><span>On limit reached</span><select
                    bind:value={settings.on_limit_reached}
                    disabled={!isAdmin}
                    ><option value="defer">defer</option><option value="stop">stop</option></select
                  ></label
                >
              </div>
              <div class="checks-grid">
                <label
                  ><input
                    type="checkbox"
                    bind:checked={settings.respect_robots_txt}
                    disabled={!isAdmin}
                  /> Respect robots.txt</label
                >
                <label
                  ><input
                    type="checkbox"
                    bind:checked={settings.use_conditional_requests}
                    disabled={!isAdmin}
                  /> Conditional requests</label
                >
                <label
                  ><input
                    type="checkbox"
                    bind:checked={settings.fallback_to_get_when_head_fails}
                    disabled={!isAdmin}
                  /> Fallback to GET</label
                >
                <label
                  ><input
                    type="checkbox"
                    bind:checked={settings.recompute_pagerank_when_graph_changed}
                    disabled={!isAdmin}
                  /> Recompute PageRank on graph changes</label
                >
                <label
                  ><input
                    type="checkbox"
                    bind:checked={settings.include_footer_links_in_pagerank}
                    disabled={!isAdmin}
                  /> Include footer links in internal PageRank</label
                >
              </div>
            </div>

            <div class="delta-section compact">
              <div class="section-heading">
                <h4>URL Normalization</h4>
              </div>
              <div class="settings-grid">
                <label
                  ><span>Canonical host policy</span><select
                    bind:value={settings.canonical_host_policy}
                    disabled={!isAdmin}
                    ><option value="project">project</option><option value="follow_redirect"
                      >follow redirect</option
                    ><option value="none">none</option></select
                  ></label
                >
                <label
                  ><span>Keep history days</span><input
                    type="number"
                    min="1"
                    bind:value={settings.keep_delta_history_days}
                    disabled={!isAdmin}
                  /></label
                >
                <label
                  ><span>Snapshot deltas kept</span><input
                    type="number"
                    min="1"
                    bind:value={settings.current_snapshot_max_deltas}
                    disabled={!isAdmin}
                  /></label
                >
                <label
                  ><span>Fold baseline every days</span><input
                    type="number"
                    min="1"
                    bind:value={settings.current_snapshot_baseline_interval_days}
                    disabled={!isAdmin}
                  /></label
                >
              </div>
              <div class="checks-grid">
                <label
                  ><input
                    type="checkbox"
                    bind:checked={settings.normalize_trailing_slash}
                    disabled={!isAdmin}
                  /> Normalize trailing slash</label
                >
                <label
                  ><input
                    type="checkbox"
                    bind:checked={settings.strip_fragments}
                    disabled={!isAdmin}
                  /> Strip fragments</label
                >
                <label
                  ><input
                    type="checkbox"
                    bind:checked={settings.strip_tracking_params}
                    disabled={!isAdmin}
                  /> Strip tracking params</label
                >
              </div>
              <div class="textarea-grid">
                <label
                  ><span>Allowed query params</span><textarea
                    value={listValue(settings.allowed_query_params)}
                    oninput={(e) => updateListField('allowed_query_params', e.target.value)}
                    disabled={!isAdmin}
                  ></textarea></label
                >
                <label
                  ><span>Blocked URL patterns</span><textarea
                    value={listValue(settings.blocked_url_patterns)}
                    oninput={(e) => updateListField('blocked_url_patterns', e.target.value)}
                    disabled={!isAdmin}
                  ></textarea></label
                >
                <label
                  ><span>Allowed URL patterns</span><textarea
                    value={listValue(settings.allowed_url_patterns)}
                    oninput={(e) => updateListField('allowed_url_patterns', e.target.value)}
                    disabled={!isAdmin}
                  ></textarea></label
                >
                <label
                  ><span>Footer selectors</span><textarea
                    value={listValue(settings.footer_selector_patterns)}
                    placeholder="footer&#10;.site-footer&#10;[data-section=&quot;footer&quot;]"
                    oninput={(e) => updateListField('footer_selector_patterns', e.target.value)}
                    disabled={!isAdmin}
                  ></textarea></label
                >
              </div>
            </div>

            <div class="delta-section compact">
              <div class="section-heading">
                <h4>Safety</h4>
              </div>
              <div class="checks-grid">
                <label
                  ><input
                    type="checkbox"
                    bind:checked={settings.require_confirmation_on_scope_change}
                    disabled={!isAdmin}
                  /> Confirm scope changes</label
                >
                <label
                  ><input
                    type="checkbox"
                    bind:checked={settings.require_confirmation_on_full_recrawl}
                    disabled={!isAdmin}
                  /> Confirm full recrawl</label
                >
                <label
                  ><input
                    type="checkbox"
                    bind:checked={settings.never_delete_previous_snapshot_before_success}
                    disabled={!isAdmin}
                  /> Preserve previous snapshot</label
                >
                <label
                  ><input
                    type="checkbox"
                    bind:checked={settings.pause_delta_when_full_crawl_running}
                    disabled={!isAdmin}
                  /> Pause when crawl is running</label
                >
              </div>
            </div>
          </div>
        {/if}
      </div>
    </section>

    <aside class="delta-side">
      <div class="side-card">
        <div class="side-heading">
          <h4>Preview Details</h4>
          <button
            class="btn btn-sm"
            onclick={copyPreviewURLs}
            disabled={!preview?.sample_urls?.length}>Copy URLs</button
          >
        </div>

        {#if preview?.deferred > 0}
          <div class="side-warning">
            {formatInt(preview.deferred)} candidates will wait for a later run because current limits
            are reached.
          </div>
        {/if}

        <h5>Sources</h5>
        <div class="source-list">
          {#each Object.entries(preview?.by_source || {}) as [source, count]}
            <div class="source-row">
              <span>{sourceLabel(source)}</span><strong>{formatInt(count)}</strong>
            </div>
          {/each}
        </div>

        <h5>Sample URLs</h5>
        <div class="sample-list">
          {#each preview?.sample_urls || [] as u}
            <div title={u}>{u}</div>
          {:else}
            <div>No candidate URLs yet.</div>
          {/each}
        </div>
      </div>

      {#if isAdmin}
        <div class="side-card">
          <h4>Manual Queue</h4>
          <p>
            Add one URL per line. Queued URLs are consumed only after they are actually launched.
          </p>
          <textarea
            class="manual-box"
            bind:value={manualURLs}
            placeholder="https://example.com/page"
          ></textarea>
          <button class="btn btn-sm btn-primary" onclick={queueManualURLs}>Queue URLs</button>
        </div>
      {/if}
    </aside>
  </div>
{/if}

<style>
  .delta-shell {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(300px, 320px);
    gap: 24px;
    align-items: start;
    padding: 24px 28px 32px;
  }

  .delta-main {
    min-width: 0;
  }

  .delta-topbar,
  .delta-actions,
  .status-strip,
  .delta-summary,
  .side-heading,
  .source-row,
  .advanced-toggle {
    display: flex;
    align-items: center;
  }

  .delta-topbar {
    align-items: flex-start;
    justify-content: space-between;
    gap: 20px;
    margin-bottom: 18px;
  }

  .delta-title-group {
    max-width: 720px;
  }

  .delta-eyebrow {
    color: var(--accent);
    font-size: 12px;
    font-weight: 700;
    letter-spacing: 0.03em;
    text-transform: uppercase;
  }

  h3,
  h4,
  h5,
  p {
    margin: 0;
  }

  h3 {
    margin-top: 4px;
    color: var(--text);
    font-size: 22px;
    line-height: 1.2;
  }

  h4 {
    color: var(--text);
    font-size: 14px;
  }

  h5 {
    margin-top: 18px;
    margin-bottom: 8px;
    color: var(--text);
    font-size: 12px;
    letter-spacing: 0.03em;
    text-transform: uppercase;
  }

  p,
  .section-heading span,
  label span,
  .status-strip em,
  .side-card p {
    color: var(--text-muted);
    font-size: 13px;
  }

  .delta-title-group p {
    margin-top: 8px;
    max-width: 640px;
  }

  .delta-actions {
    flex-wrap: wrap;
    justify-content: flex-end;
    gap: 8px;
    min-width: 230px;
  }

  .delta-message {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 14px;
    border: 1px solid rgba(34, 197, 94, 0.28);
    border-radius: 8px;
    background: rgba(34, 197, 94, 0.08);
    color: var(--success);
    font-size: 13px;
    padding: 9px 12px;
  }

  .delta-message .btn {
    flex: 0 0 auto;
  }

  .status-strip {
    justify-content: space-between;
    gap: 18px;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: color-mix(in srgb, var(--surface) 78%, var(--accent-light, transparent));
    padding: 14px 16px;
  }

  .status-strip.active {
    border-color: color-mix(in srgb, var(--accent) 55%, var(--border));
  }

  .status-strip.warning {
    border-color: rgba(245, 158, 11, 0.5);
  }

  .toggle-row {
    display: flex;
    align-items: center;
    gap: 12px;
    min-width: 0;
    cursor: pointer;
  }

  .toggle-row input {
    position: absolute;
    opacity: 0;
    pointer-events: none;
  }

  .toggle-row span:last-child {
    display: grid;
    gap: 2px;
  }

  .toggle-row strong,
  .status-meta strong {
    color: var(--text);
    font-size: 14px;
  }

  .toggle-row em {
    font-style: normal;
  }

  .toggle-control {
    position: relative;
    width: 42px;
    height: 24px;
    flex: 0 0 auto;
    border: 1px solid var(--border);
    border-radius: 999px;
    background: var(--surface);
    transition:
      background 0.15s ease,
      border-color 0.15s ease;
  }

  .toggle-control::after {
    content: '';
    position: absolute;
    top: 3px;
    left: 3px;
    width: 16px;
    height: 16px;
    border-radius: 50%;
    background: var(--text-muted);
    transition:
      transform 0.15s ease,
      background 0.15s ease;
  }

  .toggle-row input:checked + .toggle-control {
    border-color: var(--accent);
    background: var(--accent);
  }

  .toggle-row input:checked + .toggle-control::after {
    background: var(--accent-text, #fff);
    transform: translateX(18px);
  }

  .status-meta {
    display: grid;
    gap: 3px;
    min-width: 220px;
    text-align: right;
  }

  .status-meta span {
    color: var(--text-muted);
    font-size: 12px;
  }

  .status-meta strong {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .delta-summary {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 12px;
    margin: 16px 0 20px;
  }

  .sitemap-refresh {
    display: grid;
    gap: 10px;
    margin: 16px 0;
    padding: 14px 16px;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--bg-card);
  }

  .sitemap-refresh.warning {
    border-color: color-mix(in srgb, var(--warning) 55%, var(--border));
  }

  .sitemap-v2-summary {
    grid-template-columns: repeat(5, minmax(0, 1fr));
  }

  .sitemap-provenance {
    display: grid;
    gap: 12px;
    margin: 16px 0;
    padding: 14px 16px;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--bg-card);
  }

  .provenance-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 10px 18px;
  }

  .provenance-grid > div {
    display: grid;
    gap: 3px;
    min-width: 0;
  }

  .provenance-grid span {
    color: var(--text-muted);
    font-size: 12px;
  }

  .provenance-grid strong {
    overflow: hidden;
    color: var(--text);
    font-size: 13px;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .section-heading.compact {
    margin: 0;
  }

  .refresh-metrics {
    display: flex;
    flex-wrap: wrap;
    gap: 10px 18px;
    color: var(--text-secondary);
    font-size: 13px;
  }

  .refresh-metrics strong {
    color: var(--text);
  }

  .refresh-roots,
  .refresh-warning {
    overflow: hidden;
    margin: 0;
    color: var(--text-muted);
    font-size: 12px;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .refresh-warning {
    color: var(--warning);
    white-space: normal;
  }

  .delta-summary > div {
    min-width: 0;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface);
    padding: 14px;
  }

  .delta-summary .summary-primary {
    border-color: color-mix(in srgb, var(--accent) 50%, var(--border));
    box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--accent) 18%, transparent);
  }

  .delta-summary strong {
    display: block;
    color: var(--text);
    font-size: 24px;
    line-height: 1.1;
  }

  .delta-summary span {
    display: block;
    margin-top: 8px;
    color: var(--text-muted);
    font-size: 12px;
  }

  .delta-section,
  .delta-advanced,
  .side-card {
    border: 1px solid var(--border);
    border-radius: 8px;
    background: color-mix(in srgb, var(--surface) 86%, transparent);
  }

  .delta-section {
    margin-top: 14px;
    padding: 16px;
  }

  .delta-section.compact {
    margin-top: 12px;
  }

  .section-heading {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 16px;
    margin-bottom: 14px;
  }

  .section-heading span {
    text-align: right;
  }

  .settings-grid,
  .textarea-grid {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 12px;
  }

  .source-grid,
  .checks-grid {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 10px 16px;
  }

  label {
    display: flex;
    flex-direction: column;
    gap: 6px;
    color: var(--text);
    font-size: 13px;
  }

  .source-grid label,
  .checks-grid label {
    flex-direction: row;
    align-items: center;
    min-height: 30px;
  }

  .source-grid label {
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface);
    padding: 8px 10px;
  }

  .source-grid label span {
    color: var(--text);
  }

  input,
  select,
  textarea {
    width: 100%;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--surface);
    color: var(--text);
    min-height: 36px;
    padding: 7px 9px;
  }

  input[type='checkbox'] {
    width: auto;
    min-height: auto;
    padding: 0;
  }

  textarea {
    min-height: 76px;
    resize: vertical;
  }

  .delta-note,
  .side-warning {
    margin-top: 12px;
    border: 1px solid rgba(245, 158, 11, 0.35);
    border-radius: 8px;
    background: rgba(245, 158, 11, 0.08);
    color: var(--text);
    font-size: 13px;
    padding: 10px 12px;
  }

  .delta-note code {
    color: var(--text);
  }

  .delta-advanced {
    margin-top: 14px;
    overflow: hidden;
  }

  .advanced-toggle {
    width: 100%;
    justify-content: space-between;
    border: 0;
    background: transparent;
    color: var(--text);
    cursor: pointer;
    font: inherit;
    padding: 14px 16px;
  }

  .advanced-toggle strong {
    color: var(--accent);
    font-size: 13px;
  }

  .advanced-body {
    border-top: 1px solid var(--border);
    padding: 0 16px 16px;
  }

  .delta-side {
    display: grid;
    gap: 14px;
    min-width: 0;
    position: sticky;
    top: 24px;
  }

  .side-card {
    padding: 18px;
  }

  .side-heading {
    justify-content: space-between;
    gap: 10px;
  }

  .source-list {
    display: grid;
  }

  .source-row {
    justify-content: space-between;
    gap: 12px;
    border-bottom: 1px solid var(--border);
    padding: 8px 0;
    font-size: 13px;
  }

  .source-row span {
    color: var(--text-muted);
  }

  .source-row strong {
    color: var(--text);
  }

  .sample-list {
    display: grid;
    gap: 6px;
    max-height: 240px;
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
    min-height: 112px;
    margin: 12px 0 8px;
  }

  .delta-empty {
    color: var(--text-muted);
    padding: 24px;
  }

  @media (max-width: 1180px) {
    .delta-shell {
      grid-template-columns: 1fr;
      padding: 22px;
    }

    .delta-side {
      position: static;
    }
  }

  @media (max-width: 760px) {
    .delta-topbar,
    .status-strip,
    .section-heading {
      align-items: stretch;
      flex-direction: column;
    }

    .delta-actions {
      justify-content: flex-start;
      min-width: 0;
    }

    .delta-message {
      align-items: flex-start;
      flex-direction: column;
    }

    .status-meta,
    .section-heading span {
      min-width: 0;
      text-align: left;
    }

    .delta-summary,
    .settings-grid,
    .textarea-grid,
    .source-grid,
    .checks-grid,
    .sitemap-v2-summary,
    .provenance-grid {
      grid-template-columns: 1fr;
    }

    .delta-shell {
      padding: 18px 16px 24px;
    }
  }
</style>
