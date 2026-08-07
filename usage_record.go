package main

import (
	"encoding/json"
	"strings"
)

// recordUsageFromBody extracts token usage from a non-stream OpenAI
// chat.completion response and records it against the key.
func recordUsageFromBody(key string, body []byte) {
	if usageStore_ == nil {
		return
	}
	var resp struct {
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return
	}
	if resp.Usage.PromptTokens == 0 && resp.Usage.CompletionTokens == 0 {
		return
	}
	usageStore_.Record(key, resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
	if rateLimiter != nil {
		rateLimiter.Record(key, resp.Usage.PromptTokens+resp.Usage.CompletionTokens)
	}
}

// recordUsageFromStream extracts token usage from an SSE stream by scanning
// the last usage-bearing chunk (OpenAI includes usage in the final chunk).
func recordUsageFromStream(key string, streamText string) {
	if usageStore_ == nil {
		return
	}
	var lastPrompt, lastCompletion int64
	lines := strings.Split(streamText, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" || data == "" {
			continue
		}
		var chunk struct {
			Usage *struct {
				PromptTokens     int64 `json:"prompt_tokens"`
				CompletionTokens int64 `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil || chunk.Usage == nil {
			continue
		}
		lastPrompt = chunk.Usage.PromptTokens
		lastCompletion = chunk.Usage.CompletionTokens
	}
	if lastPrompt == 0 && lastCompletion == 0 {
		return
	}
	usageStore_.Record(key, lastPrompt, lastCompletion)
	if rateLimiter != nil {
		rateLimiter.Record(key, lastPrompt+lastCompletion)
	}
}
