<script>
  import { t } from '../i18n/index.svelte.js';

  let { onclick, disabled = false } = $props();
</script>

<button
  type="button"
  class="btn btn-sm export-selected-btn"
  {onclick}
  {disabled}
  title={t('common.exportCsv')}
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
    aria-hidden="true"
  >
    <path d="M12 3v12" />
    <path d="m7 10 5 5 5-5" />
    <path d="M5 21h14" />
  </svg>
  {t('common.exportCsv')}
</button>

<style>
  .export-selected-btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    white-space: nowrap;
  }
</style>
