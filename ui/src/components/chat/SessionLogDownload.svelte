<script>
  import { onDestroy } from 'svelte';
  import { t } from '../../lib/preferences.js';
  import { setSessionExportState } from '../../lib/stores.js';
  import { downloadSessionLog } from '../../lib/session-export.js';

  export let sessionID = '';
  export let compact = false;
  let state = 'idle';
  let error = '';
  let lastSessionID = '';
  let headController = null;

  $: if (sessionID !== lastSessionID) {
    lastSessionID = sessionID;
    headController?.abort();
    headController = null;
    state = 'idle';
    error = '';
  }

  async function handleDownload() {
    if (!sessionID || state === 'preparing') return;
    state = 'preparing';
    error = '';
    headController?.abort();
    headController = new AbortController();
    setSessionExportState(sessionID, { status: state });
    try {
      await downloadSessionLog(sessionID, true, headController.signal);
      state = 'started';
      setSessionExportState(sessionID, { status: state });
      window.setTimeout(() => {
        if (state === 'started') state = 'idle';
      }, 1400);
    } catch (downloadError) {
      if (downloadError?.name === 'AbortError') return;
      state = 'error';
      error = downloadError?.message || String(downloadError);
      setSessionExportState(sessionID, { status: state, error });
    } finally {
      headController = null;
    }
  }

  onDestroy(() => headController?.abort());

  $: label = state === 'preparing'
    ? $t('chat.sessionLog.preparing')
    : state === 'error'
      ? $t('chat.sessionLog.retry')
      : $t('chat.sessionLog.download');
</script>

<button
  type="button"
  class="session-log-download icon-btn"
  class:compact
  class:error={state === 'error'}
  disabled={!sessionID || state === 'preparing'}
  title={label}
  aria-label={label}
  on:click={handleDownload}
>
  {#if state === 'preparing'}
    <span aria-hidden="true">...</span>
  {:else}
    <svg aria-hidden="true" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
      <path d="M12 3v12"></path>
      <path d="m7 10 5 5 5-5"></path>
      <path d="M5 21h14"></path>
    </svg>
  {/if}
</button>
{#if state === 'error'}
  <span class="session-log-error" role="status" title={error}>{$t('chat.sessionLog.failed')}</span>
{/if}
