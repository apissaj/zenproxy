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
	"strings"
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
// Uses the upstream key pool when enabled (failover on 429/402).
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

	// attempt across pool keys (or a single attempt when pool disabled)
	maxAttempts := 1
	if upstreamPool != nil {
		maxAttempts = upstreamPool.Len()
		if maxAttempts < 1 {
			maxAttempts = 1
		}
	}
	var lastErr error
	var lastRes upstreamResult
	for attempt := 0; attempt < maxAttempts; attempt++ {
		authHeader, alias, ok := poolAuthHeader(auth)
		if !ok {
			break
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
		if err != nil {
			return upstreamResult{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", authHeader)
		req.Header.Set("User-Agent", "opencode/"+ocClientVer)
		req.Header.Set("x-opencode-client", "cli")
		req.Header.Set("x-opencode-project", ocProjectID)
		req.Header.Set("x-opencode-session", ocSessionID)
		req.Header.Set("x-opencode-request", "req_"+randomString(24))
		req.Header.Set("Accept", "application/json")

		start := time.Now()
		resp, err := upstreamClient.Do(req)
		if err != nil {
			slog.Error("upstream transport error", "request_id", reqID(ctx), "model", modelID, "key", alias, "error", err)
			lastErr = err
			continue
		}
		respBody, err := io.ReadAll(io.LimitReader(resp.Body, 50*1024*1024))
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		slog.Info("upstream_attempt",
			"request_id", reqID(ctx), "model", modelID, "key", alias, "status", resp.StatusCode,
			"duration_ms", time.Since(start).Milliseconds())
		// failover on quota/rate errors
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusPaymentRequired {
			upstreamPool.MarkExhausted(trimToken(strings.TrimPrefix(authHeader, "Bearer ")), fmt.Sprintf("status %d: %s", resp.StatusCode, truncate(string(respBody), 120)))
			lastRes = upstreamResult{status: resp.StatusCode, body: respBody, header: resp.Header}
			lastErr = upstreamStatusError{status: resp.StatusCode, body: respBody}
			continue
		}
		lastRes = upstreamResult{status: resp.StatusCode, body: respBody, header: resp.Header}
		lastErr = nil
		break
	}
	if lastErr != nil {
		return upstreamResult{}, lastErr
	}
	return lastRes, nil
}

// callUpstreamStream keeps the response body open for SSE relay.
// Uses the upstream key pool when enabled (failover on 429/402).
func callUpstreamStream(ctx context.Context, body []byte, modelID string, auth UpstreamAuth) (io.ReadCloser, http.Header, string, error) {
	initOCSession()
	var bodyMap map[string]any
	if err := json.Unmarshal(body, &bodyMap); err != nil {
		return nil, nil, "", fmt.Errorf("invalid request body")
	}
	useGo := auth.useGoEndpoint(modelID)
	url := "https://opencode.ai/zen/v1/chat/completions"
	if useGo {
		url = "https://opencode.ai/zen/go/v1/chat/completions"
	}
	bodyMap["model"] = modelID
	raw, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, nil, "", err
	}

	maxAttempts := 1
	if upstreamPool != nil {
		maxAttempts = upstreamPool.Len()
		if maxAttempts < 1 {
			maxAttempts = 1
		}
	}
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		authHeader, alias, ok := poolAuthHeader(auth)
		if !ok {
			break
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
		if err != nil {
			return nil, nil, "", err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", authHeader)
		req.Header.Set("User-Agent", "opencode/"+ocClientVer)
		req.Header.Set("x-opencode-client", "cli")
		req.Header.Set("x-opencode-project", ocProjectID)
		req.Header.Set("x-opencode-session", ocSessionID)
		req.Header.Set("x-opencode-request", "req_"+randomString(24))
		req.Header.Set("Accept", "text/event-stream")

		resp, err := upstreamClient.Do(req)
		if err != nil {
			slog.Error("upstream stream transport error", "request_id", reqID(ctx), "model", modelID, "key", alias, "error", err)
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
			resp.Body.Close()
			// failover on quota/rate errors
			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusPaymentRequired {
				upstreamPool.MarkExhausted(trimToken(strings.TrimPrefix(authHeader, "Bearer ")), fmt.Sprintf("status %d: %s", resp.StatusCode, truncate(string(body), 120)))
				lastErr = upstreamStatusError{status: resp.StatusCode, body: body}
				continue
			}
			return nil, resp.Header, "", upstreamStatusError{status: resp.StatusCode, body: body}
		}
		return resp.Body, resp.Header, alias, nil
	}
	return nil, nil, "", lastErr
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
