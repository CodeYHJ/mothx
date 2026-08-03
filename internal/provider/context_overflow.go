package provider

import "strings"

// contextOverflowPatterns matches provider error messages that indicate the
// request was rejected because it exceeds the model's context window. Patterns
// are matched case-insensitively as substrings. Keep this list specific so
// rate-limit and quota errors are not misclassified as context overflow.
var contextOverflowPatterns = []string{
	"context_length_exceeded",              // OpenAI error code
	"context length",                       // "maximum context length is N tokens"
	"context window",                       // "exceeds the model's context window"
	"context limit",                        // "context limit exceeded"
	"prompt is too long",                   // Anthropic
	"input is too long",                    // generic
	"maximum length limit",                 // Moonshot/Kimi
	"exceeds the maximum number of tokens", // Gemini
	"max message tokens",                   // Kimi image/text total
	"request entity too large",             // HTTP 413 style
}

// IsContextOverflowError reports whether err looks like a provider-side
// rejection caused by an oversized request (context window exceeded). Such
// errors are recoverable by compacting or truncating the conversation history
// and retrying, unlike auth, rate-limit, or transient network errors.
func IsContextOverflowError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, pattern := range contextOverflowPatterns {
		if strings.Contains(msg, pattern) {
			return true
		}
	}
	return false
}
