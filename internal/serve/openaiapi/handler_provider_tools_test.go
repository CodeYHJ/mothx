package openaiapi

import (
	"testing"
)

func TestModelsEndpoint(t *testing.T) {
	tests := []struct {
		base string
		want string
	}{
		{"https://api.example.test/v1", "https://api.example.test/v1/models"},
		{"https://api.example.test/v1/", "https://api.example.test/v1/models"},
		{"https://api.example.test/v1/models", "https://api.example.test/v1/models"},
	}
	for _, tt := range tests {
		got, err := modelsEndpoint(tt.base)
		if err != nil {
			t.Errorf("modelsEndpoint(%q): %v", tt.base, err)
			continue
		}
		if got != tt.want {
			t.Errorf("modelsEndpoint(%q) = %q, want %q", tt.base, got, tt.want)
		}
	}
	for _, base := range []string{"", "ftp://example.test/v1", "/relative"} {
		if _, err := modelsEndpoint(base); err == nil {
			t.Errorf("modelsEndpoint(%q) succeeded, want error", base)
		}
	}
}

func TestParseDiscoveredModels(t *testing.T) {
	got, err := parseDiscoveredModels([]byte(`{"models":[{"name":"models/gemini-2.0-flash","displayName":"Gemini 2.0 Flash"},{"name":"models/gemini-2.0-flash"},{"id":"gpt-4o","context_length":128000,"max_output_tokens":4096,"input_modalities":["text","image"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d models, want 2", len(got))
	}
	if got[0].ID != "gemini-2.0-flash" || got[0].Name != "Gemini 2.0 Flash" {
		t.Fatalf("unexpected Gemini model: %#v", got[0])
	}
	if got[1].ID != "gpt-4o" || got[1].ContextWindow != 128000 || got[1].MaxTokens != 4096 || len(got[1].Input) != 2 {
		t.Fatalf("unexpected OpenAI model: %#v", got[1])
	}
}
