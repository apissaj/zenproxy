package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"sync"
)

// AppConfig is the on-disk configuration shape (config.json).
type AppConfig struct {
	ModelAlias           map[string]string `json:"model_alias"`
	ReasoningEffortMap   map[string]string `json:"reasoning_effort_map"`
	ForceDisableThinking bool              `json:"force_disable_thinking"`
	RateLimit            *RateLimitConfig  `json:"rate_limit"`
	Usage                *UsageConfig      `json:"usage"`
	KeyAuth              *KeyAuthConfig    `json:"key_auth"`
	DashboardAuth        *DashboardAuthConfig `json:"dashboard_auth"`
}

// KeyAuthConfig controls managed API key authentication.
type KeyAuthConfig struct {
	Enabled       bool   `json:"enabled"`
	KeysPath      string `json:"keys_path"`
	AllowPublic   bool   `json:"allow_public"` // allow unauthenticated public tier
}

// DashboardAuthConfig protects /dashboard with a token.
type DashboardAuthConfig struct {
	Enabled bool   `json:"enabled"`
	Token   string `json:"token"`
}

var (
	configMu             sync.RWMutex
	modelAlias           = map[string]string{}
	reasoningEffortMap   = map[string]string{}
	forceDisableThinking bool
	keyAuthAllowPublicFlag bool
	dashboardAuthEnabled bool
	dashboardAuthToken   string
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

// applyRateUsage configures the rate limiter and usage store from config.
// Called once at startup (and could be re-called on SIGHUP).
func applyRateUsage(cfg AppConfig) {
	if cfg.RateLimit != nil {
		if rateLimiter == nil {
			rateLimiter = NewRateLimiter(*cfg.RateLimit)
		} else {
			rateLimiter.Configure(*cfg.RateLimit)
		}
	}
	if cfg.Usage != nil {
		if usageStore_ == nil {
			usageStore_ = newUsageStore(*cfg.Usage)
		} else {
			// re-create with new config (keeps loaded data if same path)
			usageStore_.mu.Lock()
			usageStore_.enabled = cfg.Usage.Enabled
			usageStore_.path = cfg.Usage.PersistPath
			usageStore_.maxKeys = cfg.Usage.MaxKeys
			usageStore_.costIn = cfg.Usage.CostPer1KIn
			usageStore_.costOut = cfg.Usage.CostPer1KOut
			usageStore_.mu.Unlock()
		}
	}
}

// initKeyAuth wires the managed key store from config (server mode).
func initKeyAuth(cfg AppConfig) {
	if cfg.KeyAuth != nil && cfg.KeyAuth.Enabled {
		keyStore = NewKeyStore(cfg.KeyAuth.KeysPath, true)
		keyAuthAllowPublicFlag = cfg.KeyAuth.AllowPublic
		slog.Info("key auth enabled", "keys_path", cfg.KeyAuth.KeysPath, "allow_public", cfg.KeyAuth.AllowPublic)
	}
	if cfg.DashboardAuth != nil && cfg.DashboardAuth.Enabled {
		dashboardAuthEnabled = true
		dashboardAuthToken = cfg.DashboardAuth.Token
		slog.Info("dashboard auth enabled")
	}
}
