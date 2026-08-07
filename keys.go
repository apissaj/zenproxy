package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// ProxyKey is a managed API key record.
type ProxyKey struct {
	Name        string    `json:"name"`
	Hash        string    `json:"hash"` // SHA-256 of the raw key
	CreatedAt   time.Time `json:"created_at"`
	Revoked     bool      `json:"revoked"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
	ModelAllow  []string  `json:"model_allow,omitempty"` // empty = all models
	BudgetUSD   float64   `json:"budget_usd,omitempty"`  // 0 = unlimited
	RequestsPerMinute int `json:"requests_per_minute,omitempty"` // 0 = global
	TokensPerMinute   int64 `json:"tokens_per_minute,omitempty"` // 0 = global
}

// KeyStore manages proxy keys persisted to a JSON file.
type KeyStore struct {
	mu    sync.RWMutex
	path  string
	keys  map[string]*ProxyKey // by hash
	enabled bool
}

// NewKeyStore loads (or creates) the key store from path.
func NewKeyStore(path string, enabled bool) *KeyStore {
	ks := &KeyStore{path: path, keys: map[string]*ProxyKey{}, enabled: enabled}
	if enabled && path != "" {
		ks.load()
	}
	return ks
}

func (ks *KeyStore) load() {
	data, err := os.ReadFile(ks.path)
	if err != nil {
		return
	}
	var stored map[string]*ProxyKey
	if err := json.Unmarshal(data, &stored); err != nil {
		return
	}
	ks.keys = stored
}

// persist writes the store to disk.
func (ks *KeyStore) persist() error {
	if ks.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(ks.keys, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ks.path, data, 0o600)
}

// Reload re-reads the store from disk (hot-reload after CLI changes).
func (ks *KeyStore) Reload() {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	data, err := os.ReadFile(ks.path)
	if err != nil {
		return
	}
	var stored map[string]*ProxyKey
	if err := json.Unmarshal(data, &stored); err != nil {
		return
	}
	ks.keys = stored
}

// keyReloader periodically reloads the key store from disk so keys
// created/revoked via the CLI take effect without a server restart.
func keyReloader(ks *KeyStore) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		ks.Reload()
	}
}

// generateKey creates a cryptographically random API key.
func generateKey() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "zp_" + hex.EncodeToString(buf), nil
}

func hashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// Create issues a new key and returns the raw key (shown once).
func (ks *KeyStore) Create(name string, modelAllow []string, budgetUSD float64, rpm int, tpm int64) (string, error) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	raw, err := generateKey()
	if err != nil {
		return "", err
	}
	rec := &ProxyKey{
		Name:        name,
		Hash:        hashKey(raw),
		CreatedAt:   time.Now().UTC(),
		ModelAllow:  modelAllow,
		BudgetUSD:   budgetUSD,
		RequestsPerMinute: rpm,
		TokensPerMinute:   tpm,
	}
	ks.keys[rec.Hash] = rec
	if err := ks.persist(); err != nil {
		return "", err
	}
	return raw, nil
}

// Lookup resolves a raw key to its record. Returns nil if unknown/revoked.
func (ks *KeyStore) Lookup(raw string) *ProxyKey {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	rec := ks.keys[hashKey(raw)]
	if rec == nil || rec.Revoked {
		return nil
	}
	return rec
}

// Revoke marks a key revoked by name or hash prefix.
func (ks *KeyStore) Revoke(id string) (bool, error) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	for _, rec := range ks.keys {
		if rec.Name == id || strings.HasPrefix(rec.Hash, id) {
			rec.Revoked = true
			now := time.Now().UTC()
			rec.RevokedAt = &now
			if err := ks.persist(); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	return false, nil
}

// List returns all keys sorted by name (dashboard + CLI).
func (ks *KeyStore) List() []*ProxyKey {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	out := make([]*ProxyKey, 0, len(ks.keys))
	for _, rec := range ks.keys {
		cp := *rec
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ActiveBudgetUSD returns the current spend for a key (from usage store).
func activeSpendUSD(rec *ProxyKey) float64 {
	if usageStore_ == nil || rec == nil {
		return 0
	}
	if u, ok := usageStore_.Snapshot()[rec.Hash]; ok {
		return u.CostUSD
	}
	return 0
}

// CLI: zenproxy key create/list/revoke
func keyCLI(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: zenproxy key create <name> [--allow model1,model2] [--budget 5.0] [--rpm 60] [--tpm 100000]")
		fmt.Fprintln(os.Stderr, "       zenproxy key list")
		fmt.Fprintln(os.Stderr, "       zenproxy key revoke <name|hash-prefix>")
		return 2
	}
	switch args[0] {
	case "create":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: zenproxy key create <name> [--allow m1,m2] [--budget 5.0] [--rpm 60] [--tpm 100000]")
			return 2
		}
		name := args[1]
		var allow []string
		var budget float64
		var rpm int
		var tpm int64
		for i := 2; i < len(args); i++ {
			switch args[i] {
			case "--allow":
				if i+1 < len(args) {
					for _, m := range strings.Split(args[i+1], ",") {
						m = strings.TrimSpace(m)
						if m != "" {
							allow = append(allow, m)
						}
					}
					i++
				}
			case "--budget":
				if i+1 < len(args) {
					fmt.Sscanf(args[i+1], "%f", &budget)
					i++
				}
			case "--rpm":
				if i+1 < len(args) {
					fmt.Sscanf(args[i+1], "%d", &rpm)
					i++
				}
			case "--tpm":
				if i+1 < len(args) {
					fmt.Sscanf(args[i+1], "%d", &tpm)
					i++
				}
			}
		}
		raw, err := keyStore.Create(name, allow, budget, rpm, tpm)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		fmt.Printf("created key %q\n", name)
		fmt.Printf("  key: %s\n", raw)
		fmt.Println("  (store only the hash — the raw key is shown once)")
		return 0
	case "list":
		ks := keyStore.List()
		if len(ks) == 0 {
			fmt.Println("no keys")
			return 0
		}
		fmt.Printf("%-20s %-12s %-8s %-10s %s\n", "NAME", "STATUS", "BUDGET", "SPEND", "MODELS")
		for _, rec := range ks {
			status := "active"
			if rec.Revoked {
				status = "revoked"
			}
			models := "all"
			if len(rec.ModelAllow) > 0 {
				models = strings.Join(rec.ModelAllow, ",")
			}
			spend := activeSpendUSD(rec)
			fmt.Printf("%-20s %-12s %-8.2f %-10.4f %s\n", rec.Name, status, rec.BudgetUSD, spend, models)
		}
		return 0
	case "revoke":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: zenproxy key revoke <name|hash-prefix>")
			return 2
		}
		ok, err := keyStore.Revoke(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		if !ok {
			fmt.Fprintln(os.Stderr, "no key found:", args[1])
			return 1
		}
		fmt.Printf("revoked %q\n", args[1])
		return 0
	default:
		fmt.Fprintln(os.Stderr, "unknown subcommand:", args[0])
		return 2
	}
}
