package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
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
	// always generate fresh session for per-request rate limit avoidance
	ocSessionID = "ses_" + randomString(24)
	ocProjectID = randomHex(40)
	lastSession = time.Now()
	slog.Debug("session initialized", "session_id", ocSessionID)
}

func currentSessionID() string {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	if ocSessionID == "" {
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
// Used when no proxy pool is configured.
var upstreamClient = &http.Client{Timeout: 120 * time.Second}

// clientForRequest returns the HTTP client to use for an upstream call.
// If proxyPool is enabled, a per-attempt client is created with the picked
// proxy URL (http, https, or socks5 — via golang.org/x/net/proxy would be
// ideal, but we keep zero deps: use the URL as Transport.Proxy for http(s)).
func clientForRequest() (*http.Client, string) {
	if proxyPool == nil || proxyPool.Len() == 0 {
		return upstreamClient, ""
	}
	proxyURL, raw := proxyPool.Pick()
	if proxyURL == nil {
		return upstreamClient, ""
	}
	// only http/https are supported via Transport.Proxy (zero-dep);
	// socks5 schemes are accepted but will not proxy (browser socks5 is rare
	// for upstream API calls anyway). Users can put http(s) proxies.
	transport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 30 * time.Second,
		IdleConnTimeout:     90 * time.Second,
		MaxIdleConnsPerHost: 10,
	}
	return &http.Client{Timeout: 120 * time.Second, Transport: transport}, raw
}

type upstreamResult struct {
	status int
	body   []byte
	header http.Header
}

// callUpstream posts a chat-completion style request to OpenCode Zen.
// Uses the upstream key pool when enabled (failover on 429/402).
// Uses the proxy pool when enabled (rotate egress IPs on per-IP limits).
// Falls back to direct connection on the final attempt if all proxies exhausted.
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

	// attempt across pool keys (or retry with fresh session when pool disabled)
	maxAttempts := 1
	poolSize := 0
	if upstreamPool != nil {
		poolSize = upstreamPool.Len()
	}
	if proxyPool != nil && proxyPool.Len() > poolSize {
		poolSize = proxyPool.Len()
	}
	if poolSize > 1 {
		maxAttempts = poolSize
	}
	var lastErr error
	var lastRes upstreamResult
	var authHeader, alias string
	for attempt := 0; attempt < maxAttempts; attempt++ {
		var proxyAlias string
		var httpClient *http.Client
		var ok bool
		authHeader, alias, ok = poolAuthHeader(auth)
		if !ok {
			break
		}
		// fresh session per attempt: free-tier rate limits are keyed on session
		initOCSession()
		// pick proxy for this attempt (nil if pool disabled)
		httpClient, proxyAlias = clientForRequest()
		status, respBody, err := retryWithBackoff(ctx, "chat:"+modelID, 2, func() (int, []byte, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
			if err != nil {
				return 0, nil, err
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
			resp, err := httpClient.Do(req)
			if err != nil {
				slog.Error("upstream transport error", "request_id", reqID(ctx), "model", modelID, "key", alias, "proxy", proxyAlias, "error", err)
				return 0, nil, err
			}
			respBody, err := io.ReadAll(io.LimitReader(resp.Body, 50*1024*1024))
			resp.Body.Close()
			slog.Info("upstream_attempt",
				"request_id", reqID(ctx), "model", modelID, "key", alias, "proxy", proxyAlias, "status", resp.StatusCode,
				"duration_ms", time.Since(start).Milliseconds())
			return resp.StatusCode, respBody, err
		})
		if err != nil && status == 0 {
			lastErr = err
			continue
		}
		// free-tier IP-limit or transport error: mark proxy as exhausted
		if (status == http.StatusTooManyRequests || status == http.StatusPaymentRequired || status >= 500) && proxyAlias != "" {
			proxyPool.MarkExhausted(proxyAlias, fmt.Sprintf("status %d: %s", status, truncate(string(respBody), 120)))
		}
		if status == http.StatusTooManyRequests || status == http.StatusPaymentRequired {
			if upstreamPool != nil {
				upstreamPool.MarkExhausted(trimToken(strings.TrimPrefix(authHeader, "Bearer ")), fmt.Sprintf("status %d: %s", status, truncate(string(respBody), 120)))
			}
			lastRes = upstreamResult{status: status, body: respBody, header: nil}
			lastErr = upstreamStatusError{status: status, body: respBody}
			continue
		}
		// success — clear proxy cooldown
		if status >= 200 && status < 300 && proxyAlias != "" {
			proxyPool.MarkSuccess(proxyAlias)
		}
		lastRes = upstreamResult{status: status, body: respBody, header: nil}
		lastErr = nil
		break
	}
	// final fallback: if proxy pool is enabled but all proxies exhausted/failed,
	// retry once with direct connection (no proxy) before giving up.
	if proxyPool != nil && proxyPool.Len() > 0 {
		slog.Warn("all proxies exhausted, falling back to direct connection", "request_id", reqID(ctx), "model", modelID)
		initOCSession()
		status, respBody, err := retryWithBackoff(ctx, "chat:"+modelID+":direct", 1, func() (int, []byte, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
			if err != nil {
				return 0, nil, err
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
				slog.Error("upstream direct fallback error", "request_id", reqID(ctx), "model", modelID, "error", err)
				return 0, nil, err
			}
			respBody, err := io.ReadAll(io.LimitReader(resp.Body, 50*1024*1024))
			resp.Body.Close()
			slog.Info("upstream_attempt_direct",
				"request_id", reqID(ctx), "model", modelID, "status", resp.StatusCode,
				"duration_ms", time.Since(start).Milliseconds())
			return resp.StatusCode, respBody, err
		})
		if err == nil && status >= 200 && status < 300 {
			return upstreamResult{status: status, body: respBody, header: nil}, nil
		}
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
	poolSize := 0
	if upstreamPool != nil {
		poolSize = upstreamPool.Len()
	}
	if proxyPool != nil && proxyPool.Len() > poolSize {
		poolSize = proxyPool.Len()
	}
	if poolSize > 1 {
		maxAttempts = poolSize
	}
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		authHeader, alias, ok := poolAuthHeader(auth)
		if !ok {
			break
		}
		// fresh session per attempt: free-tier rate limits are keyed on session
		initOCSession()
		// pick proxy for this attempt (nil if pool disabled)
		httpClient, proxyAlias := clientForRequest()
		// retry stream connection on 429 with fresh session + backoff
		for r := 0; r <= 2; r++ {
			if r > 0 {
				wait := time.Duration(1<<uint(r)) * time.Second
				if wait > 10*time.Second {
					wait = 10 * time.Second
				}
				slog.Warn("retrying upstream stream", "request_id", reqID(ctx), "model", modelID, "key", alias, "proxy", proxyAlias, "attempt", r)
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return nil, nil, "", ctx.Err()
				case <-timer.C:
				}
				initOCSession()
				// re-pick proxy on retry (a fresh proxy may have come off cooldown)
				httpClient, proxyAlias = clientForRequest()
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

			resp, err := httpClient.Do(req)
			if err != nil {
				slog.Error("upstream stream transport error", "request_id", reqID(ctx), "model", modelID, "key", alias, "proxy", proxyAlias, "error", err)
				if proxyAlias != "" {
					proxyPool.MarkExhausted(proxyAlias, err.Error())
				}
				lastErr = err
				// retry with fresh session (up to 2 retries)
				if r < 2 {
					continue
				}
				break
			}
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
				resp.Body.Close()
				// mark proxy exhausted on IP-limit / server errors
				if (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusPaymentRequired || resp.StatusCode >= 500) && proxyAlias != "" {
					proxyPool.MarkExhausted(proxyAlias, fmt.Sprintf("status %d: %s", resp.StatusCode, truncate(string(body), 120)))
				}
				// failover on quota/rate errors
				if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusPaymentRequired {
					if upstreamPool != nil {
						upstreamPool.MarkExhausted(trimToken(strings.TrimPrefix(authHeader, "Bearer ")), fmt.Sprintf("status %d: %s", resp.StatusCode, truncate(string(body), 120)))
					}
					lastErr = upstreamStatusError{status: resp.StatusCode, body: body}
					// retry with fresh session (up to 2 retries)
					if r < 2 {
						continue
					}
					break
				}
				return nil, resp.Header, "", upstreamStatusError{status: resp.StatusCode, body: body}
			}
			// success — clear proxy cooldown
			if proxyAlias != "" {
				proxyPool.MarkSuccess(proxyAlias)
			}
			return resp.Body, resp.Header, alias, nil
		}
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

// retryWithBackoff calls fn up to maxRetries times, backing off between attempts.
// Retries on 429, 5xx, and transport errors (DNS failure, connection reset, etc.).
// Returns the first non-retryable result. On last attempt, returns whatever fn returns.
func retryWithBackoff(ctx context.Context, label string, maxRetries int, fn func() (int, []byte, error)) (int, []byte, error) {
	var lastStatus int
	var lastBody []byte
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			wait := time.Duration(1<<uint(attempt)) * time.Second
			if wait > 15*time.Second {
				wait = 15 * time.Second
			}
			slog.Warn("retrying upstream", "request_id", reqID(ctx), "label", label, "attempt", attempt, "max", maxRetries, "wait", wait)
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return 0, nil, ctx.Err()
			case <-timer.C:
			}
		}
		status, body, err := fn()
		lastStatus = status
		lastBody = body
		lastErr = err
		// retry on: 429 (rate limit), 5xx (server error), or transport errors (DNS, conn reset, timeout)
		if err != nil {
			// transport error — retry
			continue
		}
		if status == http.StatusTooManyRequests || status == http.StatusPaymentRequired || status >= 500 {
			// 429/402/5xx — retry
			continue
		}
		break
	}
	return lastStatus, lastBody, lastErr
}
