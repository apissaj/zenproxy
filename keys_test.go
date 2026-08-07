package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKeyStoreCreateLookup(t *testing.T) {
	dir := t.TempDir()
	ks := NewKeyStore(filepath.Join(dir, "keys.json"), true)
	raw, err := ks.Create("alice", []string{"mimo-v2.5"}, 5.0, 10, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 20 || raw[:3] != "zp_" {
		t.Fatalf("unexpected raw key: %q", raw)
	}
	rec := ks.Lookup(raw)
	if rec == nil {
		t.Fatal("lookup should find key")
	}
	if rec.Name != "alice" || len(rec.ModelAllow) != 1 || rec.BudgetUSD != 5.0 {
		t.Fatalf("unexpected record: %+v", rec)
	}
	// persisted to disk
	if _, err := os.Stat(ks.path); err != nil {
		t.Fatalf("keys.json not persisted: %v", err)
	}
}

func TestKeyStoreUnknownAndRevoked(t *testing.T) {
	dir := t.TempDir()
	ks := NewKeyStore(filepath.Join(dir, "keys.json"), true)
	raw, _ := ks.Create("bob", nil, 0, 0, 0)
	if ks.Lookup("zp_unknown") != nil {
		t.Fatal("unknown key should be nil")
	}
	ok, _ := ks.Revoke("bob")
	if !ok {
		t.Fatal("revoke should succeed")
	}
	if ks.Lookup(raw) != nil {
		t.Fatal("revoked key should be nil")
	}
	// double revoke still finds the record (idempotent, no error)
	if ok, _ := ks.Revoke("bob"); !ok {
		t.Fatal("double revoke should still find the record")
	}
}

func TestKeyStoreReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.json")
	ks1 := NewKeyStore(path, true)
	raw, _ := ks1.Create("carol", nil, 0, 0, 0)

	// simulate a separate process (CLI) creating a key
	ks2 := NewKeyStore(path, true)
	raw2, _ := ks2.Create("dave", nil, 0, 0, 0)

	// ks1 does not know dave yet
	if ks1.Lookup(raw2) != nil {
		t.Fatal("ks1 should not know dave before reload")
	}
	ks1.Reload()
	if ks1.Lookup(raw2) == nil {
		t.Fatal("ks1 should know dave after reload")
	}
	if ks1.Lookup(raw) == nil {
		t.Fatal("ks1 should still know carol after reload")
	}
}

func TestKeyStoreHashNotPlaintext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.json")
	ks := NewKeyStore(path, true)
	raw, _ := ks.Create("eve", nil, 0, 0, 0)
	data, _ := os.ReadFile(path)
	if contains(string(data), raw) {
		t.Fatal("raw key must not be stored in plaintext")
	}
	// but the hash must be present
	if !contains(string(data), hashKey(raw)) {
		t.Fatal("hash should be stored")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestKeyStoreDisabled(t *testing.T) {
	ks := NewKeyStore("", false)
	if ks.enabled {
		t.Fatal("store should be disabled")
	}
	if _, err := ks.Create("x", nil, 0, 0, 0); err != nil {
		t.Fatal("create on disabled store should still work (in-memory)")
	}
}
