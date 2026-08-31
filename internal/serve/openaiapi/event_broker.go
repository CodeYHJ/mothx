package openaiapi

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"
)

// BrokerEvent is the unified event envelope used by EventBroker.
// Every event flowing through the system carries a sessionID, optional runID,
// a stream name, an event name, a monotonic seq, and arbitrary data.
type BrokerEvent struct {
	SessionID string `json:"sessionId"`
	RunID     string `json:"runId,omitempty"`
	Stream    string `json:"stream"` // "transcript", "run", "capability", "tool", "approval", "runtime", "control"
	Event     string `json:"event"`  // e.g. "tool_event", "transcript", "runtime_event", "done"
	Seq       int64  `json:"seq"`
	Data      any    `json:"data,omitempty"`
}

// EventBroker is the single event distribution hub for all session events.
// It replaces sessionStreamHub as the unified publish/subscribe layer.
// Subscribers receive events on channels; backpressure is handled by marking
// the connection as needing resync instead of silently dropping events.
type EventBroker struct {
	mu          sync.RWMutex
	subscribers map[string]map[chan BrokerEvent]struct{} // sessionID -> set of subscriber channels
	resync      map[chan BrokerEvent]chan struct{}       // closed only when backpressure requires reconnect
	seqs        map[string]*atomic.Int64                 // sessionID -> monotonic seq counter
}

// NewEventBroker creates a new EventBroker.
func NewEventBroker() *EventBroker {
	return &EventBroker{
		subscribers: make(map[string]map[chan BrokerEvent]struct{}),
		resync:      make(map[chan BrokerEvent]chan struct{}),
		seqs:        make(map[string]*atomic.Int64),
	}
}

// nextSeq returns the next monotonic sequence number for a session.
func (b *EventBroker) nextSeq(sessionID string) int64 {
	if b == nil || sessionID == "" {
		return 0
	}
	b.mu.Lock()
	counter, ok := b.seqs[sessionID]
	if !ok {
		counter = &atomic.Int64{}
		b.seqs[sessionID] = counter
	}
	b.mu.Unlock()
	return counter.Add(1)
}

// Subscribe returns a channel that receives BrokerEvent for the given session.
// The returned cancel function unsubscribes and closes the channel.
// The channel is buffered (capacity 256) to reduce blocking.
func (b *EventBroker) Subscribe(sessionID string) (<-chan BrokerEvent, func()) {
	events, _, cancel := b.SubscribeWithResync(sessionID)
	return events, cancel
}

// SubscribeWithResync also exposes a signal that closes only when this
// subscriber overflowed and must reconnect/replay. Ordinary unsubscribe only
// closes the event stream, so protocol adapters can distinguish the two.
func (b *EventBroker) SubscribeWithResync(sessionID string) (<-chan BrokerEvent, <-chan struct{}, func()) {
	ch := make(chan BrokerEvent, 256)
	resync := make(chan struct{})
	if b == nil || sessionID == "" {
		close(ch)
		return ch, resync, func() {}
	}
	b.mu.Lock()
	if b.subscribers[sessionID] == nil {
		b.subscribers[sessionID] = make(map[chan BrokerEvent]struct{})
	}
	b.subscribers[sessionID][ch] = struct{}{}
	b.resync[ch] = resync
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			if subs := b.subscribers[sessionID]; subs != nil {
				if _, active := subs[ch]; active {
					delete(subs, ch)
					delete(b.resync, ch)
					close(ch)
				}
				if len(subs) == 0 {
					delete(b.subscribers, sessionID)
				}
			}
			b.mu.Unlock()
		})
	}
	return ch, resync, cancel
}

// Publish sends an event to all subscribers of the event's session.
// Events are delivered best-effort; if a subscriber's channel is full,
// the event is dropped for that subscriber and the subscriber is NOT
// automatically closed. Callers should use PublishWithResync for
// backpressure-aware delivery.
func (b *EventBroker) Publish(ev BrokerEvent) {
	if b == nil || ev.SessionID == "" {
		return
	}
	if ev.Seq == 0 {
		ev.Seq = b.nextSeq(ev.SessionID)
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subscribers[ev.SessionID] {
		select {
		case ch <- ev:
		default:
			// Subscriber is too slow; drop the event for this subscriber.
			// The subscriber should detect gaps via seq and request resync.
		}
	}
}

// PublishWithResync is like Publish but sends a resync_required event
// to subscribers whose channels are full, then closes their subscription.
func (b *EventBroker) PublishWithResync(ev BrokerEvent) {
	if b == nil || ev.SessionID == "" {
		return
	}
	if ev.Seq == 0 {
		ev.Seq = b.nextSeq(ev.SessionID)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subscribers[ev.SessionID] {
		select {
		case ch <- ev:
		default:
			// Channel full — send resync and close
			delete(b.subscribers[ev.SessionID], ch)
			if resync := b.resync[ch]; resync != nil {
				delete(b.resync, ch)
				close(resync)
			}
			close(ch)
		}
	}
}

// ActiveSubscriberCount returns the number of subscribers for a session.
func (b *EventBroker) ActiveSubscriberCount(sessionID string) int {
	if b == nil || sessionID == "" {
		return 0
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers[sessionID])
}

// CurrentSeq returns the current monotonic sequence number for a session.
// This is used to establish a replay boundary: events with seq <= this value
// are covered by SQLite replay and should not be forwarded as live events.
func (b *EventBroker) CurrentSeq(sessionID string) int64 {
	if b == nil || sessionID == "" {
		return 0
	}
	b.mu.Lock()
	counter, ok := b.seqs[sessionID]
	b.mu.Unlock()
	if !ok {
		return 0
	}
	return counter.Load()
}

// PublishToolEvent is a convenience wrapper for tool status events.
func (b *EventBroker) PublishToolEvent(sessionID, runID string, data any) {
	b.PublishWithResync(BrokerEvent{
		SessionID: sessionID,
		RunID:     runID,
		Stream:    "tool",
		Event:     "tool_event",
		Data:      data,
	})
}

// PublishTranscriptEvent is a convenience wrapper for transcript events.
func (b *EventBroker) PublishTranscriptEvent(sessionID, runID string, data any) {
	b.PublishWithResync(BrokerEvent{
		SessionID: sessionID,
		RunID:     runID,
		Stream:    "transcript",
		Event:     "transcript",
		Data:      data,
	})
}

// PublishRuntimeEvent is a convenience wrapper for runtime snapshot events.
func (b *EventBroker) PublishRuntimeEvent(sessionID, runID string, data any) {
	b.PublishWithResync(BrokerEvent{
		SessionID: sessionID,
		RunID:     runID,
		Stream:    "runtime",
		Event:     "runtime_event",
		Data:      data,
	})
}

// PublishRunEvent publishes a run lifecycle event.
func (b *EventBroker) PublishRunEvent(sessionID, runID string, data any) {
	b.PublishWithResync(BrokerEvent{
		SessionID: sessionID,
		RunID:     runID,
		Stream:    "run",
		Event:     "run_event",
		Data:      data,
	})
}

// PublishCapabilityEvent publishes a capability change event.
func (b *EventBroker) PublishCapabilityEvent(sessionID, runID string, data any) {
	b.PublishWithResync(BrokerEvent{
		SessionID: sessionID,
		RunID:     runID,
		Stream:    "capability",
		Event:     "capability_event",
		Data:      data,
	})
}

// PublishApprovalEvent publishes an approval-related event.
func (b *EventBroker) PublishApprovalEvent(sessionID, runID, event string, data any) {
	b.PublishWithResync(BrokerEvent{
		SessionID: sessionID,
		RunID:     runID,
		Stream:    "approval",
		Event:     event,
		Data:      data,
	})
}

// PublishDone publishes a stream done event for a session/run.
func (b *EventBroker) PublishDone(sessionID, runID string, data any) {
	b.PublishWithResync(BrokerEvent{
		SessionID: sessionID,
		RunID:     runID,
		Stream:    "control",
		Event:     "done",
		Data:      data,
	})
}

// PublishHeartbeat sends a keepalive to session subscribers.
func (b *EventBroker) PublishHeartbeat(sessionID string) {
	b.PublishWithResync(BrokerEvent{
		SessionID: sessionID,
		Stream:    "control",
		Event:     "heartbeat",
		Data: map[string]any{
			"sessionId": sessionID,
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		},
	})
}

// PublishRawJSON publishes a raw JSON payload as an event, preserving the
// original event name and stream. This is used during migration from
// sessionStreamHub to EventBroker.
func (b *EventBroker) PublishRawJSON(sessionID, runID, eventName string, data any) {
	stream := eventName
	switch eventName {
	case "tool_event":
		stream = "tool"
	case "transcript":
		stream = "transcript"
	case "runtime_event":
		stream = "runtime"
	case "run_event":
		stream = "run"
	case "capability_event":
		stream = "capability"
	case "approval_request", "approval_response", "approval_resolved":
		stream = "approval"
	case "done", "heartbeat":
		stream = "control"
	case "esm.updated", "esm.snapshot", "esm.review", "esm.recovery", "esm.completed", "esm.paused", "esm.failed":
		stream = "esm"
	}
	b.PublishWithResync(BrokerEvent{
		SessionID: sessionID,
		RunID:     runID,
		Stream:    stream,
		Event:     eventName,
		Data:      data,
	})
}

// MarshalJSON is a helper to marshal event data, useful for replay.
func MarshalEventData(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return raw
}
