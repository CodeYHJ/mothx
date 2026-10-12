package acp

import (
	"github.com/oschina/mothx/internal/config"
	"github.com/oschina/mothx/internal/provider"
)

// The ACP configuration plane (negotiated settings, resolved default
// provider/model, switchable provider catalog, and the structured startup
// reason) is written on the JSON-RPC read loop by management handlers
// (settings/patch, providers/save/delete -> refreshProviderCatalog,
// applyACPArtifactSetting) while sessionOpLanes run session/new/load/resume/
// fork handlers on separate goroutines that read the same fields and may add
// a lazily constructed provider to the catalog (providerFor). Every access
// after startup therefore goes through the catalogMu-guarded accessors below.
//
// Invariants:
//   - The published *config.Settings and the providers map are immutable
//     after publication; writers publish a replacement (copy-on-write) under
//     catalogMu instead of mutating in place. Readers may retain and use a
//     snapshot without holding the lock.
//   - catalogMu is a leaf lock: accessors never call back into code that
//     takes catalogMu or s.mu. Callers may hold s.mu while reading the
//     catalog (lock order s.mu -> catalogMu), never the reverse.
//   - Direct field access is only valid before the read loop starts
//     (construction) or in single-threaded test fixtures.

// catalogSnapshot returns a consistent copy of the configuration plane.
func (s *server) catalogSnapshot() catalogState {
	if s == nil {
		return catalogState{}
	}
	s.catalogMu.RLock()
	defer s.catalogMu.RUnlock()
	return catalogState{
		settings:        s.settings,
		p:               s.p,
		providerName:    s.providerName,
		m:               s.m,
		providers:       s.providers,
		providerStartup: s.providerStartup,
	}
}

// catalogState is one immutable configuration-plane snapshot.
type catalogState struct {
	settings        *config.Settings
	p               provider.Provider
	providerName    string
	m               *provider.Model
	providers       map[string]provider.Provider
	providerStartup *startupError
}

func (s *server) currentSettings() *config.Settings {
	return s.catalogSnapshot().settings
}

func (s *server) currentProvider() provider.Provider {
	return s.catalogSnapshot().p
}

func (s *server) currentModel() *provider.Model {
	return s.catalogSnapshot().m
}

func (s *server) currentProviderName() string {
	return s.catalogSnapshot().providerName
}

func (s *server) currentProviderCatalog() map[string]provider.Provider {
	return s.catalogSnapshot().providers
}

func (s *server) currentStartupError() *startupError {
	return s.catalogSnapshot().providerStartup
}

// publishCatalog atomically replaces the whole configuration plane. Callers
// must have built every component (including a copied, never mutated,
// settings struct and a freshly allocated providers map) before publishing.
func (s *server) publishCatalog(next catalogState) {
	if s == nil {
		return
	}
	s.catalogMu.Lock()
	s.settings = next.settings
	s.p = next.p
	s.providerName = next.providerName
	s.m = next.m
	s.providers = next.providers
	s.providerStartup = next.providerStartup
	s.catalogMu.Unlock()
}

// publishSettings replaces only the published settings pointer. The previous
// struct is never mutated, so concurrent readers keep a consistent snapshot.
func (s *server) publishSettings(settings *config.Settings) {
	if s == nil {
		return
	}
	s.catalogMu.Lock()
	s.settings = settings
	s.catalogMu.Unlock()
}

// cacheProvider adds a lazily constructed provider to the catalog with
// copy-on-write, so concurrent lane readers never observe a map mutation.
func (s *server) cacheProvider(name string, p provider.Provider) {
	if s == nil || p == nil || name == "" {
		return
	}
	s.catalogMu.Lock()
	providers := make(map[string]provider.Provider, len(s.providers)+1)
	for key, value := range s.providers {
		providers[key] = value
	}
	providers[name] = p
	s.providers = providers
	s.catalogMu.Unlock()
}
