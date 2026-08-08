package channels

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/startvibecoding/mothx/internal/agent"
	"github.com/startvibecoding/mothx/internal/session"
)

// watchdogTick is how often the dispatcher scans channel runs for stalls.
const watchdogTick = 15 * time.Second

// startWatchdog launches the run watchdog. It stops when the dispatcher run
// root context is canceled (Dispatcher.Close).
func (d *Dispatcher) startWatchdog() {
	if d == nil || d.runRootCtx == nil {
		return
	}
	go d.runWatchdog()
}

func (d *Dispatcher) runWatchdog() {
	ticker := time.NewTicker(watchdogTick)
	defer ticker.Stop()
	for {
		select {
		case <-d.runRootCtx.Done():
			return
		case now := <-ticker.C:
			d.checkStalledRuns(now)
		}
	}
}

// checkStalledRuns force-stops channel runs that stopped making progress.
// A run is stalled when no agent event arrived within the configured stale
// timeout, or when it exceeds the configured total duration cap. Force-stopping
// aborts the agent (unblocking waits that ignore context cancellation), cancels
// the run context and marks the persisted run as cancelling so /new, /status
// and the WebUI converge on reality instead of reporting a phantom active run.
func (d *Dispatcher) checkStalledRuns(now time.Time) {
	runtime := d.runtimeSnapshot()
	cfg := runtime.cfg
	if cfg == nil {
		cfg = DefaultConfig()
	}
	stale := cfg.Agent.GetRunStaleTimeout()
	maxDuration := cfg.Agent.GetRunMaxDuration()
	if stale <= 0 && maxDuration <= 0 {
		return
	}

	d.mu.RLock()
	sessions := make([]*ChannelSession, 0, len(d.sessions))
	for _, sess := range d.sessions {
		sessions = append(sessions, sess)
	}
	d.mu.RUnlock()

	activeRuns := make(map[string]struct{})
	for _, sess := range sessions {
		if sess == nil {
			continue
		}
		sess.runStateMu.Lock()
		runID := sess.runID
		cancel := sess.runCancel
		runningAgent := sess.runAgent
		startedAt := sess.runStartedAt
		lastEventAt := sess.lastEventAt
		sess.runStateMu.Unlock()
		if runID == "" {
			continue
		}
		activeRuns[runID] = struct{}{}

		var reason string
		switch {
		case maxDuration > 0 && !startedAt.IsZero() && now.Sub(startedAt) > maxDuration:
			reason = fmt.Sprintf("run exceeded max duration %s", maxDuration.Round(time.Second))
		case stale > 0 && !lastEventAt.IsZero() && now.Sub(lastEventAt) > stale:
			reason = fmt.Sprintf("no agent events for %s", stale.Round(time.Second))
		}
		if reason == "" {
			continue
		}
		if d.watchdogAlreadyFired(runID) {
			continue
		}
		d.forceStopRun(sess, runID, reason, cancel, runningAgent)
	}
	d.pruneWatchdogFired(activeRuns)
}

func (d *Dispatcher) forceStopRun(sess *ChannelSession, runID, reason string, cancel func(), runningAgent *agent.Agent) {
	sessionID := sess.ID
	log.Printf("[channels] watchdog forcing stop of run %s (session %s): %s", runID, sessionID, reason)
	if runningAgent != nil {
		runningAgent.Abort()
	}
	if cancel != nil {
		cancel()
	}
	message := "watchdog: " + reason
	if err := session.UpdateSessionRunStatus(d.sessionDir, runID, "cancelling", message, nil); err != nil {
		log.Printf("[channels] watchdog update run %s: %v", runID, err)
	}
	eventData, _ := json.Marshal(map[string]string{"error": message})
	if _, err := session.SaveSessionRunEvent(d.sessionDir, session.SessionRunEvent{
		SessionID: sessionID, RunID: runID, EventType: "canceled",
		Source: "channel:watchdog", Status: "cancelling", Data: eventData,
	}); err != nil {
		log.Printf("[channels] watchdog save run event %s: %v", runID, err)
	}
	d.notifyRunObserver(sessionID)
}

// watchdogAlreadyFired records that a run was already force-stopped so a run
// that ignores abort does not get spammed with repeated stop requests. Entries
// are pruned once the run leaves the active set.
func (d *Dispatcher) watchdogAlreadyFired(runID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.watchdogFired == nil {
		d.watchdogFired = make(map[string]struct{})
	}
	if _, ok := d.watchdogFired[runID]; ok {
		return true
	}
	d.watchdogFired[runID] = struct{}{}
	return false
}

func (d *Dispatcher) pruneWatchdogFired(activeRuns map[string]struct{}) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for runID := range d.watchdogFired {
		if _, ok := activeRuns[runID]; !ok {
			delete(d.watchdogFired, runID)
		}
	}
}
