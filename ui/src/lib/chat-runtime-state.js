// Session runtime state manager for the Chat view.
//
// Owns the runtime snapshot lifecycle: loading (GET /runtime), PATCH updates
// with optimistic apply + rollback, mode/display-mode switching, responses-run
// polling/reconnect, and capability -> session-tool synchronization. All
// server actions, the runtime store, and mirror-sync callbacks are injected,
// so the manager is pure JS and unit-testable without Svelte.
//
// Chat.svelte stays a thin adapter: it injects real store/API functions and
// renders the state the manager publishes through onSnapshot.

const RESPONSES_POLL_INTERVAL_MS = 1000;

// Snapshot display-mode normalization: the UI collapses everything but 'code'
// into 'work' (the expanded/runtime panel state is kept in the view).
export function displayModeFromSnapshot(snapshot) {
  return snapshot?.displayMode === 'code' ? 'code' : 'work';
}

// Boolean { key: enabled } map directly usable by the session tool options.
export function enabledToolsFromCapabilities(snapshot) {
  return Object.fromEntries(
    Object.entries(snapshot?.capabilities || {}).map(([key, state]) => [key, Boolean(state?.enabled)])
  );
}

/**
 * @param {object} deps Injected dependencies:
 *   store: { get(): snapshot|null, set(snapshot): void }  — sessionRuntime store adapter.
 *   currentSession(): string  — selected session id ('' when composing a new session).
 *   runLifecycle(): number    — monotonic run lifecycle version owned by the view.
 *   isBusy(): boolean         — completion/runtime-busy state from the view.
 *   getSessionRuntime(id), patchSessionRuntime(id, patch),
 *   getResponsesRun(sessionID, localRunID), reconnectResponsesRun(sessionID, localRunID)
 *   getSessionTools(): object, setSessionTools(id, tools): void
 *   upsertSession(patch), refreshSessions(): Promise
 *   isActiveRunStatus(status): boolean
 *   onError(err), onSnapshot(next), onDisplayMode(mode),
 *   onNewSessionMode(mode), onPersist(id), onUpdatingChange(flag)
 */
export function createSessionRuntimeManager(deps) {
  // Guards against stale GETs: every PATCH bumps this; an in-flight load that
  // started before the mutation must not overwrite the authoritative response.
  let mutationVersion = 0;
  let updating = false;
  let pollTimer = 0;
  let pollReconnectKey = '';

  const currentSnapshot = () => deps.store.get();

  function publish(next, persistSessionID) {
    deps.store.set(next);
    deps.onSnapshot(next);
    if (persistSessionID) deps.onPersist(persistSessionID);
  }

  function syncToolsFromCapabilities(id, snapshot) {
    const enabledTools = enabledToolsFromCapabilities(snapshot);
    deps.setSessionTools(id, { ...deps.getSessionTools(), ...enabledTools });
  }

  function stillCurrent(id, snapshotMutationVersion, lifecycleVersion) {
    return id !== '' && id === deps.currentSession()
      && snapshotMutationVersion === mutationVersion
      && lifecycleVersion === deps.runLifecycle();
  }

  return {
    get updating() {
      return updating;
    },

    // Load the persisted runtime snapshot for `id`. The response only lands if
    // the session, the mutation counter, and the run lifecycle are unchanged
    // since the request started; otherwise it is dropped as stale.
    async load(id, expectedRunLifecycleVersion = null) {
      if (!id) {
        publish(null);
        return;
      }
      const snapshotMutationVersion = mutationVersion;
      const requestRunLifecycleVersion = expectedRunLifecycleVersion == null
        ? deps.runLifecycle()
        : expectedRunLifecycleVersion;
      try {
        const snapshot = await deps.getSessionRuntime(id);
        if (!stillCurrent(id, snapshotMutationVersion, requestRunLifecycleVersion)) return;
        publish(snapshot);
        syncToolsFromCapabilities(id, snapshot);
      } catch (err) {
        if (stillCurrent(id, snapshotMutationVersion, requestRunLifecycleVersion)) deps.onError(err);
      }
    },

    // PATCH the runtime with optimistic reflect + rollback on failure.
    // Incrementing mutationVersion invalidates any older in-flight session-load
    // GET so a stale snapshot can never restore the previous mode.
    async update(patch) {
      const id = deps.currentSession();
      if (!id || updating) return;
      const previous = currentSnapshot();
      const snapshotMutationVersion = ++mutationVersion;
      updating = true;
      deps.onUpdatingChange(true);

      const optimistic = {
        ...(previous || { sessionId: id }),
        ...(patch.mode ? { mode: patch.mode } : {}),
        ...(patch.displayMode ? { displayMode: patch.displayMode } : {})
      };
      publish(optimistic, id);

      try {
        const snapshot = await deps.patchSessionRuntime(id, patch);
        if (stillCurrent(id, snapshotMutationVersion, deps.runLifecycle())) {
          publish(snapshot, id);
          syncToolsFromCapabilities(id, snapshot);
        }
        // The PATCH response is authoritative; the mode controls must not stay
        // disabled while the independent session-list refresh waits for
        // first-start initialization endpoints.
        deps.upsertSession({ id, mode: snapshot?.mode });
        void deps.refreshSessions().catch((refreshErr) => {
          console.warn('Failed to refresh sessions after runtime update:', refreshErr);
        });
      } catch (err) {
        if (stillCurrent(id, snapshotMutationVersion, deps.runLifecycle())) {
          publish(previous, id);
          deps.onError(err);
        }
      } finally {
        updating = false;
        deps.onUpdatingChange(false);
      }
    },

    async setMode(mode) {
      if (deps.isBusy() || updating) return;
      if (!deps.currentSession()) {
        deps.onNewSessionMode(mode);
        return;
      }
      await this.update({ mode });
    },

    async setDisplayMode(displayMode) {
      if (deps.isBusy() || updating) return;
      const next = displayMode === 'code' ? 'code' : 'work';
      // Reflect immediately so the control stays responsive while the PATCH is
      // in flight; the authoritative response re-derives it on success, and a
      // failure rolls it back to the previous snapshot's value.
      deps.onDisplayMode(next);
      if (!deps.currentSession()) {
        deps.onPersist('__new__');
        return;
      }
      await this.update({ displayMode: next });
    },

    // Poll the durable responses-run row while it is active. Reconnects once
    // per (session, localRunID) pair; stops when the run leaves active status
    // or the session/lifecycle changes (at which point a fresh snapshot load
    // reconciles the final invocation state).
    startResponsesRunPolling(sessionID, localRunID) {
      if (!sessionID || !localRunID || pollTimer) return;
      const pollingLifecycle = deps.runLifecycle();
      const reconnectKey = `${sessionID}:${localRunID}`;
      if (pollReconnectKey !== reconnectKey) {
        pollReconnectKey = reconnectKey;
        deps.reconnectResponsesRun(sessionID, localRunID)
          .then((result) => {
            const run = result?.run;
            if (sessionID !== deps.currentSession()
              || pollingLifecycle !== deps.runLifecycle()
              || !run
              || run.localRunId !== localRunID) return;
            publish({
              ...currentSnapshot(),
              responsesRun: {
                ...currentSnapshot()?.responsesRun,
                localRunId: run.localRunId,
                responseId: run.responseId,
                state: run.state,
                cancelRequested: run.cancelRequested
              }
            }, sessionID);
          })
          .catch(() => {
            // The polling path still reports remote state; reconnect can fail
            // while another coordinator owns the session runtime lock.
          });
      }
      const poll = async () => {
        if (sessionID !== deps.currentSession() || pollingLifecycle !== deps.runLifecycle()) {
          this.stopResponsesRunPolling();
          return;
        }
        const currentResponseRun = currentSnapshot()?.responsesRun;
        if (!currentResponseRun || currentResponseRun.localRunId !== localRunID) {
          this.stopResponsesRunPolling();
          return;
        }
        try {
          const run = await deps.getResponsesRun(sessionID, localRunID);
          if (sessionID !== deps.currentSession() || pollingLifecycle !== deps.runLifecycle()) return;
          if (run && run.localRunId === localRunID) {
            publish({
              ...currentSnapshot(),
              responsesRun: {
                ...currentSnapshot()?.responsesRun,
                localRunId: run.localRunId,
                responseId: run.responseId,
                state: run.state,
                cancelRequested: run.cancelRequested
              }
            }, sessionID);
          }
          if (!run || !deps.isActiveRunStatus(run.state)) {
            this.stopResponsesRunPolling();
            this.load(sessionID, pollingLifecycle);
          }
        } catch {
          // Keep the last durable state visible; the next interval retries.
        }
      };
      poll();
      pollTimer = setInterval(poll, RESPONSES_POLL_INTERVAL_MS);
    },

    stopResponsesRunPolling() {
      if (!pollTimer) return;
      clearInterval(pollTimer);
      pollTimer = 0;
    },

    // Component teardown: stop timers; nothing else is owned here.
    destroy() {
      this.stopResponsesRunPolling();
    }
  };
}