<script>
  import { t } from '../../lib/preferences.js';
  import { isMobile, sidebarOpen } from '../../lib/stores.js';
  import SessionLogDownload from './SessionLogDownload.svelte';

  export let title = '';
  export let channelLabel = '';
  export let sessionID = '';
  export let view = 'chat';
  export let busy = false;
  export let onViewChange = () => {};

  function toggleSidebar() {
    sidebarOpen.update((value) => !value);
  }
</script>

<header class="chat-session-header">
  {#if $isMobile}
    <button
      type="button"
      class="menu-toggle chat-session-menu-toggle"
      aria-label={$t('sidebar.menu') || 'Menu'}
      aria-expanded={$sidebarOpen}
      on:click={toggleSidebar}
    >
      <span class="menu-bar"></span>
      <span class="menu-bar"></span>
      <span class="menu-bar"></span>
    </button>
  {/if}
  <div class="chat-session-heading">
    <strong>{title || $t('chat.newSession')}</strong>
    {#if channelLabel}<span>{channelLabel}</span>{/if}
    {#if busy}<span class="chat-session-run-status"><span class="dot running"></span>{$t('common.running')}</span>{/if}
  </div>
  <div class="chat-session-utilities">
    <SessionLogDownload {sessionID} compact />
  </div>
  <nav class="chat-view-tabs" aria-label={$t('chat.session')}>
    <button type="button" class:active={view === 'chat'} on:click={() => onViewChange('chat')}>{$t('chat.view.chat')}</button>
    <button type="button" class:active={view === 'trajectory'} on:click={() => onViewChange('trajectory')}>{$t('chat.view.trajectory')}</button>
  </nav>
</header>
