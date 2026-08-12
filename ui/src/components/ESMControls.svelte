<script>
  import { onDestroy } from 'svelte';
  import { createESM, getESM, updateESM, pauseESM, resumeESM, clearESM, setESMBudget, addESMGuidance } from '../lib/esm.js';

  export let sessionID = '';
  export let compact = false;
  export let onChanged = () => {};

  let snapshot = { status: 'none' };
  let loading = false;
  let error = '';
  let showCreate = false;
  let showDrawer = false;
  let showEdit = false;
  let showBudget = false;
  let showClear = false;
  let guidanceText = '';
  let objective = '';
  let budgetText = '';
  let polling = 0;
  let lastSessionID = '';

  $: if (sessionID && sessionID !== lastSessionID) {
    lastSessionID = sessionID;
    load();
  }

  $: if (snapshot.status && snapshot.status !== 'none' && !polling) {
    polling = window.setInterval(load, 5000);
  }

  onDestroy(() => { if (polling) clearInterval(polling); });

  async function load() {
    if (!sessionID) { snapshot = { status: 'none' }; return; }
    try { snapshot = await getESM(sessionID); onChanged(snapshot); } catch (err) { error = err?.message || String(err); }
  }

  function beginCreate() { error = ''; objective = ''; budgetText = ''; showCreate = true; }
  function openDrawer() { showDrawer = true; load(); }
  function closeAll() { showCreate = false; showEdit = false; showBudget = false; showClear = false; }
  function budgetValue() { const value = budgetText.trim(); return value === '' || value.toLowerCase() === 'off' ? null : Number(value); }

  async function submit(action) {
    if (loading) return;
    loading = true; error = '';
    try {
      let next;
      if (action === 'create') next = await createESM(sessionID, { objective: objective.trim(), tokenBudget: budgetValue() });
      if (action === 'edit') next = await updateESM(sessionID, { objective: objective.trim(), version: snapshot.version });
      if (action === 'budget') next = await setESMBudget(sessionID, budgetValue(), snapshot.version);
      if (action === 'pause') next = await pauseESM(sessionID);
      if (action === 'resume') next = await resumeESM(sessionID);
      if (action === 'clear') next = await clearESM(sessionID);
      if (action === 'guidance') next = await addESMGuidance(sessionID, guidanceText.trim(), snapshot.version);
      snapshot = next || { status: 'none' }; onChanged(snapshot); closeAll();
      if (action === 'create' || action === 'resume' || action === 'guidance') showDrawer = true;
      if (action === 'guidance') guidanceText = '';
    } catch (err) { error = err?.message || String(err); } finally { loading = false; }
  }

  function statusLabel(status) {
    return ({ none: '未启用', active: '运行中', paused: '已暂停', blocked: '已阻塞', budget_limited: '预算已用尽', usage_limited: '额度受限', complete_candidate: '等待验证', complete: '已完成', failed_recovery: '恢复失败' }[status] || status || '未知');
  }
  $: hasObjective = snapshot.status && snapshot.status !== 'none';
  $: canPause = ['active', 'waiting_approval', 'waiting_user'].includes(snapshot.status);
  $: canResume = ['paused', 'blocked', 'failed_recovery', 'usage_limited'].includes(snapshot.status);
  $: canEdit = hasObjective && snapshot.status !== 'complete';
</script>

<div class:compact class="esm-controls">
  {#if !hasObjective}
    <button type="button" class="esm-entry" on:click={beginCreate}>ESM <span class="muted">未启用</span></button>
  {:else}
    <button type="button" class="esm-entry" on:click={openDrawer} title={snapshot.objective || ''}>
      <span class="status-dot" class:active={snapshot.status === 'active'}></span>ESM <strong>{statusLabel(snapshot.status)}</strong>
    </button>
  {/if}

  {#if showCreate}
    <div class="esm-overlay" role="presentation" on:click={(e) => e.target === e.currentTarget && closeAll()}>
      <div class="esm-modal" role="dialog" aria-modal="true" aria-label="创建 ESM 任务">
        <header><strong>创建 ESM 任务</strong><button class="icon-btn" on:click={closeAll} aria-label="关闭">×</button></header>
        <label>目标<textarea bind:value={objective} rows="4" placeholder="例如：完成 API 迁移，运行测试并修复回归问题"></textarea></label>
        <label>Token 预算<input bind:value={budgetText} inputmode="numeric" placeholder="不限制" /></label>
        <p class="hint">任务由服务端持久化管理。关闭浏览器不会停止任务，并会持续消耗模型额度，直到完成、暂停、阻塞或预算耗尽。</p>
        {#if error}<p class="error">{error}</p>{/if}
        <footer><button class="ghost" on:click={closeAll}>取消</button><button class="primary" disabled={loading || !objective.trim()} on:click={() => submit('create')}>{loading ? '创建中…' : '创建并开始'}</button></footer>
      </div>
    </div>
  {/if}

  {#if showDrawer}
    <div class="esm-overlay" role="presentation" on:click={(e) => e.target === e.currentTarget && (showDrawer = false)}>
      <div class="esm-drawer" role="dialog" aria-modal="true" aria-label="ESM 任务">
        <header class="esm-drawer-header">
          <div class="esm-title-block">
            <strong>ESM 任务</strong>
            <div class="esm-status-line">
              <span class="esm-status-badge" class:running={snapshot.status === 'active'}>{statusLabel(snapshot.status)}</span>
              {#if snapshot.phase}<span class="esm-phase-separator">·</span><span class="esm-phase">{snapshot.phase}</span>{/if}
            </div>
          </div>
          <button type="button" class="icon-btn" on:click={() => (showDrawer = false)} aria-label="关闭">×</button>
        </header>
        <div class="esm-body">
          <div class="action-row"><button class="ghost sm" on:click={() => (showEdit = true)} disabled={!canEdit}>编辑目标</button><button class="ghost sm" on:click={() => (showBudget = true)} disabled={!canEdit}>调整预算</button></div>
          <section class="esm-card"><small>目标</small><p>{snapshot.objective || '无目标'}</p><small>进度</small><p>{snapshot.progressSummary || '等待执行'}</p>{#if snapshot.remainingWork?.length}<small>剩余工作</small><ul>{#each snapshot.remainingWork as item}<li>{item}</li>{/each}</ul>{/if}</section>
          <section class="esm-card"><small>给后台任务的指导</small><textarea class="guidance-input" bind:value={guidanceText} rows="2" placeholder="例如：优先修复失败测试，不要扩大改动范围"></textarea><button class="primary sm" disabled={loading || !guidanceText.trim()} on:click={() => submit('guidance')}>发送指导</button></section>
          {#if snapshot.guidance?.length}<section class="esm-card"><small>待处理指导</small><ul>{#each snapshot.guidance as item}<li>{item.guidance}</li>{/each}</ul></section>{/if}
          <section class="esm-card usage"><small>用量</small><strong>{snapshot.tokensUsed || 0}{snapshot.tokenBudget ? ` / ${snapshot.tokenBudget}` : ''} tokens</strong><span>{Math.round((snapshot.timeUsedMs || 0) / 60000)}m</span></section>
          {#if snapshot.blockedReason}<section class="esm-card warning"><small>阻塞原因</small><p>{snapshot.blockedReason}</p></section>{/if}
          {#if snapshot.completionReview}<section class="esm-card"><small>验证审查</small><p>{snapshot.completionReview}</p></section>{/if}
          {#if error}<p class="error">{error}</p>{/if}
        </div>
        <footer class="drawer-actions">
          {#if canPause}<button class="ghost" disabled={loading} on:click={() => submit('pause')}>暂停任务</button>{/if}
          {#if canResume}<button class="primary" disabled={loading} on:click={() => submit('resume')}>恢复任务</button>{/if}
          {#if hasObjective && snapshot.status !== 'complete'}<button class="danger ghost" disabled={loading} on:click={() => (showClear = true)}>清除任务</button>{/if}
        </footer>
      </div>
    </div>
  {/if}

  {#if showEdit || showBudget || showClear}
    <div class="esm-overlay nested" role="presentation">
      <div class="esm-modal" role="dialog" aria-modal="true">
        {#if showEdit}<header><strong>编辑 ESM 目标</strong><button class="icon-btn" on:click={closeAll}>×</button></header><label>目标<textarea bind:value={objective} rows="5">{snapshot.objective}</textarea></label><p class="hint">保存后将创建新的目标版本，历史记录保留。</p><footer><button class="ghost" on:click={closeAll}>取消</button><button class="primary" disabled={loading || !objective.trim()} on:click={() => submit('edit')}>保存新版本</button></footer>
        {:else if showBudget}<header><strong>调整执行预算</strong><button class="icon-btn" on:click={closeAll}>×</button></header><p>已用：{snapshot.tokensUsed || 0} tokens</p><label>新预算<input bind:value={budgetText} inputmode="numeric" placeholder="不限制" /></label><footer><button class="ghost" on:click={closeAll}>取消</button><button class="primary" disabled={loading} on:click={() => submit('budget')}>保存预算</button></footer>
        {:else}<header><strong>清除 ESM 任务</strong><button class="icon-btn" on:click={closeAll} aria-label="关闭">×</button></header><p>后续自动执行将停止，历史 Run、报告和审计记录会保留。</p><footer><button class="ghost" on:click={closeAll}>否</button><button class="danger" disabled={loading} on:click={() => submit('clear')}>是，清除任务</button></footer>{/if}
        {#if error}<p class="error">{error}</p>{/if}
      </div>
    </div>
  {/if}
</div>

<style>
  .esm-controls {
    position: relative;
    z-index: 1;
    width: 100%;
  }

  /* Match the existing runtime/tool controls in Chat.svelte. */
  .esm-entry {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    width: 100%;
    min-height: 30px;
    padding: 0 9px;
    border: 0;
    border-radius: 7px;
    background: var(--bg-secondary);
    color: var(--text-secondary);
    font-size: 12px;
    font-weight: 500;
    line-height: 1;
    text-align: left;
    white-space: nowrap;
    cursor: pointer;
    transition: background .12s, color .12s;
  }
  .esm-entry:hover,
  .esm-entry:focus-visible {
    background: var(--bg-hover);
    color: var(--text);
  }
  .esm-entry:focus-visible {
    outline: 2px solid var(--primary);
    outline-offset: 2px;
  }
  .esm-entry strong {
    color: inherit;
    font-weight: 650;
  }
  .muted,
  small,
  .hint {
    color: var(--text-muted);
    font-size: 11px;
  }
  .status-dot {
    width: 7px;
    height: 7px;
    flex: 0 0 auto;
    border-radius: 50%;
    background: var(--text-muted);
  }
  .status-dot.active {
    background: var(--accent);
    box-shadow: 0 0 0 3px var(--accent-bg);
  }

  .esm-overlay {
    position: fixed;
    inset: 0;
    z-index: 80;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 16px;
    background: var(--overlay);
  }
  .esm-overlay.nested { z-index: 90; }
  .esm-modal,
  .esm-drawer {
    color: var(--text);
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: var(--modal-shadow);
  }
  .esm-modal {
    width: min(560px, 100%);
    padding: 16px;
  }
  .esm-drawer {
    display: flex;
    flex-direction: column;
    width: min(590px, 100%);
    height: min(760px, 100%);
  }
  .esm-modal header,
  .esm-drawer-header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 12px;
  }
  .esm-drawer-header {
    min-height: 36px;
    margin-bottom: 0;
    padding: 0 16px 12px;
  }
  .esm-title-block {
    display: grid;
    gap: 5px;
    min-width: 0;
  }
  .esm-title-block > strong {
    color: var(--text);
    font-size: 14px;
    font-weight: 650;
    line-height: 1.2;
  }
  .esm-status-line {
    display: inline-flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 5px;
    color: var(--text-muted);
    font-size: 11px;
    line-height: 1.2;
  }
  .esm-status-badge {
    display: inline-flex;
    align-items: center;
    min-height: 20px;
    padding: 0 7px;
    border: 1px solid var(--border);
    border-radius: 999px;
    background: var(--bg-secondary);
    color: var(--text-secondary);
    font-size: 11px;
    font-weight: 600;
  }
  .esm-status-badge.running {
    border-color: var(--accent-border);
    background: var(--accent-bg);
    color: var(--accent-text);
  }
  .esm-phase-separator { color: var(--text-muted); }
  .esm-phase {
    color: var(--text-secondary);
    font-weight: 500;
    text-transform: capitalize;
  }

  .icon-btn {
    min-height: 28px;
    padding: 0 8px;
    border: 0;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-secondary);
    font-size: 18px;
    line-height: 1;
    cursor: pointer;
  }
  .icon-btn:hover { background: var(--bg-hover); color: var(--text); }
  .esm-modal label {
    display: grid;
    gap: 4px;
    margin: 12px 0;
    color: var(--text-secondary);
    font-size: 13px;
    font-weight: 500;
  }
  .esm-modal textarea,
  .esm-modal input,
  .guidance-input {
    width: 100%;
    padding: 8px 10px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--bg);
    color: var(--text);
    font: inherit;
  }
  .esm-modal textarea:focus,
  .esm-modal input:focus,
  .guidance-input:focus {
    outline: none;
    border-color: var(--primary);
    box-shadow: 0 0 0 2px var(--focus-ring);
  }
  .esm-modal footer,
  .drawer-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 12px;
  }
  .esm-body {
    flex: 1;
    overflow: auto;
    padding: 0 16px 12px;
  }
  .drawer-actions {
    padding: 12px 16px;
    border-top: 1px solid var(--border-subtle);
  }
  .esm-card {
    margin: 10px 0;
    padding: 10px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--bg);
  }
  .esm-card p {
    margin: 4px 0 10px;
    color: var(--text-secondary);
    white-space: pre-wrap;
    line-height: 1.5;
  }
  .esm-card ul { margin: 4px 0; padding-left: 20px; color: var(--text-secondary); }
  .usage { display: flex; align-items: baseline; gap: 12px; }
  .usage strong { font-size: 13px; }
  .action-row { display: flex; justify-content: flex-end; gap: 6px; }
  .warning { border-color: var(--danger-border); background: var(--danger-bg); }
  .error { color: var(--danger); font-size: 12px; }
  .primary.sm { min-height: 28px; }
  .guidance-input { min-height: 64px; resize: vertical; line-height: 1.5; }

  @media (max-width: 700px) {
    .esm-overlay { padding: 0; }
    .esm-drawer { width: 100%; height: 100%; border-radius: 0; }
    .esm-entry { padding: 0 8px; }
  }
</style>
