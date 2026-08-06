<script>
  import { onMount } from 'svelte';
  import { request, putJSON } from '../../lib/api.js';
  import { setError, setNotice, clearBanners } from '../../lib/stores.js';
  import { t } from '../../lib/preferences.js';

  let vars = {};
  let newKey = '';
  let newValue = '';
  let loading = true;
  let saving = false;
  let dirty = false;

  $: keys = Object.keys(vars).sort();

  onMount(async () => {
    try {
      const result = await request('/api/env');
      vars = result?.vars || {};
    } catch (err) {
      setError(err);
    } finally {
      loading = false;
    }
  });

  function markDirty() {
    dirty = true;
    clearBanners();
  }

  function addVariable() {
    const key = newKey.trim();
    if (!key) return;
    if (Object.prototype.hasOwnProperty.call(vars, key)) {
      setError(new Error($t('settings.env.duplicate')));
      return;
    }
    vars = { ...vars, [key]: newValue };
    newKey = '';
    newValue = '';
    markDirty();
  }

  function updateValue(key, value) {
    vars = { ...vars, [key]: value };
    markDirty();
  }

  function removeVariable(key) {
    const next = { ...vars };
    delete next[key];
    vars = next;
    markDirty();
  }

  async function save() {
    saving = true;
    clearBanners();
    try {
      await putJSON('/api/env', { vars });
      dirty = false;
      setNotice($t('settings.env.saved'));
    } catch (err) {
      setError(err);
    } finally {
      saving = false;
    }
  }

  function handleNewKey(event) {
    if (event.key === 'Enter') {
      event.preventDefault();
      addVariable();
    }
  }
</script>

<div class="env-page">
  <div class="env-heading">
    <div>
      <h2>{$t('settings.env.title')}</h2>
      <p class="hint">{$t('settings.env.hint')}</p>
    </div>
    <span class="env-count">{keys.length} {$t('settings.env.count')}</span>
  </div>

  <div class="card env-card">
    <div class="card-head">
      <div>
        <h3>{$t('settings.env.variables')}</h3>
        <span class="hint">{$t('settings.env.editHint')}</span>
      </div>
      <button type="button" class="primary" on:click={save} disabled={loading || saving || !dirty}>
        {saving ? $t('common.saving') : $t('common.save')}
      </button>
    </div>

    {#if loading}
      <div class="env-empty"><span class="loading-row">{$t('common.loading')}</span></div>
    {:else}
      <div class="env-add form-body">
        <div class="env-add-title">{$t('settings.env.add')}</div>
        <div class="env-add-row">
          <label>
            <span>{$t('settings.env.name')}</span>
            <input bind:value={newKey} on:keydown={handleNewKey} placeholder="MY_VARIABLE" autocomplete="off" />
          </label>
          <label class="value-field">
            <span>{$t('settings.env.value')}</span>
            <input bind:value={newValue} on:keydown={handleNewKey} placeholder="value" autocomplete="off" />
          </label>
          <button type="button" class="sm add-button" on:click={addVariable} disabled={!newKey.trim()}>
            + {$t('common.add')}
          </button>
        </div>
      </div>

      {#if keys.length === 0}
        <div class="env-empty">
          <div class="empty-icon">⌘</div>
          <strong>{$t('settings.env.empty')}</strong>
          <span>{$t('settings.env.emptyHint')}</span>
        </div>
      {:else}
        <div class="env-list">
          {#each keys as key (key)}
            <div class="env-row">
              <div class="env-key"><code>{key}</code></div>
              <input
                class="env-value"
                value={vars[key]}
                on:input={(event) => updateValue(key, event.currentTarget.value)}
                aria-label={`${$t('settings.env.value')}: ${key}`}
              />
              <button type="button" class="ghost danger remove-button" on:click={() => removeVariable(key)} title={$t('common.remove')}>
                ×
              </button>
            </div>
          {/each}
        </div>
      {/if}
    {/if}
  </div>
</div>

<style>
  .env-page { display: grid; gap: 16px; }
  .env-heading { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 2px 2px 0; }
  .env-heading h2 { margin: 0; font-size: 19px; }
  .env-heading p { margin: 5px 0 0; max-width: 680px; }
  .env-count { flex: 0 0 auto; padding: 5px 9px; border: 1px solid var(--border-subtle); border-radius: 999px; color: var(--text-muted); background: var(--bg-secondary); font-size: 11px; }
  .env-card { overflow: hidden; }
  .env-add { border-top: 1px solid var(--border-subtle); border-bottom: 1px solid var(--border-subtle); background: var(--bg-secondary); }
  .env-add-title { color: var(--text-secondary); font-size: 12px; font-weight: 600; margin-bottom: 10px; }
  .env-add-row { display: grid; grid-template-columns: minmax(180px, .8fr) minmax(220px, 1.2fr) auto; gap: 10px; align-items: end; }
  .env-add-row label { display: grid; gap: 4px; color: var(--text-secondary); font-size: 12px; font-weight: 500; }
  .env-add-row input { width: 100%; min-width: 0; }
  .add-button { white-space: nowrap; }
  .env-list { display: grid; }
  .env-row { display: grid; grid-template-columns: minmax(160px, .8fr) minmax(220px, 1.2fr) 34px; gap: 12px; align-items: center; padding: 10px 18px; border-bottom: 1px solid var(--border-subtle); }
  .env-row:last-child { border-bottom: 0; }
  .env-key code { color: var(--accent-text); font: 600 12px var(--font-mono); overflow-wrap: anywhere; }
  .env-value { width: 100%; min-width: 0; font-family: var(--font-mono); font-size: 12px; }
  .remove-button { width: 30px; min-height: 30px; padding: 0; font-size: 19px; line-height: 1; }
  .env-empty { display: grid; justify-items: center; gap: 6px; padding: 42px 20px; color: var(--text-muted); text-align: center; }
  .env-empty strong { color: var(--text-secondary); font-size: 13px; }
  .env-empty span { font-size: 12px; }
  .empty-icon { display: grid; place-items: center; width: 34px; height: 34px; margin-bottom: 4px; border: 1px solid var(--border); border-radius: 50%; color: var(--primary); font: 16px var(--font-mono); }
  @media (max-width: 640px) {
    .env-heading { align-items: flex-start; }
    .env-add-row, .env-row { grid-template-columns: 1fr; gap: 8px; }
    .env-row { position: relative; padding-right: 52px; }
    .remove-button { position: absolute; top: 10px; right: 14px; }
    .add-button { width: 100%; }
  }
</style>
