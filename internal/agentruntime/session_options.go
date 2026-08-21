package agentruntime

import (
	"fmt"
	"sort"
	"strings"

	"github.com/startvibecoding/mothx/internal/provider"
	providerfactory "github.com/startvibecoding/mothx/internal/provider/factory"
)

// SessionConfigOption is the front-end-neutral representation of a mutable
// session setting. ACP serializes this shape directly as a select option, while
// other adapters can render the same catalog in their native UI.
type SessionConfigOption struct {
	Type         string                      `json:"type"`
	ID           string                      `json:"id"`
	Name         string                      `json:"name"`
	Description  string                      `json:"description,omitempty"`
	Category     string                      `json:"category,omitempty"`
	CurrentValue string                      `json:"currentValue"`
	Options      []SessionConfigOptionChoice `json:"options,omitempty"`
}

// SessionConfigOptionChoice is one value in a mutable session option.
type SessionConfigOptionChoice struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SessionConfigOption IDs are stable protocol-neutral identifiers.
const (
	ConfigOptionModel         = "model"
	ConfigOptionMode          = "mode"
	ConfigOptionThinkingLevel = "thinking_level"
)

// SessionModelBinding is the persisted/runtime model identity for one session.
// Provider is process-owned; Model is session-owned and can be changed without
// rebuilding credentials or any other session resources.
type SessionModelBinding struct {
	ProviderName string
	Model        *provider.Model
}

// SessionConfigOptions builds the standard model, mode, and thinking catalogs.
func SessionConfigOptions(providerName string, models []*provider.Model, model *provider.Model, mode string, thinking provider.ThinkingLevel) []SessionConfigOption {
	modelChoices := make([]SessionConfigOptionChoice, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, candidate := range models {
		if candidate == nil || candidate.ID == "" {
			continue
		}
		value := providerfactory.QualifiedModel(providerName, candidate)
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		name := candidate.Name
		if name == "" {
			name = candidate.ID
		}
		modelChoices = append(modelChoices, SessionConfigOptionChoice{Value: value, Name: name})
	}
	sort.SliceStable(modelChoices, func(i, j int) bool { return modelChoices[i].Value < modelChoices[j].Value })
	options := []SessionConfigOption{
		{Type: "select", ID: ConfigOptionModel, Name: "Model", Category: "model", CurrentValue: providerfactory.QualifiedModel(providerName, model), Options: modelChoices},
		{Type: "select", ID: ConfigOptionMode, Name: "Mode", Category: "mode", CurrentValue: mode, Options: []SessionConfigOptionChoice{
			{Value: ModeAgent, Name: "Agent"}, {Value: ModePlan, Name: "Plan"}, {Value: ModeYolo, Name: "Yolo"}, {Value: ModeOS, Name: "OS"},
		}},
	}
	if model != nil && model.Reasoning {
		options = append(options, SessionConfigOption{Type: "select", ID: ConfigOptionThinkingLevel, Name: "Thinking level", Category: "thought_level", CurrentValue: string(thinking), Options: []SessionConfigOptionChoice{
			{Value: string(provider.ThinkingOff), Name: "Off"}, {Value: string(provider.ThinkingMinimal), Name: "Minimal"}, {Value: string(provider.ThinkingLow), Name: "Low"},
			{Value: string(provider.ThinkingMedium), Name: "Medium"}, {Value: string(provider.ThinkingHigh), Name: "High"}, {Value: string(provider.ThinkingXHigh), Name: "XHigh"}, {Value: string(provider.ThinkingMax), Name: "Max"},
		}})
	}
	return options
}

// ValidateThinkingLevel validates a config option value without making
// provider-specific assumptions.
func ValidateThinkingLevel(value string) (provider.ThinkingLevel, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return provider.ThinkingMedium, nil
	}
	level := provider.ThinkingLevel(value)
	switch level {
	case provider.ThinkingOff, provider.ThinkingMinimal, provider.ThinkingLow, provider.ThinkingMedium, provider.ThinkingHigh, provider.ThinkingXHigh, provider.ThinkingMax:
		return level, nil
	default:
		return "", fmt.Errorf("invalid thinking level %q", value)
	}
}
