package main

import (
	"testing"
	"time"
)

func TestRateLimiterAllowsWithinLimit(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{Enabled: true, RequestsPerMinute: 5, TokensPerMinute: 1000})
	for i := 0; i < 5; i++ {
		st := rl.Check("key1", 10)
		if st.Limited {
			t.Fatalf("request %d should be allowed", i+1)
		}
		rl.Record("key1", 10)
	}
	st := rl.Check("key1", 10)
	if !st.Limited {
		t.Fatal("6th request should be limited")
	}
	if st.Requests != 5 || st.RetryAfter <= 0 {
		t.Fatalf("unexpected status: %+v", st)
	}
}

func TestRateLimiterDisabled(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{Enabled: false, RequestsPerMinute: 1})
	for i := 0; i < 10; i++ {
		st := rl.Check("key1", 0)
		if st.Limited {
			t.Fatal("disabled limiter should never limit")
		}
	}
}

func TestRateLimiterExempt(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{Enabled: true, RequestsPerMinute: 1, ExemptKeys: []string{"admin"}})
	for i := 0; i < 10; i++ {
		st := rl.Check("admin", 0)
		if st.Limited {
			t.Fatal("exempt key should never be limited")
		}
	}
}

func TestRateLimiterTokenLimit(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{Enabled: true, TokensPerMinute: 100})
	// 3 requests of 40 tokens each -> 120 > 100 -> 3rd limited
	rl.Record("key1", 40)
	rl.Record("key1", 40)
	st := rl.Check("key1", 40)
	if !st.Limited {
		t.Fatal("token limit should trip")
	}
	if st.Tokens != 80 {
		t.Fatalf("tokens: %d", st.Tokens)
	}
}

func TestRateLimiterWindowExpiry(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{Enabled: true, RequestsPerMinute: 1})
	rl.Record("key1", 0)
	// manually age the window by rewriting the timestamp
	rl.mu.Lock()
	w := rl.windows["key1"]
	w.reqs[0] = time.Now().Add(-2 * time.Minute)
	rl.mu.Unlock()
	st := rl.Check("key1", 0)
	if st.Limited {
		t.Fatal("expired window should allow request")
	}
}

func TestUsageStoreRecordAndSnapshot(t *testing.T) {
	s := newUsageStore(UsageConfig{Enabled: true, CostPer1KIn: 1.0, CostPer1KOut: 2.0})
	s.Record("key1", 1000, 500)
	s.Record("key1", 1000, 500)
	// also test a second key
	s.Record("key2", 100, 100)
	snap := s.Snapshot()
	k1 := snap["key1"]
	if k1.Requests != 2 || k1.PromptTokens != 2000 || k1.CompletionTokens != 1000 {
		t.Fatalf("key1 usage: %+v", k1)
	}
	// cost: 2000/1000*1 + 1000/1000*2 = 2 + 2 = 4
	if k1.CostUSD < 3.99 || k1.CostUSD > 4.01 {
		t.Fatalf("key1 cost: %f", k1.CostUSD)
	}
	if k2 := snap["key2"]; k2.Requests != 1 || k2.TotalTokens != 200 {
		t.Fatalf("key2 usage: %+v", k2)
	}
}

func TestUsageStoreDisabled(t *testing.T) {
	s := newUsageStore(UsageConfig{Enabled: false})
	s.Record("key1", 100, 100)
	if len(s.Snapshot()) != 0 {
		t.Fatal("disabled store should record nothing")
	}
}
