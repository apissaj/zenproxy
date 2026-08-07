package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ClaudeRequest mirrors the Anthropic Messages API request shape.
type ClaudeRequest struct {
	Model       string          `json:"model"`
	MaxTokens   int             `json:"max_tokens"`
	Messages    []ClaudeMessage `json:"messages"`
	System      json.RawMessage `json:"system,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	Stream      bool            `json:"stream"`
	Thinking    json.RawMessage `json:"thinking,omitempty"`
}

// ClaudeMessage is a single Anthropic message.
type ClaudeMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// claudeToOpenAI converts an Anthropic Messages request into the
// chat.completions shape used by the upstream pipeline.
func claudeToOpenAI(req ClaudeRequest) *OpenAIRequest {
	chat := &OpenAIRequest{
		Model:   req.Model,
		Stream:  req.Stream,
		MaxTokens: &req.MaxTokens,
	}
	if req.Temperature != nil {
		chat.Temperature = req.Temperature
	}
	if req.TopP != nil {
		chat.TopP = req.TopP
	}
	if len(req.System) > 0 {
		var sysText string
		var sysArr []map[string]any
		if err := json.Unmarshal(req.System, &sysArr); err == nil {
			for _, b := range sysArr {
				if t, ok := b["text"].(string); ok {
					sysText += t
				}
			}
		} else {
			_ = json.Unmarshal(req.System, &sysText)
		}
		if sysText != "" {
			chat.Messages = append(chat.Messages, Message{Role: "system", Content: sysText})
		}
	}
	for _, m := range req.Messages {
		content := flattenClaudeContent(m.Content)
		chat.Messages = append(chat.Messages, Message{Role: m.Role, Content: content})
	}
	return chat
}

// flattenClaudeContent normalizes Anthropic content (string or blocks)
// into a plain string. Images are dropped (text pipeline).
func flattenClaudeContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []map[string]any
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return string(raw)
	}
	var out strings.Builder
	for _, b := range blocks {
		typ, _ := b["type"].(string)
		switch typ {
		case "text":
			if t, ok := b["text"].(string); ok {
				out.WriteString(t)
			}
		case "thinking":
			if t, ok := b["thinking"].(string); ok {
				out.WriteString(t)
			}
		case "image", "tool_use", "tool_result":
			// not representable in the text pipeline; skip
		}
	}
	return out.String()
}

// anthropicToOpenAIResponse converts an Anthropic-style SSE stream into
// OpenAI chat.completion.chunk events. It handles:
//
//	event: content_block_delta -> delta.content
//	event: message_delta -> finish_reason + usage
//	event: message_stop -> [DONE]
//
// Returns the raw bytes to write to the client.
func anthropicSSEToOpenAI(rc io.Reader, model string) ([]byte, error) {
	var out strings.Builder
	scanner := bufio.NewScanner(rc)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	eventType := ""
	done := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event:") {
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		switch eventType {
		case "content_block_delta":
			var ev struct {
				Delta struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"delta"`
			}
			if json.Unmarshal([]byte(data), &ev) != nil {
				continue
			}
			if ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
				chunk := map[string]any{
					"id":      "chatcmpl-anthropic",
					"object":  "chat.completion.chunk",
					"model":   model,
					"choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": ev.Delta.Text}}},
				}
				b, _ := json.Marshal(chunk)
				out.WriteString("data: " + string(b) + "\n\n")
			}
		case "message_delta":
			var ev struct {
				Delta struct {
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Usage struct {
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal([]byte(data), &ev) != nil {
				continue
			}
			fr := "stop"
			if ev.Delta.StopReason == "max_tokens" {
				fr = "length"
			}
			chunk := map[string]any{
				"id":     "chatcmpl-anthropic",
				"object": "chat.completion.chunk",
				"model":  model,
				"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": fr}},
			}
			b, _ := json.Marshal(chunk)
			out.WriteString("data: " + string(b) + "\n\n")
		case "message_stop":
			done = true
			out.WriteString("data: [DONE]\n\n")
		}
		eventType = ""
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return []byte(out.String()), err
	}
	if !done {
		// upstream may omit message_stop; emit a terminating chunk anyway
		out.WriteString("data: [DONE]\n\n")
	}
	return []byte(out.String()), nil
}

func claudeMessagesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	auth := extractUpstreamAuth(r)

	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	var req ClaudeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = 1024
	}

	chatReq := claudeToOpenAI(req)
	modelIn := chatReq.Model
	chatReq.Model = resolveModel(chatReq.Model)
	if chatReq.Model == "" {
		ids := getFreeModelIDs()
		if len(ids) > 0 {
			chatReq.Model = ids[0]
		} else {
			chatReq.Model = "deepseek-v4-flash-free"
		}
	}
	chatReq.Messages = fixToolCallGaps(chatReq.Messages)

	slogInfo("request_plan",
		"protocol", "anthropic",
		"model_in", modelIn, "model_resolved", chatReq.Model,
		"auth_mode", authModeString(auth.Mode), "stream", chatReq.Stream)

	upstreamBody := buildUpstreamBody(chatReq)

	if chatReq.Stream {
		// The upstream (OpenCode Zen) always returns OpenAI-format SSE
		// chunks, even for /v1/messages. Relay them directly — the client
		// already speaks OpenAI chat.completion.chunk at the SSE level;
		// the JSON envelope is what we map to Anthropic below.
		relayStream(w, r, upstreamBody, chatReq.Model, auth)
		return
	}

	// Non-stream: relay upstream body as-is (OpenAI chat.completion JSON).
	relayNonStream(w, r, upstreamBody, chatReq.Model, auth)
}

// slogInfo is a tiny helper to keep the anthropic handler log line short.
func slogInfo(msg string, args ...any) {
	logSlogInfo(msg, args...)
}

var _ = fmt.Sprintf
