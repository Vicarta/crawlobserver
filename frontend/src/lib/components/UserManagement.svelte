<script>
  import {
    getProjects,
    getUsers,
    createUser,
    updateUser,
    deleteUser,
    sendUserInvitation,
  } from '../api.js';
  import { t } from '../i18n/index.svelte.js';
  import ConfirmModal from './ConfirmModal.svelte';
  import SearchSelect from './SearchSelect.svelte';

  let { onerror, onprojectschanged } = $props();

  let confirmState = $state(null);
  let projects = $state([]);
  let users = $state([]);
  let emailDrafts = $state({});
  let newUser = $state({
    email: '',
    role: 'viewer',
    project_ids: [],
  });

  let adminCount = $derived(users.filter((u) => u.role === 'admin').length);

  function showConfirm(message, onConfirm, opts = {}) {
    confirmState = { message, onConfirm, ...opts };
  }

  function isLastAdmin(user) {
    return user.role === 'admin' && adminCount <= 1;
  }

  async function loadData() {
    try {
      projects = await getProjects();
      users = await getUsers();
      emailDrafts = Object.fromEntries(users.map((user) => [user.id, user.email || '']));
      onprojectschanged?.(projects);
    } catch (e) {
      onerror?.(e.message);
    }
  }

  function setEmailDraft(userId, email) {
    emailDrafts = { ...emailDrafts, [userId]: email };
  }

  function invitationStatus(user) {
    if (user.email_verified) return 'Verified';
    if (user.invitation_status) return user.invitation_status;
    if (user.invite_status) return user.invite_status;
    return user.email ? 'Invite pending' : 'Email required';
  }

  function toggleNewUserProject(projectId) {
    const ids = new Set(newUser.project_ids || []);
    if (ids.has(projectId)) ids.delete(projectId);
    else ids.add(projectId);
    newUser = { ...newUser, project_ids: [...ids] };
  }

  async function handleCreateUser() {
    if (!newUser.email.trim()) return;
    try {
      const created = await createUser({
        email: newUser.email.trim(),
        role: newUser.role,
        project_ids: newUser.role === 'viewer' ? newUser.project_ids : [],
      });
      newUser = { email: '', role: 'viewer', project_ids: [] };
      try {
        await sendUserInvitation(created.id);
      } catch (e) {
        await loadData();
        onerror?.(e.message);
        return;
      }
      await loadData();
    } catch (e) {
      onerror?.(e.message);
    }
  }

  async function handleUpdateUser(user, patch) {
    if (isLastAdmin(user) && patch.role && patch.role !== 'admin') return false;
    try {
      await updateUser(user.id, {
        email: patch.email ?? user.email ?? '',
        role: patch.role ?? user.role,
        active: patch.active ?? user.active,
        project_ids: patch.project_ids ?? user.project_ids ?? [],
      });
      await loadData();
      return true;
    } catch (e) {
      onerror?.(e.message);
      return false;
    }
  }

  async function handleSaveAndInvite(user) {
    const email = (emailDrafts[user.id] ?? '').trim();
    if (!email) return;
    if (!(await handleUpdateUser(user, { email }))) return;
    try {
      await sendUserInvitation(user.id);
      await loadData();
    } catch (e) {
      onerror?.(e.message);
    }
  }

  function handleDeleteUser(user) {
    if (isLastAdmin(user)) return;
    showConfirm(
      `Delete user "${user.email || user.id}"?`,
      async () => {
        try {
          await deleteUser(user.id);
          await loadData();
        } catch (e) {
          onerror?.(e.message);
        }
      },
      { danger: true, confirmLabel: t('common.delete') },
    );
  }

  loadData();
</script>

<div class="page-header section-gap">
  <h1>Users</h1>
</div>
<p class="text-sm text-muted mb-md user-subtitle">
  Invite email users and scope viewer access to specific projects.
</p>

<div class="card mb-md">
  <div class="form-grid">
    <div class="form-group">
      <label for="user-email">Email address</label>
      <input
        id="user-email"
        type="email"
        autocomplete="email"
        bind:value={newUser.email}
        placeholder="client@example.com"
      />
    </div>
    <div class="form-group">
      <label for="user-role">Role</label>
      <SearchSelect
        id="user-role"
        bind:value={newUser.role}
        options={[
          { value: 'viewer', label: 'Viewer' },
          { value: 'admin', label: 'Admin' },
        ]}
      />
    </div>
  </div>
  {#if newUser.role === 'viewer'}
    <div class="form-group mt-md">
      <div class="form-label">Projects</div>
      <div class="project-checkbox-list">
        {#each projects as p}
          <label class="project-checkbox-item">
            <input
              type="checkbox"
              checked={(newUser.project_ids || []).includes(p.id)}
              onchange={() => toggleNewUserProject(p.id)}
            />
            <span>{p.name}</span>
          </label>
        {/each}
      </div>
    </div>
  {/if}
  <div class="mt-md">
    <button
      class="btn btn-primary"
      onclick={handleCreateUser}
      disabled={!newUser.email.trim()}>Create and send invitation</button
    >
  </div>
</div>

{#if users.length === 0}
  <div class="card text-center text-muted empty-state">No invited users yet.</div>
{:else}
  <div class="card card-flush mb-lg">
    {#each users as u}
      {@const lastAdmin = isLastAdmin(u)}
      <div class="user-row">
        <div class="user-info">
          {#if !u.email}
            <div class="legacy-identifier">
              <span>Legacy identifier</span>
              <code>{u.username || u.id}</code>
            </div>
          {/if}
          <div class="user-email">
            <label class="sr-only" for={`user-email-${u.id}`}>Email address</label>
            <input
              id={`user-email-${u.id}`}
              class="user-email-input"
              type="email"
              autocomplete="email"
              value={emailDrafts[u.id] ?? ''}
              placeholder="user@example.com"
              oninput={(event) => setEmailDraft(u.id, event.currentTarget.value)}
            />
          </div>
          <div class="user-meta">
            <span class="badge" class:badge-info={u.role === 'admin'}>{u.role}</span>
            <span class:status-disabled={!u.active}>{u.active ? 'Active' : 'Disabled'}</span>
            <span>{invitationStatus(u)}</span>
            {#if lastAdmin}
              <span>Last administrator</span>
            {/if}
            {#if u.role === 'viewer'}
              <span>
                {(u.project_ids || [])
                  .map((id) => projects.find((p) => p.id === id)?.name || id)
                  .join(', ') || 'No projects'}
              </span>
            {/if}
          </div>
          {#if u.role === 'viewer'}
            <div class="project-checkbox-list project-checkbox-list-sm">
              {#each projects as p}
                <label class="project-checkbox-item">
                  <input
                    type="checkbox"
                    checked={(u.project_ids || []).includes(p.id)}
                    onchange={() => {
                      const ids = new Set(u.project_ids || []);
                      if (ids.has(p.id)) ids.delete(p.id);
                      else ids.add(p.id);
                      handleUpdateUser(u, { project_ids: [...ids] });
                    }}
                  />
                  <span>{p.name}</span>
                </label>
              {/each}
            </div>
          {/if}
        </div>
        <div class="user-actions">
          <button
            class="btn btn-sm"
            disabled={lastAdmin}
            title={lastAdmin ? 'Create another admin before changing this role.' : ''}
            onclick={() =>
              handleUpdateUser(u, {
                role: u.role === 'admin' ? 'viewer' : 'admin',
                project_ids: u.role === 'admin' ? [] : u.project_ids || [],
              })}
          >
            Make {u.role === 'admin' ? 'viewer' : 'admin'}
          </button>
          <button class="btn btn-sm" onclick={() => handleUpdateUser(u, { active: !u.active })}>
            {u.active ? 'Disable' : 'Enable'}
          </button>
          <button
            class="btn btn-sm"
            onclick={() => handleSaveAndInvite(u)}
            disabled={!(emailDrafts[u.id] ?? '').trim()}
          >
            {u.email_verified ? 'Resend invitation' : 'Save and send invitation'}
          </button>
          <button
            class="btn btn-sm btn-danger"
            disabled={lastAdmin}
            title={lastAdmin ? 'Create another admin before deleting this user.' : ''}
            onclick={() => handleDeleteUser(u)}>{t('common.delete')}</button
          >
        </div>
      </div>
    {/each}
  </div>
{/if}

{#if confirmState}<ConfirmModal
    message={confirmState.message}
    danger={confirmState.danger}
    confirmLabel={confirmState.confirmLabel}
    onconfirm={() => {
      confirmState.onConfirm();
      confirmState = null;
    }}
    oncancel={() => (confirmState = null)}
  />{/if}

<style>
  .section-gap {
    margin-top: 32px;
  }

  .user-subtitle {
    max-width: 720px;
  }

  .form-label {
    display: block;
    margin-bottom: 6px;
    font-size: 13px;
    font-weight: 500;
    color: var(--text);
  }

  .project-checkbox-list {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
    gap: 8px;
    max-height: 180px;
    overflow: auto;
    padding: 10px;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--bg-secondary);
  }

  .project-checkbox-item {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
    color: var(--text);
  }

  .project-checkbox-list-sm {
    grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
    max-height: 120px;
    margin-top: 8px;
    padding: 8px;
  }

  .status-disabled {
    color: var(--danger);
  }

  .user-row {
    display: flex;
    justify-content: space-between;
    gap: 14px;
    padding: 14px 16px;
    border-bottom: 1px solid var(--border);
    transition: background 0.15s ease;
  }

  .user-row:last-child {
    border-bottom: none;
  }

  .user-row:hover {
    background: var(--bg-secondary);
  }

  .user-info {
    min-width: 0;
    flex: 1;
  }

  .user-email {
    font-weight: 600;
    color: var(--text);
    margin-bottom: 6px;
  }

  .legacy-identifier {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin-bottom: 6px;
    color: var(--text-muted);
    font-size: 12px;
  }

  .legacy-identifier code {
    color: var(--text-secondary);
    font: inherit;
    overflow-wrap: anywhere;
  }

  .user-email-input {
    width: min(360px, 100%);
    padding: 7px 9px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--bg-input);
    color: var(--text);
    font: inherit;
  }

  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    margin: -1px;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    white-space: nowrap;
    border: 0;
  }

  .user-meta {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    font-size: 12px;
    color: var(--text-muted);
  }

  .user-actions {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-shrink: 0;
  }

  @media (max-width: 760px) {
    .user-row {
      flex-direction: column;
    }

    .user-actions {
      flex-wrap: wrap;
    }
  }
</style>
