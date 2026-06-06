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
  <section class="login-product" aria-label="{appName} overview">
    <div class="brand-lockup">
      <div class="brand-mark" aria-hidden="true">
        <span></span>
        <span></span>
        <span></span>
      </div>
      <div>
        <div class="brand-name">{appName}</div>
        <div class="brand-subtitle">Crawl intelligence workspace</div>
      </div>
    </div>

    <div class="product-copy">
      <p class="product-kicker">Secure console</p>
      <h1>Monitor crawls, projects, and API access from one controlled workspace.</h1>
      <p>
        Sign in to review crawl sessions, project data, rendering status, Search Console signals,
        and scoped user access.
      </p>
    </div>

    <div class="signal-board" aria-hidden="true">
      <div class="signal-row">
        <span>Health</span>
        <strong>Online</strong>
      </div>
      <div class="signal-row">
        <span>Render pool</span>
        <strong>Ready</strong>
      </div>
      <div class="signal-row">
        <span>API access</span>
        <strong>Scoped</strong>
      </div>
      <div class="signal-meter">
        <span style="width: 72%"></span>
      </div>
    </div>
  </section>

  <section class="login-card" aria-labelledby="login-title">
    <div class="login-card-header">
      <p>Account access</p>
      <h2 id="login-title">Sign in to continue</h2>
    </div>

    <form
      class="login-form"
      onsubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      <div class="field">
        <label for="login-username">Username</label>
        <input
          id="login-username"
          autocomplete="username"
          bind:value={username}
          aria-invalid={error ? 'true' : 'false'}
        />
      </div>

      <div class="field">
        <label for="login-password">Password</label>
        <input
          id="login-password"
          type="password"
          autocomplete="current-password"
          bind:value={password}
          aria-invalid={error ? 'true' : 'false'}
        />
      </div>

      {#if error}
        <div class="login-error" role="alert">{error}</div>
      {/if}

      <button class="btn btn-primary login-submit" disabled={loading || !username.trim() || !password}>
        <span>{loading ? 'Signing in...' : 'Sign in'}</span>
        <span class="submit-arrow" aria-hidden="true">-&gt;</span>
      </button>
    </form>

    <div class="login-note">
      Access is managed by your CrawlObserver administrator. Project-scoped accounts only see
      assigned projects.
    </div>
  </section>
</main>

<style>
  .login-shell {
    min-height: 100vh;
    display: grid;
    grid-template-columns: minmax(0, 1.05fr) minmax(360px, 480px);
    align-items: stretch;
    background:
      linear-gradient(135deg, color-mix(in srgb, var(--bg) 92%, #0f766e) 0%, var(--bg) 54%),
      var(--bg);
    color: var(--text);
    padding: 24px;
    gap: 24px;
  }

  .login-product {
    min-height: calc(100vh - 48px);
    display: grid;
    align-content: space-between;
    gap: 32px;
    padding: clamp(28px, 5vw, 56px);
    border: 1px solid var(--border);
    border-radius: 8px;
    background:
      linear-gradient(180deg, color-mix(in srgb, var(--bg-card) 96%, #0f766e) 0%, var(--bg-card) 100%),
      var(--bg-card);
    box-shadow: var(--shadow-md);
    overflow: hidden;
    position: relative;
  }

  .login-product::after {
    content: '';
    position: absolute;
    inset: auto -20% -1px 18%;
    height: 42%;
    background:
      linear-gradient(90deg, transparent 0 8%, color-mix(in srgb, var(--accent) 18%, transparent) 8% 9%, transparent 9% 100%),
      linear-gradient(0deg, transparent 0 14%, color-mix(in srgb, #0f766e 18%, transparent) 14% 15%, transparent 15% 100%);
    background-size:
      68px 68px,
      68px 68px;
    opacity: 0.42;
    pointer-events: none;
  }

  .brand-lockup {
    display: flex;
    align-items: center;
    gap: 14px;
    position: relative;
    z-index: 1;
  }

  .brand-mark {
    width: 44px;
    height: 44px;
    border-radius: 8px;
    display: grid;
    place-items: center;
    background: var(--accent);
    box-shadow: 0 16px 34px color-mix(in srgb, var(--accent) 24%, transparent);
    position: relative;
  }

  .brand-mark span {
    position: absolute;
    width: 22px;
    height: 3px;
    border-radius: 999px;
    background: var(--accent-text);
  }

  .brand-mark span:nth-child(1) {
    transform: translateY(-8px);
  }

  .brand-mark span:nth-child(2) {
    width: 28px;
  }

  .brand-mark span:nth-child(3) {
    transform: translateY(8px);
    width: 16px;
  }

  .brand-name {
    font-family:
      'Nunito Sans',
      'Inter',
      system-ui,
      sans-serif;
    font-size: 18px;
    font-weight: 700;
    line-height: 1.15;
  }

  .brand-subtitle {
    margin-top: 2px;
    color: var(--text-muted);
    font-size: 13px;
  }

  .product-copy {
    max-width: 660px;
    position: relative;
    z-index: 1;
  }

  .product-kicker {
    color: var(--accent);
    font-size: 12px;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    margin-bottom: 12px;
  }

  .product-copy h1 {
    font-size: clamp(34px, 5vw, 58px);
    line-height: 1.03;
    max-width: 760px;
    margin: 0;
    letter-spacing: 0;
  }

  .product-copy p:not(.product-kicker) {
    margin-top: 18px;
    max-width: 580px;
    color: var(--text-secondary);
    font-size: 16px;
    line-height: 1.65;
  }

  .signal-board {
    width: min(430px, 100%);
    position: relative;
    z-index: 1;
    display: grid;
    gap: 1px;
    border: 1px solid var(--border);
    border-radius: 8px;
    overflow: hidden;
    background: var(--border);
    box-shadow: var(--shadow);
  }

  .signal-row {
    display: flex;
    justify-content: space-between;
    gap: 16px;
    padding: 13px 15px;
    background: color-mix(in srgb, var(--bg-card) 96%, var(--bg));
    font-size: 13px;
  }

  .signal-row span {
    color: var(--text-muted);
  }

  .signal-row strong {
    font-weight: 700;
    color: var(--text);
  }

  .signal-meter {
    height: 7px;
    background: var(--bg-hover);
  }

  .signal-meter span {
    display: block;
    height: 100%;
    background: linear-gradient(90deg, #0f766e, var(--accent));
  }

  .login-card {
    min-height: calc(100vh - 48px);
    display: grid;
    align-content: center;
    gap: 24px;
    padding: clamp(24px, 5vw, 52px);
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--bg-card);
    box-shadow: var(--shadow-md);
  }

  .login-card-header p {
    color: var(--accent);
    font-size: 12px;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    margin-bottom: 8px;
  }

  .login-card-header h2 {
    font-size: 28px;
    line-height: 1.15;
    letter-spacing: 0;
  }

  .login-form {
    display: grid;
    gap: 16px;
  }

  .field {
    display: grid;
    gap: 8px;
  }

  label {
    font-size: 13px;
    font-weight: 600;
    color: var(--text-secondary);
  }

  input {
    width: 100%;
    height: 44px;
    padding: 0 13px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--bg-input);
    color: var(--text);
    font: inherit;
    transition:
      border-color 0.15s,
      box-shadow 0.15s,
      background 0.15s;
  }

  input:focus {
    outline: none;
    border-color: var(--accent);
    box-shadow: 0 0 0 3px var(--accent-light);
  }

  input[aria-invalid='true'] {
    border-color: var(--error);
  }

  .login-error {
    padding: 10px 12px;
    border: 1px solid color-mix(in srgb, var(--error) 28%, transparent);
    border-radius: var(--radius-sm);
    background: var(--error-bg);
    color: var(--error);
    font-size: 13px;
  }

  .login-submit {
    width: 100%;
    min-height: 44px;
    justify-content: center;
    margin-top: 2px;
  }

  .submit-arrow {
    font-weight: 700;
  }

  .login-note {
    border-top: 1px solid var(--border-light);
    padding-top: 18px;
    color: var(--text-muted);
    font-size: 13px;
    line-height: 1.55;
  }

  @media (max-width: 920px) {
    .login-shell {
      grid-template-columns: 1fr;
      padding: 16px;
    }

    .login-product,
    .login-card {
      min-height: auto;
    }

    .product-copy h1 {
      font-size: 32px;
      max-width: 560px;
    }

    .login-card {
      align-content: start;
    }
  }

  @media (max-width: 560px) {
    .login-shell {
      padding: 0;
      gap: 0;
      background: var(--bg);
    }

    .login-product,
    .login-card {
      border-radius: 0;
      border-left: none;
      border-right: none;
      box-shadow: none;
    }

    .login-product {
      padding: 24px 20px;
    }

    .login-card {
      padding: 28px 20px 32px;
      border-top: none;
    }

    .product-copy p:not(.product-kicker),
    .signal-board {
      display: none;
    }

    .product-copy h1 {
      font-size: 26px;
      line-height: 1.08;
    }
  }
</style>
