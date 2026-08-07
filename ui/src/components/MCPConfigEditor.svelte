<script>
  import { onMount } from 'svelte';
  import { request, putJSON } from '../lib/api.js';
  import { setError, setNotice, clearBanners } from '../lib/stores.js';
  import { t } from '../lib/preferences.js';

  export let endpoint = '/api/mcp';
  export let title = '';
  export let hint = '';

  let servers = [];
  let loading = true;
  let saving = false;
  let dirty = false;

  const pair = () => ({ name: '', value: '' });
  const emptyServer = () => ({ name: '', type: 'stdio', command: '', url: '', messageUrl: '', args: [], headers: [], env: [] });
  const basicTemplate = () => ({ name: 'example-stdio', type: 'stdio', command: '/absolute/path/to/mcp-server', url: '', messageUrl: '', args: [], headers: [], env: [] });
  const fullTemplate = () => ([
    { name: 'local-stdio', type: 'stdio', command: '/absolute/path/to/mcp-server', url: '', messageUrl: '', args: ['--port', '8080'], headers: [], env: [{ name: 'API_KEY', value: 'replace-me' }] },
    { name: 'remote-http', type: 'http', command: '', url: 'https://mcp.example.com', messageUrl: '', args: [], headers: [{ name: 'Authorization', value: 'Bearer replace-me' }], env: [] },
    { name: 'legacy-sse', type: 'sse', command: '', url: 'https://legacy.example.com/sse', messageUrl: 'https://legacy.example.com/messages', args: [], headers: [{ name: 'Authorization', value: 'Bearer replace-me' }], env: [] }
  ]);

  onMount(load);

  function normalizeServer(server = {}) {
    return {
      name: server.name || '', type: server.type || 'stdio', command: server.command || '', url: server.url || '', messageUrl: server.messageUrl || '',
      args: Array.isArray(server.args) ? [...server.args] : [],
      headers: Array.isArray(server.headers) ? server.headers.map((item) => ({ name: item?.name || '', value: item?.value || '' })) : [],
      env: Array.isArray(server.env) ? server.env.map((item) => ({ name: item?.name || '', value: item?.value || '' })) : []
    };
  }

  async function load() {
    loading = true;
    try {
      const cfg = await request(endpoint);
      servers = Array.isArray(cfg?.mcpServers) ? cfg.mcpServers.map(normalizeServer) : [];
      dirty = false;
    } catch (err) {
      setError(err);
    } finally {
      loading = false;
    }
  }

  function markDirty() {
    dirty = true;
    clearBanners();
  }

  function addServer(server = emptyServer()) { servers = [...servers, server]; markDirty(); }
  function removeServer(index) { servers = servers.filter((_, i) => i !== index); markDirty(); }
  function useBasicTemplate() { servers = [basicTemplate()]; markDirty(); }
  function useFullTemplate() { servers = fullTemplate(); markDirty(); }
  function updateArg(server, index, value) { server.args[index] = value; servers = servers; markDirty(); }
  function addArg(server) { server.args = [...server.args, '']; servers = servers; markDirty(); }
  function removeArg(server, index) { server.args = server.args.filter((_, i) => i !== index); servers = servers; markDirty(); }
  function updatePair(server, field, index, key, value) { server[field][index][key] = value; servers = servers; markDirty(); }
  function addPair(server, field) { server[field] = [...server[field], pair()]; servers = servers; markDirty(); }
  function removePair(server, field, index) { server[field] = server[field].filter((_, i) => i !== index); servers = servers; markDirty(); }

  function updateServer(server, key, value) {
    server[key] = value;
    servers = servers;
    markDirty();
  }

  async function save() {
    if (servers.some((server) => !server.name.trim())) {
      setError(new Error($t('settings.mcp.nameRequired')));
      return;
    }
    saving = true;
    clearBanners();
    try {
      const saved = await putJSON(endpoint, { mcpServers: servers });
      servers = Array.isArray(saved?.mcpServers) ? saved.mcpServers.map(normalizeServer) : [];
      dirty = false;
      setNotice($t('settings.mcp.saved'));
    } catch (err) {
      setError(err);
    } finally {
      saving = false;
    }
  }
</script>

<div class="mcp-editor">
  <div class="mcp-heading">
    <div><h2>{title}</h2><p class="hint">{hint}</p></div>
    <button type="button" class="primary" on:click={save} disabled={loading || saving || !dirty}>{saving ? $t('common.saving') : $t('common.save')}</button>
  </div>

  <div class="card mcp-card">
    <div class="card-head">
      <div><h3>{$t('settings.mcp.servers')}</h3><span class="hint">{$t('settings.mcp.formHint')}</span></div>
      <div class="mcp-actions">
        <button type="button" class="ghost sm" on:click={useBasicTemplate} disabled={loading}>{$t('settings.mcp.basicTemplate')}</button>
        <button type="button" class="ghost sm" on:click={useFullTemplate} disabled={loading}>{$t('settings.mcp.fullTemplate')}</button>
        <button type="button" class="ghost sm" on:click={() => addServer()} disabled={loading}>+ {$t('settings.mcp.addServer')}</button>
      </div>
    </div>

    {#if loading}
      <div class="mcp-loading">{$t('common.loading')}</div>
    {:else if servers.length === 0}
      <div class="mcp-empty"><strong>{$t('settings.mcp.empty')}</strong><span>{$t('settings.mcp.emptyHint')}</span><button type="button" class="ghost sm" on:click={() => addServer()}>+ {$t('settings.mcp.addServer')}</button></div>
    {:else}
      <div class="mcp-server-list">
        {#each servers as server, serverIndex (server)}
          <section class="mcp-server-card">
            <header><strong>{server.name || `${$t('settings.mcp.server')} ${serverIndex + 1}`}</strong><button type="button" class="ghost danger sm" on:click={() => removeServer(serverIndex)}>{$t('common.remove')}</button></header>
            <div class="mcp-fields">
              <label><span>{$t('settings.mcp.name')}</span><input value={server.name} on:input={(event) => updateServer(server, 'name', event.currentTarget.value)} placeholder="filesystem" /></label>
              <label><span>{$t('settings.mcp.transport')}</span><select value={server.type} on:change={(event) => updateServer(server, 'type', event.currentTarget.value)}><option value="stdio">stdio</option><option value="http">HTTP</option><option value="sse">SSE</option></select></label>
              {#if server.type === 'stdio'}
                <label class="full"><span>{$t('settings.mcp.command')}</span><input value={server.command} on:input={(event) => updateServer(server, 'command', event.currentTarget.value)} placeholder="/absolute/path/to/mcp-server" /></label>
              {:else}
                <label class="full"><span>{$t('settings.mcp.url')}</span><input value={server.url} on:input={(event) => updateServer(server, 'url', event.currentTarget.value)} placeholder="https://mcp.example.com" /></label>
                {#if server.type === 'sse'}
                  <label class="full"><span>{$t('settings.mcp.messageUrl')}</span><input value={server.messageUrl} on:input={(event) => updateServer(server, 'messageUrl', event.currentTarget.value)} placeholder="https://mcp.example.com/messages" /></label>
                {/if}
              {/if}
            </div>

            {#if server.type === 'stdio'}
              <div class="mcp-list-section"><div class="mcp-list-head"><strong>{$t('settings.mcp.args')}</strong><button type="button" class="ghost sm" on:click={() => addArg(server)}>+ {$t('common.add')}</button></div>
                {#each server.args as arg, index}
                  <div class="mcp-list-row"><input value={arg} on:input={(event) => updateArg(server, index, event.currentTarget.value)} placeholder="--argument" /><button type="button" class="ghost danger sm" on:click={() => removeArg(server, index)}>×</button></div>
                {/each}
              </div>
            {/if}

            <div class="mcp-pairs">
              <div class="mcp-list-section"><div class="mcp-list-head"><strong>{$t('settings.mcp.headers')}</strong><button type="button" class="ghost sm" on:click={() => addPair(server, 'headers')}>+ {$t('common.add')}</button></div>
                {#each server.headers as item, index}
                  <div class="mcp-pair-row"><input value={item.name} on:input={(event) => updatePair(server, 'headers', index, 'name', event.currentTarget.value)} placeholder={$t('settings.mcp.headerName')} /><input value={item.value} on:input={(event) => updatePair(server, 'headers', index, 'value', event.currentTarget.value)} placeholder={$t('settings.mcp.value')} /><button type="button" class="ghost danger sm" on:click={() => removePair(server, 'headers', index)}>×</button></div>
                {/each}
              </div>
              {#if server.type === 'stdio'}
                <div class="mcp-list-section"><div class="mcp-list-head"><strong>{$t('settings.mcp.env')}</strong><button type="button" class="ghost sm" on:click={() => addPair(server, 'env')}>+ {$t('common.add')}</button></div>
                  {#each server.env as item, index}
                    <div class="mcp-pair-row"><input value={item.name} on:input={(event) => updatePair(server, 'env', index, 'name', event.currentTarget.value)} placeholder="API_KEY" /><input value={item.value} on:input={(event) => updatePair(server, 'env', index, 'value', event.currentTarget.value)} placeholder={$t('settings.mcp.value')} /><button type="button" class="ghost danger sm" on:click={() => removePair(server, 'env', index)}>×</button></div>
                  {/each}
                </div>
              {/if}
            </div>
          </section>
        {/each}
      </div>
    {/if}
  </div>
  <p class="mcp-note">{$t('settings.mcp.applyHint')}</p>
</div>

<style>
  .mcp-editor { display: grid; gap: 16px; }
  .mcp-heading { display: flex; align-items: start; justify-content: space-between; gap: 16px; padding: 2px 2px 0; }
  .mcp-heading h2 { margin: 0; font-size: 19px; }.mcp-heading p { margin: 5px 0 0; max-width: 760px; }
  .mcp-card { overflow: hidden; }.mcp-actions { display: flex; flex-wrap: wrap; justify-content: end; gap: 8px; }.mcp-loading, .mcp-empty { padding: 40px 18px; color: var(--text-muted); font-size: 12px; }.mcp-empty { display: grid; justify-items: center; gap: 8px; }.mcp-empty strong { color: var(--text-secondary); }
  .mcp-server-list { display: grid; gap: 12px; padding: 14px; border-top: 1px solid var(--border-subtle); background: var(--bg-secondary); }.mcp-server-card { display: grid; gap: 14px; padding: 14px; border: 1px solid var(--border-subtle); border-radius: 8px; background: var(--bg); }.mcp-server-card > header, .mcp-list-head { display: flex; align-items: center; justify-content: space-between; gap: 10px; }.mcp-server-card > header strong { font-size: 13px; }.mcp-fields { display: grid; grid-template-columns: minmax(160px, 1fr) minmax(130px, .45fr); gap: 10px; }.mcp-fields label { display: grid; gap: 4px; color: var(--text-secondary); font-size: 12px; font-weight: 500; }.mcp-fields .full { grid-column: 1 / -1; }.mcp-fields input, .mcp-fields select, .mcp-list-row input, .mcp-pair-row input { min-width: 0; width: 100%; }
  .mcp-list-section { display: grid; gap: 8px; }.mcp-list-head strong { color: var(--text-secondary); font-size: 12px; }.mcp-list-row, .mcp-pair-row { display: grid; grid-template-columns: minmax(0, 1fr) 30px; gap: 8px; align-items: center; }.mcp-pairs { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; }.mcp-pair-row { grid-template-columns: minmax(0, .7fr) minmax(0, 1.3fr) 30px; }.mcp-list-row button, .mcp-pair-row button { width: 30px; min-height: 30px; padding: 0; font-size: 16px; line-height: 1; }.mcp-note { margin: 0 2px; color: var(--text-muted); font-size: 12px; line-height: 1.55; }
  :global(.mcp-session-overlay) { position: fixed; z-index: 40; inset: 0; display: grid; place-items: center; padding: 20px; background: var(--overlay); overflow: auto; }:global(.mcp-session-dialog) { width: min(900px, 100%); max-height: calc(100vh - 40px); overflow: auto; padding: 20px; border: 1px solid var(--border); border-radius: 12px; background: var(--bg); box-shadow: var(--modal-shadow); }:global(.mcp-session-head) { display: flex; align-items: center; justify-content: space-between; gap: 16px; margin-bottom: 16px; }:global(.mcp-session-head > div) { display: grid; gap: 3px; }:global(.mcp-session-head span) { color: var(--text-muted); font-size: 12px; }
  @media (max-width: 640px) { .mcp-heading { align-items: flex-start; flex-direction: column; }.mcp-heading > button { width: 100%; }.mcp-actions { justify-content: start; }.mcp-fields, .mcp-pairs { grid-template-columns: 1fr; }.mcp-pair-row { grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) 30px; } }
</style>
