<script>
  import { onDestroy } from 'svelte';
  import { Download, LoaderCircle } from '@lucide/svelte';
  import { Button } from '$lib/components/ui/button';
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

  $: buttonClass = `session-log-download ${compact ? 'compact' : ''} ${state === 'error' ? 'error' : ''}`.trim();
</script>

<Button
  type="button"
  variant="ghost"
  size="icon"
  class={buttonClass}
  disabled={!sessionID || state === 'preparing'}
  title={label}
  aria-label={label}
  onclick={handleDownload}
>
  {#if state === 'preparing'}
    <LoaderCircle size={15} class="session-log-spinner" aria-hidden="true" />
  {:else}
    <Download size={15} aria-hidden="true" />
  {/if}
</Button>
{#if state === 'error'}
  <span class="session-log-error" role="status" title={error}>{$t('chat.sessionLog.failed')}</span>
{/if}
