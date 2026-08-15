<script>
  import { onDestroy, onMount } from 'svelte';
  import { get } from 'svelte/store';
  import Sidebar from './components/Sidebar.svelte';
  import Topbar from './components/Topbar.svelte';
  import Banners from './components/Banners.svelte';
  import Chat from './views/Chat.svelte';
  import Sessions from './views/Sessions.svelte';
  import Stats from './views/Stats.svelte';
  import Cron from './views/Cron.svelte';
  import Skills from './views/Skills.svelte';
  import Settings from './views/Settings.svelte';
  import Login from './views/Login.svelte';
  import { route, navigate } from './lib/router.js';
  import { refreshAll, connectLogs, disconnectLogs, connectRuns, disconnectRuns, currentSession, status, channels, serveConfig } from './lib/stores.js';
  import { t } from './lib/preferences.js';

  let stopRouteSync = null;
  let stopSessionSync = null;
  let authChecked = false;
  let authenticated = false;
  let appStarted = false;

  onMount(async () => {
    try {
      const auth = await fetch('/api/auth/status').then(async (response) => {
        const data = await response.json();
        if (!response.ok) throw new Error(data?.error?.message || data?.message || 'Authentication status unavailable');
        return data;
      });
      authenticated = auth?.authenticated === true;
    } catch {
      authenticated = false;
    } finally {
      authChecked = true;
      if (authenticated) startApp();
    }
  });

  function startApp() {
    if (appStarted) return;
    appStarted = true;
    if (!window.location.hash) navigate('/chat');
    stopRouteSync = route.subscribe(syncSessionFromRoute);
    stopSessionSync = currentSession.subscribe(syncRouteFromSession);
    connectLogs();
    connectRuns();
    refreshAll();
  }

  function handleAuthenticated() {
    authenticated = true;
    startApp();
  }

  onDestroy(() => {
    stopRouteSync?.();
    stopSessionSync?.();
    disconnectLogs();
    disconnectRuns();
  });

  function syncSessionFromRoute(nextRoute) {
    if (nextRoute.section !== 'chat') return;
    const routeSession = nextRoute.query?.session || '';
    if (get(currentSession) !== routeSession) currentSession.set(routeSession);
  }

  function syncRouteFromSession(sessionID) {
    const currentRoute = get(route);
    if (currentRoute.section !== 'chat') return;
    const routeSession = currentRoute.query?.session || '';
    const nextSession = sessionID || '';
    if (routeSession === nextSession) return;
    navigate(nextSession ? `/chat?session=${encodeURIComponent(nextSession)}` : '/chat');
  }
</script>

{#if !authChecked}
  <div class="login-loading"><span class="spinner lg"></span></div>
{:else if !authenticated}
  <Login on:authenticated={handleAuthenticated} />
{:else}
<div class="app-shell">
  <Sidebar />
  <main class="workbench">
    <Topbar />
    <Banners />
    <div class="view-container">
      {#if $route.section === 'chat'}
        <Chat />
      {:else if $route.section === 'sessions'}
        <Sessions />
      {:else if $route.section === 'stats'}
        <Stats />
      {:else if $route.section === 'cron'}
        <Cron />
      {:else if $route.section === 'skills'}
        <Skills />
      {:else if $route.section === 'settings'}
        <Settings />
      {:else}
        <section class="page">
          <p class="empty">{$t('app.unknownPage')}: {$route.path}</p>
        </section>
      {/if}
    </div>
  </main>
</div>
{/if}
