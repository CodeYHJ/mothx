package agentruntime

import (
	"fmt"
	"sync"
)

// DecisionKind identifies an interactive decision that can pause a run.
type DecisionKind string

const (
	DecisionApproval DecisionKind = "approval"
	DecisionQuestion DecisionKind = "question"
)

// DecisionRequest is the adapter-neutral identity of a pending decision.
type DecisionRequest struct {
	ID        string
	RunID     string
	SessionID string
	Kind      DecisionKind
	Resolve   func(string) error
}

// DecisionResolution is the adapter-neutral result of resolving a decision.
type DecisionResolution struct {
	ID     string
	Kind   DecisionKind
	Status string
	Value  string
}

// DecisionService owns pending decision identity and first-response-wins
// semantics. Protocol-specific payloads and rendering remain in adapters.
type DecisionService struct {
	mu        sync.Mutex
	pending   map[string]DecisionRequest
	resolvers map[string]func(string) error
}

func (s *DecisionService) Register(request DecisionRequest) error {
	if s == nil {
		return fmt.Errorf("decision service is nil")
	}
	if request.ID == "" || request.RunID == "" {
		return fmt.Errorf("decision ID and run ID are required")
	}
	if request.Kind != DecisionApproval && request.Kind != DecisionQuestion {
		return fmt.Errorf("unsupported decision kind: %s", request.Kind)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		s.pending = make(map[string]DecisionRequest)
	}
	if s.resolvers == nil {
		s.resolvers = make(map[string]func(string) error)
	}
	if _, ok := s.pending[request.ID]; ok {
		return fmt.Errorf("decision already pending: %s", request.ID)
	}
	s.pending[request.ID] = request
	return nil
}

// Bind associates an adapter-owned resume callback with a registered decision.
// The callback runs after Resolve removes the decision from the pending set.
func (s *DecisionService) Bind(id string, resolve func(string) error) error {
	if s == nil {
		return fmt.Errorf("decision service is nil")
	}
	if id == "" || resolve == nil {
		return fmt.Errorf("decision ID and resolve callback are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.pending[id]; !ok {
		return fmt.Errorf("decision is not pending: %s", id)
	}
	if s.resolvers == nil {
		s.resolvers = make(map[string]func(string) error)
	}
	s.resolvers[id] = resolve
	return nil
}

func (s *DecisionService) Resolve(resolution DecisionResolution) (DecisionRequest, error) {
	if s == nil {
		return DecisionRequest{}, fmt.Errorf("decision service is nil")
	}
	s.mu.Lock()
	request, ok := s.pending[resolution.ID]
	if !ok {
		s.mu.Unlock()
		return DecisionRequest{}, fmt.Errorf("decision is no longer pending: %s", resolution.ID)
	}
	if resolution.Kind != "" && resolution.Kind != request.Kind {
		s.mu.Unlock()
		return DecisionRequest{}, fmt.Errorf("decision kind mismatch: %s", resolution.ID)
	}
	resolver := s.resolvers[resolution.ID]
	delete(s.pending, resolution.ID)
	delete(s.resolvers, resolution.ID)
	s.mu.Unlock()
	if resolver != nil {
		if err := resolver(resolution.Value); err != nil {
			return request, err
		}
	}
	return request, nil
}

// ClearRunWithValue removes all decisions for a Run and invokes their resolver
// callbacks with the supplied value. It is used for cancellation and timeout
// paths where no protocol resolution is available.
func (s *DecisionService) ClearRunWithValue(runID, value string) []DecisionRequest {
	if s == nil || runID == "" {
		return nil
	}
	s.mu.Lock()
	var cleared []DecisionRequest
	var resolvers []func(string) error
	for id, request := range s.pending {
		if request.RunID != runID {
			continue
		}
		cleared = append(cleared, request)
		if resolver := s.resolvers[id]; resolver != nil {
			resolvers = append(resolvers, resolver)
		}
		delete(s.pending, id)
		delete(s.resolvers, id)
	}
	s.mu.Unlock()
	for _, resolver := range resolvers {
		_ = resolver(value)
	}
	return cleared
}
func (s *DecisionService) ClearRun(runID string) []DecisionRequest {
	if s == nil || runID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var cleared []DecisionRequest
	for id, request := range s.pending {
		if request.RunID == runID {
			cleared = append(cleared, request)
			delete(s.pending, id)
			delete(s.resolvers, id)
		}
	}
	return cleared
}

func (s *DecisionService) Pending() []DecisionRequest {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]DecisionRequest, 0, len(s.pending))
	for _, request := range s.pending {
		result = append(result, request)
	}
	return result
}
