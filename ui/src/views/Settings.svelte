<script>
  import { route, navigate } from '../lib/router.js';
  import SettingsOverview from './settings/Overview.svelte';
  import SettingsServe from './settings/ServeConfig.svelte';
  import SettingsApp from './settings/AppSettings.svelte';
  import SettingsProviders from './settings/ProviderSettings.svelte';
  import SettingsMemory from './settings/Memory.svelte';
  import SettingsWorkDir from './settings/WorkDir.svelte';
  import SettingsChannels from './settings/Channels.svelte';
  import SettingsLogs from './settings/Logs.svelte';
  import SettingsSkillHub from './settings/SkillHub.svelte';
  import SettingsEnv from './settings/Env.svelte';
  import SettingsMCP from './settings/MCP.svelte';
  import { Tabs, TabsList, TabsTrigger } from '$lib/components/ui/tabs/index.js';
  import { t } from '../lib/preferences.js';

  const tabs = [
    { key: '', label: 'settings.tabs.overview' },
    { key: 'serve', label: 'settings.tabs.serve' },
    { key: 'workdir', label: 'settings.tabs.workdir' },
    { key: 'providers', label: 'settings.tabs.providers' },
    { key: 'app', label: 'settings.tabs.app' },
    { key: 'env', label: 'settings.tabs.env' },
    { key: 'mcp', label: 'settings.tabs.mcp' },
    { key: 'memory', label: 'settings.tabs.memory' },
    { key: 'channels', label: 'settings.tabs.channels' },
    { key: 'logs', label: 'settings.tabs.logs' },
    { key: 'skillhub', label: 'SkillHub' }
  ];

  $: activeTab = $route.sub || '';

  function open(sub) {
    navigate(sub ? `/settings/${sub}` : '/settings');
  }
</script>

<section class="page settings-page">
  <Tabs
    value={activeTab || 'overview'}
    class="settings-tabs"
    onValueChange={(value) => open(value === 'overview' ? '' : value)}
  >
    <TabsList variant="line" class="settings-tabs-list" aria-label={$t('nav.settings')}>
      {#each tabs as tab}
        <TabsTrigger value={tab.key || 'overview'}>{$t(tab.label)}</TabsTrigger>
      {/each}
    </TabsList>

  <div class="sub-body">
    {#if activeTab === ''}
      <SettingsOverview />
    {:else if activeTab === 'serve'}
      <SettingsServe />
    {:else if activeTab === 'workdir'}
      <SettingsWorkDir />
    {:else if activeTab === 'providers'}
      <SettingsProviders />
    {:else if activeTab === 'app'}
      <SettingsApp />
    {:else if activeTab === 'env'}
      <SettingsEnv />
    {:else if activeTab === 'mcp'}
      <SettingsMCP />
    {:else if activeTab === 'memory'}
      <SettingsMemory />
    {:else if activeTab === 'channels'}
      <SettingsChannels />
    {:else if activeTab === 'logs'}
      <SettingsLogs />
    {:else if activeTab === 'skillhub'}
      <SettingsSkillHub />
    {:else}
      <p class="empty">{$t('settings.unknown')}</p>
    {/if}
  </div>
  </Tabs>
</section>
