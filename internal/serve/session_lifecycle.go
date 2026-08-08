package serve

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/startvibecoding/mothx/internal/serve/channels"
	"github.com/startvibecoding/mothx/internal/session"
)

type lifecycleConflict struct {
	Code    string
	Message string
}

func (e *lifecycleConflict) Error() string { return e.Message }

// SessionLifecycleService is the single coordinator for session persistence,
// API pool state and dispatcher cache state. Database helpers remain in the
// session package; this service owns the cross-runtime lock and ordering.
type SessionLifecycleService struct {
	sessions    interface{ DeleteActiveSession(string) (bool, error) }
	dispatcher  *channels.Dispatcher
	sessionDir  string
	identityMux *session.IdentityLocks
	eventMu     sync.RWMutex
	publish     func(string, any)
}

func NewSessionLifecycleService(sessions interface{ DeleteActiveSession(string) (bool, error) }, dispatcher *channels.Dispatcher, sessionDir string, identityMux *session.IdentityLocks) *SessionLifecycleService {
	if identityMux == nil {
		identityMux = session.NewIdentityLocks()
	}
	return &SessionLifecycleService{sessions: sessions, dispatcher: dispatcher, sessionDir: sessionDir, identityMux: identityMux}
}

func (s *SessionLifecycleService) SetEventPublisher(publish func(string, any)) {
	if s == nil {
		return
	}
	s.eventMu.Lock()
	s.publish = publish
	s.eventMu.Unlock()
}

func (s *SessionLifecycleService) publishEvent(eventType string, data any) {
	if s == nil {
		return
	}
	s.eventMu.RLock()
	publish := s.publish
	s.eventMu.RUnlock()
	if publish != nil {
		publish(eventType, data)
	}
}

func (s *SessionLifecycleService) Delete(ctx context.Context, sessionID string) (bool, error) {
	if s == nil || s.sessions == nil || sessionID == "" {
		return false, fmt.Errorf("session lifecycle service unavailable")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	release, ok := session.TryLockRuntime(s.sessionDir, sessionID)
	if !ok {
		return false, &lifecycleConflict{Code: "session_running", Message: "session has an active run"}
	}
	defer release()
	releaseData := session.LockSessionData(s.sessionDir, sessionID)
	defer releaseData()

	binding, err := session.FindBindingBySessionID(s.sessionDir, sessionID)
	if err != nil {
		return false, err
	}
	if binding != nil {
		return false, &lifecycleConflict{Code: "session_bound", Message: "unbind the channel identity before deleting this session"}
	}
	deleted, err := s.sessions.DeleteActiveSession(sessionID)
	if err != nil || !deleted {
		return deleted, err
	}
	if s.dispatcher != nil {
		s.dispatcher.RefreshSessionTools(sessionID)
	}
	s.publishEvent("session_deleted", map[string]any{"sessionId": sessionID})
	return true, nil
}

func (s *SessionLifecycleService) Bind(ctx context.Context, sessionID, channelType, channelID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	releaseRuntime, ok := session.TryLockRuntime(s.sessionDir, sessionID)
	if !ok {
		return &lifecycleConflict{Code: "session_running", Message: "target session has an active run"}
	}
	defer releaseRuntime()
	releaseIdentity := s.identityMux.Lock(channelType, channelID)
	defer releaseIdentity()
	if err := session.BindSession(s.sessionDir, sessionID, channelType, channelID); err != nil {
		return err
	}
	if s.dispatcher != nil {
		s.dispatcher.RefreshBinding(channelType, channelID)
	}
	s.publishEvent("binding_changed", map[string]any{
		"sessionId": sessionID, "channelType": channelType, "channelId": channelID,
		"toSessionId": sessionID,
	})
	return nil
}

func (s *SessionLifecycleService) Unbind(ctx context.Context, sessionID string) (*session.Binding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	releaseRuntime, ok := session.TryLockRuntime(s.sessionDir, sessionID)
	if !ok {
		return nil, &lifecycleConflict{Code: "session_running", Message: "session has an active run"}
	}
	defer releaseRuntime()
	binding, err := session.FindBindingBySessionID(s.sessionDir, sessionID)
	if err != nil {
		return nil, err
	}
	if binding != nil {
		releaseIdentity := s.identityMux.Lock(binding.ChannelType, binding.ChannelID)
		defer releaseIdentity()
		binding, err = session.FindBindingBySessionID(s.sessionDir, sessionID)
		if err != nil {
			return nil, err
		}
		if binding == nil {
			return nil, &lifecycleConflict{Code: "binding_changed", Message: "session binding changed; retry"}
		}
	}
	if err := session.UnbindSession(s.sessionDir, sessionID); err != nil {
		return nil, err
	}
	if binding != nil && s.dispatcher != nil {
		s.dispatcher.RefreshBinding(binding.ChannelType, binding.ChannelID)
	}
	if binding != nil {
		s.publishEvent("binding_changed", map[string]any{
			"sessionId": binding.SessionID, "channelType": binding.ChannelType,
			"channelId": binding.ChannelID, "fromSessionId": binding.SessionID,
			"toSessionId": "",
		})
	}
	return binding, nil
}

func (s *SessionLifecycleService) Transfer(ctx context.Context, channelType, channelID, fromSessionID, toSessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	releaseRuntime, ok := session.TryLockRuntimes(s.sessionDir, []string{fromSessionID, toSessionID})
	if !ok {
		return &lifecycleConflict{Code: "session_running", Message: "source and target sessions must be idle"}
	}
	defer releaseRuntime()
	releaseIdentity := s.identityMux.Lock(channelType, channelID)
	defer releaseIdentity()
	if err := session.TransferBinding(s.sessionDir, channelType, channelID, fromSessionID, toSessionID); err != nil {
		return err
	}
	if s.dispatcher != nil {
		s.dispatcher.RefreshBinding(channelType, channelID)
	}
	s.publishEvent("binding_changed", map[string]any{
		"channelType": channelType, "channelId": channelID,
		"fromSessionId": fromSessionID, "toSessionId": toSessionID,
	})
	return nil
}

// Rotate creates a new bound session for a channel identity. It is used by
// the channel /new and /clear commands so those commands share the same
// runtime/identity lock ordering as HTTP binding mutations. A forced rotate
// requests cancellation of the active run (context cancel + agent abort via
// the dispatcher), waits a grace period for the runtime lock, and finally
// rotates without the lock — the displaced writer fails its next persistence
// write with ErrSessionModified and exits.
func (s *SessionLifecycleService) Rotate(ctx context.Context, platform, userID string, force bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if platform != "wechat" && platform != "feishu" {
		if s.dispatcher != nil {
			s.dispatcher.RemoveSession("channels/" + platform + "/" + userID)
		}
		return nil
	}
	// Read the binding before taking the runtime lock, then re-read it while
	// holding the identity lock. A concurrent transfer may have changed the
	// session in between; retry with the new session instead of operating on an
	// unlocked target.
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		binding, err := session.FindBinding(s.sessionDir, platform, userID)
		if err != nil {
			return err
		}
		if binding == nil {
			return nil
		}
		releaseRuntime, ok := session.TryLockRuntime(s.sessionDir, binding.SessionID)
		if !ok {
			if !force {
				return &lifecycleConflict{Code: "session_running", Message: channels.ErrSessionRunBusy.Error()}
			}
			if s.dispatcher != nil {
				s.dispatcher.CancelChannelSessionRun(binding.SessionID)
			}
			releaseRuntime, ok = channels.AwaitRuntimeRelease(ctx, s.sessionDir, binding.SessionID, channels.RotateForceGrace)
			if !ok {
				log.Printf("[serve] force-rotating session %s without runtime lock: active run ignored cancellation", binding.SessionID)
				releaseRuntime = func() {}
			}
		}
		releaseIdentity := s.identityMux.Lock(platform, userID)
		current, readErr := session.FindBinding(s.sessionDir, platform, userID)
		if readErr != nil {
			releaseIdentity()
			releaseRuntime()
			return readErr
		}
		if current == nil {
			releaseIdentity()
			releaseRuntime()
			return nil
		}
		if current.SessionID != binding.SessionID {
			releaseIdentity()
			releaseRuntime()
			continue
		}
		workDir := ""
		if s.dispatcher != nil {
			workDir = s.dispatcher.PlatformWorkDir(platform)
		}
		rotated, rotateErr := session.RotateBoundSession(workDir, s.sessionDir, platform, userID, current.SessionID)
		if rotateErr != nil {
			releaseIdentity()
			releaseRuntime()
			return rotateErr
		}
		if s.dispatcher != nil {
			s.dispatcher.RefreshBinding(platform, userID)
		}
		s.publishEvent("binding_changed", map[string]any{
			"channelType": platform, "channelId": userID,
			"fromSessionId": current.SessionID, "toSessionId": rotated.GetHeader().ID,
		})
		releaseIdentity()
		releaseRuntime()
		return nil
	}
}
