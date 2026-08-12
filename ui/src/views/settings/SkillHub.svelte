<script>
  import { settings, setError, setNotice, clearBanners } from '../../lib/stores.js';
  import { putJSON } from '../../lib/api.js';
  import { t } from '../../lib/preferences.js';

  let form = { defaultMarket: 'skillhub.cn', defaultInstallScope: 'project', officialHandles: [], markets: [] };
  let last = '';
  $: if ($settings !== last) { last = $settings; load($settings); }

  function load(raw) {
    try {
      const cfg = JSON.parse(raw || '{}');
      const hub = cfg.skillHub || {};
      form = {
        defaultMarket: hub.defaultMarket || 'skillhub.cn',
        defaultInstallScope: hub.defaultInstallScope || 'project',
        officialHandles: hub.officialHandles || [],
        markets: hub.markets || []
      };
    } catch (e) {
      setError(e);
    }
  }

  function addMarket() {
    form.markets = [...form.markets, { id: '', name: '', siteURL: '', apiURL: '', enabled: true, apiToken: '' }];
  }

  function removeMarket(i) {
    form.markets = form.markets.filter((_, n) => n !== i);
  }

  async function save() {
    clearBanners();
    try {
      const cfg = JSON.parse($settings || '{}');
      cfg.skillHub = form;
      const saved = await putJSON('/api/settings', cfg);
      settings.set(JSON.stringify(saved, null, 2));
      setNotice($t('settings.skillhub.saved'));
    } catch (e) {
      setError(e);
    }
  }
</script>

<section class="settings-card">
  <div class="settings-card-head">
    <div>
      <h3>SkillHub</h3>
      <span class="hint">{$t('settings.skillhub.hint')}</span>
    </div>
    <button type="button" class="primary" on:click={save}>{$t('settings.skillhub.save')}</button>
  </div>
  <div class="settings-form-grid">
    <label><span>{$t('settings.skillhub.defaultMarket')}</span><input bind:value={form.defaultMarket} /></label>
    <label>
      <span>{$t('settings.skillhub.installScope')}</span>
      <select bind:value={form.defaultInstallScope}>
        <option value="project">{$t('settings.skillhub.project')}</option>
        <option value="global">{$t('settings.skillhub.global')}</option>
      </select>
    </label>
    <label class="full">
      <span>{$t('settings.skillhub.officialHandles')}</span>
      <input value={form.officialHandles.join(', ')} on:change={(e) => form.officialHandles = e.currentTarget.value.split(',').map((v) => v.trim()).filter(Boolean)} />
    </label>
  </div>
  <div class="settings-card-head">
    <h4>{$t('settings.skillhub.markets')}</h4>
    <button type="button" on:click={addMarket}>{$t('settings.skillhub.addMarket')}</button>
  </div>
  {#each form.markets as market, i}
    <div class="settings-form-grid market-editor">
      <label><span>{$t('settings.skillhub.id')}</span><input bind:value={market.id} placeholder="skillhub.cn" /></label>
      <label><span>{$t('settings.skillhub.name')}</span><input bind:value={market.name} /></label>
      <label><span>{$t('settings.skillhub.siteURL')}</span><input bind:value={market.siteURL} /></label>
      <label><span>{$t('settings.skillhub.apiURL')}</span><input bind:value={market.apiURL} /></label>
      <label><span>{$t('settings.skillhub.bearerToken')}</span><input type="password" bind:value={market.apiToken} /></label>
      <label class="checkbox"><input type="checkbox" bind:checked={market.enabled} /> {$t('settings.skillhub.enabled')}</label>
      <button type="button" on:click={() => removeMarket(i)}>{$t('settings.skillhub.remove')}</button>
    </div>
  {/each}
</section>
