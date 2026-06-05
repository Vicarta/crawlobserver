<script>
  import { login } from '../api.js';

  let { appName = 'CrawlObserver', onlogin } = $props();

  let username = $state('');
  let password = $state('');
  let error = $state('');
  let loading = $state(false);

  async function submit() {
    if (!username.trim() || !password || loading) return;
    loading = true;
    error = '';
    try {
      const user = await login(username.trim(), password);
      onlogin?.(user);
    } catch (e) {
      error = e.message;
    } finally {
      loading = false;
    }
  }
</script>

<main class="login-shell">
  <form
    class="login-panel"
    onsubmit={(e) => {
      e.preventDefault();
      submit();
    }}
  >
    <div class="login-brand">{appName}</div>
    <label>
      Username
      <input autocomplete="username" bind:value={username} autofocus />
    </label>
    <label>
      Password
      <input type="password" autocomplete="current-password" bind:value={password} />
    </label>
    {#if error}
      <div class="login-error">{error}</div>
    {/if}
    <button class="btn btn-primary" disabled={loading || !username.trim() || !password}>
      {loading ? 'Signing in...' : 'Sign in'}
    </button>
  </form>
</main>

<style>
  .login-shell {
    min-height: 100vh;
    display: grid;
    place-items: center;
    background: var(--bg);
    color: var(--text);
    padding: 24px;
  }

  .login-panel {
    width: min(360px, 100%);
    display: grid;
    gap: 14px;
    padding: 22px;
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    background: var(--bg-card);
    box-shadow: var(--shadow-sm);
  }

  .login-brand {
    font-size: 20px;
    font-weight: 700;
    margin-bottom: 4px;
  }

  label {
    display: grid;
    gap: 6px;
    font-size: 13px;
    color: var(--text-muted);
  }

  input {
    width: 100%;
    box-sizing: border-box;
    padding: 9px 10px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--bg);
    color: var(--text);
    font: inherit;
  }

  input:focus {
    outline: none;
    border-color: var(--accent);
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent) 18%, transparent);
  }

  .login-error {
    color: var(--danger);
    font-size: 13px;
  }
</style>
