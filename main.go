package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"
)

const version = "0.1.0"

// keyStore is the managed API key store (nil if key_auth disabled).
var keyStore *KeyStore

// upstreamPool is the multi-key rotation pool (nil if disabled).
var upstreamPool *UpstreamPool

// proxyPool is the HTTP/SOCKS proxy rotation pool (nil if disabled).
// Used to bypass per-IP free-tier limits by routing requests through
// different proxy endpoints.
var proxyPool *ProxyPool

func main() {
	port := flag.String("port", "8000", "server port")
	configPath := flag.String("config", "config.json", "config file path")
	logLevel := flag.String("log-level", "info", "log level: debug/info/warn/error")
	versionFlag := flag.Bool("version", false, "show version")
	flag.Parse()

	if *versionFlag {
		fmt.Println("zenproxy", version)
		return
	}

	// CLI subcommand: zenproxy key create/list/revoke
	if flag.NArg() > 0 && flag.Arg(0) == "key" {
		cfg := loadConfig(*configPath)
		applyConfig(cfg)
		applyRateUsage(cfg)
		if cfg.KeyAuth != nil && cfg.KeyAuth.Enabled {
			keyStore = NewKeyStore(cfg.KeyAuth.KeysPath, true)
		}
		if keyStore == nil {
			fmt.Fprintln(os.Stderr, "key management disabled (key_auth.enabled=false in config)")
			os.Exit(2)
		}
		os.Exit(keyCLI(flag.Args()[1:]))
	}

	setupLogger(*logLevel)

	cfg := loadConfig(*configPath)
	applyConfig(cfg)
	applyRateUsage(cfg)
	initKeyAuth(cfg)
	initUpstreamPool(cfg)
	initProxyPool(cfg)
	slog.Info("config loaded", "path", *configPath)

	initOCSession()
	refreshCatalogs()
	// keep the catalog fresh
	go func() {
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			refreshCatalogs()
		}
	}()
	// persist usage periodically
	if usageStore_ != nil {
		go usageSaver(usageStore_)
	}
	// hot-reload managed keys from disk (CLI changes take effect live)
	if keyStore != nil {
		go keyReloader(keyStore)
	}

	mux := http.NewServeMux()
	mux.Handle("/health", withRequestID(http.HandlerFunc(healthHandler)))
	mux.Handle("/v1/models", withRequestID(http.HandlerFunc(modelsHandler)))
	mux.Handle("/v1/chat/completions", withRequestID(withKeyAuth(withKeyLimits(withRateLimit(http.HandlerFunc(chatCompletionsHandler))))))
	mux.Handle("/v1/responses", withRequestID(withKeyAuth(withKeyLimits(withRateLimit(http.HandlerFunc(responsesHandler))))))
	mux.Handle("/v1/messages", withRequestID(withKeyAuth(withKeyLimits(withRateLimit(http.HandlerFunc(claudeMessagesHandler))))))
	mux.Handle("/dashboard", withRequestID(withDashboardAuth(http.HandlerFunc(dashboardHandler))))
	mux.Handle("/dashboard/data", withRequestID(withDashboardAuth(http.HandlerFunc(dashboardDataHandler))))

	addr := ":" + *port
	slog.Info("server starting", "port", *port, "version", version)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// logSlogInfo delegates to slog.Info so handlers in other files can log
// request plans without importing slog directly everywhere.
func logSlogInfo(msg string, args ...any) {
	slog.Info(msg, args...)
}

func modelsHandler(w http.ResponseWriter, r *http.Request) {
	modelMu.RLock()
	models := make([]ModelInfo, len(modelsCache))
	copy(models, modelsCache)
	modelMu.RUnlock()
	if len(models) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "model catalog not loaded yet"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data":   replaceIDsWithAliases(models),
	})
}

func chatCompletionsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	auth := extractUpstreamAuth(r)

	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	var req OpenAIRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	modelIn := req.Model
	req.Model = resolveModel(req.Model)
	if req.Model == "" {
		ids := getFreeModelIDs()
		if len(ids) > 0 {
			req.Model = ids[0]
		} else {
			req.Model = "deepseek-v4-flash-free"
		}
	}

	req.Messages = fixToolCallGaps(req.Messages)
	keepReasoning := wantsReasoning(&req)
	_ = keepReasoning

	slog.Info("request_plan",
		"request_id", reqID(r.Context()), "protocol", "chat",
		"model_in", modelIn, "model_resolved", req.Model,
		"auth_mode", authModeString(auth.Mode), "stream", req.Stream)

	upstreamBody := buildUpstreamBody(&req)

	if req.Stream {
		relayStream(w, r, upstreamBody, req.Model, auth)
		return
	}
	relayNonStream(w, r, upstreamBody, req.Model, auth)
}

func relayNonStream(w http.ResponseWriter, r *http.Request, body []byte, modelID string, auth UpstreamAuth) {
	ctx := r.Context()
	result, err := callUpstreamRelay(ctx, body, modelID)
	if err != nil {
		slog.Error("relay_error", "request_id", reqID(ctx), "model", modelID, "error", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if result.status != http.StatusOK {
		w.WriteHeader(result.status)
	}
	w.Write(result.body)
	recordUsageFromBody(apiKeyForRequest(r), result.body)
}

func relayStream(w http.ResponseWriter, r *http.Request, body []byte, modelID string, auth UpstreamAuth) {
	ctx := r.Context()
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	slog.Info("relay_stream_start", "request_id", reqID(ctx), "model", modelID)

	err := relaySSE(ctx, w, func() { flusher.Flush() }, body, modelID)
	if err != nil {
		slog.Error("relay_stream_error", "request_id", reqID(ctx), "model", modelID, "error", err)
		// if nothing was written yet, send proper error; otherwise client already got partial SSE
		payload := map[string]any{
			"error": map[string]any{"message": err.Error(), "type": "relay_error"},
		}
		raw, _ := json.Marshal(payload)
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", raw)
		flusher.Flush()
	}
}
