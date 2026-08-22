<script>
  import { createEventDispatcher, onDestroy } from 'svelte';
  import { t } from '../../lib/preferences.js';
  import { getSessionToolResult, getSessionTrajectory, getTrajectoryState, setTrajectoryState, getTrajectoryViewState, setTrajectoryViewState } from '../../lib/stores.js';
  import { reduceTrajectoryState, trajectoryRecords } from '../../lib/trajectory/reducer.js';
  import { flattenTrajectoryGroups, groupTrajectoryRecords, visibleWindow } from '../../lib/trajectory/layout.js';
  import { createTrajectorySearch } from '../../lib/trajectory/search.js';

  export let sessionID = '';
  export let messages = [];
  export let runEvents = [];
  export let capabilityEvents = [];
  export let toolEvents = [];
  export let decisionEvents = [];
  export let busy = false;

  const dispatch = createEventDispatcher();
  let query = '';
  let kindFilter = 'all';
  let statusFilter = 'all';
  let selectedID = '';
  let collapsed = new Set();
  let ledger;
  let scrollTop = 0;
  let viewportHeight = 560;
  let detailLoading = false;
  let detailError = '';
  let detailResult = null;
  let trajectorySessionKey = '';
  let remoteRecords = [];
  let remoteHighWater = {};
  let remoteHasMore = false;
  let loadingOlder = false;
  let remoteError = '';
  let detailWidth = 380;
  let resizingDetails = false;

  $: if (sessionID && sessionID !== trajectorySessionKey) {
    trajectorySessionKey = sessionID;
    const stored = getTrajectoryState(sessionID);
    const view = getTrajectoryViewState(sessionID);
    query = view.query;
    kindFilter = view.kindFilter;
    statusFilter = view.statusFilter;
    selectedID = view.selectedID;
    collapsed = new Set(view.collapsed || []);
    detailWidth = view.detailWidth;
    remoteRecords = [];
    remoteHighWater = stored.highWater || {};
    remoteError = '';
    void loadRemoteTrajectory(sessionID);
  }
  $: state = reduceTrajectoryState(getTrajectoryState(sessionID), {
    sessionId: sessionID,
    records: remoteRecords,
    highWater: remoteHighWater,
    messages,
    runEvents,
    capabilityEvents,
    toolEvents,
    decisionEvents,
    loading: false
  });
  $: if (sessionID && sessionID === trajectorySessionKey) setTrajectoryViewState(sessionID, {
    query,
    kindFilter,
    statusFilter,
    selectedID,
    collapsed: [...collapsed],
    detailWidth
  });
  $: allRecords = trajectoryRecords(state);
  $: search = createTrajectorySearch(allRecords);
  $: filteredRecords = search.query(query, { kind: kindFilter, status: statusFilter });
  $: groups = groupTrajectoryRecords(filteredRecords, collapsed);
  $: rows = flattenTrajectoryGroups(groups);
  $: windowed = visibleWindow(rows, scrollTop, viewportHeight);
  $: selected = allRecords.find((record) => record.id === selectedID) || null;

  function updateViewport() {
    if (!ledger) return;
    viewportHeight = ledger.clientHeight || 560;
  }

  function handleLedgerScroll() {
    scrollTop = ledger?.scrollTop || 0;
  }

  function cursorBeforeFirstRecord() {
    const cursor = { entrySeq: 0, runSeq: 0, capabilitySeq: 0, decisionSeq: 0 };
    for (const record of remoteRecords) {
      const seq = Number(record.seq || 0);
      if (!seq) continue;
      if (record.source === 'transcript') cursor.entrySeq = cursor.entrySeq ? Math.min(cursor.entrySeq, seq) : seq;
      else if (record.source === 'run') cursor.runSeq = cursor.runSeq ? Math.min(cursor.runSeq, seq) : seq;
      else if (record.source === 'capability') cursor.capabilitySeq = cursor.capabilitySeq ? Math.min(cursor.capabilitySeq, seq) : seq;
      else if (record.source === 'decision') cursor.decisionSeq = cursor.decisionSeq ? Math.min(cursor.decisionSeq, seq) : seq;
    }
    return cursor;
  }

  function encodeCursor(cursor) {
    const bytes = new TextEncoder().encode(JSON.stringify(cursor));
    let binary = '';
    for (const byte of bytes) binary += String.fromCharCode(byte);
    return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/g, '');
  }

  function toggleGroup(id) {
    const next = new Set(collapsed);
    if (next.has(id)) next.delete(id); else next.add(id);
    collapsed = next;
  }

  function collapseAllGroups() {
    collapsed = new Set(groups.map((group) => group.id));
  }

  function expandAllGroups() {
    collapsed = new Set();
  }

  function selectRecord(record) {
    if (!record) return;
    selectedID = record.id;
    detailError = '';
    detailResult = null;
    dispatch('select', record);
    if (record.kind === 'tool' && record.toolCallId && sessionID) loadToolDetail(record);
  }

  async function loadToolDetail(record) {
    detailLoading = true;
    try {
      detailResult = await getSessionToolResult(sessionID, record.toolCallId);
    } catch (error) {
      detailError = error?.message || String(error);
    } finally {
      detailLoading = false;
    }
  }

  async function loadRemoteTrajectory(id, before = '', append = false) {
    try {
      if (append) loadingOlder = true;
      const response = await getSessionTrajectory(id, before, 500);
      if (id !== sessionID) return;
      remoteRecords = append ? [...(response.records || []), ...remoteRecords] : (response.records || []);
      remoteHighWater = response.highWater || {};
      remoteHasMore = Boolean(response.hasMore);
      setTrajectoryState(id, reduceTrajectoryState(getTrajectoryState(id), {
        sessionId: id,
        records: remoteRecords,
        highWater: remoteHighWater,
        loading: false
      }));
    } catch (error) {
      if (id === sessionID) remoteError = error?.message || String(error);
    } finally {
      loadingOlder = false;
    }
  }

  async function loadEarlier() {
    if (!sessionID || loadingOlder || !remoteHasMore || !remoteRecords.length) return;
    await loadRemoteTrajectory(sessionID, encodeCursor(cursorBeforeFirstRecord()), true);
  }

  function formatTime(value) {
    if (!value) return '—';
    const parsed = Date.parse(value);
    if (!Number.isFinite(parsed)) return '—';
    return new Date(parsed).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
  }

  function formatDuration(record) {
    if (!record.startedAt || !record.completedAt) return record.status === 'running' ? 'running' : '—';
    const duration = Date.parse(record.completedAt) - Date.parse(record.startedAt);
    return Number.isFinite(duration) ? `${Math.max(0, duration)} ms` : '—';
  }

  function handleRecordKeydown(event, record) {
    if (event.key === 'Escape') {
      event.preventDefault();
      closeDetails();
      return;
    }
    if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault();
      selectRecord(record);
      return;
    }
    if (event.key === 'ArrowUp' || event.key === 'ArrowDown') {
      event.preventDefault();
      const index = filteredRecords.findIndex((item) => item.id === record.id);
      const next = filteredRecords[Math.max(0, Math.min(filteredRecords.length - 1, index + (event.key === 'ArrowUp' ? -1 : 1)))];
      if (next) selectRecord(next);
    }
  }

  function closeDetails() {
    selectedID = '';
    dispatch('closeDetails');
  }

  function startDetailResize(event) {
    if (event.button !== 0) return;
    resizingDetails = true;
    window.addEventListener('pointermove', handleDetailResize);
    window.addEventListener('pointerup', stopDetailResize, { once: true });
  }

  function handleDetailResize(event) {
    if (!resizingDetails || !ledger) return;
    const layout = ledger.closest('.trajectory-layout');
    if (!layout) return;
    const bounds = layout.getBoundingClientRect();
    detailWidth = Math.max(280, Math.min(520, bounds.right - event.clientX));
  }

  function stopDetailResize() {
    resizingDetails = false;
    window.removeEventListener('pointermove', handleDetailResize);
  }

  onDestroy(() => {
    window.removeEventListener('pointermove', handleDetailResize);
  });
</script>

<section class="trajectory-view" aria-label={$t('chat.trajectory.title')}>
  <header class="trajectory-toolbar">
    <div class="trajectory-toolbar-main">
      <strong>{$t('chat.trajectory.title')}</strong>
      <span class="trajectory-count">{filteredRecords.length}/{allRecords.length}</span>
      {#if busy}<span class="trajectory-live"><span class="dot running"></span>{$t('chat.trajectory.live')}</span>{/if}
    </div>
    <div class="trajectory-toolbar-controls">
      <input class="trajectory-search" bind:value={query} placeholder={$t('chat.trajectory.search')} aria-label={$t('chat.trajectory.search')} />
      <select bind:value={kindFilter} aria-label="Filter event type">
        <option value="all">{$t('chat.trajectory.allEvents')}</option>
        <option value="user">{$t('chat.trajectory.kind.user')}</option>
        <option value="assistant">{$t('chat.trajectory.kind.assistant')}</option>
        <option value="reasoning">{$t('chat.trajectory.kind.reasoning')}</option>
        <option value="tool">{$t('chat.trajectory.kind.tool')}</option>
        <option value="run">{$t('chat.trajectory.kind.run')}</option>
        <option value="capability">{$t('chat.trajectory.kind.capability')}</option>
        <option value="decision">{$t('chat.trajectory.kind.decision')}</option>
        <option value="error">{$t('chat.trajectory.kind.error')}</option>
      </select>
      <select bind:value={statusFilter} aria-label="Filter event status">
        <option value="all">{$t('chat.trajectory.allStatus')}</option>
        <option value="running">{$t('chat.trajectory.status.running')}</option>
        <option value="completed">{$t('chat.trajectory.status.completed')}</option>
        <option value="failed">{$t('chat.trajectory.status.failed')}</option>
        <option value="pending">{$t('chat.trajectory.status.pending')}</option>
        <option value="canceled">{$t('chat.trajectory.status.canceled')}</option>
      </select>
      <button type="button" class="trajectory-toolbar-action" on:click={collapseAllGroups}>{$t('chat.trajectory.collapseAll')}</button>
      <button type="button" class="trajectory-toolbar-action" on:click={expandAllGroups}>{$t('chat.trajectory.expandAll')}</button>
      {#if remoteHasMore}
        <button type="button" class="trajectory-load-earlier" disabled={loadingOlder} on:click={loadEarlier}>{loadingOlder ? $t('common.loading') : $t('chat.trajectory.loadEarlier')}</button>
      {/if}
    </div>
  </header>

  <div class="trajectory-layout" class:has-details={selected} class:resizing={resizingDetails} style={`--trajectory-detail-width:${detailWidth}px`}>
    <div class="trajectory-ledger" bind:this={ledger} on:scroll={handleLedgerScroll} on:mouseenter={updateViewport} role="region" aria-label={$t('chat.trajectory.title')}>
      {#if rows.length === 0}
        <div class="trajectory-empty">{$t('chat.trajectory.empty')}</div>
      {:else}
        <div style={`height:${windowed.before}px`}></div>
        {#each windowed.items as record (record.id)}
          <div class="trajectory-row-wrap" class:group-summary={record.isGroupSummary}>
            {#if record.isGroupSummary}
              <button type="button" class="trajectory-row trajectory-group-row" on:click={() => toggleGroup(record.groupID)}>
                <span class="trajectory-index">{record.seq || '—'}</span>
                <span class="trajectory-kind">{$t('chat.trajectory.group')}</span>
                <span class="trajectory-summary">{record.summary} · {record.groupSummary}</span>
                <span class="trajectory-status">{$t(`chat.trajectory.status.${record.status}`)}</span>
              </button>
            {:else}
              <button type="button" class="trajectory-row" class:selected={selectedID === record.id} on:click={() => selectRecord(record)} on:keydown={(event) => handleRecordKeydown(event, record)}>
                <span class="trajectory-index">{record.seq || '—'}</span>
                <span class="trajectory-kind"><span class="trajectory-kind-mark {record.kind} {record.status}"></span>{$t(`chat.trajectory.kind.${record.kind}`)}</span>
                <span class="trajectory-summary" title={record.preview}>{record.summary}<small>{record.preview}</small></span>
                <span class="trajectory-time">{formatTime(record.timestamp)}<small>{formatDuration(record)}</small></span>
                <span class="trajectory-status {record.status}">{$t(`chat.trajectory.status.${record.status}`)}</span>
              </button>
            {/if}
          </div>
        {/each}
        <div style={`height:${windowed.after}px`}></div>
      {/if}
    </div>

    {#if selected}
      <button type="button" class="trajectory-detail-divider" aria-label={$t('chat.trajectory.resizeDetails')} title={$t('chat.trajectory.resizeDetails')} on:pointerdown={startDetailResize}></button>
      <aside class="trajectory-details" aria-label={$t('chat.trajectory.details')}>
        <header>
          <div><span class="trajectory-detail-kind">{selected.kind}</span><strong>{selected.summary}</strong></div>
          <button type="button" class="icon-btn" title={$t('chat.trajectory.closeDetails')} aria-label={$t('chat.trajectory.closeDetails')} on:click={closeDetails}>×</button>
        </header>
        <dl class="trajectory-meta">
          <div><dt>{$t('chat.trajectory.meta.status')}</dt><dd class={selected.status}>{$t(`chat.trajectory.status.${selected.status}`)}</dd></div>
          <div><dt>{$t('chat.trajectory.meta.session')}</dt><dd>{selected.sessionId || '—'}</dd></div>
          <div><dt>{$t('chat.trajectory.meta.run')}</dt><dd>{selected.runId || '—'}</dd></div>
          <div><dt>{$t('chat.trajectory.meta.sequence')}</dt><dd>{selected.seq || '—'}</dd></div>
          <div><dt>{$t('chat.trajectory.meta.time')}</dt><dd>{formatTime(selected.timestamp)}</dd></div>
        </dl>
        {#if selected.preview}<section><h3>{$t('chat.trajectory.preview')}</h3><p class="trajectory-detail-text">{selected.preview}</p></section>{/if}
        {#if detailLoading}<p class="pending-text">{$t('chat.trajectory.loadingToolResult')}</p>{/if}
        {#if detailError}<p class="error-text">{detailError}</p>{/if}
        {#if detailResult?.content}<section><h3>{$t('chat.trajectory.output')}</h3><pre>{detailResult.content}</pre></section>{/if}
        {#if selected.input}<section><h3>{$t('chat.trajectory.input')}</h3><pre>{JSON.stringify(selected.input, null, 2)}</pre></section>{/if}
        {#if selected.output && !detailResult?.content}<section><h3>{$t('chat.trajectory.output')}</h3><pre>{typeof selected.output === 'string' ? selected.output : JSON.stringify(selected.output, null, 2)}</pre></section>{/if}
        <details><summary>{$t('chat.trajectory.eventJSON')}</summary><pre>{JSON.stringify(selected.sourceEvent, null, 2)}</pre></details>
      </aside>
    {/if}
  </div>
  {#if remoteError}<p class="trajectory-load-error" role="status">{remoteError}</p>{/if}
</section>
