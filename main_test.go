package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExtractUpstreamAuth(t *testing.T) {
	cases := []struct {
		name  string
		auth  string
		want  AuthMode
	}{
		{"none", "", AuthRoutePublic},
		{"public", "Bearer public", AuthRoutePublic},
		{"placeholder", "Bearer no-key-required", AuthRoutePublic},
		{"anthropic key rejected", "Bearer sk-ant-abc123", AuthRoutePublic},
		{"auto key", "Bearer sk-opencode123456789", AuthRouteAuto},
		{"zen prefix", "Bearer zen:sk-opencode123456789", AuthRouteZen},
		{"go prefix", "Bearer go:sk-opencode123456789", AuthRouteGo},
		{"short key invalid", "Bearer sk-short", AuthRoutePublic},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
			if c.auth != "" {
				r.Header.Set("Authorization", c.auth)
			}
			got := extractUpstreamAuth(r)
			if got.Mode != c.want {
				t.Fatalf("auth %q: got mode %v, want %v", c.auth, got.Mode, c.want)
			}
		})
	}
}

func TestResolveModelAlias(t *testing.T) {
	applyConfig(AppConfig{ModelAlias: map[string]string{"mimo-v2.5": "mimo-v2.5-free"}})
	if got := resolveModel("mimo-v2.5"); got != "mimo-v2.5-free" {
		t.Fatalf("alias resolve: got %q", got)
	}
	// unknown model passes through untouched
	if got := resolveModel("some-model"); got != "some-model" {
		t.Fatalf("passthrough: got %q", got)
	}
}

func TestResolveModelFreeSuffix(t *testing.T) {
	modelMu.Lock()
	modelsCache = []ModelInfo{{ID: "mimo-v2.5-free"}}
	modelMu.Unlock()
	// "mimo-v2.5" not in catalog but "mimo-v2.5-free" is -> auto append
	if got := resolveModel("mimo-v2.5"); got != "mimo-v2.5-free" {
		t.Fatalf("auto -free: got %q", got)
	}
}

func TestIsValidOpenCodeKey(t *testing.T) {
	if !isValidOpenCodeKey("sk-abcdefghijklmnop") {
		t.Fatal("valid sk- key rejected")
	}
	if isValidOpenCodeKey("sk-ant-abcdef") {
		t.Fatal("anthropic key accepted")
	}
	if isValidOpenCodeKey("sk-short") {
		t.Fatal("short key accepted")
	}
}

func TestPublicFacingID(t *testing.T) {
	if got := publicFacingID("mimo-v2.5-free"); got != "mimo-v2.5" {
		t.Fatalf("publicFacingID: got %q", got)
	}
	if got := publicFacingID("grok-4.20-fast"); got != "grok-4.20-fast" {
		t.Fatalf("publicFacingID passthrough: got %q", got)
	}
}

func TestIsFreeModel(t *testing.T) {
	if !isFreeModel("mimo-v2.5-free") {
		t.Fatal("free model not detected")
	}
	if isFreeModel("grok-4.20-fast") {
		t.Fatal("non-free model flagged free")
	}
}

func TestFixToolCallGaps(t *testing.T) {
	msgs := []Message{
		{Role: "assistant", Content: "let me check", ToolCalls: []any{
			map[string]any{"id": "call_1", "function": map[string]any{"name": "f", "arguments": "{}"}},
		}},
	}
	fixed := fixToolCallGaps(msgs)
	if len(fixed) != 2 {
		t.Fatalf("expected 2 messages (assistant + tool stub), got %d", len(fixed))
	}
	if fixed[1].Role != "tool" || fixed[1].ToolCallID != "call_1" {
		t.Fatalf("tool stub wrong: %+v", fixed[1])
	}
}

func TestStripPrefix(t *testing.T) {
	if got := stripPrefix("data: {\"a\":1}"); got != `{"a":1}` {
		t.Fatalf("stripPrefix: got %q", got)
	}
	if got := stripPrefix(": keep-alive"); got != ": keep-alive" {
		t.Fatalf("keep-alive should pass through: got %q", got)
	}
}

func TestBuildUpstreamBody(t *testing.T) {
	applyConfig(AppConfig{ReasoningEffortMap: map[string]string{"medium": "medium"}})
	effort := "medium"
	req := &OpenAIRequest{
		Model:           "mimo-v2.5-free",
		Messages:        []Message{{Role: "user", Content: "hi"}},
		Stream:          false,
		ReasoningEffort: effort,
	}
	body := buildUpstreamBody(req)
	s := string(body)
	if !strings.Contains(s, `"model":"mimo-v2.5-free"`) {
		t.Fatalf("body missing model: %s", s)
	}
	if !strings.Contains(s, `"reasoning_effort":"medium"`) {
		t.Fatalf("body missing reasoning_effort: %s", s)
	}
	if strings.Contains(s, `"thinking"`) {
		t.Fatalf("thinking should not be set without explicit request: %s", s)
	}
}
