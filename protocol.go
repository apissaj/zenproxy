package main

import (
	"encoding/json"
	"strings"
)

// OpenAIRequest mirrors the OpenAI chat.completions request shape.
type OpenAIRequest struct {
	Model            string          `json:"model"`
	Messages         []Message       `json:"messages"`
	Stream           bool            `json:"stream"`
	Temperature      *float64        `json:"temperature,omitempty"`
	MaxTokens        *int            `json:"max_tokens,omitempty"`
	TopP             *float64        `json:"top_p,omitempty"`
	Tools            []json.RawMessage `json:"tools,omitempty"`
	ToolChoice       json.RawMessage `json:"tool_choice,omitempty"`
	Thinking         json.RawMessage `json:"thinking,omitempty"`
	ReasoningEffort  string          `json:"reasoning_effort,omitempty"`
	ExtraBody        map[string]any  `json:"-"`
}

// Message is a single chat message.
type Message struct {
	Role            string          `json:"role"`
	Content         any             `json:"content"`
	ReasoningContent *string        `json:"reasoning_content,omitempty"`
	ToolCalls       []any           `json:"tool_calls,omitempty"`
	ToolCallID      string          `json:"tool_call_id,omitempty"`
	Name            string          `json:"name,omitempty"`
}

// UnmarshalJSON keeps unknown fields in ExtraBody so they are forwarded.
func (r *OpenAIRequest) UnmarshalJSON(data []byte) error {
	type alias OpenAIRequest
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*r = OpenAIRequest(a)
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	known := map[string]bool{
		"model": true, "messages": true, "stream": true, "temperature": true,
		"max_tokens": true, "top_p": true, "tools": true, "tool_choice": true,
		"thinking": true, "reasoning_effort": true,
	}
	extra := map[string]any{}
	for k, v := range raw {
		if !known[k] {
			extra[k] = v
		}
	}
	if len(extra) > 0 {
		r.ExtraBody = extra
	}
	return nil
}

func isThinkingDisabled(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err == nil {
		t, _ := m["type"].(string)
		return t == "disabled"
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return !b
	}
	return false
}

func isThinkingEnabled(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err == nil {
		t, _ := m["type"].(string)
		return t == "enabled" || t == "adaptive"
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b
	}
	return false
}

// wantsReasoning decides whether to keep/preserve reasoning content.
func wantsReasoning(req *OpenAIRequest) bool {
	if getForceDisableThinking() {
		return false
	}
	if isThinkingDisabled(req.Thinking) {
		return false
	}
	if isThinkingEnabled(req.Thinking) {
		return true
	}
	if v, ok := req.ExtraBody["thinking"]; ok {
		raw, _ := json.Marshal(v)
		if isThinkingDisabled(raw) {
			return false
		}
	}
	return true
}

func buildUpstreamBody(req *OpenAIRequest) []byte {
	converted := map[string]any{
		"model":    req.Model,
		"messages": convertMessages(req.Messages),
		"stream":   req.Stream,
	}
	if req.Temperature != nil {
		converted["temperature"] = *req.Temperature
	}
	if req.MaxTokens != nil {
		converted["max_tokens"] = *req.MaxTokens
	}
	if req.TopP != nil {
		converted["top_p"] = *req.TopP
	}
	if len(req.Tools) > 0 {
		converted["tools"] = req.Tools
	}
	if len(req.ToolChoice) > 0 {
		converted["tool_choice"] = req.ToolChoice
	}
	if getForceDisableThinking() || isThinkingDisabled(req.Thinking) {
		converted["thinking"] = map[string]string{"type": "disabled"}
	} else if isThinkingEnabled(req.Thinking) {
		converted["thinking"] = map[string]string{"type": "enabled"}
	}
	if !getForceDisableThinking() && req.ReasoningEffort != "" {
		converted["reasoning_effort"] = mapReasoningEffort(req.ReasoningEffort)
	}
	if req.ExtraBody != nil {
		for k, v := range req.ExtraBody {
			if _, exists := converted[k]; !exists {
				converted[k] = v
			}
		}
	}
	b, _ := json.Marshal(converted)
	return b
}

func convertMessages(messages []Message) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, msg := range messages {
		clean := map[string]any{}
		if msg.Role != "" {
			clean["role"] = msg.Role
		}
		if msg.Content != nil {
			clean["content"] = msg.Content
		}
		if msg.ReasoningContent != nil {
			clean["reasoning_content"] = *msg.ReasoningContent
		}
		if len(msg.ToolCalls) > 0 {
			clean["tool_calls"] = msg.ToolCalls
		}
		if msg.ToolCallID != "" {
			clean["tool_call_id"] = msg.ToolCallID
		}
		if msg.Name != "" {
			clean["name"] = msg.Name
		}
		out = append(out, clean)
	}
	return out
}

// fixToolCallGaps reorders messages so every tool call has its tool response.
func fixToolCallGaps(messages []Message) []Message {
	toolResponses := map[string]*Message{}
	for i := range messages {
		if messages[i].Role == "tool" && messages[i].ToolCallID != "" {
			toolResponses[messages[i].ToolCallID] = &messages[i]
		}
	}
	fixed := make([]Message, 0, len(messages)+len(messages)/4)
	emitted := map[string]bool{}
	for _, msg := range messages {
		if msg.Role == "tool" && msg.ToolCallID != "" {
			if emitted[msg.ToolCallID] {
				continue
			}
		}
		fixed = append(fixed, msg)
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				callID := ""
				if m, ok := tc.(map[string]any); ok {
					callID, _ = m["id"].(string)
				}
				if resp, found := toolResponses[callID]; found {
					fixed = append(fixed, *resp)
				} else {
					fixed = append(fixed, Message{Role: "tool", ToolCallID: callID, Content: "Tool call result not available"})
				}
				emitted[callID] = true
			}
		}
	}
	return fixed
}

// stripPrefix trims the "data: " SSE prefix.
func stripPrefix(line string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "data:"))
}
