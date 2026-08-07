package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ============================================================
// OpenAI (chat.completion) -> Anthropic (message) conversion
//
// The upstream (OpenCode Zen) always speaks OpenAI chat.completion,
// even when we hit it via /v1/messages. Anthropic clients (Claude
// Code, Cursor, etc.) expect the Anthropic Messages envelope:
//
//	{
//	  "id": "msg_...",
//	  "type": "message",
//	  "role": "assistant",
//	  "content": [{"type":"text","text":"..."}],
//	  "model": "...",
//	  "stop_reason": "end_turn",
//	  "usage": {"input_tokens":N,"output_tokens":N}
//	}
// ============================================================

const anthropicMsgIDPrefix = "msg_zenproxy_"

// openaiToAnthropicMessage converts a non-stream OpenAI chat.completion
// JSON body into the Anthropic Messages response shape.
func openaiToAnthropicMessage(openAIJSON []byte) ([]byte, error) {
	var resp struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		Model   string `json:"model"`
		Choices []struct {
			Index        int    `json:"index"`
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Role    string `json:"role"`
				Content *string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(openAIJSON, &resp); err != nil {
		return nil, err
	}

	msgID := "msg_zenproxy_" + resp.ID
	model := resp.Model
	stopReason := "end_turn"
	if len(resp.Choices) > 0 {
		switch resp.Choices[0].FinishReason {
		case "length", "max_tokens":
			stopReason = "max_tokens"
		case "content_filter":
			stopReason = "refusal"
		}
	}

	var contentBlocks []map[string]any
	var text string
	if len(resp.Choices) > 0 && resp.Choices[0].Message.Content != nil {
		text = *resp.Choices[0].Message.Content
	}
	if text != "" {
		contentBlocks = append(contentBlocks, map[string]any{
			"type": "text",
			"text": text,
		})
	}
	if len(contentBlocks) == 0 {
		contentBlocks = append(contentBlocks, map[string]any{
			"type": "text",
			"text": "",
		})
	}

	out := map[string]any{
		"id":      msgID,
		"type":    "message",
		"role":    "assistant",
		"content": contentBlocks,
		"model":   model,
		"stop_reason": stopReason,
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  resp.Usage.PromptTokens,
			"output_tokens": resp.Usage.CompletionTokens,
		},
	}
	return json.Marshal(out)
}

// anthropicStreamWriter converts an OpenAI chat.completion.chunk SSE stream
// into Anthropic Messages SSE events:
//
//	event: message_start        {message:{...}, usage:{...}}
//	event: content_block_start  {index, content_block:{type:"text",...}}
//	event: content_block_delta  {index, delta:{type:"text_delta",text:"..."}}
//	event: content_block_stop   {index}
//	event: message_delta        {delta:{stop_reason}, usage:{output_tokens}}
//	event: message_stop         {}
//
// It writes converted events directly to w, flushing after each event.
type anthropicStreamWriter struct {
	w        io.Writer
	flusher  interface{ Flush() }
	model    string
	msgID    string
	started  bool
	stopSent bool
	usageIn  int
	usageOut int
}

func newAnthropicStreamWriter(w io.Writer, flusher interface{ Flush() }, model string) *anthropicStreamWriter {
	return &anthropicStreamWriter{
		w:       w,
		flusher: flusher,
		model:   model,
		msgID:   anthropicMsgIDPrefix + "stream",
	}
}

func (a *anthropicStreamWriter) writeEvent(event string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.w, "event: %s\ndata: %s\n\n", event, b); err != nil {
		return err
	}
	if a.flusher != nil {
		a.flusher.Flush()
	}
	return nil
}

// handleChunk processes one OpenAI chunk line and emits Anthropic events.
func (a *anthropicStreamWriter) handleChunk(data string) error {
	var chunk struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Index        int `json:"index"`
			FinishReason *string `json:"finish_reason"`
			Delta        struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return nil // skip malformed
	}
	if chunk.ID != "" {
		a.msgID = anthropicMsgIDPrefix + chunk.ID
	}
	if chunk.Model != "" {
		a.model = chunk.Model
	}

	// message_start
	if !a.started {
		a.started = true
		msg := map[string]any{
			"id":      a.msgID,
			"type":    "message",
			"role":    "assistant",
			"content": []any{},
			"model":   a.model,
			"stop_reason": nil,
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":  0,
				"output_tokens": 0,
			},
		}
		if err := a.writeEvent("message_start", map[string]any{"message": msg, "type": "message_start"}); err != nil {
			return err
		}
		if err := a.writeEvent("content_block_start", map[string]any{
			"type":         "content_block_start",
			"index":        0,
			"content_block": map[string]any{"type": "text", "text": ""},
		}); err != nil {
			return err
		}
	}

	if len(chunk.Choices) > 0 {
		delta := chunk.Choices[0].Delta
		if delta.Content != "" {
			if err := a.writeEvent("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{"type": "text_delta", "text": delta.Content},
			}); err != nil {
				return err
			}
		}
		if chunk.Choices[0].FinishReason != nil && !a.stopSent {
			a.stopSent = true
			stopReason := "end_turn"
			switch *chunk.Choices[0].FinishReason {
			case "length", "max_tokens":
				stopReason = "max_tokens"
			case "content_filter":
				stopReason = "refusal"
			}
			if err := a.writeEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}); err != nil {
				return err
			}
			if err := a.writeEvent("message_delta", map[string]any{
				"type":  "message_delta",
				"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
				"usage": map[string]any{"output_tokens": a.usageOut},
			}); err != nil {
				return err
			}
			if err := a.writeEvent("message_stop", map[string]any{"type": "message_stop"}); err != nil {
				return err
			}
		}
	}

	if chunk.Usage != nil {
		a.usageIn = chunk.Usage.PromptTokens
		a.usageOut = chunk.Usage.CompletionTokens
	}
	return nil
}

// drain reads the upstream SSE stream until EOF or [DONE].
func (a *anthropicStreamWriter) drain(rc io.Reader) error {
	scanner := bufio.NewScanner(rc)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, ":") { // keep-alive
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				break
			}
			if err := a.handleChunk(data); err != nil {
				return err
			}
		}
	}
	// Ensure terminal events even if upstream cut early
	if a.started && !a.stopSent {
		a.stopSent = true
		_ = a.writeEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		_ = a.writeEvent("message_delta", map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
			"usage": map[string]any{"output_tokens": a.usageOut},
		})
		_ = a.writeEvent("message_stop", map[string]any{"type": "message_stop"})
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return err
	}
	return nil
}
