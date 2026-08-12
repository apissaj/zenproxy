package main

import (
	"net/url"
	"testing"
	"time"
)

func TestProxyPoolSkipsInvalid(t *testing.T) {
	p := NewProxyPool([]string{
		"",
		"not-a-url",
		"ftp://example.com",
		"http://1.2.3.4:8080",
		"socks5://1.2.3.4:1080",
	}, 30)
	if p.Len() != 2 {
		t.Fatalf("expected 2 valid proxies, got %d", p.Len())
	}
}

func TestProxyPoolRoundRobin(t *testing.T) {
	p := NewProxyPool([]string{
		"http://1.2.3.4:8080",
		"http://5.6.7.8:8080",
		"http://9.10.11.12:8080",
	}, 60)
	if p.Len() != 3 {
		t.Fatalf("expected 3 proxies, got %d", p.Len())
	}
	seen := map[string]int{}
	for i := 0; i < 9; i++ {
		_, raw := p.Pick()
		if _, err := url.Parse(raw); err != nil {
			t.Fatalf("invalid proxy URL: %s", raw)
		}
		seen[raw]++
	}
	if len(seen) != 3 {
		t.Fatalf("expected 3 unique proxies round-robin, got %d", len(seen))
	}
}

func TestProxyPoolCooldown(t *testing.T) {
	p := NewProxyPool([]string{
		"http://1.2.3.4:8080",
		"http://5.6.7.8:8080",
	}, 1)
	_, a := p.Pick()
	_, b := p.Pick()
	if a == b {
		t.Fatalf("expected different proxies on consecutive picks, both=%s", a)
	}
	// exhaust a
	p.MarkExhausted(a, "test 429")
	// next pick should skip a and pick b (or any other ready one)
	_, next := p.Pick()
	if next == a {
		t.Fatalf("expected pick to skip exhausted proxy %s, got %s", a, next)
	}
	// wait for cooldown to expire
	time.Sleep(1100 * time.Millisecond)
	// MarkSuccess clears it explicitly
	p.MarkSuccess(a)
	_, ok := p.Pick()
	if ok == "" {
		t.Fatalf("expected a valid proxy after MarkSuccess")
	}
}

func TestProxyPoolSnapshot(t *testing.T) {
	p := NewProxyPool([]string{
		"http://1.2.3.4:8080",
		"http://5.6.7.8:8080",
	}, 30)
	_, a := p.Pick()
	p.MarkExhausted(a, "rate limit")
	snap := p.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(snap))
	}
	var foundExhausted bool
	for _, e := range snap {
		if e.URL == a && e.Status == "cooldown" {
			foundExhausted = true
		}
	}
	if !foundExhausted {
		t.Fatalf("expected proxy %s to be in cooldown, snapshot=%+v", a, snap)
	}
}