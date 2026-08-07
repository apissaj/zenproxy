package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
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

// withKeyAuth enforces managed API key authentication when enabled.
// - key_auth.enabled=false: pass-through (existing behavior)
// - key_auth.allow_public=true: "public" / no key uses public tier
// - otherwise: only registered keys allowed, with model allowlist + budget cap
func withKeyAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if keyStore == nil {
			next.ServeHTTP(w, r)
			return
		}
		identity, rec := resolveIdentity(r)
		if rec == nil {
			// no managed key — allow public tier if open, or valid upstream
			// OpenCode credentials (sk-* with optional zen:/go: prefix).
			if identity == "public" && keyAuthAllowPublic() {
				next.ServeHTTP(w, r)
				return
			}
			upstream := strings.TrimPrefix(identity, "zen:")
			upstream = strings.TrimPrefix(upstream, "go:")
			if isValidOpenCodeKey(upstream) {
				next.ServeHTTP(w, r)
				return
			}
			writeAPIError(w, http.StatusUnauthorized, "invalid or revoked API key", "authentication_error", nil)
			return
		}
		// budget cap
		if rec.BudgetUSD > 0 {
			spend := activeSpendUSD(rec)
			if spend >= rec.BudgetUSD {
				writeAPIError(w, http.StatusForbidden, "budget exceeded", "budget_exceeded", map[string]any{
					"budget_usd": rec.BudgetUSD,
					"spent_usd":  spend,
				})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// resolveIdentity maps a request to its identity string and managed key record.
// Priority: managed key (zp_...) > OpenCode tier key (sk-... with zen:/go: prefix)
// > public. The identity string is used for rate limiting + usage tracking.
func resolveIdentity(r *http.Request) (string, *ProxyKey) {
	token := ""
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		token = strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	}
	if token == "" {
		token = strings.TrimSpace(r.Header.Get("x-api-key"))
	}
	if token == "" || token == "public" {
		return "public", nil
	}
	if keyStore != nil {
		// managed keys use zp_ prefix (or bare); exact match required
		if rec := keyStore.Lookup(token); rec != nil {
			return rec.Hash, rec
		}
		// a zp_-prefixed token that isn't registered is INVALID — do not
		// fall through to public/OpenCode semantics.
		if strings.HasPrefix(token, "zp_") {
			return token, nil
		}
	}
	// fall back to OpenCode tier semantics (zen:/go:/bare sk-)
	ua := extractUpstreamAuth(r)
	if ua.Mode != AuthRoutePublic {
		return ua.Token, nil
	}
	return "public", nil
}

// apiKeyForRequest resolves the identity used for rate limiting / usage.
func apiKeyForRequest(r *http.Request) string {
	id, _ := resolveIdentity(r)
	return id
}

// keyAuthAllowPublic reads the allow_public config flag (set at startup).
func keyAuthAllowPublic() bool {
	return keyAuthAllowPublicFlag
}

// writeAPIError emits a standard OpenAI-style error JSON.
func writeAPIError(w http.ResponseWriter, status int, message, typ string, extra map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    typ,
		},
	}
	for k, v := range extra {
		body["error"].(map[string]any)[k] = v
	}
	json.NewEncoder(w).Encode(body)
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

// helper: is this string a known managed key (for logging)?
func isManagedKey(token string) bool {
	if keyStore == nil {
		return false
	}
	return keyStore.Lookup(token) != nil
}

// withDashboardAuth protects /dashboard when dashboard_auth.enabled=true.
// Token passed via ?token= or Authorization: Bearer <token> or x-dashboard-token.
func withDashboardAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !dashboardAuthEnabled {
			next.ServeHTTP(w, r)
			return
		}
		token := r.URL.Query().Get("token")
		if token == "" {
			auth := r.Header.Get("Authorization")
			if strings.HasPrefix(auth, "Bearer ") {
				token = strings.TrimPrefix(auth, "Bearer ")
			}
		}
		if token == "" {
			token = r.Header.Get("x-dashboard-token")
		}
		if token != "" && token == dashboardAuthToken {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"message": "dashboard authentication required",
				"type":    "authentication_error",
			},
		})
	})
}

// withKeyLimits enforces per-key model allowlist + budget when key auth is on.
func withKeyLimits(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if keyStore == nil {
			next.ServeHTTP(w, r)
			return
		}
		_, rec := resolveIdentity(r)
		if rec != nil && len(rec.ModelAllow) > 0 {
			// parse requested model from body
			body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			var req struct {
				Model string `json:"model"`
			}
			json.Unmarshal(body, &req)
			model := req.Model
			// strip ZP/ prefix (9Router)
			if idx := strings.Index(model, "/"); idx >= 0 {
				model = model[idx+1:]
			}
			allowed := false
			for _, m := range rec.ModelAllow {
				if m == model || m == "*" {
					allowed = true
					break
				}
			}
			if !allowed {
				writeAPIError(w, http.StatusForbidden, "model not allowed for this key", "model_not_allowed", map[string]any{
					"model":   req.Model,
					"allowed": rec.ModelAllow,
				})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
