package openaiapi

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/startvibecoding/mothx/internal/session"
	"golang.org/x/net/websocket"
)

type runWebSocketMessage struct {
	Type          string                     `json:"type"`
	ClientID      string                     `json:"clientId,omitempty"`
	Subscriptions []runWebSocketSubscription `json:"subscriptions,omitempty"`
	SessionIDs    []string                   `json:"sessionIds,omitempty"`
}

type runWebSocketSubscription struct {
	SessionID string              `json:"sessionId"`
	Cursor    sessionStreamCursor `json:"cursor"`
}

type runWebSocketEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId,omitempty"`
	RunID     string `json:"runId,omitempty"`
	Stream    string `json:"stream,omitempty"`
	Event     string `json:"event,omitempty"`
	Seq       int64  `json:"seq,omitempty"`
	Data      any    `json:"data,omitempty"`
}

func (s *Server) RunWebSocketHandler() websocket.Handler {
	return websocket.Handler(func(ws *websocket.Conn) {
		s.runWebSocketLoop(ws)
	})
}

// HandleRunWebSocket serves the durable session/run event protocol used by WebUI.
// Disconnecting this socket only removes subscriptions; it never cancels a run.
func (s *Server) HandleRunWebSocket(w http.ResponseWriter, r *http.Request) {
	s.RunWebSocketHandler().ServeHTTP(w, r)
}

func (s *Server) runWebSocketLoop(ws *websocket.Conn) {
	if s == nil || ws == nil {
		return
	}
	var writeMu sync.Mutex
	write := func(value any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return websocket.JSON.Send(ws, value)
	}
	type subscription struct {
		sessionID string
		cancel    func()
	}
	subs := make(map[string]subscription)
	defer func() {
		for _, sub := range subs {
			sub.cancel()
		}
	}()

	for {
		var msg runWebSocketMessage
		if err := websocket.JSON.Receive(ws, &msg); err != nil {
			return
		}
		switch msg.Type {
		case "hello":
			if err := write(map[string]any{"type": "ready", "protocol": 1, "clientId": msg.ClientID}); err != nil {
				return
			}
		case "subscribe":
			for _, item := range msg.Subscriptions {
				if item.SessionID == "" {
					continue
				}
				if old, ok := subs[item.SessionID]; ok {
					old.cancel()
				}
				events, cancel := s.getSessionStreamHub().subscribe(item.SessionID)
				subs[item.SessionID] = subscription{sessionID: item.SessionID, cancel: cancel}
				cursor := item.Cursor
				if err := s.writeRunWebSocketReplay(write, item.SessionID, &cursor); err != nil {
					cancel()
					delete(subs, item.SessionID)
					_ = write(runWebSocketEvent{Type: "error", SessionID: item.SessionID, Data: err.Error()})
					continue
				}
				go s.forwardRunWebSocketEvents(write, item.SessionID, events, &cursor)
			}
		case "unsubscribe":
			for _, id := range msg.SessionIDs {
				if sub, ok := subs[id]; ok {
					sub.cancel()
					delete(subs, id)
				}
			}
		case "replay":
			// Clients may reconnect and request a cursor replay without resubscribing.
		default:
			_ = write(map[string]any{"type": "error", "error": "unknown websocket message type"})
		}
	}
}

func (s *Server) writeRunWebSocketReplay(write func(any) error, sessionID string, cursor *sessionStreamCursor) error {
	if _, found, err := s.findSessionWorkDir(sessionID); err != nil {
		return err
	} else if !found {
		return ErrSessionNotFound
	}
	sessionDir := s.settings.GetSessionDir()
	messages, err := session.ListSessionMessagesAfter(sessionDir, sessionID, cursor.EntrySeq, 200)
	if err != nil {
		return err
	}
	for _, item := range messages {
		for _, entry := range providerMessageToSessionEntries(item.Message, item.Seq, item.EntryID) {
			if err := write(runWebSocketEvent{Type: "session_event", SessionID: sessionID, Stream: "transcript", Event: "transcript", Seq: item.Seq, Data: messageTranscriptEvent(entry)}); err != nil {
				return err
			}
		}
		if item.Seq > cursor.EntrySeq {
			cursor.EntrySeq = item.Seq
		}
	}
	runEvents, err := session.ListSessionRunEventsAfter(sessionDir, sessionID, cursor.RunSeq, 200)
	if err != nil {
		return err
	}
	for _, item := range runEvents {
		if err := write(runWebSocketEvent{Type: "session_event", SessionID: sessionID, RunID: item.Event.RunID, Stream: "run", Event: item.Event.EventType, Seq: item.Seq, Data: sessionRunEventToEntry(item.Event, item.Seq)}); err != nil {
			return err
		}
		cursor.RunSeq = item.Seq
	}
	capEvents, err := session.ListSessionCapabilityEventsAfter(sessionDir, sessionID, cursor.CapabilitySeq, 200)
	if err != nil {
		return err
	}
	for _, item := range capEvents {
		if err := write(runWebSocketEvent{Type: "session_event", SessionID: sessionID, RunID: item.Event.RunID, Stream: "capability", Event: item.Event.EventType, Seq: item.Seq, Data: sessionCapabilityEventToEntry(item.Event, item.Seq)}); err != nil {
			return err
		}
		cursor.CapabilitySeq = item.Seq
	}
	return nil
}

func (s *Server) forwardRunWebSocketEvents(write func(any) error, sessionID string, events <-chan sessionStreamEvent, cursor *sessionStreamCursor) {
	for event := range events {
		payload := event.Data
		var runID string
		if raw, err := json.Marshal(payload); err == nil {
			var envelope struct {
				RunID string `json:"runId"`
			}
			_ = json.Unmarshal(raw, &envelope)
			runID = envelope.RunID
		}
		if err := write(runWebSocketEvent{Type: "session_event", SessionID: sessionID, RunID: runID, Stream: event.Name, Event: event.Name, Data: payload}); err != nil {
			return
		}
	}
}
