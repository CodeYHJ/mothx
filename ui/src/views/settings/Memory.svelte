<script>
  import { memory, memoryInfo, setError, setNotice, clearBanners } from '../../lib/stores.js';
  import { putJSON } from '../../lib/api.js';
  import { t } from '../../lib/preferences.js';
  import { Button } from '$lib/components/ui/button/index.js';
  import { Save } from '@lucide/svelte';
  import { Badge } from '$lib/components/ui/badge/index.js';
  import SettingsSection from './SettingsSection.svelte';

  $: disabled = $memoryInfo?.enabled === false;

  async function save() {
    clearBanners();
    try {
      const saved = await putJSON('/api/memory', { content: $memory });
      memoryInfo.set(saved);
      memory.set(saved?.content || '');
      setNotice($t('settings.memory.saved'));
    } catch (err) {
      setError(err);
    }
  }
</script>

<SettingsSection title="Memory" description={disabled ? $t('common.disabledState') : ($memoryInfo?.path || $t('settings.memory.notInitialized'))}>
  <div class="memory-status">
    <Badge variant={disabled ? 'secondary' : 'default'}>
      {disabled ? $t('common.disabledState') : $t('common.enabled')}
    </Badge>
    {#if $memoryInfo?.path}
      <span class="memory-path">{$memoryInfo.path}</span>
    {/if}
  </div>
  <textarea class="settings-textarea memory-textarea" bind:value={$memory} disabled={disabled} spellcheck="false"></textarea>
  <div class="settings-form-actions">
    <Button type="button" variant="outline" size="sm" onclick={save} disabled={disabled}>
      <Save size={14} aria-hidden="true" />
      <span>{$t('common.save')}</span>
    </Button>
  </div>
</SettingsSection>

<style>
  .memory-status {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-bottom: 12px;
    flex-wrap: wrap;
  }
  .memory-path {
    font-size: 12px;
    color: var(--text-muted);
    font-family: var(--font-mono);
  }
  .memory-textarea {
    min-height: 240px;
    max-height: 60vh;
    font-family: var(--font-mono);
    font-size: 13px;
  }
  .settings-form-actions { margin-top: 12px; }
</style>
