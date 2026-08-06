package openai

import "time"

// hostedToolDescriptor is deliberately scoped to the OpenAI Responses codec.
// Other providers can expose the same provider-neutral observations without
// inheriting OpenAI request or lifecycle semantics.
type hostedToolDescriptor struct {
	Type            string
	Capability      string
	RequestTypes    []string
	ExecutionMode   string
	ResumePolicy    string
	AttachmentKinds []string
}

type responsesHostedPolicy struct {
	MaxCalls    int
	MaxCallsSet bool
	Timeout     time.Duration
	Configured  bool
}

// responsesHostedToolRegistry is the single source for the hosted tools that
// this codec understands. Unknown upstream types remain forward-compatible:
// they can still be archived as canonical items, but do not acquire guessed
// execution or download behavior.
var responsesHostedToolRegistry = []hostedToolDescriptor{
	{Type: "web_search_call", Capability: "web_search", RequestTypes: []string{"web_search", "web_search_preview"}, ExecutionMode: "hosted", ResumePolicy: "poll", AttachmentKinds: []string{"citation"}},
	{Type: "file_search_call", Capability: "file_search", RequestTypes: []string{"file_search"}, ExecutionMode: "hosted", ResumePolicy: "poll", AttachmentKinds: []string{"citation", "file"}},
	{Type: "code_interpreter_call", Capability: "code_interpreter", RequestTypes: []string{"code_interpreter"}, ExecutionMode: "hosted", ResumePolicy: "poll", AttachmentKinds: []string{"artifact", "file"}},
	{Type: "image_generation_call", Capability: "image_generation", RequestTypes: []string{"image_generation"}, ExecutionMode: "hosted", ResumePolicy: "poll", AttachmentKinds: []string{"image"}},
	{Type: "mcp_call", Capability: "remote_mcp", RequestTypes: []string{"mcp"}, ExecutionMode: "hosted", ResumePolicy: "reconnect", AttachmentKinds: nil},
	{Type: "mcp_call_output", Capability: "remote_mcp", RequestTypes: []string{"mcp"}, ExecutionMode: "hosted", ResumePolicy: "reconnect", AttachmentKinds: nil},
}

func hostedToolDescriptorForType(itemType string) (hostedToolDescriptor, bool) {
	for _, descriptor := range responsesHostedToolRegistry {
		if descriptor.Type == itemType {
			return descriptor, true
		}
	}
	return hostedToolDescriptor{}, false
}

func hostedToolTypes() []string {
	types := make([]string, 0, len(responsesHostedToolRegistry))
	for _, descriptor := range responsesHostedToolRegistry {
		types = append(types, descriptor.Type)
	}
	return types
}

func hostedRequestCapabilities() map[string]bool {
	capabilities := make(map[string]bool)
	for _, descriptor := range responsesHostedToolRegistry {
		for _, requestType := range descriptor.RequestTypes {
			capabilities[requestType] = true
		}
	}
	return capabilities
}
