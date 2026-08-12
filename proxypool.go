package main

import (
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ProxyStatus is the live snapshot of one proxy in the pool.
type ProxyStatus struct {
	URL       string    `json:"url"`
	Status    string    `json:"status"` // "ready" | "cooldown" | "error"
	Uses      int64     `json:"uses"`
	Errors    int64     `json:"errors"`
	LastError string    `json:"last_error,omitempty"`
	Until     time.Time `json:"until,omitempty"`
}

// ProxyPool rotates through HTTP/SOCKS proxies. When a proxy returns 429
// or a transport error (connect refused, dial timeout), it's marked exhausted
// and skipped for cooldown_secs. After that it returns to the rotation.
//
// Order strategy: round-robin with atomic counter — every call to Pick()
// advances the cursor. Proxies in cooldown are skipped. If all are in cooldown,
// the call returns the next one in sequence (caller will likely hit 429 again,
// but the cursor continues so cooldown windows expire naturally).
type ProxyPool struct {
	mu       sync.RWMutex
	proxies  []*proxyEntry
	cooldown time.Duration
	cursor   atomic.Uint64
}

type proxyEntry struct {
	raw       string
	url       *url.URL
	status    string
	uses      atomic.Int64
	errors    atomic.Int64
	lastError atomic.Value // string
	until     time.Time
}

// NewProxyPool builds a pool from URLs. Invalid URLs are skipped with a warning.
func NewProxyPool(raws []string, cooldownSecs int) *ProxyPool {
	if cooldownSecs <= 0 {
		cooldownSecs = 60
	}
	p := &ProxyPool{
		cooldown: time.Duration(cooldownSecs) * time.Second,
	}
	for _, raw := range raws {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" {
			slog.Warn("proxy_pool: invalid URL, skipping", "url", raw, "error", err)
			continue
		}
		// only http/https/socks5/socks5h are useful proxies
		switch u.Scheme {
		case "http", "https", "socks5", "socks5h":
		default:
			slog.Warn("proxy_pool: unsupported scheme, skipping", "url", raw, "scheme", u.Scheme)
			continue
		}
		e := &proxyEntry{raw: raw, url: u, status: "ready"}
		e.lastError.Store("")
		p.proxies = append(p.proxies, e)
	}
	return p
}

// Len returns the number of proxies in the pool (after parsing).
func (p *ProxyPool) Len() int {
	if p == nil {
		return 0
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.proxies)
}

// Pick returns the URL of the next healthy proxy. Advances the cursor even
// when all proxies are in cooldown — this lets cooldown windows expire
// naturally rather than piling retries on a single entry.
func (p *ProxyPool) Pick() (*url.URL, string) {
	if p == nil || len(p.proxies) == 0 {
		return nil, ""
	}
	p.mu.RLock()
	n := len(p.proxies)
	p.mu.RUnlock()
	if n == 0 {
		return nil, ""
	}
	now := time.Now()
	// try up to n times to find a ready proxy
	startIdx := p.cursor.Add(1) % uint64(n)
	for i := uint64(0); i < uint64(n); i++ {
		idx := (startIdx + i) % uint64(n)
		p.mu.RLock()
		e := p.proxies[idx]
		ready := e.status == "ready" || now.After(e.until)
		u := e.url
		raw := e.raw
		p.mu.RUnlock()
		if ready {
			e.uses.Add(1)
			return u, raw
		}
	}
	// all in cooldown — return the one with the soonest expiry so caller
	// still makes progress (cursor advances anyway)
	p.mu.RLock()
	var best *proxyEntry
	bestUntil := time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, e := range p.proxies {
		if e.until.Before(bestUntil) {
			best = e
			bestUntil = e.until
		}
	}
	u := best.url
	raw := best.raw
	p.mu.RUnlock()
	if best != nil {
		best.uses.Add(1)
	}
	return u, raw
}

// MarkExhausted marks a proxy as in cooldown until cooldown window expires.
// Use this when upstream returns FreeUsageLimitError, 429, or transport errors.
func (p *ProxyPool) MarkExhausted(rawURL string, reason string) {
	if p == nil || rawURL == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.proxies {
		if p.proxies[i].raw == rawURL {
			p.proxies[i].status = "cooldown"
			p.proxies[i].until = time.Now().Add(p.cooldown)
			p.proxies[i].errors.Add(1)
			p.proxies[i].lastError.Store(reason)
			return
		}
	}
}

// MarkSuccess clears cooldown for a proxy after a successful request.
func (p *ProxyPool) MarkSuccess(rawURL string) {
	if p == nil || rawURL == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.proxies {
		if p.proxies[i].raw == rawURL {
			p.proxies[i].status = "ready"
			p.proxies[i].until = time.Time{}
			p.proxies[i].lastError.Store("")
			return
		}
	}
}

// Snapshot returns a live snapshot of all proxies (for the dashboard).
func (p *ProxyPool) Snapshot() []ProxyStatus {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]ProxyStatus, 0, len(p.proxies))
	now := time.Now()
	for _, e := range p.proxies {
		st := e.status
		if e.status == "cooldown" && now.After(e.until) {
			st = "ready"
		}
		out = append(out, ProxyStatus{
			URL:       e.raw,
			Status:    st,
			Uses:      e.uses.Load(),
			Errors:    e.errors.Load(),
			LastError: e.lastError.Load().(string),
			Until:     e.until,
		})
	}
	return out
}

// String returns a compact summary for logging.
func (p *ProxyPool) String() string {
	if p == nil {
		return "proxy_pool: nil"
	}
	return fmt.Sprintf("proxy_pool: %d proxies", p.Len())
}