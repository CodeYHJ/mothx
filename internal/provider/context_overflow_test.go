package provider

import (
	"errors"
	"testing"
)

func TestIsContextOverflowError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"openai code", errors.New(`API error 400: {"error":{"code":"context_length_exceeded"}}`), true},
		{"openai message", errors.New("This model's maximum context length is 128000 tokens"), true},
		{"anthropic", errors.New("prompt is too long: 213462 tokens > 200000 maximum"), true},
		{"moonshot", errors.New("Invalid request: the request exceeds the maximum length limit of the model"), true},
		{"kimi", errors.New("total tokens of image and text exceed max message tokens"), true},
		{"context window", errors.New("request exceeds the model's context window"), true},
		{"rate limit is not overflow", errors.New("API error 429: rate limit exceeded, please retry later"), false},
		{"auth error", errors.New("API error 401: invalid api key"), false},
		{"generic stream failure", errors.New("responses stream failed"), false},
		{"network error", errors.New("connection reset by peer"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsContextOverflowError(tt.err); got != tt.want {
				t.Errorf("IsContextOverflowError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
