<script>
  let { text = '', href = '', onclick, className = '' } = $props();

  const display = $derived(text || '-');
</script>

{#if href}
  <a
    class="overflow-text {className}"
    href={href}
    title={display}
    aria-label={display}
    onclick={onclick}
  >
    {display}
  </a>
{:else}
  <span class="overflow-text {className}" title={display} aria-label={display}>
    {display}
  </span>
{/if}

<style>
  .overflow-text {
    display: block;
    max-width: 100%;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
