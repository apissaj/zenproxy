package main

import (
	"sync"
	"time"
)

// RateLimitConfig is the on-disk rate limiting configuration.
type RateLimitConfig struct {
	Enabled           bool     `json:"enabled"`
	RequestsPerMinute int      `json:"requests_per_minute"`
	TokensPerMinute   int64    `json:"tokens_per_minute"`
	ExemptKeys        []string `json:"exempt_keys"`
}

// RateStatus is a live snapshot of one key's window usage.
type RateStatus struct {
	Requests    int     `json:"requests"`
	Tokens      int64   `json:"tokens"`
	ReqLimit    int     `json:"requests_per_minute"`
	TokenLimit  int64   `json:"tokens_per_minute"`
	Limited     bool    `json:"limited,omitempty"`
	RetryAfter  float64 `json:"retry_after_seconds,omitempty"`
}

// windowEntry is one recorded event inside a sliding window.
type windowEntry struct {
	at     time.Time
	tokens int64
}

// keyWindow holds the recent events for a single key.
type keyWindow struct {
	reqs   []time.Time
	tokens []windowEntry
}

// RateLimiter is a per-key sliding-window limiter (requests + tokens).
type RateLimiter struct {
	mu      sync.Mutex
	enabled bool
	reqLim  int
	tokLim  int64
	exempt  map[string]bool
	windows map[string]*keyWindow
}

// NewRateLimiter builds a limiter from config.
func NewRateLimiter(cfg RateLimitConfig) *RateLimiter {
	rl := &RateLimiter{
		enabled: cfg.Enabled,
		reqLim:  cfg.RequestsPerMinute,
		tokLim:  cfg.TokensPerMinute,
		exempt:  map[string]bool{},
		windows: map[string]*keyWindow{},
	}
	for _, k := range cfg.ExemptKeys {
		rl.exempt[k] = true
	}
	return rl
}

// Configure re-applies config at runtime (loadConfig path).
func (rl *RateLimiter) Configure(cfg RateLimitConfig) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.enabled = cfg.Enabled
	rl.reqLim = cfg.RequestsPerMinute
	rl.tokLim = cfg.TokensPerMinute
	rl.exempt = map[string]bool{}
	for _, k := range cfg.ExemptKeys {
		rl.exempt[k] = true
	}
}

// windowLen is the sliding window length.
const windowLen = time.Minute

// prune drops events older than the window.
func pruneWindow(w *keyWindow, now time.Time) {
	cutoff := now.Add(-windowLen)
	i := 0
	for i < len(w.reqs) && w.reqs[i].Before(cutoff) {
		i++
	}
	w.reqs = w.reqs[i:]
	j := 0
	for j < len(w.tokens) && w.tokens[j].at.Before(cutoff) {
		j++
	}
	w.tokens = w.tokens[j:]
}

// Check evaluates a request against the window WITHOUT recording it.
// estTokens is the estimated prompt tokens for the incoming request.
func (rl *RateLimiter) Check(key string, estTokens int64) RateStatus {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	st := RateStatus{ReqLimit: rl.reqLim, TokenLimit: rl.tokLim}
	if !rl.enabled || rl.exempt[key] {
		return st
	}
	now := time.Now()
	w := rl.windows[key]
	if w == nil {
		w = &keyWindow{}
		rl.windows[key] = w
	}
	pruneWindow(w, now)

	st.Requests = len(w.reqs)
	st.Tokens = 0
	for _, e := range w.tokens {
		st.Tokens += e.tokens
	}

	limited := false
	retryAfter := 0.0
	if rl.reqLim > 0 && st.Requests >= rl.reqLim {
		limited = true
		retryAfter = windowLen.Seconds() - now.Sub(w.reqs[0]).Seconds()
	}
	if rl.tokLim > 0 && st.Tokens+estTokens > rl.tokLim {
		limited = true
		if len(w.tokens) > 0 {
			ra := windowLen.Seconds() - now.Sub(w.tokens[0].at).Seconds()
			if ra > retryAfter {
				retryAfter = ra
			}
		}
	}
	if retryAfter < 0 {
		retryAfter = 0
	}
	st.Limited = limited
	st.RetryAfter = retryAfter
	return st
}

// Record adds a completed request (and its actual token usage) to the window.
func (rl *RateLimiter) Record(key string, tokens int64) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if !rl.enabled {
		return
	}
	now := time.Now()
	w := rl.windows[key]
	if w == nil {
		w = &keyWindow{}
		rl.windows[key] = w
	}
	pruneWindow(w, now)
	w.reqs = append(w.reqs, now)
	if tokens > 0 {
		w.tokens = append(w.tokens, windowEntry{at: now, tokens: tokens})
	}
	// keep memory bounded: drop the map if no activity for a long time
	if len(rl.windows) > 10000 {
		for k := range rl.windows {
			if len(rl.windows) <= 10000 {
				break
			}
			w2 := rl.windows[k]
			if len(w2.reqs) == 0 && len(w2.tokens) == 0 {
				delete(rl.windows, k)
			}
		}
	}
}

// Snapshot returns current window status for every tracked key (dashboard).
func (rl *RateLimiter) Snapshot() map[string]RateStatus {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	out := make(map[string]RateStatus, len(rl.windows))
	now := time.Now()
	for k, w := range rl.windows {
		pruneWindow(w, now)
		st := RateStatus{ReqLimit: rl.reqLim, TokenLimit: rl.tokLim}
		st.Requests = len(w.reqs)
		for _, e := range w.tokens {
			st.Tokens += e.tokens
		}
		out[k] = st
	}
	return out
}
