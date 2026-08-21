<script>
  import { createEventDispatcher, onDestroy } from 'svelte';
  import { t } from '../../lib/preferences.js';
  import { getSessionToolResult, getSessionTrajectory, getTrajectoryState, setTrajectoryState, getTrajectoryViewState, setTrajectoryViewState } from '../../lib/stores.js';
  import { reduceTrajectoryState, trajectoryRecords } from '../../lib/trajectory/reducer.js';
  import { flattenTrajectoryGroups, groupTrajectoryRecords, timelineSpans, visibleWindow } from '../../lib/trajectory/layout.js';
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
  let timeMode = 'actual';
  let selectedID = '';
  let collapsed = new Set();
  let ledger;
  let scrollTop = 0;
  let viewportHeight = 560;
  let detailLoading = false;
  let detailError = '';
  let detailResult = null;
  let timelineRange = null;
  let timelinePointer = null;
  let timelineSelection = null;
  let timelineSpaceHeld = false;
  let timelineFocusIndex = -1;
  let timelineElement;
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
    timeMode = view.timeMode;
    selectedID = view.selectedID;
    collapsed = new Set(view.collapsed || []);
    timelineRange = view.timelineRange;
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
    timeMode,
    selectedID,
    collapsed: [...collapsed],
    timelineRange,
    detailWidth
  });
  $: allRecords = trajectoryRecords(state);
  $: search = createTrajectorySearch(allRecords);
  $: filteredRecords = search.query(query, { kind: kindFilter, status: statusFilter });
  $: groups = groupTrajectoryRecords(filteredRecords, collapsed);
  $: rows = flattenTrajectoryGroups(groups);
  $: windowed = visibleWindow(rows, scrollTop, viewportHeight);
  $: timeline = timelineSpans(filteredRecords, timelineRange);
  $: selected = allRecords.find((record) => record.id === selectedID) || null;
  $: if (timeMode === 'equal' && filteredRecords.length) {
    timeline = {
      ...timeline,
      spans: filteredRecords.map((record, index) => ({ id: record.id, left: (index / filteredRecords.length) * 100, width: Math.max(0.8, 100 / filteredRecords.length), unknownEnd: false }))
    };
  }

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

  function handleTimelineWheel(event) {
    if (!filteredRecords.length) return;
    event.preventDefault();
    const factor = event.deltaY > 0 ? 1.2 : 0.84;
    const currentMin = timeline.min;
    const currentMax = timeline.max || currentMin + 1;
    const center = currentMin + (currentMax - currentMin) / 2;
    const nextWidth = Math.max(1, (currentMax - currentMin) * factor);
    timelineRange = { min: center - nextWidth / 2, max: center + nextWidth / 2 };
  }

  function handleTimelinePointerDown(event) {
    if (event.button !== 0) return;
    const range = timelineRange || { min: timeline.min, max: timeline.max || timeline.min + 1 };
    timelinePointer = {
      x: event.clientX,
      range,
      mode: event.shiftKey || timelineSpaceHeld ? 'pan' : 'select',
      startValue: timelineValueAt(event.clientX)
    };
    timelineSelection = timelinePointer.mode === 'select' ? { min: timelinePointer.startValue, max: timelinePointer.startValue } : null;
    window.addEventListener('pointermove', handleTimelinePointerMove);
    window.addEventListener('pointerup', handleTimelinePointerUp, { once: true });
  }

  function timelineValueAt(clientX) {
    const width = Math.max(1, timelineElement?.clientWidth || 1);
    const range = timelineRange || { min: timeline.min, max: timeline.max || timeline.min + 1 };
    const ratio = Math.max(0, Math.min(1, (clientX - timelineElement.getBoundingClientRect().left) / width));
    return range.min + (range.max - range.min) * ratio;
  }

  function handleTimelinePointerMove(event) {
    if (!timelinePointer) return;
    const width = timelineElement?.clientWidth || 1;
    if (timelinePointer.mode === 'select') {
      const value = timelineValueAt(event.clientX);
      timelineSelection = { min: Math.min(timelinePointer.startValue, value), max: Math.max(timelinePointer.startValue, value) };
      return;
    }
    const delta = ((event.clientX - timelinePointer.x) / Math.max(1, width)) * (timelinePointer.range.max - timelinePointer.range.min);
    timelineRange = { min: timelinePointer.range.min - delta, max: timelinePointer.range.max - delta };
  }

  function handleTimelinePointerUp() {
    if (timelinePointer?.mode === 'select' && timelineSelection && timelineSelection.max - timelineSelection.min > 1) {
      timelineRange = timelineSelection;
    }
    timelinePointer = null;
    timelineSelection = null;
    window.removeEventListener('pointermove', handleTimelinePointerMove);
  }

  function resetTimeline() {
    timelineRange = null;
    timelineSelection = null;
  }

  function handleTimelineKeydown(event) {
    if (event.key === ' ') {
      event.preventDefault();
      timelineSpaceHeld = true;
      return;
    }
    if (event.key === 'Escape') {
      resetTimeline();
      closeDetails();
      return;
    }
    if (event.key === 'Enter' && timelineFocusIndex >= 0) {
      event.preventDefault();
      selectRecord(filteredRecords[timelineFocusIndex]);
      return;
    }
    if (event.key === 'ArrowLeft' || event.key === 'ArrowRight') {
      event.preventDefault();
      const direction = event.key === 'ArrowLeft' ? -1 : 1;
      timelineFocusIndex = Math.max(0, Math.min(filteredRecords.length - 1, (timelineFocusIndex < 0 ? 0 : timelineFocusIndex) + direction));
      const record = filteredRecords[timelineFocusIndex];
      if (record) selectRecord(record);
    }
  }

  function handleTimelineKeyup(event) {
    if (event.key === ' ') timelineSpaceHeld = false;
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

  function handleTimelineDoubleClick() {
    resetTimeline();
  }

  function focusRecordByKeyboard(event, record) {
    if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault();
      selectRecord(record);
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
    if (!resizingDetails || !timelineElement) return;
    const layout = timelineElement.closest('.trajectory-layout');
    if (!layout) return;
    const bounds = layout.getBoundingClientRect();
    detailWidth = Math.max(280, Math.min(520, bounds.right - event.clientX));
  }

  function stopDetailResize() {
    resizingDetails = false;
    window.removeEventListener('pointermove', handleDetailResize);
  }

  onDestroy(() => {
    window.removeEventListener('pointermove', handleTimelinePointerMove);
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
      <div class="trajectory-segmented" role="group" aria-label="Timeline mode">
        <button type="button" class:active={timeMode === 'actual'} on:click={() => (timeMode = 'actual')}>{$t('chat.trajectory.actual')}</button>
        <button type="button" class:active={timeMode === 'equal'} on:click={() => (timeMode = 'equal')}>{$t('chat.trajectory.equal')}</button>
      </div>
      <button type="button" class="trajectory-toolbar-action" on:click={collapseAllGroups}>{$t('chat.trajectory.collapseAll')}</button>
      <button type="button" class="trajectory-toolbar-action" on:click={expandAllGroups}>{$t('chat.trajectory.expandAll')}</button>
      <button type="button" class="icon-btn" title={$t('chat.trajectory.reset')} aria-label={$t('chat.trajectory.reset')} on:click={resetTimeline}>↺</button>
      {#if remoteHasMore}
        <button type="button" class="trajectory-load-earlier" disabled={loadingOlder} on:click={loadEarlier}>{loadingOlder ? $t('common.loading') : $t('chat.trajectory.loadEarlier')}</button>
      {/if}
    </div>
  </header>

  <div class="trajectory-timeline" bind:this={timelineElement} on:wheel={handleTimelineWheel} on:pointerdown={handleTimelinePointerDown} on:dblclick={handleTimelineDoubleClick} on:keydown={handleTimelineKeydown} on:keyup={handleTimelineKeyup} tabindex="0" role="button" aria-label={$t('chat.trajectory.timeline')}>
    <div class="trajectory-timeline-grid"></div>
    {#each timeline.spans as span (span.id)}
      <button
        type="button"
        class="trajectory-span"
        class:unknown={span.unknownEnd}
        style={`left:${span.left}%;width:${span.width}%`}
        title={span.id}
        aria-label={`Focus ${span.id}`}
        on:click|stopPropagation={() => selectRecord(allRecords.find((record) => record.id === span.id))}
      ></button>
    {/each}
  </div>

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
