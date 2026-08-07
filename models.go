package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// ModelInfo is a single catalog entry.
type ModelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

var (
	modelMu       sync.RWMutex
	modelsCache   []ModelInfo // free Zen catalog
	goModelsCache []ModelInfo // Go subscription catalog
)

func isFreeModel(id string) bool { return strings.HasSuffix(id, "-free") }

// publicFacingID strips the upstream "-free" suffix for client-visible catalogs.
func publicFacingID(id string) string {
	if isFreeModel(id) {
		return strings.TrimSuffix(id, "-free")
	}
	return id
}

func containsID(models []ModelInfo, id string) bool {
	for _, m := range models {
		if m.ID == id {
			return true
		}
	}
	return false
}

func isModelInGoCatalog(id string) bool {
	modelMu.RLock()
	defer modelMu.RUnlock()
	return containsID(goModelsCache, id)
}

func isGoCatalogOnly(id string) bool {
	modelMu.RLock()
	defer modelMu.RUnlock()
	return containsID(goModelsCache, id) && !containsID(modelsCache, id)
}

func modelExists(id string) bool {
	modelMu.RLock()
	defer modelMu.RUnlock()
	return containsID(modelsCache, id) || containsID(goModelsCache, id)
}

func getFreeModelIDs() []string {
	modelMu.RLock()
	defer modelMu.RUnlock()
	ids := make([]string, 0, len(modelsCache))
	for _, m := range modelsCache {
		ids = append(ids, m.ID)
	}
	return ids
}

func fetchCatalog(url string) ([]ModelInfo, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer public")
	req.Header.Set("x-opencode-session", currentSessionID())
	resp, err := upstreamClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp.StatusCode)
	}
	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	out := make([]ModelInfo, 0, len(result.Data))
	for _, m := range result.Data {
		out = append(out, ModelInfo{ID: m.ID, Object: "model", Created: now, OwnedBy: "opencode"})
	}
	return out, nil
}

func refreshCatalogs() {
	free, err := fetchCatalog("https://opencode.ai/zen/v1/models")
	if err != nil {
		slog.Error("free catalog refresh failed", "error", err)
	} else {
		modelMu.Lock()
		modelsCache = free
		modelMu.Unlock()
		slog.Info("free catalog refreshed", "count", len(free))
	}
	goCat, err := fetchCatalog("https://opencode.ai/zen/go/v1/models")
	if err != nil {
		slog.Error("go catalog refresh failed", "error", err)
	} else {
		modelMu.Lock()
		goModelsCache = goCat
		modelMu.Unlock()
		slog.Info("go catalog refreshed", "count", len(goCat))
	}
}

// resolveModel maps a client-visible model name to the upstream ID.
// Priority: explicit alias > "-free" auto-append when the base name is not
// a real model but the "-free" variant exists.
func resolveModel(model string) string {
	m := strings.TrimSpace(model)
	configMu.RLock()
	alias, ok := modelAlias[m]
	configMu.RUnlock()
	if ok {
		return alias
	}
	if m != "" && !isFreeModel(m) {
		freeID := m + "-free"
		if !modelExists(m) && modelExists(freeID) {
			return freeID
		}
	}
	return m
}

// replaceIDsWithAliases rewrites catalog IDs to their client-visible aliases.
func replaceIDsWithAliases(models []ModelInfo) []ModelInfo {
	configMu.RLock()
	aliases := make(map[string]string, len(modelAlias))
	for alias, upstream := range modelAlias {
		aliases[alias] = upstream
	}
	configMu.RUnlock()

	byUpstream := map[string][]string{}
	for alias, upstream := range aliases {
		alias = strings.TrimSpace(alias)
		upstream = strings.TrimSpace(upstream)
		if alias == "" || upstream == "" {
			continue
		}
		byUpstream[upstream] = append(byUpstream[upstream], alias)
	}
	for up := range byUpstream {
		sort.Strings(byUpstream[up])
	}

	result := make([]ModelInfo, 0, len(models))
	seen := map[string]struct{}{}
	for _, model := range models {
		visible := byUpstream[model.ID]
		if len(visible) == 0 {
			visible = []string{publicFacingID(model.ID)}
		}
		for _, vid := range visible {
			if _, dup := seen[vid]; dup {
				continue
			}
			vm := model
			vm.ID = vid
			if vid != model.ID {
				vm.OwnedBy = "alias"
			}
			result = append(result, vm)
			seen[vid] = struct{}{}
		}
	}
	return result
}
