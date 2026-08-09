package main

import (
	"log/slog"
	"strings"
	"sync"
	"time"
)

// UpstreamKeyStatus tracks the state of a single upstream key.
type UpstreamKeyStatus struct {
	Token       string    `json:"-"` // never serialized
	Alias       string    `json:"alias"`
	CooldownUntil time.Time `json:"cooldown_until"`
	Exhausted   bool      `json:"exhausted"`
	LastError   string    `json:"last_error,omitempty"`
	LastUsed    time.Time `json:"last_used,omitempty"`
	UseCount    int64     `json:"use_count"`
	ErrorCount  int64     `json:"error_count"`
}

// UpstreamPool manages a pool of upstream API keys with automatic failover.
// When a key returns 429/402 (rate limit / quota exhausted) it enters a
// cooldown period; subsequent requests skip it and try the next key.
type UpstreamPool struct {
	mu           sync.RWMutex
	keys         []*UpstreamKeyStatus
	nextIdx      int
	cooldownSecs int // seconds to wait before retrying an exhausted key
}

// NewUpstreamPool creates a pool from a list of raw tokens.
// cooldownSecs: how long to wait before retrying an exhausted key (default 60).
func NewUpstreamPool(tokens []string, cooldownSecs int) *UpstreamPool {
	if cooldownSecs <= 0 {
		cooldownSecs = 60
	}
	p := &UpstreamPool{
		keys:         make([]*UpstreamKeyStatus, 0, len(tokens)),
		nextIdx:      0,
		cooldownSecs: cooldownSecs,
	}
	for _, tok := range tokens {
		tok = trimToken(tok)
		if tok == "" {
			continue
		}
		p.keys = append(p.keys, &UpstreamKeyStatus{
			Token: tok,
			Alias: maskToken(tok),
		})
	}
	slog.Info("upstream pool initialized", "count", len(p.keys), "cooldown_secs", cooldownSecs)
	return p
}

func trimToken(t string) string {
	// strip whitespace
	out := make([]byte, 0, len(t))
	for _, b := range []byte(t) {
		if b == ' ' || b == '	' || b == '\n' || b == '\r' {
			continue
		}
		out = append(out, b)
	}
	s := string(out)
	// strip optional "zen:" / "go:" prefix (3-char prefix + colon)
	for _, prefix := range []string{"zen:", "go:", "sk-"} {
		if len(s) > len(prefix) && s[:len(prefix)] == prefix {
			s = s[len(prefix):]
		}
	}
	// ensure it starts with sk- after stripping
	if !strings.HasPrefix(s, "sk-") {
		s = "sk-" + s
	}
	return s
}

func maskToken(tok string) string {
	if len(tok) <= 8 {
		return tok
	}
	return tok[:4] + "..." + tok[len(tok)-4:]
}

// Len returns the number of keys in the pool.
func (p *UpstreamPool) Len() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.keys)
}

// Status snapshot for dashboard display.
func (p *UpstreamPool) Status() []UpstreamKeyStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]UpstreamKeyStatus, len(p.keys))
	for i, k := range p.keys {
		out[i] = *k
		out[i].Token = "" // never expose raw token
	}
	return out
}

// Available returns true if at least one key is not in cooldown.
func (p *UpstreamPool) Available() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	now := time.Now()
	for _, k := range p.keys {
		if !k.Exhausted && now.After(k.CooldownUntil) {
			return true
		}
	}
	return false
}

// Next picks the next available key (round-robin, skipping exhausted/cooldown keys).
// Returns ("", false) if no key is available.
func (p *UpstreamPool) Next() (string, string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.keys) == 0 {
		return "", "", false
	}
	now := time.Now()
	start := p.nextIdx
	for i := 0; i < len(p.keys); i++ {
		idx := (start + i) % len(p.keys)
		k := p.keys[idx]
		if k.Exhausted {
			continue
		}
		if now.Before(k.CooldownUntil) {
			continue
		}
		p.nextIdx = (idx + 1) % len(p.keys)
		k.LastUsed = now
		k.UseCount++
		return k.Token, k.Alias, true
	}
	// All keys exhausted or in cooldown — pick the one that recovers soonest
	best := p.keys[0]
	bestIdx := 0
	for i, k := range p.keys {
		if k.Exhausted {
			continue
		}
		if k.CooldownUntil.Before(best.CooldownUntil) {
			best = k
			bestIdx = i
		}
	}
	p.nextIdx = (bestIdx + 1) % len(p.keys)
	best.LastUsed = now
	best.UseCount++
	return best.Token, best.Alias, true
}

// MarkExhausted marks a key as exhausted (429/402) and sets cooldown.
func (p *UpstreamPool) MarkExhausted(token string, errMsg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, k := range p.keys {
		if k.Token != token {
			continue
		}
		k.ErrorCount++
		k.LastError = errMsg
		k.CooldownUntil = time.Now().Add(time.Duration(p.cooldownSecs) * time.Second)
		// Only mark as exhausted if we have other keys; otherwise keep trying
		hasOther := false
		for _, k2 := range p.keys {
			if k2.Token != token && !k2.Exhausted {
				hasOther = true
				break
			}
		}
		if hasOther {
			k.Exhausted = true
		}
		slog.Warn("upstream key exhausted",
			"alias", k.Alias, "cooldown_secs", p.cooldownSecs,
			"error", errMsg, "exhausted", k.Exhausted)
		return
	}
}