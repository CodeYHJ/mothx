<script>
  import { createEventDispatcher } from 'svelte';
  import { request } from '../lib/api.js';
  import { t } from '../lib/preferences.js';
  import Modal from './Modal.svelte';

  export let open = false;
  export let initialPath = '';

  const dispatch = createEventDispatcher();

  let currentPath = '';
  let parentPath = '';
  let entries = [];
  let loading = false;
  let error = '';
  let selectable = true;
  let browserOpened = false;

  $: if (open) openBrowser();
  $: if (!open) browserOpened = false;

  function openBrowser() {
    if (browserOpened) return;
    browserOpened = true;
    currentPath = initialPath || '';
    load();
  }

  async function load() {
    loading = true;
    error = '';
    selectable = false;
    try {
      const params = currentPath ? `?path=${encodeURIComponent(currentPath)}` : '';
      const data = await request(`/api/browse${params}`);
      currentPath = data.path;
      parentPath = data.parent;
      entries = data.entries || [];
      selectable = data.selectable !== false;
    } catch (err) {
      error = err.message;
      entries = [];
      selectable = false;
    } finally {
      loading = false;
    }
  }

  function enter(dirPath) {
    currentPath = dirPath;
    load();
  }

  function goUp() {
    if (parentPath && parentPath !== currentPath) {
      currentPath = parentPath;
      load();
    }
  }

  function select() {
    if (loading || !selectable || !currentPath.trim()) return;
    dispatch('select', { path: currentPath });
    open = false;
  }

  function close() {
    open = false;
    dispatch('close');
  }

  function handlePathKeydown(event) {
    if (event.key === 'Enter') {
      event.preventDefault();
      load();
    }
  }

</script>

<Modal open={open} title={$t('dirBrowser.title')} className="dir-overlay" on:close={close}>
    <div class="dir-modal">
      <div class="dir-header">
        <h3>{$t('dirBrowser.title')}</h3>
        <button type="button" class="ghost" on:click={close}>✕</button>
      </div>

      <div class="dir-nav">
        <button type="button" class="ghost" on:click={goUp} disabled={parentPath === currentPath || !parentPath}>↑ {$t('dirBrowser.up')}</button>
        <div class="dir-path">
          <span class="ico">📁</span>
          <input
            type="text"
            class="path-input path-text"
            bind:value={currentPath}
            on:keydown={handlePathKeydown}
            aria-label={$t('dirBrowser.title')}
            title={$t('dirBrowser.title')}
          />
        </div>
      </div>

      <div class="dir-list">
        {#if loading}
          <p class="empty">{$t('dirBrowser.loading')}</p>
        {:else if error}
          <p class="empty dir-error">{error}</p>
        {:else if entries.length === 0}
          <p class="empty">{$t('dirBrowser.empty')}</p>
        {:else}
          {#each entries as entry (entry.path)}
            <button
              type="button"
              class="dir-entry"
              on:click={() => enter(entry.path)}
            >
              <span class="ico">📂</span>
              <span class="name">{entry.name}</span>
            </button>
          {/each}
        {/if}
      </div>

      <div class="dir-footer">
        <button type="button" class="ghost" on:click={close}>{$t('dirBrowser.cancel')}</button>
        <button type="button" class="primary" on:click={select} disabled={loading || !selectable || !currentPath.trim()}>{$t('dirBrowser.select')}</button>
      </div>
    </div>
</Modal>

<style>
  .path-input {
    flex: 1;
    min-width: 0;
    background: transparent;
    border: none;
    outline: none;
    color: inherit;
    font: inherit;
    padding: 0;
  }
</style>
