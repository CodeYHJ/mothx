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
  let toolAppliesTo = { wechat: 'next_run', feishu: 'next_run' };
  let toolCatalog = { wechat: [], feishu: [] };
  let toolCatalogReady = { wechat: false, feishu: false };
  let toolSaving = { wechat: false, feishu: false };
  let toolCatalogGeneration = { wechat: 0, feishu: 0 };
  let toolStateGeneration = { wechat: 0, feishu: 0 };
  let configRequestGeneration = 0;

  $: syncFromStore($serveConfig);

  onDestroy(() => { stopWechatPolling(); });

  async function loadToolCatalog(platform) {
    const generation = ++toolCatalogGeneration[platform];
    toolCatalogReady[platform] = false;
    toolCatalogReady = toolCatalogReady;
    try {
      const data = await request(`/api/session-tools/catalog?platform=${platform}`);
      if (generation !== toolCatalogGeneration[platform]) return;
      toolCatalog[platform] = data?.tools || [];
      toolCatalog = toolCatalog;
      toolCatalogReady[platform] = true;
      toolCatalogReady = toolCatalogReady;
    } catch (err) {
      if (generation !== toolCatalogGeneration[platform]) return;
      toolCatalog[platform] = [];
      toolCatalog = toolCatalog;
      setError(err);
    }
  }

  async function loadChannelBindings() {
    let bindingError = null;
    try {
      const data = await request('/api/session-bindings');
      channelBindings = data?.bindings || [];
    } catch (err) {
      bindingError = err;
      channelBindings = [];
    }

    // Older serve runtimes may not expose the dedicated binding list yet;
    // session rows still carry the same channel metadata, so use them as a
    // compatibility fallback instead of rendering empty selectors.
    if (channelBindings.length === 0) {
      let sessionRows = $sessions || [];
      try {
        const sessionData = await request('/api/sessions?limit=1000');
        sessionRows = sessionData?.sessions || sessionRows;
      } catch (err) {
        if (!bindingError) bindingError = err;
      }
      channelBindings = sessionRows
        .filter((item) => (item.channelType === 'wechat' || item.channelType === 'feishu') && item.channelId)
        .map((item) => ({
          sessionId: item.id,
          channelType: item.channelType,
          channelId: item.channelId
        }));
    }
    if (bindingError && channelBindings.length === 0) {
      setError(bindingError);
    }

    const nextIdentity = { ...selectedIdentity };
    const nextBinding = { ...selectedBinding };
    for (const platform of ['wechat', 'feishu']) {
      const bindings = channelBindings.filter((item) => item.channelType === platform);
      if (!bindings.some((item) => item.channelId === nextIdentity[platform])) {
        nextIdentity[platform] = bindings[0]?.channelId || '';
      }
      const binding = bindings.find((item) => item.channelId === nextIdentity[platform]);
      nextBinding[platform] = binding?.sessionId || '';
    }
    selectedIdentity = nextIdentity;
    selectedBinding = nextBinding;

    // Keep binding/session selection usable even if a tool catalog or tool
    // settings request is temporarily unavailable.
    await Promise.all([
      loadToolCatalog('wechat'),
      loadToolCatalog('feishu'),
      loadSelectedChannelTools('wechat'),
      loadSelectedChannelTools('feishu')
    ]);
  }

  async function loadSelectedChannelTools(platform) {
    const generation = ++toolStateGeneration[platform];
    const sessionID = selectedBinding[platform];
    try {
      const data = sessionID
        ? await request(`/api/sessions/${encodeURIComponent(sessionID)}/channel-tools`)
        : { tools: [], appliesTo: 'next_run' };
      if (generation !== toolStateGeneration[platform] || sessionID !== selectedBinding[platform]) return;
      channelTools[platform] = data?.tools || [];
      toolAppliesTo[platform] = data?.appliesTo || 'next_run';
      channelTools = channelTools;
      toolAppliesTo = toolAppliesTo;
    } catch (err) {
      if (generation !== toolStateGeneration[platform]) return;
      channelTools[platform] = [];
      channelTools = channelTools;
      setError(err);
    }
  }

  onMount(() => {
    loadChannelBindings();
  });

  async function saveChannelTools(platform) {
    const sessionID = resolveSelectedBinding(platform);
    if (!sessionID) {
      setError($t('settings.channels.toolSessionRequired'));
      return;
    }
    if (!toolCatalogReady[platform]) {
      setError($t('settings.channels.toolsLoading'));
      return;
    }
    const configured = new Map(channelTools[platform].map((item) => [item.name || item.toolName, item.requestedEnabled ?? item.enabled]));
    const tools = toolCatalog[platform].map((tool) => ({
      name: tool.name,
      enabled: toolAvailable(tool) && (configured.has(tool.name) ? configured.get(tool.name) : tool.default !== false)
    }));
    const generation = ++toolStateGeneration[platform];
    toolSaving[platform] = true;
    toolSaving = toolSaving;
    try {
      const saved = await putJSON(`/api/sessions/${encodeURIComponent(sessionID)}/channel-tools`, { tools });
      if (generation !== toolStateGeneration[platform] || sessionID !== selectedBinding[platform]) return;
      channelTools[platform] = saved?.tools || tools;
      toolAppliesTo[platform] = saved?.appliesTo || 'next_run';
      channelTools = channelTools;
      toolAppliesTo = toolAppliesTo;
      await loadSelectedChannelTools(platform);
      setNotice($t('settings.channels.saved'));
    } catch (err) {
      if (generation === toolStateGeneration[platform]) setError(err);
    } finally {
      toolSaving[platform] = false;
      toolSaving = toolSaving;
    }
  }

  function resolveSelectedBinding(platform) {
    if (selectedBinding[platform]) return selectedBinding[platform];
    const identity = selectedIdentity[platform];
    const binding = channelBindings.find((item) => item.channelType === platform && item.channelId === identity);
    if (!binding?.sessionId) return '';
    selectedBinding[platform] = binding.sessionId;
    selectedBinding = selectedBinding;
    return binding.sessionId;
  }

  function identityOptions(platform) {
    return channelBindings.filter((item) => item.channelType === platform);
  }

  async function selectChannelIdentity(platform, channelID) {
    selectedIdentity[platform] = channelID;
    selectedIdentity = selectedIdentity;
    const binding = channelBindings.find((item) => item.channelType === platform && item.channelId === channelID);
    selectedBinding[platform] = binding?.sessionId || '';
    selectedBinding = selectedBinding;
    await loadSelectedChannelTools(platform);
  }
  // These are reactive-derived: Svelte only tracks dependencies referenced in
  // the expression, so we pass $sessions/channelBindings/selectedIdentity in
  // explicitly. Calling bindingOptions('feishu') from a template would only
  // see the literal string and never re-render when bindings load, leaving the
  // session <select> blank.
  $: feishuBindingOptions = buildBindingOptions('feishu', $sessions, channelBindings, selectedIdentity);
  $: wechatBindingOptions = buildBindingOptions('wechat', $sessions, channelBindings, selectedIdentity);

  function buildBindingOptions(platform, sessions, bindings, identity) {
    const options = [...(sessions || [])];
    const boundSessionID = bindings.find((item) =>
      item.channelType === platform && item.channelId === identity[platform]
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
    try {
      await loadChannelBindings();
      setNotice($t('settings.channels.saved'));
    } catch (err) {
      setError(err);
    }
  }

  // These take the per-platform states/catalog arrays as explicit args so the
  // template expressions reference channelTools/toolCatalog and Svelte
  // re-evaluates them after a save/refresh. Reading those stores inside the
  // function body (as before) would never re-run the checkbox on load.
  function toolEnabled(platform, name, states, catalog) {
    const configured = (states || []).find((item) => (item.name || item.toolName) === name);
    if (configured) return configured.requestedEnabled ?? configured.enabled;
    return (catalog || []).find((item) => item.name === name)?.default !== false;
  }

  function toolAvailable(tool) {
    return tool?.available !== false;
  }

  function toolUnavailableReason(tool) {
    return tool?.unavailableReason || $t('settings.channels.toolUnavailable');
  }

  function toolState(platform, name, states) {
    return (states || []).find((item) => (item.name || item.toolName) === name);
  }

  function toolStateLabel(platform, name, states) {
    const state = toolState(platform, name, states);
    if (!state) return '';
    if (!state.available) return '⛔';
    if (state.registered) return '✓';
    if (state.willRegister || state.effectiveEnabled) return '⏳';
    return '○';
  }

  function toolStateTitle(platform, name, states) {
    const state = toolState(platform, name, states);
    if (!state) return '';
    if (!state.available) return $t('settings.channels.toolUnavailable');
    if (state.registered) return $t('settings.channels.toolRegistered');
    if (state.willRegister || state.effectiveEnabled) return $t('settings.channels.toolNextRun');
    return $t('settings.channels.toolDisabled');
  }

  function toggleTool(platform, name, enabled) {
    const current = channelTools[platform].filter((item) => (item.name || item.toolName) !== name);
    channelTools[platform] = [...current, { name, requestedEnabled: enabled }];
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

  function channelPayload(platform) {
    if (platform === 'wechat') {
      return {
        enabled: Boolean(form.wechat.enabled),
        credPath: String(form.wechat.credPath || '').trim(),
        workDir: String(form.wechat.workDir || '').trim(),
        autoTyping: Boolean(form.wechat.autoTyping),
        allowedUsers: form.wechat.allowedUsers.map((item) => String(item || '').trim()).filter(Boolean)
      };
    }
    return {
      enabled: Boolean(form.feishu.enabled),
      appId: String(form.feishu.appID || '').trim(),
      appSecret: String(form.feishu.appSecret || '').trim(),
      workDir: String(form.feishu.workDir || '').trim(),
      allowedUsers: form.feishu.allowedUsers.map((item) => String(item || '').trim()).filter(Boolean)
    };
  }

  async function saveChannelConfig(platform, noticeKey = 'settings.channels.saved') {
    clearBanners();
    const generation = ++configRequestGeneration;
    saving = true;
    try {
      await patchJSON(`/api/serve/config/channels/${platform}`, channelPayload(platform));
      if (generation !== configRequestGeneration) return;
      await refreshAll();
      if (noticeKey) setNotice($t(noticeKey));
    } catch (err) {
      if (generation === configRequestGeneration) setError(err);
      throw err;
    } finally {
      if (generation === configRequestGeneration) saving = false;
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
    await saveChannelConfig('feishu', 'settings.channels.feishuSaved');
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
    await saveChannelConfig('wechat', 'settings.channels.wechatSaved');
  }

  async function disableWechat() {
    form.wechat.enabled = false;
    form = form;
    await saveChannelConfig('wechat', 'settings.channels.wechatDisabled');
  }

  async function startWechatLogin() {
    clearBanners();
    wechatOpen = true;
    wechatLogin = { state: 'starting' };
    try {
      await saveChannelConfig('wechat', '');
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
        <select class="channel-identity-select" bind:value={selectedIdentity.feishu} on:change={(event) => selectChannelIdentity('feishu', event.currentTarget.value)}>
          <option value="">未绑定身份</option>
          {#each identityOptions('feishu') as item (item.channelId)}<option value={item.channelId}>{item.channelId}</option>{/each}
        </select>
        <span>Session</span>
        <select class="channel-session-picker" bind:value={selectedBinding.feishu} disabled={!selectedIdentity.feishu} on:change={(event) => selectBinding('feishu', event.currentTarget.value)}>
          <option value="" disabled>未绑定 Session</option>
          {#each feishuBindingOptions as item (item.id)}
            <option value={item.id}>{item.title || item.preview || item.id} · {item.id}</option>
          {/each}
        </select>
      </div>
      <div class="channel-tools-config">
        <div class="channel-tools-head"><span>{$t('settings.channels.sessionTools')}</span><span class="hint">{$t('settings.channels.sessionToolsHint')}</span></div>
        <div class="hint">{toolAppliesTo.feishu === 'current' ? $t('settings.channels.toolAppliesCurrent') : $t('settings.channels.toolAppliesNext')}</div>
        <div class="channel-tools-list">
          {#each toolCatalog.feishu as tool}<label class="channel-tool-toggle" title={toolAvailable(tool) ? tool.name : toolUnavailableReason(tool)}><input type="checkbox" disabled={!toolAvailable(tool)} checked={toolEnabled('feishu', tool.name, channelTools.feishu, toolCatalog.feishu)} on:change={(e) => toggleTool('feishu', tool.name, e.currentTarget.checked)} /> <span>{tool.name}</span>{#if toolStateLabel('feishu', tool.name, channelTools.feishu)} <small class="sub tool-state-icon" title={toolStateTitle('feishu', tool.name, channelTools.feishu)}>{toolStateLabel('feishu', tool.name, channelTools.feishu)}</small>{/if}</label>{/each}
        </div>
        <div class="channel-tools-actions"><button type="button" class="ghost sm" disabled={toolSaving.feishu || !selectedBinding.feishu || !toolCatalogReady.feishu} on:click={() => saveChannelTools('feishu')}>{$t('settings.channels.saveTools')}</button></div>
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
        <select class="channel-identity-select" bind:value={selectedIdentity.wechat} on:change={(event) => selectChannelIdentity('wechat', event.currentTarget.value)}>
          <option value="">未绑定身份</option>
          {#each identityOptions('wechat') as item (item.channelId)}<option value={item.channelId}>{item.channelId}</option>{/each}
        </select>
        <span>Session</span>
        <select class="channel-session-picker" bind:value={selectedBinding.wechat} disabled={!selectedIdentity.wechat} on:change={(event) => selectBinding('wechat', event.currentTarget.value)}>
          <option value="" disabled>未绑定 Session</option>
          {#each wechatBindingOptions as item (item.id)}
            <option value={item.id}>{item.title || item.preview || item.id} · {item.id}</option>
          {/each}
        </select>
      </div>
      <div class="channel-tools-config">
        <div class="channel-tools-head"><span>{$t('settings.channels.sessionTools')}</span><span class="hint">{$t('settings.channels.sessionToolsHint')}</span></div>
        <div class="hint">{toolAppliesTo.wechat === 'current' ? $t('settings.channels.toolAppliesCurrent') : $t('settings.channels.toolAppliesNext')}</div>
        <div class="channel-tools-list">
          {#each toolCatalog.wechat as tool}<label class="channel-tool-toggle" title={toolAvailable(tool) ? tool.name : toolUnavailableReason(tool)}><input type="checkbox" disabled={!toolAvailable(tool)} checked={toolEnabled('wechat', tool.name, channelTools.wechat, toolCatalog.wechat)} on:change={(e) => toggleTool('wechat', tool.name, e.currentTarget.checked)} /> <span>{tool.name}</span>{#if toolStateLabel('wechat', tool.name, channelTools.wechat)} <small class="sub tool-state-icon" title={toolStateTitle('wechat', tool.name, channelTools.wechat)}>{toolStateLabel('wechat', tool.name, channelTools.wechat)}</small>{/if}</label>{/each}
        </div>
        <div class="channel-tools-actions"><button type="button" class="ghost sm" disabled={toolSaving.wechat || !selectedBinding.wechat || !toolCatalogReady.wechat} on:click={() => saveChannelTools('wechat')}>{$t('settings.channels.saveTools')}</button></div>
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
