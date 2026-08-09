package main

import (
	"testing"
	"time"
)

func TestUpstreamPoolRoundRobin(t *testing.T) {
	p := NewUpstreamPool([]string{"sk-key-1", "sk-key-2", "sk-key-3"}, 60)
	if p.Len() != 3 {
		t.Fatalf("expected 3 keys, got %d", p.Len())
	}
	// round-robin cycles through all keys
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		tok, _, ok := p.Next()
		if !ok {
			t.Fatal("expected key available")
		}
		seen[tok] = true
	}
	if len(seen) != 3 {
		t.Fatalf("expected 3 distinct keys, got %d", len(seen))
	}
}

func TestUpstreamPoolFailover(t *testing.T) {
	p := NewUpstreamPool([]string{"sk-key-1", "sk-key-2"}, 60)
	tok1, _, _ := p.Next()
	p.MarkExhausted(tok1, "429 rate limited")
	// next should skip key-1 and return key-2
	tok2, _, ok := p.Next()
	if !ok {
		t.Fatal("expected key available after failover")
	}
	if tok2 == tok1 {
		t.Fatalf("failover returned same exhausted key: %s", tok2)
	}
}

func TestUpstreamPoolAllExhausted(t *testing.T) {
	p := NewUpstreamPool([]string{"sk-key-1", "sk-key-2"}, 60)
	tok1, _, _ := p.Next()
	tok2, _, _ := p.Next()
	p.MarkExhausted(tok1, "429")
	p.MarkExhausted(tok2, "429")
	// both exhausted -> Available false
	if p.Available() {
		t.Fatal("expected no keys available when all exhausted")
	}
	// after cooldown passes, key becomes available again
	p.keys[0].CooldownUntil = time.Now().Add(-time.Second)
	p.keys[0].Exhausted = false
	if !p.Available() {
		t.Fatal("expected key available after cooldown")
	}
}

func TestUpstreamPoolTrimToken(t *testing.T) {
	if got := trimToken("  sk-abc123  "); got != "sk-abc123" {
		t.Fatalf("trimToken failed: %q", got)
	}
	if got := trimToken("go:sk-abc123"); got != "sk-abc123" {
		t.Fatalf("trimToken go: prefix failed: %q", got)
	}
	if got := trimToken("zen:sk-abc123"); got != "sk-abc123" {
		t.Fatalf("trimToken zen: prefix failed: %q", got)
	}
}

func TestUpstreamPoolMaskToken(t *testing.T) {
	m := maskToken("sk-abcdefghijkl")
	if len(m) >= len("sk-abcdefghijkl") {
		t.Fatalf("mask should shorten token: %q", m)
	}
	if m != "sk-a...ijkl" {
		t.Fatalf("unexpected mask: %q", m)
	}
}