package acp

import (
	"fmt"
	"sync"
	"testing"

	"github.com/oschina/mothx/internal/config"
	"github.com/oschina/mothx/internal/provider"
)

// session/new/load/resume/fork handlers run on sessionOpLanes goroutines while
// the JSON-RPC read loop republishes the configuration plane
// (refreshProviderCatalog, applyACPArtifactSetting). Before catalogMu, lane
// handlers iterated s.providers and cached lazily constructed providers with
// no synchronization, so one concurrent management write (or two lane cache
// writes) could crash the process with "concurrent map iteration and map
// write". This test pins the synchronized contract; it must be run with
// -race to also catch the torn settings/field reads.
func TestCatalogConcurrentLaneAndManageAccess(t *testing.T) {
	model := &provider.Model{ID: "catalog-model", Name: "Catalog Model"}
	defaultProvider := provider.NewMockProvider("catalog-default", []*provider.Model{model}, nil)
	settings := &config.Settings{}

	srv := &server{sessions: make(map[string]*sessionRuntime)}
	srv.publishCatalog(catalogState{
		settings:     settings,
		p:            defaultProvider,
		providerName: "catalog-default",
		m:            model,
		providers:    map[string]provider.Provider{"catalog-default": defaultProvider},
	})

	const workers = 8
	const iterations = 50
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				// Lane-side reads: the default hit path and a miss path that
				// would construct (and fail) without touching shared state.
				if p, m, err := srv.providerFor("catalog-default", ""); err != nil || p == nil || m == nil {
					t.Errorf("providerFor default: %v", err)
					return
				}
				if _, _, err := srv.providerFor("missing-provider", ""); err == nil {
					t.Errorf("expected construction failure for unknown provider")
					return
				}
				// Lane-side copy-on-write cache fill.
				srv.cacheProvider(fmt.Sprintf("lane-%d-%d", w, i), defaultProvider)
				// Read-loop-side republish, mirroring refreshProviderCatalog's
				// settings copy-on-write publication.
				updated := *srv.currentSettings()
				updated.DefaultModel = fmt.Sprintf("model-%d", i)
				srv.publishCatalog(catalogState{
					settings:     &updated,
					p:            defaultProvider,
					providerName: "catalog-default",
					m:            model,
					providers:    map[string]provider.Provider{"catalog-default": defaultProvider},
				})
				srv.applyACPArtifactSetting(i%2 == 0)
				if srv.providerUnavailable() {
					t.Errorf("provider unexpectedly unavailable")
					return
				}
			}
		}(w)
	}
	wg.Wait()

	snapshot := srv.catalogSnapshot()
	if snapshot.p == nil || snapshot.providerName != "catalog-default" || snapshot.settings == nil {
		t.Fatalf("catalog lost state after concurrent access: %+v", snapshot)
	}
}
