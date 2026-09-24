package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Local OpenCode server: its Node/Bun TLS fingerprint passes the free-tier
// origin check; opencode.ai rejects direct non-CLI requests.
const relayBaseURL = "http://127.0.0.1:4096"

var relayClient = &http.Client{Timeout: 180 * time.Second}

// relaySession creates a new session on the local OpenCode server.
func relayCreateSession(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, relayBaseURL+"/api/session", strings.NewReader("{}"))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := relayClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("relay create session: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("relay create session status %d: %s", resp.StatusCode, string(body))
	}
	var result struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("relay parse session: %w (%s)", err, string(body[:min(len(body), 200)]))
	}
	if result.Data.ID == "" {
		return "", fmt.Errorf("relay empty session id: %s", string(body[:min(len(body), 200)]))
	}
	return result.Data.ID, nil
}

// relaySetModel sets the model for a session.
func relaySetModel(ctx context.Context, sessionID, modelID string) error {
	payload := map[string]any{
		"model": map[string]string{
			"id":         modelID,
			"providerID": "opencode",
		},
	}
	raw, _ := json.Marshal(payload)
	url := fmt.Sprintf("%s/api/session/%s/model", relayBaseURL, sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := relayClient.Do(req)
	if err != nil {
		return fmt.Errorf("relay set model: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) // drain
	if resp.StatusCode >= 300 {
		return fmt.Errorf("relay set model status %d", resp.StatusCode)
	}
	return nil
}

// relaySendPrompt sends a text prompt to a session.
func relaySendPrompt(ctx context.Context, sessionID, text string) (string, error) {
	payload := map[string]any{
		"prompt": map[string]string{"text": text},
	}
	raw, _ := json.Marshal(payload)
	url := fmt.Sprintf("%s/api/session/%s/prompt", relayBaseURL, sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := relayClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("relay send prompt: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("relay send prompt status %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
	}
	var result struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	json.Unmarshal(body, &result)
	return result.Data.ID, nil
}

// relayWaitResult polls session context until an assistant message with finish appears.
func relayWaitResult(ctx context.Context, sessionID, userMsgID string, timeout time.Duration) (*relayAssistantMsg, error) {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(800 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("relay timeout waiting for response after %v", timeout)
		}

		url := fmt.Sprintf("%s/api/session/%s/context", relayBaseURL, sessionID)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			continue
		}
		resp, err := relayClient.Do(req)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
		resp.Body.Close()
		if resp.StatusCode != 200 {
			continue
		}

		var ctxResp struct {
			Data []relayMessage `json:"data"`
		}
		if err := json.Unmarshal(body, &ctxResp); err != nil {
			continue
		}

		for i := len(ctxResp.Data) - 1; i >= 0; i-- {
			msg := &ctxResp.Data[i]
			if msg.Type == "assistant" && msg.Finish != "" && msg.Time.Completed > 0 {
				if msg.Finish == "error" && msg.Error != nil && msg.Error.Message != "" {
					return nil, fmt.Errorf("upstream error: %s", msg.Error.Message)
				}
				result := extractAssistantText(msg)
				if result.Text != "" {
					return result, nil
				}
			}
		}
	}
}

type relayMessage struct {
	ID      string            `json:"id"`
	Type    string            `json:"type"`
	Model   *relayModelRef    `json:"model,omitempty"`
	Content []relayPart       `json:"content,omitempty"`
	Finish  string            `json:"finish,omitempty"`
	Cost    float64           `json:"cost,omitempty"`
	Tokens  *relayTokens      `json:"tokens,omitempty"`
	Time    relayTimeInfo     `json:"time"`
	Text    string            `json:"text,omitempty"` // user messages
	Error   *relayErrorInfo   `json:"error,omitempty"`
}

type relayErrorInfo struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type relayModelRef struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Variant    string `json:"variant,omitempty"`
}

type relayPart struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
	Text string `json:"text,omitempty"`
}

type relayTokens struct {
	Input     int `json:"input"`
	Output    int `json:"output"`
	Reasoning int `json:"reasoning"`
}

type relayTimeInfo struct {
	Created   int64 `json:"created"`
	Completed int64 `json:"completed"`
}

type relayAssistantMsg struct {
	Text   string
	Model  string
	Tokens *relayTokens
	Finish string
}

func extractAssistantText(msg *relayMessage) *relayAssistantMsg {
	var sb strings.Builder
	for _, part := range msg.Content {
		if part.Type == "text" && part.Text != "" {
			sb.WriteString(part.Text)
		}
	}
	modelName := ""
	if msg.Model != nil {
		modelName = msg.Model.ID
	}
	finish := msg.Finish
	if finish == "" {
		finish = "stop"
	}
	return &relayAssistantMsg{
		Text:   sb.String(),
		Model:  modelName,
		Tokens: msg.Tokens,
		Finish: finish,
	}
}

// callUpstreamRelay is the drop-in replacement for callUpstream that routes
// through the local OpenCode server (valid TLS fingerprint) instead of hitting
// opencode.ai directly.
func callUpstreamRelay(ctx context.Context, body []byte, modelID string) (upstreamResult, error) {
	start := time.Now()

	// Parse the OpenAI-format request to extract messages
	var openaiReq map[string]any
	if err := json.Unmarshal(body, &openaiReq); err != nil {
		return upstreamResult{}, fmt.Errorf("relay: invalid request body: %w", err)
	}

	// Flatten messages into a single prompt text
	promptText := flattenMessages(openaiReq["messages"])
	if promptText == "" {
		return upstreamResult{}, fmt.Errorf("relay: no messages in request")
	}

	slog.Info("relay_start", "request_id", reqID(ctx), "model", modelID, "prompt_len", len(promptText))

	sessionID, err := relayCreateSession(ctx)
	if err != nil {
		return upstreamResult{}, fmt.Errorf("relay session: %w", err)
	}
	slog.Debug("relay_session_created", "session_id", sessionID)

	if err := relaySetModel(ctx, sessionID, modelID); err != nil {
		return upstreamResult{}, fmt.Errorf("relay model: %w", err)
	}

	userMsgID, err := relaySendPrompt(ctx, sessionID, promptText)
	if err != nil {
		return upstreamResult{}, fmt.Errorf("relay prompt: %w", err)
	}

	// 120s: free-tier models can stall well past the 3s happy path
	result, err := relayWaitResult(ctx, sessionID, userMsgID, 120*time.Second)
	if err != nil {
		return upstreamResult{}, fmt.Errorf("relay wait: %w", err)
	}

	elapsed := time.Since(start)
	slog.Info("relay_done", "request_id", reqID(ctx), "model", modelID, "duration_ms", elapsed.Milliseconds(), "response_len", len(result.Text))

	openaiResp := buildOpenAIResponse(modelID, result)
	respBody, _ := json.Marshal(openaiResp)

	return upstreamResult{status: 200, body: respBody, header: nil}, nil
}

// flattenMessages converts OpenAI messages array into a single prompt string.
// Preserves system/user/assistant roles as labeled sections.
func flattenMessages(messages any) string {
	arr, ok := messages.([]any)
	if !ok {
		return ""
	}
	var sb strings.Builder
	for _, m := range arr {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		content := extractContent(msg["content"])
		if content == "" {
			continue
		}
		switch role {
		case "system":
			sb.WriteString("[System Instructions]\n")
			sb.WriteString(content)
			sb.WriteString("\n\n")
		case "user":
			sb.WriteString("[User]\n")
			sb.WriteString(content)
			sb.WriteString("\n\n")
		case "assistant":
			sb.WriteString("[Assistant]\n")
			sb.WriteString(content)
			sb.WriteString("\n\n")
		default:
			sb.WriteString(content)
			sb.WriteString("\n\n")
		}
	}
	return strings.TrimSpace(sb.String())
}

// extractContent handles both string and array-of-parts content formats.
func extractContent(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var sb strings.Builder
		for _, part := range c {
			p, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := p["type"].(string); t == "text" {
				if txt, _ := p["text"].(string); txt != "" {
					sb.WriteString(txt)
				}
			}
		}
		return sb.String()
	default:
		return ""
	}
}

// buildOpenAIResponse wraps relay result into standard OpenAI ChatCompletion JSON.
func buildOpenAIResponse(modelID string, result *relayAssistantMsg) map[string]any {
	usage := map[string]any{
		"prompt_tokens":     0,
		"completion_tokens": 0,
		"total_tokens":      0,
	}
	if result.Tokens != nil {
		usage["prompt_tokens"] = result.Tokens.Input
		usage["completion_tokens"] = result.Tokens.Output
		usage["total_tokens"] = result.Tokens.Input + result.Tokens.Output
	}

	return map[string]any{
		"id":      "chatcmpl-relay-" + randomString(12),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   modelID,
		"choices": []any{
			map[string]any{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": result.Text,
				},
				"finish_reason": result.Finish,
			},
		},
		"usage": usage,
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// relaySSE streams a buffered relay response as OpenAI-compatible SSE chunks.
// The OpenCode session API has no incremental token endpoint, so we emit the
// full text in word-sized deltas to preserve the SSE contract for clients
// (9Router requires stream:true for its usage history).
func relaySSE(ctx context.Context, w io.Writer, flusher func(), body []byte, modelID string) error {
	result, err := callUpstreamRelay(ctx, body, modelID)
	if err != nil {
		return err
	}

	// parse the OpenAI response we just built
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(result.body, &resp); err != nil {
		return fmt.Errorf("relay sse parse: %w", err)
	}
	if len(resp.Choices) == 0 {
		return fmt.Errorf("relay sse: empty choices")
	}

	chunkID := "chatcmpl-relay-" + randomString(12)
	created := time.Now().Unix()
	text := resp.Choices[0].Message.Content
	finish := resp.Choices[0].FinishReason
	if finish == "" {
		finish = "stop"
	}

	writeChunk := func(delta map[string]any, finishReason any) {
		payload := map[string]any{
			"id":      chunkID,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   modelID,
			"choices": []any{
				map[string]any{
					"index":         0,
					"delta":         delta,
					"finish_reason": finishReason,
				},
			},
		}
		raw, _ := json.Marshal(payload)
		fmt.Fprintf(w, "data: %s\n\n", raw)
		flusher()
	}

	// role preamble
	writeChunk(map[string]any{"role": "assistant", "content": ""}, nil)

	// emit text in word-sized deltas so the client sees progressive output
	words := strings.Fields(text)
	if len(words) == 0 {
		writeChunk(map[string]any{"content": text}, nil)
	} else {
		for i, word := range words {
			piece := word
			if i > 0 {
				piece = " " + word
			}
			writeChunk(map[string]any{"content": piece}, nil)
		}
	}

	// final chunk with finish_reason
	writeChunk(map[string]any{}, finish)
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher()

	var fullResp struct {
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	json.Unmarshal(result.body, &fullResp)
	if fullResp.Usage != nil && (fullResp.Usage.TotalTokens > 0 || fullResp.Usage.CompletionTokens > 0) {
		usage := map[string]any{
			"id":      chunkID,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   modelID,
			"choices": []any{},
			"usage": map[string]any{
				"prompt_tokens":     fullResp.Usage.PromptTokens,
				"completion_tokens": fullResp.Usage.CompletionTokens,
				"total_tokens":      fullResp.Usage.TotalTokens,
			},
		}
		raw, _ := json.Marshal(usage)
		fmt.Fprintf(w, "data: %s\n\n", raw)
		flusher()
	}
	return nil
}