package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

// Global rate limiter and usage store, wired from config at startup.
var (
	rateLimiter *RateLimiter
	usageStore_ *usageStore
)

// withRateLimit enforces the per-key rate limit before the handler runs.
// It uses the resolved API key (or "public" for unauthenticated traffic)
// as the identity, estimates prompt tokens from the body, and returns
// 429 with a Retry-After header when the window is exceeded.
func withRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rateLimiter == nil {
			next.ServeHTTP(w, r)
			return
		}
		key := apiKeyForRequest(r)
		estTokens := estimatePromptTokens(r)
		st := rateLimiter.Check(key, estTokens)
		if st.Limited {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", formatRetryAfter(st.RetryAfter))
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"message": "rate limit exceeded",
					"type":    "rate_limit_error",
					"retry_after_seconds": st.RetryAfter,
					"requests_in_window":  st.Requests,
					"tokens_in_window":    st.Tokens,
					"requests_limit":      st.ReqLimit,
					"tokens_limit":        st.TokenLimit,
				},
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// apiKeyForRequest resolves the identity used for rate limiting / usage.
// Prefixed keys (zen:/go:) are normalized to the bare token so a single
// key counts once regardless of tier prefix.
func apiKeyForRequest(r *http.Request) string {
	auth := extractUpstreamAuth(r)
	if auth.Mode == AuthRoutePublic {
		return "public"
	}
	return auth.Token
}

// estimatePromptTokens gives a cheap estimate of the request size in tokens
// (~4 chars/token) for pre-flight rate limiting. Actual usage is recorded
// after the upstream responds.
// It reads the body safely, then RESTORES it so the handler sees the full body.
func estimatePromptTokens(r *http.Request) int64 {
	if r.Body == nil {
		return 0
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return 0
	}
	// restore body for the handler
	r.Body = io.NopCloser(bytes.NewReader(body))
	return int64(len(body)) / 4
}

func formatRetryAfter(seconds float64) string {
	if seconds <= 0 {
		return "1"
	}
	return fmtInt(int(seconds) + 1)
}

func fmtInt(n int) string {
	return itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
