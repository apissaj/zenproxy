package main

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// UsageConfig controls cost tracking persistence.
type UsageConfig struct {
	Enabled       bool   `json:"enabled"`
	PersistPath   string `json:"persist_path"`
	MaxKeys       int    `json:"max_keys"`
	CostPer1KIn   float64 `json:"cost_per_1k_input_tokens"`
	CostPer1KOut  float64 `json:"cost_per_1k_output_tokens"`
}

// KeyUsage is the accumulated usage for a single API key.
type KeyUsage struct {
	Requests        int     `json:"requests"`
	PromptTokens    int64   `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	TotalTokens     int64   `json:"total_tokens"`
	CostUSD         float64 `json:"cost_usd"`
	LastUsed        string  `json:"last_used"`
}

// usageStore persists per-key token/cost usage to a JSON file.
type usageStore struct {
	mu         sync.Mutex
	enabled    bool
	path       string
	maxKeys    int
	costIn     float64
	costOut    float64
	keys       map[string]*KeyUsage
	dirty      bool
}

func newUsageStore(cfg UsageConfig) *usageStore {
	s := &usageStore{
		enabled: cfg.Enabled,
		path:    cfg.PersistPath,
		maxKeys: cfg.MaxKeys,
		costIn:  cfg.CostPer1KIn,
		costOut: cfg.CostPer1KOut,
		keys:    map[string]*KeyUsage{},
	}
	if s.maxKeys <= 0 {
		s.maxKeys = 1000
	}
	if s.enabled && s.path != "" {
		s.load()
	}
	return s
}

func (s *usageStore) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var stored map[string]*KeyUsage
	if err := json.Unmarshal(data, &stored); err != nil {
		return
	}
	for k, v := range stored {
		s.keys[k] = v
	}
}

// Record adds token usage for a key.
func (s *usageStore) Record(key string, promptTokens, completionTokens int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return
	}
	ku := s.keys[key]
	if ku == nil {
		ku = &KeyUsage{}
		s.keys[key] = ku
	}
	ku.Requests++
	ku.PromptTokens += promptTokens
	ku.CompletionTokens += completionTokens
	ku.TotalTokens += promptTokens + completionTokens
	ku.CostUSD += float64(promptTokens)/1000*s.costIn + float64(completionTokens)/1000*s.costOut
	ku.LastUsed = time.Now().UTC().Format(time.RFC3339)
	s.dirty = true
	if len(s.keys) > s.maxKeys {
		// evict oldest by last_used
		var oldestKey string
		var oldest time.Time
		for k, v := range s.keys {
			t, _ := time.Parse(time.RFC3339, v.LastUsed)
			if oldestKey == "" || t.Before(oldest) {
				oldestKey = k
				oldest = t
			}
		}
		delete(s.keys, oldestKey)
	}
}

// Snapshot returns a copy of all key usage (dashboard).
func (s *usageStore) Snapshot() map[string]KeyUsage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]KeyUsage, len(s.keys))
	for k, v := range s.keys {
		out[k] = *v
	}
	return out
}

// Persist writes the store to disk if dirty.
func (s *usageStore) Persist() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled || s.path == "" || !s.dirty {
		return
	}
	data, err := json.MarshalIndent(s.keys, "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(s.path, data, 0o644); err != nil {
		return
	}
	s.dirty = false
}

// usageSaver periodically persists the store.
func usageSaver(s *usageStore) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		s.Persist()
	}
}
