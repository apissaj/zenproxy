package main

import (
	"encoding/json"
	"os"
	"sync"
)

// AppConfig is the on-disk configuration shape (config.json).
type AppConfig struct {
	ModelAlias           map[string]string `json:"model_alias"`
	ReasoningEffortMap   map[string]string `json:"reasoning_effort_map"`
	ForceDisableThinking bool              `json:"force_disable_thinking"`
}

var (
	configMu             sync.RWMutex
	modelAlias           = map[string]string{}
	reasoningEffortMap   = map[string]string{}
	forceDisableThinking bool
)

func loadConfig(path string) AppConfig {
	var cfg AppConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg
	}
	return cfg
}

func applyConfig(cfg AppConfig) {
	configMu.Lock()
	defer configMu.Unlock()
	if cfg.ModelAlias != nil {
		modelAlias = cfg.ModelAlias
	}
	if cfg.ReasoningEffortMap != nil {
		reasoningEffortMap = cfg.ReasoningEffortMap
	}
	forceDisableThinking = cfg.ForceDisableThinking
}

func getForceDisableThinking() bool {
	configMu.RLock()
	defer configMu.RUnlock()
	return forceDisableThinking
}

func mapReasoningEffort(effort string) string {
	configMu.RLock()
	defer configMu.RUnlock()
	if mapped, ok := reasoningEffortMap[effort]; ok {
		return mapped
	}
	return effort
}
