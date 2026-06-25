<script>
  import { onMount, onDestroy } from 'svelte';
  import {
    getSessionsPaginated,
    renameProject,
    deleteProject,
    deleteProjectWithSessions,
    getProviderConnections,
    disassociateSession,
    getSessionQuality,
  } from '../api.js';
  import { fmtN, timeAgo } from '../utils.js';
  import { pushURL } from '../router.js';
  import { t } from '../i18n/index.svelte.js';
  import GSCTab from './GSCTab.svelte';
  import ProvidersTab from './ProvidersTab.svelte';
  import DeltaCrawlTab from './DeltaCrawlTab.svelte';
  import QualitySettingsTab from './QualitySettingsTab.svelte';
  import ConfirmModal from './ConfirmModal.svelte';

  const PROJ_SESSIONS_LIMIT = 30;

  /** @param {HTMLElement} node */
  function focusOnMount(node) {
    node.focus();
  }

  let {
    project,
    initialProjectTab = 'sessions',
    initialGscSubView = 'overview',
    initialProviderSubView = 'overview',
    onerror,
    onselectsession,
    ongohome,
    onnewcrawl,
    onprojectrenamed,
    onprojectdeleted,
    onpushurl,
    currentUser,
  } = $props();

  // --- Local state ---
  let isAdmin = $derived(currentUser?.role === 'admin');
  let projectTab = $state(initialProjectTab);
  let projSessions = $state([]);
  let projSessionsTotal = $state(0);
  let projSessionsOffset = $state(0);
  let renamingProject = $state(false);
  let renameValue = $state('');
  let gscSubView = $state(initialGscSubView);
  let providerSubView = $state(initialProviderSubView);
  let confirmState = $state(null);
  let providerConnections = $state([]);
  let qualityDetailSession = $state(null);
  let qualityDetail = $state(null);
  let qualityDetailLoading = $state(false);

  function showConfirm(message, onConfirm, opts = {}) {
    confirmState = { message, onConfirm, ...opts };
  }

  // --- Data loading ---
  async function loadProjectSessions() {
    if (!project) return;
    try {
      const res = await getSessionsPaginated(PROJ_SESSIONS_LIMIT, projSessionsOffset, {
        projectId: project.id,
      });
      projSessions = res.sessions || [];
      projSessionsTotal = res.total || 0;
      startPollingIfRunning();
    } catch (e) {
      onerror?.(e.message);
    }
  }

  function switchProjectTab(tab) {
    if (!isAdmin && tab === 'providers') return;
    projectTab = tab;
    // Use "providers" in URL for any provider tab
    const urlTab = tab.startsWith('provider:') ? 'providers' : tab;
    if (project) pushURL(`/projects/${project.id}/${urlTab}`);
  }

  function sessionTypeLabel(session) {
    return session?.quality?.is_full_crawl === false || session?.Label === 'Daily Delta Crawl'
      ? 'Daily Delta'
      : 'Full crawl';
  }

  function qualityBadgeLabel(quality) {
    if (!quality) return 'pending';
    if (quality.is_full_crawl === false) return 'Delta · not baseline';
    return `${quality.status} · ${quality.score}`;
  }

  async function openQualityDetail(session) {
    qualityDetailSession = session;
    qualityDetail = null;
    qualityDetailLoading = true;
    try {
      qualityDetail = await getSessionQuality(session.ID);
    } catch (e) {
      onerror?.(e.message);
    } finally {
      qualityDetailLoading = false;
    }
  }

  // --- Rename ---
  function startRenameProject() {
    if (!isAdmin) return;
    renamingProject = true;
    renameValue = project?.name || '';
  }

  async function confirmRenameProject() {
    if (!isAdmin) return;
    const name = renameValue.trim();
    if (name && name !== project?.name) {
      try {
        await renameProject(project.id, name);
        onprojectrenamed?.(project.id);
      } catch (e) {
        onerror?.(e.message);
      }
    }
    renamingProject = false;
  }

  function cancelRenameProject() {
    renamingProject = false;
  }

  // --- Delete ---
  function handleDeleteProject() {
    if (!isAdmin) return;
    showConfirm(
      t('project.deleteProject') + ` "${project?.name}"?`,
      async () => {
        try {
          await deleteProject(project.id);
          onprojectdeleted?.();
        } catch (e) {
          onerror?.(e.message);
        }
      },
      { danger: true, confirmLabel: t('common.delete') },
    );
  }

  function handleDeleteProjectWithSessions() {
    if (!isAdmin) return;
    showConfirm(
      t('project.deleteProjectWithSessions') + ` "${project?.name}"?`,
      async () => {
        try {
          await deleteProjectWithSessions(project.id);
          onprojectdeleted?.();
        } catch (e) {
          onerror?.(e.message);
        }
      },
      { danger: true, confirmLabel: t('common.delete') },
    );
  }

  // --- Mount / auto-refresh ---
  let pollInterval = null;

  function startPollingIfRunning() {
    stopPolling();
    const hasRunning = projSessions.some((s) => s.is_running || s.is_queued);
    if (hasRunning) {
      pollInterval = setInterval(loadProjectSessions, 3000);
    }
  }

  function stopPolling() {
    if (pollInterval) {
      clearInterval(pollInterval);
      pollInterval = null;
    }
  }

  const providerMeta = {
    seobserver: {
      label: 'SEObserver',
      icon: '/seobserver.png',
    },
  };

  async function loadProviderConnections() {
    try {
      providerConnections = await getProviderConnections(project.id);
    } catch {
      providerConnections = [];
    }
    // Resolve legacy "providers" tab to first connected provider
    if (projectTab === 'providers' && providerConnections.length > 0) {
      projectTab = 'provider:' + providerConnections[0].provider;
    }
  }

  onMount(() => {
    loadProjectSessions();
    loadProviderConnections();
  });

  onDestroy(stopPolling);
</script>

<div class="breadcrumb">
  <a
    href="/"
    onclick={(e) => {
      e.preventDefault();
      ongohome?.();
    }}>{t('project.dashboard')}</a
  >
  <span>/</span>
  {#if renamingProject}
    <input
      class="project-rename-input"
      type="text"
      bind:value={renameValue}
      use:focusOnMount
      onkeydown={(e) => {
        if (e.key === 'Enter') confirmRenameProject();
        if (e.key === 'Escape') cancelRenameProject();
      }}
      onblur={confirmRenameProject}
    />
  {:else}
    <button
      class="inline-btn breadcrumb-active"
      ondblclick={isAdmin ? startRenameProject : undefined}
      title={isAdmin ? t('project.doubleClickRename') : undefined}>{project.name}</button
    >
  {/if}
  {#if isAdmin}
    <button class="btn btn-primary btn-sm project-new-crawl" onclick={() => onnewcrawl?.()}>
      <svg
        viewBox="0 0 24 24"
        width="16"
        height="16"
        fill="none"
        stroke="currentColor"
        stroke-width="2"
        ><line x1="12" y1="5" x2="12" y2="19" /><line x1="5" y1="12" x2="19" y2="12" /></svg
      >
      {t('sessions.newCrawl')}
    </button>
  {/if}
</div>

<div class="tab-bar">
  <button
    class="tab"
    class:tab-active={projectTab === 'sessions'}
    onclick={() => switchProjectTab('sessions')}>{t('project.sessions')}</button
  >
  <button
    class="tab"
    class:tab-active={projectTab === 'gsc'}
    onclick={() => switchProjectTab('gsc')}>{t('project.searchConsole')}</button
  >
  <button
    class="tab"
    class:tab-active={projectTab === 'delta'}
    onclick={() => switchProjectTab('delta')}>Daily Delta</button
  >
  <button
    class="tab"
    class:tab-active={projectTab === 'quality'}
    onclick={() => switchProjectTab('quality')}>Quality</button
  >
  {#each providerConnections as conn}
    {@const meta = providerMeta[conn.provider]}
    <button
      class="tab"
      class:tab-active={projectTab === 'provider:' + conn.provider}
      onclick={() => switchProjectTab('provider:' + conn.provider)}
      >{#if meta?.icon}<img
          src={meta.icon}
          alt=""
          style="width:16px;height:16px;vertical-align:-3px;margin-right:4px"
        />{/if}{meta?.label || conn.provider} Data</button
    >
  {/each}
  {#if isAdmin && providerConnections.length === 0}
    <button
      class="tab"
      class:tab-active={projectTab === 'providers'}
      onclick={() => switchProjectTab('providers')}>{t('project.seoData')}</button
    >
  {/if}
</div>

<div class="card card-flush card-tab-body">
  {#if projectTab === 'sessions'}
    {#if projSessions.length > 0}
      <table>
        <thead>
          <tr>
            <th>{t('project.seedUrl')}</th>
            <th>Type</th>
            <th>{t('common.status')}</th>
            <th>Quality</th>
            <th>{t('common.pages')}</th>
            <th>{t('actionBar.started')}</th>
            <th style="width:1%"></th>
          </tr>
        </thead>
        <tbody>
          {#each projSessions as s}
            <tr class="clickable-row" onclick={() => onselectsession?.(s)}>
              <td class="cell-url">{s.SeedURLs?.[0] || s.ID}</td>
              <td>
                <span
                  class="badge"
                  class:badge-info={sessionTypeLabel(s) === 'Daily Delta'}
                  title={sessionTypeLabel(s) === 'Daily Delta'
                    ? 'Checks changed, new, stale, problem, and manually queued candidates. It is not a full site baseline.'
                    : 'Full crawl session.'}>{sessionTypeLabel(s)}</span
                >
              </td>
              <td>
                {#if s.is_running}
                  <span class="badge badge-info">{t('common.running')}</span>
                {:else if s.Status === 'completed'}
                  <span class="badge badge-success">{t('common.completed')}</span>
                {:else if s.Status === 'failed' || s.Status === 'crashed'}
                  <span class="badge badge-error">{s.Status}</span>
                {:else}
                  <span class="badge">{s.Status || t('common.unknown')}</span>
                {/if}
              </td>
              <td>
                {#if s.quality}
                  <button
                    type="button"
                    class="badge"
                    class:badge-success={s.quality.status === 'trusted'}
                    class:badge-warning={s.quality.status === 'warning'}
                    class:badge-error={s.quality.status === 'untrusted'}
                    class:badge-info={s.quality.is_full_crawl === false}
                    title={s.quality.summary}
                    onclick={(e) => {
                      e.stopPropagation();
                      openQualityDetail(s);
                    }}>{qualityBadgeLabel(s.quality)}</button
                  >
                {:else}
                  <span class="badge">pending</span>
                {/if}
              </td>
              <td>{fmtN(s.PagesCrawled || 0)}</td>
              <td class="nowrap text-muted text-sm">{s.StartedAt ? timeAgo(s.StartedAt) : '-'}</td>
              <td onclick={(e) => e.stopPropagation()}>
                {#if isAdmin}
                  <button
                    class="btn-ghost btn-unlink"
                    title={t('session.disassociate')}
                    onclick={() =>
                      showConfirm(t('session.disassociateConfirm'), async () => {
                        try {
                          await disassociateSession(project.id, s.ID);
                          loadProjectSessions();
                        } catch (e) {
                          onerror?.(e.message);
                        }
                      })}
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
                      ><line x1="18" y1="6" x2="6" y2="18" /><line
                        x1="6"
                        y1="6"
                        x2="18"
                        y2="18"
                      /></svg
                    >
                  </button>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
      {#if projSessionsTotal > PROJ_SESSIONS_LIMIT}
        <div class="pagination-controls">
          <button
            class="btn btn-sm"
            onclick={() => {
              projSessionsOffset = Math.max(0, projSessionsOffset - PROJ_SESSIONS_LIMIT);
              loadProjectSessions();
            }}
            disabled={projSessionsOffset === 0}>{t('common.previous')}</button
          >
          <span class="text-sm text-muted"
            >{projSessionsOffset + 1}-{Math.min(
              projSessionsOffset + PROJ_SESSIONS_LIMIT,
              projSessionsTotal,
            )}
            {t('common.of')}
            {projSessionsTotal}</span
          >
          <button
            class="btn btn-sm"
            onclick={() => {
              projSessionsOffset += PROJ_SESSIONS_LIMIT;
              loadProjectSessions();
            }}
            disabled={projSessionsOffset + PROJ_SESSIONS_LIMIT >= projSessionsTotal}
            >{t('common.next')}</button
          >
        </div>
      {/if}
    {:else}
      <div class="empty-state">
        <p>{t('project.noSessions')}</p>
        {#if isAdmin}
          <button class="btn btn-primary mt-md" onclick={() => onnewcrawl?.()}>
            <svg
              viewBox="0 0 24 24"
              width="16"
              height="16"
              fill="none"
              stroke="currentColor"
              stroke-width="2"
              ><line x1="12" y1="5" x2="12" y2="19" /><line x1="5" y1="12" x2="19" y2="12" /></svg
            >
            {t('sessions.newCrawl')}
          </button>
        {/if}
      </div>
    {/if}
  {:else if projectTab === 'gsc'}
    <GSCTab
      projectId={project.id}
      initialSubView={gscSubView}
      onerror={(msg) => onerror?.(msg)}
      onpushurl={(u) => onpushurl?.(u)}
      {isAdmin}
    />
  {:else if projectTab === 'delta'}
    <DeltaCrawlTab
      projectId={project.id}
      onerror={(msg) => onerror?.(msg)}
      onopensessions={async () => {
        await loadProjectSessions();
        switchProjectTab('sessions');
      }}
      {isAdmin}
    />
  {:else if projectTab === 'quality'}
    <QualitySettingsTab projectId={project.id} {isAdmin} onerror={(msg) => onerror?.(msg)} />
  {:else if projectTab.startsWith('provider:')}
    <ProvidersTab
      projectId={project.id}
      provider={projectTab.replace('provider:', '')}
      initialSubView={providerSubView}
      onerror={(msg) => onerror?.(msg)}
      onpushurl={(u) => onpushurl?.(u)}
      {isAdmin}
    />
  {:else if projectTab === 'providers' && isAdmin}
    <ProvidersTab
      projectId={project.id}
      initialSubView={providerSubView}
      onerror={(msg) => onerror?.(msg)}
      onpushurl={(u) => onpushurl?.(u)}
      {isAdmin}
    />
  {/if}
</div>

{#if isAdmin && projectTab === 'sessions'}
  <details class="danger-zone">
    <summary>{t('project.dangerZone')}</summary>
    <div class="danger-zone-item">
      <div class="danger-zone-text">
        <strong>{t('project.deleteProject')}</strong>
        <p>{t('project.deleteProjectDesc')}</p>
      </div>
      <button class="btn btn-danger" onclick={handleDeleteProject}>
        <svg
          viewBox="0 0 24 24"
          width="14"
          height="14"
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          stroke-linecap="round"
          stroke-linejoin="round"
          ><polyline points="3 6 5 6 21 6" /><path
            d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"
          /></svg
        >
        {t('project.deleteProject')}
      </button>
    </div>
    <div class="danger-zone-item">
      <div class="danger-zone-text">
        <strong>{t('project.deleteProjectWithSessions')}</strong>
        <p>{t('project.deleteProjectWithSessionsDesc')}</p>
      </div>
      <button class="btn btn-danger" onclick={handleDeleteProjectWithSessions}>
        <svg
          viewBox="0 0 24 24"
          width="14"
          height="14"
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          stroke-linecap="round"
          stroke-linejoin="round"
          ><polyline points="3 6 5 6 21 6" /><path
            d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"
          /></svg
        >
        {t('project.deleteProjectWithSessions')}
      </button>
    </div>
  </details>
{/if}

{#if qualityDetailSession}
  <div
    class="quality-modal-overlay"
    role="button"
    tabindex="0"
    onclick={() => {
      qualityDetailSession = null;
      qualityDetail = null;
    }}
    onkeydown={(e) => {
      if (e.key === 'Escape') {
        qualityDetailSession = null;
        qualityDetail = null;
      }
    }}
  >
    <div class="quality-modal" role="dialog" onclick={(e) => e.stopPropagation()}>
      <div class="quality-modal-header">
        <div>
          <div class="eyebrow">Crawl Quality</div>
          <h2>{sessionTypeLabel(qualityDetailSession)} · {qualityDetailSession.SeedURLs?.[0]}</h2>
        </div>
        <button
          class="btn btn-sm"
          onclick={() => {
            qualityDetailSession = null;
            qualityDetail = null;
          }}>Close</button
        >
      </div>
      {#if qualityDetailLoading}
        <p class="text-muted">Loading quality details...</p>
      {:else if qualityDetail}
        <div class="quality-summary-grid">
          <div>
            <span>Status</span>
            <strong>{qualityBadgeLabel(qualityDetail)}</strong>
          </div>
          <div>
            <span>Trusted</span>
            <strong>{qualityDetail.trusted ? 'Yes' : 'No'}</strong>
          </div>
          <div>
            <span>Baseline</span>
            <strong>{qualityDetail.baseline_session_id || '-'}</strong>
          </div>
        </div>
        <p class="quality-summary-text">{qualityDetail.summary}</p>

        {#if qualityDetail.metrics}
          <div class="quality-metrics">
            {#each Object.entries(qualityDetail.metrics) as [key, value]}
              <div>
                <span>{key.replaceAll('_', ' ')}</span>
                <strong>{fmtN(value)}</strong>
              </div>
            {/each}
          </div>
        {/if}

        <h3>Findings</h3>
        {#if qualityDetail.findings?.length}
          <div class="quality-findings">
            {#each qualityDetail.findings as f}
              <div class="quality-finding" class:blocking={f.blocking}>
                <div>
                  <strong>{f.finding_type}</strong>
                  <p>{f.message}</p>
                </div>
                <span class="badge" class:badge-error={f.severity === 'error'} class:badge-warning={f.severity === 'warning'}>{f.severity}</span>
              </div>
            {/each}
          </div>
        {:else}
          <p class="text-muted">No findings recorded.</p>
        {/if}
      {/if}
    </div>
  </div>
{/if}

{#if confirmState}
  <ConfirmModal
    message={confirmState.message}
    danger={confirmState.danger}
    confirmLabel={confirmState.confirmLabel}
    onconfirm={() => {
      confirmState.onConfirm();
      confirmState = null;
    }}
    oncancel={() => (confirmState = null)}
  />
{/if}

<style>
  .breadcrumb-active {
    color: var(--text);
  }
  .project-new-crawl {
    margin-left: auto;
  }
  .card-tab-body {
    border-top-left-radius: 0;
    border-top-right-radius: 0;
    border-top: none;
  }
  .pagination-controls {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 12px;
    padding: 12px 0;
  }
  .danger-zone {
    margin-top: 32px;
    border: 1px solid var(--border);
    border-radius: 8px;
  }
  .danger-zone summary {
    padding: 12px 16px;
    font-size: 13px;
    font-weight: 600;
    color: var(--text-muted);
    cursor: pointer;
    list-style: none;
  }
  .danger-zone summary::-webkit-details-marker {
    display: none;
  }
  .danger-zone[open] summary {
    color: #dc2626;
    border-bottom: 1px solid var(--border);
  }
  .danger-zone-item {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    padding: 16px;
  }
  .danger-zone-item + .danger-zone-item {
    border-top: 1px solid var(--border);
  }
  .danger-zone-text p {
    margin: 4px 0 0;
    font-size: 13px;
    color: var(--text-muted);
  }
  .danger-zone-text strong {
    font-size: 13px;
  }
  .btn-unlink {
    padding: 4px;
    color: var(--text-muted);
    cursor: pointer;
  }
  .btn-unlink:hover {
    color: #dc2626;
  }
  .badge {
    border: 0;
  }
  button.badge {
    cursor: pointer;
    font: inherit;
  }
  .quality-modal-overlay {
    position: fixed;
    inset: 0;
    z-index: 1000;
    background: rgba(15, 23, 42, 0.55);
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 24px;
  }
  .quality-modal {
    width: min(900px, 100%);
    max-height: 86vh;
    overflow: auto;
    background: var(--bg-card);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 20px;
    box-shadow: 0 24px 80px rgba(15, 23, 42, 0.3);
  }
  .quality-modal-header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 16px;
    margin-bottom: 18px;
  }
  .quality-modal-header h2 {
    margin: 3px 0 0;
    font-size: 18px;
    word-break: break-word;
  }
  .eyebrow {
    color: var(--accent);
    font-size: 12px;
    font-weight: 700;
    text-transform: uppercase;
  }
  .quality-summary-grid,
  .quality-metrics {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
    gap: 10px;
    margin-bottom: 14px;
  }
  .quality-summary-grid > div,
  .quality-metrics > div {
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 12px;
  }
  .quality-summary-grid span,
  .quality-metrics span {
    display: block;
    color: var(--text-muted);
    font-size: 12px;
    text-transform: capitalize;
  }
  .quality-summary-grid strong,
  .quality-metrics strong {
    display: block;
    margin-top: 4px;
    font-size: 16px;
  }
  .quality-summary-text {
    color: var(--text);
    margin: 0 0 18px;
  }
  .quality-findings {
    display: grid;
    gap: 8px;
  }
  .quality-finding {
    display: flex;
    justify-content: space-between;
    gap: 12px;
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 12px;
  }
  .quality-finding.blocking {
    border-color: rgba(220, 38, 38, 0.45);
  }
  .quality-finding p {
    margin: 4px 0 0;
    color: var(--text-muted);
    font-size: 13px;
  }
  .btn-danger {
    background: var(--bg-card);
    color: #dc2626;
    border: 1px solid var(--border);
    padding: 6px 14px;
    border-radius: 6px;
    cursor: pointer;
    font-size: 13px;
    font-weight: 500;
    display: flex;
    align-items: center;
    gap: 6px;
    white-space: nowrap;
    flex-shrink: 0;
  }
  .btn-danger:hover {
    background: #dc2626;
    color: #fff;
    border-color: #dc2626;
  }
</style>
