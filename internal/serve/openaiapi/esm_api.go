package openaiapi

import (
	"context"
	"errors"
	"strings"

	"github.com/startvibecoding/mothx/internal/esm"
	"github.com/startvibecoding/mothx/internal/session"
)

// ESMControlRequest contains user-controlled ESM fields exposed by WebUI.
type ESMControlRequest struct {
	Objective   string `json:"objective,omitempty"`
	TokenBudget *int64 `json:"tokenBudget,omitempty"`
	Version     string `json:"version,omitempty"`
}

// ESMSnapshot is the stable WebUI representation of an ESM objective.
type ESMSnapshot struct {
	SessionID        string                `json:"sessionId"`
	ESMID            string                `json:"esmId,omitempty"`
	Status           string                `json:"status"`
	Phase            string                `json:"phase,omitempty"`
	Objective        string                `json:"objective,omitempty"`
	TokenBudget      *int64                `json:"tokenBudget,omitempty"`
	TokensUsed       int64                 `json:"tokensUsed"`
	TimeUsedMS       int64                 `json:"timeUsedMs"`
	BlockedCount     int                   `json:"blockedCount,omitempty"`
	BlockedReason    string                `json:"blockedReason,omitempty"`
	CompletionReason string                `json:"completionReason,omitempty"`
	CompletionReview string                `json:"completionReview,omitempty"`
	ProgressSummary  string                `json:"progressSummary,omitempty"`
	RemainingWork    []string              `json:"remainingWork,omitempty"`
	RejectionCount   int                   `json:"rejectionCount,omitempty"`
	RecoveryCount    int                   `json:"recoveryCount,omitempty"`
	RecoveryReason   string                `json:"recoveryReason,omitempty"`
	Version          string                `json:"version,omitempty"`
	CreatedAt        string                `json:"createdAt,omitempty"`
	UpdatedAt        string                `json:"updatedAt,omitempty"`
	Guidance         []session.ESMGuidance `json:"guidance,omitempty"`
}

func (s *Server) esmStore() *esm.Store {
	if s == nil || s.settings == nil {
		return nil
	}
	return esm.NewStore(s.settings.GetSessionDir())
}

func esmSnapshot(obj *esm.Objective) *ESMSnapshot {
	if obj == nil {
		return &ESMSnapshot{Status: "none"}
	}
	return &ESMSnapshot{
		SessionID: obj.SessionID, ESMID: obj.ESMID, Status: string(obj.Status), Phase: string(obj.Phase),
		Objective: obj.Objective, TokenBudget: obj.TokenBudget, TokensUsed: obj.TokensUsed, TimeUsedMS: obj.TimeUsedMS,
		BlockedCount: obj.BlockedCount, BlockedReason: obj.BlockedReason, CompletionReason: obj.CompletionReason,
		CompletionReview: obj.CompletionReview, ProgressSummary: obj.ProgressSummary, RemainingWork: append([]string(nil), obj.RemainingWork...),
		RejectionCount: obj.RejectionCount, RecoveryCount: obj.RecoveryCount, RecoveryReason: obj.RecoveryReason,
		Version:   obj.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		CreatedAt: obj.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), UpdatedAt: obj.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
	}
}

func (s *Server) GetESM(sessionID string) (*ESMSnapshot, error) {
	if sessionID == "" {
		return nil, ErrSessionNotFound
	}
	store := s.esmStore()
	if store == nil {
		return nil, ErrSessionNotFound
	}
	obj, err := store.Get(context.Background(), sessionID)
	if errors.Is(err, esm.ErrNotFound) {
		return &ESMSnapshot{SessionID: sessionID, Status: "none"}, nil
	}
	if err != nil {
		return nil, err
	}
	out := esmSnapshot(obj)
	out.SessionID = sessionID
	if guidance, guidanceErr := session.ListESMGuidance(s.settings.GetSessionDir(), sessionID, "pending", 100); guidanceErr == nil {
		out.Guidance = guidance
	}
	return out, nil
}

func (s *Server) publishESM(sessionID string, snapshot *ESMSnapshot) {
	if s == nil || snapshot == nil {
		return
	}
	s.getEventBroker().PublishRawJSON(sessionID, "", "esm.updated", map[string]any{"snapshot": snapshot, "version": snapshot.Version})
	s.PublishSessionRuntime(sessionID)
}

func (s *Server) CreateESM(sessionID, objective string, budget *int64) (*ESMSnapshot, error) {
	store := s.esmStore()
	if store == nil {
		return nil, ErrSessionNotFound
	}
	obj, err := store.Create(context.Background(), sessionID, objective, budget)
	if err != nil {
		return nil, err
	}
	out := esmSnapshot(obj)
	s.publishESM(sessionID, out)
	s.startESM(sessionID)
	return out, nil
}

func (s *Server) EditESM(sessionID, objective string) (*ESMSnapshot, error) {
	store := s.esmStore()
	if store == nil {
		return nil, ErrSessionNotFound
	}
	obj, err := store.Edit(context.Background(), sessionID, objective)
	if err != nil {
		return nil, err
	}
	out := esmSnapshot(obj)
	s.publishESM(sessionID, out)
	s.startESM(sessionID)
	return out, nil
}

func (s *Server) PauseESM(sessionID string) (*ESMSnapshot, error) {
	s.stopESM(sessionID)
	store := s.esmStore()
	if store == nil {
		return nil, ErrSessionNotFound
	}
	obj, err := store.Pause(context.Background(), sessionID)
	if err != nil {
		return nil, err
	}
	out := esmSnapshot(obj)
	s.publishESM(sessionID, out)
	return out, nil
}

func (s *Server) ResumeESM(sessionID string) (*ESMSnapshot, error) {
	store := s.esmStore()
	if store == nil {
		return nil, ErrSessionNotFound
	}
	obj, err := store.Resume(context.Background(), sessionID)
	if err != nil {
		return nil, err
	}
	out := esmSnapshot(obj)
	s.publishESM(sessionID, out)
	s.startESM(sessionID)
	return out, nil
}

func (s *Server) SetESMBudget(sessionID string, budget *int64) (*ESMSnapshot, error) {
	store := s.esmStore()
	if store == nil {
		return nil, ErrSessionNotFound
	}
	obj, err := store.SetBudget(context.Background(), sessionID, budget)
	if err != nil {
		return nil, err
	}
	out := esmSnapshot(obj)
	s.publishESM(sessionID, out)
	s.startESM(sessionID)
	return out, nil
}

func (s *Server) ClearESM(sessionID string) error {
	s.stopESM(sessionID)
	store := s.esmStore()
	if store == nil {
		return ErrSessionNotFound
	}
	if err := store.Clear(context.Background(), sessionID); err != nil {
		return err
	}
	s.publishESM(sessionID, &ESMSnapshot{SessionID: sessionID, Status: "none"})
	return nil
}

func (s *Server) AddESMGuidance(sessionID, version, text string) (*ESMSnapshot, error) {
	if err := s.ValidateESMVersion(sessionID, version); err != nil {
		return nil, err
	}
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("guidance cannot be empty")
	}
	current, err := s.GetESM(sessionID)
	if err != nil {
		return nil, err
	}
	g := session.ESMGuidance{ID: "guidance-" + session.GenerateID(), SessionID: sessionID, ObjectiveVersion: current.Version, Guidance: text, Status: "pending"}
	if err := session.SaveESMGuidance(s.settings.GetSessionDir(), g); err != nil {
		return nil, err
	}
	out, err := s.GetESM(sessionID)
	if err == nil {
		s.publishESM(sessionID, out)
		s.startESM(sessionID)
	}
	return out, err
}

func (s *Server) ValidateESMVersion(sessionID, version string) error {
	if strings.TrimSpace(version) == "" {
		return nil
	}
	current, err := s.GetESM(sessionID)
	if err != nil {
		return err
	}
	if current.Version != version {
		return errors.New("esm objective changed; reload and try again")
	}
	return nil
}
