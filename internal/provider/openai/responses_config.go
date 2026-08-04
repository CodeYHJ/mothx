package openai

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/startvibecoding/mothx/internal/config"
	"github.com/startvibecoding/mothx/internal/provider"
)

// responsesCapabilities is resolved from immutable model compatibility data
// for each request. Nil flags retain the historical OpenAI-compatible default
// so existing configurations remain valid, while explicit false values gate
// fields before any upstream request is made.
type responsesCapabilities struct {
	supportsResponses          bool
	supportsPreviousResponseID bool
	supportsConversation       bool
	supportsBackground         bool
	supportsStructuredOutput   bool
	supportsServiceTier        bool
	supportsParallelTools      bool
	supportsToolChoice         bool
	hostedTools                map[string]bool
	include                    map[string]bool
}

var defaultResponsesInclude = map[string]bool{
	"reasoning.encrypted_content": true,
	"file_search_call.results":    true,
}

func resolveResponsesCapabilities(model *provider.Model) responsesCapabilities {
	caps := responsesCapabilities{
		supportsResponses:          true,
		supportsPreviousResponseID: true,
		supportsConversation:       true,
		supportsBackground:         true,
		supportsStructuredOutput:   true,
		supportsServiceTier:        true,
		supportsParallelTools:      true,
		supportsToolChoice:         true,
		hostedTools:                map[string]bool{},
		include:                    cloneBoolMap(defaultResponsesInclude),
	}
	if model == nil || model.Compat == nil {
		return caps
	}
	compat := model.Compat
	if compat.SupportsResponses != nil {
		caps.supportsResponses = *compat.SupportsResponses
	}
	if compat.SupportsPreviousResponseID != nil {
		caps.supportsPreviousResponseID = *compat.SupportsPreviousResponseID
	}
	if compat.SupportsConversation != nil {
		caps.supportsConversation = *compat.SupportsConversation
	}
	if compat.SupportsBackground != nil {
		caps.supportsBackground = *compat.SupportsBackground
	}
	if compat.SupportsStructuredOutput != nil {
		caps.supportsStructuredOutput = *compat.SupportsStructuredOutput
	}
	if compat.SupportsServiceTier != nil {
		caps.supportsServiceTier = *compat.SupportsServiceTier
	}
	if compat.SupportsParallelToolCalls != nil {
		caps.supportsParallelTools = *compat.SupportsParallelToolCalls
	}
	if compat.SupportsToolChoice != nil {
		caps.supportsToolChoice = *compat.SupportsToolChoice
	}
	for key, value := range compat.SupportsHostedTools {
		caps.hostedTools[key] = value
	}
	if compat.SupportedInclude != nil {
		caps.include = make(map[string]bool, len(compat.SupportedInclude))
		for _, value := range compat.SupportedInclude {
			value = strings.TrimSpace(value)
			if value != "" {
				caps.include[value] = true
			}
		}
	}
	return caps
}

func validateResponsesConfig(cfg config.ResponsesConfig) error {
	if cfg.HostedTools.ComputerUse != nil {
		return fmt.Errorf("responses.hostedTools.computerUse is not supported")
	}
	if err := validateResponsesRemoteMCP(cfg.HostedTools.RemoteMCP); err != nil {
		return err
	}
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
	switch cfg.ReasoningContext {
	case "", "auto", "current_turn", "all_turns":
	default:
		return fmt.Errorf("responses.reasoningContext %q is invalid; use auto, current_turn, or all_turns", cfg.ReasoningContext)
	}
	switch cfg.ReasoningMode {
	case "", "standard", "pro":
	default:
		return fmt.Errorf("responses.reasoningMode %q is invalid; use standard or pro", cfg.ReasoningMode)
	}
	switch cfg.PromptCacheMode {
	case "", "implicit", "explicit":
	default:
		return fmt.Errorf("responses.promptCacheMode %q is invalid; use implicit or explicit", cfg.PromptCacheMode)
	}
	if cfg.PromptCacheTTL != "" && strings.TrimSpace(cfg.PromptCacheTTL) != cfg.PromptCacheTTL {
		return fmt.Errorf("responses.promptCacheTTL must not contain leading or trailing whitespace")
	}
	if len(cfg.Metadata) > 16 {
		return fmt.Errorf("responses.metadata cannot contain more than 16 entries")
	}
	for key, value := range cfg.Metadata {
		if len(key) == 0 || len(key) > 64 || len(value) > 512 {
			return fmt.Errorf("responses.metadata entry %q exceeds key/value limits", key)
		}
		lower := strings.ToLower(key)
		if strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "authorization") || strings.Contains(lower, "api_key") {
			return fmt.Errorf("responses.metadata key %q is reserved for sensitive data", key)
		}
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
	if structured.Strict != nil && *structured.Strict {
		var schema any
		if err := json.Unmarshal(structured.Schema, &schema); err != nil {
			return fmt.Errorf("decode responses.structuredOutput.schema: %w", err)
		}
		if err := validateStrictResponsesSchema(schema, "$"); err != nil {
			return fmt.Errorf("responses.structuredOutput.schema: %w", err)
		}
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

func validateStrictResponsesSchema(value any, path string) error {
	schema, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s must be an object", path)
	}
	properties, hasProperties := schema["properties"].(map[string]any)
	typeName, _ := schema["type"].(string)
	if path == "$" && typeName != "object" {
		return fmt.Errorf("%s root type must be object when strict=true", path)
	}
	if typeName == "object" || hasProperties {
		if additional, ok := schema["additionalProperties"].(bool); !ok || additional {
			return fmt.Errorf("%s object requires additionalProperties=false when strict=true", path)
		}
		requiredValues, ok := schema["required"].([]any)
		if !ok {
			return fmt.Errorf("%s object requires every property in required when strict=true", path)
		}
		required := make(map[string]struct{}, len(requiredValues))
		for _, rawName := range requiredValues {
			name, ok := rawName.(string)
			if !ok {
				return fmt.Errorf("%s required entries must be strings", path)
			}
			required[name] = struct{}{}
		}
		for name, child := range properties {
			if _, ok := required[name]; !ok {
				return fmt.Errorf("%s.properties.%s must appear in required when strict=true", path, name)
			}
			if err := validateStrictResponsesSchema(child, path+".properties."+name); err != nil {
				return err
			}
		}
	}
	if items, ok := schema["items"]; ok {
		if err := validateStrictResponsesSchema(items, path+".items"); err != nil {
			return err
		}
	}
	if alternatives, ok := schema["anyOf"].([]any); ok {
		for index, child := range alternatives {
			if err := validateStrictResponsesSchema(child, fmt.Sprintf("%s.anyOf[%d]", path, index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateResponsesRemoteMCP(tools []map[string]any) error {
	for index, tool := range tools {
		if len(tool) == 0 {
			continue
		}
		serverURL, _ := tool["server_url"].(string)
		connectorID, _ := tool["connector_id"].(string)
		serverURL = strings.TrimSpace(serverURL)
		connectorID = strings.TrimSpace(connectorID)
		if serverURL == "" && connectorID == "" {
			return fmt.Errorf("responses.hostedTools.remoteMCP[%d] requires server_url or connector_id", index)
		}
		if serverURL == "" {
			continue
		}
		parsed, err := url.Parse(serverURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return fmt.Errorf("responses.hostedTools.remoteMCP[%d].server_url must be a public https URL", index)
		}
		host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
		if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") {
			return fmt.Errorf("responses.hostedTools.remoteMCP[%d].server_url must not target localhost", index)
		}
		if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()) {
			return fmt.Errorf("responses.hostedTools.remoteMCP[%d].server_url must not target a private IP address", index)
		}
	}
	return nil
}

func (p *Provider) validateResponsesCapabilities(model *provider.Model, params provider.ChatParams) error {
	cfg := p.responsesConfig
	if cfg == nil {
		cfg = &responsesConfig{}
	}
	caps := resolveResponsesCapabilities(model)
	if !caps.supportsResponses {
		return fmt.Errorf("Responses capability error: model %q does not support the Responses API", modelID(model))
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
	if cfg.background && !caps.supportsBackground {
		return fmt.Errorf("Responses capability error: model %q does not support background runs", modelID(model))
	}
	if cfg.stateMode == "previous_response_id" && !caps.supportsPreviousResponseID {
		return fmt.Errorf("Responses capability error: model %q does not support previous_response_id", modelID(model))
	}
	if cfg.stateMode == "conversation" && !caps.supportsConversation {
		return fmt.Errorf("Responses capability error: model %q does not support conversation state", modelID(model))
	}
	if cfg.serviceTier != "" && !caps.supportsServiceTier {
		return fmt.Errorf("Responses capability error: model %q does not support service_tier", modelID(model))
	}
	for _, include := range cfg.include {
		if !caps.include[strings.TrimSpace(include)] {
			return fmt.Errorf("Responses capability error: model %q does not support include %q", modelID(model), include)
		}
	}
	for _, tool := range cfg.hostedTools {
		if supported, configured := caps.hostedTools[tool.Type]; configured && !supported {
			return fmt.Errorf("Responses capability error: model %q does not support hosted tool %q", modelID(model), tool.Type)
		}
	}
	for _, tool := range params.Tools {
		if tool.Kind == "custom" {
			if err := validateResponsesCustomTool(tool); err != nil {
				return err
			}
			continue
		}
		if tool.Kind != "hosted" {
			continue
		}
		toolType := provider.HostedWebSearchToolType(tool.ProviderType, tool.Name)
		if toolType == "" {
			toolType = strings.TrimSpace(tool.ProviderType)
		}
		if supported, configured := caps.hostedTools[toolType]; configured && !supported {
			return fmt.Errorf("Responses capability error: model %q does not support hosted tool %q", modelID(model), toolType)
		}
	}
	if cfg.parallelToolCalls != nil && !caps.supportsParallelTools {
		return fmt.Errorf("Responses capability error: model %q does not support parallel tool calls", modelID(model))
	}
	if cfg.toolChoice != nil && !caps.supportsToolChoice {
		return fmt.Errorf("Responses capability error: model %q does not support tool_choice", modelID(model))
	}
	if cfg.structuredOutput != nil && !caps.supportsStructuredOutput {
		return fmt.Errorf("Responses capability error: model %q does not support structured output", modelID(model))
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
	if params.ResponseOptions != nil {
		if params.ResponseOptions.PreviousResponseID != "" && !caps.supportsPreviousResponseID {
			return fmt.Errorf("Responses capability error: model %q does not support previous_response_id", modelID(model))
		}
		if params.ResponseOptions.ParallelTools != nil && !caps.supportsParallelTools {
			return fmt.Errorf("Responses capability error: model %q does not support parallel tool calls", modelID(model))
		}
		if params.ResponseOptions.ToolChoice != nil && !caps.supportsToolChoice {
			return fmt.Errorf("Responses capability error: model %q does not support tool_choice", modelID(model))
		}
		if params.ResponseOptions.StructuredOutput != nil && !caps.supportsStructuredOutput {
			return fmt.Errorf("Responses capability error: model %q does not support structured output", modelID(model))
		}
	}
	return nil
}

func validateResponsesCustomTool(tool provider.ToolDefinition) error {
	if strings.TrimSpace(tool.Name) == "" {
		return fmt.Errorf("Responses custom tool must define a name")
	}
	if len(tool.Format) == 0 {
		return nil
	}
	if !json.Valid(tool.Format) {
		return fmt.Errorf("Responses custom tool %q format must be valid JSON", tool.Name)
	}
	var format struct {
		Type       string `json:"type"`
		Syntax     string `json:"syntax"`
		Definition string `json:"definition"`
	}
	if err := json.Unmarshal(tool.Format, &format); err != nil {
		return fmt.Errorf("decode Responses custom tool %q format: %w", tool.Name, err)
	}
	switch format.Type {
	case "text":
		return nil
	case "grammar":
		if format.Syntax != "lark" && format.Syntax != "regex" {
			return fmt.Errorf("Responses custom tool %q grammar syntax must be lark or regex", tool.Name)
		}
		if strings.TrimSpace(format.Definition) == "" {
			return fmt.Errorf("Responses custom tool %q grammar definition is required", tool.Name)
		}
		return nil
	default:
		return fmt.Errorf("Responses custom tool %q format type must be text or grammar", tool.Name)
	}
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
			Extra: cloneResponsesToolExtra(values),
		})
	}
	appendConfig(cfg.WebSearch, "web_search")
	appendConfig(cfg.FileSearch, "file_search")
	appendConfig(cfg.CodeInterpreter, "code_interpreter")
	appendConfig(cfg.ImageGeneration, "image_generation")
	for _, values := range cfg.RemoteMCP {
		if len(values) == 0 {
			continue
		}
		// Remote MCP tools may trigger external actions in the upstream service.
		// Keep approval explicit by default without mutating the persisted config.
		remote := make(map[string]any, len(values)+1)
		for key, value := range values {
			remote[key] = value
		}
		if _, configured := remote["require_approval"]; !configured {
			remote["require_approval"] = "always"
		}
		result = append(result, responsesTool{
			Type:  hostedToolType(remote, "mcp"),
			Extra: remote,
		})
	}
	return result
}

// cloneResponsesToolExtra keeps the provider's effective descriptor immutable
// after SetResponsesConfig returns. Hosted tool settings use map payloads so a
// shallow struct copy alone would otherwise allow callers to alter live
// request data, including approval policy, between turns.
func cloneResponsesToolExtra(values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]any, len(values))
	for key, value := range values {
		switch typed := value.(type) {
		case map[string]any:
			result[key] = cloneResponsesToolExtra(typed)
		case []map[string]any:
			items := make([]map[string]any, len(typed))
			for i := range typed {
				items[i] = cloneResponsesToolExtra(typed[i])
			}
			result[key] = items
		case []any:
			items := make([]any, len(typed))
			for i := range typed {
				if nested, ok := typed[i].(map[string]any); ok {
					items[i] = cloneResponsesToolExtra(nested)
				} else {
					items[i] = typed[i]
				}
			}
			result[key] = items
		default:
			result[key] = value
		}
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
