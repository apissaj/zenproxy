package main

import (
	"encoding/json"
	"io"
	"net/http"
)

// ResponsesRequest mirrors the OpenAI Responses API request shape.
type ResponsesRequest struct {
	Model    string          `json:"model"`
	Input    json.RawMessage `json:"input"`
	Stream   bool            `json:"stream"`
	MaxOut   *int            `json:"max_output_tokens,omitempty"`
	Reasoning map[string]any `json:"reasoning,omitempty"`
}

// responsesToChat converts a Responses API request into the chat.completions
// shape used by the upstream pipeline. Input may be a string or an array of
// items (message / function_call / function_call_output).
func responsesToChat(req ResponsesRequest) (*OpenAIRequest, error) {
	messages := []Message{}
	var input any
	if len(req.Input) > 0 {
		if err := json.Unmarshal(req.Input, &input); err != nil {
			return nil, err
		}
	}
	switch v := input.(type) {
	case nil:
		// no input -> empty user message
	case string:
		messages = append(messages, Message{Role: "user", Content: v})
	case []any:
		for _, item := range v {
			obj, ok := item.(map[string]any)
			if !ok {
				continue
			}
			typ, _ := obj["type"].(string)
			switch typ {
			case "message", "":
				role, _ := obj["role"].(string)
				if role == "" {
					role = "user"
				}
				content := flattenResponsesContent(obj["content"])
				messages = append(messages, Message{Role: role, Content: content})
			case "function_call":
				// assistant tool call -> represent as assistant message w/ tool_calls
				callID, _ := obj["call_id"].(string)
				name, _ := obj["name"].(string)
				args, _ := obj["arguments"].(string)
				tc := map[string]any{
					"id":   callID,
					"type": "function",
					"function": map[string]any{"name": name, "arguments": args},
				}
				messages = append(messages, Message{Role: "assistant", ToolCalls: []any{tc}})
			case "function_call_output":
				callID, _ := obj["call_id"].(string)
				output, _ := obj["output"].(string)
				messages = append(messages, Message{Role: "tool", ToolCallID: callID, Content: output})
			}
		}
	default:
		// fallback: marshal to string
		b, err := json.Marshal(v)
		if err == nil {
			messages = append(messages, Message{Role: "user", Content: string(b)})
		}
	}
	return &OpenAIRequest{
		Model:     req.Model,
		Messages:  messages,
		Stream:    req.Stream,
		MaxTokens: req.MaxOut,
	}, nil
}

// flattenResponsesContent normalizes Responses content (string or blocks)
// into a plain string for the chat messages array.
func flattenResponsesContent(content any) string {
	switch v := content.(type) {
	case nil:
		return ""
	case string:
		return v
	case []any:
		var out string
		for _, part := range v {
			p, ok := part.(map[string]any)
			if !ok {
				continue
			}
			switch p["type"] {
			case "input_text", "output_text", "text":
				if t, ok := p["text"].(string); ok {
					out += t
				}
			case "input_image", "image":
				// images are not supported by the text pipeline; skip
			}
		}
		return out
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func responsesHandler(w http.ResponseWriter, r *http.Request) {
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
	var req ResponsesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	chatReq, err := responsesToChat(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
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
		"protocol", "responses",
		"model_in", modelIn, "model_resolved", chatReq.Model,
		"auth_mode", authModeString(auth.Mode), "stream", chatReq.Stream)

	upstreamBody := buildUpstreamBody(chatReq)

	if chatReq.Stream {
		relayStream(w, r, upstreamBody, chatReq.Model, auth)
		return
	}
	relayNonStream(w, r, upstreamBody, chatReq.Model, auth)
}
