package agentruntime

import (
	"testing"

	"github.com/startvibecoding/mothx/internal/provider"
	"github.com/startvibecoding/mothx/internal/session"
)

func TestSessionRuntimeConfigOptionsPersistAndRejectInvalidModel(t *testing.T) {
	workDir := t.TempDir()
	manager := session.New(workDir, t.TempDir())
	if err := manager.Init(); err != nil {
		t.Fatal(err)
	}
	modelOne := &provider.Model{ID: "model-one", Name: "Model One", ContextWindow: 32768, Reasoning: true}
	modelTwo := &provider.Model{ID: "model-two", Name: "Model Two", ContextWindow: 65536, Reasoning: true}
	p := provider.NewMockProvider("test-provider", []*provider.Model{modelOne, modelTwo}, nil)
	runtime := &SessionRuntime{
		ID: manager.GetHeader().ID, Source: SourceACP, EntrySource: SourceACP,
		WorkDir: workDir, Manager: manager,
	}
	if err := runtime.ConfigureSession(p, "test-provider", modelOne, ModeAgent, provider.ThinkingMedium); err != nil {
		t.Fatal(err)
	}
	options := runtime.ConfigOptions()
	if got := optionCurrentValue(options, ConfigOptionModel); got != "test-provider/model-one" {
		t.Fatalf("initial model option = %q", got)
	}
	if err := runtime.SetConfigOption(ConfigOptionModel, "test-provider/model-two"); err != nil {
		t.Fatalf("set model: %v", err)
	}
	if err := runtime.SetConfigOption(ConfigOptionMode, ModePlan); err != nil {
		t.Fatalf("set mode: %v", err)
	}
	if err := runtime.SetConfigOption(ConfigOptionThinkingLevel, string(provider.ThinkingHigh)); err != nil {
		t.Fatalf("set thinking level: %v", err)
	}
	_, _, currentModel, currentMode, currentThinking := runtime.ConfigSnapshot()
	if currentModel == nil || currentModel.ID != modelTwo.ID || currentMode != ModePlan || currentThinking != provider.ThinkingHigh {
		t.Fatalf("runtime config = model %v, mode %q, thinking %q", currentModel, currentMode, currentThinking)
	}
	if entry, ok := manager.GetLatestModelChange(); !ok || entry.ModelID != modelTwo.ID {
		t.Fatalf("persisted model entry = %#v, ok=%v", entry, ok)
	}
	if entry, ok := manager.GetLatestModeChange(); !ok || entry.Mode != ModePlan {
		t.Fatalf("persisted mode entry = %#v, ok=%v", entry, ok)
	}
	if entry, ok := manager.GetLatestThinkingLevelChange(); !ok || entry.ThinkingLevel != string(provider.ThinkingHigh) {
		t.Fatalf("persisted thinking entry = %#v, ok=%v", entry, ok)
	}
	if err := runtime.SetConfigOption(ConfigOptionModel, "test-provider/missing"); err == nil {
		t.Fatal("invalid model unexpectedly succeeded")
	}
	_, _, currentModel, _, _ = runtime.ConfigSnapshot()
	if currentModel == nil || currentModel.ID != modelTwo.ID {
		t.Fatalf("invalid model changed runtime model to %#v", currentModel)
	}
}

func TestSessionConfigOptionsOmitThinkingForNonReasoningModel(t *testing.T) {
	model := &provider.Model{ID: "plain-model", Name: "Plain Model"}
	options := SessionConfigOptions("test-provider", []*provider.Model{model}, model, ModeAgent, provider.ThinkingMedium)
	if optionCurrentValue(options, ConfigOptionThinkingLevel) != "" {
		t.Fatal("non-reasoning model advertised a thinking-level option")
	}
	for _, option := range options {
		if option.ID == ConfigOptionThinkingLevel {
			t.Fatalf("non-reasoning model advertised thinking option: %#v", option)
		}
	}
}

func optionCurrentValue(options []SessionConfigOption, id string) string {
	for _, option := range options {
		if option.ID == id {
			return option.CurrentValue
		}
	}
	return ""
}
