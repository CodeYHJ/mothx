package session

import "sync"

// IdentityLocks serializes operations for one external channel identity. It is
// shared by inbound dispatch and session lifecycle management.
type IdentityLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func NewIdentityLocks() *IdentityLocks {
	return &IdentityLocks{locks: make(map[string]*sync.Mutex)}
}

func (s *IdentityLocks) Lock(channelType, channelID string) func() {
	if s == nil {
		return func() {}
	}
	key := channelType + "\x00" + channelID
	s.mu.Lock()
	lock := s.locks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		s.locks[key] = lock
	}
	s.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}
