<script>
  import { MessageSquareText, Activity, Menu } from '@lucide/svelte';
  import { t } from '../../lib/preferences.js';
  import { isMobile, sidebarOpen } from '../../lib/stores.js';
  import { Button } from '$lib/components/ui/button';
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
  <div class="chat-session-headline">
    {#if $isMobile}
      <Button
        type="button"
        variant="ghost"
        size="icon"
        class="chat-session-menu-toggle"
        aria-label={$t('sidebar.menu') || 'Menu'}
        aria-expanded={$sidebarOpen}
        title={$t('sidebar.menu') || 'Menu'}
        onclick={toggleSidebar}
      >
        <Menu size={18} />
      </Button>
    {/if}
    <div class="chat-session-heading">
      <strong>{title || $t('chat.newSession')}</strong>
      {#if channelLabel}<span>{channelLabel}</span>{/if}
      {#if busy}<span class="chat-session-run-status"><span class="dot running"></span>{$t('common.running')}</span>{/if}
    </div>
  </div>
  <div class="chat-session-utilities">
    <SessionLogDownload {sessionID} compact />
  </div>
  <nav class="chat-view-tabs" aria-label={$t('chat.session')}>
    <Button
      type="button"
      variant={view === 'chat' ? 'secondary' : 'ghost'}
      size="sm"
      class={view === 'chat' ? 'chat-view-tab active' : 'chat-view-tab'}
      onclick={() => onViewChange('chat')}
    >
      <MessageSquareText size={16} />
      <span>{$t('chat.view.chat')}</span>
    </Button>
    <Button
      type="button"
      variant={view === 'trajectory' ? 'secondary' : 'ghost'}
      size="sm"
      class={view === 'trajectory' ? 'chat-view-tab active' : 'chat-view-tab'}
      onclick={() => onViewChange('trajectory')}
    >
      <Activity size={16} />
      <span>{$t('chat.view.trajectory')}</span>
    </Button>
  </nav>
</header>
