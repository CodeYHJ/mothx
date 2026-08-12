<script>
  import { onDestroy, onMount, tick } from 'svelte';
  import { get } from 'svelte/store';
  import { fly, fade } from 'svelte/transition';
  import { sessions, currentSession, features, statsSummary, refreshStatsSummary, sidebarOpen, isMobile } from '../lib/stores.js';
  import { shortID } from '../lib/format.js';
  import { route, navigate } from '../lib/router.js';
  import { t } from '../lib/preferences.js';
  import PreferenceControls from './PreferenceControls.svelte';

  let searchTerm = '';
  let searchInput;
  let isMac = false;
  let searchShortcut = 'Ctrl K';
  let newChatShortcut = 'Ctrl⇧K';
  let removeShortcutListener = null;
  let historyScrollbarVisible = false;
  let hideHistoryScrollbarTimer = null;
  let previousBodyOverflow = '';
  let previousRoutePath = '';

  const primaryNav = [
    { key: 'chat', path: '/chat', label: 'nav.newChat', icon: 'edit', accent: true },
    { key: 'sessions', path: '/sessions', label: 'nav.sessions', icon: 'clock' },
    { key: 'skills', path: '/skills', label: 'nav.skills', icon: 'skills' },
    { key: 'stats', path: '/stats', label: 'nav.stats', icon: 'chart' },
    { key: 'cron', path: '/cron', label: 'nav.cron', icon: 'timer' }
  ];

  const secondaryNav = [
    { key: 'settings', path: '/settings', label: 'nav.settings', icon: 'settings' }
  ];

  $: filteredSessions = filterSessions($sessions, searchTerm);
  $: recentSessions = filteredSessions.slice(0, 12);
  $: summaryStats = $statsSummary || {};
  $: searchAriaShortcut = isMac ? 'Meta+K' : 'Control+K';
  $: newChatAriaShortcut = isMac ? 'Shift+Meta+K' : 'Shift+Control+K';

  onMount(() => {
    isMac = /Mac|iPhone|iPad|iPod/.test(navigator.platform || '');
    searchShortcut = isMac ? '⌘K' : 'Ctrl K';
    newChatShortcut = isMac ? '⇧⌘K' : 'Ctrl⇧K';
    const onKeydown = (event) => {
      if (event.key === 'Escape' && get(sidebarOpen)) {
        closeSidebar();
        return;
      }
      handleGlobalShortcut(event);
    };
    window.addEventListener('keydown', onKeydown);
    removeShortcutListener = () => window.removeEventListener('keydown', onKeydown);
    refreshStatsSummary();
  });

  $: if ($isMobile && $sidebarOpen) lockBodyScroll();
  $: if ((!$isMobile || !$sidebarOpen) && previousBodyOverflow !== '') unlockBodyScroll();
  $: if ($route.path !== previousRoutePath) {
    previousRoutePath = $route.path;
    if (previousRoutePath && $sidebarOpen) closeSidebar();
  }

  function lockBodyScroll() {
    if (previousBodyOverflow !== '') return;
    previousBodyOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
  }

  function unlockBodyScroll() {
    document.body.style.overflow = previousBodyOverflow;
    previousBodyOverflow = '';
  }

  onDestroy(() => {
    removeShortcutListener?.();
    if (hideHistoryScrollbarTimer) clearTimeout(hideHistoryScrollbarTimer);
    if (previousBodyOverflow !== '') unlockBodyScroll();
  });

  function filterSessions(list, term) {
    const t = term.trim().toLowerCase();
    if (!t) return list;
    return list.filter((s) => {
      const hay = `${s.id || ''} ${s.workDir || ''} ${(s.title || '')}`.toLowerCase();
      return hay.includes(t);
    });
  }

  function openSession(id) {
    currentSession.set(id);
    navigate(id ? `/chat?session=${encodeURIComponent(id)}` : '/chat');
    closeSidebar();
  }

  function openNewChat() {
    currentSession.set('');
    navigate('/chat');
    closeSidebar();
  }

  function closeSidebar() {
    sidebarOpen.set(false);
  }

  function onNavClick(item) {
    navigate(item.path);
    closeSidebar();
  }

  async function focusSearch() {
    await tick();
    searchInput?.focus();
    searchInput?.select();
  }

  function handleGlobalShortcut(event) {
    const key = (event.key || '').toLowerCase();
    const mod = isMac ? event.metaKey : event.ctrlKey;
    if (!mod || key !== 'k' || event.altKey) return;
    event.preventDefault();
    event.stopPropagation();
    if (event.shiftKey) {
      openNewChat();
      return;
    }
    focusSearch();
  }

  function handleSearchKeydown(event) {
    if (event.key === 'Escape' && searchTerm) {
      event.preventDefault();
      searchTerm = '';
    }
  }

  function showHistoryScrollbar() {
    historyScrollbarVisible = true;
    if (hideHistoryScrollbarTimer) clearTimeout(hideHistoryScrollbarTimer);
    hideHistoryScrollbarTimer = setTimeout(() => {
      historyScrollbarVisible = false;
      hideHistoryScrollbarTimer = null;
    }, 900);
  }

  function isActive(item) {
    return $route.section === item.key;
  }

  function isFeatureEnabled(item) {
    if (!item.feature) return true;
    return $features[item.feature] !== false;
  }

  function formatStat(value) {
    const n = Number(value || 0);
    if (!Number.isFinite(n)) return '0';
    if (Math.abs(n) < 10000) return new Intl.NumberFormat().format(n);
    return new Intl.NumberFormat(undefined, {
      notation: 'compact',
      maximumFractionDigits: 1
    }).format(n);
  }
</script>

{#snippet content()}
  <div class="side-search">
    <span class="ico" aria-hidden="true">🔍</span>
    <input
      bind:this={searchInput}
      bind:value={searchTerm}
      placeholder={$t('sidebar.search')}
      aria-label={$t('sidebar.search')}
      aria-keyshortcuts={searchAriaShortcut}
      on:keydown={handleSearchKeydown}
    />
    <kbd>{searchShortcut}</kbd>
  </div>

  <button
    type="button"
    class="new-chat"
    on:click={openNewChat}
    aria-keyshortcuts={newChatAriaShortcut}
    title={`${$t('nav.newChat')} (${newChatShortcut})`}
  >
    <span class="ico" aria-hidden="true">✎</span>
    <span class="label">{$t('nav.newChat')}</span>
    <kbd>{newChatShortcut}</kbd>
  </button>

  <nav class="side-nav" aria-label={$t('nav.sessions')}>
    {#each primaryNav.slice(1) as item}
      <button
        type="button"
        class="nav-item"
        class:active={isActive(item)}
        disabled={!isFeatureEnabled(item)}
        on:click={() => onNavClick(item)}
      >
        <span class="ico ico-{item.icon}" aria-hidden="true"></span>
        <span class="label">{$t(item.label)}</span>
      </button>
    {/each}

    <div class="nav-divider"></div>

    {#each secondaryNav as item}
      <button
        type="button"
        class="nav-item"
        class:active={isActive(item)}
        on:click={() => onNavClick(item)}
      >
        <span class="ico ico-{item.icon}" aria-hidden="true"></span>
        <span class="label">{$t(item.label)}</span>
      </button>
    {/each}
  </nav>

  <section class="side-history" aria-label={$t('sidebar.history')}>
    <div class="side-history-head">
      <span>{$t('sidebar.history')}</span>
      <button
        type="button"
        class="link-btn"
        on:click={() => onNavClick({ path: '/sessions' })}
      >
        {$t('sidebar.all')}
      </button>
    </div>
    <div
      class="side-history-list"
      class:scrolling={historyScrollbarVisible}
      on:wheel={showHistoryScrollbar}
      on:scroll={showHistoryScrollbar}
    >
      <button
        type="button"
        class="history-item"
        class:active={$currentSession === '' && $route.section === 'chat'}
        on:click={() => openSession('')}
      >
        <span class="dot" aria-hidden="true"></span>
        <span class="text">{$t('sidebar.defaultSession')}</span>
      </button>
      {#each recentSessions as session (session.id)}
        <button
          type="button"
          class="history-item"
          class:active={$currentSession === session.id && $route.section === 'chat'}
          title={session.title || session.workDir || session.id}
          on:click={() => openSession(session.id)}
        >
          <span class="dot" aria-hidden="true"></span>
          <span class="text">
            <span class="name">{session.title || shortID(session.id)}</span>
            {#if session.workDir}<span class="dir">{session.workDir}</span>{/if}
          </span>
        </button>
      {/each}
      {#if recentSessions.length === 0 && !searchTerm}
        <p class="empty">{$t('sidebar.noHistory')}</p>
      {:else if recentSessions.length === 0 && searchTerm}
        <p class="empty">{$t('sidebar.noMatches')}</p>
      {/if}
    </div>
  </section>

  <div class="side-utility">
    <button type="button" class="side-stats" aria-label={$t('sidebar.stats')} title={$t('sidebar.stats')} on:click={() => onNavClick({ path: '/stats' })}>
      <svg aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
        <path d="M4 19V5M4 19h16M7.5 16v-4M12 16V8M16.5 16v-7"></path>
      </svg>
      <span class="stat-value" title={$t('sidebar.stats.requests')}>{formatStat(summaryStats.totalRequests)}</span>
      <span class="stat-divider" aria-hidden="true"></span>
      <span class="stat-value" title={$t('sidebar.stats.tokens')}>{formatStat(summaryStats.totalTokens)}</span>
    </button>
    <PreferenceControls />
  </div>
{/snippet}

{#if $isMobile}
  {#if $sidebarOpen}
    <button
      type="button"
      class="sidebar-overlay"
      aria-label="Close menu"
      transition:fade={{ duration: 200 }}
      on:click={closeSidebar}
    ></button>
    <aside class="sidebar mobile-drawer" transition:fly={{ x: -300, duration: 250 }}>
      {@render content()}
    </aside>
  {/if}
{:else}
  <aside class="sidebar">
    {@render content()}
  </aside>
{/if}
