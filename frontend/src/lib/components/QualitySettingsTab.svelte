<script>
  import {
    getProjectQualitySettings,
    updateProjectQualitySettings,
    getProjectCanaries,
    createProjectCanary,
    updateProjectCanary,
    deleteProjectCanary,
  } from '../api.js';

  let { projectId, isAdmin = false, onerror } = $props();

  let loading = $state(true);
  let saving = $state(false);
  let message = $state('');
  let settings = $state(null);
  let canaries = $state([]);
  let editingCanary = $state(newCanary());

  const numberFields = [
    ['min_trusted_score', 'Trusted score', 'Session is trusted at or above this score.'],
    ['untrusted_score_below', 'Untrusted below', 'Below this score agents must not use the data.'],
    ['coverage_drop_percent', 'Coverage drop %', 'HTML page count drop threshold.'],
    ['coverage_growth_percent', 'Coverage growth %', 'HTML page count growth warning threshold.'],
    ['coverage_min_pages_delta', 'Coverage min delta', 'Minimum absolute page change before coverage % applies.'],
    ['internal_links_drop_percent', 'Internal links drop %', 'Internal link count drop threshold.'],
    ['internal_links_min_delta', 'Internal links min delta', 'Minimum absolute internal link change.'],
    ['status_404_percent', '404 growth %', '404 growth warning threshold.'],
    ['status_404_min_delta', '404 min delta', 'Minimum absolute 404 growth.'],
    ['noindex_percent', 'Noindex growth %', 'Noindex growth warning threshold.'],
    ['noindex_min_delta', 'Noindex min delta', 'Minimum absolute noindex growth.'],
    ['redirect_percent', 'Redirect growth %', 'Redirect growth warning threshold.'],
    ['redirect_min_delta', 'Redirect min delta', 'Minimum absolute redirect growth.'],
    ['canonical_mismatch_percent', 'Canonical mismatch growth %', 'Canonical mismatch growth warning threshold.'],
    ['canonical_mismatch_min_delta', 'Canonical mismatch min delta', 'Minimum absolute canonical mismatch growth.'],
    ['pagerank_top_n', 'PageRank top N', 'How many top PageRank pages to compare.'],
    ['pagerank_top_overlap_min_percent', 'PageRank overlap min %', 'Minimum overlap with baseline top pages.'],
    ['pagerank_zero_top_pages_max', 'Zero PageRank top pages max', 'Allowed count of top pages with zero PageRank.'],
    ['canary_min_internal_links_default', 'Default canary internal links', 'Default lower bound for canary internal outlinks.'],
  ];

  function newCanary() {
    return {
      url: '',
      expected_status: 200,
      expected_final_url: '',
      expected_canonical: '',
      title_contains: '',
      min_internal_links: 1,
      expect_indexable: true,
      active: true,
    };
  }

  async function load() {
    loading = true;
    message = '';
    try {
      const [st, list] = await Promise.all([
        getProjectQualitySettings(projectId),
        getProjectCanaries(projectId),
      ]);
      settings = st;
      canaries = list || [];
      editingCanary.min_internal_links = st.canary_min_internal_links_default ?? 1;
    } catch (e) {
      onerror?.(e.message);
    } finally {
      loading = false;
    }
  }

  async function saveSettings() {
    if (!isAdmin || !settings) return;
    saving = true;
    message = '';
    try {
      settings = await updateProjectQualitySettings(projectId, settings);
      message = 'Quality settings saved.';
    } catch (e) {
      onerror?.(e.message);
    } finally {
      saving = false;
    }
  }

  async function saveCanary() {
    if (!isAdmin) return;
    try {
      if (editingCanary.id) {
        await updateProjectCanary(projectId, editingCanary.id, editingCanary);
      } else {
        await createProjectCanary(projectId, editingCanary);
      }
      editingCanary = newCanary();
      if (settings) editingCanary.min_internal_links = settings.canary_min_internal_links_default ?? 1;
      canaries = await getProjectCanaries(projectId);
    } catch (e) {
      onerror?.(e.message);
    }
  }

  async function removeCanary(id) {
    if (!isAdmin) return;
    try {
      await deleteProjectCanary(projectId, id);
      canaries = await getProjectCanaries(projectId);
    } catch (e) {
      onerror?.(e.message);
    }
  }

  function editCanary(c) {
    editingCanary = { ...c };
  }

  load();
</script>

{#if loading}
  <p class="loading-msg">Loading quality settings...</p>
{:else if settings}
  <div class="quality-layout">
    <section class="quality-section">
      <div class="section-head">
        <div>
          <div class="eyebrow">Data Quality</div>
          <h2>Crawl trust gate</h2>
        </div>
        <label class="toggle-row">
          <input type="checkbox" bind:checked={settings.enabled} disabled={!isAdmin} />
          <span>{settings.enabled ? 'Enabled' : 'Disabled'}</span>
        </label>
      </div>

      <div class="settings-grid">
        {#each numberFields as [field, label, hint]}
          <label class="setting-field">
            <span>{label}</span>
            <input
              type="number"
              min="0"
              step={field.includes('percent') ? '0.1' : '1'}
              bind:value={settings[field]}
              disabled={!isAdmin}
            />
            <small>{hint}</small>
          </label>
        {/each}
      </div>

      {#if isAdmin}
        <div class="form-actions">
          <button class="btn btn-primary" onclick={saveSettings} disabled={saving}>
            {saving ? 'Saving...' : 'Save quality settings'}
          </button>
          {#if message}<span class="text-sm text-muted">{message}</span>{/if}
        </div>
      {/if}
    </section>

    <section class="quality-section">
      <div class="section-head">
        <div>
          <div class="eyebrow">Canaries</div>
          <h2>Stable control URLs</h2>
        </div>
      </div>

      {#if canaries.length > 0}
        <div class="canary-list">
          {#each canaries as c}
            <div class="canary-row">
              <div>
                <div class="canary-url">{c.url}</div>
                <div class="canary-meta">
                  status {c.expected_status} · min links {c.min_internal_links} · {c.expect_indexable
                    ? 'indexable'
                    : 'not indexable'}
                </div>
              </div>
              {#if isAdmin}
                <div class="row-actions">
                  <button class="btn btn-sm" onclick={() => editCanary(c)}>Edit</button>
                  <button class="btn btn-sm" onclick={() => removeCanary(c.id)}>Delete</button>
                </div>
              {/if}
            </div>
          {/each}
        </div>
      {:else}
        <p class="text-muted">No canary URLs configured.</p>
      {/if}

      {#if isAdmin}
        <div class="canary-form">
          <label class="setting-field wide">
            <span>URL</span>
            <input type="url" bind:value={editingCanary.url} placeholder="https://example.com/" />
          </label>
          <label class="setting-field">
            <span>Expected status</span>
            <input type="number" min="100" max="599" bind:value={editingCanary.expected_status} />
          </label>
          <label class="setting-field">
            <span>Min internal links</span>
            <input type="number" min="0" bind:value={editingCanary.min_internal_links} />
          </label>
          <label class="setting-field wide">
            <span>Expected final URL</span>
            <input type="url" bind:value={editingCanary.expected_final_url} />
          </label>
          <label class="setting-field wide">
            <span>Expected canonical</span>
            <input type="url" bind:value={editingCanary.expected_canonical} />
          </label>
          <label class="setting-field wide">
            <span>Title contains</span>
            <input type="text" bind:value={editingCanary.title_contains} />
          </label>
          <label class="check-row">
            <input type="checkbox" bind:checked={editingCanary.expect_indexable} />
            <span>Expect indexable</span>
          </label>
          <label class="check-row">
            <input type="checkbox" bind:checked={editingCanary.active} />
            <span>Active</span>
          </label>
          <div class="form-actions wide">
            <button class="btn btn-primary" onclick={saveCanary}>
              {editingCanary.id ? 'Update canary' : 'Add canary'}
            </button>
            {#if editingCanary.id}
              <button class="btn" onclick={() => (editingCanary = newCanary())}>Cancel edit</button>
            {/if}
          </div>
        </div>
      {/if}
    </section>
  </div>
{/if}

<style>
  .quality-layout {
    display: grid;
    gap: 20px;
  }
  .quality-section {
    padding: 20px;
    border: 1px solid var(--border);
    border-radius: 8px;
  }
  .section-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    margin-bottom: 16px;
  }
  .section-head h2 {
    margin: 2px 0 0;
    font-size: 18px;
  }
  .eyebrow {
    color: var(--accent);
    font-size: 12px;
    font-weight: 700;
    text-transform: uppercase;
  }
  .settings-grid,
  .canary-form {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
    gap: 14px;
  }
  .setting-field {
    display: grid;
    gap: 6px;
  }
  .setting-field span,
  .check-row span,
  .toggle-row span {
    font-size: 13px;
    font-weight: 600;
  }
  .setting-field small {
    color: var(--text-muted);
    font-size: 12px;
    line-height: 1.35;
  }
  .setting-field input {
    width: 100%;
  }
  .wide {
    grid-column: 1 / -1;
  }
  .toggle-row,
  .check-row {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .canary-list {
    display: grid;
    gap: 8px;
    margin-bottom: 18px;
  }
  .canary-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 12px;
    border: 1px solid var(--border);
    border-radius: 8px;
  }
  .canary-url {
    font-size: 13px;
    font-weight: 600;
    word-break: break-all;
  }
  .canary-meta {
    color: var(--text-muted);
    font-size: 12px;
    margin-top: 4px;
  }
  .row-actions {
    display: flex;
    gap: 8px;
    flex-shrink: 0;
  }
</style>
