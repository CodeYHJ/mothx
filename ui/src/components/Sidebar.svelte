<script>
  import { onDestroy, onMount, tick } from 'svelte';
  import { get } from 'svelte/store';
  import { fly, fade } from 'svelte/transition';
  import { sessions, currentSession, features, statsSummary, refreshStatsSummary, refreshSessions, sidebarOpen, isMobile, setError } from '../lib/stores.js';
  import { shortID } from '../lib/format.js';
  import { route, navigate } from '../lib/router.js';
  import { t } from '../lib/preferences.js';
  import { request, jsonBody, del } from '../lib/api.js';
  import PreferenceControls from './PreferenceControls.svelte';
  import Modal from './Modal.svelte';

  let searchTerm = '';
  let searchInput;
  let isMac = false;
  let searchShortcut = 'Ctrl K';
  let newChatShortcut = 'Ctrl⇧K';
  let removeShortcutListener = null;
  let removeMenuOutsideListener = null;
  let historyScrollbarVisible = false;
  let hideHistoryScrollbarTimer = null;
  let previousBodyOverflow = '';
  let previousRoutePath = '';
  let projects = [];
  let openMenu = '';
  let collapsed = { projects: false, recent: false, unprojected: true };
  let collapsedProjects = {};
  let namingDialog = null;
  let nameDraft = '';

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
  $: recentSessions = filteredSessions.slice(0, 8);
  $: unprojectedSessions = filteredSessions.filter((item) => !item.projectId);
  $: projectSessions = (projectID) => filteredSessions.filter((item) => item.projectId === projectID);
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
    const onPointerDown = (event) => {
      if (!openMenu || event.target.closest('.side-menu, .more-btn')) return;
      openMenu = '';
    };
    document.addEventListener('pointerdown', onPointerDown, true);
    removeMenuOutsideListener = () => document.removeEventListener('pointerdown', onPointerDown, true);
    refreshStatsSummary();
    loadProjects();
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
    removeMenuOutsideListener?.();
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

  async function loadProjects() {
    try { projects = (await request('/api/projects'))?.projects || []; } catch (err) { setError(err); }
  }

  function toggle(section) { collapsed = { ...collapsed, [section]: !collapsed[section] }; }
  function toggleProject(id) { collapsedProjects = { ...collapsedProjects, [id]: !collapsedProjects[id] }; }
  function toggleMenu(event, key) { event.stopPropagation(); openMenu = openMenu === key ? '' : key; }
  function updateSessionLocal(id, patch) {
    sessions.update((list) => list.map((item) => item.id === id ? { ...item, ...patch } : item));
  }
  async function patchSession(id, body) {
    updateSessionLocal(id, body);
    try {
      const updated = await request(`/api/sessions/${encodeURIComponent(id)}/metadata`, { method: 'PATCH', ...jsonBody(body) });
      if (updated) updateSessionLocal(id, updated);
      await refreshSessions();
    } catch (err) { await refreshSessions(); setError(err); }
    openMenu = '';
  }
  function openNamingDialog(kind, target = null, current = '') {
    openMenu = '';
    namingDialog = { kind, target };
    nameDraft = current || '';
  }
  function closeNamingDialog() {
    namingDialog = null;
    nameDraft = '';
  }
  async function submitNamingDialog() {
    const name = nameDraft.trim();
    if (!name || !namingDialog) return;
    const dialog = namingDialog;
    closeNamingDialog();
    try {
      if (dialog.kind === 'session') {
        updateSessionLocal(dialog.target, { title: name });
        try {
          const updated = await request(`/api/sessions/${encodeURIComponent(dialog.target)}/title`, { method: 'POST', ...jsonBody({ title: name }) });
          if (updated) updateSessionLocal(dialog.target, updated);
          await refreshSessions();
        } catch (err) { await refreshSessions(); throw err; }
      } else if (dialog.kind === 'new-project') {
        await request('/api/projects', { method: 'POST', ...jsonBody({ name }) });
        await loadProjects();
      } else {
        await request(`/api/projects/${encodeURIComponent(dialog.target.id)}`, { method: 'PATCH', ...jsonBody({ name }) });
        await loadProjects();
      }
    } catch (err) { setError(err); }
    openMenu = '';
  }
  async function renameSession(id, current) {
    openNamingDialog('session', id, current);
  }
  async function deleteSession(id) {
    if (!window.confirm($t('sessions.deleteConfirm'))) return;
    try { await del(`/api/sessions/${encodeURIComponent(id)}`); if ($currentSession === id) openSession(''); await refreshSessions(); } catch (err) { setError(err); }
    openMenu = '';
  }
  function createProject() {
    openNamingDialog('new-project');
  }
  function renameProject(project) {
    openNamingDialog('project', project, project.name);
  }
  async function deleteProject(project) {
    if (!window.confirm($t('projects.deleteConfirm', { name: project.name }))) return;
    try { await del(`/api/projects/${encodeURIComponent(project.id)}`); await Promise.all([loadProjects(), refreshSessions()]); } catch (err) { setError(err); }
    openMenu = '';
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

{#snippet sessionRow(session, rowKey)}
  <div class="session-tree-row" class:active={$currentSession === session.id && $route.section === 'chat'}>
    <button type="button" class="session-tree-open" title={session.title || session.preview || shortID(session.id)} on:click={() => openSession(session.id)}>
      {#if session.pinned}<span class="pin" aria-label={$t('sessions.pinned')}>⌖</span>{/if}
      <span>{session.title || session.preview || shortID(session.id)}</span>
    </button>
    <button type="button" class="more-btn" aria-label={$t('sessions.manage')} on:click={(event) => toggleMenu(event, rowKey)}>•••</button>
    {#if openMenu === rowKey}
      <div class="side-menu">
        <button type="button" on:click={() => patchSession(session.id, { pinned: !session.pinned })}>{$t(session.pinned ? 'sessions.unpin' : 'sessions.pin')}</button>
        <button type="button" on:click={() => renameSession(session.id, session.title)}>{$t('sessions.rename')}</button>
        <div class="menu-label">{$t('sessions.moveToProject')}</div>
        {#each projects as project (project.id)}
          {#if project.id !== session.projectId}<button type="button" on:click={() => patchSession(session.id, { projectId: project.id })}>{project.name}</button>{/if}
        {/each}
        <button type="button" on:click={createProject}>{$t('projects.new')}</button>
        {#if session.projectId}<button type="button" on:click={() => patchSession(session.id, { projectId: '' })}>{$t('sessions.removeFromProject')}</button>{/if}
        <button type="button" class="danger-menu" on:click={() => deleteSession(session.id)}>{$t('common.delete')}</button>
      </div>
    {/if}
  </div>
{/snippet}

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
    <div class="side-history-list" class:scrolling={historyScrollbarVisible} on:wheel={showHistoryScrollbar} on:scroll={showHistoryScrollbar}>
      <div class="session-section">
        <div class="side-history-head">
          <button type="button" class="section-toggle" aria-expanded={!collapsed.projects} on:click={() => toggle('projects')}>⌄ <span>{$t('projects.title')}</span></button>
          <button type="button" class="section-action" title={$t('projects.new')} aria-label={$t('projects.new')} on:click={createProject}>+</button>
        </div>
        {#if !collapsed.projects}
          {#each projects as project (project.id)}
            <div class="project-group">
              <div class="project-row">
                <button type="button" class="section-toggle" aria-expanded={!collapsedProjects[project.id]} on:click={() => toggleProject(project.id)}>⌄ <span>{project.name}</span></button>
                <button type="button" class="more-btn" aria-label={$t('common.open')} on:click={(event) => toggleMenu(event, `project-${project.id}`)}>•••</button>
                {#if openMenu === `project-${project.id}`}
                  <div class="side-menu project-menu">
                    <button type="button" on:click={() => renameProject(project)}>{$t('projects.rename')}</button>
                    <button type="button" class="danger-menu" on:click={() => deleteProject(project)}>{$t('common.delete')}</button>
                  </div>
                {/if}
              </div>
              {#if !collapsedProjects[project.id]}
                {#each projectSessions(project.id) as session (session.id)}
                  {@render sessionRow(session, `project-${project.id}-${session.id}`)}
                {:else}
                  <p class="project-empty">{$t('projects.empty')}</p>
                {/each}
              {/if}
            </div>
          {/each}
        {/if}
      </div>

      <div class="session-section">
        <div class="side-history-head"><button type="button" class="section-toggle" aria-expanded={!collapsed.recent} on:click={() => toggle('recent')}>⌄ <span>{$t('projects.recent')}</span></button></div>
        {#if !collapsed.recent}
          {#each recentSessions as session (session.id)}
            {@render sessionRow(session, `recent-${session.id}`)}
          {/each}
        {/if}
      </div>

      <div class="session-section">
        <div class="side-history-head"><button type="button" class="section-toggle" aria-expanded={!collapsed.unprojected} on:click={() => toggle('unprojected')}>⌄ <span>{$t('projects.unprojected')}</span></button></div>
        {#if !collapsed.unprojected}
          {#each unprojectedSessions as session (session.id)}
            {@render sessionRow(session, `unprojected-${session.id}`)}
          {/each}
        {/if}
      </div>
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

{#if namingDialog}
  <Modal open={true} title={namingDialog.kind === 'session' ? $t('sessions.rename') : (namingDialog.kind === 'new-project' ? $t('projects.new') : $t('projects.rename'))} className="sidebar-naming-overlay" on:close={closeNamingDialog}>
    <div class="sidebar-naming-dialog">
      <div class="sidebar-naming-header">
        <h3>{namingDialog.kind === 'session' ? $t('sessions.rename') : (namingDialog.kind === 'new-project' ? $t('projects.new') : $t('projects.rename'))}</h3>
        <button type="button" class="sidebar-naming-close" aria-label={$t('common.cancel')} on:click={closeNamingDialog}>×</button>
      </div>
      <label for="sidebar-naming-input">{namingDialog.kind === 'session' ? $t('sessions.nameLabel') : $t('projects.nameLabel')}</label>
      <input id="sidebar-naming-input" bind:value={nameDraft} maxlength="80" autocomplete="off" placeholder={namingDialog.kind === 'session' ? $t('sessions.namePlaceholder') : $t('projects.namePlaceholder')} on:keydown={(event) => event.key === 'Enter' && submitNamingDialog()} />
      <div class="sidebar-naming-actions">
        <button type="button" class="sidebar-naming-cancel" on:click={closeNamingDialog}>{$t('common.cancel')}</button>
        <button type="button" class="sidebar-naming-submit" disabled={!nameDraft.trim()} on:click={submitNamingDialog}>{$t('common.confirm')}</button>
      </div>
    </div>
  </Modal>
{/if}

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
