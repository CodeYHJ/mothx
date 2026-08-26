package agentruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/startvibecoding/mothx/internal/tools"
)

const maxAttachmentReadBytes = 50 << 10

// ReadAttachmentTool gives an Agent read-only access to file attachments
// explicitly accepted for its current RunInput. It is intentionally separate
// from the workspace read tool: attachment IDs cannot escape into arbitrary
// session storage paths.
type ReadAttachmentTool struct {
	service   *AttachmentService
	sessionID string
	allowed   map[string]SessionAttachment
}

func NewReadAttachmentTool(service *AttachmentService, sessionID string, records []SessionAttachment) *ReadAttachmentTool {
	allowed := make(map[string]SessionAttachment, len(records))
	for _, record := range records {
		allowed[record.ID] = record
	}
	return &ReadAttachmentTool{service: service, sessionID: sessionID, allowed: allowed}
}

func (t *ReadAttachmentTool) Name() string { return "read_attachment" }

func (t *ReadAttachmentTool) Description() string {
	return "Read a file attached to the current user request by its attachmentId. The tool accepts only attachment IDs listed in the user message and never exposes local file paths."
}

func (t *ReadAttachmentTool) PromptSnippet() string {
	return "Read a runtime-managed file attachment by attachment ID"
}

func (t *ReadAttachmentTool) PromptGuidelines() []string {
	return []string{"Use read_attachment for files supplied with the current user request; do not ask for or guess a local attachment path."}
}

func (t *ReadAttachmentTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"attachmentId":{"type":"string","description":"Attachment ID from the current user message"}},"required":["attachmentId"]}`)
}

func (t *ReadAttachmentTool) Execute(ctx context.Context, params map[string]any) (tools.ToolResult, error) {
	if t == nil || t.service == nil {
		return tools.ToolResult{}, fmt.Errorf("attachment reader is unavailable")
	}
	id, _ := params["attachmentId"].(string)
	id = strings.TrimSpace(id)
	record, ok := t.allowed[id]
	if !ok || record.Kind != AttachmentFile {
		return tools.ToolResult{}, fmt.Errorf("attachment %q is not available for this run", id)
	}
	_, reader, err := t.service.Open(ctx, t.sessionID, id)
	if err != nil {
		return tools.ToolResult{}, err
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, maxAttachmentReadBytes+1))
	if err != nil {
		return tools.ToolResult{}, fmt.Errorf("read attachment %s: %w", id, err)
	}
	truncated := len(data) > maxAttachmentReadBytes
	if truncated {
		data = data[:maxAttachmentReadBytes]
	}
	if !utf8.Valid(data) {
		return tools.NewTextToolResult(fmt.Sprintf("Attachment %q (%s, %d bytes) is binary and cannot be rendered as text.", record.Filename, record.MediaType, record.Bytes)), nil
	}
	text := string(data)
	if truncated {
		text += fmt.Sprintf("\n… truncated after %d bytes", maxAttachmentReadBytes)
	}
	return tools.NewTextToolResult(text), nil
}

var _ tools.Tool = (*ReadAttachmentTool)(nil)
