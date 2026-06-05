<script>
  import { getProjects, getUsers, createUser, updateUser, deleteUser } from '../api.js';
  import { t } from '../i18n/index.svelte.js';
  import ConfirmModal from './ConfirmModal.svelte';
  import SearchSelect from './SearchSelect.svelte';

  let { onerror, onprojectschanged } = $props();

  let confirmState = $state(null);
  let projects = $state([]);
  let users = $state([]);
  let newUser = $state({
    username: '',
    password: '',
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
      onprojectschanged?.(projects);
    } catch (e) {
      onerror?.(e.message);
    }
  }

  function toggleNewUserProject(projectId) {
    const ids = new Set(newUser.project_ids || []);
    if (ids.has(projectId)) ids.delete(projectId);
    else ids.add(projectId);
    newUser = { ...newUser, project_ids: [...ids] };
  }

  async function handleCreateUser() {
    if (!newUser.username.trim() || !newUser.password) return;
    try {
      await createUser({
        username: newUser.username.trim(),
        password: newUser.password,
        role: newUser.role,
        project_ids: newUser.role === 'viewer' ? newUser.project_ids : [],
      });
      newUser = { username: '', password: '', role: 'viewer', project_ids: [] };
      await loadData();
    } catch (e) {
      onerror?.(e.message);
    }
  }

  async function handleUpdateUser(user, patch) {
    if (isLastAdmin(user) && patch.role && patch.role !== 'admin') return;
    try {
      await updateUser(user.id, {
        username: user.username,
        role: patch.role ?? user.role,
        active: patch.active ?? user.active,
        project_ids: patch.project_ids ?? user.project_ids ?? [],
      });
      await loadData();
    } catch (e) {
      onerror?.(e.message);
    }
  }

  function handleDeleteUser(user) {
    if (isLastAdmin(user)) return;
    showConfirm(
      `Delete user "${user.username}"?`,
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
  Create browser users and scope viewer access to specific projects.
</p>

<div class="card mb-md">
  <div class="form-grid">
    <div class="form-group">
      <label for="user-name">Username</label>
      <input id="user-name" type="text" bind:value={newUser.username} placeholder="client" />
    </div>
    <div class="form-group">
      <label for="user-password">Password</label>
      <input id="user-password" type="password" bind:value={newUser.password} />
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
      disabled={!newUser.username.trim() || !newUser.password}>Create user</button
    >
  </div>
</div>

{#if users.length === 0}
  <div class="card text-center text-muted empty-state">No local users yet.</div>
{:else}
  <div class="card card-flush mb-lg">
    {#each users as u}
      {@const lastAdmin = isLastAdmin(u)}
      <div class="user-row">
        <div class="user-info">
          <div class="user-name">{u.username}</div>
          <div class="user-meta">
            <span class="badge" class:badge-info={u.role === 'admin'}>{u.role}</span>
            <span class:status-disabled={!u.active}>{u.active ? 'Active' : 'Disabled'}</span>
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

  .user-name {
    font-weight: 600;
    color: var(--text);
    margin-bottom: 6px;
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
