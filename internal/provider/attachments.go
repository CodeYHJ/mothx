package provider

import (
	"context"
	"fmt"
)

// AttachmentContent is an optional provider-specific download result. It is
// deliberately not part of Provider so vendors without file retrieval remain
// unaffected.
type AttachmentContent struct {
	Data      []byte
	MediaType string
	Filename  string
}

// AttachmentResolver is implemented only by providers that can resolve an
// archived provider reference into bytes. Callers must authorize the ref
// against the session archive before invoking it.
type AttachmentResolver interface {
	ResolveAttachment(context.Context, string) (AttachmentContent, error)
}

// AttachmentMetadataResolver is an optional refinement for providers whose
// file reference needs lifecycle context, such as a Code Interpreter
// container ID. Callers authorize the full archived Attachment first.
type AttachmentMetadataResolver interface {
	ResolveAttachmentWithMetadata(context.Context, Attachment) (AttachmentContent, error)
}

// ValidateAttachmentReferenceForResolver validates an opaque provider ref
// before it is interpolated into a provider-owned path.
func ValidateAttachmentReferenceForResolver(ref string) error {
	if ref == "" || len(ref) > 128 {
		return fmt.Errorf("invalid attachment reference")
	}
	for _, char := range ref {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_' || char == '-' {
			continue
		}
		return fmt.Errorf("invalid attachment reference")
	}
	return nil
}
