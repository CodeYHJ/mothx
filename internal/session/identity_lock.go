package session

// IdentityLocks serializes operations for one external channel identity. It is
// shared by inbound dispatch and session lifecycle management. Entries are
// evicted once the last holder releases them so the map does not grow without
// bound in long-running processes.
type IdentityLocks struct {
	registry *lockRegistry
}

func NewIdentityLocks() *IdentityLocks {
	return &IdentityLocks{registry: newLockRegistry()}
}

func (s *IdentityLocks) Lock(channelType, channelID string) func() {
	if s == nil {
		return func() {}
	}
	key := channelType + "\x00" + channelID
	lock := s.registry.acquire(key)
	lock.Lock()
	return func() {
		lock.Unlock()
		s.registry.drop(key, lock)
	}
}
