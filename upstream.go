package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"sync"
	"time"
)

// opencode version pinned for the User-Agent; fetched at startup.
var ocClientVer = "1.18.15"

// ocSessionID / ocProjectID emulate an OpenCode CLI client session.
var (
	ocSessionID  string
	ocProjectID  string
	sessionMu    sync.Mutex
	lastSession  time.Time
)

func initOCSession() {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	if ocSessionID != "" && time.Since(lastSession) < 30*time.Minute {
		return
	}
	ocSessionID = "ses_" + randomString(24)
	ocProjectID = randomHex(40)
	lastSession = time.Now()
	slog.Info("session initialized", "session_id", ocSessionID)
}

func currentSessionID() string {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	if ocSessionID == "" {
		// callers usually init first; guard anyway
		ocSessionID = "ses_" + randomString(24)
		ocProjectID = randomHex(40)
		lastSession = time.Now()
	}
	return ocSessionID
}

const letters = "abcdefghijklmnopqrstuvwxyz0123456789"

func randomString(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

func randomHex(n int) string {
	const hexDigits = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		b[i] = hexDigits[rand.Intn(16)]
	}
	return string(b)
}

// upstreamClient is the shared HTTP client with sane timeouts.
var upstreamClient = &http.Client{Timeout: 120 * time.Second}

type upstreamResult struct {
	status int
	body   []byte
	header http.Header
}

// callUpstream posts a chat-completion style request to OpenCode Zen.
func callUpstream(ctx context.Context, body []byte, modelID string, auth UpstreamAuth) (upstreamResult, error) {
	initOCSession()
	var bodyMap map[string]any
	if err := json.Unmarshal(body, &bodyMap); err != nil {
		return upstreamResult{}, fmt.Errorf("invalid request body")
	}
	useGo := auth.useGoEndpoint(modelID)
	url := "https://opencode.ai/zen/v1/chat/completions"
	if useGo {
		url = "https://opencode.ai/zen/go/v1/chat/completions"
	}
	bodyMap["model"] = modelID
	raw, err := json.Marshal(bodyMap)
	if err != nil {
		return upstreamResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return upstreamResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", auth.authHeader())
	req.Header.Set("User-Agent", "opencode/"+ocClientVer)
	req.Header.Set("x-opencode-client", "cli")
	req.Header.Set("x-opencode-project", ocProjectID)
	req.Header.Set("x-opencode-session", ocSessionID)
	req.Header.Set("x-opencode-request", "req_"+randomString(24))
	req.Header.Set("Accept", "application/json")

	start := time.Now()
	resp, err := upstreamClient.Do(req)
	if err != nil {
		slog.Error("upstream transport error", "request_id", reqID(ctx), "model", modelID, "error", err)
		return upstreamResult{}, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 50*1024*1024))
	if err != nil {
		return upstreamResult{}, err
	}
	slog.Info("upstream_attempt",
		"request_id", reqID(ctx), "model", modelID, "status", resp.StatusCode,
		"duration_ms", time.Since(start).Milliseconds())
	if resp.StatusCode != http.StatusOK {
		return upstreamResult{status: resp.StatusCode, body: respBody, header: resp.Header}, nil
	}
	return upstreamResult{status: resp.StatusCode, body: respBody, header: resp.Header}, nil
}

// callUpstreamStream keeps the response body open for SSE relay.
func callUpstreamStream(ctx context.Context, body []byte, modelID string, auth UpstreamAuth) (io.ReadCloser, http.Header, error) {
	initOCSession()
	var bodyMap map[string]any
	if err := json.Unmarshal(body, &bodyMap); err != nil {
		return nil, nil, fmt.Errorf("invalid request body")
	}
	useGo := auth.useGoEndpoint(modelID)
	url := "https://opencode.ai/zen/v1/chat/completions"
	if useGo {
		url = "https://opencode.ai/zen/go/v1/chat/completions"
	}
	bodyMap["model"] = modelID
	raw, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", auth.authHeader())
	req.Header.Set("User-Agent", "opencode/"+ocClientVer)
	req.Header.Set("x-opencode-client", "cli")
	req.Header.Set("x-opencode-project", ocProjectID)
	req.Header.Set("x-opencode-session", ocSessionID)
	req.Header.Set("x-opencode-request", "req_"+randomString(24))
	req.Header.Set("Accept", "text/event-stream")

	resp, err := upstreamClient.Do(req)
	if err != nil {
		slog.Error("upstream stream transport error", "request_id", reqID(ctx), "model", modelID, "error", err)
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
		return nil, resp.Header, upstreamStatusError{status: resp.StatusCode, body: b}
	}
	return resp.Body, resp.Header, nil
}

type upstreamStatusError struct {
	status int
	body   []byte
}

func (e upstreamStatusError) Error() string {
	return fmt.Sprintf("upstream status %d: %s", e.status, string(e.body))
}

type statusError int

func (s statusError) Error() string { return fmt.Sprintf("upstream status %d", int(s)) }
