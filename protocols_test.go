package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResponsesToChatString(t *testing.T) {
	req := ResponsesRequest{Model: "mimo-v2.5", Input: json.RawMessage(`"hello"`), Stream: false}
	chat, err := responsesToChat(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(chat.Messages) != 1 || chat.Messages[0].Role != "user" {
		t.Fatalf("expected 1 user message, got %+v", chat.Messages)
	}
	if chat.Messages[0].Content != "hello" {
		t.Fatalf("content: %v", chat.Messages[0].Content)
	}
}

func TestResponsesToChatBlocks(t *testing.T) {
	input := json.RawMessage(`[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
		{"type":"function_call","call_id":"call_1","name":"f","arguments":"{}"},
		{"type":"function_call_output","call_id":"call_1","output":"42"}
	]`)
	req := ResponsesRequest{Model: "mimo-v2.5", Input: input}
	chat, err := responsesToChat(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(chat.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d: %+v", len(chat.Messages), chat.Messages)
	}
	if chat.Messages[0].Content != "hi" {
		t.Fatalf("content: %v", chat.Messages[0].Content)
	}
	if chat.Messages[1].Role != "assistant" || len(chat.Messages[1].ToolCalls) == 0 {
		t.Fatalf("expected assistant tool call, got %+v", chat.Messages[1])
	}
	if chat.Messages[2].Role != "tool" || chat.Messages[2].ToolCallID != "call_1" {
		t.Fatalf("expected tool output, got %+v", chat.Messages[2])
	}
}

func TestClaudeToOpenAI(t *testing.T) {
	sys := json.RawMessage(`[{"type":"text","text":"be helpful"}]`)
	content := json.RawMessage(`[{"type":"text","text":"hi"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"xxx"}}]`)
	req := ClaudeRequest{
		Model:     "mimo-v2.5",
		MaxTokens: 512,
		System:    sys,
		Messages:  []ClaudeMessage{{Role: "user", Content: content}},
	}
	chat := claudeToOpenAI(req)
	if len(chat.Messages) != 2 {
		t.Fatalf("expected 2 messages (system+user), got %d: %+v", len(chat.Messages), chat.Messages)
	}
	if chat.Messages[0].Role != "system" || chat.Messages[0].Content != "be helpful" {
		t.Fatalf("system: %+v", chat.Messages[0])
	}
	// image block dropped, text kept
	if chat.Messages[1].Content != "hi" {
		t.Fatalf("user content: %v", chat.Messages[1].Content)
	}
	if chat.MaxTokens == nil || *chat.MaxTokens != 512 {
		t.Fatalf("max_tokens: %v", chat.MaxTokens)
	}
}

func TestAnthropicSSEToOpenAI(t *testing.T) {
	sse := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hel\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"lo\"}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	out, err := anthropicSSEToOpenAI(strings.NewReader(sse), "mimo-v2.5")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, `"content":"Hel"`) || !strings.Contains(s, `"content":"lo"`) {
		t.Fatalf("missing text deltas: %s", s)
	}
	if !strings.Contains(s, `"finish_reason":"stop"`) {
		t.Fatalf("missing finish_reason: %s", s)
	}
	if !strings.Contains(s, "[DONE]") {
		t.Fatalf("missing [DONE]: %s", s)
	}
}

func TestAnthropicSSEToOpenAIWithoutStop(t *testing.T) {
	sse := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"x\"}}\n\n"
	out, err := anthropicSSEToOpenAI(strings.NewReader(sse), "mimo-v2.5")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "[DONE]") {
		t.Fatalf("missing [DONE] fallback: %s", string(out))
	}
}

func TestFlattenClaudeContent(t *testing.T) {
	raw := json.RawMessage(`[{"type":"text","text":"a"},{"type":"thinking","thinking":"t"},{"type":"image","source":{"type":"base64","data":"x"}}]`)
	got := flattenClaudeContent(raw)
	if got != "at" {
		t.Fatalf("flatten: %q", got)
	}
	if got := flattenClaudeContent(json.RawMessage(`"plain"`)); got != "plain" {
		t.Fatalf("plain: %q", got)
	}
}

func TestOpenAIToAnthropicMessage(t *testing.T) {
	openAI := []byte(`{
		"id":"gen-1","object":"chat.completion","created":1786000000,"model":"mimo-v2.5-free",
		"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"hello world"}}],
		"usage":{"prompt_tokens":10,"completion_tokens":5}
	}`)
	out, err := openaiToAnthropicMessage(openAI)
	if err != nil {
		t.Fatal(err)
	}
	var msg map[string]any
	if err := json.Unmarshal(out, &msg); err != nil {
		t.Fatal(err)
	}
	if msg["type"] != "message" || msg["role"] != "assistant" {
		t.Fatalf("envelope: %+v", msg)
	}
	content, _ := msg["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content blocks: %+v", content)
	}
	block := content[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "hello world" {
		t.Fatalf("block: %+v", block)
	}
	if msg["stop_reason"] != "end_turn" {
		t.Fatalf("stop_reason: %v", msg["stop_reason"])
	}
	usage := msg["usage"].(map[string]any)
	if usage["input_tokens"] != float64(10) || usage["output_tokens"] != float64(5) {
		t.Fatalf("usage: %+v", usage)
	}
}

func TestOpenAIToAnthropicMaxTokens(t *testing.T) {
	openAI := []byte(`{
		"id":"gen-2","object":"chat.completion","model":"mimo-v2.5-free",
		"choices":[{"index":0,"finish_reason":"length","message":{"role":"assistant","content":"cut off"}}],
		"usage":{"prompt_tokens":1,"completion_tokens":100}
	}`)
	out, err := openaiToAnthropicMessage(openAI)
	if err != nil {
		t.Fatal(err)
	}
	var msg map[string]any
	_ = json.Unmarshal(out, &msg)
	if msg["stop_reason"] != "max_tokens" {
		t.Fatalf("stop_reason should be max_tokens, got %v", msg["stop_reason"])
	}
}

func TestAnthropicStreamWriterEvents(t *testing.T) {
	var out strings.Builder
	flusher := &nopFlusher{}
	aw := newAnthropicStreamWriter(&out, flusher, "mimo-v2.5-free")

	chunk1 := `{"id":"gen-3","model":"mimo-v2.5-free","choices":[{"index":0,"delta":{"content":"Hel"}}]}`
	chunk2 := `{"id":"gen-3","model":"mimo-v2.5-free","choices":[{"index":0,"delta":{"content":"lo"}}]}`
	chunk3 := `{"id":"gen-3","model":"mimo-v2.5-free","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`

	for _, c := range []string{chunk1, chunk2, chunk3} {
		if err := aw.handleChunk(c); err != nil {
			t.Fatal(err)
		}
	}

	s := out.String()
	for _, want := range []string{
		"event: message_start",
		"event: content_block_start",
		`"text":"Hel"`,
		`"text":"lo"`,
		"event: content_block_stop",
		`"stop_reason":"end_turn"`,
		"event: message_stop",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
}

type nopFlusher struct{}

func (n *nopFlusher) Flush() {}
