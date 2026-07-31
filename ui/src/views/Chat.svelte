<script>
  import { onDestroy, onMount, tick } from 'svelte';
  import { get } from 'svelte/store';
  import { markdownToHTML, highlightedCodeToHTML } from '../lib/markdown.js';
  import { readSSE, postJSON } from '../lib/api.js';
  import { approvalSessionID, approvalRequestOwnership, approvalHistoryFromRunEvents, applyApprovalRequestToRuntime } from '../lib/approval.js';
  import {
    sessions,
    upsertSession,
    currentSession,
    selectedModel,
    models,
    features,
    setError,
    setNotice,
    clearBanners,
    refreshSessions,
    refreshStatsSummary,
    resetSelectedModelToDefault,
    getSessionMessages,
    getSessionToolResult,
    getSessionSubAgents,
    getSessionSubAgentMessages,
    getSessionRunEvents,
    getSessionCapabilityEvents,
    getSessionRuntime,
    patchSessionRuntime,
    sessionRuntime,
    runEvents,
    runsConnected,
    activeApproval,
    sessionToolOptions,
    sessionToolsFor,
    setSessionTools,
    moveSessionTools
  } from '../lib/stores.js';
  import { shortID, toolStateClass, formatArgs } from '../lib/format.js';
  import {
    buildToolCallView,
    normalizeSessionMessage,
    upsertMessageInList,
    viewFromSessionState,
    sessionStateWithView,
    reduceTranscriptEvent,
    reduceToolStatusEvent,
    reduceRunEvent,
    reduceCapabilityEvent,
    reduceRuntimeSnapshot,
    reduceStreamDone,
    reduceStreamError,
    reduceApprovalRequest,
    reduceApprovalResolved,
    maxSeq,
    textFromContents,
    toolResultKind,
    parseReadResult,
    parseLsResult,
    parseGrepResult,
    parseBashResult,
    parseBrowserResult,
    parseSubAgentResult,
    parseWorkflowLintResult
  } from '../lib/session-view.js';
  import {
    sessionRunStates,
    ensureSessionState,
    getSessionState,
    updateSessionState,
    isCompletionActive,
    registerCompletion,
    markCompletion,
    clearCompletion,
    abortCompletion,
    registerObserver,
    clearObserver,
    stopObserver,
    eventBelongsToSession
  } from '../lib/session-runs.js';
  import DirBrowser from '../components/DirBrowser.svelte';
  import { t } from '../lib/preferences.js';

  let prompt = '';
  let availableSkills = [];
  let activeSkills = [];
  let showSkillPicker = false;
  let showToolMenu = false;
  let toolMenuBtn;
  let loadedSkillsKey = '';
  let messages = [];
  let busy = false;
  let chatEvents = [];
  let sessionRunEvents = [];
  let sessionCapabilityEvents = [];
  let workDir = '';
  let sessionCreated = false;
  let showBrowser = false;
  let imageInput;
  let imageUploads = [];
  let chatScroll;
  let shouldFollowOutput = true;
  let scrollFrame = 0;
  let streamUsesTranscript = false;
  let streamHadError = false;
  let sessionHistoryLoadedFor = '';
  let sessionStreamCompletedFor = '';
  let sessionStreamCursor = { entrySeq: 0, runSeq: 0, capabilitySeq: 0 };
  let optimisticRunEventID = '';
  let sessionToolKey = '__new__';
  let sessionTools = sessionToolsFor({}, sessionToolKey);
  let subAgents = [];
  let subAgentTranscripts = {};
  let showSubAgentModal = false;
  let selectedSubAgentID = '';
  let subAgentModalMessages = [];
  let subAgentModalLoading = false;
  let subAgentModalError = '';
  let subAgentRefreshTimer = 0;
  let sessionRuntimeValue = null;
  let newSessionMode = 'yolo';
  let runtimeUpdating = false;
  let approvalHistory = [];
  let runEventCursor = 0;
  let runtimeControls;
  let modelPicker;
  let skillPicker;
  let showRuntimePanel = false;
  let showModelPicker = false;
  let showApprovalCenter = false;
  let selectedApprovalID = '';
  let approvalSubmitting = false;
  let stopSubmitting = false;
  $: activeSession = ($sessions || []).find((item) => item?.id === $currentSession);
  $: channelBadge = activeSession?.channelLabel || $t('sessions.local');

  const suggestions = [
    'chat.suggestion.projectSummary',
    'chat.suggestion.reviewChanges',
    'chat.suggestion.addTests',
    'chat.suggestion.fixTests',
    'chat.suggestion.refactor',
    'chat.suggestion.configAudit',
    'chat.suggestion.readme',
    'chat.suggestion.multiAgent'
  ];

  const toolToggles = [
    { key: 'webSearch', label: 'webSearch' },
    { key: 'browser', label: 'browser' },
    { key: 'a2aMaster', label: 'a2aMaster' },
    { key: 'delegate', label: 'delegate' },
    { key: 'multiAgent', label: 'multi-agent' },
    { key: 'workflows', label: 'workflow' }
  ];

  // Reset or load state when the selected session changes.
  let prevSession = $currentSession;
  onMount(() => {
    const handleRuntimeOutsidePointer = (event) => {
      if (showRuntimePanel && runtimeControls && !runtimeControls.contains(event.target)) {
        showRuntimePanel = false;
      }
      if (showModelPicker && modelPicker && !modelPicker.contains(event.target)) {
        showModelPicker = false;
      }
      if (showSkillPicker && skillPicker && !skillPicker.contains(event.target)) {
        showSkillPicker = false;
      }
      if (showToolMenu && toolMenuBtn && !toolMenuBtn.contains(event.target)) {
        showToolMenu = false;
      }
    };
    document.addEventListener('pointerdown', handleRuntimeOutsidePointer);
    if ($currentSession) {
      loadSessionMessages($currentSession);
    }
    loadSkills();
    return () => document.removeEventListener('pointerdown', handleRuntimeOutsidePointer);
  });
  onDestroy(() => {
    for (const state of Object.values(get(sessionRunStates))) {
      state.observer?.controller?.abort();
      // Runs are persistent; do not abort on component destroy. completion is left intact.;
    }
    if (subAgentRefreshTimer) clearTimeout(subAgentRefreshTimer);
  });

  $: {
    const nextSession = $currentSession;
    if (nextSession !== prevSession) {
      if (prevSession) persistLocalSessionState(prevSession);
      if (prevSession && prevSession !== nextSession) stopObserver(prevSession);
      sessionHistoryLoadedFor = '';
      subAgents = [];
      subAgentTranscripts = {};
      closeSubAgentModal();
      activeApproval.set(null);
      selectedApprovalID = '';
      if (nextSession === '') {
        sessionCreated = false;
        workDir = '';
        messages = []; // new chat — no history
        chatEvents = []; // reset tool events
        sessionRunEvents = [];
        sessionCapabilityEvents = [];
        resetSelectedModelToDefault();
        shouldFollowOutput = true;
      } else {
        const cached = getSessionState(nextSession);
        if (cached.historyLoaded || isCompletionActive(cached)) {
          try {
            restoreLocalSessionState(cached);
            sessionCreated = true;
            scrollChatToBottom({ force: true });
          } catch (err) {
            console.warn("Failed to restore cached session state, loading from server:", err);
            loadSessionMessages(nextSession);
          }
        } else {
          loadSessionMessages(nextSession);
        }
      }
      prevSession = nextSession;
    }
  }

  function persistLocalSessionState(id) {
    if (!id) return;
    updateSessionState(id, (state) => {
      // Skip the store write entirely when nothing changed (e.g. replayed
      // history frames) — each write notifies every sessionRunStates subscriber.
      if (
        state.messages === messages &&
        state.toolEvents === chatEvents &&
        state.runEvents === sessionRunEvents &&
        state.capabilityEvents === sessionCapabilityEvents &&
        state.runtime === sessionRuntimeValue &&
        state.cursor === sessionStreamCursor &&
        state.streamCompleted === (sessionStreamCompletedFor === id) &&
        state.subAgents === subAgents &&
        state.subAgentTranscripts === subAgentTranscripts &&
        state.streamUsesTranscript === streamUsesTranscript &&
        state.optimisticRunEventID === optimisticRunEventID &&
        (state.historyLoaded || sessionHistoryLoadedFor !== id)
      ) {
        return state;
      }
      return {
        ...state,
        messages,
        toolEvents: chatEvents,
        runEvents: sessionRunEvents,
        capabilityEvents: sessionCapabilityEvents,
        runtime: sessionRuntimeValue,
        pendingApprovals: sessionRuntimeValue?.pendingApprovals || [],
        cursor: sessionStreamCursor,
        historyLoaded: sessionHistoryLoadedFor === id || state.historyLoaded,
        streamCompleted: sessionStreamCompletedFor === id,
        streamUsesTranscript,
        optimisticRunEventID,
        subAgents,
        subAgentTranscripts
      };
    });
  }

  function restoreLocalSessionState(state) {
    messages = state?.messages || [];
    chatEvents = state?.toolEvents || [];
    sessionRunEvents = state?.runEvents || [];
    sessionCapabilityEvents = state?.capabilityEvents || [];
    sessionRuntimeValue = state?.runtime || null;
    sessionRuntime.set(sessionRuntimeValue);
    sessionStreamCursor = state?.cursor || { entrySeq: 0, runSeq: 0, capabilitySeq: 0 };
    sessionHistoryLoadedFor = state?.historyLoaded ? state.sessionId : '';
    sessionStreamCompletedFor = state?.streamCompleted ? state.sessionId : '';
    streamUsesTranscript = Boolean(state?.streamUsesTranscript);
    optimisticRunEventID = state?.optimisticRunEventID || '';
    subAgents = state?.subAgents || [];
    subAgentTranscripts = state?.subAgentTranscripts || {};
    approvalHistory = approvalHistoryFromRunEvents(sessionRunEvents);
  }

  // --- Session view state ---
  // The visible session renders from component locals; background sessions
  // update their own entry in sessionRunStates directly. Both paths share the
  // same pure reducers from lib/session-view.js — no projection swapping.

  function currentView() {
    return {
      messages,
      toolEvents: chatEvents,
      runEvents: sessionRunEvents,
      capabilityEvents: sessionCapabilityEvents,
      runtime: sessionRuntimeValue,
      cursor: sessionStreamCursor,
      streamCompleted: Boolean($currentSession) && sessionStreamCompletedFor === $currentSession,
      subAgents,
      subAgentTranscripts
    };
  }

  function applyView(view) {
    // Assign only changed references — Svelte invalidates on every
    // assignment, so no-op replay frames must not touch the view.
    if (messages !== view.messages) messages = view.messages;
    if (chatEvents !== view.toolEvents) chatEvents = view.toolEvents;
    if (sessionRunEvents !== view.runEvents) sessionRunEvents = view.runEvents;
    if (sessionCapabilityEvents !== view.capabilityEvents) sessionCapabilityEvents = view.capabilityEvents;
    if (sessionRuntimeValue !== view.runtime) {
      sessionRuntimeValue = view.runtime;
      sessionRuntime.set(view.runtime);
    }
    if (sessionStreamCursor !== view.cursor) sessionStreamCursor = view.cursor;
    sessionStreamCompletedFor = view.streamCompleted ? $currentSession : '';
    if (subAgents !== view.subAgents) subAgents = view.subAgents;
    if (subAgentTranscripts !== view.subAgentTranscripts) subAgentTranscripts = view.subAgentTranscripts;
    approvalHistory = approvalHistoryFromRunEvents(sessionRunEvents);
    persistLocalSessionState($currentSession);
  }

  // applySessionViewReducer runs a pure view reducer for session `id`.
  // For the visible session it applies the result to the component view and
  // optionally scrolls; for background sessions it writes straight into
  // sessionRunStates without touching the DOM or scroll position.
  function applySessionViewReducer(id, reduce, { scroll = false } = {}) {
    if (!id || typeof reduce !== 'function') return { effects: {} };
    if (id === $currentSession) {
      const previousMessages = messages;
      const { view, effects = {} } = reduce(currentView());
      applyView(view);
      if (effects.forceScroll) scrollChatToBottom({ force: true });
      else if ((scroll || effects.scroll) && view.messages !== previousMessages) scrollChatToBottom();
      return { effects };
    }
    let effects = {};
    updateSessionState(id, (state) => {
      const result = reduce(viewFromSessionState(state));
      effects = result.effects || {};
      return sessionStateWithView(state, result.view);
    });
    return { effects };
  }

  async function loadSessionMessages(id) {
    try {
      const msgs = await getSessionMessages(id);
      if (id !== $currentSession) return;
      if (msgs && msgs.length > 0) {
        messages = msgs.map((msg) => normalizeSessionMessage(msg, $t)).filter(Boolean);
      } else {
        messages = [];
      }
      chatEvents = []; // reset tool events for new session view
      await loadSessionEvents(id);
      await loadSessionRuntime(id);
      sessionHistoryLoadedFor = id;
      updateSessionStreamCursorFromState();
      persistLocalSessionState(id);
      scrollChatToBottom({ force: true });
    } catch {
      if (id !== $currentSession) return;
      // Leave messages empty on error
      sessionHistoryLoadedFor = id;
      updateSessionStreamCursorFromState();
      persistLocalSessionState(id);
    }
    sessionCreated = true; // existing session, not "new"
  }

  $: activeSession = $sessions.find((s) => s.id === $currentSession);
  $: selectedRunState = $currentSession ? $sessionRunStates[$currentSession] : null;
  $: busy = isCompletionActive(selectedRunState) || ['running', 'cancelling', 'terminalizing'].includes(selectedRunState?.runtime?.activeRun?.status);
  $: runtimeMode = sessionRuntimeValue?.mode || activeSession?.mode || (!$currentSession ? newSessionMode : 'yolo');
  $: pendingApprovalCount = (sessionRuntimeValue?.pendingApprovals || []).length;
  $: {
    const pending = sessionRuntimeValue?.pendingApprovals || [];
    if (pending.length > 0 && !pending.some((approval) => approval.approvalId === selectedApprovalID)) {
      selectedApprovalID = pending[0].approvalId;
      activeApproval.set(pending[0]);
    } else if (pending.length === 0 && selectedApprovalID) {
      selectedApprovalID = '';
      activeApproval.set(null);
    }
  }
  $: approvalToolViewValue = approvalToolView(selectedApproval);
  $: selectedApproval = (sessionRuntimeValue?.pendingApprovals || []).find((approval) => approval.approvalId === selectedApprovalID) || $activeApproval || null;
  $: runtimeActiveRun = sessionRuntimeValue?.activeRun;
  $: sessionToolKey = $currentSession || '__new__';
  $: sessionTools = sessionToolsFor($sessionToolOptions, sessionToolKey, activeSession || $features);
  $: availableToolToggles = toolToggles.filter(isToolToggleVisible);
  $: visibleSessionTools = filterHiddenSessionTools(sessionTools, $features);
  $: recentTools = chatEvents.slice(-6).reverse();
  $: sessionEventSummary = buildSessionEventSummary(sessionRunEvents, sessionCapabilityEvents, activeSessionWorkDir, $selectedModel);
  $: subAgentSummary = buildSubAgentSummary(subAgents);
  $: modelOptions = $models;
  $: activeModel = modelOptions.find((m) => m.id === $selectedModel);
  $: selectedModelSupportsImages = (activeModel?.input || []).includes('image');
  $: apiEnabled = $features.api;
  $: skillNames = activeSkills;
  $: skillsWorkDir = activeSessionWorkDir || workDir.trim();
  $: skillsContextKey = `${$currentSession || ''}:${skillsWorkDir}`;
  $: if (apiEnabled && skillsContextKey && skillsContextKey !== loadedSkillsKey) {
    loadedSkillsKey = skillsContextKey;
    loadSkills();
  }
  $: isNewSession = !$currentSession && !sessionCreated;
  $: activeToolCount = availableToolToggles.filter(i => sessionTools[i.key]).length;
  $: activeSessionWorkDir = activeSession?.workDir || workDir.trim();
  $: if ($currentSession && activeSession?.workDir && workDir !== activeSession.workDir) {
    workDir = activeSession.workDir;
  }
  $: if (!selectedModelSupportsImages && imageUploads.length > 0) {
    clearImages();
  }
  $: {
    // runEvents is capped (trimmed) in stores.js, so track a monotonic wsSeq
    // cursor instead of an array index — trimmed events must not stall processing.
    const pendingEvents = $runEvents.filter((item) => Number(item?.wsSeq || 0) > runEventCursor);
    if (pendingEvents.length > 0) {
      runEventCursor = Number(pendingEvents[pendingEvents.length - 1].wsSeq) || runEventCursor;
      for (const item of pendingEvents) {
        if (item?.type !== 'session_event' || !item.sessionId) continue;
        const eventName = item.event || item.stream || '';
        if (!eventName) continue;
        handleSessionStreamEvent(item.sessionId, { event: eventName, data: JSON.stringify(item.data ?? item) });
      }
    }
  }
  $: {
    const tailID = $currentSession;
    // The SSE tail is only a fallback for when the runs WebSocket is down.
    // When the socket is connected it already forwards live events, and the
    // SSE stream's periodic message replay would interleave persisted
    // messages with the live stream.
    const shouldTail = Boolean(
      tailID &&
      !$runsConnected &&
      !isCompletionActive($sessionRunStates[tailID]) &&
      activeSession?.running &&
      sessionHistoryLoadedFor === tailID &&
      sessionStreamCompletedFor !== tailID
    );
    if (shouldTail) {
      startSessionStream(tailID);
    } else if (!shouldTail && tailID) {
      stopObserver(tailID);
    }
  }

  function pick(text) {
    if (busy) return;
    prompt = text;
    sendPrompt();
  }

  function handleKeydown(event) {
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault();
      sendPrompt();
    }
  }

  async function loadSkills() {
    if (!apiEnabled) return;
    try {
      const params = new URLSearchParams();
      if ($currentSession) params.set('sessionId', $currentSession);
      else if (activeSessionWorkDir || workDir.trim()) params.set('workDir', activeSessionWorkDir || workDir.trim());
      const data = await fetch(`/api/skillhub/installed?${params}`).then((r) => r.ok ? r.json() : null);
      availableSkills = (data?.installed || []).filter((item) => item?.name).map((item) => ({ name: item.name, description: item.name, active: Boolean(item.active) }));
      const serverActive = data?.session?.activeSkills;
      activeSkills = Array.isArray(serverActive) ? serverActive : availableSkills.filter((item) => item.active).map((item) => item.name);
    } catch { availableSkills = []; }
  }

  async function toggleSkill(name, event) {
    const next = event.currentTarget.checked ? [...activeSkills, name] : activeSkills.filter((item) => item !== name);
    activeSkills = [...new Set(next)];
    if (!$currentSession) return;
    try {
      await postJSON('/api/skillhub/set-active', { sessionId: $currentSession, names: activeSkills });
    } catch (err) { setError(err); await loadSkills(); }
  }

  async function sendPrompt() {
    const outgoing = prompt.trim();
    const outgoingImages = imageUploads;
    if ((!outgoing && outgoingImages.length === 0) || !apiEnabled) return;
    if (outgoingImages.length > 0 && !selectedModelSupportsImages) {
      setError($t('chat.error.modelNoImages'));
      return;
    }
    const creatingSession = isNewSession;
    if (creatingSession && !workDir.trim()) {
      setError($t('chat.error.needWorkDir'));
      return;
    }

    const sessionID = $currentSession || newWebUISessionID();
    const creatingExplicitSession = !$currentSession;
    const existingState = getSessionState(sessionID);
    if (isCompletionActive(existingState) || ['running', 'cancelling', 'terminalizing'].includes(existingState.runtime?.activeRun?.status)) {
      setError('This session already has an active run.');
      return;
    }
    if (creatingExplicitSession) {
      ensureSessionState(sessionID);
      moveSessionTools('__new__', sessionID);
      currentSession.set(sessionID);
    }
    stopObserver(sessionID);
    sessionStreamCompletedFor = '';
    chatEvents = [];
    streamUsesTranscript = false;
    streamHadError = false;

    messages = [...messages, { role: 'user', content: outgoing, images: outgoingImages }];
    if (creatingExplicitSession) {
      upsertSession(buildOptimisticSessionInfo(sessionID, outgoing));
      refreshSessions().catch(() => {});
    }
    scrollChatToBottom({ force: true });
    prompt = '';
    imageUploads = [];
    if (imageInput) imageInput.value = '';

    const controller = new AbortController();
    registerCompletion(sessionID, controller);
    optimisticRunEventID = beginOptimisticRunEvent(sessionID);
    persistLocalSessionState(sessionID);
    try {
      // Use the new submit run API instead of /v1/chat/completions.
      // The run is submitted to the server and events are received via WebSocket.
      const submitBody = JSON.stringify({
        message: outgoing,
        model: $selectedModel || 'default',
        mode: creatingSession ? newSessionMode : undefined,
        tools: visibleSessionTools ? Object.keys(visibleSessionTools).filter(k => visibleSessionTools[k]) : [],
        skills: activeSkills,
        images: outgoingImages.map(img => img.dataUrl),
        transcript: true
      });
      const res = await fetch(`/api/sessions/${encodeURIComponent(sessionID)}/runs`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: submitBody,
        signal: controller.signal
      });
      if (!res.ok) {
        const text = await res.text();
        let data = null;
        try { data = text ? JSON.parse(text) : null; } catch { data = null; }
        throw new Error(data?.error?.message || data?.error || data?.message || `${res.status} ${res.statusText}`);
      }
      const submitResult = await res.json();
      markCompletion(sessionID, 'running');
      if (creatingExplicitSession) {
        upsertSession(buildOptimisticSessionInfo(sessionID, outgoing, { running: true }));
        refreshSessions().catch(() => {});
      }
      applySessionViewReducer(sessionID, (view) => ({
        view: { ...view, messages: [...view.messages, { role: 'assistant', content: '' }] },
        effects: { forceScroll: true }
      }));
      // Events are received via WebSocket (runEvents store) and processed
      // by handleSessionStreamEvent. We wait for the run to complete via
      // the sessionRunStates observer.
      await waitForRunCompletion(sessionID, controller.signal);
      const finalStatus = streamHadError ? 'failed' : 'completed';
      finishOptimisticRunEvent(sessionID, finalStatus, streamHadError ? getSessionState(sessionID).lastError : '');
      markCompletion(sessionID, finalStatus, streamHadError ? getSessionState(sessionID).lastError : '');
      sessionCreated = true;
    } catch (err) {
      const canceled = err?.name === 'AbortError';
      finishOptimisticRunEvent(sessionID, canceled ? 'canceled' : 'failed', canceled ? '' : errorMessage(err));
      if (!canceled) applySessionViewReducer(sessionID, (view) => reduceStreamError(view, errorMessage(err), $t));
      markCompletion(sessionID, canceled ? 'canceled' : 'failed', canceled ? '' : err);
      if (sessionID === $currentSession) {
        if (canceled) setNotice($t('chat.notice.stopped'));
        else setError(err);
      }
    } finally {
      clearCompletion(sessionID, controller);
      try { await refreshSessions(); } catch {
        // opportunistic
      }
      try { await refreshStatsSummary(); } catch {
        // opportunistic
      }
      if (sessionID === $currentSession) {
        try { await loadSessionMessages(sessionID); } catch {
          // opportunistic
        }
        try { await loadSubAgents(sessionID); } catch {
          // opportunistic
        }
      }
      updateSessionState(sessionID, (state) => ({ ...state, optimisticRunEventID: '' }));
      if (sessionID === $currentSession) optimisticRunEventID = '';
    }
  }

  function newWebUISessionID() {
    if (globalThis.crypto?.randomUUID) return globalThis.crypto.randomUUID();
    return `webui-${Date.now()}-${Math.random().toString(16).slice(2)}`;
  }

  // waitForRunCompletion polls the session state until the run completes or is aborted.
  // This replaces the old SSE-based readSSE loop.
  async function waitForRunCompletion(sessionID, signal) {
    return new Promise((resolve, reject) => {
      if (signal?.aborted) { resolve(); return; }
      const onAbort = () => {
        clearInterval(interval);
        signal?.removeEventListener('abort', onAbort);
        resolve();
      };
      signal?.addEventListener('abort', onAbort);

      const interval = setInterval(() => {
        if (signal?.aborted) {
          clearInterval(interval);
          signal?.removeEventListener('abort', onAbort);
          resolve();
          return;
        }
        const state = getSessionState(sessionID);
        const isDone = state.streamCompleted;
        const completionStatus = state.completion?.status;
        if (isDone || completionStatus === 'completed' || completionStatus === 'failed' || completionStatus === 'cancel_requested') {
          clearInterval(interval);
          signal?.removeEventListener('abort', onAbort);
          resolve();
          return;
        }
      }, 250);
    });
  }

  function buildOptimisticSessionInfo(id, firstMessage = '', overrides = {}) {
    const now = new Date().toISOString();
    const cwd = workDir.trim() || activeSessionWorkDir || '';
    return {
      id,
      workDir: cwd,
      mode: overrides.mode || newSessionMode || runtimeMode || 'yolo',
      active: true,
      running: false,
      lastUsed: now,
      messageCount: Math.max(1, messages.length || 1),
      preview: firstMessage,
      title: firstMessage,
      ...overrides
    };
  }

  async function stop() {
    if (!$currentSession || stopSubmitting) return;
    const id = $currentSession;
    stopSubmitting = true;
    markCompletion(id, 'cancel_requested');
    if (id === $currentSession && sessionRuntimeValue?.activeRun) {
      sessionRuntimeValue = {
        ...sessionRuntimeValue,
        activeRun: { ...sessionRuntimeValue.activeRun, status: 'cancelling' }
      };
      sessionRuntime.set(sessionRuntimeValue);
      persistLocalSessionState(id);
    }
    try {
      await postJSON(`/api/sessions/${encodeURIComponent(id)}/stop`, {});
      abortCompletion(id);
      setNotice($t('chat.notice.stopped'));
      const snapshot = await getSessionRuntime(id);
      if (id === $currentSession) {
        sessionRuntimeValue = snapshot;
        sessionRuntime.set(snapshot);
        persistLocalSessionState(id);
      }
    } catch (err) {
      setError(err);
      if (err?.message?.includes('no active run')) {
        markCompletion(id, 'failed', err);
        if (id === $currentSession) await loadSessionRuntime(id);
      }
    } finally {
      stopSubmitting = false;
    }
  }

  function resetSession() {
    resetSelectedModelToDefault();
    newSessionMode = 'yolo';
    currentSession.set('');
  }

  function handleChatScroll() {
    shouldFollowOutput = isChatNearBottom();
  }

  function isChatNearBottom() {
    if (!chatScroll) return true;
    const distance = chatScroll.scrollHeight - chatScroll.scrollTop - chatScroll.clientHeight;
    return distance < 96;
  }

  async function scrollChatToBottom({ force = false } = {}) {
    if (!chatScroll) return;
    if (!force && !shouldFollowOutput) return;
    await tick();
    if (!chatScroll) return;
    if (scrollFrame) cancelAnimationFrame(scrollFrame);
    scrollFrame = requestAnimationFrame(() => {
      scrollFrame = 0;
      if (!chatScroll) return;
      if (!force && !shouldFollowOutput) return;
      chatScroll.scrollTop = chatScroll.scrollHeight;
      shouldFollowOutput = true;
    });
  }

  async function updateToolOption(key, event) {
    const targetSession = $currentSession;
    const previousTools = sessionTools;
    const nextTools = filterHiddenSessionTools({
      ...sessionTools,
      [key]: Boolean(event.currentTarget.checked)
    }, $features);
    setSessionTools(sessionToolKey, nextTools);
    if (!targetSession) return;
    try {
      const updated = await patchSessionRuntime(
        targetSession,
        { capabilities: { [key]: Boolean(event.currentTarget.checked) } }
      );
      if (targetSession === $currentSession) {
        sessionRuntime.set(updated);
        sessionRuntimeValue = updated;
        setSessionTools(targetSession, { ...nextTools, [key]: Boolean(updated?.capabilities?.[key]?.enabled) });
        await loadSessionEvents(targetSession);
      }
      await refreshSessions();
    } catch (err) {
      setSessionTools(targetSession, previousTools);
      setError(err);
    }
  }

  function isToolToggleVisible(item) {
    if (item?.key === 'webSearch' || item?.key === 'a2aMaster') {
      return $features[item.key] === true;
    }
    return true;
  }

  function filterHiddenSessionTools(tools = {}, featureState = {}) {
    return {
      ...tools,
      webSearch: featureState.webSearch === true && tools.webSearch === true,
      a2aMaster: featureState.a2aMaster === true && tools.a2aMaster === true
    };
  }

  function onDirSelect(e) {
    workDir = e.detail.path;
    showBrowser = false;
  }

  async function chooseWorkDir() {
    const desktop = globalThis.__MOTHX_DESKTOP__;
    if (!desktop?.chooseDirectory) {
      showBrowser = true;
      return;
    }
    try {
      const selected = await desktop.chooseDirectory(workDir.trim());
      if (selected) workDir = selected;
    } catch (err) {
      setError(err);
    }
  }

  async function handleImageSelect(event) {
    const files = Array.from(event.target.files || []);
    if (files.length === 0) return;
    if (!selectedModelSupportsImages) {
      setError($t('chat.error.modelNoImages'));
      event.target.value = '';
      return;
    }
    try {
      const next = await Promise.all(files.map(readImageFile));
      imageUploads = [...imageUploads, ...next].slice(0, 6);
    } catch (err) {
      setError(err);
    } finally {
      event.target.value = '';
    }
  }

  function readImageFile(file) {
    if (!file.type.startsWith('image/')) {
      throw new Error($t('chat.error.unsupportedFileType', { name: file.name }));
    }
    return new Promise((resolve, reject) => {
      const reader = new FileReader();
      reader.onload = () => resolve({
        name: file.name,
        type: file.type,
        size: file.size,
        dataUrl: String(reader.result || '')
      });
      reader.onerror = () => reject(new Error($t('chat.error.imageReadFailed', { name: file.name })));
      reader.readAsDataURL(file);
    });
  }

  function removeImage(index) {
    imageUploads = imageUploads.filter((_, i) => i !== index);
  }

  function clearImages() {
    imageUploads = [];
    if (imageInput) imageInput.value = '';
  }

  function formatImageSize(bytes) {
    if (!bytes) return '';
    if (bytes < 1024 * 1024) return `${Math.max(1, Math.round(bytes / 1024))} KB`;
    return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
  }


  function recordApprovalResolution(resolution, sessionID) {
    if (!resolution?.approvalId || !sessionID) return;
    const id = `approval-resolution-${resolution.approvalId}-${resolution.status || 'resolved'}`;
    applySessionViewReducer(sessionID, (view) => ({
      view: reduceRunEvent(view, {
        id,
        sessionId: sessionID,
        eventType: 'approval_resolved',
        status: resolution.status || 'resolved',
        timestamp: resolution.timestamp || new Date().toISOString(),
        data: { resolution }
      })
    }));
  }

  function approvalToolView(approval) {
    const tool = approval?.tool || {};
    return buildToolCallView(tool.name || '', tool.args || tool.details || {}, '', $t);
  }

  function approvalBashCommand(approval) {
    const args = approval?.tool?.args || {};
    return approval?.tool?.details?.command || args.command || args.cmd || '';
  }

  function approvalBashWorkDir(approval) {
    return approval?.tool?.details?.workDir || approval?.context?.workDir || '';
  }

  async function respondApproval(approval, action) {
    const sessionID = approvalSessionID(approval, $currentSession);
    if (!approval?.approvalId || !sessionID || approvalSubmitting) return;
    approvalSubmitting = true;
    try {
      const resolved = await postJSON(`/api/sessions/${encodeURIComponent(sessionID)}/approvals/${encodeURIComponent(approval.approvalId)}`, { action });
      recordApprovalResolution(resolved, sessionID);
      applySessionViewReducer(sessionID, (view) => ({ view: reduceApprovalResolved(view, resolved) }));
      if (sessionID === $currentSession) {
        activeApproval.set(null);
        selectedApprovalID = '';
        showApprovalCenter = false;
      }
    } catch (err) { setError(err); }
    finally { approvalSubmitting = false; }
  }

  async function loadSessionRuntime(id) {
    if (!id) {
      sessionRuntime.set(null);
      sessionRuntimeValue = null;
      return;
    }
    try {
      const snapshot = await getSessionRuntime(id);
      if (id !== $currentSession) return;
      sessionRuntime.set(snapshot);
      sessionRuntimeValue = snapshot;
      const enabledTools = Object.fromEntries(Object.entries(snapshot?.capabilities || {}).map(([key, state]) => [key, Boolean(state?.enabled)]));
      setSessionTools(id, { ...sessionTools, ...enabledTools });
    } catch (err) {
      if (id === $currentSession) setError(err);
    }
  }

  async function updateRuntime(patch) {
    const id = $currentSession;
    if (!id || runtimeUpdating) return;
    const previous = sessionRuntimeValue;
    runtimeUpdating = true;
    try {
      const snapshot = await patchSessionRuntime(id, patch);
      if (id === $currentSession) {
        sessionRuntime.set(snapshot);
        sessionRuntimeValue = snapshot;
        const enabledTools = Object.fromEntries(Object.entries(snapshot?.capabilities || {}).map(([key, state]) => [key, Boolean(state?.enabled)]));
        setSessionTools(id, { ...sessionTools, ...enabledTools });
      }
      await refreshSessions();
    } catch (err) {
      sessionRuntime.set(previous);
      sessionRuntimeValue = previous;
      setError(err);
    } finally {
      runtimeUpdating = false;
    }
  }

  async function setMode(mode) {
    if (!$currentSession) {
      newSessionMode = mode;
      return;
    }
    await updateRuntime({ mode });
  }

  async function loadSessionEvents(id) {
    if (!id) {
      sessionRunEvents = [];
      sessionCapabilityEvents = [];
      approvalHistory = [];
      return;
    }
    try {
      const [runs, caps] = await Promise.all([
        getSessionRunEvents(id),
        getSessionCapabilityEvents(id)
      ]);
      if (id !== $currentSession) return;
      sessionRunEvents = runs || [];
      approvalHistory = approvalHistoryFromRunEvents(sessionRunEvents);
      sessionCapabilityEvents = caps || [];
    } catch {
      if (id !== $currentSession) return;
      sessionRunEvents = [];
      sessionCapabilityEvents = [];
      approvalHistory = [];
    }
  }

  async function loadSubAgents(id) {
    if (!id) {
      subAgents = [];
      return;
    }
    const agents = await getSessionSubAgents(id);
    if (id !== $currentSession) return;
    subAgents = mergeSubAgents(subAgents, agents || []);
    if (showSubAgentModal) {
      if (!selectedSubAgentID && subAgents.length > 0) {
        selectedSubAgentID = subAgents[0].id;
      }
      if (selectedSubAgentID) {
        await loadSubAgentMessages(selectedSubAgentID);
      }
    }
  }

  function scheduleSubAgentRefresh(delay = 250) {
    if (!$currentSession) return;
    if (subAgentRefreshTimer) clearTimeout(subAgentRefreshTimer);
    const targetSession = $currentSession;
    subAgentRefreshTimer = setTimeout(() => {
      subAgentRefreshTimer = 0;
      if (targetSession === $currentSession) {
        loadSubAgents(targetSession).catch(() => {});
      }
    }, delay);
  }

  function mergeSubAgents(existing = [], incoming = []) {
    const byID = new Map();
    for (const item of existing) {
      if (item?.id) byID.set(item.id, item);
    }
    for (const item of incoming) {
      if (!item?.id) continue;
      byID.set(item.id, { ...byID.get(item.id), ...item });
    }
    return Array.from(byID.values()).sort((a, b) => {
      const left = Date.parse(a.startedAt || a.updatedAt || '') || 0;
      const right = Date.parse(b.startedAt || b.updatedAt || '') || 0;
      if (left !== right) return left - right;
      return String(a.id).localeCompare(String(b.id));
    });
  }

  async function loadSubAgentMessages(agentID) {
    if (!$currentSession || !agentID) {
      subAgentModalMessages = [];
      return;
    }
    subAgentModalLoading = true;
    subAgentModalError = '';
    try {
      const msgs = await getSessionSubAgentMessages($currentSession, agentID);
      if (agentID !== selectedSubAgentID) return;
      const normalized = (msgs || []).map((msg) => normalizeSessionMessage(msg, $t)).filter(Boolean);
      const live = subAgentTranscripts[agentID] || [];
      subAgentModalMessages = mergeMessageLists(normalized, live);
    } catch (err) {
      subAgentModalError = err instanceof Error ? err.message : String(err || '');
      subAgentModalMessages = subAgentTranscripts[agentID] || [];
    } finally {
      subAgentModalLoading = false;
    }
  }

  function mergeMessageLists(base = [], live = []) {
    let out = [...base];
    for (const item of live) {
      out = upsertMessageInList(out, item);
    }
    return out;
  }

  function openSubAgentModal(agentID = '') {
    selectedSubAgentID = agentID || selectedSubAgentID || subAgents[0]?.id || '';
    showSubAgentModal = true;
    if ($currentSession) {
      loadSubAgents($currentSession).catch(() => {});
    }
    if (selectedSubAgentID) {
      loadSubAgentMessages(selectedSubAgentID).catch(() => {});
    }
  }

  function closeSubAgentModal() {
    showSubAgentModal = false;
    subAgentModalError = '';
  }

  function selectSubAgent(agentID) {
    selectedSubAgentID = agentID;
    subAgentModalMessages = subAgentTranscripts[agentID] || [];
    loadSubAgentMessages(agentID).catch(() => {});
  }

  function beginOptimisticRunEvent(sessionID = $currentSession) {
    const id = `local-run-${Date.now()}`;
    const runID = `local_${Date.now()}`;
    const event = {
      id,
      runId: runID,
      sessionId: sessionID || '',
      eventType: 'started',
      source: 'webui',
      status: 'running',
      model: $selectedModel || 'default',
      mode: activeSession?.mode || '',
      timestamp: new Date().toISOString(),
      data: {
        workDir: isNewSession ? workDir.trim() : activeSessionWorkDir,
        optimistic: true
      }
    };
    sessionRunEvents = [...sessionRunEvents.filter((item) => item.id !== id), event];
    return id;
  }

  function finishOptimisticRunEvent(sessionID, status, error = '') {
    if (optimisticRunEventID && sessionID) {
      const localID = optimisticRunEventID;
      applySessionViewReducer(sessionID, (view) => {
        const idx = view.runEvents.findIndex((item) => item.id === localID);
        if (idx < 0) return { view };
        const eventType = status === 'failed' ? 'failed' : status === 'canceled' ? 'canceled' : 'finished';
        const runEvents = [...view.runEvents];
        runEvents[idx] = {
          ...runEvents[idx], eventType, status, timestamp: new Date().toISOString(),
          data: { ...(runEvents[idx].data || {}), ...(error ? { error } : {}) }
        };
        return { view: { ...view, runEvents } };
      });
    }
    if (sessionID) upsertSession({ id: sessionID, active: true, running: false });
  }

  function errorMessage(error) {
    return String(error?.message || error || '').trim();
  }

  function resetSessionStreamCursor() {
    sessionStreamCursor = { entrySeq: 0, runSeq: 0, capabilitySeq: 0 };
  }

  function updateSessionStreamCursorFromState() {
    sessionStreamCursor = {
      entrySeq: maxSeq(messages),
      runSeq: maxSeq(sessionRunEvents),
      capabilitySeq: maxSeq(sessionCapabilityEvents)
    };
  }

  function startSessionStream(id) {
    if (!id || isCompletionActive(getSessionState(id))) return;
    const state = getSessionState(id);
    if (state.observer?.controller) return;
    const cursor = { ...(state.cursor || sessionStreamCursor) };
    const abort = new AbortController();
    registerObserver(id, abort);
    consumeSessionStream(id, cursor, abort).finally(() => {
      clearObserver(id, abort);
    });
  }

  async function consumeSessionStream(id, cursor, abort) {
    const params = new URLSearchParams();
    if (cursor.entrySeq > 0) params.set('after_entry_seq', String(cursor.entrySeq));
    if (cursor.runSeq > 0) params.set('after_run_seq', String(cursor.runSeq));
    if (cursor.capabilitySeq > 0) params.set('after_capability_seq', String(cursor.capabilitySeq));
    const query = params.toString();
    try {
      const res = await fetch(`/api/sessions/${encodeURIComponent(id)}/stream${query ? `?${query}` : ''}`, {
        signal: abort.signal
      });
      if (!res.ok || !res.body) {
        const text = await res.text();
        let data = null;
        try { data = text ? JSON.parse(text) : null; } catch { data = null; }
        throw new Error(data?.error?.message || data?.error || data?.message || `${res.status} ${res.statusText}`);
      }
      await readSSE(res.body, (event) => handleSessionStreamEvent(id, event));
    } catch (err) {
      if (err?.name !== 'AbortError') {
        setError(err);
      }
    }
  }

  function handleSessionStreamEvent(id, event) {
    if (!id || event.data === '[DONE]') return;
    const visible = id === $currentSession;

    if (event.event === 'status') {
      try {
        const item = JSON.parse(event.data);
        if (item?.message) {
          const entry = { id: `stream-status-${Date.now()}`, sessionId: id, eventType: 'status', status: 'running', timestamp: new Date().toISOString(), data: { message: item.message } };
          applySessionViewReducer(id, (view) => ({ view: reduceRunEvent(view, entry) }));
        }
      } catch {}
      return;
    }
    if (event.event === 'done') {
      applySessionViewReducer(id, (view) => ({ view: reduceStreamDone(view) }));
      if (visible) {
        refreshSessions().catch(() => {});
        loadSessionMessages(id).catch(() => {});
        loadSubAgents(id).catch(() => {});
        refreshStatsSummary().catch(() => {});
      }
      return;
    }
    if (event.event === 'heartbeat') return;
    if (event.event === 'error') {
      // Only flag the local run as failed when this session owns the active
      // completion; background sessions must not poison another run's status.
      if (visible && isCompletionActive(getSessionState(id))) streamHadError = true;
      let message = event.data;
      try {
        const item = JSON.parse(event.data);
        if (item?.error) message = item.error;
      } catch {}
      applySessionViewReducer(id, (view) => reduceStreamError(view, message, $t));
      if (visible) setError(message);
      return;
    }
    if (event.event === 'transcript') {
      try {
        const item = JSON.parse(event.data);
        if (!eventBelongsToSession(id, item)) return;
        const { effects } = applySessionViewReducer(id, (view) => reduceTranscriptEvent(view, item, $t), { scroll: true });
        if (visible) handleSubAgentEffects(effects);
      } catch {
        // ignore malformed transcript frames
      }
      return;
    }
    if (event.event === 'run_event') {
      try {
        const item = JSON.parse(event.data);
        if (!eventBelongsToSession(id, item)) return;
        applySessionViewReducer(id, (view) => ({ view: reduceRunEvent(view, item) }));
        if ((item.status === 'failed' || item.eventType === 'failed') && item.data?.error) {
          if (visible && isCompletionActive(getSessionState(id))) streamHadError = true;
          applySessionViewReducer(id, (view) => reduceStreamError(view, item.data.error, $t));
        }
      } catch {
        // ignore malformed event frames
      }
      return;
    }
    if (event.event === 'runtime_event') {
      try {
        const snapshot = JSON.parse(event.data);
        if (!eventBelongsToSession(id, snapshot)) return;
        applySessionViewReducer(id, (view) => ({ view: reduceRuntimeSnapshot(view, snapshot) }));
      } catch {
        // ignore malformed runtime frames
      }
      return;
    }
    if (event.event === 'approval_request') {
      try {
        const item = typeof event.data === 'string' ? JSON.parse(event.data) : event.data;
        if (!item?.approvalId || !eventBelongsToSession(id, item)) return;
        const { effects } = applySessionViewReducer(id, (view) => reduceApprovalRequest(view, item, id));
        if (visible && effects.applies) {
          activeApproval.set(item);
          selectedApprovalID = item.approvalId;
          showApprovalCenter = true;
        }
      } catch {
        // ignore malformed approval frames
      }
      return;
    }
    if (event.event === 'approval_resolved') {
      try {
        const item = JSON.parse(event.data);
        const resolvedSessionID = approvalSessionID(item, id);
        if (resolvedSessionID) {
          recordApprovalResolution(item, resolvedSessionID);
          applySessionViewReducer(resolvedSessionID, (view) => ({ view: reduceApprovalResolved(view, item) }));
        }
        if (!visible || resolvedSessionID !== id) return;
        if (selectedApprovalID === item.approvalId) {
          activeApproval.set(null);
          selectedApprovalID = '';
        }
      } catch {
        // ignore malformed approval frames
      }
      return;
    }
    if (event.event === 'tool_event') {
      try {
        const item = JSON.parse(event.data);
        if (!eventBelongsToSession(id, item)) return;
        const { effects } = applySessionViewReducer(id, (view) => reduceToolStatusEvent(view, item, $t), { scroll: true });
        if (visible) handleSubAgentEffects(effects);
      } catch {
        // ignore malformed tool frames
      }
      return;
    }
    if (event.event === 'capability_event') {
      try {
        const item = JSON.parse(event.data);
        if (!eventBelongsToSession(id, item)) return;
        applySessionViewReducer(id, (view) => ({ view: reduceCapabilityEvent(view, item) }));
      } catch {
        // ignore malformed event frames
      }
    }
  }

  // handleSubAgentEffects applies view-only side effects reported by reducers
  // (sub-agent list refresh, open modal sync). Visible session only.
  function handleSubAgentEffects(effects = {}) {
    if (effects.subAgentRefresh) scheduleSubAgentRefresh();
    if (showSubAgentModal && selectedSubAgentID && effects.subAgentTranscriptAgent === selectedSubAgentID) {
      subAgentModalMessages = subAgentTranscripts[selectedSubAgentID] || [];
    }
  }

  function buildSessionEventSummary(runEvents = [], capabilityEvents = [], workDir = '', model = '') {
    const runs = mergeRunEvents(runEvents);
    const currentModel = model && model !== 'default' ? model : '';
    const matchingRuns = runs.filter((run) => {
      if (!run.usage) return false;
      if (currentModel && run.model && run.model !== currentModel) return false;
      if (workDir && run.workDir && run.workDir !== workDir) return false;
      return true;
    });
    const totals = runs.reduce((acc, run) => {
      if (!run.usage) return acc;
      acc.promptTokens += run.usage.promptTokens;
      acc.completionTokens += run.usage.completionTokens;
      acc.totalTokens += run.usage.totalTokens;
      acc.cacheReadTokens += run.usage.cacheReadTokens;
      acc.cacheWriteTokens += run.usage.cacheWriteTokens;
      return acc;
    }, { promptTokens: 0, completionTokens: 0, totalTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0 });
    return {
      visible: runs.length > 0 || capabilityEvents.length > 0,
      lastRun: runs[0] || null,
      runCount: runs.length,
      capabilityCount: capabilityEvents.length,
      model: currentModel || runs[0]?.model || '',
      workDir: workDir || runs[0]?.workDir || '',
      matchingRuns: matchingRuns.length,
      ...totals
    };
  }

  function buildSubAgentSummary(agents = []) {
    const list = (agents || []).filter((item) => item?.id);
    const running = list.filter((item) => item.status === 'running' || item.status === 'ready').length;
    const failed = list.filter((item) => item.status === 'error' || item.status === 'failed').length;
    const done = list.filter((item) => item.status === 'done' || item.status === 'destroyed').length;
    return {
      visible: list.length > 0,
      count: list.length,
      running,
      failed,
      done,
      label: running > 0
        ? $t('chat.subagents.running', { count: running, total: list.length })
        : failed > 0
          ? $t('chat.subagents.failed', { count: failed, total: list.length })
          : $t('chat.subagents.done', { count: done || list.length, total: list.length })
    };
  }

  function selectModel(modelID) {
    $selectedModel = modelID;
    showModelPicker = false;
  }

  // Derived (not a template function call): Svelte cannot see reactive
  // reads inside function bodies, so {modelLabel()} in the template would be
  // evaluated once at mount and never update.
  $: currentModelLabel =
    (modelOptions.find((model) => model.id === $selectedModel)?.name ||
      modelOptions.find((model) => model.id === $selectedModel)?.id) ||
    $t('chat.defaultModel');
  function subAgentStateClass(agent) {
    if (!agent) return 'done';
    if (agent.status === 'error' || agent.status === 'failed') return 'error';
    if (agent.status === 'running' || agent.status === 'ready') return 'running';
    return 'done';
  }

  function subAgentStatusLabel(status) {
    if (status === 'running' || status === 'ready') return $t('common.running');
    if (status === 'error' || status === 'failed') return $t('common.failed');
    if (status === 'destroyed') return $t('chat.subagents.destroyed');
    return $t('common.completed');
  }

  function mergeRunEvents(events = []) {
    const byRun = new Map();
    for (const event of events) {
      const runId = event.runId || event.id || '';
      if (!runId) continue;
      const run = byRun.get(runId) || {
        runId,
        eventType: '',
        status: '',
        model: '',
        mode: '',
        workDir: '',
        timestamp: '',
        usage: null
      };
      const eventTime = Date.parse(event.timestamp || '') || 0;
      const runTime = Date.parse(run.timestamp || '') || 0;
      if (eventTime >= runTime) {
        run.timestamp = event.timestamp || run.timestamp;
        run.eventType = event.eventType || run.eventType;
        run.status = event.status || run.status;
      }
      if (event.model) run.model = event.model;
      if (event.mode) run.mode = event.mode;
      if (event.data?.workDir) run.workDir = event.data.workDir;
      const usage = normalizeRunUsage(event.data?.usage);
      if (usage) run.usage = usage;
      byRun.set(runId, run);
    }
    return Array.from(byRun.values())
      .sort((a, b) => (Date.parse(b.timestamp || '') || 0) - (Date.parse(a.timestamp || '') || 0));
  }

  function normalizeRunUsage(raw) {
    if (!raw || typeof raw !== 'object') return null;
    const promptTokens = readNumber(raw, ['prompt_tokens', 'promptTokens', 'inputTokens', 'input']);
    const completionTokens = readNumber(raw, ['completion_tokens', 'completionTokens', 'outputTokens', 'output']);
    const cacheReadTokens = readNumber(raw, ['cache_read_tokens', 'cacheReadTokens', 'cacheRead', 'cached_tokens']);
    const cacheWriteTokens = readNumber(raw, ['cache_write_tokens', 'cacheWriteTokens', 'cacheWrite']);
    const explicitTotal = readNumber(raw, ['total_tokens', 'totalTokens']);
    const totalTokens = explicitTotal || promptTokens + completionTokens;
    if (promptTokens === 0 && completionTokens === 0 && totalTokens === 0 && cacheReadTokens === 0 && cacheWriteTokens === 0) return null;
    return { promptTokens, completionTokens, totalTokens, cacheReadTokens, cacheWriteTokens };
  }

  function readNumber(source, keys) {
    for (const key of keys) {
      const value = Number(source?.[key]);
      if (Number.isFinite(value) && value > 0) return value;
    }
    return 0;
  }

  function sessionRunStateClass(run) {
    if (!run) return 'done';
    if (run.status === 'failed' || run.eventType === 'failed') return 'error';
    if (run.status === 'running' || run.eventType === 'started') return 'running';
    return 'done';
  }

  function sessionRunLabel(run) {
    if (!run) return $t('chat.sessionEvents.idle');
    if (run.status === 'running' || run.eventType === 'started') return $t('common.running');
    if (run.status === 'failed' || run.eventType === 'failed') return $t('common.failed');
    if (run.status === 'canceled' || run.eventType === 'canceled') return $t('chat.sessionEvents.canceled');
    return $t('common.completed');
  }

  function formatCompactTokens(value) {
    const n = Number(value) || 0;
    if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(n >= 10_000_000 ? 0 : 1)}M`;
    if (n >= 1_000) return `${(n / 1_000).toFixed(n >= 10_000 ? 0 : 1)}K`;
    return String(n);
  }

  function formatCacheRate(summary) {
    if (!summary || summary.promptTokens <= 0) return '--';
    const pct = Math.min(100, Math.max(0, (summary.cacheReadTokens / summary.promptTokens) * 100));
    return `${Math.round(pct)}%`;
  }

  function compactPath(path) {
    if (!path) return '';
    const normalized = String(path).replace(/\/$/, '');
    const parts = normalized.split('/').filter(Boolean);
    if (parts.length <= 2) return normalized || '/';
    return `.../${parts.slice(-2).join('/')}`;
  }

  function compactWorkDir(path) {
    if (!path) return '';
    const parts = String(path).split('/').filter(Boolean);
    if (parts.length === 0) return '/';
    return parts[parts.length - 1];
  }

  function sessionEventTooltip(summary) {
    if (!summary) return '';
    const parts = [];
    if (summary.workDir) parts.push(summary.workDir);
    if (summary.model) parts.push(summary.model);
    parts.push(`${formatCompactTokens(summary.totalTokens)} tokens`);
    parts.push(`cache ${formatCacheRate(summary)}`);
    if (summary.lastRun?.timestamp) parts.push(formatEventTime(summary.lastRun.timestamp));
    return parts.join(' · ');
  }

  function formatEventTime(value) {
    if (!value) return '';
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return '';
    return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
  }

  function planStatusLabel(status) {
    switch (status) {
      case 'done': return $t('chat.plan.done');
      case 'running': return $t('chat.plan.running');
      case 'failed': return $t('chat.plan.failed');
      default: return $t('chat.plan.pending');
    }
  }

  async function loadToolResultDetail(msg, event) {
    if (!event.currentTarget.open || !msg.hasDetail || msg.detailLoaded || msg.detailLoading) return;
    if (!$currentSession || !msg.toolCallId) return;
    msg.detailLoading = true;
    msg.detailError = '';
    messages = messages;
    try {
      const detail = await getSessionToolResult($currentSession, msg.toolCallId);
      msg.detail = normalizeToolResultDetail(detail);
      msg.detailLoaded = true;
    } catch (err) {
      msg.detailError = err instanceof Error ? err.message : String(err || $t('chat.tool.detailLoadFailed'));
    } finally {
      msg.detailLoading = false;
      messages = messages;
    }
  }

  function normalizeToolResultDetail(detail) {
    if (!detail) return { content: '', images: [] };
    const images = [];
    for (const block of detail.contents || []) {
      if (block.type !== 'image' || !block.image?.data || !block.image?.mimeType) continue;
      images.push({
        name: block.image.mimeType,
        type: block.image.mimeType,
        size: block.image.bytes || block.image.originalBytes || 0,
        dataUrl: `data:${block.image.mimeType};base64,${block.image.data}`
      });
    }
    const content = detail.content || textFromContents(detail.contents);
    return {
      toolName: detail.toolName || '',
      kind: toolResultKind(detail.toolName, content),
      content: detail.content || textFromContents(detail.contents),
      images,
      readLines: parseReadResult(content),
      lsEntries: parseLsResult(content),
      grepMatches: parseGrepResult(content),
      bashResult: parseBashResult(content),
      browserResult: parseBrowserResult(content),
      subAgentResult: parseSubAgentResult(content),
      workflowLintResult: parseWorkflowLintResult(content)
    };
  }

  function codeBlockControls(node) {
    const handleClick = (event) => { void copyCodeBlock(event); };
    node.addEventListener('click', handleClick);
    node.addEventListener('toggle', updateCodeBlockToggle);
    return {
      destroy() {
        node.removeEventListener('click', handleClick);
        node.removeEventListener('toggle', updateCodeBlockToggle);
      }
    };
  }

  async function copyCodeBlock(event) {
    const button = event.target.closest('.code-copy');
    if (!button) return;
    event.preventDefault();
    event.stopPropagation();
    const code = button.closest('.code-block')?.querySelector('code')?.textContent || '';
    if (!code) return;
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(code);
      } else {
        const textarea = document.createElement('textarea');
        textarea.value = code;
        textarea.style.position = 'fixed';
        textarea.style.opacity = '0';
        document.body.appendChild(textarea);
        textarea.select();
        document.execCommand('copy');
        textarea.remove();
      }
      button.textContent = 'Copied';
      button.classList.add('copied');
      window.setTimeout(() => {
        button.textContent = 'Copy';
        button.classList.remove('copied');
      }, 1600);
    } catch {
      button.textContent = 'Failed';
      window.setTimeout(() => { button.textContent = 'Copy'; }, 1600);
    }
  }

  function updateCodeBlockToggle(event) {
    const block = event.target.closest('.code-block');
    if (!block) return;
    const toggle = block.querySelector('.code-block-toggle');
    if (toggle) toggle.textContent = block.open ? toggle.dataset.collapse : toggle.dataset.expand;
  }
</script>

<section class="chat-view">
  {#if subAgentSummary.visible}
    <button type="button" class="subagent-strip" on:click={() => openSubAgentModal()}>
      <span class="dot {subAgentSummary.failed > 0 ? 'error' : subAgentSummary.running > 0 ? 'running' : 'done'}"></span>
      <strong>{$t('chat.subagents.title')}</strong>
      <span>{subAgentSummary.label}</span>
      <em>{$t('chat.subagents.open')}</em>
    </button>
  {/if}
  <div class="chat-scroll" bind:this={chatScroll} on:scroll={handleChatScroll}>
    {#if messages.length === 0 && !busy}
      <div class="welcome">
        <h2>{$t('chat.welcome')}</h2>
        <div class="suggestions">
          {#each suggestions as key}
            <button
              type="button"
              class="chip"
              disabled={!apiEnabled || (isNewSession && !workDir.trim())}
              on:click={() => pick($t(key))}
            >
              {$t(key)}
            </button>
          {/each}
        </div>
      </div>
    {:else}
      <div class="transcript">
        {#each messages as msg, idx}
          {#if msg.role === 'user'}
            <article class="msg user">
              <div class="meta">
                <strong>{$t('chat.you')}</strong>
                <span>{shortID($currentSession)}</span>
              </div>
              <p>{msg.content}</p>
              {#if msg.images?.length}
                <div class="msg-images">
                  {#each msg.images as image}
                    <img src={image.dataUrl} alt={image.name} on:load={() => scrollChatToBottom()} />
                  {/each}
                </div>
              {/if}
            </article>
          {:else if msg.role === 'assistant'}
            <article class="msg assistant" class:error={msg.isError}>
              <div class="meta">
                <strong>MothX</strong>
                <span>{msg.isError ? $t('common.failed') : busy && idx === messages.length - 1 ? $t('chat.generating') : $t('common.completed')}</span>
              </div>
              {#if msg.content}
                <div class="markdown" use:codeBlockControls>{@html markdownToHTML(msg.content)}</div>
              {:else if busy && idx === messages.length - 1}
                <p class="pending-text">{$t('chat.waitingModel')}</p>
              {/if}
            </article>
          {:else if msg.role === 'plan'}
            <article class="msg plan-card">
              <div class="meta">
                <strong>{$t('chat.plan')}</strong>
                {#if msg.toolCallId}<span>{shortID(msg.toolCallId)}</span>{/if}
              </div>
              <section class="todo-plan">
                {#if msg.plan.title}
                  <h3>{msg.plan.title}</h3>
                {/if}
                <ol>
                  {#each msg.plan.steps as step}
                    <li class:done={step.status === 'done'} class:running={step.status === 'running'} class:failed={step.status === 'failed'}>
                      <span class="todo-mark" aria-hidden="true"></span>
                      <span class="todo-title">{step.title}</span>
                      <em>{planStatusLabel(step.status)}</em>
                    </li>
                  {/each}
                </ol>
                {#if msg.plan.note}
                  <p>{msg.plan.note}</p>
                {/if}
              </section>
            </article>
          {:else if msg.role === 'toolCall'}
            <article class="msg tool-call">
              <div class="meta">
                <strong>{$t('chat.toolCall')}</strong>
                <span>{msg.toolName}</span>
              </div>
              <div class="tool-call-body">
                <div class="tool-title">
                  <span class="dot running"></span>
                  <strong>{msg.callView?.label || msg.toolName}</strong>
                  {#if msg.callView?.target}
                    <span class="tool-target">{msg.callView.target}</span>
                  {/if}
                  {#if msg.toolCallId}<em>{shortID(msg.toolCallId)}</em>{/if}
                </div>
                {#if msg.callView?.details?.length}
                  <div class="tool-call-tags">
                    {#each msg.callView.details as item}
                      <span>{item}</span>
                    {/each}
                  </div>
                {/if}
                {#if msg.callView?.kind === 'edit' && msg.callView.edits?.length}
                  <div class="edit-call">
                    {#each msg.callView.edits as edit}
                      <section class="edit-block">
                        <div class="edit-block-head">
                          <strong>{$t('chat.tool.edit.editNumber', { number: edit.index })}</strong>
                          <span>{$t('chat.tool.edit.lineChange', { old: edit.oldLines, next: edit.newLines })}</span>
                        </div>
                        <div class="edit-columns">
                          <div class="edit-pane old">
                            <span>{$t('chat.tool.edit.oldText')}</span>
                            <pre class:empty={edit.oldText === ''}><code>{@html edit.oldText ? highlightedCodeToHTML(edit.oldText, msg.callView.target) : $t('chat.tool.edit.empty')}</code></pre>
                          </div>
                          <div class="edit-pane new">
                            <span>{$t('chat.tool.edit.newText')}</span>
                            <pre class:empty={edit.newText === ''}><code>{@html edit.newText ? highlightedCodeToHTML(edit.newText, msg.callView.target) : $t('chat.tool.edit.empty')}</code></pre>
                          </div>
                        </div>
                      </section>
                    {/each}
                  </div>
                {:else if msg.callView?.kind === 'write'}
                  <div class="write-call">
                    <div class="write-call-head">
                      <strong>{$t('chat.tool.write.preview')}</strong>
                      <span>{$t('chat.tool.write.summary', { lines: msg.callView.lines, chars: msg.callView.chars })}</span>
                    </div>
                    <span>{$t('chat.tool.write.content')}</span>
                    <pre class:empty={msg.callView.content === ''}>{msg.callView.content || $t('chat.tool.edit.empty')}</pre>
                  </div>
                {:else if msg.callView?.kind === 'insert'}
                  <div class="write-call">
                    <div class="write-call-head">
                      <strong>{$t('chat.tool.insert.preview')}</strong>
                      <span>{$t('chat.tool.insert.summary', { lines: msg.callView.lines, chars: msg.callView.chars })}</span>
                    </div>
                    <span>{$t('chat.tool.insert.content')}</span>
                    <pre class:empty={msg.callView.content === ''}>{msg.callView.content || $t('chat.tool.edit.empty')}</pre>
                  </div>
                {:else if msg.callView?.kind === 'find'}
                  <div class="find-call">
                    <div class="find-row">
                      <span>{$t('chat.tool.find.pattern')}</span>
                      <code>{msg.callView.pattern || $t('chat.tool.find.missing')}</code>
                    </div>
                    <div class="find-row">
                      <span>{$t('chat.tool.find.searchPath')}</span>
                      <code>{msg.callView.path}</code>
                    </div>
                    {#if msg.callView.maxDepth !== ''}
                      <div class="find-row">
                        <span>{$t('chat.tool.find.depth')}</span>
                        <code>{msg.callView.maxDepth}</code>
                      </div>
                    {/if}
                    {#if msg.callView.maxResults !== ''}
                      <div class="find-row">
                        <span>{$t('chat.tool.find.resultLimit')}</span>
                      <code>{msg.callView.maxResults}</code>
                    </div>
                  {/if}
                  </div>
                {:else if msg.callView?.kind === 'browser'}
                  <div class="browser-call">
                    <div class="find-row">
                      <span>{$t('chat.tool.browser.action')}</span>
                      <code>{msg.callView.action || $t('chat.tool.browser.missing')}</code>
                    </div>
                    {#if msg.callView.url}
                      <div class="find-row">
                        <span>{$t('chat.tool.browser.url')}</span>
                        <code>{msg.callView.url}</code>
                      </div>
                    {/if}
                    {#if msg.callView.selector}
                      <div class="find-row">
                        <span>{$t('chat.tool.browser.selectorLabel')}</span>
                        <code>{msg.callView.selector}</code>
                      </div>
                    {/if}
                    {#if msg.callView.value}
                      <div class="find-row">
                        <span>{$t('chat.tool.browser.value')}</span>
                        <code>{msg.callView.value}</code>
                      </div>
                    {/if}
                    {#if msg.callView.expression}
                      <div class="find-row">
                        <span>{$t('chat.tool.browser.expression')}</span>
                        <code>{msg.callView.expression}</code>
                      </div>
                    {/if}
                  </div>
                {:else if msg.callView?.kind === 'skill-ref'}
                  <div class="skill-ref-call">
                    <div class="find-row">
                      <span>{$t('chat.tool.skillRef.skillLabel')}</span>
                      <code>{msg.callView.skill || $t('chat.tool.skillRef.missing')}</code>
                    </div>
                    <div class="find-row">
                      <span>{$t('chat.tool.skillRef.refLabel')}</span>
                      <code>{msg.callView.ref || $t('chat.tool.skillRef.missing')}</code>
                    </div>
                  </div>
                {:else if msg.callView?.kind === 'workflow-lint'}
                  <div class="workflow-lint-call">
                    <div class="write-call-head">
                      <strong>{$t('chat.tool.workflowLint.source')}</strong>
                      <span>{$t('chat.tool.write.summary', { lines: msg.callView.lines, chars: msg.callView.chars })}</span>
                    </div>
                    <pre class:empty={msg.callView.source === ''}>{msg.callView.source || $t('chat.tool.workflowLint.missing')}</pre>
                  </div>
                {:else if msg.callView?.kind === 'subagent-task'}
                  <div class="subagent-call">
                    <span>{$t('chat.tool.subagent.task')}</span>
                    <p>{msg.callView.task || msg.callView.target}</p>
                  </div>
                {:else if msg.callView?.kind === 'subagent-handle'}
                  <div class="subagent-call compact">
                    <div class="find-row">
                      <span>{$t('chat.tool.subagent.handle')}</span>
                      <code>{msg.callView.handle || $t('chat.tool.subagent.handleMissing')}</code>
                    </div>
                    {#if msg.callView.message}
                      <div class="find-row">
                        <span>{$t('chat.tool.subagent.message')}</span>
                        <code>{msg.callView.message}</code>
                      </div>
                    {/if}
                  </div>
                {/if}
                {#if msg.callView?.kind !== 'generic' && msg.arguments}
                  <details class="tool-raw">
                    <summary>{$t('chat.argsJson')}</summary>
                    <pre>{formatArgs(msg.arguments)}</pre>
                  </details>
                {:else if msg.arguments}
                  <pre>{formatArgs(msg.arguments)}</pre>
                {:else if msg.invalidArguments}
                  <pre>{msg.invalidArguments}</pre>
                {/if}
              </div>
            </article>
          {:else if msg.role === 'toolResult'}
            <article class="msg tool-result">
              <details on:toggle={(event) => loadToolResultDetail(msg, event)}>
                <summary>
                  <span class="dot {msg.isError ? 'error' : 'done'}"></span>
                  <strong>{msg.toolName}</strong>
                  <span>{msg.isError ? $t('common.failed') : $t('common.completed')}</span>
                  <em>{msg.summary}</em>
                </summary>
                {#if msg.detailLoading}
                  <p class="pending-text">{$t('chat.loadingToolResult')}</p>
                {:else if msg.detailError}
                  <p class="error-text">{msg.detailError}</p>
                {:else if msg.detailLoaded}
                  {#if msg.detail?.kind === 'browser' && msg.detail.browserResult}
                    <div class="browser-result">
                      <div class="browser-result-head">
                        <strong>{msg.detail.browserResult.status}</strong>
                        {#if msg.detail.browserResult.title}<span>{msg.detail.browserResult.title}</span>{/if}
                      </div>
                      {#if msg.detail.browserResult.url}
                        <code>{msg.detail.browserResult.url}</code>
                      {/if}
                      {#if !msg.detail.browserResult.title && !msg.detail.browserResult.url && msg.detail.browserResult.content}
                        <pre>{msg.detail.browserResult.content}</pre>
                      {/if}
                    </div>
                  {:else if msg.detail?.kind === 'subagent' && msg.detail.subAgentResult}
                    <div class="subagent-result">
                      {#if msg.detail.subAgentResult.handle}
                        <div><span>{$t('chat.tool.subagent.handle')}</span><code>{msg.detail.subAgentResult.handle}</code></div>
                      {/if}
                      {#if msg.detail.subAgentResult.status}
                        <div><span>{$t('chat.tool.subagent.statusLabel')}</span><strong>{msg.detail.subAgentResult.status}</strong></div>
                      {/if}
                      {#if msg.detail.subAgentResult.duration}
                        <div><span>{$t('chat.tool.subagent.duration')}</span><code>{msg.detail.subAgentResult.duration}</code></div>
                      {/if}
                      {#if msg.detail.subAgentResult.tool_calls !== undefined}
                        <div><span>{$t('chat.tool.subagent.toolCalls')}</span><code>{msg.detail.subAgentResult.tool_calls}</code></div>
                      {/if}
                      {#if msg.detail.subAgentResult.error}
                        <p class="error-text">{msg.detail.subAgentResult.error}</p>
                      {/if}
                      {#if msg.detail.subAgentResult.result || msg.detail.subAgentResult.last_response || msg.detail.subAgentResult.partial_result}
                        <pre>{msg.detail.subAgentResult.result || msg.detail.subAgentResult.last_response || msg.detail.subAgentResult.partial_result}</pre>
                      {/if}
                    </div>
                  {:else if msg.detail?.kind === 'skill-ref' && msg.detail.content}
                    <div class="skill-ref-result">
                      <div class="markdown" use:codeBlockControls>{@html markdownToHTML(msg.detail.content)}</div>
                    </div>
                  {:else if msg.detail?.kind === 'workflow-lint' && msg.detail.workflowLintResult}
                    <div class="workflow-lint-result">
                      <div class="workflow-lint-head">
                        <strong class:failed={!msg.detail.workflowLintResult.valid}>
                          {msg.detail.workflowLintResult.valid ? $t('chat.tool.workflowLint.valid') : $t('chat.tool.workflowLint.invalid')}
                        </strong>
                        {#if msg.detail.workflowLintResult.status}
                          <span>{msg.detail.workflowLintResult.status}</span>
                        {/if}
                      </div>
                      {#if msg.detail.workflowLintResult.error}
                        <p class="error-text">{msg.detail.workflowLintResult.error}</p>
                      {/if}
                      {#if msg.detail.workflowLintResult.tasks.length}
                        <section>
                          <strong>{$t('chat.tool.workflowLint.tasks')}</strong>
                          <div class="workflow-chip-row">
                            {#each msg.detail.workflowLintResult.tasks as task}
                              <code>{task}</code>
                            {/each}
                          </div>
                        </section>
                      {/if}
                      {#if msg.detail.workflowLintResult.results.length}
                        <section>
                          <strong>{$t('chat.tool.workflowLint.results')}</strong>
                          <div class="workflow-chip-row">
                            {#each msg.detail.workflowLintResult.results as result}
                              <code>{result}</code>
                            {/each}
                          </div>
                        </section>
                      {/if}
                    </div>
                  {:else if msg.detail?.kind === 'bash' && msg.detail.bashResult}
                    <div class="bash-result">
                      <div class="bash-meta">
                        {#if msg.detail.bashResult.runtime}<span>{msg.detail.bashResult.runtime}</span>{/if}
                        {#if msg.detail.bashResult.cwd}<span>{msg.detail.bashResult.cwd}</span>{/if}
                        {#if msg.detail.bashResult.exitCode}
                          <strong class:failed={msg.detail.bashResult.exitCode !== '0'}>exit {msg.detail.bashResult.exitCode}</strong>
                        {/if}
                      </div>
                      {#if msg.detail.bashResult.prefix}
                        <p class="bash-note">{msg.detail.bashResult.prefix}</p>
                      {/if}
                      {#if msg.detail.bashResult.command}
                        <div class="bash-block">
                          <span>command</span>
                          <pre>{msg.detail.bashResult.command}</pre>
                        </div>
                      {/if}
                      {#if msg.detail.bashResult.stdout}
                        <div class="bash-block">
                          <span>stdout</span>
                          <pre class:empty={msg.detail.bashResult.stdout === '(no output)'}>{msg.detail.bashResult.stdout}</pre>
                        </div>
                      {/if}
                      {#if msg.detail.bashResult.stderr}
                        <div class="bash-block">
                          <span>stderr</span>
                          <pre class:empty={msg.detail.bashResult.stderr === '(no output)'}>{msg.detail.bashResult.stderr}</pre>
                        </div>
                      {/if}
                      {#if msg.detail.bashResult.note}
                        <p class="bash-note">{msg.detail.bashResult.note}</p>
                      {/if}
                    </div>
                  {:else if msg.detail?.kind === 'read' && msg.detail.readLines?.length}
                    <div class="read-result">
                      {#each msg.detail.readLines as line}
                        <div class="code-line">
                          <span>{line.number}</span>
                          <code>{line.text}</code>
                        </div>
                      {/each}
                    </div>
                  {:else if msg.detail?.kind === 'ls' && msg.detail.lsEntries?.length}
                    <div class="ls-result">
                      {#each msg.detail.lsEntries as entry}
                        <div class="ls-entry {entry.type}">
                          <span>{entry.type === 'dir' ? 'dir' : 'file'}</span>
                          <strong>{entry.name}</strong>
                          {#if entry.size}<em>{entry.size}</em>{/if}
                        </div>
                      {/each}
                    </div>
                  {:else if msg.detail?.kind === 'grep' && msg.detail.grepMatches?.matches?.length}
                    <div class="grep-result">
                      {#each msg.detail.grepMatches.matches as match}
                        <div class="grep-match">
                          <div><strong>{match.path}</strong><span>:{match.line}</span></div>
                          <code>{match.text}</code>
                        </div>
                      {/each}
                      {#if msg.detail.grepMatches.note}
                        <p>{msg.detail.grepMatches.note}</p>
                      {/if}
                    </div>
                  {:else if msg.detail?.content}
                    <pre>{msg.detail.content}</pre>
                  {/if}
                  {#if msg.detail?.images?.length}
                    <div class="msg-images">
                      {#each msg.detail.images as image}
                        <img src={image.dataUrl} alt={image.name} on:load={() => scrollChatToBottom()} />
                      {/each}
                    </div>
                  {/if}
                {/if}
              </details>
            </article>
          {/if}
        {/each}
        {#if recentTools.length > 0}
          <aside class="tool-feed">
            <div class="tf-head"><span>{$t('chat.toolEvents')}</span><strong>{chatEvents.length}</strong></div>
            {#each recentTools as item}
              <details class="tool-item" open={item.status === 'running'}>
                <summary>
                  <span class="dot {toolStateClass(item)}"></span>
                  <strong>{item.tool || item.type}</strong>
                  <em>{item.status || 'event'}</em>
                </summary>
                {#if item.args}
                  <pre>{formatArgs(item.args)}</pre>
                {:else if item.raw}
                  <pre>{item.raw}</pre>
                {/if}
              </details>
            {/each}
          </aside>
        {/if}
        {#if sessionEventSummary.visible}
          <aside class="session-event-strip" title={sessionEventTooltip(sessionEventSummary)}>
            <span class="dot {sessionRunStateClass(sessionEventSummary.lastRun)}"></span>
            <strong>{sessionRunLabel(sessionEventSummary.lastRun)}</strong>
            {#if sessionEventSummary.workDir}<span class="path">{compactPath(sessionEventSummary.workDir)}</span>{/if}
            {#if sessionEventSummary.model}<span>{sessionEventSummary.model}</span>{/if}
            <span class="metric">{$t('chat.sessionEvents.tokens', { tokens: formatCompactTokens(sessionEventSummary.totalTokens) })}</span>
            <span class="metric">{$t('chat.sessionEvents.cache', { rate: formatCacheRate(sessionEventSummary) })}</span>
            {#if sessionEventSummary.capabilityCount > 0}
              <span>{$t('chat.sessionEvents.capabilities', { count: sessionEventSummary.capabilityCount })}</span>
            {/if}
          </aside>
        {/if}
      </div>
    {/if}
  </div>

  <div class="composer">
    <div class="composer-card">
      <div class="composer-row">
      {#if imageUploads.length > 0}
        <div class="image-preview-row">
          {#each imageUploads as image, idx}
            <div class="image-preview">
              <img src={image.dataUrl} alt={image.name} />
              <span title={image.name}>{image.name}</span>
              <em>{formatImageSize(image.size)}</em>
              <button type="button" aria-label={$t('chat.removeImage')} on:click={() => removeImage(idx)}>×</button>
            </div>
          {/each}
        </div>
      {/if}
      <textarea
        bind:value={prompt}
        on:keydown={handleKeydown}
        placeholder={!apiEnabled ? $t('chat.apiDisabled') : (isNewSession && !workDir.trim()) ? $t('chat.error.needWorkDir') : $t('chat.messagePlaceholder')}
        disabled={!apiEnabled}
        rows="1"
      ></textarea>
    </div>
    <div class="composer-bar">
      <div class="left">
        <input
          bind:this={imageInput}
          class="file-input"
          type="file"
          accept="image/png,image/jpeg,image/gif,image/webp"
          multiple
          on:change={handleImageSelect}
        />
        {#if selectedModelSupportsImages}
          <button
            type="button"
            class="icon-btn"
            disabled={!apiEnabled || busy}
            title={$t('chat.uploadImage')}
            aria-label={$t('chat.uploadImage')}
            on:click={() => imageInput?.click()}
          >
            📎
          </button>
        {/if}
        <div bind:this={modelPicker} class="model-picker" aria-label={$t('chat.selectModel')}>
          <button
            type="button"
            class="model-picker-toggle"
            class:open={showModelPicker}
            disabled={!apiEnabled || modelOptions.length === 0}
            aria-expanded={showModelPicker}
            on:click={() => (showModelPicker = !showModelPicker)}
          >
            <span>{currentModelLabel}</span>
            <span class="model-picker-chevron" aria-hidden="true">⌄</span>
          </button>
          {#if showModelPicker}
            <div class="model-picker-menu" role="listbox">
              {#each modelOptions as m}
                <button
                  type="button"
                  class:active={$selectedModel === m.id}
                  role="option"
                  aria-selected={$selectedModel === m.id}
                  on:click={() => selectModel(m.id)}
                >{m.id}</button>
              {/each}
            </div>
          {/if}
        </div>
        <div bind:this={runtimeControls} class="runtime-controls" aria-label="Session runtime controls">
          <button
            type="button"
            class:open={showRuntimePanel}
            class="runtime-toggle"
            aria-expanded={showRuntimePanel}
            aria-controls="session-runtime-panel"
            on:click={() => (showRuntimePanel = !showRuntimePanel)}
          >
            <span class="runtime-label">Mode</span>
            <strong>{runtimeMode}</strong>
            <span class="runtime-chevron" aria-hidden="true">⌄</span>
            {#if pendingApprovalCount}<span class="runtime-badge">{pendingApprovalCount}</span>{/if}
          </button>
          {#if showRuntimePanel}
            <section id="session-runtime-panel" class="runtime-panel">
              <header>
                <strong>Session runtime</strong>
                {#if runtimeActiveRun}<span class="dot running"></span><span>{runtimeActiveRun.status}</span>{/if}
              </header>
              <p class="runtime-hint">plan is read-only planning, agent requests approval for guarded actions, and yolo runs automatically.</p>
              <div class="mode-switcher" role="group" aria-label="Agent mode">
                {#each ['plan', 'agent', 'yolo'] as mode}
                  <button type="button" class:active={runtimeMode === mode} disabled={runtimeUpdating || busy} on:click={() => setMode(mode)}>{mode}</button>
                {/each}
              </div>
              {#if pendingApprovalCount}
                <div class="approval-summary"><strong>{pendingApprovalCount} pending approval{pendingApprovalCount === 1 ? '' : 's'}</strong><button type="button" class="ghost sm" on:click={() => (showApprovalCenter = true)}>Review approvals</button></div>
              {/if}
            </section>
          {/if}
        </div>
        <div bind:this={skillPicker} class="skill-picker" aria-label="Active skills">
          <button type="button" class="skill-picker-toggle" disabled={!apiEnabled || busy} on:click={() => (showSkillPicker = !showSkillPicker)} aria-expanded={showSkillPicker}>
            <span>Skills</span>
            <strong>{activeSkills.length ? `${activeSkills.length} active` : 'none active'}</strong>
            <span class="runtime-chevron">⌄</span>
          </button>
          {#if showSkillPicker}
            <div class="skill-picker-menu">
              <header><strong>Project skills</strong><span>{activeSkills.length} active · {availableSkills.length - activeSkills.length} pending</span></header>
              {#if availableSkills.length === 0}
                <p class="skill-picker-empty">No skills found in this project.</p>
              {:else}
                {#each availableSkills as skill}
                  <label class:active={activeSkills.includes(skill.name)}>
                    <input type="checkbox" checked={activeSkills.includes(skill.name)} disabled={busy} on:change={(event) => toggleSkill(skill.name, event)} />
                    <span class="skill-name">{skill.name}</span>
                    <em>{activeSkills.includes(skill.name) ? 'active' : 'pending'}</em>
                  </label>
                {/each}
              {/if}
            </div>
          {/if}
        </div>
        <div bind:this={toolMenuBtn} class="tool-menu" aria-label={$t('chat.tools')}>
          <button
            type="button"
            class="tool-menu-toggle"
            class:open={showToolMenu}
            disabled={!apiEnabled || busy}
            on:click={() => (showToolMenu = !showToolMenu)}
            aria-expanded={showToolMenu}
          >
            <span class="tool-menu-label">Tools</span>
            <strong>{activeToolCount}</strong>
            <span class="runtime-chevron">⌄</span>
          </button>
          {#if showToolMenu}
            <div class="tool-menu-popover">
              <header><strong>{$t('chat.tools')}</strong><span>{$t('chat.toolHint')}</span></header>
              {#each availableToolToggles as item}
                <label class="tool-menu-item" class:active={sessionTools[item.key]} title={$t(`chat.toolToggle.${item.key}`)}>
                  <input
                    type="checkbox"
                    checked={sessionTools[item.key]}
                    disabled={!apiEnabled || busy}
                    on:change={(event) => { updateToolOption(item.key, event); }}
                  />
                  <span class="tool-item-name">{item.label}</span>
                  <em>{sessionTools[item.key] ? 'on' : 'off'}</em>
                </label>
              {/each}
            </div>
          {/if}
        </div>
        {#if isNewSession}
          <button
            type="button"
            class="workdir-pill"
            class:has-dir={Boolean(workDir.trim())}
            disabled={!apiEnabled || busy}
            on:click={chooseWorkDir}
            title={workDir || $t('chat.selectWorkDir')}
          >
            <span class="workdir-icon">📁</span>
            <span class="workdir-text">{workDir ? compactWorkDir(workDir) : $t('chat.selectWorkDir')}</span>
          </button>
        {/if}
      </div>
      <div class="right">
        {#if busy}
          <button type="button" class="stop-btn" disabled={stopSubmitting} on:click={stop} title={stopSubmitting ? 'Stopping…' : $t('common.stop')} aria-label={stopSubmitting ? 'Stopping…' : $t('common.stop')}>
            <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor"><rect x="6" y="6" width="12" height="12" rx="2"/></svg>
          </button>
        {/if}
        <button
          type="button"
          class="send-btn primary"
          disabled={busy || (!prompt.trim() && imageUploads.length === 0) || !apiEnabled || (isNewSession && !workDir.trim())}
          on:click={sendPrompt}
          title={busy ? $t('chat.sending') : $t('chat.send')}
          aria-label={busy ? $t('chat.sending') : $t('chat.send')}
        >
          {#if busy}
            <span class="spinner sm"></span>
          {:else}
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><line x1="12" y1="19" x2="12" y2="5"/><polyline points="5 12 12 5 19 12"/></svg>
          {/if}
        </button>
      </div>
      </div>
    </div>
    {#if $currentSession}
      <div class="composer-session-info">
        <span class="session-badge">{$t('chat.session')}</span>
        <span class="session-id">{shortID($currentSession)}</span>
        {#if activeSessionWorkDir}<span class="session-dir">{activeSessionWorkDir}</span>{/if}
        <button type="button" class="ghost sm" on:click={resetSession}>{$t('chat.newSession')}</button>
      </div>
    {/if}
  </div>
</section>


{#if showApprovalCenter}
  <div class="subagent-overlay" role="dialog" aria-modal="true" aria-label="Approval center">
    <div class="subagent-modal approval-center">
      <header>
        <div>
          <strong>Approval center</strong>
          <span>{pendingApprovalCount} pending · {approvalHistory.length} recorded for this session</span>
        </div>
        <button type="button" class="ghost sm" on:click={() => (showApprovalCenter = false)}>Close</button>
      </header>
      <div class="approval-list" aria-live="polite">
        {#if selectedApproval}
          <article class="approval-card" aria-labelledby="approval-title-{selectedApproval.approvalId}">
            <div class="approval-card-head">
              <div class="approval-title-group">
                <div class="approval-kicker"><span class="approval-risk {selectedApproval.risk || 'medium'}">{selectedApproval.risk || 'medium'} risk</span><span>{selectedApproval.mode || runtimeMode} mode</span></div>
                <strong id="approval-title-{selectedApproval.approvalId}">{selectedApproval.summary || selectedApproval.tool?.name}</strong>
                <p>{selectedApproval.reason || 'This action requires confirmation.'}</p>
              </div>
              {#if pendingApprovalCount > 1}
                <label class="approval-picker">Request
                  <select aria-label="Select pending approval" value={selectedApprovalID} on:change={(event) => { selectedApprovalID = event.currentTarget.value; activeApproval.set((sessionRuntimeValue?.pendingApprovals || []).find((approval) => approval.approvalId === selectedApprovalID) || null); }}>
                    {#each sessionRuntimeValue?.pendingApprovals || [] as approval}<option value={approval.approvalId}>{approval.summary || approval.tool?.name}</option>{/each}
                  </select>
                </label>
              {/if}
            </div>
            {#if selectedApproval.tool?.name === 'bash'}
              <div class="approval-bash tool-call-body embedded">
                <div class="tool-title">
                  <span class="dot running"></span>
                  <strong>Bash</strong>
                  {#if approvalBashWorkDir(selectedApproval)}<span class="tool-target">{approvalBashWorkDir(selectedApproval)}</span>{/if}
                </div>
                <div class="bash-block">
                  <span>command</span>
                  <pre>{approvalBashCommand(selectedApproval)}</pre>
                </div>
              </div>
            {:else}
              <div class="approval-tool tool-call-body embedded">
                <div class="tool-title">
                  <span class="dot running"></span>
                  <strong>{approvalToolViewValue.label || selectedApproval.tool?.label || selectedApproval.tool?.name}</strong>
                  {#if approvalToolViewValue.target}<span class="tool-target">{approvalToolViewValue.target}</span>{/if}
                </div>
                {#if approvalToolViewValue.details?.length}
                  <div class="tool-call-tags">
                    {#each approvalToolViewValue.details as detail}<span>{detail}</span>{/each}
                  </div>
                {/if}
                {#if approvalToolViewValue.kind === 'edit' && approvalToolViewValue.edits?.length}
                  <div class="edit-call">
                    {#each approvalToolViewValue.edits as edit}
                      <section class="edit-block">
                        <div class="edit-block-head"><strong>{$t('chat.tool.edit.editNumber', { number: edit.index })}</strong><span>{$t('chat.tool.edit.lineChange', { old: edit.oldLines, next: edit.newLines })}</span></div>
                        <div class="edit-columns"><div class="edit-pane old"><span>{$t('chat.tool.edit.oldText')}</span><pre class:empty={edit.oldText === ''}><code>{@html edit.oldText ? highlightedCodeToHTML(edit.oldText, approvalToolViewValue.target) : $t('chat.tool.edit.empty')}</code></pre></div><div class="edit-pane new"><span>{$t('chat.tool.edit.newText')}</span><pre class:empty={edit.newText === ''}><code>{@html edit.newText ? highlightedCodeToHTML(edit.newText, approvalToolViewValue.target) : $t('chat.tool.edit.empty')}</code></pre></div></div>
                      </section>
                    {/each}
                  </div>
                {:else if approvalToolViewValue.kind === 'write'}
                  <div class="write-call"><div class="write-call-head"><strong>{$t('chat.tool.write.preview')}</strong><span>{$t('chat.tool.write.summary', { lines: approvalToolViewValue.lines, chars: approvalToolViewValue.chars })}</span></div><span>{$t('chat.tool.write.content')}</span><pre class:empty={approvalToolViewValue.content === ''}>{approvalToolViewValue.content || $t('chat.tool.edit.empty')}</pre></div>
                {:else if approvalToolViewValue.kind === 'insert'}
                  <div class="write-call"><div class="write-call-head"><strong>{$t('chat.tool.insert.preview')}</strong><span>{$t('chat.tool.insert.summary', { lines: approvalToolViewValue.lines, chars: approvalToolViewValue.chars })}</span></div><span>{$t('chat.tool.insert.content')}</span><pre class:empty={approvalToolViewValue.content === ''}>{approvalToolViewValue.content || $t('chat.tool.edit.empty')}</pre></div>
                {:else if approvalToolViewValue.kind === 'find'}
                  <div class="find-call"><div class="find-row"><span>{$t('chat.tool.find.pattern')}</span><code>{approvalToolViewValue.pattern || $t('chat.tool.find.missing')}</code></div><div class="find-row"><span>{$t('chat.tool.find.searchPath')}</span><code>{approvalToolViewValue.path}</code></div></div>
                {:else if approvalToolViewValue.kind === 'browser'}
                  <div class="browser-call"><div class="find-row"><span>{$t('chat.tool.browser.action')}</span><code>{approvalToolViewValue.action || $t('chat.tool.browser.missing')}</code></div>{#if approvalToolViewValue.url}<div class="find-row"><span>{$t('chat.tool.browser.url')}</span><code>{approvalToolViewValue.url}</code></div>{/if}{#if approvalToolViewValue.selector}<div class="find-row"><span>{$t('chat.tool.browser.selectorLabel')}</span><code>{approvalToolViewValue.selector}</code></div>{/if}</div>
                {:else if approvalToolViewValue.kind === 'skill-ref'}
                  <div class="skill-ref-call"><div class="find-row"><span>{$t('chat.tool.skillRef.skillLabel')}</span><code>{approvalToolViewValue.skill || $t('chat.tool.skillRef.missing')}</code></div><div class="find-row"><span>{$t('chat.tool.skillRef.refLabel')}</span><code>{approvalToolViewValue.ref || $t('chat.tool.skillRef.missing')}</code></div></div>
                {:else}
                  <div class="approval-tool-summary"><span>{selectedApproval.tool?.details?.path || selectedApproval.context?.workDir || 'This action requires permission.'}</span></div>
                {/if}
              </div>
            {/if}
            <div class="approval-actions">
              <button class="primary" disabled={approvalSubmitting} on:click={() => respondApproval(selectedApproval, 'approve_once')}>Approve once</button>
              <button class="ghost approval-deny" disabled={approvalSubmitting} on:click={() => respondApproval(selectedApproval, 'deny_once')}>Deny</button>
              {#if selectedApproval.actions?.includes('remember_command')}<span class="approval-action-divider"></span><button class="ghost sm" disabled={approvalSubmitting} on:click={() => respondApproval(selectedApproval, 'remember_command')}>Always allow command</button><button class="ghost sm" disabled={approvalSubmitting} on:click={() => respondApproval(selectedApproval, 'remember_prefix')}>Always allow prefix</button>{/if}
              {#if selectedApproval.actions?.includes('allow_edit_path')}<button class="ghost sm" disabled={approvalSubmitting} on:click={() => respondApproval(selectedApproval, 'allow_edit_path')}>Allow this path</button>{/if}
            </div>
            <details class="approval-raw"><summary>Request JSON</summary><pre>{JSON.stringify(selectedApproval, null, 2)}</pre></details>
          </article>
        {:else}
          <div class="approval-empty"><strong>No pending approvals</strong><span>New approval requests will appear here.</span></div>
        {/if}
        {#if approvalHistory.length}
          <section class="approval-history" aria-label="Session approval history">
            <div class="approval-history-head"><h4>Session audit history</h4><span>{approvalHistory.length} decisions</span></div>
            <div class="approval-history-list">
              {#each approvalHistory as item}
                <article class="approval-history-item">
                  <strong>{item.action === 'deny_once' ? 'Denied' : 'Approved'}</strong>
                  <span>{item.message || item.action}</span>
                </article>
              {/each}
            </div>
          </section>
        {/if}
      </div>
    </div>
  </div>
{/if}

<DirBrowser bind:open={showBrowser} on:select={onDirSelect} on:close={() => (showBrowser = false)} />

{#if showSubAgentModal}
  <div class="subagent-overlay" role="dialog" aria-modal="true" aria-label={$t('chat.subagents.history')}>
    <div class="subagent-modal">
      <header>
        <div>
          <strong>{$t('chat.subagents.history')}</strong>
          <span>{$t('chat.subagents.subtitle', { count: subAgents.length })}</span>
        </div>
        <button type="button" class="ghost sm" on:click={closeSubAgentModal}>{$t('common.close')}</button>
      </header>
      <div class="subagent-modal-body">
        <aside class="subagent-list">
          {#each subAgents as agent}
            <button
              type="button"
              class:active={agent.id === selectedSubAgentID}
              on:click={() => selectSubAgent(agent.id)}
            >
              <span class="dot {subAgentStateClass(agent)}"></span>
              <strong>{shortID(agent.id)}</strong>
              <em>{subAgentStatusLabel(agent.status)}</em>
              {#if agent.messageCount}<small>{agent.messageCount}</small>{/if}
            </button>
          {/each}
        </aside>
        <section class="subagent-history">
          {#if subAgentModalLoading}
            <p class="pending-text">{$t('chat.subagents.loading')}</p>
          {:else if subAgentModalError}
            <p class="error-text">{subAgentModalError}</p>
          {:else if subAgentModalMessages.length === 0}
            <p class="pending-text">{$t('chat.subagents.empty')}</p>
          {:else}
            {#each subAgentModalMessages as item}
              <article class="subagent-msg {item.role}">
                <div class="meta">
                  <strong>{item.role === 'assistant' ? 'assistant' : item.role}</strong>
                  {#if item.toolName}<span>{item.toolName}</span>{/if}
                </div>
                {#if item.role === 'assistant'}
                  <div class="markdown" use:codeBlockControls>{@html markdownToHTML(item.content || '')}</div>
                {:else if item.role === 'user'}
                  <p>{item.content}</p>
                {:else if item.role === 'toolCall'}
                  <div class="tool-call-body embedded">
                    <div class="tool-title">
                      <span class="dot running"></span>
                      <strong>{item.callView?.label || item.toolName}</strong>
                      {#if item.callView?.target}<span class="tool-target">{item.callView.target}</span>{/if}
                    </div>
                    {#if item.callView?.details?.length}
                      <div class="tool-call-tags">
                        {#each item.callView.details as detail}
                          <span>{detail}</span>
                        {/each}
                      </div>
                    {/if}
                    {#if item.callView?.kind === 'browser'}
                      <div class="browser-call">
                        <div class="find-row">
                          <span>{$t('chat.tool.browser.action')}</span>
                          <code>{item.callView.action || $t('chat.tool.browser.missing')}</code>
                        </div>
                        {#if item.callView.url}
                          <div class="find-row">
                            <span>{$t('chat.tool.browser.url')}</span>
                            <code>{item.callView.url}</code>
                          </div>
                        {/if}
                        {#if item.callView.selector}
                          <div class="find-row">
                            <span>{$t('chat.tool.browser.selectorLabel')}</span>
                            <code>{item.callView.selector}</code>
                          </div>
                        {/if}
                      </div>
                    {:else if item.callView?.kind === 'skill-ref'}
                      <div class="skill-ref-call">
                        <div class="find-row">
                          <span>{$t('chat.tool.skillRef.skillLabel')}</span>
                          <code>{item.callView.skill || $t('chat.tool.skillRef.missing')}</code>
                        </div>
                        <div class="find-row">
                          <span>{$t('chat.tool.skillRef.refLabel')}</span>
                          <code>{item.callView.ref || $t('chat.tool.skillRef.missing')}</code>
                        </div>
                      </div>
                    {:else if item.callView?.kind === 'workflow-lint'}
                      <div class="workflow-lint-call">
                        <div class="write-call-head">
                          <strong>{$t('chat.tool.workflowLint.source')}</strong>
                          <span>{$t('chat.tool.write.summary', { lines: item.callView.lines, chars: item.callView.chars })}</span>
                        </div>
                        <pre class:empty={item.callView.source === ''}>{item.callView.source || $t('chat.tool.workflowLint.missing')}</pre>
                      </div>
                    {:else if item.callView?.kind === 'subagent-task'}
                      <div class="subagent-call">
                        <span>{$t('chat.tool.subagent.task')}</span>
                        <p>{item.callView.task || item.callView.target}</p>
                      </div>
                    {:else if item.callView?.kind === 'subagent-handle'}
                      <div class="subagent-call compact">
                        <div class="find-row">
                          <span>{$t('chat.tool.subagent.handle')}</span>
                          <code>{item.callView.handle || $t('chat.tool.subagent.handleMissing')}</code>
                        </div>
                      </div>
                    {/if}
                  </div>
                {:else if item.role === 'toolResult'}
                  <div class="tool-mini">
                    <span class="dot {item.isError ? 'error' : 'done'}"></span>
                    <strong>{item.toolName}</strong>
                    <span>{item.summary}</span>
                  </div>
                {:else if item.role === 'status'}
                  <div class="tool-mini">
                    <span class="dot {item.isError ? 'error' : 'done'}"></span>
                    <strong>{subAgentStatusLabel(item.content)}</strong>
                    {#if item.summary}<span>{item.summary}</span>{/if}
                  </div>
                {:else}
                  <pre>{item.content || formatArgs(item.arguments)}</pre>
                {/if}
              </article>
            {/each}
          {/if}
        </section>
      </div>
    </div>
  </div>
{/if}
