package openai

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/startvibecoding/mothx/internal/provider"
)

const (
	responsesMaxCanonicalItemBytes = 128 * 1024
	responsesMaxMetadataBytes      = 16 * 1024
)

var (
	errResponsesStop  = errors.New("responses stream reached terminal event")
	errResponsesAbort = errors.New("responses stream aborted")
)

type responsesSSEFrame struct {
	Event    string
	ID       string
	Data     string
	Sequence int64
}

// decodeResponsesSSE implements the SSE framing rules used by Responses API.
// It deliberately leaves JSON decoding to the schema-aware event normalizer.
func decodeResponsesSSE(r io.Reader, fn func(responsesSSEFrame) error) error {
	reader := bufio.NewReader(r)
	var (
		eventName string
		eventID   string
		data      []string
		sequence  int64
	)

	dispatch := func() error {
		if len(data) == 0 {
			eventName = ""
			eventID = ""
			return nil
		}
		sequence++
		frame := responsesSSEFrame{
			Event:    eventName,
			ID:       eventID,
			Data:     strings.Join(data, "\n"),
			Sequence: sequence,
		}
		eventName = ""
		eventID = ""
		data = nil
		return fn(frame)
	}

	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimSuffix(line, "\n")
			line = strings.TrimSuffix(line, "\r")
			if line == "" {
				if dispatchErr := dispatch(); dispatchErr != nil {
					return dispatchErr
				}
			} else if strings.HasPrefix(line, ":") {
				// SSE comments are keep-alive frames.
				continue
			} else {
				field, value := line, ""
				if colon := strings.IndexByte(line, ':'); colon >= 0 {
					field, value = line[:colon], line[colon+1:]
					if strings.HasPrefix(value, " ") {
						value = value[1:]
					}
				}
				switch field {
				case "event":
					eventName = value
				case "id":
					eventID = value
				case "data":
					data = append(data, value)
					// A number of OpenAI-compatible gateways emit one
					// complete JSON event per line without the blank-line
					// separator required by SSE. Preserve that legacy
					// behavior while still supporting multi-line data frames.
					if eventName == "" && eventID == "" {
						joined := strings.Join(data, "\n")
						if joined == "[DONE]" || json.Valid([]byte(joined)) {
							if dispatchErr := dispatch(); dispatchErr != nil {
								return dispatchErr
							}
						}
					}
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return dispatch()
			}
			return err
		}
	}
}

type responsesResponseItem struct {
	ID          string
	Type        string
	Status      string
	OutputIndex int
	CallID      string
	Name        string
	Arguments   json.RawMessage
	Canonical   json.RawMessage
}

type responsesNormalizedResponse struct {
	ID                 string
	Status             string
	PreviousResponseID string
	ConversationID     string
	IncompleteReason   string
	Usage              *responsesUsage
	Error              *responsesError
	Items              []*responsesResponseItem
	UnknownItems       int
}

type responsesNormalizer struct {
	items         map[string]*responsesResponseItem
	itemOrder     []string
	argumentBytes map[string]*strings.Builder
	response      responsesNormalizedResponse
}

func newResponsesNormalizer() *responsesNormalizer {
	return &responsesNormalizer{
		items:         make(map[string]*responsesResponseItem),
		argumentBytes: make(map[string]*strings.Builder),
	}
}

func (n *responsesNormalizer) apply(event responsesSSEEvent, raw []byte) error {
	if event.Response != nil {
		n.applyResponse(event.Response)
	}

	switch event.Type {
	case "response.output_item.added", "response.output_item.done":
		if event.Item == nil {
			return fmt.Errorf("responses event %q is missing item", event.Type)
		}
		item, err := n.upsertItem(event.Item, event.OutputIndex, responsesEventItemRaw(raw))
		if err != nil {
			return err
		}
		if event.Type == "response.output_item.done" && item.Type == "function_call" {
			if len(item.Arguments) == 0 {
				item.Arguments = n.arguments(event.Item.ID, event.OutputIndex)
			}
		}
	case "response.function_call_arguments.delta":
		key, err := responsesEventItemKey(event.ItemID, event.OutputIndex)
		if err != nil {
			return err
		}
		if event.Delta != "" {
			if n.argumentBytes[key] == nil {
				n.argumentBytes[key] = &strings.Builder{}
			}
			n.argumentBytes[key].WriteString(event.Delta)
		}
		item := n.ensureItem(event.ItemID, event.OutputIndex)
		if item.Type == "" {
			item.Type = "function_call"
		}
		item.Arguments = n.arguments(event.ItemID, event.OutputIndex)
	case "response.function_call_arguments.done":
		key, err := responsesEventItemKey(event.ItemID, event.OutputIndex)
		if err != nil {
			return err
		}
		if len(event.Arguments) > 0 {
			if n.argumentBytes[key] == nil {
				n.argumentBytes[key] = &strings.Builder{}
			}
			n.argumentBytes[key].Reset()
			n.argumentBytes[key].WriteString(responsesArgumentsText(event.Arguments))
		}
		item := n.ensureItem(event.ItemID, event.OutputIndex)
		if item.Type == "" {
			item.Type = "function_call"
		}
		item.Arguments = n.arguments(event.ItemID, event.OutputIndex)
	}

	if event.Type == "response.completed" || event.Type == "response.incomplete" ||
		event.Type == "response.failed" {
		n.applyResponse(event.Response)
	}
	return nil
}

func (n *responsesNormalizer) applyResponse(response *responsesCompletedObject) {
	if response == nil {
		return
	}
	n.response.ID = response.ID
	n.response.Status = response.Status
	n.response.PreviousResponseID = response.PreviousResponseID
	n.response.ConversationID = responsesConversationID(response)
	n.response.Usage = response.Usage
	n.response.Error = response.Error
	if response.IncompleteDetails != nil {
		n.response.IncompleteReason = response.IncompleteDetails.Reason
	}
	for index, rawItem := range response.Output {
		item, err := decodeResponsesOutputItem(rawItem, index)
		if err != nil {
			continue
		}
		n.upsertDecodedItem(item)
	}
}

func responsesConversationID(response *responsesCompletedObject) string {
	if response == nil {
		return ""
	}
	if response.ConversationID != "" {
		return response.ConversationID
	}
	if len(response.ConversationRaw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(response.ConversationRaw, &text) == nil {
		return text
	}
	var value struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(response.ConversationRaw, &value) == nil {
		return value.ID
	}
	return ""
}

func (n *responsesNormalizer) upsertItem(item *responsesOutputItem, outputIndex int, raw []byte) (*responsesResponseItem, error) {
	decoded := &responsesResponseItem{
		ID:          item.ID,
		Type:        item.Type,
		Status:      item.Status,
		OutputIndex: outputIndex,
		CallID:      item.CallID,
		Name:        item.Name,
		Arguments:   responsesArgumentsRaw(item.Arguments),
		Canonical:   canonicalResponsesJSON(raw, responsesMaxCanonicalItemBytes),
	}
	n.upsertDecodedItem(decoded)
	return decoded, nil
}

func (n *responsesNormalizer) upsertDecodedItem(item *responsesResponseItem) {
	key := responsesToolKey(item.ID, item.OutputIndex)
	if existing := n.items[key]; existing != nil {
		if item.ID != "" {
			existing.ID = item.ID
		}
		if item.Type != "" {
			existing.Type = item.Type
		}
		if item.Status != "" {
			existing.Status = item.Status
		}
		if item.CallID != "" {
			existing.CallID = item.CallID
		}
		if item.Name != "" {
			existing.Name = item.Name
		}
		if len(item.Arguments) > 0 {
			existing.Arguments = cloneRawMessage(item.Arguments)
		}
		if len(item.Canonical) > 0 {
			existing.Canonical = cloneRawMessage(item.Canonical)
		}
		return
	}
	n.items[key] = item
	n.itemOrder = append(n.itemOrder, key)
	n.response.Items = append(n.response.Items, item)
	if !isKnownResponsesItemType(item.Type) {
		n.response.UnknownItems++
	}
}

func (n *responsesNormalizer) ensureItem(itemID string, outputIndex int) *responsesResponseItem {
	key := responsesToolKey(itemID, outputIndex)
	if item := n.items[key]; item != nil {
		return item
	}
	item := &responsesResponseItem{ID: itemID, OutputIndex: outputIndex}
	n.upsertDecodedItem(item)
	return item
}

func (n *responsesNormalizer) arguments(itemID string, outputIndex int) json.RawMessage {
	key := responsesToolKey(itemID, outputIndex)
	if buffer := n.argumentBytes[key]; buffer != nil {
		return json.RawMessage(buffer.String())
	}
	return nil
}

func (n *responsesNormalizer) toolCalls() []providerToolCall {
	calls := make([]providerToolCall, 0)
	for _, key := range n.itemOrder {
		item := n.items[key]
		if item == nil || item.Type != "function_call" {
			continue
		}
		arguments := cloneRawMessage(item.Arguments)
		if len(arguments) == 0 {
			arguments = n.arguments(item.ID, item.OutputIndex)
		}
		calls = append(calls, providerToolCall{
			Key:       key,
			ID:        item.CallID,
			ItemID:    item.ID,
			Name:      item.Name,
			Arguments: arguments,
		})
	}
	return calls
}

type providerToolCall struct {
	Key       string
	ID        string
	ItemID    string
	Name      string
	Arguments json.RawMessage
}

func (n *responsesNormalizer) metadata() map[string]any {
	metadata := map[string]any{
		"itemCount": len(n.response.Items),
	}
	if n.response.UnknownItems > 0 {
		metadata["unknownItemCount"] = n.response.UnknownItems
	}
	if n.response.ID != "" {
		metadata["responseId"] = n.response.ID
	}
	if n.response.Status != "" {
		metadata["responseStatus"] = n.response.Status
	}
	if n.response.PreviousResponseID != "" {
		metadata["previousResponseId"] = n.response.PreviousResponseID
	}
	if n.response.ConversationID != "" {
		metadata["conversationId"] = n.response.ConversationID
	}
	if n.response.IncompleteReason != "" {
		metadata["incompleteReason"] = n.response.IncompleteReason
	}
	return limitResponsesMetadata(metadata)
}

func (n *responsesNormalizer) attachments() []provider.Attachment {
	seen := make(map[string]struct{})
	var attachments []provider.Attachment
	for _, item := range n.response.Items {
		if item == nil || len(item.Canonical) == 0 {
			continue
		}
		var value any
		if json.Unmarshal(item.Canonical, &value) != nil {
			continue
		}
		collectResponsesAttachments(value, item.ID, "", seen, &attachments)
	}
	return attachments
}

// collectResponsesAttachments only exposes references from known output
// contexts. In particular, it does not turn arbitrary URLs (for example a
// remote MCP server URL) into clickable UI attachments.
func collectResponsesAttachments(value any, itemID, parentType string, seen map[string]struct{}, out *[]provider.Attachment) {
	switch typed := value.(type) {
	case []any:
		for _, nested := range typed {
			collectResponsesAttachments(nested, itemID, parentType, seen, out)
		}
	case map[string]any:
		itemType, _ := typed["type"].(string)
		if itemType == "" {
			itemType = parentType
		}
		lowerType := strings.ToLower(itemType)
		kind := ""
		switch {
		case strings.Contains(lowerType, "file") || strings.Contains(lowerType, "container") || strings.Contains(lowerType, "code_interpreter"):
			kind = "file"
		case strings.Contains(lowerType, "citation") || strings.Contains(lowerType, "search"):
			kind = "citation"
		case strings.Contains(lowerType, "image"):
			kind = "image"
		}
		if kind != "" {
			url, _ := typed["url"].(string)
			if url == "" {
				url, _ = typed["file_url"].(string)
			}
			name, _ := typed["title"].(string)
			if name == "" {
				name, _ = typed["filename"].(string)
			}
			ref, _ := typed["file_id"].(string)
			if ref == "" {
				ref, _ = typed["container_id"].(string)
			}
			if url != "" || ref != "" {
				key := kind + "\x00" + url + "\x00" + ref
				if _, ok := seen[key]; !ok {
					seen[key] = struct{}{}
					*out = append(*out, provider.Attachment{Kind: kind, Name: name, URL: url, ProviderRef: ref})
				}
			}
		}
		for _, nested := range typed {
			collectResponsesAttachments(nested, itemID, itemType, seen, out)
		}
	}
}

func responsesEventItemKey(itemID string, outputIndex int) (string, error) {
	if itemID == "" && outputIndex < 0 {
		return "", fmt.Errorf("responses event is missing item identity")
	}
	return responsesToolKey(itemID, outputIndex), nil
}

func decodeResponsesOutputItem(raw json.RawMessage, outputIndex int) (*responsesResponseItem, error) {
	var item struct {
		ID        string          `json:"id"`
		Type      string          `json:"type"`
		Status    string          `json:"status"`
		CallID    string          `json:"call_id"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, err
	}
	if item.Type == "" {
		return &responsesResponseItem{
			ID:          item.ID,
			OutputIndex: outputIndex,
			Canonical:   canonicalResponsesJSON(raw, responsesMaxCanonicalItemBytes),
		}, nil
	}
	return &responsesResponseItem{
		ID:          item.ID,
		Type:        item.Type,
		Status:      item.Status,
		OutputIndex: outputIndex,
		CallID:      item.CallID,
		Name:        item.Name,
		Arguments:   responsesArgumentsRaw(item.Arguments),
		Canonical:   canonicalResponsesJSON(raw, responsesMaxCanonicalItemBytes),
	}, nil
}

func responsesArgumentsRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return json.RawMessage(text)
	}
	return cloneRawMessage(raw)
}

func responsesArgumentsText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return string(raw)
}

func responsesEventItemRaw(raw []byte) []byte {
	var envelope struct {
		Item json.RawMessage `json:"item"`
	}
	if json.Unmarshal(raw, &envelope) != nil || len(envelope.Item) == 0 {
		return raw
	}
	return envelope.Item
}

func isKnownResponsesItemType(itemType string) bool {
	switch itemType {
	case "message", "reasoning", "function_call", "function_call_output",
		"custom_tool_call", "custom_tool_call_output", "item_reference",
		"web_search_call", "file_search_call", "computer_call",
		"computer_call_output", "code_interpreter_call", "image_generation_call",
		"mcp_call", "mcp_call_output":
		return true
	default:
		return false
	}
}

func canonicalResponsesJSON(raw []byte, maxBytes int) json.RawMessage {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return nil
	}
	value = redactResponsesValue(value)
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	if len(canonical) <= maxBytes {
		return canonical
	}
	sum := sha256.Sum256(canonical)
	truncated, _ := json.Marshal(map[string]any{
		"_truncated": true,
		"sha256":     hex.EncodeToString(sum[:]),
		"bytes":      len(canonical),
	})
	return truncated
}

func redactResponsesValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, nested := range typed {
			if isSensitiveResponsesKey(key) {
				result[key] = "[REDACTED]"
				continue
			}
			result[key] = redactResponsesValue(nested)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for i, nested := range typed {
			result[i] = redactResponsesValue(nested)
		}
		return result
	default:
		return value
	}
}

func isSensitiveResponsesKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), " ", "_"))
	for _, marker := range []string{"authorization", "api_key", "apikey", "access_token", "refresh_token", "secret", "password", "cookie", "credential"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}

func limitResponsesMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	encoded, err := json.Marshal(metadata)
	if err == nil && len(encoded) <= responsesMaxMetadataBytes {
		return metadata
	}
	return map[string]any{"metadataTruncated": true}
}
