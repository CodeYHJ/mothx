<script>
  import { onMount, onDestroy } from 'svelte';
  import { channels, sessions, serveConfig, refreshAll, setError, setNotice, clearBanners } from '../../lib/stores.js';
  import { del, postJSON, putJSON, patchJSON, request } from '../../lib/api.js';
  import { t } from '../../lib/preferences.js';
  import ListEditor from './ListEditor.svelte';

  let form = defaultForm();
  let lastRaw = '';
  let parseError = '';
  let saving = false;
  let feishuOpen = false;
  let feishuDraft = defaultForm().feishu;
  let wechatOpen = false;
  let wechatLogin = null;
  let wechatPoll = null;
  let channelBindings = [];
  let selectedBinding = { wechat: '', feishu: '' };
  let selectedIdentity = { wechat: '', feishu: '' };
  let channelTools = { wechat: [], feishu: [] };
  let toolCatalog = { wechat: [], feishu: [] };

  $: syncFromStore($serveConfig);

  onDestroy(() => { stopWechatPolling(); });

  async function loadToolCatalog(platform) {
    const data = await request(`/api/session-tools/catalog?platform=${platform}`);
    toolCatalog[platform] = data?.tools || [];
    toolCatalog = toolCatalog;
  }

  async function loadChannelBindings() {
    try {
      await Promise.all([loadToolCatalog('wechat'), loadToolCatalog('feishu')]);
      const data = await request('/api/session-bindings');
      channelBindings = data?.bindings || [];
      for (const platform of ['wechat', 'feishu']) {
        const bindings = channelBindings.filter((item) => item.channelType === platform);
        if (!bindings.some((item) => item.channelId === selectedIdentity[platform])) {
          selectedIdentity[platform] = bindings[0]?.channelId || '';
        }
        const binding = bindings.find((item) => item.channelId === selectedIdentity[platform]);
        selectedBinding[platform] = binding?.sessionId || '';
        await loadSelectedChannelTools(platform);
      }
      selectedIdentity = selectedIdentity;
      selectedBinding = selectedBinding;
    } catch (err) { setError(err); }
  }

  async function loadSelectedChannelTools(platform) {
    const sessionID = selectedBinding[platform];
    channelTools[platform] = sessionID
      ? (await request(`/api/sessions/${encodeURIComponent(sessionID)}/bindings`)).tools || []
      : [];
    channelTools = channelTools;
  }

  onMount(() => {
    loadChannelBindings();
  });

  async function saveChannelTools(platform) {
    const sessionID = selectedBinding[platform];
    if (!sessionID) return;
    const configured = new Map(channelTools[platform].map((item) => [item.toolName, item.enabled]));
    const tools = toolCatalog[platform].map((tool) => ({
      toolName: tool.name,
      enabled: configured.has(tool.name) ? configured.get(tool.name) : tool.default !== false
    }));
    await patchJSON(`/api/sessions/${encodeURIComponent(sessionID)}/bindings`, { tools });
    channelTools[platform] = tools;
    channelTools = channelTools;
    setNotice($t('settings.channels.saved'));
  }

  function identityOptions(platform) {
    return channelBindings.filter((item) => item.channelType === platform);
  }

  function selectChannelIdentity(platform) {
    selectedIdentity = selectedIdentity;
    loadChannelBindings();
  }
  function bindingOptions(platform) {
    const options = [...($sessions || [])];
    const boundSessionID = channelBindings.find((item) =>
      item.channelType === platform && item.channelId === selectedIdentity[platform]
    )?.sessionId;
    // /api/sessions is intentionally paginated. Keep a selected bound session
    // visible even when it falls outside the current page of recent sessions.
    if (boundSessionID && !options.some((item) => item?.id === boundSessionID)) {
      options.unshift({ id: boundSessionID, title: `已绑定 Session · ${boundSessionID}`, bound: true });
    }
    return options;
  }

  $: syncBoundSessionSelection(channelBindings, $sessions);

  function syncBoundSessionSelection(bindings) {
    let changed = false;
    for (const platform of ['wechat', 'feishu']) {
      const platformBindings = bindings.filter((item) => item.channelType === platform);
      const binding = platformBindings.find((item) => item.channelId === selectedIdentity[platform]);
      if (binding && selectedBinding[platform] !== binding.sessionId) {
        selectedBinding[platform] = binding.sessionId;
        changed = true;
      }
    }
    if (changed) selectedBinding = selectedBinding;
  }

  async function selectBinding(platform, sessionID) {
    const currentBinding = channelBindings.find((binding) => binding.channelType === platform && binding.channelId === selectedIdentity[platform]);
    if (!currentBinding) {
      setError('该通道尚未有可转移的用户绑定；请先让对应微信或飞书用户发送一条消息以创建绑定 Session。');
      return;
    }
    if (currentBinding.sessionId !== sessionID) {
      try {
        await putJSON(`/api/sessions/${encodeURIComponent(sessionID)}/bindings`, {
          channelType: platform,
          channelId: currentBinding.channelId,
          fromSessionId: currentBinding.sessionId,
          toSessionId: sessionID
        });
      } catch (err) {
        setError(err);
        return;
      }
    }
    selectedBinding[platform] = sessionID;
    form = form;
    try {
      await saveConfig('');
      await loadChannelBindings();
      setNotice($t('settings.channels.saved'));
    } catch (err) {
      setError(err);
    }
  }

  function toolEnabled(platform, name) {
    const configured = channelTools[platform].find((item) => item.toolName === name);
    if (configured) return configured.enabled;
    return toolCatalog[platform].find((item) => item.name === name)?.default !== false;
  }

  function toolAvailable(tool) {
    return true;
  }

  function toggleTool(platform, name, enabled) {
    const current = channelTools[platform].filter((item) => item.toolName !== name);
    channelTools[platform] = [...current, { toolName: name, enabled }];
    channelTools = channelTools;
  }

  function defaultForm() {
    return {
      wechat: { enabled: false, credPath: '', workDir: '', autoTyping: true, allowedUsers: [] },
      feishu: { enabled: false, appID: '', appSecret: '', workDir: '', allowedUsers: [] }
    };
  }

  function syncFromStore(raw) {
    if (raw === lastRaw) return;
    lastRaw = raw;
    try {
      const cfg = JSON.parse(raw || '{}');
      form = {
        wechat: {
          enabled: Boolean(cfg.channels?.wechat?.enabled ?? cfg.features?.wechat),
          credPath: stringValue(cfg.channels?.wechat?.credPath),
          workDir: stringValue(cfg.channels?.wechat?.workDir),
          autoTyping: readBool(cfg.channels?.wechat?.autoTyping, true),
          allowedUsers: arrayValue(cfg.channels?.wechat?.allowedUsers)
        },
        feishu: {
          enabled: Boolean(cfg.channels?.feishu?.enabled ?? cfg.features?.feishu),
          appID: stringValue(cfg.channels?.feishu?.appId),
          appSecret: stringValue(cfg.channels?.feishu?.appSecret),
          workDir: stringValue(cfg.channels?.feishu?.workDir),
          allowedUsers: arrayValue(cfg.channels?.feishu?.allowedUsers)
        }
      };
      parseError = '';
    } catch (err) {
      parseError = err instanceof Error ? err.message : String(err);
      form = defaultForm();
    }
  }

  function stringValue(value) {
    return typeof value === 'string' ? value : '';
  }

  function readBool(value, fallback) {
    return typeof value === 'boolean' ? value : fallback;
  }

  function arrayValue(value) {
    return Array.isArray(value) ? value.map((item) => String(item ?? '')) : [];
  }

  function statusFor(name) {
    return $channels.find((item) => item.name === name) || { name, enabled: false, connected: false };
  }

  function statusLabel(name) {
    const status = statusFor(name);
    if (!status.enabled) return $t('common.disabledState');
    return status.connected ? $t('common.connected') : $t('common.disconnected');
  }

  function buildConfig() {
    const cfg = JSON.parse(lastRaw || '{}');
    cfg.features = ensureObject(cfg, 'features');
    cfg.channels = ensureObject(cfg, 'channels');
    cfg.channels.wechat = ensureObject(cfg.channels, 'wechat');
    cfg.channels.feishu = ensureObject(cfg.channels, 'feishu');

    cfg.features.wechat = Boolean(form.wechat.enabled);
    cfg.features.feishu = Boolean(form.feishu.enabled);

    cfg.channels.wechat.enabled = Boolean(form.wechat.enabled);
    cfg.channels.wechat.autoTyping = Boolean(form.wechat.autoTyping);
    writeString(cfg.channels.wechat, 'credPath', form.wechat.credPath);
    writeString(cfg.channels.wechat, 'workDir', form.wechat.workDir);
    writeList(cfg.channels.wechat, 'allowedUsers', form.wechat.allowedUsers);

    cfg.channels.feishu.enabled = Boolean(form.feishu.enabled);
    writeString(cfg.channels.feishu, 'appId', form.feishu.appID);
    writeString(cfg.channels.feishu, 'appSecret', form.feishu.appSecret);
    writeString(cfg.channels.feishu, 'workDir', form.feishu.workDir);
    writeList(cfg.channels.feishu, 'allowedUsers', form.feishu.allowedUsers);
    return cfg;
  }

  function ensureObject(parent, key) {
    if (!parent[key] || typeof parent[key] !== 'object' || Array.isArray(parent[key])) parent[key] = {};
    return parent[key];
  }

  function writeString(target, key, value) {
    const text = String(value || '').trim();
    if (text) target[key] = text;
    else delete target[key];
  }

  function writeList(target, key, values) {
    const list = values.map((item) => String(item || '').trim()).filter(Boolean);
    if (list.length > 0) target[key] = list;
    else delete target[key];
  }

  async function saveConfig(noticeKey = 'settings.channels.saved') {
    clearBanners();
    saving = true;
    try {
      const saved = await putJSON('/api/serve/config', buildConfig());
      serveConfig.set(JSON.stringify(saved, null, 2));
      await refreshAll();
      if (noticeKey) setNotice($t(noticeKey));
      return saved;
    } catch (err) {
      setError(err);
      throw err;
    } finally {
      saving = false;
    }
  }

  function openFeishu() {
    feishuDraft = cloneChannel(form.feishu);
    feishuOpen = true;
  }

  function closeFeishu() {
    feishuOpen = false;
  }

  async function saveFeishu() {
    if (feishuDraft.enabled && (!feishuDraft.appID.trim() || !feishuDraft.appSecret.trim())) {
      setError($t('settings.channels.feishuKeyRequired'));
      return;
    }
    form.feishu = cloneChannel(feishuDraft);
    form = form;
    await saveConfig('settings.channels.feishuSaved');
    feishuOpen = false;
  }

  function cloneChannel(channel) {
    return {
      ...channel,
      allowedUsers: [...(channel.allowedUsers || [])]
    };
  }

  function addDraftUser(target) {
    target.allowedUsers = [...target.allowedUsers, ''];
  }

  function removeDraftUser(target, index) {
    target.allowedUsers = target.allowedUsers.filter((_, i) => i !== index);
  }

  function addListItem(list) {
    list.push('');
    form = form;
  }

  function removeListItem(list, index) {
    list.splice(index, 1);
    form = form;
  }

  async function saveWechatConfig() {
    await saveConfig('settings.channels.wechatSaved');
  }

  async function disableWechat() {
    form.wechat.enabled = false;
    form = form;
    await saveConfig('settings.channels.wechatDisabled');
  }

  async function startWechatLogin() {
    clearBanners();
    wechatOpen = true;
    wechatLogin = { state: 'starting' };
    try {
      await saveConfig('');
      wechatLogin = await postJSON('/api/channels/wechat/login', {});
      startWechatPolling();
    } catch (err) {
      setError(err);
    }
  }

  function startWechatPolling() {
    stopWechatPolling();
    wechatPoll = window.setInterval(loadWechatLogin, 1800);
    loadWechatLogin();
  }

  function stopWechatPolling() {
    if (wechatPoll) window.clearInterval(wechatPoll);
    wechatPoll = null;
  }

  async function loadWechatLogin() {
    try {
      wechatLogin = await request('/api/channels/wechat/login');
      if (wechatLogin?.state === 'confirmed') {
        stopWechatPolling();
        form.wechat.enabled = true;
        form = form;
        await refreshAll();
        setNotice($t('settings.channels.wechatEnabled'));
      } else if (wechatLogin?.state === 'error' || wechatLogin?.state === 'cancelled') {
        stopWechatPolling();
      }
    } catch (err) {
      stopWechatPolling();
      setError(err);
    }
  }

  async function closeWechatLogin() {
    stopWechatPolling();
    if (wechatLogin && !['confirmed', 'error', 'cancelled'].includes(wechatLogin.state)) {
      try {
        await del('/api/channels/wechat/login');
      } catch {
        // Closing the modal should not mask the current page state.
      }
    }
    wechatOpen = false;
  }

  function qrEmbedSrc(value) {
    const text = String(value || '').trim();
    if (!text || text.startsWith('/') || text.startsWith('http://') || text.startsWith('https://') || text.startsWith('data:')) return text;
    return `data:image/png;base64,${text}`;
  }

  function openWechatQRTab() {
    const url = wechatLogin?.qrOpenUrl || wechatLogin?.qrUrl || '';
    if (!url) return;
    const win = window.open(qrEmbedSrc(url), '_blank');
    if (!win) setError($t('settings.channels.popupBlocked'));
  }
</script>

<div class="page-toolbar embedded">
  <button type="button" class="ghost" on:click={refreshAll}>{$t('common.refresh')}</button>
</div>

{#if parseError}
  <p class="error-text">{$t('settings.app.parseError', { error: parseError })}</p>
{/if}

<div class="channel-settings-grid">
  <div class="card channel-config-card">
    <div class="card-head">
      <div>
        <h3>Feishu</h3>
        <span class="hint">{$t('settings.channels.feishuHint')}</span>
      </div>
      <span class="pill" class:on={statusFor('feishu').connected} class:off={!statusFor('feishu').connected}>
        {statusLabel('feishu')}
      </span>
    </div>
    <div class="channel-card-body">
      <dl class="kv compact">
        <dt>App ID</dt><dd>{form.feishu.appID || $t('common.uninitialized')}</dd>
        <dt>{$t('sessions.workDir')}</dt><dd>{form.feishu.workDir || $t('common.uninitialized')}</dd>
      </dl>
      <div class="channel-session-select">
        <span>Session 身份</span>
        <select class="channel-identity-select" bind:value={selectedIdentity.feishu} on:change={() => selectChannelIdentity('feishu')}>
          <option value="">未绑定身份</option>
          {#each identityOptions('feishu') as item (item.channelId)}<option value={item.channelId}>{item.channelId}</option>{/each}
        </select>
        <span>Session</span>
        <select class="channel-session-picker" bind:value={selectedBinding.feishu} disabled={!selectedIdentity.feishu} on:change={(event) => selectBinding('feishu', event.currentTarget.value)}>
          <option value="" disabled>未绑定 Session</option>
          {#each bindingOptions('feishu') as item (item.id)}
            <option value={item.id}>{item.title || item.preview || item.id} · {item.id}</option>
          {/each}
        </select>
      </div>
      <div class="channel-tools-config">
        <div class="channel-tools-head"><span>工具注册</span><span class="hint">保存后对该绑定 session 生效</span></div>
        <div class="channel-tools-list">
          {#each toolCatalog.feishu as tool}<label class="channel-tool-toggle"><input type="checkbox" checked={toolEnabled('feishu', tool.name)} on:change={(e) => toggleTool('feishu', tool.name, e.currentTarget.checked)} /> <span>{tool.name}</span></label>{/each}
        </div>
        <div class="channel-tools-actions"><button type="button" class="ghost sm" on:click={() => saveChannelTools('feishu')}>保存工具</button></div>
      </div>
      <div class="channel-card-actions"><button type="button" class="primary" on:click={openFeishu}>{$t('settings.channels.configure')}</button></div>
    </div>
  </div>

  <div class="card channel-config-card">
    <div class="card-head">
      <div>
        <h3>WeChat</h3>
        <span class="hint">{$t('settings.channels.wechatHint')}</span>
      </div>
      <span class="pill" class:on={statusFor('wechat').connected} class:off={!statusFor('wechat').connected}>
        {statusLabel('wechat')}
      </span>
    </div>
    <div class="channel-card-body">
      <div class="form-grid compact-grid">
        <label><span>{$t('settings.serve.wechatCred')}</span><input bind:value={form.wechat.credPath} placeholder="wechat-credentials.json" /></label>
        <label><span>{$t('settings.serve.wechatWorkDir')}</span><input bind:value={form.wechat.workDir} placeholder="/home/user/project" /></label>
        <label class="checkbox"><input type="checkbox" bind:checked={form.wechat.autoTyping} /> <span>{$t('settings.serve.autoTyping')}</span></label>
      </div>
      <ListEditor title={$t('settings.serve.wechatUsers')} list={form.wechat.allowedUsers} onAdd={() => addListItem(form.wechat.allowedUsers)} onRemove={(i) => removeListItem(form.wechat.allowedUsers, i)} />
      <div class="channel-session-select">
        <span>Session 身份</span>
        <select class="channel-identity-select" bind:value={selectedIdentity.wechat} on:change={() => selectChannelIdentity('wechat')}>
          <option value="">未绑定身份</option>
          {#each identityOptions('wechat') as item (item.channelId)}<option value={item.channelId}>{item.channelId}</option>{/each}
        </select>
        <span>Session</span>
        <select class="channel-session-picker" bind:value={selectedBinding.wechat} disabled={!selectedIdentity.wechat} on:change={(event) => selectBinding('wechat', event.currentTarget.value)}>
          <option value="" disabled>未绑定 Session</option>
          {#each bindingOptions('wechat') as item (item.id)}
            <option value={item.id}>{item.title || item.preview || item.id} · {item.id}</option>
          {/each}
        </select>
      </div>
      <div class="channel-tools-config">
        <div class="channel-tools-head"><span>工具注册</span><span class="hint">保存后对该绑定 session 生效</span></div>
        <div class="channel-tools-list">
          {#each toolCatalog.wechat as tool}<label class="channel-tool-toggle"><input type="checkbox" checked={toolEnabled('wechat', tool.name)} on:change={(e) => toggleTool('wechat', tool.name, e.currentTarget.checked)} /> <span>{tool.name}</span></label>{/each}
        </div>
        <div class="channel-tools-actions"><button type="button" class="ghost sm" on:click={() => saveChannelTools('wechat')}>保存工具</button></div>
      </div>
      <div class="channel-card-actions"><button type="button" class="ghost" disabled={saving} on:click={saveWechatConfig}>{$t('common.save')}</button>
        <button type="button" class="primary" disabled={saving} on:click={startWechatLogin}>{$t(form.wechat.enabled ? 'settings.channels.wechatRelogin' : 'settings.channels.wechatScanEnable')}</button>
        {#if form.wechat.enabled}
          <button type="button" class="ghost danger" disabled={saving} on:click={disableWechat}>{$t('common.disable')}</button>
        {/if}
      </div>
    </div>
  </div>
</div>

{#if feishuOpen}
  <div class="channel-modal-overlay" role="dialog" aria-modal="true" aria-label={$t('settings.channels.feishuConfig')}>
    <div class="channel-modal">
      <header>
        <div>
          <h3>{$t('settings.channels.feishuConfig')}</h3>
          <span class="hint">{$t('settings.channels.feishuConfigHint')}</span>
        </div>
        <button type="button" class="ghost sm" on:click={closeFeishu}>{$t('common.close')}</button>
      </header>
      <div class="form-grid">
        <label class="checkbox"><input type="checkbox" bind:checked={feishuDraft.enabled} /> <span>{$t('common.enabled')}</span></label>
        <label><span>{$t('settings.serve.feishuAppID')}</span><input bind:value={feishuDraft.appID} /></label>
        <label><span>{$t('settings.serve.feishuAppSecret')}</span><input type="password" bind:value={feishuDraft.appSecret} /></label>
        <label><span>{$t('settings.serve.feishuWorkDir')}</span><input bind:value={feishuDraft.workDir} placeholder="/home/user/project" /></label>
      </div>
      <div class="form-body">
        <div class="list-editor">
          <div class="list-head">
            <span>{$t('settings.serve.feishuUsers')}</span>
            <button type="button" class="ghost sm" on:click={() => addDraftUser(feishuDraft)}>{$t('common.add')}</button>
          </div>
          {#each feishuDraft.allowedUsers as user, i (i)}
            <div class="inline-row">
              <input bind:value={feishuDraft.allowedUsers[i]} />
              <button type="button" class="ghost sm" on:click={() => removeDraftUser(feishuDraft, i)}>{$t('common.remove')}</button>
            </div>
          {/each}
        </div>
      </div>
      <footer>
        <button type="button" class="ghost" on:click={closeFeishu}>{$t('dirBrowser.cancel')}</button>
        <button type="button" class="primary" disabled={saving} on:click={saveFeishu}>{$t('common.save')}</button>
      </footer>
    </div>
  </div>
{/if}

{#if wechatOpen}
  <div class="channel-modal-overlay" role="dialog" aria-modal="true" aria-label={$t('settings.channels.wechatLogin')}>
    <div class="channel-modal qr-modal">
      <header>
        <div>
          <h3>{$t('settings.channels.wechatLogin')}</h3>
          <span class="hint">{$t('settings.channels.wechatLoginHint')}</span>
        </div>
        <button type="button" class="ghost sm" on:click={closeWechatLogin}>{$t('common.close')}</button>
      </header>
      <div class="qr-panel">
        <p class="empty">
          {#if wechatLogin?.qrOpenUrl || wechatLogin?.qrUrl}
            {$t('settings.channels.wechatOpenQrHint')}
          {:else if wechatLogin?.state === 'starting'}
            {$t('common.loading')}
          {:else}
            {$t('settings.channels.wechatNoQr')}
          {/if}
        </p>
        <div class="qr-status">
          <span class="pill" class:on={wechatLogin?.state === 'confirmed'} class:off={wechatLogin?.state === 'error' || wechatLogin?.state === 'cancelled'}>
            {$t(`settings.channels.wechatState.${wechatLogin?.state || 'idle'}`)}
          </span>
          {#if wechatLogin?.userId}
            <code>{wechatLogin.userId}</code>
          {/if}
          {#if wechatLogin?.error}
            <p class="error-text">{wechatLogin.error}</p>
          {/if}
        </div>
      </div>
      <footer>
        <button type="button" class="ghost" on:click={closeWechatLogin}>{$t('dirBrowser.cancel')}</button>
        <button type="button" class="ghost" disabled={!wechatLogin?.qrUrl && !wechatLogin?.qrOpenUrl} on:click={openWechatQRTab}>{$t('settings.channels.openQrTab')}</button>
        <button type="button" class="primary" on:click={startWechatLogin}>{$t('settings.channels.refreshQr')}</button>
      </footer>
    </div>
  </div>
{/if}
