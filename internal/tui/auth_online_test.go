package tui

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/startvibecoding/mothx/internal/config"
	"github.com/startvibecoding/mothx/internal/provider"
	"github.com/startvibecoding/mothx/internal/tui/i18n"
)

func newOnlineModelsTestApp() *App {
	return &App{
		settings:   config.DefaultSettings(),
		translator: i18n.New(i18n.LanguageEN),
		auth: authDialogState{
			Open:       true,
			View:       authViewModelList,
			ProviderID: "test-provider",
			Provider: providerEditState{
				API:     "openai-chat",
				BaseURL: "https://api.example.test/v1",
				APIKey:  "test-key",
			},
		},
	}
}

func TestStartFetchOnlineModelsRequiresBaseURLAndAPI(t *testing.T) {
	a := newOnlineModelsTestApp()
	a.auth.Provider.BaseURL = ""

	cmd := a.startFetchOnlineModels()
	if cmd != nil {
		t.Fatal("expected no command when Base URL is missing")
	}
	if a.auth.OnlineLoading {
		t.Fatal("OnlineLoading = true, want false")
	}
	if a.auth.Error == "" {
		t.Fatal("expected an error message when Base URL is missing")
	}

	a.auth.Provider.BaseURL = "https://api.example.test/v1"
	a.auth.Provider.API = ""
	if cmd := a.startFetchOnlineModels(); cmd != nil {
		t.Fatal("expected no command when API type is missing")
	}
}

func TestStartFetchOnlineModelsDiscoversFromModelsEndpoint(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			t.Errorf("upstream request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"online-model","name":"Online Model","context_length":64000,"max_output_tokens":4096,"input_modalities":["text","image"],"reasoning":true}]}`)
	}))
	defer upstream.Close()

	a := newOnlineModelsTestApp()
	a.auth.Provider.BaseURL = upstream.URL + "/v1"

	cmd := a.startFetchOnlineModels()
	if cmd == nil {
		t.Fatalf("expected fetch command, got error %q", a.auth.Error)
	}
	if !a.auth.OnlineLoading {
		t.Fatal("OnlineLoading = false, want true")
	}
	msg, ok := cmd().(onlineModelsLoadedMsg)
	if !ok {
		t.Fatalf("unexpected message type %T", cmd())
	}
	if msg.err != nil {
		t.Fatalf("fetch error: %v", msg.err)
	}

	a.handleOnlineModelsLoaded(msg)
	if a.auth.OnlineLoading {
		t.Fatal("OnlineLoading still true after result")
	}
	if a.auth.View != authViewModelsOnline {
		t.Fatalf("view = %v, want authViewModelsOnline", a.auth.View)
	}
	if len(a.auth.OnlineModels) != 1 || a.auth.OnlineModels[0].ID != "online-model" {
		t.Fatalf("online models = %#v", a.auth.OnlineModels)
	}
}

func TestHandleOnlineModelsLoadedErrorsStayInPlace(t *testing.T) {
	a := newOnlineModelsTestApp()

	a.handleOnlineModelsLoaded(onlineModelsLoadedMsg{providerID: "test-provider", err: fmt.Errorf("boom")})
	if a.auth.View != authViewModelList {
		t.Fatalf("view = %v, want authViewModelList", a.auth.View)
	}
	if !strings.Contains(a.auth.Error, "boom") {
		t.Fatalf("error = %q, want fetch failure containing boom", a.auth.Error)
	}

	a.auth.Error = ""
	a.handleOnlineModelsLoaded(onlineModelsLoadedMsg{providerID: "test-provider"})
	if a.auth.View != authViewModelList || a.auth.Error == "" {
		t.Fatalf("empty result should stay and set error: view=%v error=%q", a.auth.View, a.auth.Error)
	}
}

func TestHandleOnlineModelsLoadedIgnoresStaleProvider(t *testing.T) {
	a := newOnlineModelsTestApp()

	a.handleOnlineModelsLoaded(onlineModelsLoadedMsg{
		providerID: "other-provider",
		models:     []provider.DiscoveredModel{{ID: "m1"}},
	})
	if a.auth.View != authViewModelList || len(a.auth.OnlineModels) != 0 {
		t.Fatalf("stale result applied: view=%v models=%v", a.auth.View, a.auth.OnlineModels)
	}

	a.auth.Open = false
	a.handleOnlineModelsLoaded(onlineModelsLoadedMsg{
		providerID: "test-provider",
		models:     []provider.DiscoveredModel{{ID: "m1"}},
	})
	if len(a.auth.OnlineModels) != 0 {
		t.Fatal("result applied after dialog closed")
	}
}

func TestSelectOnlineModelTogglesDraftMembership(t *testing.T) {
	a := newOnlineModelsTestApp()
	a.auth.OnlineModels = []provider.DiscoveredModel{{
		ID:            "online-model",
		Name:          "Online Model",
		ContextWindow: 64000,
		MaxTokens:     4096,
		Input:         []string{"text", "image"},
		Reasoning:     true,
	}}
	a.pushAuthView(authViewModelsOnline)

	a.selectOnlineModel("online:online-model")
	me := a.auth.Models["online-model"]
	if me == nil {
		t.Fatalf("model not added to draft: %#v", a.auth.Models)
	}
	if me.Name != "Online Model" || me.ContextWindow != 64000 || me.MaxTokens != 4096 || !me.MaxTokensEdited || !me.Reasoning {
		t.Fatalf("discovered metadata not applied: %#v", me)
	}
	if len(me.Input) != 2 || me.Input[1] != "image" {
		t.Fatalf("input modalities = %#v", me.Input)
	}
	if len(a.auth.ModelOrder) != 1 || a.auth.ModelOrder[0] != "online-model" {
		t.Fatalf("model order = %#v", a.auth.ModelOrder)
	}
	if !a.isAuthModelAdded("online-model") {
		t.Fatal("isAuthModelAdded = false after add")
	}

	// Marker renders for added models.
	opts := a.authOnlineModelOptions()
	if !strings.HasPrefix(opts[0].Title, "✓") {
		t.Fatalf("added model marker missing: %q", opts[0].Title)
	}

	// Toggle again removes it.
	a.selectOnlineModel("online:online-model")
	if len(a.auth.Models) != 0 || len(a.auth.ModelOrder) != 0 {
		t.Fatalf("model not removed: models=%#v order=%#v", a.auth.Models, a.auth.ModelOrder)
	}
}

func TestSelectOnlineModelPrefersBuiltInDefaults(t *testing.T) {
	a := newOnlineModelsTestApp()
	// "xai" is a built-in provider with known model defaults.
	a.auth.ProviderID = "xai"
	a.auth.OnlineModels = []provider.DiscoveredModel{{ID: "grok-3", ContextWindow: 1}}
	a.pushAuthView(authViewModelsOnline)

	a.selectOnlineModel("online:grok-3")
	me := a.auth.Models["grok-3"]
	if me == nil {
		t.Fatalf("model not added: %#v", a.auth.Models)
	}
	if me.ContextWindow == 1 {
		t.Fatalf("built-in defaults should win over discovered metadata: %#v", me)
	}
}

func TestModelListOptionsIncludeFetchOnline(t *testing.T) {
	a := newOnlineModelsTestApp()
	opts := a.authModelListOptions()
	found := false
	for _, opt := range opts {
		if opt.Value == "fetchOnline" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("fetchOnline option missing from model list: %#v", opts)
	}

	detail := a.authSettingsDetailOptions()
	found = false
	for _, opt := range detail {
		if opt.Value == "fetchOnline" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("fetchOnline option missing from settings detail: %#v", detail)
	}
}

func TestDeleteSelectedOnlineModelRemovesDraftEntry(t *testing.T) {
	a := newOnlineModelsTestApp()
	a.auth.OnlineModels = []provider.DiscoveredModel{{ID: "online-model", Name: "Online Model"}}
	a.pushAuthView(authViewModelsOnline)
	a.selectOnlineModel("online:online-model")

	// First option is the added model.
	a.auth.Cursor = 0
	if !a.deleteSelectedOnlineModel() {
		t.Fatal("deleteSelectedOnlineModel = false")
	}
	if len(a.auth.Models) != 0 {
		t.Fatalf("model still present: %#v", a.auth.Models)
	}

	// Deleting a not-added model is a no-op.
	if a.deleteSelectedOnlineModel() {
		t.Fatal("deleteSelectedOnlineModel = true for not-added model")
	}
}
