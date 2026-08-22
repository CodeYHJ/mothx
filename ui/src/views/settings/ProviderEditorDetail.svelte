<script>
  import { Input } from '$lib/components/ui/input/index.js';
  import { Button } from '$lib/components/ui/button/index.js';
  import { Switch } from '$lib/components/ui/switch/index.js';
  import SettingsField from './SettingsField.svelte';
  import SettingsSwitch from './SettingsSwitch.svelte';
  import { t } from '../../lib/preferences.js';
  import { ChevronDown, Plus, Trash2 } from '@lucide/svelte';

  let {
    provider,
    modelTestStates = {},
    loadingDiscoveredModels = false,
    isMobileDetail = false,
    onRename,
    onAddHeader,
    onRemoveHeader,
    onAddModel,
    onRemoveModel,
    onFetchModels,
    onTestModel,
    onRemoveProvider,
    apiTypeOptionsForProvider
  } = $props();
</script>

<section class="provider-detail" class:provider-detail-mobile={isMobileDetail}>
  <div class="settings-form-grid">
    <SettingsField label={$t('settings.app.providerID')}>
      <Input value={provider.id} oninput={(event) => onRename(provider, event.currentTarget.value)} />
    </SettingsField>
    <SettingsField label={$t('settings.app.providerVendor')}>
      <Input bind:value={provider.vendor} />
    </SettingsField>
    <SettingsField label={$t('settings.app.providerAPI')}>
      <select bind:value={provider.api} class="settings-select">
        {#each apiTypeOptionsForProvider(provider, provider.api) as apiType}
          <option value={apiType}>{apiType}</option>
        {/each}
      </select>
    </SettingsField>
    <SettingsField label={$t('settings.app.providerThinkingFormat')}>
      <select bind:value={provider.thinkingFormat} class="settings-select">
        <option value="">{$t('common.uninitialized')}</option>
        <option value="anthropic">anthropic</option>
        <option value="deepseek">deepseek</option>
        <option value="openai">openai</option>
        <option value="xiaomi">xiaomi</option>
        <option value="zai">zai</option>
        <option value="kimi">kimi</option>
        <option value="qwen">qwen</option>
      </select>
    </SettingsField>
    <SettingsField label={$t('settings.app.providerBaseURL')} className="full">
      <Input bind:value={provider.baseUrl} />
    </SettingsField>
    <SettingsField label={$t('settings.app.providerAPIKey')} className="full">
      <Input type="password" autocomplete="off" bind:value={provider.apiKey} />
    </SettingsField>
    <SettingsField label={$t('settings.app.httpProxy')}>
      <Input bind:value={provider.httpProxy} />
    </SettingsField>
    <SettingsField label={$t('settings.app.maxImagesPerRequest')}>
      <Input type="number" min="-1" step="1" bind:value={provider.maxImagesPerRequest} />
    </SettingsField>
    <SettingsSwitch title={$t('settings.app.forceHTTP11')} bind:checked={provider.forceHTTP11} />
    <SettingsField label={$t('settings.app.cacheControl')}>
      <select bind:value={provider.cacheControl} class="settings-select">
        <option value="">{$t('common.uninitialized')}</option>
        <option value="true">{$t('common.enabled')}</option>
        <option value="false">{$t('common.disabled')}</option>
      </select>
    </SettingsField>
    <SettingsField label={$t('settings.app.reasoningSummary')}>
      <select bind:value={provider.responses.reasoningSummary} class="settings-select">
        <option value="">{$t('common.uninitialized')}</option>
        <option value="auto">auto</option>
        <option value="concise">concise</option>
        <option value="detailed">detailed</option>
        <option value="none">none</option>
      </select>
    </SettingsField>
    <SettingsField label={$t('settings.app.promptCacheKey')}>
      <Input bind:value={provider.responses.promptCacheKey} />
    </SettingsField>
    <SettingsField label={$t('settings.app.promptCacheRetention')}>
      <Input bind:value={provider.responses.promptCacheRetention} />
    </SettingsField>
  </div>
  <div class="provider-actions">
    <Button variant="outline" size="sm" type="button" onclick={() => onAddHeader(provider)}>
      <Plus size={14} aria-hidden="true" />
      <span>{$t('settings.app.addHeader')}</span>
    </Button>
    <Button variant="outline" size="sm" type="button" onclick={() => onAddModel(provider)}>
      <Plus size={14} aria-hidden="true" />
      <span>{$t('settings.app.addModel')}</span>
    </Button>
    <Button variant="destructive" size="sm" type="button" onclick={() => onRemoveProvider(provider)}>
      <Trash2 size={14} aria-hidden="true" />
      <span>{$t('common.remove')}</span>
    </Button>
  </div>
  {#if provider.headers.length > 0}
    <div class="model-list">
      <div class="list-head"><span>{$t('settings.app.headers')}</span></div>
      {#each provider.headers as header, i (i)}
        <div class="provider-header-row">
          <Input bind:value={header.key} placeholder="Header" />
          <Input bind:value={header.value} placeholder="Value" />
          <Button variant="ghost" size="icon-xs" type="button" onclick={() => onRemoveHeader(provider, i)} aria-label={$t('common.remove')}>
            <Trash2 size={14} aria-hidden="true" />
          </Button>
        </div>
      {/each}
    </div>
  {/if}
  <div class="model-list">
    <div class="list-head">
      <span>{$t('settings.app.models')}</span>
      <div class="model-list-actions">
        <Button variant="outline" size="sm" type="button" disabled={loadingDiscoveredModels} onclick={() => onFetchModels(provider)}>
          {$t('settings.app.fetchModels')}
        </Button>
        <Button variant="outline" size="sm" type="button" onclick={() => onAddModel(provider)}>
          <Plus size={14} aria-hidden="true" />
          <span>{$t('common.add')}</span>
        </Button>
      </div>
    </div>
    {#each provider.models as model, i (i)}
      {@const testKey = `${provider.id}:${i}`}
      <details class="model-detail">
        <summary class="model-detail-summary">
          <span class="model-summary-main">
            <strong>{model.id || $t('settings.app.unnamedModel')}</strong>
            {#if model.name}
              <span class="model-summary-name">{model.name}</span>
            {/if}
          </span>
          <span class="model-summary-side">
            {#if model.contextWindow || model.maxTokens}
              <span class="model-summary-meta">
                {[
                  model.contextWindow ? `${$t('settings.app.modelContext')}: ${model.contextWindow}` : '',
                  model.maxTokens ? `${$t('settings.app.modelMaxTokens')}: ${model.maxTokens}` : ''
                ].filter(Boolean).join(' · ')}
              </span>
            {/if}
            <ChevronDown size={16} aria-hidden="true" class="model-detail-chevron" />
          </span>
        </summary>
        <div class="model-detail-body">
          <SettingsField label={$t('settings.app.modelID')}>
            <Input bind:value={model.id} placeholder={$t('settings.app.modelID')} />
          </SettingsField>
          <SettingsField label={$t('settings.app.modelName')}>
            <Input bind:value={model.name} placeholder={$t('settings.app.modelName')} />
          </SettingsField>
          <SettingsField label={$t('settings.app.modelContext')}>
            <Input type="number" min="0" bind:value={model.contextWindow} placeholder={$t('settings.app.modelContext')} />
          </SettingsField>
          <SettingsField label={$t('settings.app.modelMaxTokens')}>
            <Input type="number" min="0" bind:value={model.maxTokens} placeholder={$t('settings.app.modelMaxTokens')} />
          </SettingsField>
          <SettingsField label={$t('settings.app.modelTemperature')}>
            <Input type="number" step="0.1" bind:value={model.temperature} placeholder={$t('settings.app.modelTemperature')} />
          </SettingsField>
          <SettingsField label={$t('settings.app.modelTopP')}>
            <Input type="number" step="0.1" bind:value={model.topP} placeholder={$t('settings.app.modelTopP')} />
          </SettingsField>
          <SettingsField label={$t('settings.app.modelInput')}>
            <Input bind:value={model.input} placeholder={$t('settings.app.modelInput')} />
          </SettingsField>
          <SettingsSwitch title={$t('settings.app.modelReasoning')} bind:checked={model.reasoning} />
          <SettingsSwitch title={$t('settings.app.modelAllowSampling')} bind:checked={model.allowSampling} />
          <div class="model-detail-actions">
            <Button variant="outline" size="sm" type="button" disabled={!model.id.trim() || modelTestStates[testKey]?.loading} onclick={() => onTestModel(provider, model, i)}>
              {$t('settings.app.testModel')}
            </Button>
            <Button variant="ghost" size="icon-xs" type="button" onclick={() => onRemoveModel(provider, i)} aria-label={$t('common.remove')}>
              <Trash2 size={14} aria-hidden="true" />
            </Button>
          </div>
          {#if modelTestStates[testKey]}
            <span class:success-text={modelTestStates[testKey].ok === true} class:error-text={modelTestStates[testKey].ok === false} class="model-test-status">
              {modelTestStates[testKey].loading ? $t('common.loading') : modelTestStates[testKey].message}
            </span>
          {/if}
        </div>
      </details>
    {/each}
  </div>
</section>
