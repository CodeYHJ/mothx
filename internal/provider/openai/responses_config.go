package openai

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/startvibecoding/mothx/internal/config"
	"github.com/startvibecoding/mothx/internal/provider"
)

func validateResponsesConfig(cfg config.ResponsesConfig) error {
	switch cfg.StateMode {
	case "", "replay", "previous_response_id", "conversation":
	default:
		return fmt.Errorf("responses.stateMode %q is invalid; use replay, previous_response_id, or conversation", cfg.StateMode)
	}
	if cfg.StateMode == "conversation" && strings.TrimSpace(cfg.Conversation) == "" {
		return fmt.Errorf("responses.conversation is required when stateMode is conversation")
	}
	if cfg.StateMode != "" && cfg.StateMode != "conversation" && strings.TrimSpace(cfg.Conversation) != "" {
		return fmt.Errorf("responses.conversation requires stateMode conversation")
	}

	switch cfg.Truncation {
	case "", "auto", "disabled":
	default:
		return fmt.Errorf("responses.truncation %q is invalid; use auto or disabled", cfg.Truncation)
	}
	switch cfg.ReasoningSummary {
	case "", "auto", "concise", "detailed", "none", "off":
	default:
		return fmt.Errorf("responses.reasoningSummary %q is invalid; use auto, concise, detailed, none, or off", cfg.ReasoningSummary)
	}

	seenInclude := make(map[string]struct{}, len(cfg.Include))
	for _, value := range cfg.Include {
		value = strings.TrimSpace(value)
		if value == "" {
			return fmt.Errorf("responses.include cannot contain empty values")
		}
		if _, ok := seenInclude[value]; ok {
			return fmt.Errorf("responses.include contains duplicate value %q", value)
		}
		seenInclude[value] = struct{}{}
	}
	if strings.TrimSpace(cfg.ServiceTier) != cfg.ServiceTier {
		return fmt.Errorf("responses.serviceTier must not contain leading or trailing whitespace")
	}

	structured := cfg.StructuredOutput
	if len(structured.Schema) > 0 {
		if !json.Valid(structured.Schema) {
			return fmt.Errorf("responses.structuredOutput.schema must be valid JSON")
		}
	} else if structured.Name != "" || structured.Description != "" || structured.Strict != nil {
		return fmt.Errorf("responses.structuredOutput.schema is required when structured output is configured")
	}
	if structured.Strict != nil && *structured.Strict && len(structured.Schema) == 0 {
		return fmt.Errorf("responses.structuredOutput.strict requires a schema")
	}

	if cfg.ToolControl.MaxCalls < 0 {
		return fmt.Errorf("responses.toolControl.maxCalls cannot be negative")
	}
	switch strings.ToLower(strings.TrimSpace(cfg.ToolControl.Choice)) {
	case "", "auto", "none", "required":
	default:
		if strings.TrimSpace(cfg.ToolControl.Choice) == "" {
			return fmt.Errorf("responses.toolControl.choice cannot be empty")
		}
	}
	return nil
}

func (p *Provider) validateResponsesCapabilities(model *provider.Model, params provider.ChatParams) error {
	cfg := p.responsesConfig
	if cfg == nil {
		return nil
	}
	if cfg.store != nil && !supportsStore(model) {
		return fmt.Errorf("Responses capability error: model %q does not support configured store", modelID(model))
	}
	if cfg.promptCacheKey != "" && !supportsPromptCacheKey(model) {
		return fmt.Errorf("Responses capability error: model %q does not support configured prompt_cache_key", modelID(model))
	}
	if cfg.promptCacheRetention != "" && !supportsPromptCacheRetention(model) {
		return fmt.Errorf("Responses capability error: model %q does not support configured prompt_cache_retention", modelID(model))
	}
	if cfg.reasoningSummary != "" && !supportsReasoningSummary(model) {
		return fmt.Errorf("Responses capability error: model %q does not support configured reasoning summary", modelID(model))
	}
	if cfg.structuredOutput != nil && cfg.structuredOutput.Strict != nil &&
		*cfg.structuredOutput.Strict && !supportsStrictMode(model) {
		return fmt.Errorf("Responses capability error: model %q does not support strict structured output", modelID(model))
	}
	if cfg.stateMode == "conversation" && (cfg.store == nil || !*cfg.store) {
		return fmt.Errorf("Responses state mode conversation requires store=true")
	}
	if params.ResponseOptions != nil && params.ResponseOptions.StructuredOutput != nil &&
		params.ResponseOptions.StructuredOutput.Strict && !supportsStrictMode(model) {
		return fmt.Errorf("Responses capability error: model %q does not support strict structured output", modelID(model))
	}
	return nil
}

func modelID(model *provider.Model) string {
	if model == nil || model.ID == "" {
		return "unknown"
	}
	return model.ID
}

func supportsStore(model *provider.Model) bool {
	if model != nil && model.Compat != nil && model.Compat.SupportsStore != nil {
		return *model.Compat.SupportsStore
	}
	return true
}

func supportsStrictMode(model *provider.Model) bool {
	if model != nil && model.Compat != nil && model.Compat.SupportsStrictMode != nil {
		return *model.Compat.SupportsStrictMode
	}
	return true
}

func responsesConfigTextFormat(cfg config.ResponsesStructuredOutputConfig) *responsesTextFormat {
	if len(cfg.Schema) == 0 && cfg.Name == "" && cfg.Description == "" && cfg.Strict == nil {
		return nil
	}
	return &responsesTextFormat{
		Type:        "json_schema",
		Name:        cfg.Name,
		Description: cfg.Description,
		Strict:      cloneBoolPtr(cfg.Strict),
		Schema:      cloneRawMessage(cfg.Schema),
	}
}

func responsesConfigToolChoice(choice string) interface{} {
	choice = strings.TrimSpace(choice)
	switch choice {
	case "":
		return nil
	case "auto", "none", "required":
		return choice
	default:
		return map[string]any{"type": "function", "name": choice}
	}
}

func responsesConfigHostedTools(cfg config.ResponsesHostedToolsConfig) []responsesTool {
	result := make([]responsesTool, 0, 6+len(cfg.RemoteMCP))
	appendConfig := func(values map[string]any, defaultType string) {
		if len(values) == 0 {
			return
		}
		result = append(result, responsesTool{
			Type:  hostedToolType(values, defaultType),
			Extra: values,
		})
	}
	appendConfig(cfg.WebSearch, "web_search")
	appendConfig(cfg.FileSearch, "file_search")
	appendConfig(cfg.CodeInterpreter, "code_interpreter")
	appendConfig(cfg.ComputerUse, "computer_use_preview")
	appendConfig(cfg.ImageGeneration, "image_generation")
	for _, values := range cfg.RemoteMCP {
		if len(values) == 0 {
			continue
		}
		result = append(result, responsesTool{
			Type:  hostedToolType(values, "mcp"),
			Extra: values,
		})
	}
	return result
}

func hostedToolType(values map[string]any, fallback string) string {
	if value, ok := values["type"].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

func (p *Provider) mergeResponsesTools(explicit []responsesTool) []responsesTool {
	if p.responsesConfig == nil || len(p.responsesConfig.hostedTools) == 0 {
		return explicit
	}
	result := append([]responsesTool(nil), explicit...)
	seen := make(map[string]struct{}, len(result))
	for _, tool := range result {
		seen[tool.Type] = struct{}{}
	}
	for _, tool := range p.responsesConfig.hostedTools {
		if _, ok := seen[tool.Type]; ok {
			continue
		}
		result = append(result, tool)
		seen[tool.Type] = struct{}{}
	}
	return result
}
